package sqlglot

import "strings"

// tableCall reads a FROM-clause function. Two Oracle JSON_TABLE shapes are
// rewritten into the parenthesised column list the ordinary reader accepts,
// and a NESTED PATH column is grafted back on afterwards.
func (p *parser) tableCall() (*Expression, error) {
	nested, err := p.prepareOracleJSONTable()
	if err != nil {
		return nil, err
	}
	node, err := p.parseFunction()
	if err != nil || nested == nil {
		return node, err
	}
	schema, _ := node.Args["schema"].(*Expression)
	if schema == nil {
		return nil, p.unsupported("JSON_TABLE nested column without COLUMNS")
	}
	schema.Append("expressions", nested)
	return node, nil
}

// prepareOracleJSONTable lifts a NESTED PATH column out of the token stream
// and, when COLUMNS has no parentheses, inserts them. Anything else is left
// untouched for the ordinary reader.
func (p *parser) prepareOracleJSONTable() (*Expression, error) {
	if p.dialect != "oracle" || !p.atWords("JSON_TABLE") {
		return nil, nil
	}
	nestedAt := -1
	for i := p.index; i < len(p.tokens); i++ {
		if strings.EqualFold(p.tokens[i].Text, "NESTED") {
			nestedAt = i
			break
		}
	}
	if nestedAt < 0 {
		p.wrapBareJSONColumns()
		return nil, nil
	}
	nested, err := p.cutNestedJSONColumn(nestedAt)
	if err != nil {
		return nil, err
	}
	p.wrapBareJSONColumns()
	return nested, nil
}

func (p *parser) wrapBareJSONColumns() {
	columnsAt := -1
	for i := p.index; i < len(p.tokens); i++ {
		if strings.EqualFold(p.tokens[i].Text, "COLUMNS") {
			columnsAt = i
			break
		}
	}
	if columnsAt < 0 || columnsAt+1 >= len(p.tokens) || p.tokens[columnsAt+1].Type == TokL_PAREN {
		return
	}
	closer := -1
	for i := columnsAt + 1; i < len(p.tokens); i++ {
		if p.tokens[i].Type == TokR_PAREN {
			closer = i
			break
		}
	}
	if closer < 0 {
		return
	}
	opened := make([]Token, 0, len(p.tokens)+1)
	opened = append(opened, p.tokens[:columnsAt+1]...)
	opened = append(opened, Token{Type: TokL_PAREN, Text: "("})
	opened = append(opened, p.tokens[columnsAt+1:]...)
	shut := make([]Token, 0, len(opened)+1)
	shut = append(shut, opened[:closer+1]...)
	shut = append(shut, Token{Type: TokR_PAREN, Text: ")"})
	shut = append(shut, opened[closer+1:]...)
	p.tokens = shut
}

func (p *parser) cutNestedJSONColumn(nestedAt int) (*Expression, error) {
	if nestedAt+3 >= len(p.tokens) || !strings.EqualFold(p.tokens[nestedAt+1].Text, "PATH") || p.tokens[nestedAt+2].Type != TokSTRING {
		return nil, p.unsupported("JSON_TABLE NESTED column")
	}
	pathTok := p.tokens[nestedAt+2]
	innerColumns := -1
	for i := nestedAt + 3; i < len(p.tokens); i++ {
		if strings.EqualFold(p.tokens[i].Text, "COLUMNS") {
			innerColumns = i
			break
		}
	}
	if innerColumns < 0 || innerColumns+1 >= len(p.tokens) || p.tokens[innerColumns+1].Type != TokL_PAREN {
		return nil, p.unsupported("JSON_TABLE NESTED column")
	}
	closeAt := -1
	for i := innerColumns + 2; i < len(p.tokens); i++ {
		if p.tokens[i].Type == TokR_PAREN {
			closeAt = i
			break
		}
	}
	if closeAt < 0 {
		return nil, p.unsupported("JSON_TABLE NESTED column")
	}
	inner := append([]Token(nil), p.tokens[innerColumns+2:closeAt]...)
	start := nestedAt
	if start > 0 && p.tokens[start-1].Type == TokCOMMA {
		start--
	}
	kept := append([]Token{}, p.tokens[:start]...)
	p.tokens = append(kept, p.tokens[closeAt+1:]...)
	pathLit, err := p.withTokens([]Token{pathTok}, func() (*Expression, error) {
		return p.tryParseStringLiteral(), nil
	})
	if err != nil || pathLit == nil {
		return nil, p.unsupported("JSON_TABLE PATH without a string")
	}
	col, err := p.withTokens(inner, func() (*Expression, error) {
		return p.parseJSONColumnDef()
	})
	if err != nil {
		return nil, err
	}
	node := New("JSONColumnDef")
	node.Set("path", pathLit)
	node.Set("nested_schema", New("JSONSchema", Arg{"expressions", []*Expression{col}}))
	node.Set("format_json", false)
	return node, nil
}

func (p *parser) withTokens(tokens []Token, read func() (*Expression, error)) (*Expression, error) {
	saved, at, from := p.tokens, p.index, p.commentsFrom
	p.tokens = tokens
	p.index = 0
	p.commentsFrom = 0
	node, err := read()
	p.tokens, p.index, p.commentsFrom = saved, at, from
	return node, err
}

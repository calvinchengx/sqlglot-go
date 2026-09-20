package sqlglot

import "strings"

// JSON_QUERY and JSON_VALUE with the SQL:2016 clauses Trino and MySQL read:
// WITH/WITHOUT [CONDITIONAL|UNCONDITIONAL] [ARRAY] WRAPPER, KEEP/OMIT QUOTES
// [ON SCALAR STRING], RETURNING <type>, and `<action> ON ERROR|EMPTY`.

// jsonQueryWrapperOptions is the reference's JSON_QUERY_OPTIONS, spelled out
// (including its own `WRAPPED`) for both WITH and WITHOUT.
var jsonQueryWrapperOptions = [][]string{
	{"WRAPPER"},
	{"ARRAY", "WRAPPER"},
	{"CONDITIONAL", "WRAPPER"},
	{"CONDITIONAL", "ARRAY", "WRAPPED"},
	{"UNCONDITIONAL", "WRAPPER"},
	{"UNCONDITIONAL", "ARRAY", "WRAPPER"},
}

func (p *parser) parseJSONQueryOption() *Expression {
	c := p.curr()
	if c == nil {
		return nil
	}
	word := strings.ToUpper(c.Text)
	if word != "WITH" && word != "WITHOUT" {
		return nil
	}
	start := p.index
	p.advance()
	for _, keywords := range jsonQueryWrapperOptions {
		if p.atWords(keywords...) {
			for range keywords {
				p.advance()
			}
			return New("Var", Arg{"this", word + " " + strings.Join(keywords, " ")})
		}
	}
	p.index = start
	return nil
}

func (p *parser) parseJSONQueryQuote() *Expression {
	if !p.atWords("KEEP", "QUOTES") && !p.atWords("OMIT", "QUOTES") {
		return nil
	}
	option := strings.ToUpper(p.curr().Text)
	p.advance()
	p.advance()
	scalar := false
	if p.atWords("ON", "SCALAR", "STRING") {
		p.advance()
		p.advance()
		p.advance()
		scalar = true
	}
	return New("JSONExtractQuote", Arg{"option", option}, Arg{"scalar", scalar})
}

// parseOnHandling reads `<VALUE> ON <on>` (kept as the string it spells) or
// `DEFAULT <expr> ON <on>` (kept as the expression).
func (p *parser) parseOnHandling(on string) (any, error) {
	for _, value := range []string{"ERROR", "NULL", "TRUE", "FALSE", "EMPTY"} {
		if p.atWords(value, "ON", on) {
			p.advance()
			p.advance()
			p.advance()
			return value + " ON " + on, nil
		}
	}
	if p.at(TokDEFAULT) {
		start := p.index
		p.advance()
		value, err := p.parseBitwise()
		if err != nil {
			return nil, err
		}
		if p.atWords("ON", on) {
			p.advance()
			p.advance()
			return value, nil
		}
		p.index = start
	}
	return nil, nil
}

func (p *parser) parseOnCondition() (*Expression, error) {
	empty, err := p.parseOnHandling("EMPTY")
	if err != nil {
		return nil, err
	}
	errorHandling, err := p.parseOnHandling("ERROR")
	if err != nil {
		return nil, err
	}
	null, err := p.parseOnHandling("NULL")
	if err != nil {
		return nil, err
	}
	if empty == nil && errorHandling == nil && null == nil {
		return nil, nil
	}
	return New("OnCondition", Arg{"empty", empty}, Arg{"error", errorHandling}, Arg{"null", null}), nil
}

// jsonPathArgument is the dialect's to_json_path: a string literal that
// reads as a path becomes one; a `lax`/`strict` path the reference cannot
// read stays the literal it was.
func (p *parser) jsonPathArgument(path *Expression) (*Expression, error) {
	if path == nil || path.Class != "Literal" {
		return path, nil
	}
	text, _ := path.Args["this"].(string)
	if !isStringLiteral(path) {
		text = "[" + text + "]"
	}
	folded, err := parseJSONPath(text, false)
	if err == nil {
		return folded, nil
	}
	if isStringLiteral(path) && jsonPathKeptAsLiteral(text) {
		return path, nil
	}
	return nil, p.unsupported("a JSON path this port cannot read")
}

func (p *parser) parseJSONQuery() (*Expression, error) {
	p.advance()
	p.advance()
	this, err := p.parseBitwise()
	if err != nil {
		return nil, err
	}
	node := New("JSONExtract", Arg{"this", this})
	if p.match(TokCOMMA) {
		path, err := p.parseBitwise()
		if err != nil {
			return nil, err
		}
		node.Set("expression", path)
	} else {
		node.Set("expression", false)
	}
	if option := p.parseJSONQueryOption(); option != nil {
		node.Set("option", option)
	}
	node.Set("json_query", true)
	if quote := p.parseJSONQueryQuote(); quote != nil {
		node.Set("quote", quote)
	}
	on, err := p.parseOnCondition()
	if err != nil {
		return nil, err
	}
	if on != nil {
		node.Set("on_condition", on)
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed JSON_QUERY")
	}
	return node, nil
}

func (p *parser) parseJSONValue() (*Expression, error) {
	p.advance()
	p.advance()
	this, err := p.parseBitwise()
	if err != nil {
		return nil, err
	}
	p.match(TokCOMMA)
	path, err := p.parseBitwise()
	if err != nil {
		return nil, err
	}
	path, err = p.jsonPathArgument(path)
	if err != nil {
		return nil, err
	}
	var returning any = false
	if p.match(TokRETURNING) {
		// The reference reads a column here -- the type's name is one, and
		// so is a call spelled like a sized type -- and reads a typed
		// literal as a cast, which this port leaves alone.
		if c := p.curr(); c == nil || c.Type == TokSTRING {
			return nil, p.unsupported("RETURNING a typed literal")
		}
		var column *Expression
		var err error
		if n := p.next(); n != nil && n.Type == TokL_PAREN {
			column, err = p.parseFunction()
		} else {
			column, err = p.parseColumn()
		}
		if err != nil {
			return nil, err
		}
		returning = column
	}
	on, err := p.parseOnCondition()
	if err != nil {
		return nil, err
	}
	node := New("JSONValue", Arg{"this", this}, Arg{"path", path}, Arg{"returning", returning})
	if on != nil {
		node.Set("on_condition", on)
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed JSON_VALUE")
	}
	return node, nil
}

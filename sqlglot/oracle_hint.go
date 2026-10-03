package sqlglot

import "strings"

// Oracle's hint is a list of names and calls whose arguments are bare
// words separated by spaces. A comma, a dot, a keyword or a nested
// parenthesis means the reference keeps the whole comment as one string.
func (p *parser) parseOracleHint(text string) (*Expression, error) {
	body := strings.TrimSpace(text)
	body = strings.TrimPrefix(body, "/*+")
	body = strings.TrimSuffix(body, "*/")
	body = strings.TrimSpace(body)
	if body == "" {
		return nil, p.unsupported("a hint that says nothing")
	}
	tk, err := NewTokenizer(p.dialect)
	if err != nil {
		return nil, err
	}
	toks, err := tk.Tokenize(body)
	if err != nil {
		return New("Hint", Arg{"expressions", []string{body}}), nil
	}
	inner := &parser{tokens: toks, cfg: p.cfg, tables: p.tables, dialect: p.dialect}
	items := inner.oracleHintItems()
	if items == nil {
		return New("Hint", Arg{"expressions", []string{body}}), nil
	}
	return New("Hint", Arg{"expressions", items}), nil
}

func (p *parser) oracleHintItems() []*Expression {
	var items []*Expression
	for {
		item := p.oracleHintItem()
		if item == nil {
			return nil
		}
		items = append(items, item)
		switch {
		case p.curr() == nil:
			return items
		case p.match(TokCOMMA) && p.curr() == nil:
			return nil
		}
	}
}

func (p *parser) oracleHintItem() *Expression {
	c := p.curr()
	if c != nil && c.Type != TokL_PAREN && c.Type != TokR_PAREN && c.Type != TokCOMMA &&
		p.next() != nil && p.next().Type == TokL_PAREN {
		name := c.Text
		p.advance()
		p.advance()
		return p.oracleHintCall(name)
	}
	name := p.oracleHintWord()
	if name == "" {
		return nil
	}
	p.advance()
	return New("Var", Arg{"this", name})
}

func (p *parser) oracleHintCall(name string) *Expression {
	var args []*Expression
	for {
		word := p.oracleHintWord()
		if word == "" {
			break
		}
		args = append(args, New("Var", Arg{"this", word}))
		p.advance()
	}
	if !p.match(TokR_PAREN) {
		return nil
	}
	return New("Anonymous", Arg{"this", name}, Arg{"expressions", args})
}

func (p *parser) oracleHintWord() string {
	c := p.curr()
	if c == nil || c.Type != TokVAR {
		return ""
	}
	return c.Text
}

func (g *generator) writeOracleHint(e *Expression) string {
	return "/*+ " + g.oracleHintText(e.Args["expressions"]) + " */"
}

func (g *generator) oracleHintText(value any) string {
	switch items := value.(type) {
	case []string:
		return strings.Join(items, " ")
	case []*Expression:
		words := make([]string, len(items))
		for i, item := range items {
			words[i] = g.oracleHintCall(item)
		}
		return strings.Join(words, " ")
	default:
		return ""
	}
}

func (g *generator) oracleHintCall(e *Expression) string {
	if e == nil || e.Class != "Anonymous" {
		return g.node(e)
	}
	name, _ := e.Args["this"].(string)
	args, _ := e.Args["expressions"].([]*Expression)
	words := make([]string, len(args))
	for i, arg := range args {
		words[i] = g.node(arg)
	}
	return name + "(" + strings.Join(words, " ") + ")"
}

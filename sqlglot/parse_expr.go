package sqlglot

import "strings"

// The expression grammar: precedence climbing, in the reference's shapes.
//
// Everything this file does not recognise is refused, not guessed. That is the
// rule the whole port runs on -- a construct parsed into a plausible-looking
// wrong tree is worse than one refused, because the guard above would then
// reason about something the engine will not execute.

// binaryClass maps an operator token to the reference's node class, for the
// levels where the mapping is one-to-one.
var (
	comparisonOps = map[TokenType]string{
		TokEQ:  "EQ",
		TokNEQ: "NEQ",
		TokGT:  "GT",
		TokGTE: "GTE",
		TokLT:  "LT",
		TokLTE: "LTE",
	}
	additiveOps = map[TokenType]string{
		TokPLUS: "Add",
		TokDASH: "Sub",
	}
	multiplicativeOps = map[TokenType]string{
		TokSTAR:  "Mul",
		TokSLASH: "Div",
		TokMOD:   "Mod",
	}
)

func (p *parser) parseExpression() (*Expression, error) { return p.parseOr() }

func (p *parser) parseOr() (*Expression, error) {
	this, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.match(TokOR) {
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		this = New("Or", Arg{"this", this}, Arg{"expression", right})
	}
	return this, nil
}

func (p *parser) parseAnd() (*Expression, error) {
	this, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	for p.match(TokAND) {
		right, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		this = New("And", Arg{"this", this}, Arg{"expression", right})
	}
	return this, nil
}

func (p *parser) parseNot() (*Expression, error) {
	if p.match(TokNOT) {
		this, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		return New("Not", Arg{"this", this}), nil
	}
	return p.parseComparison()
}

func (p *parser) parseComparison() (*Expression, error) {
	this, err := p.parseAdditive()
	if err != nil {
		return nil, err
	}
	for {
		c := p.curr()
		if c == nil {
			return this, nil
		}
		class, ok := comparisonOps[c.Type]
		if !ok {
			return this, nil
		}
		p.advance()
		right, err := p.parseAdditive()
		if err != nil {
			return nil, err
		}
		this = New(class, Arg{"this", this}, Arg{"expression", right})
	}
}

func (p *parser) parseAdditive() (*Expression, error) {
	return p.parseBinary(additiveOps, p.parseMultiplicative)
}

func (p *parser) parseMultiplicative() (*Expression, error) {
	return p.parseBinary(multiplicativeOps, p.parseUnary)
}

func (p *parser) parseBinary(ops map[TokenType]string, next func() (*Expression, error)) (*Expression, error) {
	this, err := next()
	if err != nil {
		return nil, err
	}
	for {
		c := p.curr()
		if c == nil {
			return this, nil
		}
		class, ok := ops[c.Type]
		if !ok {
			return this, nil
		}
		p.advance()
		right, err := next()
		if err != nil {
			return nil, err
		}
		if class == "Div" {
			// Div carries two more args, and the reference sets them false
			// rather than leaving them absent -- so they dump, and a port that
			// omitted them would mismatch on every division.
			this = New(class, Arg{"this", this}, Arg{"expression", right},
				Arg{"typed", false}, Arg{"safe", false})
			continue
		}
		this = New(class, Arg{"this", this}, Arg{"expression", right})
	}
}

func (p *parser) parseUnary() (*Expression, error) {
	if p.match(TokDASH) {
		this, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return New("Neg", Arg{"this", this}), nil
	}
	return p.parsePrimary()
}

func (p *parser) parsePrimary() (*Expression, error) {
	c := p.curr()
	if c == nil {
		return nil, p.unsupported("expression")
	}

	switch c.Type {
	case TokNUMBER:
		p.advance()
		return New("Literal", Arg{"this", c.Text}, Arg{"is_string", false}), nil
	case TokSTRING:
		p.advance()
		return New("Literal", Arg{"this", c.Text}, Arg{"is_string", true}), nil
	case TokTRUE, TokFALSE:
		p.advance()
		return New("Boolean", Arg{"this", c.Type == TokTRUE}), nil
	case TokNULL:
		p.advance()
		return New("Null"), nil
	case TokSTAR:
		p.advance()
		return newStar(), nil
	case TokL_PAREN:
		p.advance()
		inner, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		if !p.match(TokR_PAREN) {
			return nil, p.unsupported("unclosed parenthesis")
		}
		return New("Paren", Arg{"this", inner}), nil
	case TokVAR, TokIDENTIFIER:
		return p.parseColumn()
	}
	return nil, p.unsupported("expression")
}

// newStar builds a bare `*`. The reference constructs it with four modifier
// args, all empty; they dump as nothing but are kept so the shape is the
// reference's rather than a lookalike.
func newStar() *Expression {
	return New("Star", Arg{"ilike", nil}, Arg{"except_", nil}, Arg{"replace", nil}, Arg{"rename", nil})
}

// parseColumn reads a possibly-qualified column reference: name, table.name,
// db.table.name, catalog.db.table.name, and the `t.*` form.
func (p *parser) parseColumn() (*Expression, error) {
	first, err := p.parseIdentifier()
	if err != nil {
		return nil, err
	}

	// A function call is a different construct; refuse rather than mis-read it.
	if p.at(TokL_PAREN) {
		return nil, p.unsupported("function call")
	}

	parts := []*Expression{first}
	star := false
	for p.match(TokDOT) {
		if p.match(TokSTAR) {
			star = true
			break
		}
		id, err := p.parseIdentifier()
		if err != nil {
			return nil, err
		}
		if p.at(TokL_PAREN) {
			return nil, p.unsupported("qualified function call")
		}
		parts = append(parts, id)
	}

	if star {
		// t.* keeps all four qualifier slots, as the reference builds it.
		col := New("Column", Arg{"this", newStar()})
		names := []string{"table", "db", "catalog"}
		for i := len(parts) - 1; i >= 0; i-- {
			slot := len(parts) - 1 - i
			if slot >= len(names) {
				return nil, p.unsupported("over-qualified column")
			}
			col.Set(names[slot], parts[i])
		}
		for _, n := range names {
			if _, ok := col.Args[n]; !ok {
				col.Set(n, nil)
			}
		}
		return col, nil
	}

	// The last part is the column; the ones before it qualify it, nearest first.
	col := New("Column", Arg{"this", parts[len(parts)-1]})
	names := []string{"table", "db", "catalog"}
	for i := len(parts) - 2; i >= 0; i-- {
		slot := len(parts) - 2 - i
		if slot >= len(names) {
			return nil, p.unsupported("over-qualified column")
		}
		col.Set(names[slot], parts[i])
	}
	return col, nil
}

func (p *parser) parseIdentifier() (*Expression, error) {
	c := p.curr()
	if c == nil {
		return nil, p.unsupported("identifier")
	}
	// T-SQL strips a leading # from some identifiers and not others -- the
	// temp-table rule. Refuse rather than reproduce half of it.
	if p.dialect == "tsql" && strings.Contains(c.Text, "#") {
		return nil, p.unsupported("T-SQL temp-table identifier")
	}
	switch c.Type {
	case TokVAR:
		p.advance()
		return New("Identifier", Arg{"this", c.Text}, Arg{"quoted", false}), nil
	case TokIDENTIFIER:
		p.advance()
		return New("Identifier", Arg{"this", c.Text}, Arg{"quoted", true}), nil
	}
	return nil, p.unsupported("identifier")
}

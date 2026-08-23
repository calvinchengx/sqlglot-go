package sqlglot

import "strings"

// FROM and its table references.

// parseFrom is entered with the FROM token current; the caller checked.
func (p *parser) parseFrom() (*Expression, error) {
	p.advance()
	table, err := p.parseTable()
	if err != nil {
		return nil, err
	}
	return New("From", Arg{"this", table}), nil
}

// parseJoins reads every join that follows the FROM clause, comma joins
// included. A comma join is a join: reading `FROM a, b` as `FROM a` is exactly
// the bypass that made this port necessary, so the comma is handled here
// rather than left to be mistaken for the end of the clause.
func (p *parser) parseJoins() ([]*Expression, error) {
	var joins []*Expression
	for {
		join, err := p.parseJoin()
		if err != nil {
			return nil, err
		}
		if join == nil {
			return joins, nil
		}
		joins = append(joins, join)
	}
}

func (p *parser) parseJoin() (*Expression, error) {
	if p.match(TokCOMMA) {
		table, err := p.parseTable()
		if err != nil {
			return nil, err
		}
		join := New("Join", Arg{"this", table})
		if p.tables.JoinsHaveEqualPrecedence {
			join.Set("kind", "CROSS")
		}
		return join, nil
	}

	c := p.curr()
	if c == nil {
		return nil, nil
	}
	if _, isMethod := p.tables.JoinMethods[c.Type]; isMethod {
		return nil, p.unsupported("join method " + c.Text)
	}

	var side, kind *Token
	if _, ok := p.tables.JoinSides[c.Type]; ok {
		side = c
		p.advance()
	}
	if c2 := p.curr(); c2 != nil {
		if _, ok := p.tables.JoinKinds[c2.Type]; ok {
			kind = c2
			p.advance()
		}
	}

	// CROSS APPLY and OUTER APPLY look like a join with a kind but are a
	// different node, and one the guard has been bypassed through before.
	if p.at(TokAPPLY) {
		return nil, p.unsupported("APPLY")
	}
	if !p.match(TokJOIN) {
		if side != nil || kind != nil {
			return nil, p.unsupported("join without JOIN")
		}
		return nil, nil
	}

	table, err := p.parseTable()
	if err != nil {
		return nil, err
	}
	join := New("Join", Arg{"this", table})
	if side != nil {
		join.Set("side", strings.ToUpper(side.Text))
	}
	if kind != nil {
		join.Set("kind", strings.ToUpper(kind.Text))
	}
	switch {
	case p.match(TokON):
		on, err := p.parseDisjunction()
		if err != nil {
			return nil, err
		}
		join.Set("on", on)
	case p.at(TokUSING):
		return nil, p.unsupported("USING")
	}
	join.Set("pivots", nil)
	return join, nil
}

func (p *parser) parseTable() (*Expression, error) {
	if p.at(TokL_PAREN) {
		return p.parseSubqueryTable()
	}

	// STREAM t reads a table as a stream in some dialects; it is a different
	// node, not a table called STREAM.
	if p.at(TokSTREAM) {
		return nil, p.unsupported("STREAM table")
	}

	parts := []*Expression{}
	for {
		id, err := p.parseIdentifier()
		if err != nil {
			return nil, err
		}
		if p.at(TokL_PAREN) {
			return nil, p.unsupported("table function")
		}
		parts = append(parts, id)
		if !p.match(TokDOT) {
			break
		}
	}
	if len(parts) > 3 {
		return nil, p.unsupported("over-qualified table")
	}

	// this is the table; the parts before it are db then catalog.
	table := New("Table", Arg{"this", parts[len(parts)-1]})
	names := []string{"db", "catalog"}
	for i := len(parts) - 2; i >= 0; i-- {
		table.Set(names[len(parts)-2-i], parts[i])
	}

	alias, err := p.parseTableAlias()
	if err != nil {
		return nil, err
	}
	if alias != nil {
		table.Set("alias", alias)
	}
	return table, nil
}

// parseSubqueryTable reads a parenthesised query used where a table goes.
//
// Only a query is accepted. `FROM (t)` is also a Subquery in the reference,
// wrapping a table rather than a select, and the two are different enough that
// guessing between them is not worth the statements it would buy.
func (p *parser) parseSubqueryTable() (*Expression, error) {
	p.advance() // the opening parenthesis
	if !p.at(TokSELECT) {
		return nil, p.unsupported("parenthesised table")
	}
	inner, err := p.parseSelect()
	if err != nil {
		return nil, err
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed subquery")
	}
	sub := New("Subquery", Arg{"this", inner})
	alias, err := p.parseTableAlias()
	if err != nil {
		return nil, err
	}
	sub.Set("alias", alias)
	sub.Set("sample", nil)
	return sub, nil
}

func (p *parser) parseTableAlias() (*Expression, error) {
	explicit := p.match(TokALIAS)
	if !explicit && !p.atAliasName() {
		return nil, nil
	}
	id, err := p.parseIdentifier()
	if err != nil {
		return nil, err
	}
	if p.at(TokL_PAREN) {
		return nil, p.unsupported("table alias with column list")
	}
	return New("TableAlias", Arg{"this", id}, Arg{"columns", nil}), nil
}

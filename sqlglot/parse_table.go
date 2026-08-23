package sqlglot

// FROM and its table references.

// parseFrom is entered with the FROM token current; the caller checked.
func (p *parser) parseFrom() (*Expression, error) {
	p.advance()
	table, err := p.parseTable()
	if err != nil {
		return nil, err
	}
	// A comma here is a cross join, not a second FROM. Refusing keeps the
	// port from reading `FROM a, b` as `FROM a` -- the exact shape of the
	// bypass that made this port necessary.
	if p.at(TokCOMMA) {
		return nil, p.unsupported("comma join")
	}
	if p.atAny(TokJOIN, TokINNER, TokLEFT, TokRIGHT, TokFULL, TokCROSS, TokAPPLY, TokLATERAL) {
		return nil, p.unsupported("join")
	}
	return New("From", Arg{"this", table}), nil
}

func (p *parser) parseTable() (*Expression, error) {
	if p.at(TokL_PAREN) {
		return nil, p.unsupported("subquery or parenthesised table")
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

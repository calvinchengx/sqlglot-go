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

	// CROSS APPLY and OUTER APPLY read as a join with a kind but build a
	// Lateral instead, and the kind is recorded on the Lateral rather than on
	// the Join. Both forms are parsed: the guard has been bypassed through
	// APPLY over a function before, and it can only refuse what it can see.
	if p.at(TokAPPLY) && kind != nil && (kind.Type == TokCROSS || kind.Type == TokOUTER) && side == nil {
		p.advance()
		return p.parseApply(kind.Type == TokCROSS)
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
	case p.tables.BareJoinIsOnTrue:
		// Databricks records a bare JOIN as `ON TRUE` rather than leaving the
		// slot empty and writing the comma form. The same relation either way,
		// but a different tree and a different statement to the engine -- so
		// the two executors would not be sending the same SQL.
		join.Set("on", New("Boolean", Arg{"this", true}))
	}
	join.Set("pivots", nil)
	return join, nil
}

// parseApply builds the Lateral that CROSS APPLY and OUTER APPLY produce.
func (p *parser) parseApply(cross bool) (*Expression, error) {
	lateral := New("Lateral")
	switch {
	case p.at(TokL_PAREN):
		sub, err := p.parseSubqueryTable()
		if err != nil {
			return nil, err
		}
		// The alias belongs to the Lateral, not to the subquery inside it.
		alias, _ := sub.Args["alias"].(*Expression)
		sub.Set("alias", nil)
		lateral.Set("this", sub)
		lateral.Set("view", nil)
		lateral.Set("outer", nil)
		lateral.Set("alias", alias)
		lateral.Set("cross_apply", cross)
		lateral.Set("ordinality", nil)
	case p.namesAFunctionCall() || p.atIdentifier():
		target, err := p.parseQualifiedCall()
		if err != nil {
			return nil, err
		}
		lateral.Set("this", target)
		lateral.Set("view", nil)
		lateral.Set("outer", nil)
		lateral.Set("alias", nil)
		lateral.Set("cross_apply", cross)
		lateral.Set("ordinality", false)
	default:
		return nil, p.unsupported("APPLY over something the port cannot read")
	}
	return New("Join", Arg{"this", lateral}, Arg{"pivots", nil}), nil
}

// parseQualifiedCall reads `f(…)` or `schema.f(…)`, which the reference
// represents as a chain of Dots ending in the call.
func (p *parser) parseQualifiedCall() (*Expression, error) {
	var this *Expression
	for {
		if p.namesAFunctionCall() {
			fn, err := p.parseFunction()
			if err != nil {
				return nil, err
			}
			if this == nil {
				return fn, nil
			}
			return New("Dot", Arg{"this", this}, Arg{"expression", fn}), nil
		}
		id, err := p.parseIdentifier()
		if err != nil {
			return nil, err
		}
		if this == nil {
			this = id
		} else {
			this = New("Dot", Arg{"this", this}, Arg{"expression", id})
		}
		if !p.match(TokDOT) {
			return nil, p.unsupported("APPLY over something that is not a call")
		}
	}
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
	var fn *Expression
	for {
		// A callable in a FROM clause is a table function, not a table. The
		// port builds it rather than refusing: the guard has to SEE that the
		// relation is a function to say so, and a statement it could not read
		// would be refused for the wrong reason. Reading a local file through
		// `main.read_csv_auto('/etc/passwd')` is a real bypass, and the audit
		// line has to name it.
		if p.namesAFunctionCall() {
			// Some names are not a Table here at all: `FROM UNNEST(x)` is an
			// Unnest in the reference. Wrapping the call in a Table writes the
			// same SQL back and is a different tree, so it is refused.
			if c := p.curr(); c != nil {
				if _, isTable := p.tables.TableFunctions[strings.ToUpper(c.Text)]; isTable {
					return nil, p.unsupported("table function " + strings.ToUpper(c.Text))
				}
			}
			f, err := p.parseFunction()
			if err != nil {
				return nil, err
			}
			fn = f
			break
		}
		id, err := p.parseIdentifier()
		if err != nil {
			return nil, err
		}
		parts = append(parts, id)
		if !p.match(TokDOT) {
			break
		}
	}

	var table *Expression
	names := []string{"db", "catalog"}
	if fn != nil {
		if len(parts) > 2 {
			return nil, p.unsupported("over-qualified table function")
		}
		// A table function always carries both qualifier slots, filled or not.
		table = New("Table", Arg{"this", fn})
		for i := len(parts) - 1; i >= 0; i-- {
			table.Set(names[len(parts)-1-i], parts[i])
		}
		for _, n := range names {
			if _, ok := table.Args[n]; !ok {
				table.Set(n, nil)
			}
		}
	} else {
		if len(parts) > 3 {
			return nil, p.unsupported("over-qualified table")
		}
		// this is the table; the parts before it are db then catalog.
		table = New("Table", Arg{"this", parts[len(parts)-1]})
		for i := len(parts) - 2; i >= 0; i-- {
			table.Set(names[len(parts)-2-i], parts[i])
		}
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
	if !p.at(TokSELECT) && !p.at(TokWITH) {
		return nil, p.unsupported("parenthesised table")
	}
	inner, err := p.parseQuery()
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

// namesAFunctionCall reports whether the current token opens a call. It is
// wider than atIdentifier on purpose: DuckDB's `glob` is an operator token
// everywhere else, and `main.glob('/**')` reads the filesystem, so the guard
// has to see the call rather than the parser lose it.
func (p *parser) namesAFunctionCall() bool {
	// A next token implies a current one, so curr is safe below.
	n := p.next()
	if n == nil || n.Type != TokL_PAREN {
		return false
	}
	// FuncTokens ALONE, which is the reference's own rule -- and it already
	// contains VAR and IDENTIFIER, so the extra atIdentifier() branch that
	// used to be here bought nothing and cost correctness: it accepted
	// keywords usable as identifiers, and `FROM SET('a','b')` became a call
	// where the reference reads a table named SET with an alias column list.
	//
	// Not cosmetic. The guard refuses a function used as a table by name, so
	// the two executors were refusing the same statement for DIFFERENT
	// reasons -- and the conformance suite checks the reason.
	_, ok := p.tables.FuncTokens[p.curr().Type]
	return ok
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
	columns, err := p.parseAliasColumns()
	if err != nil {
		return nil, err
	}
	return New("TableAlias", Arg{"this", id}, Arg{"columns", columns}), nil
}

// parseAliasColumns reads the `(a, b)` that may follow an alias, naming the
// columns of what it aliases. The same shape serves a table alias and a CTE:
// both carry it on a TableAlias.
func (p *parser) parseAliasColumns() ([]*Expression, error) {
	if !p.at(TokL_PAREN) {
		return nil, nil
	}
	p.advance()
	var columns []*Expression
	for !p.at(TokR_PAREN) {
		id, err := p.parseIdentifier()
		if err != nil {
			return nil, err
		}
		columns = append(columns, id)
		if !p.match(TokCOMMA) {
			break
		}
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed alias column list")
	}
	return columns, nil
}

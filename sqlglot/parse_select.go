package sqlglot

import "strings"

// The SELECT grammar, written against the reference's _parse_select_query.
//
// Argument order matters as much as argument content: the reference dumps a
// node's args in the order they were first assigned, and the comparison is
// exact. So a Select is created with the reference's full prefix of keys --
// most of them empty -- and later clauses fill the slots they reserved. Get the
// order wrong and every statement mismatches even when the tree is right.

// parseQuery reads a statement that produces rows: an optional WITH clause,
// a SELECT, and any set operations chained onto it.
func (p *parser) parseQuery() (*Expression, error) {
	with, err := p.parseWith()
	if err != nil {
		return nil, err
	}

	this, err := p.parseSelect()
	if err != nil {
		return nil, err
	}
	this, err = p.parseSetOperations(this)
	if err != nil {
		return nil, err
	}
	if with != nil {
		// The WITH clause is assigned to the whole statement once it is built,
		// so it dumps last -- and lands on the set operation, not on the first
		// query inside it, however early it was written.
		this.Set("with_", with)
	}
	return this, nil
}

// parseWith reads a WITH clause. RECURSIVE is refused: it changes what the
// CTE means and the reference records it.
func (p *parser) parseWith() (*Expression, error) {
	if !p.match(TokWITH) {
		return nil, nil
	}
	if p.at(TokRECURSIVE) {
		return nil, p.unsupported("WITH RECURSIVE")
	}

	var ctes []*Expression
	for {
		alias, err := p.parseIdentifier()
		if err != nil {
			return nil, err
		}
		if p.at(TokL_PAREN) && p.next() != nil && p.next().Type != TokSELECT {
			return nil, p.unsupported("CTE with a column list")
		}
		if !p.match(TokALIAS) {
			return nil, p.unsupported("CTE without AS")
		}
		if !p.match(TokL_PAREN) {
			return nil, p.unsupported("CTE without a parenthesised query")
		}
		inner, err := p.parseQuery()
		if err != nil {
			return nil, err
		}
		if !p.match(TokR_PAREN) {
			return nil, p.unsupported("unclosed CTE")
		}
		ctes = append(ctes, New("CTE",
			Arg{"this", inner},
			Arg{"alias", New("TableAlias", Arg{"this", alias}, Arg{"columns", nil})},
			Arg{"materialized", nil},
			Arg{"key_expressions", nil},
		))
		if !p.match(TokCOMMA) {
			break
		}
	}
	return New("With", Arg{"expressions", ctes}, Arg{"recursive", nil},
		Arg{"search", nil}, Arg{"udfs", nil}), nil
}

// setOperations are the words that chain two queries together.
var setOperations = map[TokenType]string{
	TokUNION:     "Union",
	TokEXCEPT:    "Except",
	TokINTERSECT: "Intersect",
}

func (p *parser) parseSetOperations(this *Expression) (*Expression, error) {
	for {
		c := p.curr()
		if c == nil {
			return this, nil
		}
		class, ok := setOperations[c.Type]
		if !ok {
			return this, nil
		}
		p.advance()

		// UNION is distinct, UNION ALL is not; the reference records which.
		distinct := true
		switch {
		case p.match(TokALL):
			distinct = false
		case p.match(TokDISTINCT):
			distinct = true
		}
		if c := p.curr(); c != nil && strings.EqualFold(c.Text, "BY") {
			return nil, p.unsupported("set operation BY NAME")
		}

		right, err := p.parseSelectOrParenthesised()
		if err != nil {
			return nil, err
		}
		this = New(class,
			Arg{"this", this}, Arg{"distinct", distinct}, Arg{"by_name", nil},
			Arg{"expression", right}, Arg{"side", nil}, Arg{"kind", nil}, Arg{"on", nil})
		p.liftSetOpModifiers(this, right)
	}
}

// liftSetOpModifiers moves a trailing ORDER BY, LIMIT or OFFSET off the
// right-hand query and onto the set operation. `SELECT a UNION SELECT b LIMIT 1`
// limits the union, not the second query -- the reference parses it onto the
// query and then lifts it, and which of the three lift differs per dialect.
func (p *parser) liftSetOpModifiers(setOp, right *Expression) {
	if !p.tables.ModifiersAttachedToSetOp || right == nil {
		return
	}
	for _, key := range p.tables.SetOpModifiers {
		if value, _ := right.Args[key].(*Expression); value != nil {
			right.Set(key, nil)
			setOp.Set(key, value)
		}
	}
}

func (p *parser) parseSelectOrParenthesised() (*Expression, error) {
	if !p.at(TokSELECT) {
		return nil, p.unsupported("set operation over something other than a SELECT")
	}
	return p.parseSelect()
}

// selectPrefix is the key order exp.Select is constructed with.
var selectPrefix = []string{
	"kind", "hint", "distinct", "expressions", "limit", "exclude", "operation_modifiers",
}

// parseSelect is entered with the SELECT token current; parseStatement checked.
func (p *parser) parseSelect() (*Expression, error) {
	p.advance()

	if p.at(TokHINT) {
		return nil, p.unsupported("hint")
	}

	distinct := p.match(TokDISTINCT)
	if distinct && p.at(TokON) {
		return nil, p.unsupported("DISTINCT ON")
	}
	if p.at(TokALL) {
		return nil, p.unsupported("SELECT ALL")
	}

	// TOP is read before the projections and lands in the slot Select
	// reserves for `limit` -- which is why a LIMIT written at the end of the
	// statement still dumps before the FROM clause.
	top, err := p.parseTop()
	if err != nil {
		return nil, err
	}

	projections, err := p.parseProjections()
	if err != nil {
		return nil, err
	}

	sel := New("Select")
	for _, k := range selectPrefix {
		sel.Set(k, nil)
	}
	if distinct {
		sel.Set("distinct", New("Distinct", Arg{"on", nil}))
	}
	sel.Set("expressions", projections)
	if top != nil {
		sel.Set("limit", top)
	}

	if p.at(TokFROM) {
		from, err := p.parseFrom()
		if err != nil {
			return nil, err
		}
		sel.Set("from_", from)
	}

	joins, err := p.parseJoins()
	if err != nil {
		return nil, err
	}
	for _, j := range joins {
		sel.Append("joins", j)
	}

	if err := p.parseQueryModifiers(sel); err != nil {
		return nil, err
	}
	return sel, nil
}

// parseTop reads T-SQL's row limiter. PERCENT is kept rather than refused:
// the guard has to see it to say that a proportion is not a row ceiling, and
// a statement it cannot parse would be refused for the wrong reason.
func (p *parser) parseTop() (*Expression, error) {
	if !p.match(TokTOP) {
		return nil, nil
	}
	c := p.curr()
	if c == nil || c.Type != TokNUMBER {
		return nil, p.unsupported("TOP without a count")
	}
	p.advance()
	limit := New("Limit",
		Arg{"this", nil},
		Arg{"expression", New("Literal", Arg{"this", c.Text}, Arg{"is_string", false})},
		Arg{"offset", nil},
		Arg{"limit_options", nil},
		Arg{"expressions", nil},
	)
	if cc := p.curr(); cc != nil && strings.EqualFold(cc.Text, "PERCENT") {
		p.advance()
		limit.Set("limit_options", New("LimitOptions",
			Arg{"percent", true}, Arg{"rows", false}, Arg{"with_ties", false}))
	}
	return limit, nil
}

func (p *parser) parseProjections() ([]*Expression, error) {
	var out []*Expression
	for {
		e, err := p.parseProjection()
		if err != nil {
			return nil, err
		}
		out = append(out, e)
		if !p.match(TokCOMMA) {
			break
		}
	}
	return out, nil
}

func (p *parser) parseProjection() (*Expression, error) {
	// T-SQL's `alias = expression` names a column with the left-hand side,
	// which is the opposite of what the same tokens mean anywhere else.
	if p.dialect == "tsql" && p.atAliasName() {
		if n := p.next(); n != nil && n.Type == TokEQ {
			return nil, p.unsupported("T-SQL alias assignment")
		}
	}
	e, err := p.parseExpression()
	if err != nil {
		return nil, err
	}
	return p.parseAlias(e)
}

// parseAlias attaches an explicit or implicit column alias.
func (p *parser) parseAlias(this *Expression) (*Expression, error) {
	explicit := p.match(TokALIAS)
	if !explicit && !p.atAliasName() {
		return this, nil
	}
	alias, err := p.parseIdentifier()
	if err != nil {
		return nil, err
	}
	return New("Alias", Arg{"this", this}, Arg{"alias", alias}), nil
}

// atAliasName reports whether the current token can begin an implicit alias.
// Only a plain word or a quoted identifier qualifies: anything else -- an
// operator, a keyword that starts a clause -- would silently swallow structure.
func (p *parser) atAliasName() bool {
	c := p.curr()
	if c == nil {
		return false
	}
	return c.Type == TokVAR || c.Type == TokIDENTIFIER
}

// parseQueryModifiers reads the trailing clauses in source order, as the
// reference does -- the order they appear in is the order they are assigned,
// and therefore the order they dump in.
func (p *parser) parseQueryModifiers(sel *Expression) error {
	for {
		switch {
		case p.at(TokWHERE):
			p.advance()
			e, err := p.parseExpression()
			if err != nil {
				return err
			}
			if err := p.setOnce(sel, "where", New("Where", Arg{"this", e})); err != nil {
				return err
			}
		case p.at(TokGROUP_BY):
			p.advance()
			// CUBE, ROLLUP and GROUPING SETS look like calls but land on
			// their own args of Group, not in its expression list.
			if p.atAny(TokCUBE, TokROLLUP, TokGROUPING_SETS) {
				return p.unsupported("GROUP BY " + p.curr().Text)
			}
			es, err := p.parseExpressionList()
			if err != nil {
				return err
			}
			if err := p.setOnce(sel, "group", New("Group", Arg{"expressions", es})); err != nil {
				return err
			}
		case p.at(TokHAVING):
			p.advance()
			e, err := p.parseExpression()
			if err != nil {
				return err
			}
			if err := p.setOnce(sel, "having", New("Having", Arg{"this", e})); err != nil {
				return err
			}
		case p.at(TokORDER_BY):
			p.advance()
			order, err := p.parseOrder()
			if err != nil {
				return err
			}
			if err := p.setOnce(sel, "order", order); err != nil {
				return err
			}
		case p.at(TokLIMIT):
			p.advance()
			e, err := p.parseExpression()
			if err != nil {
				return err
			}
			limit := New("Limit", Arg{"this", nil}, Arg{"expression", e},
				Arg{"limit_options", nil}, Arg{"expressions", nil})
			if err := p.setOnce(sel, "limit", limit); err != nil {
				return err
			}
		case p.at(TokOFFSET):
			p.advance()
			e, err := p.parseExpression()
			if err != nil {
				return err
			}
			offset := New("Offset", Arg{"this", nil}, Arg{"expression", e}, Arg{"expressions", nil})
			if err := p.setOnce(sel, "offset", offset); err != nil {
				return err
			}
		default:
			return nil
		}
	}
}

// setOnce refuses a repeated clause rather than letting the later one win --
// the reference raises there, and silently dropping one would change meaning.
func (p *parser) setOnce(node *Expression, key string, value *Expression) error {
	if existing, ok := node.Args[key]; ok && existing != nil {
		return p.unsupported("repeated " + key + " clause")
	}
	node.Set(key, value)
	return nil
}

func (p *parser) parseOrder() (*Expression, error) {
	var ordered []*Expression
	for {
		e, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		desc := false
		hasDirection := false
		switch {
		case p.match(TokDESC):
			desc, hasDirection = true, true
		case p.match(TokASC):
			desc, hasDirection = false, true
		}
		// NULLS FIRST/LAST tokenizes as the word NULLS then FIRST/LAST; the
		// reference records it on the Ordered node and this slice does not.
		if c := p.curr(); c != nil && (c.Text == "NULLS" || c.Type == TokWITH) {
			return nil, p.unsupported("NULLS FIRST/LAST or WITH FILL")
		}
		o := New("Ordered", Arg{"this", e})
		if hasDirection {
			o.Set("desc", desc)
		}
		o.Set("nulls_first", p.nullsFirst(hasDirection && desc))
		o.Set("with_fill", nil)
		ordered = append(ordered, o)
		if !p.match(TokCOMMA) {
			break
		}
	}
	return New("Order", Arg{"this", nil}, Arg{"expressions", ordered}, Arg{"siblings", nil}), nil
}

// nullsFirst answers where NULLs sort when the statement does not say.
//
// It is not "the opposite of DESC": PostgreSQL sorts NULLs last ascending and
// first descending, DuckDB always last, T-SQL the other way round. The rule is
// the reference's, read off the dialect.
func (p *parser) nullsFirst(desc bool) bool {
	ordering := p.tables.NullOrdering
	if ordering == "nulls_are_last" {
		return false
	}
	if desc {
		return ordering != "nulls_are_small"
	}
	return ordering == "nulls_are_small"
}

func (p *parser) parseExpressionList() ([]*Expression, error) {
	var out []*Expression
	for {
		e, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		out = append(out, e)
		if !p.match(TokCOMMA) {
			break
		}
	}
	return out, nil
}

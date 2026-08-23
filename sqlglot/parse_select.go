package sqlglot

// The SELECT grammar, written against the reference's _parse_select_query.
//
// Argument order matters as much as argument content: the reference dumps a
// node's args in the order they were first assigned, and the comparison is
// exact. So a Select is created with the reference's full prefix of keys --
// most of them empty -- and later clauses fill the slots they reserved. Get the
// order wrong and every statement mismatches even when the tree is right.

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

	if p.at(TokFROM) {
		from, err := p.parseFrom()
		if err != nil {
			return nil, err
		}
		sel.Set("from_", from)
	}

	if err := p.parseQueryModifiers(sel); err != nil {
		return nil, err
	}
	return sel, nil
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
		if n := p.peek(1); n != nil && n.Type == TokEQ {
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
		o.Set("nulls_first", !desc)
		ordered = append(ordered, o)
		if !p.match(TokCOMMA) {
			break
		}
	}
	return New("Order", Arg{"this", nil}, Arg{"expressions", ordered}, Arg{"siblings", nil}), nil
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

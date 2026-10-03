package sqlglot

// Oracle writes a pivot alias without AS, and it drops a table qualifier
// from the FOR and IN columns when it writes them. The aggregate stays
// qualified. The tree keeps the qualifier; only the spelling drops it.
func init() {
	generators["PivotAlias"] = (*generator).writePivotAlias
}

func (g *generator) writePivotAlias(e *Expression) string {
	value := g.child(e, "this")
	name := g.child(e, "alias")
	if value == "" || name == "" {
		return g.fail("PivotAlias without a value and a name")
	}
	return value + " AS " + name
}

func (g *generator) pivotAliasSep() string {
	if g.dialect == "oracle" {
		return " "
	}
	return " AS "
}

func (g *generator) appendJoinPivots(e *Expression, out string) string {
	pivots, _ := e.Args["pivots"].([]*Expression)
	for _, pivot := range pivots {
		out += g.node(pivot)
	}
	return out
}

func (p *parser) attachJoinPivots(join *Expression) (*Expression, error) {
	found, err := p.parsePivots()
	switch {
	case err != nil:
		return nil, err
	case len(found) > 0:
		join.Set("pivots", found)
	default:
		join.Set("pivots", nil)
	}
	return join, nil
}

// pivotAggregate drops a false join mark from the aggregate itself. Oracle's
// pivot parser never records one there; the columns inside the call still do.
func (p *parser) pivotAggregate(e *Expression) *Expression {
	mark, stamped := false, false
	if e != nil {
		mark, stamped = e.Args["join_mark"].(bool)
	}
	if stamped && !mark && p.dialect == "oracle" {
		e.Set("join_mark", nil)
	}
	return e
}

func (p *parser) pivotInAlias(value *Expression) (*Expression, error) {
	alias, err := p.parseBitwise()
	if err != nil {
		return nil, err
	}
	if alias == nil {
		return nil, p.unsupported("PIVOT value without an alias")
	}
	// A bare name is a column here and the reference keeps the identifier.
	// A name with a database stays a column. A literal or an expression stays
	// itself: UNPIVOT aliases are often strings or concatenations.
	if alias.Class == "Column" && alias.Args["db"] == nil && alias.Args["catalog"] == nil {
		if id, _ := alias.Args["this"].(*Expression); id != nil {
			alias = id
		}
	}
	return New("PivotAlias", Arg{"this", value}, Arg{"alias", alias}), nil
}

func unqualifiedPivotField(field *Expression) *Expression {
	if field == nil || field.Class != "In" {
		return field
	}
	out := field.shallowCopy()
	if this, _ := out.Args["this"].(*Expression); this != nil {
		out.Set("this", unqualifiedColumnTree(this))
	}
	exprs, _ := out.Args["expressions"].([]*Expression)
	if len(exprs) == 0 {
		return out
	}
	copied := make([]*Expression, len(exprs))
	for i, expr := range exprs {
		copied[i] = unqualifiedColumnTree(expr)
	}
	out.Set("expressions", copied)
	return out
}

func unqualifiedColumnTree(e *Expression) *Expression {
	if e == nil {
		return nil
	}
	if e.Class == "Column" {
		if e.Args["table"] == nil && e.Args["db"] == nil && e.Args["catalog"] == nil {
			return e
		}
		out := e.shallowCopy()
		out.Set("table", nil)
		out.Set("db", nil)
		out.Set("catalog", nil)
		return out
	}
	var out *Expression
	for _, key := range e.Keys {
		switch child := e.Args[key].(type) {
		case *Expression:
			next := unqualifiedColumnTree(child)
			if next == child {
				continue
			}
			if out == nil {
				out = e.shallowCopy()
			}
			out.Set(key, next)
		case []*Expression:
			nexts := make([]*Expression, len(child))
			changed := false
			for i, item := range child {
				nexts[i] = unqualifiedColumnTree(item)
				if nexts[i] != item {
					changed = true
				}
			}
			if !changed {
				continue
			}
			if out == nil {
				out = e.shallowCopy()
			}
			out.Set(key, nexts)
		}
	}
	if out == nil {
		return e
	}
	return out
}

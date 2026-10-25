package sqlglot

import "strings"

// parseDuckDBListSort reads DuckDB's LIST_SORT and ARRAY_REVERSE_SORT, whose
// optional string arguments name the order and where NULLs go
// (DuckDBParser.FUNCTIONS, _build_list_sort).
//
// Entered with the name current and the opening parenthesis after it.
func (p *parser) parseDuckDBListSort(name string) (*Expression, error) {
	p.advance() // the name
	p.advance() // (
	var args []*Expression
	for {
		arg, err := p.parseAssignment()
		if err != nil {
			return nil, err
		}
		args = append(args, arg)
		if !p.match(TokCOMMA) {
			break
		}
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed " + name)
	}
	if len(args) == 0 {
		return nil, p.unsupported(name + " without an argument")
	}
	orders := args[1:]
	if name == "ARRAY_REVERSE_SORT" {
		orders = append([]*Expression{New("Literal",
			Arg{"this", "DESC"}, Arg{"is_string", true})}, orders...)
	}
	orderName, orderOK := listSortOrderName(orders, 0, "ASC")
	nullsName, nullsOK := listSortOrderName(orders, 1, "NULLS LAST")
	valid := orderOK && nullsOK &&
		(orderName == "ASC" || orderName == "DESC") &&
		(nullsName == "NULLS FIRST" || nullsName == "NULLS LAST")
	if !valid {
		return New("Anonymous", Arg{"this", name}, Arg{"expressions", args}), nil
	}
	node := New("ArraySort", Arg{"this", args[0]})
	if orderName == "DESC" {
		node.Set("asc", New("Boolean", Arg{"this", false}))
	}
	node.Set("nulls_first", New("Boolean", Arg{"this", nullsName == "NULLS FIRST"}))
	return node, nil
}

// listSortOrderName is the upper-cased name of a STRING argument, or the
// default where the argument was not written at all.
func listSortOrderName(args []*Expression, i int, fallback string) (string, bool) {
	if i >= len(args) || args[i] == nil {
		return fallback, true
	}
	if !isStringLiteral(args[i]) {
		return "", false
	}
	text, _ := args[i].Args["this"].(string)
	return strings.ToUpper(text), true
}

// writeArraySort writes an ArraySort back. DuckDB spells the default order as
// LIST_SORT and the reverse as ARRAY_REVERSE_SORT, and only names the order
// when NULLs go first (DuckDBGenerator, _array_sort_sql); every other dialect
// writes ARRAY_SORT over its two arguments.
func (g *generator) writeArraySort(e *Expression) string {
	if g.dialect != "duckdb" {
		return "ARRAY_SORT(" + g.child(e, "this") + ", " + g.child(e, "expression") + ")"
	}
	asc := true
	if v, ok := e.Args["asc"].(*Expression); ok && v != nil {
		if v.Class != "Boolean" {
			return g.fail("LIST_SORT with an order that is not a literal")
		}
		asc, _ = v.Args["this"].(bool)
	}
	nullsFirst := false
	if v, ok := e.Args["nulls_first"].(*Expression); ok && v != nil {
		if v.Class != "Boolean" {
			return g.fail("LIST_SORT with a null order that is not a literal")
		}
		nullsFirst, _ = v.Args["this"].(bool)
	}
	if !nullsFirst {
		name := "LIST_SORT"
		if !asc {
			name = "ARRAY_REVERSE_SORT"
		}
		return name + "(" + g.child(e, "this") + ")"
	}
	order := "ASC"
	if !asc {
		order = "DESC"
	}
	return "LIST_SORT(" + g.child(e, "this") + ", '" + order + "', 'NULLS FIRST')"
}

func init() {
	generators["ArraySort"] = (*generator).writeArraySort
}

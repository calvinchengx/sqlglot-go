package sqlglot

// starrocksNode writes the classes StarRocks handles its own way, the same
// shape as the sqlite and hive dispatchers.
func (g *generator) starrocksNode(e *Expression) (string, bool) {
	if e.Class == "Select" {
		return g.starrocksRecursiveDateArray(e)
	}
	return "", false
}

// starrocksRecursiveDateArray rewrites a SELECT over
// `UNNEST(GENERATE_DATE_ARRAY(start, end, INTERVAL n unit))` as a RECURSIVE
// CTE, which is how StarRocks generates a series of dates: the dialect has no
// UNNEST over a generated array, so the reference walks one row per step
// (StarRocksGenerator.TRANSFORMS,
// transforms.unnest_generate_date_array_using_recursive_cte).
//
// The rewritten statement is rendered through the ordinary writer, and the
// CTE it builds carries no such UNNEST, so the rewrite does not run again.
func (g *generator) starrocksRecursiveDateArray(e *Expression) (string, bool) {
	from, _ := e.Args["from_"].(*Expression)
	if from == nil {
		return "", false
	}
	unnest, _ := from.Args["this"].(*Expression)
	if unnest == nil || unnest.Class != "Unnest" {
		return "", false
	}
	items, _ := unnest.Args["expressions"].([]*Expression)
	if len(items) != 1 || items[0] == nil || items[0].Class != "GenerateDateArray" {
		return "", false
	}
	array := items[0]
	start, _ := array.Args["start"].(*Expression)
	end, _ := array.Args["end"].(*Expression)
	step, _ := array.Args["step"].(*Expression)
	if start == nil || end == nil || step == nil || step.Class != "Interval" {
		return "", false
	}
	// The column the series is named by: the alias's own first column, or
	// the name the reference falls back to.
	column := "date_value"
	if alias, _ := unnest.Args["alias"].(*Expression); alias != nil {
		if cols, _ := alias.Args["columns"].([]*Expression); len(cols) > 0 && cols[0] != nil {
			column = cols[0].Name()
		}
	}
	start = castTo(start, "DATE")
	added := New("DateAdd",
		Arg{"this", columnRef(column)},
		Arg{"expression", numberLiteral(step.Name())},
		Arg{"unit", step.Args["unit"]})
	added = castTo(added, "DATE")
	end = castTo(end, "DATE")

	const cteName = "_generated_dates"
	base := New("Select", Arg{"expressions", []*Expression{
		New("Alias", Arg{"this", start}, Arg{"alias", identifierOf(column)}),
	}})
	recursive := New("Select",
		Arg{"expressions", []*Expression{added}},
		Arg{"from_", New("From", Arg{"this", New("Table", Arg{"this", identifierOf(cteName)})})},
		Arg{"where", New("Where", Arg{"this", New("LTE",
			Arg{"this", added}, Arg{"expression", end})})})
	query := New("Union", Arg{"this", base}, Arg{"distinct", false}, Arg{"expression", recursive})

	// The UNNEST stands in for a subquery over the CTE.
	inner := New("Select", Arg{"expressions", []*Expression{columnRef(column)}},
		Arg{"from_", New("From", Arg{"this", New("Table", Arg{"this", identifierOf(cteName)})})})
	from.Set("this", New("Subquery", Arg{"this", inner},
		Arg{"alias", New("TableAlias", Arg{"this", identifierOf(cteName)})}))

	cte := New("CTE", Arg{"this", query},
		Arg{"alias", New("TableAlias",
			Arg{"this", identifierOf(cteName)},
			Arg{"columns", []*Expression{identifierOf(column)}})})
	with, _ := e.Args["with_"].(*Expression)
	if with == nil {
		with = New("With")
	} else {
		with = with.shallowCopy()
	}
	with.Set("recursive", true)
	existing, _ := with.Args["expressions"].([]*Expression)
	with.Set("expressions", append([]*Expression{cte}, existing...))
	rewritten := e.shallowCopy()
	rewritten.Set("with_", with)
	return g.node(rewritten), true
}

// castTo wraps a node in a cast to the named type, leaving one already cast
// there alone, as the reference's own exp.cast does.
func castTo(e *Expression, kind string) *Expression {
	if e != nil && e.Class == "Cast" {
		if to, _ := e.Args["to"].(*Expression); to != nil {
			if text, _ := to.Args["this"].(DataTypeKind); string(text) == kind {
				return e
			}
		}
	}
	return New("Cast", Arg{"this", e},
		Arg{"to", New("DataType", Arg{"this", DataTypeKind(kind)})})
}

func identifierOf(name string) *Expression {
	return New("Identifier", Arg{"this", name}, Arg{"quoted", false})
}

func columnRef(name string) *Expression {
	return New("Column", Arg{"this", identifierOf(name)})
}

func numberLiteral(text string) *Expression {
	return New("Literal", Arg{"this", text}, Arg{"is_string", false})
}

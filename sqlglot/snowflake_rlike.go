package sqlglot

// Snowflake's RLIKE is REGEXP_LIKE, and both always match the whole
// value. That mark is stored and not written.
func init() {
	pinWholeRegexpMatch("snowflake", "REGEXP_LIKE", "RLIKE")
}

func pinWholeRegexpMatch(dialect, kept, alias string) {
	tables := parserTables[dialect]
	if tables == nil || tables.Functions == nil || tables.FunctionSQL == nil {
		return
	}
	functions, forms := tables.Functions, tables.FunctionSQL
	spec, known := functions[kept]
	if !known || spec.Class != "RegexpLike" {
		return
	}
	rewritten := make([]FuncArg, 0, len(spec.Args))
	for _, arg := range spec.Args {
		if arg.Key == "full_match" {
			arg = FuncArg{Key: "full_match", Index: -1, Const: true}
		}
		rewritten = append(rewritten, arg)
	}
	spec.Args = rewritten
	functions[kept] = spec
	functions[alias] = spec

	prior := make([]FuncSQL, len(forms["RegexpLike"]))
	copy(prior, forms["RegexpLike"])
	wholeValue := FuncSQL{
		Name: kept,
		Keys: []string{"this", "expression"},
		Consts: []FuncConst{
			{Key: "flag", Value: nil},
			{Key: "full_match", Value: true},
		},
	}
	forms["RegexpLike"] = append(prior, wholeValue)
}

// wholeRegexpLike marks a Snowflake infix RLIKE as a whole-value match.
func wholeRegexpLike(dialect string, e *Expression) *Expression {
	if dialect != "snowflake" || e == nil || e.Class != "RegexpLike" {
		return e
	}
	e.Set("full_match", true)
	return e
}

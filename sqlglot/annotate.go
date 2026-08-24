package sqlglot

// Annotate answers what TYPE an expression has.
//
// This is the reference's `annotate_types`, and it is what several of the
// port's refusals are actually waiting on: whether a subscript's index is an
// integer, whether a connector's operand is already a condition, whether
// REGEXP_REPLACE's last argument is text. Each of those is a question about a
// type, and the port has been refusing rather than guessing.
//
// SCOPE-FREE only. The reference resolves a column through the optimizer's
// scope machinery to a table to a schema; none of that is ported, so a column
// annotates as UNKNOWN here rather than being guessed at. That is not a
// crippling limit for the callers above -- a literal, a cast and an operator
// over them all have local answers, which is most of what the refusals need.
//
// A nil result means "no answer", which callers must treat as UNKNOWN rather
// than as any particular type.
func Annotate(e *Expression, dialect string) *Expression {
	// A bare NULL is UNKNOWN, not NULL. The reference types it NULL while it
	// works and then converts it at the end -- a NULL-typed RESULT tells a
	// caller nothing that UNKNOWN does not. Inside an operator it still
	// behaves as NULL, which is why the conversion happens here and not in
	// annotate below.
	t := annotate(e, dialect)
	if typeKind(t) == "NULL" && !supportsNullType[dialect] {
		return dataType("UNKNOWN")
	}
	return t
}

func annotate(e *Expression, dialect string) *Expression {
	if e == nil {
		return nil
	}
	switch e.Class {
	case "Literal":
		// A string is VARCHAR, a whole number INT, anything else DOUBLE.
		if str, _ := e.Args["is_string"].(bool); str {
			return dataType("VARCHAR")
		}
		if text, _ := e.Args["this"].(string); isIntegerText(text) {
			return dataType("INT")
		}
		return dataType("DOUBLE")
	case "Boolean":
		return dataType("BOOLEAN")
	case "Null":
		return dataType("NULL")
	case "Cast", "TryCast":
		// A cast is the type it casts to. That is the whole rule, and it is
		// why so many of the fixture's cases need no column at all.
		to, _ := e.Args["to"].(*Expression)
		return to
	case "Paren":
		return annotate(childOf(e, "this"), dialect)
	case "Subquery":
		// A scalar subquery is the type of its single projection, which needs
		// no scope: `(SELECT 2.5 AS c)` is a DOUBLE.
		return annotateScalarSubquery(e, dialect)
	case "Not":
		return dataType("BOOLEAN")
	case "Array":
		return annotateArray(e, dialect)
	}

	// A connector or a predicate is a BOOLEAN whatever its operands are --
	// which is the part of this the simplifier most needs, and the part that
	// needs no schema.
	if isA("Connector", e) || isA("Predicate", e) {
		return dataType("BOOLEAN")
	}

	// A unary operator carries its operand's type: `-5` is an INT, `~5` is an
	// INT.
	if isA("Unary", e) {
		return annotate(childOf(e, "this"), dialect)
	}

	// A function returns what its rule says: a fixed type, or its arguments
	// coerced -- possibly widened, possibly wrapped in an array.
	if rule, ok := funcReturns[dialect][e.Class]; ok {
		return annotateFunction(e, dialect, rule)
	}

	// A binary operator coerces its two operands -- but an operand with NO
	// answer poisons the whole thing. `NULL + 1` is an INT because NULL
	// contributes nothing; `x + 1` is UNKNOWN because x could be anything,
	// and answering INT there would be inventing a type from half a
	// expression. The two look alike and are not, which is what a test of my
	// own declining cases caught.
	if isA("Binary", e) {
		left := annotate(childOf(e, "this"), dialect)
		right := annotate(childOf(e, "expression"), dialect)
		if left == nil || right == nil {
			return nil
		}
		return coerceTypes(left, right)
	}
	return nil
}

func childOf(e *Expression, key string) *Expression {
	child, _ := e.Args[key].(*Expression)
	return child
}

func dataType(kind string) *Expression {
	return New("DataType", Arg{"this", DataTypeKind(kind)}, Arg{"nested", false})
}

func typeKind(t *Expression) string {
	if t == nil || t.Class != "DataType" {
		return ""
	}
	kind, _ := t.Args["this"].(DataTypeKind)
	return string(kind)
}

func isIntegerText(text string) bool {
	if text == "" {
		return false
	}
	for _, r := range text {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// annotateArray types an array literal by its elements: `[1, 1.5]` is an
// ARRAY<DOUBLE>, because the elements coerce to DOUBLE before being wrapped.
func annotateArray(e *Expression, dialect string) *Expression {
	items, _ := e.Args["expressions"].([]*Expression)
	var element *Expression
	for _, item := range items {
		t := annotate(item, dialect)
		if t == nil {
			return nil
		}
		element = coerceTypes(element, t)
	}
	if element == nil {
		return nil
	}
	return New("DataType", Arg{"this", DataTypeKind("ARRAY")},
		Arg{"expressions", []*Expression{element}}, Arg{"nested", true})
}

// coerceTypes is the reference's `_maybe_coerce`: the second type wins where
// the first widens into it, and the first wins otherwise. A PARAMETERISED type
// -- DECIMAL(18, 2) -- never coerces, because its parameters would have to be
// reconciled too and the reference declines to.
func coerceTypes(a, b *Expression) *Expression {
	// A NULL operand contributes nothing: `NULL + 1` is an INT, not a NULL.
	switch {
	case a == nil, typeKind(a) == "NULL":
		return b
	case b == nil, typeKind(b) == "NULL":
		return a
	}
	if hasTypeParams(a) {
		return a
	}
	if hasTypeParams(b) {
		return b
	}
	if coercesTo[typeKind(a)][typeKind(b)] {
		return b
	}
	return a
}

func hasTypeParams(t *Expression) bool {
	if t == nil {
		return false
	}
	params, _ := t.Args["expressions"].([]*Expression)
	return len(params) > 0
}

// annotateScalarSubquery types `(SELECT expr)` as its one projection. More
// than one, or none, and there is no single answer to give.
func annotateScalarSubquery(e *Expression, dialect string) *Expression {
	inner := childOf(e, "this")
	if inner == nil || inner.Class != "Select" {
		return nil
	}
	projections, _ := inner.Args["expressions"].([]*Expression)
	if len(projections) != 1 {
		return nil
	}
	only := projections[0]
	if only.Class == "Alias" {
		only = childOf(only, "this")
	}
	return annotate(only, dialect)
}

// annotateFunction applies one function's return rule.
//
// The reference returns UNKNOWN the moment ANY argument is unknown, rather
// than coercing what it does know -- so a call over a column, whose type the
// port cannot resolve, has no answer here either. That is the honest result
// and not a limitation worth working around: a type inferred from half a
// call is a type nobody checked.
func annotateFunction(e *Expression, dialect string, rule funcReturn) *Expression {
	if rule.Kind == "fixed" {
		return dataType(rule.Type)
	}

	var coerced *Expression
	seen := false
	for _, key := range []string{"this", "expressions"} {
		switch v := e.Args[key].(type) {
		case *Expression:
			t := annotate(v, dialect)
			if t == nil {
				return nil
			}
			seen = true
			coerced = coerceTypes(coerced, t)
		case []*Expression:
			for _, item := range v {
				t := annotate(item, dialect)
				if t == nil {
					return nil
				}
				seen = true
				coerced = coerceTypes(coerced, t)
			}
		}
	}
	if !seen || coerced == nil {
		return nil
	}

	switch rule.Kind {
	case "promote":
		// An integer widens to BIGINT and a real to DOUBLE; anything else is
		// left as it is.
		switch typeKind(coerced) {
		case "INT", "SMALLINT", "TINYINT", "BIGINT":
			return dataType("BIGINT")
		case "FLOAT", "DOUBLE":
			return dataType("DOUBLE")
		}
		return coerced
	case "array":
		return New("DataType", Arg{"this", DataTypeKind("ARRAY")},
			Arg{"expressions", []*Expression{coerced}}, Arg{"nested", true})
	}
	return coerced
}

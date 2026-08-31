package sqlglot

import "strconv"

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

// annotate is Annotate without the conversion at the end, and it MEMOISES:
// see Expression.rawType for why the raw answer is the one worth keeping.
func annotate(e *Expression, dialect string) *Expression {
	if e == nil {
		return nil
	}
	if e.rawTypeKnown && e.rawTypeDialect == dialect {
		return e.rawType
	}
	t := annotateNode(e, dialect)
	e.rawType, e.rawTypeDialect, e.rawTypeKnown = t, dialect, true
	return t
}

func annotateNode(e *Expression, dialect string) *Expression {
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
	case "Case":
		return annotateCase(e, dialect)
	case "Bracket":
		return annotateBracket(e, dialect)
	case "Slice":
		// A slice's own type is UNKNOWN; its BOUNDS are annotated by the walk
		// above, which is what the reference records.
		return dataType("UNKNOWN")
	case "Column", "Identifier", "Star":
		// UNKNOWN is the ANSWER here, not the absence of one. Resolving a
		// column needs a schema and the port has none, and that is exactly
		// what the reference reports without one too. Distinguishing this
		// from "no rule for this node" is what lets a caller tell an honest
		// UNKNOWN from a gap in the port -- see applyIndexOffset, which
		// refuses on the second and proceeds on the first.
		return dataType("UNKNOWN")
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
	// A class the reference's annotator has no entry for at all. UNKNOWN is
	// its answer there rather than its silence -- it looks the class up, finds
	// nothing, and says so -- and the difference matters to a subscript, which
	// may only be shifted over a base that is UNKNOWN or an ARRAY.
	//
	// Left until last so that every rule the port DOES have still decides
	// first: a class here that the port also reads as a binary operator keeps
	// the coercion, which is the answer the reference's own binary annotator
	// would give it.
	if unannotatedClasses[dialect][e.Class] {
		return dataType("UNKNOWN")
	}
	return nil
}

func childOf(e *Expression, key string) *Expression {
	child, _ := e.Args[key].(*Expression)
	return child
}

// dataType builds the kind of DataType the ANNOTATOR produces, which is not
// the kind the parser produces: the reference makes this one from a bare type
// name and it carries no `nested` arg at all, where a written type always
// records one. Setting `nested: false` here put an extra record in the dump
// of every annotated node -- 44 mismatches, all the same cause.
func dataType(kind string) *Expression {
	return New("DataType", Arg{"this", DataTypeKind(kind)})
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

// AnnotateFully stamps a type on every node of a subtree and reports whether
// every one of them had an answer.
//
// It MUTATES, and that is the point: the reference's annotations are part of
// the tree it produces -- they are dumped, and the differential compares them
// -- so a port that computed a type without recording it would agree about
// the answer and disagree about the tree.
//
// Where it returns false the port met a node it has no rule for. A caller
// that stamped UNKNOWN there would be recording its own ignorance as the
// reference's answer, which is why the subscript rule refuses instead.
func AnnotateFully(e *Expression, dialect string) bool {
	if e == nil {
		return true
	}
	// A DataType is a type, not an expression that HAS one. Walking into one
	// stamped UNKNOWN on the members of `MAP(INT, TEXT)`, which the reference
	// leaves alone -- the annotation belongs to the cast, not to the spelling
	// of what it casts to.
	if e.Class == "DataType" {
		return true
	}
	complete := true
	for _, key := range e.Keys {
		switch v := e.Args[key].(type) {
		case *Expression:
			complete = AnnotateFully(v, dialect) && complete
		case []*Expression:
			for _, k := range v {
				complete = AnnotateFully(k, dialect) && complete
			}
		}
	}
	if e.Type != nil {
		return complete
	}
	t := Annotate(e, dialect)
	if t == nil {
		return false
	}
	e.Type = t
	return complete
}

// IsIntegerType reports whether a stamped type is one the reference counts as
// an integer -- which is what decides whether a subscript's index is shifted.
func IsIntegerType(t *Expression) bool { return integerTypes[typeKind(t)] }

var integerTypes = map[string]bool{
	"BIGINT": true, "BIT": true, "INT": true, "INT128": true, "INT256": true,
	"MEDIUMINT": true, "SMALLINT": true, "TINYINT": true, "UBIGINT": true,
	"UINT": true, "UINT128": true, "UINT256": true, "UMEDIUMINT": true,
	"USMALLINT": true, "UTINYINT": true,
}

// annotateCase types a CASE by what its BRANCHES produce -- the value of each
// WHEN and the ELSE. The conditions are beside the point: they are all
// booleans and none of them is the result.
func annotateCase(e *Expression, dialect string) *Expression {
	var result *Expression
	branches, _ := e.Args["ifs"].([]*Expression)
	for _, branch := range branches {
		value := childOf(branch, "true")
		if value == nil {
			continue
		}
		t := annotate(value, dialect)
		if t == nil {
			return nil
		}
		result = coerceTypes(result, t)
	}
	if fallback := childOf(e, "default"); fallback != nil {
		t := annotate(fallback, dialect)
		if t == nil {
			return nil
		}
		result = coerceTypes(result, t)
	}
	return result
}

// annotateBracket types a subscript. A SLICE of something is that same thing
// -- `arr[1:2]` is still an array -- while an element of an ARRAY<T> is a T.
// A subscript of anything else has no answer here; the reference resolves a
// map's key to its value, which needs the map's own keys and is not ported.
func annotateBracket(e *Expression, dialect string) *Expression {
	items, _ := e.Args["expressions"].([]*Expression)
	if len(items) != 1 {
		return nil
	}
	base := annotate(childOf(e, "this"), dialect)
	if base == nil {
		return nil
	}
	if items[0].Class == "Slice" {
		return base
	}
	if typeKind(base) != "ARRAY" {
		return nil
	}
	members, _ := base.Args["expressions"].([]*Expression)
	if len(members) != 1 {
		return nil
	}
	return members[0]
}

// ApplyIndexOffset shifts a subscript between the dialect's numbering and
// sqlglot's 0-based Bracket. The PARSER subtracts the offset and the
// GENERATOR adds it back, which is why both call this.
//
// Almost all of it is conditions rather than arithmetic: it fires only for a
// single index, only where the base is UNKNOWN or an ARRAY, and only where
// the index is an INTEGER. So `a[x]`, `a['k']` and `a[1:2]` keep the index
// they were written with, and gain only the type annotations the reference
// stamps on them while deciding not to shift.
//
// It reports false where the port cannot type the subtree. Its annotator is
// narrower than the reference's, and stamping UNKNOWN where the reference
// infers a type would record the port's own ignorance as an answer.
func ApplyIndexOffset(this *Expression, items []*Expression, offset int, dialect string) ([]*Expression, bool) {
	if offset == 0 || len(items) != 1 || this == nil {
		return items, true
	}
	if this.Type == nil && !AnnotateFully(this, dialect) {
		return nil, false
	}
	switch typeKind(this.Type) {
	case "UNKNOWN", "ARRAY":
	default:
		return items, true
	}
	index := items[0]
	if index.Type == nil && !AnnotateFully(index, dialect) {
		return nil, false
	}
	if !IsIntegerType(index.Type) {
		return items, true
	}
	// The shift goes through the SIMPLIFIER, as the reference's does: reading
	// `a[1]` gives `a[0]`, and writing that back gives `a[1]` again.
	//
	// The Add is BUILT the way the reference builds one, because both halves
	// of how it is built are load-bearing:
	//
	//   -- a negative offset is a NEGATED one, not a negative literal. The
	//      port wrote `Literal(-1)`, which its own annotator reads as a
	//      DOUBLE, which made the sum a DOUBLE, which made the GENERATOR
	//      decline to shift it back -- and `a[CAST(x AS INT)]` came out as
	//      `a[CAST(x AS INT) + -1]`, one element lower than it went in.
	//
	//   -- an operand that is itself a binary operation is parenthesised,
	//      unless the sum is being added to a sum, in which case the
	//      reference leaves both alone.
	amount := New("Literal", Arg{"this", strconv.Itoa(offset)}, Arg{"is_string", false})
	if offset < 0 {
		amount = New("Neg", Arg{"this",
			New("Literal", Arg{"this", strconv.Itoa(-offset)}, Arg{"is_string", false})})
	}
	left := index
	if index.Class != "Add" && isA("Binary", index) {
		left = New("Paren", Arg{"this", index})
	}
	shifted := New("Add", Arg{"this", left}, Arg{"expression", amount})
	return []*Expression{Simplify(shifted, dialect)}, true
}

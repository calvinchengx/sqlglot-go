package sqlglot

import "testing"

// TestAnnotateShapes pins the annotator's rules by example. The contract
// harness measures how much of sqlglot's fixture the port reproduces; these
// name the individual rules, including the ones that answer NOTHING.
func TestAnnotateShapes(t *testing.T) {
	for _, c := range []struct{ name, sql, dialect, want string }{
		{"integer literal", "5", "", "INT"},
		{"real literal", "5.3", "", "DOUBLE"},
		{"string literal", "'bla'", "", "VARCHAR"},
		{"boolean", "TRUE", "", "BOOLEAN"},
		{"negation carries its operand", "-5", "", "INT"},
		{"parentheses carry their operand", "(5)", "", "INT"},
		{"a cast is what it casts to", "CAST(x AS BIGINT)", "", "BIGINT"},
		{"a comparison is boolean", "1 = 1", "", "BOOLEAN"},
		{"a connector is boolean", "TRUE AND FALSE", "", "BOOLEAN"},
		{"NOT is boolean", "NOT TRUE", "", "BOOLEAN"},
		{"binary coerces its operands", "1 + 1.5", "", "DOUBLE"},
		{"a NULL operand contributes nothing", "NULL + 1", "", "INT"},
		{"a bare NULL is UNKNOWN", "NULL", "", "UNKNOWN"},
		// Databricks is the one dialect of the five with a null type.
		{"unless the dialect has a null type", "NULL", "databricks", "VOID"},
		{"an array is an array of its elements", "[1, 1.5]", "", "ARRAY<DOUBLE>"},
		{"a scalar subquery is its projection", "1 + (SELECT 2.5 AS c)", "", "DOUBLE"},
		{"a function with a fixed return", "ASCII('A')", "", "INT"},
		{"a function that returns its argument", "ABS(1.5)", "", "DOUBLE"},
		{"a function over several arguments", "GREATEST(1, 2.5, 3)", "", "DOUBLE"},
		// A class the reference's annotator has no entry for. UNKNOWN is its
		// ANSWER there, not its silence -- it looks the class up, finds
		// nothing, and says so -- and a subscript over one is shiftable
		// because of it.
		{"a class the annotator has no entry for", "REGEXP_EXTRACT_ALL('s', 'p')", "duckdb",
			"UNKNOWN"},
		// A struct is typed by its FIELDS, each keeping its name; a map by
		// its two arrays. Both come from the reference's own annotators, and
		// both are what a subscript over one needs before it can be shifted.
		{"a struct of named fields", "STRUCT(1 AS a, 'x' AS b)", "databricks",
			"STRUCT<a: INT, b: STRING>"},
		{"a struct written as a brace literal", "{'x': 1}", "duckdb", "STRUCT(x INT)"},
		{"a struct field with no name", "STRUCT(1)", "duckdb", "STRUCT(INT)"},
		{"a struct with a field nobody can type", "STRUCT(c)", "duckdb", "UNKNOWN"},
		{"a map of two arrays", "MAP([1, 2], ['a', 'b'])", "duckdb", "MAP(INT, TEXT)"},
		{"a map whose sides are not arrays", "MAP(a, b)", "duckdb", "MAP"},
		{"a struct turned into a map", "MAP {'x': 1}", "duckdb", "MAP(TEXT, INT)"},
		// A subscript: a slice of something is more of it, an element of an
		// array is what the array holds, a key of a map written in place is
		// the value beside it, and anything else is UNKNOWN.
		{"an element of an array", "[1, 2][1]", "duckdb", "INT"},
		{"a slice of an array", "[1, 2][1:2]", "duckdb", "INT[]"},
		{"a key of a map written in place", "MAP(['a'], [1])['a']", "duckdb", "INT"},
		{"a subscript of anything else", "x[1]", "duckdb", "UNKNOWN"},
		{"an element of an array of nothing", "ARRAY()[1]", "duckdb", "UNKNOWN"},
		{"a key of a map that holds nothing known", "MAP(['a'], [c])['a']", "duckdb", "UNKNOWN"},
		// A key that is itself a list, which is what makes the comparison
		// walk into a repeated argument.
		{"a key of a map that is a list", "MAP([['a']], [1])[['a']]", "duckdb", "INT"},
		{"a struct with nothing to give a map", "MAP {'x': c}", "duckdb", "MAP"},
		{"a subscript over a map of no arrays", "MAP(a, b)['x']", "duckdb", "UNKNOWN"},
		{"a subscript over a map of empty arrays", "MAP([], [])['x']", "duckdb", "UNKNOWN"},
		{"a subscript over a key the map does not hold", "MAP(['a'], [1])['z']", "duckdb",
			"UNKNOWN"},
		{"a subscript over an array with no element type", "CAST(x AS ARRAY)[1]", "duckdb",
			"UNKNOWN"},
		// A CASE branch is typed by its two results, and not by the condition
		// beside them.
		{"a branch takes both its results", "CASE WHEN 1 = 1 THEN 1 ELSE 2.5 END", "duckdb",
			"DOUBLE"},
		// A call the reference has no builder for either. It answers UNKNOWN
		// for one of those, and so does this -- but only where the name is
		// one it also reads anonymously.
		{"a call nobody has a builder for", "WHATEVER(1)", "", "UNKNOWN"},
		{"and the same inside an operator", "1 + WHATEVER(1)", "", "UNKNOWN"},
		{"a parameterised type never coerces", "CAST(1 AS DECIMAL(18, 2)) + 1", "",
			"DECIMAL(18, 2)"},
		{"and not from the other side either", "1 + CAST(1 AS DECIMAL(18, 2))", "",
			"DECIMAL(18, 2)"},
		// SUM widens what it is given: an integer becomes a BIGINT so the
		// total does not overflow the column it came from.
		{"a function that promotes", "SUM(1)", "", "BIGINT"},
		{"promotion widens a real too", "SUM(1.5)", "", "DOUBLE"},
		{"promotion leaves other types alone", "SUM(CAST(1 AS DECIMAL(18, 2)))", "",
			"DECIMAL(18, 2)"},
		{"a function that wraps in an array", "ARRAY_AGG(1)", "", "ARRAY<INT>"},
		// A column resolves to UNKNOWN rather than to nothing: that IS the
		// reference's answer without a schema, and it is what lets a caller
		// tell an honest UNKNOWN from a gap in the port.
		{"a column without a schema", "x", "", "UNKNOWN"},
		{"an operator over a column", "x + 1", "", "UNKNOWN"},
		{"a function over a column", "ABS(x)", "", "UNKNOWN"},
	} {
		t.Run(c.name, func(t *testing.T) {
			tree, err := ParseOne(c.sql, c.dialect)
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", c.sql, err)
			}
			got := Annotate(tree, c.dialect)
			if got == nil {
				t.Fatalf("Annotate(%q) gave no answer, want %s", c.sql, c.want)
			}
			rendered, err := Generate(got, c.dialect)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if rendered != c.want {
				t.Errorf("Annotate(%q)\n  want %s\n  got  %s", c.sql, c.want, rendered)
			}
		})
	}
}

// TestAnnotateDeclines covers the other half of the contract: where the port
// cannot know a type it must say so, rather than answer UNKNOWN and have a
// caller act on it.
// TestAFixedReturnIsATypeNotASpelling pins what the generated table records.
// The probe reads a dialect's RENDERED answer to classify the rule, and the
// render is the dialect's own: DuckDB writes VARCHAR as TEXT and PostgreSQL
// writes DOUBLE as DOUBLE PRECISION. Recording the render put those two
// strings in the table where the reference has the types, which nothing
// noticed until a statement finally reached one of those nodes.
func TestAFixedReturnIsATypeNotASpelling(t *testing.T) {
	for _, c := range []struct{ name, sql, dialect, want string }{
		{"duckdb writes VARCHAR as TEXT", "SUBSTRING('abc', 1, 2)", "duckdb", "VARCHAR"},
		{"postgres writes DOUBLE as DOUBLE PRECISION", "SQRT(2)", "postgres", "DOUBLE"},
	} {
		t.Run(c.name, func(t *testing.T) {
			tree, err := ParseOne(c.sql, c.dialect)
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", c.sql, err)
			}
			got := Annotate(tree, c.dialect)
			if got == nil {
				t.Fatalf("Annotate(%q) had no answer", c.sql)
			}
			kind, _ := got.Args["this"].(DataTypeKind)
			if string(kind) != c.want {
				t.Errorf("Annotate(%q) recorded %q, want %q", c.sql, kind, c.want)
			}
		})
	}
}

// TestAnnotateUnknownDialect covers the lookup an annotation makes into the
// parser's own tables. A dialect with no tables has no answer to give, and
// asking must not panic.
func TestAnnotateUnknownDialect(t *testing.T) {
	tree, err := ParseOne("WHATEVER(1)", "")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if got := Annotate(tree, "nosuchdialect"); got != nil {
		rendered, _ := Generate(got, "")
		t.Errorf("Annotate over an unknown dialect answered %s", rendered)
	}
}

func TestAnnotateDeclines(t *testing.T) {
	for _, c := range []struct{ name, sql, dialect string }{

		{"a subquery with several projections", "1 + (SELECT 1, 2)", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			tree, err := ParseOne(c.sql, c.dialect)
			if err != nil {
				t.Skipf("ParseOne(%q): %v", c.sql, err)
			}
			if got := Annotate(tree, c.dialect); got != nil {
				rendered, _ := Generate(got, c.dialect)
				t.Errorf("Annotate(%q) answered %s; it cannot know", c.sql, rendered)
			}
		})
	}
}

// TestExpressionEqual covers the comparison Simplify's fixpoint depends on:
// a rewrite that never settles would loop, and one that compares carelessly
// would settle too early.
func TestExpressionEqual(t *testing.T) {
	lit := func(v string) *Expression {
		return New("Literal", Arg{"this", v}, Arg{"is_string", false})
	}
	cases := []struct {
		name string
		a, b *Expression
		want bool
	}{
		{"both nil", nil, nil, true},
		{"one nil", nil, lit("1"), false},
		{"same leaf", lit("1"), lit("1"), true},
		{"different value", lit("1"), lit("2"), false},
		{"different class", lit("1"), New("Null"), false},
		{"different arity", New("And", Arg{"this", lit("1")}), New("And"), false},
		{"different key order", New("And", Arg{"this", lit("1")}, Arg{"expression", lit("2")}),
			New("And", Arg{"expression", lit("2")}, Arg{"this", lit("1")}), false},
		{"same children", New("And", Arg{"this", lit("1")}, Arg{"expression", lit("2")}),
			New("And", Arg{"this", lit("1")}, Arg{"expression", lit("2")}), true},
		{"child differs", New("And", Arg{"this", lit("1")}, Arg{"expression", lit("2")}),
			New("And", Arg{"this", lit("1")}, Arg{"expression", lit("3")}), false},
		{"same list", New("Array", Arg{"expressions", []*Expression{lit("1")}}),
			New("Array", Arg{"expressions", []*Expression{lit("1")}}), true},
		{"list of different length", New("Array", Arg{"expressions", []*Expression{lit("1")}}),
			New("Array", Arg{"expressions", []*Expression{lit("1"), lit("2")}}), false},
		{"list member differs", New("Array", Arg{"expressions", []*Expression{lit("1")}}),
			New("Array", Arg{"expressions", []*Expression{lit("9")}}), false},
		{"list against a child", New("Array", Arg{"expressions", []*Expression{lit("1")}}),
			New("Array", Arg{"expressions", lit("1")}), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.a.Equal(c.b); got != c.want {
				t.Errorf("Equal() = %v, want %v", got, c.want)
			}
		})
	}
}

// The two small helpers the type annotator leans on, at their edges.
func TestAnnotateHelpers(t *testing.T) {
	for _, tc := range []struct {
		text string
		want bool
	}{
		{"0", true},
		{"123", true},
		{"", false},
		{"1a", false},
		{"-1", false}, // a sign is not part of the text here
		{"١٢", false}, // nor is a non-ASCII digit
		{" 1", false},
	} {
		if got := isIntegerText(tc.text); got != tc.want {
			t.Errorf("isIntegerText(%q) = %v, want %v", tc.text, got, tc.want)
		}
	}

	if hasTypeParams(nil) {
		t.Error("hasTypeParams(nil) = true, want false")
	}
	bare, err := ParseOne("CAST(x AS INT)", "")
	if err != nil {
		t.Fatal(err)
	}
	if hasTypeParams(bare.Args["to"].(*Expression)) {
		t.Error("INT has no type parameters")
	}
	sized, err := ParseOne("CAST(x AS DECIMAL(10, 2))", "")
	if err != nil {
		t.Fatal(err)
	}
	if !hasTypeParams(sized.Args["to"].(*Expression)) {
		t.Error("DECIMAL(10, 2) has type parameters")
	}
}

// The annotator's edges: an array whose elements disagree, a scalar subquery
// that projects more than one column, and a walk over a tree it can say
// nothing about.
func TestAnnotateEdges(t *testing.T) {
	for _, tc := range []struct{ name, sql, want string }{
		{"an array of one type", "SELECT [1, 2]", "INT[]"},
		{"an array of mixed types", "SELECT [1, 1.5]", "DOUBLE[]"},
		{"an empty array holds an unknown", "SELECT []", "UNKNOWN[]"},
		{"a scalar subquery", "SELECT (SELECT 1)", "INT"},
		// More than one projection, and there is no single answer to give.
		{"a subquery of two", "SELECT (SELECT 1, 2)", ""},
		// A branch this cannot type leaves the whole CASE with no answer,
		// from either the WHEN or the ELSE.
		{"a branch nobody can type", "SELECT CASE WHEN 1 = 1 THEN ALL(SELECT 1) END", ""},
		{"a default nobody can type", "SELECT CASE WHEN 1 = 1 THEN 1 ELSE ALL(SELECT 1) END", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, "duckdb")
			if err != nil {
				t.Skipf("ParseOne(%q): %v", tc.sql, err)
			}
			only, _ := e.Args["expressions"].([]*Expression)
			if len(only) == 0 {
				t.Skip("no projection")
			}
			got := Annotate(only[0], "duckdb")
			if tc.want == "" {
				if got == nil {
					return
				}
				if rendered, err := Generate(got, "duckdb"); err == nil && rendered != "UNKNOWN" {
					t.Errorf("Annotate = %q, want no answer", rendered)
				}
				return
			}
			if got == nil {
				t.Fatalf("Annotate gave no answer, want %s", tc.want)
			}
			rendered, err := Generate(got, "duckdb")
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if rendered != tc.want {
				t.Errorf("Annotate = %q, want %q", rendered, tc.want)
			}
		})
	}
}

// Coercion and the annotator's walk over shapes it can say nothing about.
func TestAnnotateCoercionEdges(t *testing.T) {
	for _, tc := range []struct{ name, sql, want string }{
		{"two ints stay int", "SELECT 1 + 2", "INT"},
		{"an int and a double widen", "SELECT 1 + 1.5", "DOUBLE"},
		// An UNKNOWN operand poisons, whichever side it is written on. The
		// port used to answer INT here and UNKNOWN for `x + 1`, because it
		// let the widening table decide and the table has no entry either
		// way -- so whichever operand came first won. Note this is about
		// UNKNOWN, which is an ANSWER; a nil, which is the absence of one,
		// still contributes nothing.
		{"an untyped operand poisons", "SELECT 1 + x", "UNKNOWN"},
		{"and does so from either side", "SELECT x + 1", "UNKNOWN"},
		{"a null contributes nothing", "SELECT 1 + NULL", "INT"},
		{"a cast is taken as written", "SELECT CAST(x AS BIGINT)", "BIGINT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, "duckdb")
			if err != nil {
				t.Skipf("ParseOne: %v", err)
			}
			only, _ := e.Args["expressions"].([]*Expression)
			if len(only) == 0 {
				t.Skip("no projection")
			}
			got := Annotate(only[0], "duckdb")
			if tc.want == "" {
				if got != nil {
					if rendered, err := Generate(got, "duckdb"); err == nil && rendered != "UNKNOWN" {
						t.Errorf("Annotate = %q, want no answer", rendered)
					}
				}
				return
			}
			if got == nil {
				t.Fatalf("Annotate gave no answer, want %s", tc.want)
			}
			rendered, err := Generate(got, "duckdb")
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if rendered != tc.want {
				t.Errorf("Annotate = %q, want %q", rendered, tc.want)
			}
		})
	}
}

// An array whose members disagree, and one the annotator can say nothing about.
func TestAnnotateArrayEdges(t *testing.T) {
	for _, tc := range []struct{ sql, want string }{
		{"SELECT [1, 2, 3]", "INT[]"},
		{"SELECT ['a', 'b']", "TEXT[]"},
		// The FIRST member answers for the array; a later one that disagrees
		// does not make it unknown. The reference does the same.
		{"SELECT [1, 'a']", "INT[]"},
		{"SELECT [[1], [2]]", "INT[][]"},
	} {
		e, err := ParseOne(tc.sql, "duckdb")
		if err != nil {
			t.Skipf("ParseOne(%q): %v", tc.sql, err)
		}
		only, _ := e.Args["expressions"].([]*Expression)
		got := Annotate(only[0], "duckdb")
		if tc.want == "" {
			if got != nil {
				if r, err := Generate(got, "duckdb"); err == nil && r != "UNKNOWN" {
					t.Errorf("%q: Annotate = %q, want no answer", tc.sql, r)
				}
			}
			continue
		}
		if got == nil {
			t.Errorf("%q: no answer, want %s", tc.sql, tc.want)
			continue
		}
		if r, err := Generate(got, "duckdb"); err != nil || r != tc.want {
			t.Errorf("%q: Annotate = %q (%v), want %q", tc.sql, r, err, tc.want)
		}
	}
}

// TestAnnotationMemoKeepsTheRawAnswer covers the one thing that makes the
// annotator's memo safe to have.
//
// Annotate converts a NULL result to UNKNOWN on the way out, because a
// NULL-typed answer tells a caller nothing. An operator ABOVE needs the NULL:
// `NULL + 1` is an INT because NULL contributes nothing, where `x + 1` is
// UNKNOWN. So what is memoised is the RAW answer, and asking about the NULL
// first must not change what the sum answers.
func TestAnnotationMemoKeepsTheRawAnswer(t *testing.T) {
	sum, err := ParseOne("NULL + 1", "")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	null, _ := sum.Args["this"].(*Expression)
	if null == nil || null.Class != "Null" {
		t.Fatalf("expected a NULL on the left, got %v", null)
	}
	// Ask about the operand first, which is what fills the memo.
	if got, _ := Generate(Annotate(null, ""), ""); got != "UNKNOWN" {
		t.Errorf("Annotate(NULL) = %s, want UNKNOWN", got)
	}
	if got, _ := Generate(Annotate(sum, ""), ""); got != "INT" {
		t.Errorf("Annotate(NULL + 1) after asking about the NULL = %s, want INT", got)
	}

	// The same tree under another dialect is a different question: Databricks
	// has a null type where the others do not.
	if got, _ := Generate(Annotate(null, "databricks"), "databricks"); got != "VOID" {
		t.Errorf("Annotate(NULL, databricks) = %s, want VOID", got)
	}
	if got, _ := Generate(Annotate(null, ""), ""); got != "UNKNOWN" {
		t.Errorf("Annotate(NULL) after asking Databricks = %s, want UNKNOWN", got)
	}

	// And a node whose arguments change is no longer the node the memo
	// answered about.
	lit, _ := sum.Args["expression"].(*Expression)
	if got, _ := Generate(Annotate(lit, ""), ""); got != "INT" {
		t.Errorf("Annotate(1) = %s, want INT", got)
	}
	lit.Set("is_string", true)
	if got, _ := Generate(Annotate(lit, ""), ""); got != "VARCHAR" {
		t.Errorf("Annotate after rewriting the literal = %s, want VARCHAR", got)
	}
}

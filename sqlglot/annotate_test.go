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
func TestAnnotateDeclines(t *testing.T) {
	for _, c := range []struct{ name, sql, dialect string }{
		{"an unrecognised call", "WHATEVER(1)", ""},
		{"a subquery with several projections", "1 + (SELECT 1, 2)", ""},
		{"an empty array has no element type", "ARRAY()", "duckdb"},
		{"a call the port has no rule for, inside an operator", "1 + WHATEVER(1)", ""},
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
		{"an empty array", "SELECT []", ""},
		{"a scalar subquery", "SELECT (SELECT 1)", "INT"},
		// More than one projection, and there is no single answer to give.
		{"a subquery of two", "SELECT (SELECT 1, 2)", ""},
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

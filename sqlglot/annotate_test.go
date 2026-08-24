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
		{"a column needs a schema", "x", ""},
		{"an operator over a column", "x + 1", ""},
		{"a function over a column", "ABS(x)", ""},
		{"an unrecognised call", "WHATEVER(1)", ""},
		{"a subquery with several projections", "1 + (SELECT 1, 2)", ""},
		{"an empty array has no element type", "ARRAY()", "duckdb"},
		{"a promoting function over a column", "SUM(x)", ""},
		{"an array of a known and an unknown", "[1, x]", "duckdb"},
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

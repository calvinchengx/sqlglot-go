package sqlglot

import "testing"

// TestPropagateConstantsEndToEnd pins propagate_constants against values
// read directly from the reference (simplify(..., constant_propagation=True)):
// within a conjunction with no OR anywhere in it, a `column = literal`
// substitutes into every other occurrence of that column in scope.
func TestPropagateConstantsEndToEnd(t *testing.T) {
	for _, tc := range []struct{ name, sql, want string }{
		{"a column equated to another column takes its constant",
			"SELECT * WHERE x = 5 AND y = x", "SELECT * WHERE x = 5 AND y = 5"},
		{"the constant's own side may be written on either side of the EQ",
			"SELECT * WHERE 5 = x AND y = x", "SELECT * WHERE x = 5 AND y = 5"},
		{"a duplicate occurrence still only substitutes, it does not itself become a new source",
			"SELECT * WHERE x = 5 AND y = x AND y = x", "SELECT * WHERE x = 5 AND y = 5"},
		{"every other column referencing the source gets the same constant",
			"SELECT * WHERE x = 5 AND y = x AND z = x",
			"SELECT * WHERE x = 5 AND y = 5 AND z = 5"},
		{"OR is left alone entirely: propagation never runs under it",
			"SELECT * WHERE x = 5 OR y = x", "SELECT * WHERE x = 5 OR x = y"},
		{"an OR anywhere inside the conjunction blocks propagation for the whole AND",
			"SELECT * WHERE (x = 5 OR z = 1) AND y = x",
			"SELECT * WHERE (x = 5 OR z = 1) AND x = y"},
		{"a qualified column is a different key than the bare name",
			"SELECT * WHERE t.x = 5 AND y = x", "SELECT * WHERE t.x = 5 AND x = y"},
		{"the substituted literal folds further downstream (string concat)",
			"SELECT * WHERE t.x = 'a' AND y = CONCAT_WS('-', t.x, 'b')",
			"SELECT * WHERE t.x = 'a' AND y = 'a-b'"},
		{"the whole AND folds to FALSE once every operand is decided",
			"SELECT * WHERE x = 5 AND y = x AND y + 1 < 5", "SELECT * WHERE FALSE"},
		{"a chain of equalities propagates the same constant down the whole chain",
			"SELECT * WHERE t1.a = 39 AND t2.b = t1.a AND t3.c = t2.b",
			"SELECT * WHERE t1.a = 39 AND t2.b = 39 AND t3.c = 39"},
		{"a subquery is its own scope: propagation never reaches inside it",
			"SELECT * WHERE x = 1 AND x = y AND (SELECT z FROM t WHERE a AND (b OR c))",
			"SELECT * WHERE (SELECT z FROM t WHERE a AND (b OR c)) AND x = 1 AND y = 1"},
		{"a CASE's own condition is skipped when the mapping is built, but its result is not exempt from substitution",
			"SELECT * WHERE x = 1 AND CASE WHEN y = 5 THEN x = z END",
			"SELECT * WHERE CASE WHEN y = 5 THEN z = 1 END AND x = 1"},
		{"a simple CASE's own subject is a column too, and folds the same way once rewritten",
			"SELECT * WHERE x = 1 AND CASE x WHEN 5 THEN FALSE ELSE TRUE END",
			"SELECT * WHERE x = 1"},
		{"a column under IS NULL keeps its own spelling, not the literal",
			"SELECT * WHERE x = 5 AND x IS NULL", "SELECT * WHERE x = 5 AND x IS NULL"},
		{"the same value twice is not a contradiction",
			"SELECT * WHERE x = 5 AND x = 5", "SELECT * WHERE x = 5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, "")
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			got, err := Generate(Simplify(e, ""), "")
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("Simplify(%q)\n  want %s\n  got  %s", tc.sql, tc.want, got)
			}
			if _, err := ParseOne(got, ""); err != nil {
				t.Fatalf("the fold's own output %q does not parse back: %v", got, err)
			}
		})
	}
}

func TestScopeContainsOr(t *testing.T) {
	for _, tc := range []struct {
		sql  string
		want bool
	}{
		{"SELECT * WHERE x = 5 AND y = 1", false},
		{"SELECT * WHERE (x = 5 OR z = 1) AND y = 1", true},
		{"SELECT * WHERE x = 5 AND (SELECT 1 WHERE a OR b)", false},
	} {
		t.Run(tc.sql, func(t *testing.T) {
			e, err := ParseOne(tc.sql, "")
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			and := e.FindAll("And")
			if len(and) == 0 {
				t.Fatalf("no And found in %q", tc.sql)
			}
			if got := scopeContainsOr(and[0]); got != tc.want {
				t.Errorf("scopeContainsOr(%q) = %v, want %v", tc.sql, got, tc.want)
			}
		})
	}
}

func TestPropagateConstantsIgnoresNonAnd(t *testing.T) {
	e, err := ParseOne("SELECT * WHERE x = 5 OR y = 1", "")
	if err != nil {
		t.Fatal(err)
	}
	or := e.FindAll("Or")[0]
	if got := propagateConstants(or, nil); got != or {
		t.Errorf("propagateConstants(Or) should return the node unchanged")
	}
}

func TestWalkScopeSkipsNilChildren(t *testing.T) {
	lit := New("Boolean", Arg{"this", true})
	list := []*Expression{lit, nil}
	e := New("Array")
	e.Args["expressions"] = list
	e.Keys = append(e.Keys, "expressions")
	visited := 0
	walkScope(e, nil, func(_, child *Expression, _ func(*Expression)) {
		visited++
	})
	if visited != 1 {
		t.Errorf("walkScope visited %d children, want 1 (the nil element skipped)", visited)
	}
}

func TestPropagateConstantsDeclinesNestedAnd(t *testing.T) {
	// A non-root And whose own parent is also And is what flattening merges
	// away; propagateConstants must not do redundant work on it.
	inner := New("And",
		Arg{"this", New("EQ",
			Arg{"this", New("Column", Arg{"this", New("Identifier", Arg{"this", "x"})})},
			Arg{"expression", New("Literal", Arg{"this", "5"})})},
		Arg{"expression", New("Column", Arg{"this", New("Identifier", Arg{"this", "y"})})})
	outer := New("And", Arg{"this", inner}, Arg{"expression", New("Boolean", Arg{"this", true})})
	if got := propagateConstants(inner, outer); got != inner {
		t.Errorf("propagateConstants on a nested And should return it unchanged")
	}
}

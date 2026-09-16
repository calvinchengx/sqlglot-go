package sqlglot

import "testing"

// TestAbsorbSupersetsEndToEnd pins absorb_and_eliminate's subset-of-a-
// flattened-chain rule: an AND of two ORs where one's own members are a
// proper subset of the other's makes the larger OR redundant (and
// symmetrically for OR of ANDs), because whichever operand decides the
// outer connector, the smaller one already does. Every value here is read
// directly from the reference.
func TestAbsorbSupersetsEndToEnd(t *testing.T) {
	for _, tc := range []struct{ name, sql, want string }{
		{"a redundant OR nested directly in the other",
			"SELECT * WHERE (A OR C) AND ((A OR C) OR B)", "SELECT * WHERE A OR C"},
		{"the same, written as one flat OR instead of a nested one",
			"SELECT * WHERE (A OR C) AND (A OR B OR C)", "SELECT * WHERE A OR C"},
		{"a chain of three ORs where only the largest is redundant",
			"SELECT * WHERE (A OR B) AND (A OR C) AND (A OR B OR C)",
			"SELECT * WHERE (A OR B) AND (A OR C)"},
		{"the OR-of-ANDs dual",
			"SELECT * WHERE (A AND B) OR (A AND B AND C)", "SELECT * WHERE A AND B"},
		{"a chain of three ANDs where only the largest is redundant",
			"SELECT * WHERE (A AND B) OR (A AND B AND C) OR (A AND D)",
			"SELECT * WHERE (A AND B) OR (A AND D)"},
		{"a bare operand is the size-1 case of the same rule (pre-existing, unaffected)",
			"SELECT * WHERE A AND (A OR B)", "SELECT * WHERE A AND TRUE"},
		{"the OR-of-ANDs version of the size-1 case",
			"SELECT * WHERE A OR (A AND B)", "SELECT * WHERE A AND TRUE"},
		{"two structurally equal ORs dedup instead (pre-existing, unaffected)",
			"SELECT * WHERE (A OR B) AND (A OR B)", "SELECT * WHERE A OR B"},
		{"disjoint ORs are left alone: neither's members are a subset of the other's",
			"SELECT * WHERE (A OR B) AND (C OR D)", "SELECT * WHERE (A OR B) AND (C OR D)"},
		{"a longer OR that is not actually a superset of the shorter one is left alone",
			"SELECT * WHERE (A OR B OR C) AND (D OR E)", "SELECT * WHERE (A OR B OR C) AND (D OR E)"},
		{"a full comparison, not just a bare identifier, is a valid subset member",
			"SELECT * WHERE x = 1 AND (x = 1 OR y = 2)", "SELECT * WHERE x = 1"},
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

func TestIsProperSubset(t *testing.T) {
	a := New("Column", Arg{"this", New("Identifier", Arg{"this", "a"})})
	b := New("Column", Arg{"this", New("Identifier", Arg{"this", "b"})})
	c := New("Column", Arg{"this", New("Identifier", Arg{"this", "c"})})

	for _, tc := range []struct {
		name string
		a, b []*Expression
		want bool
	}{
		{"a proper subset", []*Expression{a}, []*Expression{a, b}, true},
		{"equal sets are not proper", []*Expression{a, b}, []*Expression{a, b}, false},
		{"a larger set is never a subset of a smaller one", []*Expression{a, b, c}, []*Expression{a, b}, false},
		{"disjoint sets", []*Expression{c}, []*Expression{a, b}, false},
		{"empty is a proper subset of anything nonempty", []*Expression{}, []*Expression{a}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isProperSubset(tc.a, tc.b); got != tc.want {
				t.Errorf("isProperSubset(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestAbsorbSupersetsIgnoresFewerThanTwoOperands(t *testing.T) {
	// absorb's own len(ops) < 2 guard means absorbSupersets never sees a
	// single-operand chain in practice; called directly, it still must not
	// panic or fabricate a change out of nothing to compare against.
	a := New("Column", Arg{"this", New("Identifier", Arg{"this", "a"})})
	if got := absorbSupersets("And", "Or", []*Expression{a}, nil); got != nil {
		t.Errorf("absorbSupersets with one operand should find nothing to absorb, got %v", got)
	}
}

package sqlglot

import "testing"

// TestEliminateComplementPairsEndToEnd pins absorb_and_eliminate's own
// elimination half: `(A AND B) OR (A AND NOT B)` is `A`, provided B cannot
// be NULL -- whichever way B goes, the OR is decided by A alone. The
// OR-of-ANDs case flips to AND-of-ORs the same way absorbSupersets does.
// Every value here is read directly from the reference (with proper type
// annotation and schema, matching how the fixture itself is generated --
// without it, the reference's OWN "nonnull" meta flag never gets set and
// nothing here would eliminate at all, an easy trap when checking these by
// hand).
func TestEliminateComplementPairsEndToEnd(t *testing.T) {
	for _, tc := range []struct{ name, sql, want string }{
		{"an IS NULL check is known non-null itself, so its complement eliminates",
			"SELECT * WHERE (B AND x IS NULL) OR (B AND NOT (x IS NULL))",
			"SELECT * WHERE B AND TRUE"},
		{"the OR-of-ANDs elimination order does not matter",
			"SELECT * WHERE (B AND NOT (x IS NULL)) OR (B AND x IS NULL)",
			"SELECT * WHERE B AND TRUE"},
		{"the AND-of-ORs dual",
			"SELECT * WHERE (B OR x IS NULL) AND (B OR NOT (x IS NULL))",
			"SELECT * WHERE B AND TRUE"},
		{"a third, unrelated operand survives untouched",
			"SELECT * WHERE (B AND x IS NULL) OR (B AND NOT (x IS NULL)) OR C",
			"SELECT * WHERE B OR C"},
		{"a bare column is NOT known non-null, so nothing eliminates",
			"SELECT * WHERE (A AND B) OR (A AND NOT B)",
			"SELECT * WHERE (A AND B) OR (A AND NOT B)"},
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

func TestComplementaryPair(t *testing.T) {
	a := New("Column", Arg{"this", New("Identifier", Arg{"this", "a"})})
	isNull := New("Is",
		Arg{"this", New("Column", Arg{"this", New("Identifier", Arg{"this", "x"})})},
		Arg{"expression", New("Null")})
	notIsNull := New("Not", Arg{"this", isNull})

	if _, ok := complementaryPair(a, isNull, a, notIsNull); !ok {
		t.Errorf("complementaryPair(a & isNull, a & NOT isNull) should match")
	}
	if _, ok := complementaryPair(isNull, a, a, notIsNull); !ok {
		t.Errorf("complementaryPair should match regardless of which side the common operand is on")
	}
	if _, ok := complementaryPair(a, isNull, a, isNull); ok {
		t.Errorf("complementaryPair(a & isNull, a & isNull) should not match: neither side is negated")
	}
	b := New("Column", Arg{"this", New("Identifier", Arg{"this", "b"})})
	notB := New("Not", Arg{"this", b})
	if _, ok := complementaryPair(a, b, a, notB); ok {
		t.Errorf("complementaryPair(a & b, a & NOT b) should not match: b is not known non-null")
	}
}

func TestEliminateComplementPairsIgnoresFewerThanTwoOperands(t *testing.T) {
	a := New("Column", Arg{"this", New("Identifier", Arg{"this", "a"})})
	if got := eliminateComplementPairs("Or", "And", []*Expression{a}, nil); got != nil {
		t.Errorf("eliminateComplementPairs with one operand should find nothing, got %v", got)
	}
}

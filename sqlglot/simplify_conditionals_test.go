package sqlglot

import "testing"

// TestSimplifyConditionalsEndToEnd pins simplify_conditionals against values
// read directly from the reference: folding a CASE or standalone IF whose
// condition is already known, and rewriting a simple CASE (`CASE x WHEN
// y ...`) into a searched one (`CASE WHEN x = y ...`).
func TestSimplifyConditionalsEndToEnd(t *testing.T) {
	for _, tc := range []struct{ name, sql, want string }{
		{"IF with a false condition and no false arm", "SELECT IF(FALSE, x)", "SELECT NULL"},
		{"IF with a null condition and no false arm", "SELECT IF(NULL, x)", "SELECT NULL"},
		{"IF with a true condition", "SELECT IF(TRUE, x, y)", "SELECT x"},
		{"IF with a false condition", "SELECT IF(FALSE, x, y)", "SELECT y"},
		{"IF with a null condition", "SELECT IF(NULL, x, y)", "SELECT y"},
		{"a standalone IF nested in a CASE branch's own result still folds",
			"SELECT CASE WHEN x > 1 THEN IF(TRUE, a, b) END",
			"SELECT CASE WHEN x > 1 THEN a END"},
		{"searched CASE, true branch wins", "SELECT CASE WHEN TRUE THEN x ELSE y END", "SELECT x"},
		{"searched CASE, false branch drops, default remains",
			"SELECT CASE WHEN FALSE THEN x ELSE y END", "SELECT y"},
		{"searched CASE, every branch false and no default",
			"SELECT CASE WHEN FALSE THEN x END", "SELECT NULL"},
		{"searched CASE, several false branches drop before a true one wins",
			"SELECT CASE WHEN FALSE THEN x WHEN FALSE THEN y WHEN TRUE THEN z END", "SELECT z"},
		{"searched CASE, every branch false, no default, more than one branch",
			"SELECT CASE WHEN FALSE THEN x WHEN FALSE THEN y END", "SELECT NULL"},
		{"searched CASE, a middle branch drops but the rest stay",
			"SELECT CASE WHEN FALSE THEN x WHEN y = 1 THEN z ELSE w END",
			"SELECT CASE WHEN y = 1 THEN z ELSE w END"},
		{"simple CASE converts to a searched one", "SELECT CASE x WHEN y THEN z ELSE w END",
			"SELECT CASE WHEN x = y THEN z ELSE w END"},
		{"simple CASE with no default", "SELECT CASE x WHEN y THEN z END",
			"SELECT CASE WHEN x = y THEN z END"},
		{"simple CASE, several branches each compare against the same subject",
			"SELECT CASE x WHEN 1 THEN a WHEN 2 THEN b END",
			"SELECT CASE WHEN x = 1 THEN a WHEN x = 2 THEN b END"},
		{"simple CASE, a repeated WHEN value is not deduplicated by this rule",
			"SELECT CASE x WHEN 1 THEN a WHEN 2 THEN b WHEN 1 THEN c END",
			"SELECT CASE WHEN x = 1 THEN a WHEN x = 2 THEN b WHEN x = 1 THEN c END"},
		{"simple CASE, a literal subject decides every branch by itself",
			"SELECT CASE 4 WHEN 1 THEN x WHEN 2 THEN y WHEN 3 THEN z ELSE w END", "SELECT w"},
		{"simple CASE, a literal subject matches the last branch",
			"SELECT CASE 4 WHEN 1 THEN x WHEN 2 THEN y WHEN 3 THEN z WHEN 4 THEN w END",
			"SELECT w"},
		{"simple CASE, a folded WHEN value still compares",
			"SELECT CASE 1 WHEN 1 + 1 THEN x END", "SELECT NULL"},
		{"simple CASE, NULL subject against a NULL WHEN value never matches",
			"SELECT CASE NULL WHEN NULL THEN x ELSE y END", "SELECT y"},
		{"simple CASE, a Binary subject and WHEN value are each parenthesized",
			"SELECT CASE x1 + x2 WHEN x3 THEN x4 WHEN x5 + x6 THEN x7 ELSE x8 END",
			"SELECT CASE WHEN x3 = (x1 + x2) THEN x4 WHEN (x1 + x2) = (x5 + x6) THEN x7 ELSE x8 END"},
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

// TestSimplifyConditionalsLeavesUnknownConditionsAlone covers the shapes the
// rule must decline: a condition that cannot be decided, and a comparison
// with a NULL operand OUTSIDE of an IF's own condition position, which must
// keep its literal NULL spelling rather than being folded away.
func TestSimplifyConditionalsLeavesUnknownConditionsAlone(t *testing.T) {
	for _, sql := range []string{
		"SELECT IF(x > 1, a, b)",
		"SELECT CASE WHEN x > 1 THEN a ELSE b END",
		"SELECT x = NULL",
		"SELECT NULL = NULL",
	} {
		t.Run(sql, func(t *testing.T) {
			e, err := ParseOne(sql, "")
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", sql, err)
			}
			simplified := Simplify(e, "")
			want, _ := Generate(e, "")
			got, err := Generate(simplified, "")
			if err != nil {
				// IF with 3 args has no generator writer of its own yet in
				// the base dialect (a separate, already-flagged gap); check
				// the tree directly rather than round-tripping through it.
				if !simplified.Equal(e) {
					t.Errorf("Simplify(%q) changed the tree; it should have stayed the same", sql)
				}
				return
			}
			if got != want {
				t.Errorf("Simplify(%q) folded to %q; it should have stayed %q", sql, got, want)
			}
		})
	}
}

func TestWrapBinary(t *testing.T) {
	add := New("Add", Arg{"this", New("Column", Arg{"this", New("Identifier", Arg{"this", "x"})})},
		Arg{"expression", New("Literal", Arg{"this", "1"})})
	if wrapped := wrapBinary(add); wrapped.Class != "Paren" {
		t.Errorf("wrapBinary(Add) = %s, want Paren", wrapped.Class)
	}

	col := New("Column", Arg{"this", New("Identifier", Arg{"this", "x"})})
	if wrapped := wrapBinary(col); wrapped != col {
		t.Errorf("wrapBinary(Column) should return the same node unchanged")
	}
}

func TestGenGreater(t *testing.T) {
	a := New("Column", Arg{"this", New("Identifier", Arg{"this", "a"})})
	z := New("Column", Arg{"this", New("Identifier", Arg{"this", "z"})})
	if genGreater(a, z, "") {
		t.Errorf("genGreater(a, z) = true, want false")
	}
	if !genGreater(z, a, "") {
		t.Errorf("genGreater(z, a) = false, want true")
	}
	if genGreater(a, a, "") {
		t.Errorf("genGreater(a, a) = true, want false (equal sorts as not-greater)")
	}
}

func TestSortComparisonGenTiebreak(t *testing.T) {
	// Neither operand is a Column or a constant: the tiebreak decides by
	// each side's own generated SQL, matching the reference's `gen(l) >
	// gen(r)` exactly -- this is what lets `x3 = (x1 + x2)` and
	// `(x1 + x2) = (x5 + x6)` land in the order simplify_conditionals'
	// own end-to-end test pins.
	left := New("Paren", Arg{"this", New("Add",
		Arg{"this", New("Column", Arg{"this", New("Identifier", Arg{"this", "x5"})})},
		Arg{"expression", New("Column", Arg{"this", New("Identifier", Arg{"this", "x6"})})})})
	right := New("Paren", Arg{"this", New("Add",
		Arg{"this", New("Column", Arg{"this", New("Identifier", Arg{"this", "x1"})})},
		Arg{"expression", New("Column", Arg{"this", New("Identifier", Arg{"this", "x2"})})})})
	eq := New("EQ", Arg{"this", left}, Arg{"expression", right})
	got := sortComparison(eq, "")
	want := "(x1 + x2) = (x5 + x6)"
	sql, err := Generate(got, "")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if sql != want {
		t.Errorf("sortComparison swapped by gen() = %q, want %q", sql, want)
	}
}

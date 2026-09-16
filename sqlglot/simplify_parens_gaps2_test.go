package sqlglot

import "testing"

// TestSimplifyParensFurtherGaps pins two more simplifyParens gaps: a Paren
// wrapping a Unary (Neg) directly under a clause wrapper (WHERE, the top of
// a SELECT list, ...), and a Paren wrapping an atomic operand (a qualified
// Column, here) directly under a comparison. Every value here is read
// directly from the reference.
func TestSimplifyParensFurtherGaps(t *testing.T) {
	for _, tc := range []struct{ name, sql, want string }{
		{"a Neg at the top of a SELECT list drops its own redundant parens",
			"SELECT (-((x.a) IS NULL)) FROM x", "SELECT -(x.a IS NULL) FROM x"},
		{"a qualified column under IS drops its own parens",
			"SELECT (x.a) = 1 FROM x", "SELECT x.a = 1 FROM x"},
		{"ANY's own parens are part of its call syntax, not redundant grouping",
			"ANY(t.value)", "ANY(t.value)"},
		{"ALL's the same", "ALL(t.value)", "ALL(t.value)"},
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

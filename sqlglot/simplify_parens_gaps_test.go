package sqlglot

import "testing"

// TestSimplifyParensGaps pins two small parenthesization gaps, each fixed
// separately: a Paren directly wrapping a Neg (arithmetic double negation
// written with explicit parens rather than a bare `--`), and a Paren
// directly wrapping a Not under another Not (NOT binds tighter than NOT
// itself needs protecting from, the same as it does under a comparison).
// Every value here is read directly from the reference.
func TestSimplifyParensGaps(t *testing.T) {
	for _, tc := range []struct{ name, sql, want string }{
		{"a parenthesised double negation folds the same as a bare one",
			"SELECT -(-1)", "SELECT 1"},
		{"a parenthesised NOT under NOT drops its own parens",
			"SELECT NOT(NOT(a)) FROM x", "SELECT NOT NOT a FROM x"},
		{"a third NOT collapses one pair, same as a schema-annotated reference run does",
			"SELECT NOT(NOT(NOT(a))) FROM x", "SELECT NOT a FROM x"},
		{"a NOT joining a connector keeps its own operand's parens dropped too",
			"SELECT * WHERE x AND (NOT y)", "SELECT * WHERE NOT y AND x"},
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

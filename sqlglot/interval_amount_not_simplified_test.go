package sqlglot

import "testing"

// TestIntervalAmountIsNeverSimplified pins the reference's own limitation:
// an INTERVAL's own amount is never visited by its traversal at all, since
// exp.Interval is not one of the classes (Binary, Func, Lambda, Predicate,
// Unary) it descends into -- so `INTERVAL (5 - 2) DAY` stays exactly that,
// never folding to `INTERVAL 3 DAY` the way `5 - 2` would fold anywhere
// else. Folding it anyway produced a signed bare number some dialects'
// grammar cannot read back (`INTERVAL -15 MONTH`), found by the execution
// oracle. Every value here is read directly from the reference.
func TestIntervalAmountIsNeverSimplified(t *testing.T) {
	for _, tc := range []struct{ name, sql, want string }{
		{"a plain arithmetic amount stays unfolded",
			"SELECT x + INTERVAL (5 - 2) DAY", "SELECT x + INTERVAL (5 - 2) DAY"},
		{"a negative amount stays unfolded rather than writing a bare signed number",
			"SELECT CAST(MAKE_DATE(2026, 1, 1) + INTERVAL (0 - 1) MONTH + INTERVAL (0 - 1) DAY AS DATE)",
			"SELECT CAST(MAKE_DATE(2026, 1, 1) + INTERVAL (0 - 1) MONTH + INTERVAL (0 - 1) DAY AS DATE)"},
		{"a larger negative amount, the same way",
			"SELECT CAST(MAKE_DATE(2026, 1, 1) + INTERVAL (-14 - 1) MONTH + INTERVAL (-32 - 1) DAY AS DATE)",
			"SELECT CAST(MAKE_DATE(2026, 1, 1) + INTERVAL (-14 - 1) MONTH + INTERVAL (-32 - 1) DAY AS DATE)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, "duckdb")
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			got, err := Generate(Simplify(e, "duckdb"), "duckdb")
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("Simplify(%q)\n  want %s\n  got  %s", tc.sql, tc.want, got)
			}
			if _, err := ParseOne(got, "duckdb"); err != nil {
				t.Fatalf("the fold's own output %q does not parse back: %v", got, err)
			}
		})
	}
}

// TestIntervalAmountGuardDoesNotBlockDateArithmetic covers the shape the
// guard must NOT interfere with: a plain literal INTERVAL amount, already
// foldable as written, still moves across a comparison via
// simplify_equality the same way it did before this guard existed --
// intervalAmount reads a literal directly, it never needed the amount to
// be recursively simplified first.
func TestIntervalAmountGuardDoesNotBlockDateArithmetic(t *testing.T) {
	sql := "SELECT * WHERE x - INTERVAL 1 DAY = CAST('2021-01-01' AS DATE)"
	want := "SELECT * WHERE x = CAST('2021-01-02' AS DATE)"
	e, err := ParseOne(sql, "")
	if err != nil {
		t.Fatalf("ParseOne(%q): %v", sql, err)
	}
	got, err := Generate(Simplify(e, ""), "")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got != want {
		t.Errorf("Simplify(%q)\n  want %s\n  got  %s", sql, want, got)
	}
}

package sqlglot

import (
	"testing"
	"time"
)

// TestDateLiteralComparisonEndToEnd extends the port's literal-comparison
// folding -- already covering two numbers or two strings -- to two date or
// datetime literals: whichever CAST each side names, the calendar value
// underneath decides the comparison, including across a DATE and a
// DATETIME naming the same instant. Every value here is read directly from
// the reference.
func TestDateLiteralComparisonEndToEnd(t *testing.T) {
	for _, tc := range []struct{ name, sql, want string }{
		{"two DATE literals on the same day are equal",
			"SELECT CAST('2023-01-01' AS DATE) = CAST('2023-01-01' AS DATE)", "SELECT TRUE"},
		{"two DATE literals on different days are not equal",
			"SELECT CAST('2023-01-01' AS DATE) = CAST('2023-01-02' AS DATE)", "SELECT FALSE"},
		{"NEQ of two equal dates declines to TRUE, not folded to FALSE-shaped default",
			"SELECT * WHERE CAST('2023-01-01' AS DATE) <> CAST('2023-01-02' AS DATE)", "SELECT *"},
		{"LT compares the two dates directly",
			"SELECT * WHERE CAST('2023-01-01' AS DATE) < CAST('2023-01-02' AS DATE)", "SELECT *"},
		{"LTE the other direction folds to FALSE",
			"SELECT * WHERE CAST('2023-01-02' AS DATE) <= CAST('2023-01-01' AS DATE)", "SELECT * WHERE FALSE"},
		{"a DATE and a DATETIME naming the same instant compare equal across the shared-operand rule",
			"SELECT * WHERE x > CAST('2023-01-01' AS DATE) AND x < CAST('2023-01-01' AS DATETIME)",
			"SELECT * WHERE FALSE"},
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

// TestDateLiteralComparisonDeclinesEqEq covers the one shape this rule must
// still leave alone: two EQs against different date constants for the same
// column. This is the pre-existing EQ-vs-EQ gap (flagged separately,
// task_c6d0d18b) that also applies to numbers -- folding it to FALSE would
// be wrong when the column is NULL.
func TestDateLiteralComparisonDeclinesEqEq(t *testing.T) {
	sql := "SELECT * WHERE x = CAST('2023-01-01' AS DATE) AND x = CAST('2023-01-02' AS DATE)"
	e, err := ParseOne(sql, "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := Generate(Simplify(e, ""), "")
	if err != nil {
		t.Fatal(err)
	}
	if got != sql {
		t.Errorf("Simplify(%q) = %q, want it left unchanged", sql, got)
	}
}

func TestEvalBooleanTime(t *testing.T) {
	early := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	late := time.Date(2023, 1, 2, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		class    string
		a, b     time.Time
		want     bool
		wantsNil bool
	}{
		{"EQ", early, early, true, false},
		{"EQ", early, late, false, false},
		{"NEQ", early, late, true, false},
		{"GT", late, early, true, false},
		{"GT", early, late, false, false},
		{"GTE", early, early, true, false},
		{"LT", early, late, true, false},
		{"LTE", late, late, true, false},
		{"Is", early, late, false, true},
	} {
		got := evalBooleanTime(tc.class, tc.a, tc.b)
		if tc.wantsNil {
			if got != nil {
				t.Errorf("evalBooleanTime(%q, ...) = %v, want nil", tc.class, got)
			}
			continue
		}
		if got == nil {
			t.Fatalf("evalBooleanTime(%q, ...) = nil, want a Boolean", tc.class)
		}
		v, _ := got.Args["this"].(bool)
		if v != tc.want {
			t.Errorf("evalBooleanTime(%q, %v, %v) = %v, want %v", tc.class, tc.a, tc.b, v, tc.want)
		}
	}
}

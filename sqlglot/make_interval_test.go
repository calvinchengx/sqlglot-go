package sqlglot

import "testing"

// MAKE_INTERVAL exists only in PostgreSQL, which keeps the call; the others
// build an INTERVAL out of it, and each spells that its own way. Every value
// here is the reference's own output.
func TestMakeIntervalWriters(t *testing.T) {
	for _, c := range [][3]string{
		// PostgreSQL keeps the call, arguments in the class's own order.
		{"postgres", "SELECT MAKE_INTERVAL(1, 2, 3, 4, 5, 6, 7.5)",
			"SELECT MAKE_INTERVAL(1, 2, 3, 4, 5, 6, 7.5)"},
		{"postgres", "SELECT MAKE_INTERVAL(days => 3)", "SELECT MAKE_INTERVAL(days => 3)"},
		{"postgres", "SELECT MAKE_INTERVAL(1, hours => 2, mins => 30)",
			"SELECT MAKE_INTERVAL(1, hours => 2, mins => 30)"},
		// DuckDB joins the parts with a space, inside one string.
		{"duckdb", "SELECT MAKE_INTERVAL(years => 1, months => 2)",
			"SELECT INTERVAL '1 year 2 month'"},
		{"duckdb", "SELECT MAKE_INTERVAL(days => 3)", "SELECT INTERVAL '3 year'"},
		// Snowflake joins them with a comma.
		{"snowflake", "SELECT MAKE_INTERVAL(years => 1, months => 2)",
			"SELECT INTERVAL '1 year, 2 month'"},
		// MySQL writes one INTERVAL per part, added together -- and subtracted
		// where the call is the right-hand side of a SUB.
		{"mysql", "SELECT MAKE_INTERVAL(years => 1, months => 2)",
			"SELECT INTERVAL 1 YEAR + INTERVAL 2 MONTH"},
		{"mysql", "SELECT MAKE_INTERVAL(days => 3)", "SELECT INTERVAL 3 DAY"},
		{"mysql", "SELECT x - MAKE_INTERVAL(days => 3)", "SELECT x - INTERVAL 3 DAY"},
	} {
		tree, err := ParseOne(c[1], c[0])
		if err != nil {
			t.Errorf("[%s] ParseOne(%q): %v", c[0], c[1], err)
			continue
		}
		got, err := Generate(tree, c[0])
		if err != nil {
			t.Errorf("[%s] Generate(%q): %v", c[0], c[1], err)
			continue
		}
		if got != c[2] {
			t.Errorf("[%s] %s\n  want %s\n  got  %s", c[0], c[1], c[2], got)
		}
	}
}

// SQLite has no DATE_ADD at all: a shift is DATE(x, '<modifier>'), with the
// amount and the unit in one string.
func TestSQLiteDateAddWritesAModifier(t *testing.T) {
	for _, c := range [][2]string{
		{"SELECT DATE_ADD(d, '1 day')", "SELECT DATE(d, '1 day')"},
		{"SELECT DATE_ADD(d, INTERVAL 1 DAY)", "SELECT DATE(d, '1 DAY')"},
		{"SELECT DATE_ADD(d, x, 'DAY')", "SELECT DATE(d, (x) || ' DAY')"},
		{"SELECT DATE_ADD(d, -1, 'DAY')", "SELECT DATE(d, '-1 DAY')"},
		{"SELECT DATE_ADD(d, 3)", "SELECT DATE(d, '3')"},
	} {
		tree, err := ParseOne(c[0], "sqlite")
		if err != nil {
			t.Errorf("ParseOne(%q): %v", c[0], err)
			continue
		}
		got, err := Generate(tree, "sqlite")
		if err != nil {
			t.Errorf("Generate(%q): %v", c[0], err)
			continue
		}
		if got != c[1] {
			t.Errorf("%s\n  want %s\n  got  %s", c[0], c[1], got)
		}
	}
}

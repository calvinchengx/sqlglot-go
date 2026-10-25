package sqlglot

import "testing"

// annotateTimeUnit and the ISO readers behind it: a date function keeps its
// operand's type for a date-component unit, answers DATETIME otherwise, and
// reads a STRING operand's own text as a date or a datetime
// (Parser._annotate_timeunit).
func TestAnnotateTimeUnit(t *testing.T) {
	for _, c := range []struct{ sql, dialect, want string }{
		{"DATE_ADD(x, INTERVAL 1 DAY)", "mysql", "UNKNOWN"},
		{"DATE_TRUNC('DAY', x)", "", "UNKNOWN"},
		{"DATE_TRUNC('MONTH', CAST(x AS DATE))", "", "DATE"},
		{"DATE_TRUNC('YEAR', CAST(x AS DATETIME))", "", "DATETIME"},
		{"DATE_TRUNC('DAY', '2020-01-01')", "", "DATE"},
		{"DATE_TRUNC('MONTH', '2020-01-01')", "", "DATE"},
		{"DATE_TRUNC('DAY', '2020-01-01 10:00:00')", "", "DATETIME"},
		{"DATE_TRUNC('DAY', 'not a date')", "", "UNKNOWN"},
	} {
		tree, err := ParseOne("SELECT "+c.sql, c.dialect)
		if err != nil {
			t.Errorf("ParseOne(%q): %v", c.sql, err)
			continue
		}
		exprs, _ := tree.Args["expressions"].([]*Expression)
		if len(exprs) != 1 {
			t.Errorf("%q did not parse to one projection", c.sql)
			continue
		}
		got := typeKind(Annotate(exprs[0], c.dialect))
		if got != c.want {
			t.Errorf("%q is %s, want %s", c.sql, got, c.want)
		}
	}
}

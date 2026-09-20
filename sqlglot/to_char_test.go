package sqlglot

import "testing"

// Presto's TO_CHAR is Teradata-compatible: the format is upper-cased and read
// through Teradata's mapping, into a TimeToStr.
func TestPrestoToChar(t *testing.T) {
	for _, c := range [][3]string{
		{"presto", "SELECT TO_CHAR(ts, 'yyyy-mm-dd hh24:mi:ss')", "SELECT DATE_FORMAT(ts, '%Y-%m-%d %T')"},
		{"presto", "SELECT TO_CHAR(ts, 'Dy')", "SELECT DATE_FORMAT(ts, '%eY')"},
		{"presto", "SELECT TO_CHAR(ts, fmt)", "SELECT DATE_FORMAT(ts, fmt)"},
		{"presto", "SELECT TO_CHAR(ts, 'dd', 'x')", "SELECT DATE_FORMAT(ts, '%d')"},
		{"presto", "SELECT TO_CHAR(ts, 'MMMM DD, YYYY')", "SELECT DATE_FORMAT(ts, '%M %d, %Y')"},
		{"presto", "SELECT TO_CHAR(1, 5)", "SELECT DATE_FORMAT(1, 5)"},
		{"presto", "SELECT TO_CHAR(ts, 'yyyy''mm')", "SELECT DATE_FORMAT(ts, '%Y''%m')"},
		{"trino", "SELECT TO_CHAR(ts, 'yyyy-mm-dd hh24:mi:ss')", "SELECT DATE_FORMAT(ts, '%Y-%m-%d %T')"},
		{"trino", "SELECT TO_CHAR(ts, 'Dy')", "SELECT DATE_FORMAT(ts, '%eY')"},
		{"trino", "SELECT TO_CHAR(ts, fmt)", "SELECT DATE_FORMAT(ts, fmt)"},
		{"trino", "SELECT TO_CHAR(ts, 'dd', 'x')", "SELECT DATE_FORMAT(ts, '%d')"},
		{"trino", "SELECT TO_CHAR(ts, 'MMMM DD, YYYY')", "SELECT DATE_FORMAT(ts, '%M %d, %Y')"},
		{"trino", "SELECT TO_CHAR(1, 5)", "SELECT DATE_FORMAT(1, 5)"},
		{"trino", "SELECT TO_CHAR(ts, 'yyyy''mm')", "SELECT DATE_FORMAT(ts, '%Y''%m')"},
	} {
		tree, err := ParseOne(c[1], c[0])
		if err != nil {
			t.Errorf("[%s] %s: %v", c[0], c[1], err)
			continue
		}
		got, err := Generate(tree, c[0])
		if err != nil {
			t.Errorf("[%s] %s: %v", c[0], c[1], err)
			continue
		}
		if got != c[2] {
			t.Errorf("[%s] %s\n  want %s\n  got  %s", c[0], c[1], c[2], got)
		}
	}
	for _, sql := range []string{"SELECT TO_CHAR(ts)", "SELECT TO_CHAR()"} {
		if _, err := ParseOne(sql, "presto"); err == nil {
			t.Errorf("%s: read a TO_CHAR with no format", sql)
		}
	}
}

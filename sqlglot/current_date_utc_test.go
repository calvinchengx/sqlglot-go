package sqlglot

import "testing"

// Dremio's bare CURRENT_DATE_UTC is today's date in UTC. The parenthesized
// form is the same tree, a name after a dot stays a column, and another
// dialect keeps the word as a column. A parse or generate error fails the row.
func TestCurrentDateUTC(t *testing.T) {
	cases := [][3]string{
		{"dremio", "SELECT CURRENT_DATE_UTC", "SELECT CURRENT_DATE_UTC"},
		{"dremio", "SELECT current_date_utc", "SELECT CURRENT_DATE_UTC"},
		{"dremio", "SELECT CURRENT_DATE_UTC()", "SELECT CURRENT_DATE_UTC"},
		{"dremio", "SELECT t.CURRENT_DATE_UTC", "SELECT t.CURRENT_DATE_UTC"},
		{"dremio", `SELECT "CURRENT_DATE_UTC"`, `SELECT "CURRENT_DATE_UTC"`},
		{"dremio", "SELECT CURRENT_DATE_UTC.x", "SELECT CURRENT_DATE_UTC.x"},
		{"mysql", "SELECT CURRENT_DATE_UTC", "SELECT CURRENT_DATE_UTC"},
	}
	written := 0
	for _, c := range cases {
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
		written++
		if got != c[2] {
			t.Errorf("[%s] %s\n  want %s\n  got  %s", c[0], c[1], c[2], got)
		}
	}
	if written != len(cases) {
		t.Errorf("wrote %d statements, want %d", written, len(cases))
	}
}

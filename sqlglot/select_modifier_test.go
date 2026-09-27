package sqlglot

import "testing"

// MySQL words after SELECT qualify the statement. They are not columns, and
// a STRAIGHT_JOIN in the FROM list is still a join. A parse or generate
// error fails the row.
func TestSelectOperationModifiers(t *testing.T) {
	cases := [][3]string{
		{"mysql", "SELECT HIGH_PRIORITY STRAIGHT_JOIN SQL_CALC_FOUND_ROWS * FROM t",
			"SELECT HIGH_PRIORITY STRAIGHT_JOIN SQL_CALC_FOUND_ROWS * FROM t"},
		{"mysql", "SELECT SQL_NO_CACHE * FROM t",
			"SELECT SQL_NO_CACHE * FROM t"},
		{"mysql", "SELECT HIGH_PRIORITY FROM t",
			"SELECT HIGH_PRIORITY FROM t"},
		{"mysql", "SELECT DISTINCT SQL_SMALL_RESULT a FROM t",
			"SELECT DISTINCT SQL_SMALL_RESULT a FROM t"},
		{"", "SELECT * FROM a STRAIGHT_JOIN b",
			"SELECT * FROM a STRAIGHT_JOIN b"},
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

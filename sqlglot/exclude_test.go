package sqlglot

import "testing"

// Redshift's select-level EXCLUDE drops named columns. Parentheses are
// optional when reading and always written. A bare EXCLUDE with no column
// after it is an alias. A parse or generate error fails the row.
func TestSelectExclude(t *testing.T) {
	cases := [][3]string{
		{"redshift", "SELECT *, 4 AS col4 EXCLUDE (col2, col3) FROM (SELECT 1 AS col1, 2 AS col2, 3 AS col3)",
			"SELECT *, 4 AS col4 EXCLUDE (col2, col3) FROM (SELECT 1 AS col1, 2 AS col2, 3 AS col3)"},
		{"redshift", "SELECT *, 4 AS col4 EXCLUDE col2, col3 FROM (SELECT 1 AS col1, 2 AS col2, 3 AS col3)",
			"SELECT *, 4 AS col4 EXCLUDE (col2, col3) FROM (SELECT 1 AS col1, 2 AS col2, 3 AS col3)"},
		{"redshift", "SELECT col1, *, col2 EXCLUDE(col3) FROM (SELECT 1 AS col1, 2 AS col2, 3 AS col3)",
			"SELECT col1, *, col2 EXCLUDE (col3) FROM (SELECT 1 AS col1, 2 AS col2, 3 AS col3)"},
		{"redshift", "SELECT col1, *, col2 EXCLUDE (col3) FROM (SELECT 1 AS col1, 2 AS col2, 3 AS col3)",
			"SELECT col1, *, col2 EXCLUDE (col3) FROM (SELECT 1 AS col1, 2 AS col2, 3 AS col3)"},
		{"redshift", "SELECT 1 EXCLUDE",
			"SELECT 1 AS EXCLUDE"},
		{"redshift", "SELECT 1 EXCLUDE FROM t",
			"SELECT 1 AS EXCLUDE FROM t"},
		{"redshift", "SELECT 1 AS EXCLUDE",
			"SELECT 1 AS EXCLUDE"},
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

	if _, err := ParseOne("SELECT 1 AS col EXCLUDE", "redshift"); err == nil {
		t.Error("ParseOne succeeded for EXCLUDE with no column; that word was dropped")
	}
}

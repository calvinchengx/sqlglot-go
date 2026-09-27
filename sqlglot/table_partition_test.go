package sqlglot

import "testing"

// MySQL names which partition a query reads. A word with no list stays an
// alias. A parse or generate error fails the row.
func TestTablePartition(t *testing.T) {
	cases := [][3]string{
		{"mysql", "SELECT * FROM t1 PARTITION(p0)",
			"SELECT * FROM t1 PARTITION(p0)"},
		{"mysql", "SELECT * FROM t1 PARTITION(p0, p1)",
			"SELECT * FROM t1 PARTITION(p0, p1)"},
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
	if _, err := ParseOne("SELECT * FROM t1 PARTITION()", "mysql"); err == nil {
		t.Error("PARTITION() parsed")
	}
}

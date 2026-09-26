package sqlglot

import "testing"

// Redshift's bare SYSDATE is a timestamp. GETDATE() is the same class without
// the flag, and a name after a dot stays a column. A parse or generate error
// fails the row.
func TestSysdate(t *testing.T) {
	cases := [][3]string{
		{"redshift", "SELECT SYSDATE", "SELECT SYSDATE"},
		{"redshift", "SELECT sysdate", "SELECT SYSDATE"},
		{"redshift", "SELECT GETDATE()", "SELECT GETDATE()"},
		{"redshift", "SELECT t.SYSDATE", `SELECT t."SYSDATE"`},
		{"redshift", "SELECT SYSDATE.x", "SELECT SYSDATE.x"},
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

package sqlglot

import "testing"

// MySQL's bare BINARY casts the column that follows. A parenthesis keeps the
// call. A parse or generate error fails the row.
func TestMySQLBinaryPrefix(t *testing.T) {
	cases := [][3]string{
		{"mysql", "SELECT * FROM x ORDER BY BINARY a",
			"SELECT * FROM x ORDER BY CAST(a AS BINARY)"},
		{"mysql", "SELECT BINARY a",
			"SELECT CAST(a AS BINARY)"},
		{"mysql", "SELECT BINARY t.a",
			"SELECT CAST(t.a AS BINARY)"},
		{"mysql", "SELECT CAST(a AS BINARY)",
			"SELECT CAST(a AS BINARY)"},
		{"mysql", "SELECT BINARY(a)",
			"SELECT BINARY(a)"},
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

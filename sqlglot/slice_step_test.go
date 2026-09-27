package sqlglot

import "testing"

// DuckDB writes `[:-:-1]` as a slice whose end and step are both -1. A slice
// with no step stays two bounds. A parse or generate error fails the row.
func TestSliceStep(t *testing.T) {
	cases := [][3]string{
		{"duckdb", "SELECT ([1,2,3])[:-:-1]", "SELECT ([1, 2, 3])[:-1:-1]"},
		{"duckdb", "SELECT ([1,2,3])[:-1:-1]", "SELECT ([1, 2, 3])[:-1:-1]"},
		{"duckdb", "SELECT x[:]", "SELECT x[:]"},
		{"duckdb", "SELECT x[1:2]", "SELECT x[1:2]"},
		{"duckdb", "SELECT x[1:2:3]", "SELECT x[1:2:3]"},
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

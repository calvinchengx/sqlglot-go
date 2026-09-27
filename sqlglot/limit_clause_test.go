package sqlglot

import "testing"

// `LIMIT offset, count` is the older spelling of `LIMIT count OFFSET offset`.
// A dialect that only accepts a literal count folds the arithmetic. A parse
// or generate error fails the row.
func TestLimitComma(t *testing.T) {
	cases := [][3]string{
		{"mysql", "SELECT * FROM test LIMIT 0 + 1, 0 + 1",
			"SELECT * FROM test LIMIT 1 OFFSET 1"},
		{"mysql", "SELECT * FROM t LIMIT 5, 10",
			"SELECT * FROM t LIMIT 10 OFFSET 5"},
		{"mysql", "SELECT * FROM t LIMIT 10",
			"SELECT * FROM t LIMIT 10"},
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

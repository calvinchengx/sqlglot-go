package sqlglot

import "testing"

// Materialize's empty LIST[] is an array of a list type, written back as
// itself. A list with a member stays a list. A parse or generate error fails
// the row.
func TestListEmptyType(t *testing.T) {
	cases := [][2]string{
		{"SELECT LIST[]", "SELECT LIST[]"},
		{"SELECT LIST[1]", "SELECT LIST[1]"},
		{"SELECT LIST[1, 2]", "SELECT LIST[1, 2]"},
	}
	written := 0
	for _, c := range cases {
		tree, err := ParseOne(c[0], "materialize")
		if err != nil {
			t.Errorf("ParseOne(%q): %v", c[0], err)
			continue
		}
		got, err := Generate(tree, "materialize")
		if err != nil {
			t.Errorf("Generate(%q): %v", c[0], err)
			continue
		}
		written++
		if got != c[1] {
			t.Errorf("%s\n  want %s\n  got  %s", c[0], c[1], got)
		}
	}
	if written != len(cases) {
		t.Errorf("wrote %d statements, want %d", written, len(cases))
	}
}

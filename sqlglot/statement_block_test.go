package sqlglot

import "testing"

func TestStatementBlock(t *testing.T) {
	cases := []struct {
		dialect string
		sql     string
		written string
	}{
		{"", "SELECT 1; SELECT 2", "SELECT 1; SELECT 2"},
		{"tsql", "SELECT 1; SELECT 2;", "SELECT 1; SELECT 2"},
		{"", "SELECT 1 -- c\n; SELECT 2", "SELECT 1; SELECT 2"},
		{"", "SELECT 1 /* c */ ; SELECT 2", "SELECT 1; SELECT 2"},
	}
	for _, c := range cases {
		tree, err := ParseOne(c.sql, c.dialect)
		if err != nil {
			t.Fatalf("%s: %v", c.dialect, err)
		}
		items, _ := tree.Args["expressions"].([]*Expression)
		if tree.Class != "Block" || len(items) != 2 || items[0].Class != "Select" || items[1].Class != "Select" {
			t.Fatalf("%s: %s", c.dialect, tree.Class)
		}
		got, gerr := Generate(tree, c.dialect)
		if gerr != nil || got != c.written {
			t.Fatalf("%s wrote %q (%v)", c.dialect, got, gerr)
		}
	}
}

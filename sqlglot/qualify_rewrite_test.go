package sqlglot

import "testing"

// eliminateQualify declines the one shape it cannot rewrite: a branch of a
// FROM-first multi-table insert shares its parent's FROM, so the query cannot
// be wrapped in a subquery (transforms.eliminate_qualify).
func TestEliminateQualifyDeclinesAMultiTableBranch(t *testing.T) {
	insert := New("Insert")
	New("MultitableInserts", Arg{"expressions", []*Expression{insert}})
	sel := New("Select",
		Arg{"expressions", []*Expression{New("Star")}},
		Arg{"qualify", New("Qualify", Arg{"this", New("Boolean", Arg{"this", true})})})
	insert.Set("expression", sel)
	if _, ok := eliminateQualify(sel); ok {
		t.Fatal("a multi-table insert branch was rewritten; it should be refused")
	}
}

// replaceNode puts the new node where the old one stood, whether the parent
// holds it singly or in a list.
func TestReplaceNode(t *testing.T) {
	old := New("Column", Arg{"this", New("Identifier", Arg{"this", "a"})})
	parent := New("Select", Arg{"expressions", []*Expression{old}})
	replacement := New("Column", Arg{"this", New("Identifier", Arg{"this", "b"})})
	replaceNode(old, replacement)
	items, _ := parent.Args["expressions"].([]*Expression)
	if len(items) != 1 || items[0] != replacement || replacement.Parent != parent {
		t.Fatalf("the list was not rewritten: %v", items)
	}
	// A single-valued argument takes the same route.
	inner := New("Paren", Arg{"this", New("Boolean", Arg{"this", true})})
	holder := New("Not", Arg{"this", inner})
	swapped := New("Boolean", Arg{"this", false})
	replaceNode(inner, swapped)
	if holder.Args["this"] != swapped || swapped.Parent != holder {
		t.Fatalf("the argument was not rewritten: %v", holder.Args["this"])
	}
	// A node with no parent is left alone rather than panicking.
	replaceNode(New("Star"), New("Star"))
}

// findNewName is the reference's own: the base, or the first `base_2`,
// `base_3`... that nothing has taken.
func TestFindNewName(t *testing.T) {
	taken := map[string]bool{"_w": true, "_w_2": true}
	if got := findNewName(taken, "_w"); got != "_w_3" {
		t.Fatalf("findNewName is %q, want _w_3", got)
	}
	if got := findNewName(map[string]bool{}, "_w"); got != "_w" {
		t.Fatalf("findNewName is %q, want _w", got)
	}
	if got := itoa(120); got != "120" {
		t.Fatalf("itoa(120) is %q", got)
	}
}

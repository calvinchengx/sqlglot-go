package sqlglot

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestSetAdoptsChildren(t *testing.T) {
	child := New("Identifier", Arg{"this", "a"})
	list := []*Expression{New("Column"), nil}
	parent := New("Select")
	parent.Set("this", child)
	parent.Set("expressions", list)
	parent.Set("nothing", (*Expression)(nil))
	parent.Set("leaf", 1)

	if child.Parent != parent {
		t.Error("a child assigned with Set was not adopted")
	}
	if list[0].Parent != parent {
		t.Error("a child in a list was not adopted")
	}
	// Re-assigning a key must not record it twice, or the dump order changes.
	parent.Set("this", child)
	if got := strings.Join(parent.Keys, ","); got != "this,expressions,nothing,leaf" {
		t.Errorf("Keys = %s", got)
	}
}

func TestThisAndName(t *testing.T) {
	if New("Table").This() != nil {
		t.Error("This() on a node with no `this` should be nil")
	}
	var nilExpr *Expression
	if nilExpr.This() != nil || nilExpr.Name() != "" {
		t.Error("This() and Name() should tolerate a nil receiver")
	}

	ident := New("Identifier", Arg{"this", "sales"})
	table := New("Table", Arg{"this", ident})
	if table.This() != ident {
		t.Error("This() did not return the primary child")
	}
	if got := table.Name(); got != "sales" {
		t.Errorf("Name() = %q, want sales", got)
	}
	if got := ident.Name(); got != "sales" {
		t.Errorf("Name() on the identifier itself = %q", got)
	}
	// A `this` that is neither an identifier nor a literal has no name.
	if got := New("Table", Arg{"this", New("Select")}).Name(); got != "" {
		t.Errorf("Name() = %q, want empty", got)
	}
	if got := New("Table", Arg{"this", 42}).Name(); got != "" {
		t.Errorf("Name() of a non-string leaf = %q, want empty", got)
	}
}

func TestWalkAndFindAll(t *testing.T) {
	a := New("Column", Arg{"this", New("Identifier", Arg{"this", "a"})})
	b := New("Column", Arg{"this", New("Identifier", Arg{"this", "b"})})
	sel := New("Select",
		Arg{"expressions", []*Expression{a, b}},
		Arg{"from_", New("Table", Arg{"this", New("Identifier", Arg{"this", "t"})})},
	)

	var seen []string
	sel.Walk(func(n *Expression) bool {
		seen = append(seen, n.Class)
		return true
	})
	want := "Select,Column,Identifier,Column,Identifier,Table,Identifier"
	if got := strings.Join(seen, ","); got != want {
		t.Errorf("Walk visited\n  %s\nwant\n  %s", got, want)
	}

	// Returning false stops descent into that node, not the whole walk.
	var shallow []string
	sel.Walk(func(n *Expression) bool {
		shallow = append(shallow, n.Class)
		return n.Class != "Column"
	})
	if got := strings.Join(shallow, ","); got != "Select,Column,Column,Table,Identifier" {
		t.Errorf("Walk with a stop returned %s", got)
	}

	cols := sel.FindAll("Column")
	if len(cols) != 2 || cols[0] != a || cols[1] != b {
		t.Errorf("FindAll(Column) returned %d nodes", len(cols))
	}
	if n := len(sel.FindAll("Column", "Table")); n != 3 {
		t.Errorf("FindAll(Column, Table) returned %d nodes, want 3", n)
	}
	if n := len(sel.FindAll("Lateral")); n != 0 {
		t.Errorf("FindAll for an absent class returned %d nodes", n)
	}
	var nilExpr *Expression
	nilExpr.Walk(func(*Expression) bool { t.Error("Walk on nil visited a node"); return true })
}

func TestDumpMatchesTheReferenceFormat(t *testing.T) {
	// Shape taken from the reference: leaves are their own records carrying
	// "v", list elements are marked "a", and every record but the root names
	// its parent's index and the arg key it sits under.
	tree := New("Table", Arg{"this", New("Identifier", Arg{"this", "sales"}, Arg{"quoted", false})})
	got := tree.Dump()
	want := []map[string]any{
		{"c": "sqlglot.expressions.query.Table"},
		{"c": "sqlglot.expressions.core.Identifier", "i": 0, "k": "this"},
		{"i": 1, "k": "this", "v": "sales"},
		{"i": 1, "k": "quoted", "v": false},
	}
	if !sameDump(t, got, want) {
		t.Errorf("Dump()\n  got  %s", tree.DumpJSON())
	}
}

func TestDumpListsAndDataTypes(t *testing.T) {
	tree := New("Select", Arg{"expressions", []*Expression{New("Star")}}, Arg{"kind", DataTypeKind("INT")}, Arg{"absent", nil})
	got := tree.Dump()
	want := []map[string]any{
		{"c": "sqlglot.expressions.query.Select"},
		{"a": true, "c": "sqlglot.expressions.core.Star", "i": 0, "k": "expressions"},
		{"c": "DataType.Type", "i": 0, "k": "kind", "v": "INT"},
	}
	if !sameDump(t, got, want) {
		t.Errorf("Dump()\n  got  %s", tree.DumpJSON())
	}
}

// A class the generated table knows is qualified by its module; one it does
// not falls back to core, which the harness will surface as a mismatch rather
// than pass silently.
func TestQualifiedClass(t *testing.T) {
	if got := qualifiedClass("Select"); got != "sqlglot.expressions.query.Select" {
		t.Errorf("qualifiedClass(Select) = %s", got)
	}
	if got := qualifiedClass("NotARealNode"); got != "sqlglot.expressions.core.NotARealNode" {
		t.Errorf("qualifiedClass of an unknown class = %s", got)
	}
}

func sameDump(t *testing.T, got, want []map[string]any) bool {
	t.Helper()
	a, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	// Round-trip both so numeric types compare as JSON does.
	var ga, gb any
	if err := json.Unmarshal(a, &ga); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &gb); err != nil {
		t.Fatal(err)
	}
	x, _ := json.Marshal(ga)
	y, _ := json.Marshal(gb)
	return string(x) == string(y)
}

func TestUnsupportedIsAGapNotAGuess(t *testing.T) {
	// Anything outside the grammar is refused with ErrUnsupported, which the
	// harness counts as a gap. It must never come back as a tree that merely
	// looks plausible -- the guard above this parser would reason about
	// something the engine will not run.
	for _, sql := range []string{
		"DELETE FROM t",
		"SELECT * FROM a CROSS APPLY f(1)",
	} {
		tree, err := ParseOne(sql, "")
		if err == nil {
			t.Errorf("ParseOne(%q) returned a tree instead of refusing:\n%s", sql, tree.DumpJSON())
			continue
		}
		if !errors.Is(err, ErrUnsupported) {
			t.Errorf("ParseOne(%q) failed with %v, want ErrUnsupported", sql, err)
		}
	}
}

func TestDumpJSONIsReadable(t *testing.T) {
	got := string(New("Star").DumpJSON())
	if !strings.Contains(got, "sqlglot.expressions.core.Star") || !strings.Contains(got, "\n") {
		t.Errorf("DumpJSON should be indented JSON naming the class, got %s", got)
	}
}

func TestEmptyListIsAnAbsentArg(t *testing.T) {
	// An empty list dumps as nothing in the reference, so it must here too --
	// but the key still reserves its position for a later assignment.
	e := New("Select", Arg{"expressions", []*Expression{}}, Arg{"from_", New("Table")})
	if got := len(e.Dump()); got != 2 {
		t.Errorf("an empty list produced %d records, want the node and its one real child", got)
	}
	if e.Keys[0] != "expressions" {
		t.Errorf("the empty list did not reserve its slot: %v", e.Keys)
	}
}

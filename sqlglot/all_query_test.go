package sqlglot

import "testing"

func TestAllOverAQuery(t *testing.T) {
	tree, err := ParseOne("ALL(SELECT 1)", "")
	if err != nil {
		t.Fatal(err)
	}
	if tree.Class != "All" {
		t.Fatal(tree.Class)
	}
	got, gerr := Generate(tree, "")
	if gerr != nil || got != "ALL (SELECT 1)" {
		t.Fatal(got, gerr)
	}
	kind, kerr := Generate(Annotate(tree, ""), "")
	if kerr != nil || kind != "BOOLEAN" {
		t.Fatal(kind, kerr)
	}
	for _, sql := range []string{"ALL()", "ALL(1, 2)", "ALL((SELECT 1))"} {
		other, oerr := ParseOne(sql, "")
		if oerr != nil {
			t.Fatal(oerr)
		}
		if other.Class == "All" {
			t.Fatal(sql)
		}
	}
}

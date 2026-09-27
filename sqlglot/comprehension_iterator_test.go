package sqlglot

import "testing"

// A list comprehension may iterate an array, not only a name. The element
// may be a call reached through a dot and then subscripted. A parse or
// generate error fails the row.
func TestComprehensionArrayIterator(t *testing.T) {
	sql := "[x.STRING_SPLIT(' ')[i] FOR x IN ['1', '2', 3] IF x.CONTAINS('1')]"
	tree, err := ParseOne(sql, "duckdb")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	got, err := Generate(tree, "duckdb")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got != sql {
		t.Errorf("got  %s\nwant %s", got, sql)
	}
}

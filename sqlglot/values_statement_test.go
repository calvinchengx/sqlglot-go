package sqlglot

import "testing"

// Presto may write a VALUES statement without parentheses. Each bare value is
// a one-column row, and it is written back with the parentheses.
func TestPrestoValuesStatement(t *testing.T) {
	tree, err := ParseOne("VALUES 1, 2, 3", "presto")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	rows, _ := tree.Args["expressions"].([]*Expression)
	if tree.Class != "Values" || len(rows) != 3 || rows[0].Class != "Tuple" {
		t.Fatalf("bare VALUES was not three tuples: %s", tree.Class)
	}
	got, err := Generate(tree, "presto")
	if err != nil || got != "VALUES (1), (2), (3)" {
		t.Fatalf("Generate = %q, %v", got, err)
	}

	paren := "VALUES (1), (2), (3)"
	tree, err = ParseOne(paren, "presto")
	if err != nil {
		t.Fatalf("ParseOne(paren): %v", err)
	}
	if got, err = Generate(tree, "presto"); err != nil || got != paren {
		t.Fatalf("Generate(paren) = %q, %v", got, err)
	}

	// Where the clause always has parentheses, a bare word is a column.
	column, err := ParseOne("values", "")
	if err != nil || column.Class != "Column" {
		t.Fatalf("bare values was not a column: %v", err)
	}
}

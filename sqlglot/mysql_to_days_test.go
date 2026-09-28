package sqlglot

import "testing"

func TestMySQLToDays(t *testing.T) {
	sql := "SELECT TO_DAYS(x)"
	want := "SELECT (DATEDIFF(x, '0000-01-01') + 1)"
	tree, err := ParseOne(sql, "mysql")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	got, gerr := Generate(tree, "mysql")
	if gerr != nil || got != want {
		t.Fatalf("Generate = %q, %v", got, gerr)
	}
}

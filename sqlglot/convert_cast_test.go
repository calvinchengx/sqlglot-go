package sqlglot

import "testing"

// Redshift writes CONVERT with the type first. The reference reads it as a
// cast of the value. A parse or generate error fails the test.
func TestRedshiftConvertIsACast(t *testing.T) {
	tree, err := ParseOne("CONVERT(INT, x)", "redshift")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if tree.Class != "Cast" {
		t.Fatalf("class %s, want Cast", tree.Class)
	}
	got, err := Generate(tree, "redshift")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got != "CAST(x AS INTEGER)" {
		t.Errorf("got %s, want CAST(x AS INTEGER)", got)
	}
}

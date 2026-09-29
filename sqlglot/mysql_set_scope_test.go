package sqlglot

import "testing"

func TestMySQLSetScopeRefusals(t *testing.T) {
	if _, err := Generate(New("SetItem"), "mysql"); err == nil {
		t.Fatal("kind")
	}
	tree, err := ParseOne("SET PERSIST max_connections = 1000", "mysql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Generate(tree, "postgres"); err == nil {
		t.Fatal("persist")
	}
	tree, err = ParseOne("SET PERSIST_ONLY back_log = 100", "mysql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Generate(tree, "postgres"); err == nil {
		t.Fatal("persist only")
	}
}

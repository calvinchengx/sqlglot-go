package sqlglot

import "testing"

func TestMySQLPersistScope(t *testing.T) {
	for _, sql := range []string{
		"SET PERSIST max_connections = 1000",
		"SET PERSIST_ONLY back_log = 100",
	} {
		tree, err := ParseOne(sql, "mysql")
		if err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		got, gerr := Generate(tree, "mysql")
		if gerr != nil || got != sql {
			t.Fatalf("%s wrote %q (%v)", sql, got, gerr)
		}
	}
}

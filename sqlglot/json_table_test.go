package sqlglot

import "testing"

// JSON_TABLE reads its COLUMNS list with or without the parentheses, and a
// NESTED PATH carries a list of its own. Both are written back with them
// (OracleParser's own token rewriting).
func TestJSONTableColumns(t *testing.T) {
	for _, c := range []struct{ sql, want string }{
		{"SELECT * FROM JSON_TABLE('[]', '$' COLUMNS a INT PATH '$.a') jt",
			"SELECT * FROM JSON_TABLE('[]', '$' COLUMNS(a INT PATH '$.a')) jt"},
		{"SELECT * FROM JSON_TABLE('[]', '$' COLUMNS (a INT, NESTED PATH '$.b' COLUMNS (b INT)))",
			"SELECT * FROM JSON_TABLE('[]', '$' COLUMNS(a INT, NESTED PATH '$.b' COLUMNS(b INT)))"},
	} {
		tree, err := ParseOne(c.sql, "oracle")
		if err != nil {
			t.Errorf("ParseOne(%q): %v", c.sql, err)
			continue
		}
		got, err := Generate(tree, "oracle")
		if err != nil || got != c.want {
			t.Errorf("%q wrote %q (%v), want %q", c.sql, got, err, c.want)
		}
	}
}

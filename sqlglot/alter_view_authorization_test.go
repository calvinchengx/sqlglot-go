package sqlglot

import "testing"

// Trino's ALTER VIEW … SET AUTHORIZATION is a Command: the reference has no
// grammar for the setting, so the keyword and the rest of the statement are
// kept as text.
func TestTrinoAlterViewSetAuthorization(t *testing.T) {
	sql := "ALTER VIEW people SET AUTHORIZATION alice"
	tree, err := ParseOne(sql, "trino")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	this, _ := tree.Args["this"].(string)
	expr, _ := tree.Args["expression"].(string)
	if tree.Class != "Command" || this != "ALTER" || expr != " VIEW people SET AUTHORIZATION alice" {
		t.Fatalf("SET AUTHORIZATION = %s %q %q", tree.Class, this, expr)
	}
	got, err := Generate(tree, "trino")
	if err != nil || got != sql {
		t.Fatalf("Generate = %q, %v", got, err)
	}
}

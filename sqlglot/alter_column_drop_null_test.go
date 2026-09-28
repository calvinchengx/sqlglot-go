package sqlglot

import "testing"

// Trino drops a column's NOT NULL by an AlterColumn whose drop and
// allow_null are both true. DROP DEFAULT is the same node with only drop.
func TestTrinoAlterColumnDropNotNull(t *testing.T) {
	sql := "ALTER TABLE users ALTER COLUMN id DROP NOT NULL"
	tree, err := ParseOne(sql, "trino")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	actions, _ := tree.Args["actions"].([]*Expression)
	if tree.Class != "Alter" || len(actions) != 1 || actions[0].Class != "AlterColumn" {
		t.Fatalf("class %s actions %d", tree.Class, len(actions))
	}
	col := actions[0]
	drop, _ := col.Args["drop"].(bool)
	allow, said := col.Args["allow_null"].(bool)
	name, _ := col.Args["this"].(*Expression)
	text, _ := name.Args["this"].(string)
	if !drop || !said || !allow || text != "id" {
		t.Fatalf("column %q drop %v allow %v said %v", text, drop, allow, said)
	}
	got, err := Generate(tree, "trino")
	if err != nil || got != sql {
		t.Fatalf("Generate = %q, %v", got, err)
	}
}

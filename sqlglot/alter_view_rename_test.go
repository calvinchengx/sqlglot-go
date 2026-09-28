package sqlglot

import "testing"

// Trino renames a view the same way it renames a table: the new name is an
// AlterRename, and the statement is still an Alter of kind VIEW.
func TestTrinoAlterViewRename(t *testing.T) {
	sql := "ALTER VIEW people RENAME TO users"
	tree, err := ParseOne(sql, "trino")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	actions, _ := tree.Args["actions"].([]*Expression)
	kind, _ := tree.Args["kind"].(string)
	target, _ := actions[0].Args["this"].(*Expression)
	if tree.Class != "Alter" || kind != "VIEW" || len(actions) != 1 || actions[0].Class != "AlterRename" || target.Class != "Table" {
		t.Fatalf("ALTER VIEW rename = %s %s", tree.Class, kind)
	}
	got, err := Generate(tree, "trino")
	if err != nil || got != sql {
		t.Fatalf("Generate = %q, %v", got, err)
	}
}

package sqlglot

import "testing"

func TestTrinoRefreshMaterializedView(t *testing.T) {
	sql := "REFRESH MATERIALIZED VIEW mynamespace.test_view"
	tree, err := ParseOne(sql, "trino")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	target, _ := tree.Args["this"].(*Expression)
	kind, _ := tree.Args["kind"].(string)
	if tree.Class != "Refresh" || target.Class != "Table" || kind != "MATERIALIZED VIEW" {
		t.Fatalf("Refresh = %s %s", tree.Class, kind)
	}
	got, err := Generate(tree, "trino")
	if err != nil || got != sql {
		t.Fatalf("Generate = %q, %v", got, err)
	}
}

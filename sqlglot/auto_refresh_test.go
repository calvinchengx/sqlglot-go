package sqlglot

import "testing"

// Redshift's AUTO REFRESH stands between a materialized view's name and its
// query. A parse or generate error fails the test.
func TestAutoRefresh(t *testing.T) {
	sql := "CREATE MATERIALIZED VIEW orders AUTO REFRESH YES AS SELECT 1"
	tree, err := ParseOne(sql, "redshift")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	got, err := Generate(tree, "redshift")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got != sql {
		t.Errorf("got  %s\nwant %s", got, sql)
	}
}

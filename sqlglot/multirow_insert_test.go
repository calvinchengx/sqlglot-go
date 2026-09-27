package sqlglot

import "testing"

// Redshift does not keyword VALUES, and an insert may name several rows.
// A parse or generate error fails the row.
func TestRedshiftMultiRowInsert(t *testing.T) {
	for _, sql := range []string{
		"INSERT INTO t (a) VALUES (1), (2), (3)",
		"INSERT INTO t (a, b) VALUES (1, 2), (3, 4)",
	} {
		tree, err := ParseOne(sql, "redshift")
		if err != nil {
			t.Fatalf("ParseOne(%s): %v", sql, err)
		}
		got, err := Generate(tree, "redshift")
		if err != nil {
			t.Fatalf("Generate(%s): %v", sql, err)
		}
		if got != sql {
			t.Errorf("got  %s\nwant %s", got, sql)
		}
	}
}

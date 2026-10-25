package sqlglot

import "testing"

// REFRESH's own edges: a kindless REFRESH of a table is a Command, a
// partition bound must be a parenthesised string, and a Refresh with no kind
// writes the bare word.
func TestRefreshEdges(t *testing.T) {
	// No kind and a target that is not a string is the whole statement kept
	// as a command.
	tree, err := ParseOne("REFRESH t", "spark")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if tree.Class != "Command" {
		t.Errorf("REFRESH t is a %s, want a Command", tree.Class)
	}
	// A string target is a Literal and writes as itself.
	for _, c := range []struct{ sql, dialect, want string }{
		{"REFRESH 'hdfs://x'", "spark", "REFRESH 'hdfs://x'"},
		{"REFRESH TABLE a.b", "spark", "REFRESH TABLE a.b"},
	} {
		tree, err := ParseOne(c.sql, c.dialect)
		if err != nil {
			t.Errorf("ParseOne(%q): %v", c.sql, err)
			continue
		}
		got, err := Generate(tree, c.dialect)
		if err != nil || got != c.want {
			t.Errorf("%q wrote %q (%v), want %q", c.sql, got, err, c.want)
		}
	}
	// Every way a StarRocks partition bound can fail to be one.
	for _, sql := range []string{
		"REFRESH MATERIALIZED VIEW mv PARTITION START 1",
		"REFRESH MATERIALIZED VIEW mv PARTITION START (1)",
		"REFRESH MATERIALIZED VIEW mv PARTITION START ('a'",
		"REFRESH MATERIALIZED VIEW mv PARTITION START ('a') END (1)",
	} {
		if _, err := ParseOne(sql, "starrocks"); err == nil {
			t.Errorf("%q was read; it should be refused", sql)
		}
	}
}

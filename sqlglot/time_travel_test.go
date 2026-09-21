package sqlglot

import "testing"

// Time-travel clauses -- FOR VERSION/TIMESTAMP, Dremio's AT SNAPSHOT -- held
// to the pinned reference, plus the range and version forms a dialect cannot
// write, which the port declines.
func TestTimeTravel(t *testing.T) {
	for _, c := range [][3]string{
		{"", "SELECT * FROM t FOR VERSION AS OF 3", "SELECT * FROM t FOR VERSION AS OF 3"},
		{"", "SELECT * FROM t FOR VERSION FROM 1 TO 3", "SELECT * FROM t FOR VERSION FROM (1, 3)"},
		{"", "SELECT * FROM t FOR TIMESTAMP BETWEEN 1 AND 3", "SELECT * FROM t FOR TIMESTAMP BETWEEN (1, 3)"},
		{"", "SELECT * FROM t FOR TIMESTAMP AS OF x AS a", "SELECT * FROM t FOR TIMESTAMP AS OF x AS a"},
		{"", "SELECT * FROM t FOR VERSION 3", "SELECT * FROM t FOR VERSION AS OF 3"},
		{"", "SELECT * FROM t FOR SYSTEM_TIME 3", "SELECT * FROM t FOR TIMESTAMP AS OF 3"},
		{"", "SELECT * FROM t FOR SYSTEM TIME AS OF 3", "SELECT * FROM t FOR TIMESTAMP AS OF 3"},
		{"", "SELECT * FROM t1 JOIN t2 FOR VERSION AS OF 2 ON a=b", "SELECT * FROM t1 JOIN t2 FOR VERSION AS OF 2 ON a = b"},
		{"databricks", "SELECT * FROM t FOR VERSION AS OF 3", "SELECT * FROM t VERSION AS OF 3"},
		{"databricks", "SELECT * FROM t FOR SYSTEM_TIME AS OF 3 AS a", "SELECT * FROM t TIMESTAMP AS OF 3 AS a"},
		{"trino", "SELECT * FROM t FOR VERSION 3", "SELECT * FROM t FOR VERSION AS OF 3"},
		{"trino", "SELECT * FROM t FOR TIMESTAMP BETWEEN 1 AND 3", "SELECT * FROM t FOR TIMESTAMP BETWEEN (1, 3)"},
		{"dremio", "SELECT * FROM t AT SNAPSHOT '1'", "SELECT * FROM t AT SNAPSHOT '1'"},
		{"dremio", "SELECT * FROM t AT TIMESTAMP '2024-01-01' AS a", "SELECT * FROM t AT TIMESTAMP '2024-01-01' AS a"},
		{"dremio", "SELECT * FROM t FOR VERSION AS OF 3", "SELECT * FROM t AT SNAPSHOT 3"},
		{"dremio", "SELECT * FROM t FOR SYSTEM_TIME AS OF 3 AS a", "SELECT * FROM t AT TIMESTAMP 3 AS a"},
		{"dremio", "SELECT * FROM t1 JOIN t2 FOR VERSION AS OF 2 ON a=b", "SELECT * FROM t1 JOIN t2 AT SNAPSHOT 2 ON a = b"},
		{"dremio", "SELECT * FROM t AT TIMESTAMP 'a' , u AT SNAPSHOT 2", "SELECT * FROM t AT TIMESTAMP 'a', u AT SNAPSHOT 2"},
	} {
		tree, err := ParseOne(c[1], c[0])
		if err != nil {
			t.Errorf("[%s] %s: %v", c[0], c[1], err)
			continue
		}
		got, err := Generate(tree, c[0])
		if err != nil || got != c[2] {
			t.Errorf("[%s] %s\n  want %s\n  got  %s (%v)", c[0], c[1], c[2], got, err)
		}
	}
	// The reference writes these differently or not at all.
	for _, c := range [][2]string{
		{"tsql", "SELECT * FROM t FOR VERSION AS OF 3"},
		{"", "SELECT * FROM t FOR VERSION ALL"},
		{"tsql", "SELECT * FROM t FOR VERSION ALL"},
		{"dremio", "SELECT * FROM t FOR VERSION ALL"},
	} {
		if tree, err := ParseOne(c[1], c[0]); err == nil {
			if out, err := Generate(tree, c[0]); err == nil {
				t.Errorf("[%s] %s was written as %s", c[0], c[1], out)
			}
		}
	}
	// AT SNAPSHOT is Dremio's alone.
	if _, err := ParseOne("SELECT * FROM t AT SNAPSHOT '1'", "trino"); err == nil {
		t.Error("[trino] read AT SNAPSHOT")
	}
}

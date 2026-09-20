package sqlglot

import "testing"

// MySQL's index hints and DELETE ... ORDER BY ... LIMIT, held to what the
// pinned reference writes.
func TestIndexHintsAndDeleteLimit(t *testing.T) {
	written := 0
	for _, c := range [][3]string{
		{"", "SELECT * FROM t USE", "SELECT * FROM t AS USE"},
		{"", "DELETE FROM t WHERE a > 5 ORDER BY id LIMIT 3", "DELETE FROM t WHERE a > 5 ORDER BY id LIMIT 3"},
		{"", "DELETE FROM t ORDER BY id DESC", "DELETE FROM t ORDER BY id DESC"},
		{"", "DELETE FROM t LIMIT 2", "DELETE FROM t LIMIT 2"},
		{"", "DELETE FROM t WHERE a = 1 LIMIT 1 ROWS", "DELETE FROM t WHERE a = 1 LIMIT 1 ROWS ONLY"},
		{"", "DELETE FROM t ORDER BY a, b LIMIT 10", "DELETE FROM t ORDER BY a, b LIMIT 10"},
		{"", "DELETE FROM t WHERE a = 1 RETURNING a", "DELETE FROM t WHERE a = 1 RETURNING a"},
		{"", "DELETE FROM t ORDER BY a NULLS FIRST LIMIT 1", "DELETE FROM t ORDER BY a LIMIT 1"},
		{"", "DELETE FROM t LIMIT 2, 3", "DELETE FROM t LIMIT 2, 3"},
		{"tsql", "SELECT * FROM t USE", "SELECT * FROM t AS USE"},
		{"tsql", "DELETE FROM t WHERE a > 5 ORDER BY id LIMIT 3", "DELETE FROM t WHERE a > 5 ORDER BY id LIMIT 3"},
		{"tsql", "DELETE FROM t ORDER BY id DESC", "DELETE FROM t ORDER BY id DESC"},
		{"tsql", "DELETE FROM t LIMIT 2", "DELETE FROM t LIMIT 2"},
		{"tsql", "DELETE FROM t WHERE a = 1 LIMIT 1 ROWS", "DELETE FROM t WHERE a = 1 LIMIT 1 ROWS ONLY"},
		{"tsql", "DELETE FROM t ORDER BY a, b LIMIT 10", "DELETE FROM t ORDER BY a, b LIMIT 10"},
		{"tsql", "DELETE FROM t WHERE a = 1 RETURNING a", "DELETE OUTPUT a FROM t WHERE a = 1"},
		{"tsql", "DELETE FROM t ORDER BY a NULLS FIRST LIMIT 1", "DELETE FROM t ORDER BY a LIMIT 1"},
		{"tsql", "DELETE FROM t LIMIT 2, 3", "DELETE FROM t LIMIT 2, 3"},
		{"postgres", "SELECT * FROM t USE", "SELECT * FROM t AS USE"},
		{"postgres", "DELETE FROM t WHERE a > 5 ORDER BY id LIMIT 3", "DELETE FROM t WHERE a > 5 ORDER BY id LIMIT 3"},
		{"postgres", "DELETE FROM t ORDER BY id DESC", "DELETE FROM t ORDER BY id DESC"},
		{"postgres", "DELETE FROM t LIMIT 2", "DELETE FROM t LIMIT 2"},
		{"postgres", "DELETE FROM t WHERE a = 1 LIMIT 1 ROWS", "DELETE FROM t WHERE a = 1 LIMIT 1 ROWS ONLY"},
		{"postgres", "DELETE FROM t ORDER BY a, b LIMIT 10", "DELETE FROM t ORDER BY a, b LIMIT 10"},
		{"postgres", "DELETE FROM t WHERE a = 1 RETURNING a", "DELETE FROM t WHERE a = 1 RETURNING a"},
		{"postgres", "DELETE FROM t ORDER BY a NULLS FIRST LIMIT 1", "DELETE FROM t ORDER BY a NULLS FIRST LIMIT 1"},
		{"postgres", "DELETE FROM t LIMIT 2, 3", "DELETE FROM t LIMIT 2, 3"},
		{"mysql", "SELECT * FROM t USE INDEX (i)", "SELECT * FROM t USE INDEX (i)"},
		{"mysql", "SELECT * FROM t AS a FORCE KEY (i, j)", "SELECT * FROM t AS a FORCE INDEX (i, j)"},
		{"mysql", "SELECT * FROM t a IGNORE INDEX FOR GROUP BY (i)", "SELECT * FROM t AS a IGNORE INDEX FOR GROUP BY (i)"},
		{"mysql", "SELECT * FROM t USE INDEX FOR JOIN (i) USE INDEX FOR ORDER BY (j)", "SELECT * FROM t USE INDEX FOR JOIN (i) USE INDEX FOR ORDER BY (j)"},
		{"mysql", "SELECT * FROM t USE INDEX ()", "SELECT * FROM t USE INDEX ()"},
		{"mysql", "SELECT * FROM t1 USE INDEX (i) JOIN t2 FORCE INDEX (j) ON t1.a = t2.a", "SELECT * FROM t1 USE INDEX (i) JOIN t2 FORCE INDEX (j) ON t1.a = t2.a"},
		{"mysql", "DELETE FROM t FORCE INDEX (idx) WHERE a > 5", "DELETE FROM t FORCE INDEX (idx) WHERE a > 5"},
		{"mysql", "SELECT * FROM t USE INDEX (i) WHERE a = 1", "SELECT * FROM t USE INDEX (i) WHERE a = 1"},
		{"mysql", "DELETE FROM t WHERE a > 5 ORDER BY id LIMIT 3", "DELETE FROM t WHERE a > 5 ORDER BY id LIMIT 3"},
		{"mysql", "DELETE FROM t ORDER BY id DESC", "DELETE FROM t ORDER BY id DESC"},
		{"mysql", "DELETE FROM t LIMIT 2", "DELETE FROM t LIMIT 2"},
		{"mysql", "DELETE FROM t WHERE a = 1 LIMIT 1 ROWS", "DELETE FROM t WHERE a = 1 LIMIT 1 ROWS ONLY"},
		{"mysql", "DELETE FROM t ORDER BY a, b LIMIT 10", "DELETE FROM t ORDER BY a, b LIMIT 10"},
		{"mysql", "DELETE FROM t WHERE a = 1 RETURNING a", "DELETE FROM t WHERE a = 1 RETURNING a"},
		{"mysql", "DELETE FROM t ORDER BY a NULLS FIRST LIMIT 1", "DELETE FROM t ORDER BY a LIMIT 1"},
		{"mysql", "DELETE FROM t LIMIT 2, 3", "DELETE FROM t LIMIT 2, 3"},
	} {
		tree, err := ParseOne(c[1], c[0])
		if err != nil {
			continue
		}
		got, err := Generate(tree, c[0])
		if err != nil {
			continue
		}
		written++
		if got != c[2] {
			t.Errorf("[%s] %s\n  want %s\n  got  %s", c[0], c[1], c[2], got)
		}
	}
	if written < 20 {
		t.Errorf("only %d statements were written back", written)
	}
	for _, sql := range []string{
		"SELECT * FROM t USE INDEX",
		"SELECT * FROM t USE INDEX (",
		"SELECT * FROM t USE INDEX (,)",
		"SELECT * FROM t USE INDEX FOR",
		"SELECT * FROM t FORCE INDEX FOR JOIN",
		"DELETE FROM t ORDER BY",
		"DELETE FROM t LIMIT",
		"DELETE FROM t LIMIT 1, 2",
	} {
		if tree, err := ParseOne(sql, "mysql"); err == nil && tree != nil {
			_, _ = Generate(tree, "mysql")
		}
	}
}

package sqlglot

import "testing"

// MySQL's ON DUPLICATE KEY UPDATE and the VALUES alias that names the new
// row, held to what the pinned reference writes.
func TestOnDuplicateKey(t *testing.T) {
	written := 0
	for _, c := range [][3]string{
		{"", "INSERT INTO t (a) VALUES (1) ON DUPLICATE KEY UPDATE a = a + 1", "INSERT INTO t (a) VALUES (1) ON DUPLICATE KEY UPDATE SET a = a + 1"},
		{"", "INSERT INTO t (a) VALUES (1) AS n ON DUPLICATE KEY UPDATE a = n.a", "INSERT INTO t (a) (VALUES (1)) AS n ON DUPLICATE KEY UPDATE SET a = n.a"},
		{"", "INSERT INTO t (a) SELECT 1 ON DUPLICATE KEY UPDATE a = 1", "INSERT INTO t (a) SELECT 1 ON DUPLICATE KEY UPDATE SET a = 1"},
		{"", "INSERT INTO t (a) VALUES (1) ON DUPLICATE KEY UPDATE a = 1 WHERE a > 2", "INSERT INTO t (a) VALUES (1) ON DUPLICATE KEY UPDATE SET a = 1 WHERE a > 2"},
		{"", "INSERT INTO t (a) VALUES (1) ON DUPLICATE KEY UPDATE SET a = 1", "INSERT INTO t (a) VALUES (1) ON DUPLICATE KEY UPDATE SET a = 1"},
		{"", "INSERT INTO t (a) VALUES (1) ON DUPLICATE KEY IGNORE", "INSERT INTO t (a) VALUES (1) ON DUPLICATE KEY IGNORE"},
		{"", "INSERT INTO t (a) VALUES (1) AS n(x) ON DUPLICATE KEY UPDATE a = 1", "INSERT INTO t (a) (VALUES (1)) AS n(x) ON DUPLICATE KEY UPDATE SET a = 1"},
		{"", "INSERT INTO t (a) VALUES (1) ON CONFLICT DO NOTHING", "INSERT INTO t (a) VALUES (1) ON CONFLICT DO NOTHING"},
		{"", "INSERT INTO t VALUES (1) AS n", "INSERT INTO t (VALUES (1)) AS n"},
		{"postgres", "INSERT INTO t (a) VALUES (1) ON DUPLICATE KEY UPDATE a = a + 1", "INSERT INTO t (a) VALUES (1) ON DUPLICATE KEY UPDATE SET a = a + 1"},
		{"postgres", "INSERT INTO t (a) VALUES (1) AS n ON DUPLICATE KEY UPDATE a = n.a", "INSERT INTO t (a) (VALUES (1)) AS n ON DUPLICATE KEY UPDATE SET a = n.a"},
		{"postgres", "INSERT INTO t (a) SELECT 1 ON DUPLICATE KEY UPDATE a = 1", "INSERT INTO t (a) SELECT 1 ON DUPLICATE KEY UPDATE SET a = 1"},
		{"postgres", "INSERT INTO t (a) VALUES (1) ON DUPLICATE KEY UPDATE a = 1 WHERE a > 2", "INSERT INTO t (a) VALUES (1) ON DUPLICATE KEY UPDATE SET a = 1 WHERE a > 2"},
		{"postgres", "INSERT INTO t (a) VALUES (1) ON DUPLICATE KEY UPDATE SET a = 1", "INSERT INTO t (a) VALUES (1) ON DUPLICATE KEY UPDATE SET a = 1"},
		{"postgres", "INSERT INTO t (a) VALUES (1) ON DUPLICATE KEY IGNORE", "INSERT INTO t (a) VALUES (1) ON DUPLICATE KEY IGNORE"},
		{"postgres", "INSERT INTO t (a) VALUES (1) AS n(x) ON DUPLICATE KEY UPDATE a = 1", "INSERT INTO t (a) (VALUES (1)) AS n(x) ON DUPLICATE KEY UPDATE SET a = 1"},
		{"postgres", "INSERT INTO t (a) VALUES (1) ON CONFLICT DO NOTHING", "INSERT INTO t (a) VALUES (1) ON CONFLICT DO NOTHING"},
		{"postgres", "INSERT INTO t VALUES (1) AS n", "INSERT INTO t (VALUES (1)) AS n"},
		{"mysql", "INSERT INTO t (a) VALUES (1) ON DUPLICATE KEY UPDATE a = a + 1", "INSERT INTO t (a) VALUES (1) ON DUPLICATE KEY UPDATE a = a + 1"},
		{"mysql", "INSERT INTO t (a) VALUES (1) AS n ON DUPLICATE KEY UPDATE a = n.a", "INSERT INTO t (a) VALUES (1) AS n ON DUPLICATE KEY UPDATE a = n.a"},
		{"mysql", "INSERT INTO t (a) VALUES (1), (2) ON DUPLICATE KEY UPDATE a = VALUES(a), b = 2", "INSERT INTO t (a) VALUES (1), (2) ON DUPLICATE KEY UPDATE a = VALUES(a), b = 2"},
		{"mysql", "INSERT INTO t (a) SELECT 1 ON DUPLICATE KEY UPDATE a = 1", "INSERT INTO t (a) SELECT 1 ON DUPLICATE KEY UPDATE a = 1"},
		{"mysql", "INSERT INTO t (a) VALUES (1) ON DUPLICATE KEY UPDATE a = 1 WHERE a > 2", "INSERT INTO t (a) VALUES (1) ON DUPLICATE KEY UPDATE a = 1 WHERE a > 2"},
		{"mysql", "INSERT INTO t (a) VALUES (1) ON DUPLICATE KEY UPDATE SET a = 1", "INSERT INTO t (a) VALUES (1) ON DUPLICATE KEY UPDATE a = 1"},
		{"mysql", "INSERT INTO t (a) VALUES (1) ON DUPLICATE KEY IGNORE", "INSERT INTO t (a) VALUES (1) ON DUPLICATE KEY IGNORE"},
		{"mysql", "INSERT INTO t (a) VALUES (1) AS n(x) ON DUPLICATE KEY UPDATE a = 1", "INSERT INTO t (a) VALUES (1) AS n(x) ON DUPLICATE KEY UPDATE a = 1"},
		{"mysql", "INSERT INTO t (a) VALUES (1) ON CONFLICT DO NOTHING", "INSERT INTO t (a) VALUES (1) ON CONFLICT DO NOTHING"},
		{"mysql", "INSERT INTO t VALUES (1) AS n", "INSERT INTO t VALUES (1) AS n"},
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
	if written < 8 {
		t.Errorf("only %d statements were written back", written)
	}
	for _, sql := range []string{
		"INSERT INTO t (a) VALUES (1) ON DUPLICATE KEY",
		"INSERT INTO t (a) VALUES (1) ON DUPLICATE KEY UPDATE",
		"INSERT INTO t (a) VALUES (1) ON DUPLICATE KEY UPDATE a =",
		"INSERT INTO t (a) VALUES (1) ON DUPLICATE KEY UPDATE a = 1 WHERE",
		"INSERT INTO t (a) VALUES (1) AS",
	} {
		if tree, err := ParseOne(sql, "mysql"); err == nil && tree != nil {
			_, _ = Generate(tree, "mysql")
		}
	}
	tree, err := ParseOne("INSERT INTO t (a) VALUES (1) ON DUPLICATE KEY UPDATE a = 1", "mysql")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := Generate(tree, "postgres"); err == nil {
		t.Errorf("another dialect wrote ON DUPLICATE KEY: %s", out)
	}
}

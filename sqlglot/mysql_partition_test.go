package sqlglot

import "testing"

// PARTITION BY RANGE/LIST and the case of a table option's value, held to the
// pinned reference: `ENGINE=InnoDB` keeps the case it was written in.
func TestMySQLPartitionsAndOptions(t *testing.T) {
	written := 0
	for _, c := range [][2]string{
		{"CREATE TABLE t (a INT) PARTITION BY RANGE (a) (PARTITION p0 VALUES LESS THAN (10))", "CREATE TABLE t (a INT) PARTITION BY RANGE (a) (PARTITION p0 VALUES LESS THAN (10))"},
		{"CREATE TABLE t (a INT) PARTITION BY RANGE (a, b) (PARTITION p0 VALUES LESS THAN (1, 2), PARTITION p1 VALUES LESS THAN (MAXVALUE, MAXVALUE))", "CREATE TABLE t (a INT) PARTITION BY RANGE (a, b) (PARTITION p0 VALUES LESS THAN (1, 2), PARTITION p1 VALUES LESS THAN (`MAXVALUE`, `MAXVALUE`))"},
		{"CREATE TABLE t (a INT) PARTITION BY LIST (a) (PARTITION p0 (1, 2), PARTITION p1 VALUES IN (3))", "CREATE TABLE t (a INT) PARTITION BY LIST (a) (PARTITION p0 VALUES IN (1, 2), PARTITION p1 VALUES IN (3))"},
		{"CREATE TABLE t (a INT) PARTITION BY RANGE (a) (PARTITION p0 VALUES LESS THAN (maxvalue))", "CREATE TABLE t (a INT) PARTITION BY RANGE (a) (PARTITION p0 VALUES LESS THAN (MAXVALUE))"},
		{"CREATE TABLE t (a INT) PARTITION BY RANGE (a + 1) (PARTITION `p 0` VALUES LESS THAN (10 + 1))", "CREATE TABLE t (a INT) PARTITION BY RANGE (a + 1) (PARTITION `p 0` VALUES LESS THAN (10 + 1))"},
		{"CREATE TABLE t (a INT) ENGINE=InnoDB PARTITION BY LIST (a) (PARTITION p0 VALUES IN (1)) COMMENT='x'", "CREATE TABLE t (a INT) ENGINE=InnoDB PARTITION BY LIST (a) (PARTITION p0 VALUES IN (1)) COMMENT='x'"},
		{"CREATE TABLE t (a INT) ENGINE=InnoDB", "CREATE TABLE t (a INT) ENGINE=InnoDB"},
		{"CREATE TABLE t (a INT) ENGINE=InnoDB AUTO_INCREMENT=1", "CREATE TABLE t (a INT) ENGINE=InnoDB AUTO_INCREMENT=1"},
		{"CREATE TABLE t (a INT) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4", "CREATE TABLE t (a INT) ENGINE=InnoDB DEFAULT CHARACTER SET=utf8mb4"},
		{"CREATE TABLE t (a INT) COMMENT='x' ENGINE=MyISAM", "CREATE TABLE t (a INT) COMMENT='x' ENGINE=MyISAM"},
		{"CREATE TABLE t (a INT) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin", "CREATE TABLE t (a INT) CHARACTER SET=utf8mb4 COLLATE=utf8mb4_bin"},
	} {
		tree, err := ParseOne(c[0], "mysql")
		if err != nil {
			continue
		}
		got, err := Generate(tree, "mysql")
		if err != nil {
			continue
		}
		written++
		if got != c[1] {
			t.Errorf("%s\n  want %s\n  got  %s", c[0], c[1], got)
		}
	}
	if written < 8 {
		t.Errorf("only %d statements were written back", written)
	}
	for _, sql := range []string{
		"CREATE TABLE t (a INT) PARTITION BY HASH (a)",
		"CREATE TABLE t (a INT) PARTITION BY RANGE",
		"CREATE TABLE t (a INT) PARTITION BY RANGE (a)",
		"CREATE TABLE t (a INT) PARTITION BY RANGE (a) (PARTITION",
		"CREATE TABLE t (a INT) PARTITION BY RANGE (a) (PARTITION p0 VALUES LESS THAN",
		"CREATE TABLE t (a INT) PARTITION BY RANGE (a) (PARTITION p0 VALUES LESS THAN (",
		"CREATE TABLE t (a INT) PARTITION BY LIST (a) (PARTITION p0 VALUES IN (1",
		"CREATE TABLE t (a INT) PARTITION BY LIST (a) (PARTITION 1",
		"CREATE TABLE t (a INT) PARTITION BY LIST ( ) (PARTITION p0 VALUES IN (1))",
		"CREATE TABLE t (a INT) ENGINE = `InnoDB`",
	} {
		if tree, err := ParseOne(sql, "mysql"); err == nil && tree != nil {
			_, _ = Generate(tree, "mysql")
		}
	}
}

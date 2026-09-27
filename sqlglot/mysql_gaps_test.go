package sqlglot

import "testing"

// The MySQL statements the pin parsed and this port refused.
// A parse or generate error fails the row.
func TestMySQLGaps(t *testing.T) {
	cases := [][2]string{
		{"ALTER ALGORITHM = MERGE VIEW v AS SELECT * FROM foo", "ALTER ALGORITHM = MERGE VIEW v AS SELECT * FROM foo"},
		{"ALTER DEFINER = 'admin'@'localhost' VIEW v AS SELECT * FROM foo", "ALTER DEFINER = 'admin'@'localhost' VIEW v AS SELECT * FROM foo"},
		{"ALTER SQL SECURITY = DEFINER VIEW v AS SELECT * FROM foo", "ALTER SQL SECURITY = DEFINER VIEW v AS SELECT * FROM foo"},
		{"ALTER TABLE t AUTO_INCREMENT=3000000000", "ALTER TABLE t AUTO_INCREMENT=3000000000"},
		{"ALTER TABLE t1 ADD COLUMN x INT, ALGORITHM=INPLACE, LOCK=EXCLUSIVE", "ALTER TABLE t1 ADD COLUMN x INT, ALGORITHM=INPLACE, LOCK=EXCLUSIVE"},
		{"CAST(x AS SET('a', 'b'))", "CAST(x AS SET('a', 'b'))"},
		{"CREATE DATABASE db CHARACTER SET=utf8", "CREATE DATABASE db CHARACTER SET=utf8"},
		{"CREATE DATABASE db DEFAULT CHARACTER SET=utf8", "CREATE DATABASE db DEFAULT CHARACTER SET=utf8"},
		{"CREATE FUNCTION f () RETURNS VARCHAR LANGUAGE SQL SQL SECURITY INVOKER SELECT 'abc'", "CREATE FUNCTION f() RETURNS TEXT LANGUAGE SQL SQL SECURITY INVOKER AS SELECT 'abc'"},
		{"CREATE OR REPLACE VIEW my_view AS SELECT column1 AS `boo`, column2 AS `foo` FROM my_table WHERE column3 = 'some_value' UNION SELECT q.* FROM fruits_table, JSON_TABLE(Fruits, '$[*]' COLUMNS(id VARCHAR(255) PATH '$.$id', value VARCHAR(255) PATH '$.value')) AS q", "CREATE OR REPLACE VIEW my_view AS SELECT column1 AS `boo`, column2 AS `foo` FROM my_table WHERE column3 = 'some_value' UNION SELECT q.* FROM fruits_table, JSON_TABLE(Fruits, '$[*]' COLUMNS(id VARCHAR(255) PATH '$.$id', value VARCHAR(255) PATH '$.value')) AS q"},
		{"CREATE TABLE `x` (`username` VARCHAR(200), PRIMARY KEY (`username`(16)))", "CREATE TABLE `x` (`username` VARCHAR(200), PRIMARY KEY (`username`(16)))"},
		{"CREATE TABLE foo (a BIGINT, INDEX b USING HASH (c) COMMENT 'd' VISIBLE ENGINE_ATTRIBUTE = 'e' WITH PARSER foo)", "CREATE TABLE foo (a BIGINT, INDEX b USING HASH (c) COMMENT 'd' VISIBLE ENGINE_ATTRIBUTE = 'e' WITH PARSER foo)"},
		{"CREATE TABLE foo (a BIGINT, UNIQUE (b) USING BTREE)", "CREATE TABLE foo (a BIGINT, UNIQUE (b) USING BTREE)"},
		{"CREATE TABLE t (c DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP) DEFAULT CHARSET=utf8 ROW_FORMAT=DYNAMIC", "CREATE TABLE t (c DATETIME DEFAULT CURRENT_TIMESTAMP() ON UPDATE CURRENT_TIMESTAMP()) DEFAULT CHARACTER SET=utf8 ROW_FORMAT=DYNAMIC"},
		{"CREATE TABLE test (id INT, PRIMARY KEY \"pk_name\" (id))", "CREATE TABLE test (id INT, PRIMARY KEY `pk_name` (id))"},
		{"DELETE /*+ MAX_EXECUTION_TIME(1) */ FROM t WHERE a = 1", "DELETE /*+ MAX_EXECUTION_TIME(1) */ FROM t WHERE a = 1"},
		{"INSERT INTO `test_table` SET `test_col_1` = 123, `test_col_2` = '456'", "INSERT INTO `test_table` (`test_col_1`, `test_col_2`) VALUES (123, '456')"},
		{"INSERT INTO t SET a = DEFAULT, b = 2 AS new ON DUPLICATE KEY UPDATE a = new.a + 1", "INSERT INTO t (a, b) VALUES (DEFAULT, 2) AS new ON DUPLICATE KEY UPDATE a = new.a + 1"},
		{"INSERT INTO t1 (a, b, c) VALUES (1, 2, 3), (4, 5, 6) ON DUPLICATE KEY UPDATE c = VALUES(a) + VALUES(b)", "INSERT INTO t1 (a, b, c) VALUES (1, 2, 3), (4, 5, 6) ON DUPLICATE KEY UPDATE c = VALUES(a) + VALUES(b)"},
		{"SELECT * FROM source, JSON_TABLE(source.links, '$.org[*]' COLUMNS(row_id FOR ORDINALITY, link VARCHAR(255) PATH '$.link')) AS links", "SELECT * FROM source, JSON_TABLE(source.links, '$.org[*]' COLUMNS(row_id FOR ORDINALITY, link VARCHAR(255) PATH '$.link')) AS links"},
		{"SELECT /*+ BKA(t1) NO_BKA(t2) */ * FROM t1 INNER JOIN t2", "SELECT /*+ BKA(t1) NO_BKA(t2) */ * FROM t1 INNER JOIN t2"},
		{"SET @var1 := 1", "SET @var1 = 1"},
		{"SHOW PROFILE BLOCK IO", "SHOW PROFILE BLOCK IO"},
		{"SHOW PROFILE BLOCK IO, PAGE FAULTS FOR QUERY 1 OFFSET 2 LIMIT 3", "SHOW PROFILE BLOCK IO, PAGE FAULTS FOR QUERY 1 OFFSET 2 LIMIT 3"},
		{"UPDATE /*+ MAX_EXECUTION_TIME(1) */ t SET a = 1", "UPDATE /*+ MAX_EXECUTION_TIME(1) */ t SET a = 1"},
	}
	written := 0
	for _, c := range cases {
		tree, err := ParseOne(c[0], "mysql")
		if err != nil {
			t.Errorf("ParseOne(%q): %v", c[0], err)
			continue
		}
		got, err := Generate(tree, "mysql")
		if err != nil {
			t.Errorf("Generate(%q): %v", c[0], err)
			continue
		}
		written++
		if got != c[1] {
			t.Errorf("%s\n  want %s\n  got  %s", c[0], c[1], got)
		}
	}
	if written != len(cases) {
		t.Errorf("wrote %d statements, want %d", written, len(cases))
	}
}

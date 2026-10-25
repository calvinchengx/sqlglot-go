package sqlglot

import "testing"

// The shapes the tail of the string-escape campaign taught the generator,
// each held to the SQL the pinned reference writes back for it. A parse or
// generate error fails the row.
func TestGeneratorTails(t *testing.T) {
	cases := [][3]string{
		// A dialect with no QUALIFY rewrites the query into an outer select
		// over a subquery that projects the window function
		// (transforms.eliminate_qualify).
		{"spark",
			"SELECT k, v FROM x QUALIFY ROW_NUMBER() OVER (PARTITION BY k ORDER BY v) = 1",
			"SELECT k, v FROM (SELECT k, v, ROW_NUMBER() OVER (PARTITION BY k ORDER BY v) AS _w FROM x) AS _t WHERE _w = 1"},
		{"hive",
			"SELECT a FROM t QUALIFY ROW_NUMBER() OVER (PARTITION BY a) = 1",
			"SELECT a FROM (SELECT a, ROW_NUMBER() OVER (PARTITION BY a) AS _w FROM t) AS _t WHERE _w = 1"},
		// An unnamed projection is given a name the outer query can read, a
		// window alias collides with `_w` and moves to `_w_2`, and a column
		// only a window reads is not projected twice.
		{"spark",
			"SELECT a + 1 FROM t QUALIFY ROW_NUMBER() OVER (ORDER BY a) = 1",
			"SELECT _c FROM (SELECT a + 1 AS _c, ROW_NUMBER() OVER (ORDER BY a) AS _w FROM t) AS _t WHERE _w = 1"},
		{"spark",
			"SELECT a, ROW_NUMBER() OVER () AS _w FROM t QUALIFY ROW_NUMBER() OVER (ORDER BY a) = 1",
			"SELECT a, _w FROM (SELECT a, ROW_NUMBER() OVER () AS _w, ROW_NUMBER() OVER (ORDER BY a) AS _w_2 FROM t) AS _t WHERE _w_2 = 1"},
		// A dialect with no FILTER folds the condition into the aggregate
		// (dialect.no_filter_sql).
		{"tsql",
			"SELECT COUNT_IF(col % 2 = 0) FILTER(WHERE col < 1000) FROM foo",
			"SELECT COUNT(IIF(col < 1000 AND col % 2 = 0, 1, NULL)) FROM foo"},
		{"bigquery",
			"SELECT COUNT(*) FILTER(WHERE x > 1) FROM t",
			"SELECT COUNTIF(x > 1) FROM t"},
		// Teradata writes a window aggregate's default frame nowhere, and a
		// SAMPLE back as the size or the fraction it was written with.
		{"teradata",
			"SELECT SUM(x) OVER (PARTITION BY p ORDER BY d ROWS BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING) FROM t",
			"SELECT SUM(x) OVER (PARTITION BY p ORDER BY d) FROM t"},
		{"teradata", "SELECT * FROM t SAMPLE 5", "SELECT * FROM t SAMPLE 5"},
		{"teradata", "SELECT * FROM t SAMPLE 0.25", "SELECT * FROM t SAMPLE 0.25"},
		// A call keeps the name it was written with where the dialect keeps
		// original names, and the dialect's own where it renames.
		{"clickhouse", "SELECT sum(x) FROM t", "SELECT sum(x) FROM t"},
		{"clickhouse", "SELECT SUM(x) FROM t", "SELECT SUM(x) FROM t"},
		{"clickhouse", "SELECT currentDatabase()", "SELECT CURRENT_DATABASE()"},
		{"bigquery", "SELECT OCTET_LENGTH('a')", "SELECT BYTE_LENGTH('a')"},
		{"bigquery", "SELECT SAFE.SUBSTR('foo', 0, -2)", "SELECT SAFE.SUBSTR('foo', 0, -2)"},
		{"bigquery", "SELECT JSON_QUERY(x, '$.a')", "SELECT JSON_QUERY(x, '$.a')"},
		{"bigquery", "SELECT struct(1 AS a)", "SELECT struct(1 AS a)"},
		// ClickHouse names a JSON extractor's path as arguments, counting
		// positions from one.
		{"clickhouse", "SELECT JSONExtractRaw(x, 'y', 1, 'z')", "SELECT JSONExtractRaw(x, 'y', 1, 'z')"},
		{"clickhouse", "SELECT JSONExtractString(x, k1, k2)", "SELECT JSONExtractString(x, k1, k2)"},
		// StarRocks names a multi-column pivot value in braces, and has no
		// UNPIVOT to write.
		{"starrocks",
			"SELECT * FROM t1 PIVOT(SUM(c1) FOR (c2, c3) IN ((1, 1), (2, 2)))",
			"SELECT * FROM t1 PIVOT(SUM(c1) FOR (c2, c3) IN ((1, 1), (2, 2)))"},
		{"starrocks", "SELECT * FROM t1 UNPIVOT(c FOR k IN (c1, c2))", "SELECT * FROM t1"},
		// DuckDB keeps a fraction inside the interval string, writes a typed
		// join by its own words, and puts USING SAMPLE before ORDER BY.
		{"duckdb", "SELECT INTERVAL '1.5 DAY'", "SELECT INTERVAL '1.5 DAY'"},
		{"duckdb",
			"SELECT a.id FROM a JOIN BY (TYPE MARK) b ON a.id = b.id",
			"SELECT a.id FROM a JOIN BY (TYPE MARK) b ON a.id = b.id"},
		{"duckdb", "SELECT [(SELECT 1)]", "SELECT [(SELECT 1)]"},
		{"duckdb", "SELECT [(SELECT 1), 2]", "SELECT [(SELECT 1), 2]"},
		// A dialect without temporal precision drops the parameter.
		{"presto", "SELECT CAST(x AS TIME(6))", "SELECT CAST(x AS TIME)"},
		// Fabric's AT TIME ZONE wraps the result once, not twice.
		{"fabric",
			"CAST(CAST(x AS DATETIMEOFFSET(6)) AT TIME ZONE 'Pacific Standard Time' AS DATETIME2(6))",
			"CAST(CAST(x AS DATETIMEOFFSET(6)) AT TIME ZONE 'Pacific Standard Time' AS DATETIME2(6))"},
		// An ALTER's partition options stand apart from the rest, with a
		// space rather than a comma.
		{"mysql",
			"ALTER TABLE t ENGINE=InnoDB PARTITION BY LIST (a) (PARTITION p0 VALUES IN (1))",
			"ALTER TABLE t ENGINE=InnoDB PARTITION BY LIST (a) (PARTITION p0 VALUES IN (1))"},
		// A T-SQL bare hint keeps its comment, and a comment on a property's
		// comma belongs to that property.
		{"tsql", "SELECT * FROM dbo.t AS s (NOLOCK /* hint */)", "SELECT * FROM dbo.t AS s WITH (NOLOCK /* hint */)"},
		{"mysql",
			"CREATE TABLE t (a INT) ENGINE=InnoDB, /* c */ ROW_FORMAT=DYNAMIC",
			"CREATE TABLE t (a INT) ENGINE=InnoDB /* c */ ROW_FORMAT=DYNAMIC"},
		// Fabric builds its own UNIX_TO_TIME, Redshift keeps a view's own
		// trailer, and a lateral UNNEST carries its ordinality column.
		{"fabric", "SELECT UNIX_TO_TIME(x)",
			"SELECT DATEADD(MICROSECONDS, CAST(ROUND(x * 1e6, 0) AS BIGINT), CAST('1970-01-01' AS DATETIME2(6)))"},
		{"redshift", "CREATE VIEW v AS SELECT 1 WITH NO SCHEMA BINDING",
			"CREATE VIEW v AS SELECT 1 WITH NO SCHEMA BINDING"},
		{"trino", "SELECT * FROM t CROSS JOIN UNNEST(a) WITH ORDINALITY AS u(x, n)",
			"SELECT * FROM t CROSS JOIN UNNEST(a) WITH ORDINALITY AS u(x, n)"},
		// A ClickHouse arrow is the same node as its JSONExtractRaw call.
		{"clickhouse", "SELECT x -> 'a'", "SELECT JSONExtractRaw(x, 'a')"},
		// A MySQL AUTO_INCREMENT written as an ACTION is an option.
		{"mysql", "ALTER TABLE t AUTO_INCREMENT 5", "ALTER TABLE t AUTO_INCREMENT=5"},
		// A LIST_SORT order that is not a string stays an ordinary call.
		{"duckdb", "SELECT LIST_SORT(x, 1)", "SELECT LIST_SORT(x, 1)"},
		// A LIST_VALUE of two names the array in brackets.
		{"duckdb", "SELECT LIST_VALUE(1, 2)", "SELECT [1, 2]"},
		// JSON_TABLE and ANALYZE are Oracle's own shapes, SingleStore spells
		// a `::` as its JSON extractors, and a JSON path key needing quotes
		// is written with brackets.
		{"oracle", "SELECT * FROM JSON_TABLE('[]', '$' COLUMNS (a INT PATH '$.a'))",
			"SELECT * FROM JSON_TABLE('[]', '$' COLUMNS(a INT PATH '$.a'))"},
		{"oracle", "ANALYZE TABLE t COMPUTE STATISTICS", "ANALYZE TABLE t COMPUTE STATISTICS"},
		{"singlestore", "SELECT x::INT", "SELECT JSON_EXTRACT_JSON(x, 'INT')"},
		{"singlestore", "SELECT x::$field", "SELECT JSON_EXTRACT_STRING(x, 'field')"},
		{"", `SELECT JSON_EXTRACT(x, '$."a b"')`, `SELECT JSON_EXTRACT(x, '$["a b"]')`},
		{"sqlite", "INSERT INTO t AS x VALUES (1)", "INSERT INTO t AS x VALUES (1)"},
		{"hive", "CREATE TABLE t (a INT) ROW FORMAT SERDE 'x'", "CREATE TABLE t (a INT) ROW FORMAT SERDE 'x'"},
		// Hive's ORDER BY nulls default, its decimal spelling, and Oracle's
		// interval span, PIVOT and TRUNC of a timestamp.
		{"hive", "SELECT a FROM t ORDER BY a NULLS LAST", "SELECT a FROM t ORDER BY a NULLS LAST"},
		{"hive", "SELECT 1.50", "SELECT 1.50"},
		{"hive", "SELECT 0.00", "SELECT 0.00"},
		{"oracle", "SELECT INTERVAL '1-2' YEAR TO MONTH FROM dual", "SELECT INTERVAL '1-2' YEAR TO MONTH FROM dual"},
		{"oracle", "SELECT * FROM t PIVOT (SUM(x) FOR y IN (1))", "SELECT * FROM t PIVOT(SUM(x) FOR y IN (1))"},
		{"oracle", "SELECT TRUNC(SYSTIMESTAMP) FROM dual", "SELECT TRUNC(SYSTIMESTAMP) FROM dual"},
		// Oracle's KEEP, NTH_VALUE and hierarchical walk; Snowflake's
		// FLATTEN names its columns; the date functions of ClickHouse and
		// BigQuery keep their own spellings.
		{"oracle", "SELECT MAX(x) KEEP (DENSE_RANK FIRST ORDER BY y) FROM t",
			"SELECT MAX(x) KEEP (DENSE_RANK FIRST ORDER BY y) FROM t"},
		{"oracle", "SELECT NTH_VALUE(x, 1) FROM t", "SELECT NTH_VALUE(x, 1) FROM t"},
		{"oracle", "SELECT * FROM t START WITH x = 1 CONNECT BY PRIOR id = pid",
			"SELECT * FROM t START WITH x = 1 CONNECT BY PRIOR id = pid"},
		{"snowflake", "SELECT * FROM t, LATERAL FLATTEN(input => x)",
			"SELECT * FROM t, LATERAL FLATTEN(input => x) AS _flattened(SEQ, KEY, PATH, INDEX, VALUE, THIS)"},
		{"clickhouse", "SELECT DATE_ADD(DAY, 1, d)", "SELECT DATE_ADD(DAY, 1, d)"},
		{"bigquery", "SELECT DATE_ADD(d, INTERVAL 1 DAY)", "SELECT DATE_ADD(d, INTERVAL '1' DAY)"},
		// SQLite merges a table-level PRIMARY KEY into the column it names,
		// and a Hive partition column stays where it was written.
		{"sqlite", "CREATE TABLE t (a INT PRIMARY KEY, b INT, PRIMARY KEY (b))",
			"CREATE TABLE t (a INTEGER PRIMARY KEY, b INTEGER PRIMARY KEY)"},
		{"hive", "CREATE TABLE t (a INT, ds STRING) PARTITIONED BY (ds STRING)",
			"CREATE TABLE t (a INT, ds STRING) PARTITIONED BY (ds STRING)"},
		// Oracle's JSON_TABLE reads COLUMNS with or without parentheses.
		{"oracle", "SELECT * FROM JSON_TABLE('[]', '$' COLUMNS a INT PATH '$.a')",
			"SELECT * FROM JSON_TABLE('[]', '$' COLUMNS(a INT PATH '$.a'))"},
		// BigQuery's own ML calls, and a DAX EVALUATE read as a SELECT.
		{"bigquery", "SELECT * FROM ML.GENERATE_EMBEDDING(MODEL m, TABLE t)",
			"SELECT * FROM ML.GENERATE_EMBEDDING(MODEL m, TABLE t)"},
		{"bigquery", "SELECT * FROM ML.GENERATE_INT(MODEL m, TABLE t)",
			"SELECT * FROM ML.GENERATE_INT(MODEL m, TABLE t)"},
		{"bigquery", "SELECT * FROM ML.GENERATE_DOUBLE(MODEL m, TABLE t)",
			"SELECT * FROM ML.GENERATE_DOUBLE(MODEL m, TABLE t)"},
		{"dax", "EVALUATE t", "SELECT * FROM t"},
		// Oracle's CONNECT BY with START WITH written AFTER it is moved in
		// front, and its TO_CHAR/TO_DATE keep their format argument.
		{"oracle", "SELECT * FROM t CONNECT BY PRIOR id = pid START WITH x = 1",
			"SELECT * FROM t START WITH x = 1 CONNECT BY PRIOR id = pid"},
		{"oracle", "SELECT TO_DATE(x, 'YYYY') FROM dual", "SELECT TO_DATE(x, 'YYYY') FROM dual"},
		// Oracle's JSON_VALUE reads its DEFAULT ... ON EMPTY clause, and a
		// CAST to its own NUMBER type.
		{"oracle", "SELECT CAST(x AS NUMBER) FROM dual", "SELECT CAST(x AS NUMBER) FROM dual"},
		// StarRocks' REFRESH keeps the mode it was given, and a LIST_SORT
		// with NULLS LAST is written in its short form.
		{"starrocks", "REFRESH MATERIALIZED VIEW mv WITH ASYNC MODE",
			"REFRESH MATERIALIZED VIEW mv WITH ASYNC MODE"},
		{"starrocks",
			"REFRESH MATERIALIZED VIEW mv PARTITION START ('2020-01-01') END ('2020-02-01') FORCE WITH SYNC MODE",
			"REFRESH MATERIALIZED VIEW mv PARTITION START ('2020-01-01') END ('2020-02-01') FORCE WITH SYNC MODE"},
		{"duckdb", "SELECT LIST_SORT(x, 'ASC', 'NULLS LAST')", "SELECT LIST_SORT(x)"},
		// A LATERAL UNNEST's ordinality column joins the alias's own column
		// list, and a JSON path slice keeps its bounds.
		{"postgres", "SELECT * FROM t, LATERAL UNNEST(a) WITH ORDINALITY AS u(x, n)",
			"SELECT * FROM t, LATERAL UNNEST(a) WITH ORDINALITY AS u(x, n)"},
		{"", `SELECT JSON_EXTRACT(x, '$[1:2]')`, `SELECT JSON_EXTRACT(x, '$[1:2]')`},
		{"", `SELECT JSON_EXTRACT(x, '$[1:2:3]')`, `SELECT JSON_EXTRACT(x, '$[1:2:3]')`},
		{"", `SELECT JSON_EXTRACT(x, '$[:2]')`, `SELECT JSON_EXTRACT(x, '$[:2]')`},
		{"", `SELECT JSON_EXTRACT(x, '$[1:]')`, `SELECT JSON_EXTRACT(x, '$[1:]')`},
		{"", `SELECT JSON_EXTRACT(x, '$[:]')`, `SELECT JSON_EXTRACT(x, '$[:]')`},
		{"", `SELECT JSON_EXTRACT(x, '$[::2]')`, `SELECT JSON_EXTRACT(x, '$[::2]')`},
		// Oracle's VALIDATE REF UPDATE with its dangling clause, and
		// SingleStore's time-of-day fields read through DATE_FORMAT.
		{"oracle", "ANALYZE TABLE t VALIDATE REF UPDATE SET DANGLING TO NULL",
			"ANALYZE TABLE t VALIDATE REF UPDATE SET DANGLING TO NULL"},
		{"oracle", "ANALYZE TABLE t VALIDATE REF UPDATE", "ANALYZE TABLE t VALIDATE REF UPDATE"},
		{"singlestore", "SELECT HOUR(x)", "SELECT DATE_FORMAT(x :> TIME(6), '%k') :> INT"},
		{"singlestore", "SELECT MINUTE(x)", "SELECT DATE_FORMAT(x :> TIME(6), '%i') :> INT"},
		// MySQL restates a table's comment, Teradata's FORMAT phrase takes
		// the place of a type, Hive drops the nulls ordering it cannot write,
		// and an IGNORE NULLS written after the ordering belongs to the call.
		{"mysql", "ALTER TABLE t COMMENT 'x'", "ALTER TABLE t COMMENT='x'"},
		{"mysql", "ALTER TABLE t COMMENT = 'x'", "ALTER TABLE t COMMENT='x'"},
		{"teradata", "SELECT CAST(x AS FORMAT 'Y4')", "SELECT CAST(x AS FORMAT 'Y4')"},
		{"hive", "SELECT a FROM t ORDER BY a NULLS FIRST", "SELECT a FROM t ORDER BY a"},
		{"hive", "SELECT a FROM t ORDER BY a DESC NULLS LAST", "SELECT a FROM t ORDER BY a DESC"},
		{"", "SELECT LAST_VALUE(x ORDER BY x IGNORE NULLS) OVER () FROM t",
			"SELECT LAST_VALUE(x ORDER BY x) IGNORE NULLS OVER () FROM t"},
		// Oracle's JSON_ARRAYAGG writes its ordering, null handling, RETURNING
		// type and STRICT in the reference's own order.
		{"oracle", "SELECT JSON_ARRAYAGG(x) FROM t", "SELECT JSON_ARRAYAGG(x) FROM t"},
		{"oracle", "SELECT JSON_ARRAYAGG(x STRICT) FROM t", "SELECT JSON_ARRAYAGG(x STRICT) FROM t"},
		{"oracle", "SELECT JSON_ARRAYAGG(x ORDER BY y) FROM t",
			"SELECT JSON_ARRAYAGG(x ORDER BY y) FROM t"},
		{"oracle", "SELECT JSON_ARRAYAGG(x ORDER BY y NULL ON NULL RETURNING CLOB STRICT) FROM t",
			"SELECT JSON_ARRAYAGG(x ORDER BY y NULL ON NULL RETURNING CLOB STRICT) FROM t"},
		{"oracle", "SELECT JSON_ARRAYAGG(x FORMAT JSON) FROM t",
			"SELECT JSON_ARRAYAGG(x FORMAT JSON) FROM t"},
		{"oracle", "SELECT JSON_ARRAYAGG(x RETURNING CLOB) FROM t",
			"SELECT JSON_ARRAYAGG(x RETURNING CLOB) FROM t"},
		// Hive's SORTED BY takes an ordering and drops the nulls words.
		{"hive", "CREATE TABLE t (a INT) CLUSTERED BY (a) SORTED BY (a NULLS FIRST) INTO 4 BUCKETS",
			"CREATE TABLE t (a INT) CLUSTERED BY (a) SORTED BY (a) INTO 4 BUCKETS"},
		{"hive", "CREATE TABLE t (a INT) CLUSTERED BY (a) SORTED BY (a DESC NULLS LAST) INTO 4 BUCKETS",
			"CREATE TABLE t (a INT) CLUSTERED BY (a) SORTED BY (a DESC) INTO 4 BUCKETS"},
		{"hive", "CREATE TABLE t (a INT) CLUSTERED BY (a) SORTED BY (a ASC) INTO 4 BUCKETS",
			"CREATE TABLE t (a INT) CLUSTERED BY (a) SORTED BY (a ASC) INTO 4 BUCKETS"},
		// A virtual table, a foreign key's NO ACTION, a numeric JSON path
		// argument and a DATE_FORMAT over a temporal type.
		{"sqlite", "CREATE VIRTUAL TABLE t USING fts5(a)", "CREATE VIRTUAL TABLE t USING fts5(a)"},
		{"", "CREATE TABLE t (a INT, FOREIGN KEY (a) REFERENCES u (b) ON DELETE NO ACTION)",
			"CREATE TABLE t (a INT, FOREIGN KEY (a) REFERENCES u (b) ON DELETE NO ACTION)"},
		{"", "SELECT JSON_EXTRACT(x, 1)", "SELECT JSON_EXTRACT(x, '$[1]')"},
		{"mysql", "SELECT DATE_FORMAT(CAST(x AS DATETIME), '%Y')",
			"SELECT DATE_FORMAT(CAST(x AS DATETIME), '%Y')"},
		{"mysql", "CREATE TABLE t (a INT, INDEX i (a DESC NULLS LAST))",
			"CREATE TABLE t (a INT, INDEX i (a DESC))"},
		// A Databricks colon path keeps its own separators, the first key's
		// colon and the later ones' dot.
		{"databricks", "SELECT x:a.b", "SELECT x:a.b"},
		{"databricks", "SELECT c1:item.price", "SELECT c1:item.price"},
		{"databricks", "SELECT x:a[0]", "SELECT x:a[0]"},
		// A FROM-first statement keeps the comment on its FROM.
		{"hive", "-- a\nFROM x INSERT OVERWRITE TABLE t SELECT k", "/* a */ FROM x INSERT OVERWRITE TABLE t SELECT k"},
	}
	written := 0
	for _, c := range cases {
		tree, err := ParseOne(c[1], c[0])
		if err != nil {
			t.Errorf("[%s] ParseOne(%q): %v", c[0], c[1], err)
			continue
		}
		got, err := Generate(tree, c[0])
		if err != nil {
			t.Errorf("[%s] Generate(%q): %v", c[0], c[1], err)
			continue
		}
		written++
		if got != c[2] {
			t.Errorf("[%s] %s\n  want %s\n  got  %s", c[0], c[1], c[2], got)
		}
	}
	if written != len(cases) {
		t.Errorf("wrote %d statements, want %d", written, len(cases))
	}
}

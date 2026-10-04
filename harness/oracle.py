"""The reference oracle: sqlglot's trees, dumped to JSON.

    python harness/oracle.py --sqlglot ~/opensource/sqlglot --out testdata/expected

Every statement in the reference corpus is parsed by the PYTHON sqlglot and
its tree written as JSON. The Go port is then verified against those files,
statement by statement, by `harness/diff_test.go`. The reference is the
oracle: where the two disagree, the port is wrong until shown otherwise.

Pinned to one sqlglot commit. Regenerating against a different one is a
deliberate change — it moves the target, and the diff that results is the
record of what moved.

The expected files are COMMITTED, so the Go tests need no Python at all: a Go
developer runs `go test ./...` and is measured against the reference without
installing it. Regeneration is the only step that needs sqlglot.
"""

from __future__ import annotations

# The reference iterates Python SETS in places that decide the ORDER a tree's
# keys are set in (a set operation's LIMIT/ORDER/OFFSET modifiers), and string
# hashing is randomised per process -- so the trees this harness records, and
# the tables it derives from them, would differ from run to run. Pinned here,
# for every way this script is started, rather than in a Makefile CI bypasses.
import os as _os
import sys as _sys

if _os.environ.get("PYTHONHASHSEED") != "0":
    _os.environ["PYTHONHASHSEED"] = "0"
    _os.execv(_sys.executable, [_sys.executable, *_sys.argv])

import argparse
import hashlib
import json
import pathlib
import subprocess
import sys

# Dialects the executor configures. sqlglot's per-dialect suites supply
# dialect-specific statements; identity.sql supplies the dialect-neutral core.
DIALECTS = ("tsql", "postgres", "duckdb", "databricks", "redshift", "materialize", "risingwave", "fabric", "presto", "trino", "dremio", "mysql")



# Statements chosen to reach the lexical corners sqlglot's own identity corpus
# does not: heredocs, bit and hex strings, numeric literal suffixes, nested and
# hinted comments, escape handling, \r\n line endings, non-ASCII identifiers.
# The reference still supplies the expectations -- these only decide WHICH
# statements get asked about, never what the right answer is.
EDGE_CORPUS: tuple[tuple[str, str], ...] = (
    ("", "SELECT 1 -- trailing\nFROM t"),
    ("", "SELECT /* leading */ 1 FROM t"),
    ("", "SELECT /*+ HINT(t) */ 1 FROM t"),
    ("", "SELECT 1\r\nFROM t\r\nWHERE x = 2"),
    ("", "SELECT 'it''s' FROM t"),
    ("", "SELECT 'a\\nb' FROM t"),
    ("", 'SELECT "a ""quoted"" id" FROM t'),
    ("", "SELECT 1.5, .5, 1e5, 1E-5, 1e+5 FROM t"),
    ("", "SELECT a.b.c FROM x.y.z"),
    ("", "SELECT * FROM t WHERE a <=> b"),
    # Null-safe comparison, promoted from a fuzz session by
    # harness/adjudicate.py. The neutral entry above did not reach it: `<=>`
    # is Databricks' spelling, and the form every dialect writes --
    # `IS [NOT] DISTINCT FROM` -- the port emitted and could not read back.
    # 96,096 findings, one cause.
    ("databricks", "SELECT * FROM t WHERE a <=> b"),
    ("databricks", "SELECT * FROM t WHERE a IS NOT DISTINCT FROM b"),
    ("postgres", "SELECT * FROM t WHERE a IS DISTINCT FROM b"),
    ("postgres", "SELECT * FROM t WHERE a IS NOT DISTINCT FROM b"),
    ("duckdb", "SELECT * FROM t WHERE a IS DISTINCT FROM b"),
    ("tsql", "SELECT * FROM t WHERE a IS NOT NULL"),
    ("", "SELECT * FROM t -- one\n-- two\nWHERE a = 1"),
    ("", "SELECT 1; SELECT 2"),
    ("", 'SELECT "\u00e9l\u00e8ve" FROM "caf\u00e9"'),
    ("", "SELECT COUNT(*) FROM t /* multi\nline */ WHERE x"),
    ("postgres", "SELECT $$a heredoc$$"),
    ("postgres", "SELECT $tag$a tagged heredoc$tag$"),
    ("postgres", "SELECT x'01af'"),
    ("postgres", "SELECT b'0101'"),
    ("postgres", "SELECT a::TEXT FROM t"),
    ("postgres", "SELECT x -> 'a' ->> 'b' FROM t"),
    ("postgres", "SELECT /* outer /* inner */ still outer */ 1"),
    ("postgres", "SELECT E'a\\nb'"),
    ("", "SELECT 'a\nb'"),
    ("", "SELECT 1 -- c\n; SELECT 2"),
    ("", "SELECT 1 /* c */ ; SELECT 2"),
    ("postgres", "SELECT E'a\\tb'"),
    ("postgres", "SELECT $1"),
    ("duckdb", "SELECT $1"),
    ("duckdb", "SELECT 'a\\nb'"),
    ("duckdb", "SELECT * FROM t WHERE x LIKE 'a\\_b'"),
    ("databricks", "SELECT 1abc"),
    ("databricks", "SELECT r'a\\nb'"),
    ("databricks", 'SELECT r"a\\nb"'),
    ("tsql", "SELECT @p.x"),
    ("tsql", "SELECT 0x1F, 1"),
    ("tsql", "SELECT 0x1F"),
    ("tsql", "SELECT 0xZZ"),
    ("postgres", "SELECT 0b1010"),
    ("postgres", "SELECT 0b12"),
    ("postgres", "SELECT 0x1_F"),
    ("databricks", "SELECT 0xFF"),
    ("duckdb", "SELECT 100_000"),
    ("duckdb", "SELECT 0x1F"),
    ("duckdb", "SELECT 0b1010"),
    ("duckdb", "SELECT 'a' || 'b'"),
    ("duckdb", "SELECT * FROM read_csv_auto('x.csv')"),
    ("tsql", "SELECT TOP 10 PERCENT a FROM t"),
    ("tsql", "SELECT [a b], [c]] d] FROM [my table]"),
    ("tsql", "SELECT N'unicode' FROM t"),
    ("tsql", "SELECT @p FROM t"),
    ("tsql", "SELECT 1 FROM t CROSS APPLY f(1)"),
    ("databricks", "SELECT 1L, 2S, 3Y, 4BD, 5D, 6F"),
    ("databricks", "SELECT `a b` FROM `c d`"),
    ("databricks", "SELECT * FROM t /* nested /* comment */ here */"),
    # Bare IF (no parens) at the very start of a statement is not a function
    # call, and Databricks has no real IF statement (unlike T-SQL) -- the
    # reference gives up and falls back to a Command. Promoted from a fuzz
    # session by harness/adjudicate.py: the minimized finding was
    # `IF>''->'\x0b\x0b\x0b'`, which the port misread as a JSON arrow path.
    ("databricks", "IF>''->'x'"),
    ("databricks", "IF 1 THEN 2 ELSE 3"),
)


# DAX's suite is validate_transpile, which corpus_dialect does not read.
# A statement is listed here once the port reads it. The reference still
# supplies the tree and the rendering.
DAX_CORPUS: tuple[tuple[str, str], ...] = (
    ("dax", "EVALUATE Sales"),
    ("dax", "EVALUATE 'Sales Data'"),
    ("dax", "EVALUATE FILTER(Sales, Sales[Amount] > 100)"),
    ("dax", "EVALUATE FILTER('Sales Data', 'Sales Data'[Amount] > 100)"),
    ("dax", "EVALUATE FILTER(Sales, [Total Amount] > 100)"),
    ("dax", "EVALUATE FILTER(Sales, Sales[Amount] > 100 && Sales[Qty] < 5)"),
    ("dax", "EVALUATE FILTER(Sales, Sales[Amount] > 100 || Sales[Qty] < 5)"),
    ("dax", 'EVALUATE FILTER(FILTER(Sales, Sales[Amount] > 100), Sales[Region] = "West")'),
    ("dax", 'EVALUATE FILTER(Sales, Sales[Note] = "He said ""hi""")'),
    ("dax", "EVALUATE Sales ORDER BY Sales[Amount] DESC, Sales[Qty]"),
    ("dax", "EVALUATE FILTER(Sales, Sales[Amount] > 100) ORDER BY Sales[Amount]"),
    ("dax", 'EVALUATE ADDCOLUMNS(Sales, "x", 1)'),
    ("dax", "EVALUATE SUMMARIZE(Sales, Sales[Region])"),
    ("dax", 'EVALUATE FILTER(ADDCOLUMNS(Sales, "x", 1), Sales[Amount] > 1)'),
    ("dax", 'EVALUATE FILTER(Sales, Sales[Note] = N"He said ""hi""")'),
    ("dax", """EVALUATE FILTER(Sales, Sales[Note] = N"it's")"""),
    ("dax", 'EVALUATE FILTER(Sales, Sales[Region] IN {"West", "East"})'),
    ("dax", 'EVALUATE DATATABLE("a", STRING, {{"x"}})'),
)

# Statements the reference accepts as Oracle and this port already
# reads and writes back. The rest of tests/dialects/test_oracle.py
# stays out until each mechanism lands.
ORACLE_CORPUS: tuple[tuple[str, str], ...] = (
    ("oracle", "SELECT UNIQUE col1, col2 FROM table"),
    ("oracle", "SELECT fred FROM barney WHERE dino ^= 'wilma'"),
    ("oracle", "1 /* /* */"),
    ("oracle", "SELECT e1.x, e2.x FROM e e1, e e2 WHERE e1.y (+) = e2.y"),
    ("oracle", "SELECT e1.x, e2.x FROM e e1, e e2 WHERE e1.y = e2.y (+)"),
    ("oracle", "NVL(NULL, 1)"),
    ("oracle", "SELECT * FROM table_name SAMPLE (25) s"),
    ("oracle", "SELECT COUNT(*) * 10 FROM orders SAMPLE (10) SEED (1)"),
    ("oracle", "SELECT * FROM t SAMPLE (.25)"),
    ("oracle", "SELECT CAST(NULL AS VARCHAR2(2328 CHAR)) AS COL1"),
    ("oracle", "SELECT CAST(NULL AS VARCHAR2(2328 BYTE)) AS COL1"),
    ("oracle", "SYSDATE"),
    ("oracle", "SELECT a$x#b"),
    ("oracle", "SELECT * FROM table_name@dblink_name.database_link_domain"),
    ("oracle", "SELECT * FROM V$SESSION"),
    ("oracle", "SELECT * FROM t ORDER BY a ASC NULLS LAST, b ASC NULLS FIRST, c DESC NULLS LAST, d DESC NULLS FIRST"),
    ("oracle", "SELECT TO_DATE('January 15, 1989, 11:00 A.M.')"),
    ("oracle", "SELECT TO_DATE('2024-12-12', 'YYYY-MM-DD')"),
    ("oracle", "SELECT TO_DATE('January 15, 1989, 11:00 A.M.', 'Month dd, YYYY, HH12:MI A.M.') FROM DUAL"),
    ("oracle", "UTC_TIME()"),
    ("oracle", "UTC_TIME(6)"),
    ("oracle", "UTC_TIMESTAMP()"),
    ("oracle", "UTC_TIMESTAMP(6)"),
    ("oracle", "CURRENT_TIMESTAMP BETWEEN TO_DATE(f.C_SDATE, 'YYYY/MM/DD') AND TO_DATE(f.C_EDATE, 'YYYY/MM/DD')"),
    ("oracle", "x::binary_double"),
    ("oracle", "x::binary_float"),
    ("oracle", "CREATE GLOBAL TEMPORARY TABLE t AS SELECT * FROM orders"),
    ("oracle", "CREATE PRIVATE TEMPORARY TABLE t AS SELECT * FROM orders"),
    ("oracle", "CREATE OR REPLACE FORCE VIEW foo1.foo2"),
    ("oracle", "SELECT INSTR(haystack, needle)"),
    ("oracle", "SELECT * FROM test WHERE MOD(col1, 4) = 3"),
    ("oracle", "SELECT TRIM('|' FROM '||Hello ||| world||')"),
    ("oracle", "TRIM(BOTH 'h' FROM 'Hello World')"),
    ("oracle", "SELECT CHR(187 USING NCHAR_CS)"),
    ("oracle", "SELECT * FROM T ORDER BY I OFFSET NVL(:variable1, 10) ROWS FETCH NEXT NVL(:variable2, 10) ROWS ONLY"),
    ("oracle", "SELECT CAST(1 AS DECIMAL)"),
    ("oracle", "ALTER TABLE Payments ADD Stock NUMBER NOT NULL"),
    ("oracle", "CONVERT('foo', 'dst')"),
    ("oracle", "CONVERT('foo', 'dst', 'src')"),
    ("oracle", "TIMESTAMP(3) WITH TIME ZONE"),
    ("oracle", "DATE '2022-01-01'"),
    ("oracle", "ALTER TABLE tbl_name DROP FOREIGN KEY fk_symbol"),
    ("oracle", "ALTER TABLE Payments ADD (Stock NUMBER NOT NULL, dropid VARCHAR2(500) NOT NULL)"),
    ("oracle", "MERGE INTO target tgt USING (SELECT id, col1 FROM source_tbl) src ON tgt.id = src.id WHEN MATCHED THEN UPDATE SET tgt.col1 = src.col1 WHERE tgt.some_column IS NULL WHEN NOT MATCHED THEN INSERT (id, col1) VALUES (src.id, src.col1) WHERE NOT src.col1 IS NULL"),
    ("oracle", "SELECT LISTAGG(last_name, '; ' ON OVERFLOW TRUNCATE '...' WITH COUNT) WITHIN GROUP (ORDER BY hire_date) FROM employees"),
    ("oracle", "SELECT LISTAGG(last_name, '; ' ON OVERFLOW ERROR) WITHIN GROUP (ORDER BY hire_date) FROM employees"),
    ("oracle", "GRANT EXECUTE ON PROCEDURE p TO george"),
    ("oracle", "GRANT USAGE ON SEQUENCE order_id TO sales_role"),
    ("oracle", "REVOKE EXECUTE ON PROCEDURE p FROM george"),
    ("oracle", "REVOKE USAGE ON SEQUENCE order_id FROM sales_role"),
    ("oracle", "TO_NUMBER(x)"),
    ("oracle", "TO_NUMBER(expr, fmt, nlsparam)"),
    ("oracle", "TO_NUMBER('dino' DEFAULT 0 ON CONVERSION ERROR)"),
    ("oracle", "TO_NUMBER('dino' DEFAULT 0 ON CONVERSION ERROR, '9999')"),
    ("oracle", "TO_NUMBER('dino' DEFAULT 0 ON CONVERSION ERROR, '9999', 'NLS_NUMERIC_CHARACTERS = ''.,''')"),
    ("oracle", "SELECT x FROM t WHERE cond FOR UPDATE"),
    ("oracle", "SELECT * FROM t FOR UPDATE"),
    ("oracle", "SELECT * FROM t FOR UPDATE WAIT 5"),
    ("oracle", "SELECT * FROM t FOR UPDATE NOWAIT"),
    ("oracle", "SELECT * FROM t FOR UPDATE SKIP LOCKED"),
    ("oracle", "SELECT * FROM t FOR UPDATE OF s.t.c, s.t.v"),
    ("oracle", "SELECT * FROM t FOR UPDATE OF s.t.c, s.t.v NOWAIT"),
    ("oracle", "SELECT * FROM t FOR UPDATE OF s.t.c, s.t.v SKIP LOCKED"),
    ("oracle", "SELECT * FROM consumer LEFT JOIN groceries ON consumer.groceries_id = consumer.id PIVOT(MAX(type_id) FOR consumer_type IN (1, 2, 3, 4))"),
    ("oracle", "SELECT * FROM test UNPIVOT INCLUDE NULLS (value FOR Description IN (col AS 'PREFIX ' || CHR(38) || ' SUFFIX'))"),
    ("oracle", "SELECT * FROM sales UNPIVOT(q FOR p IN (q1 AS 'Prod1', q2 AS 'Prod2'))"),
    ("oracle", "SELECT * FROM sales UNPIVOT(q FOR p IN (q1 AS 1, q2 AS 2))"),
    ("oracle", "SELECT * FROM t UNPIVOT(revenue FOR month IN (t.jan, t.feb)) AS u"),
    ("oracle", "SELECT * FROM t PIVOT(SUM(t.val) FOR t.cat IN ('a' AS a)) AS p"),
    ("oracle", "ANALYZE TABLE tbl"),
    ("oracle", "ANALYZE INDEX ndx"),
    ("oracle", "ANALYZE TABLE db.tbl PARTITION(foo = 'foo', bar = 'bar')"),
    ("oracle", "ANALYZE TABLE db.tbl SUBPARTITION(foo = 'foo', bar = 'bar')"),
    ("oracle", "ANALYZE INDEX db.ndx PARTITION(foo = 'foo', bar = 'bar')"),
    ("oracle", "ANALYZE INDEX db.ndx PARTITION(part1)"),
    ("oracle", "ANALYZE CLUSTER db.cluster"),
    ("oracle", "ANALYZE TABLE tbl VALIDATE REF UPDATE"),
    ("oracle", "ANALYZE LIST CHAINED ROWS"),
    ("oracle", "ANALYZE LIST CHAINED ROWS INTO tbl"),
    ("oracle", "ANALYZE DELETE STATISTICS"),
    ("oracle", "ANALYZE DELETE SYSTEM STATISTICS"),
    ("oracle", "ANALYZE VALIDATE REF UPDATE"),
    ("oracle", "ANALYZE VALIDATE REF UPDATE SET DANGLING TO NULL"),
    ("oracle", "ANALYZE VALIDATE STRUCTURE"),
    ("oracle", "ANALYZE VALIDATE STRUCTURE CASCADE FAST"),
    ("oracle", "ANALYZE TABLE tbl VALIDATE STRUCTURE CASCADE COMPLETE ONLINE INTO db.tbl"),
    ("oracle", "ANALYZE TABLE tbl VALIDATE STRUCTURE CASCADE COMPLETE OFFLINE INTO db.tbl"),
    ("oracle", "SELECT * FROM sales_history MATCH_RECOGNIZE (PARTITION BY product ORDER BY tstamp MEASURES STRT.tstamp AS start_tstamp, LAST(UP.tstamp) AS peak_tstamp, LAST(DOWN.tstamp) AS end_tstamp, MATCH_NUMBER() AS mno ONE ROW PER MATCH AFTER MATCH SKIP TO LAST DOWN PATTERN (STRT UP+ FLAT* DOWN+) DEFINE UP AS UP.units_sold > PREV(UP.units_sold), FLAT AS FLAT.units_sold = PREV(FLAT.units_sold), DOWN AS DOWN.units_sold < PREV(DOWN.units_sold)) MR"),
    ("oracle", "SELECT /*+ ORDERED */* FROM tbl"),
    ("oracle", "SELECT /*+ ORDERED */ * FROM tbl"),
    ("oracle", "SELECT /* test */ /*+ ORDERED */* FROM tbl"),
    ("oracle", "/* test */ SELECT /*+ ORDERED */ * FROM tbl"),
    ("oracle", "SELECT /*+ USE_NL(A B) */ A.COL_TEST FROM TABLE_A A, TABLE_B B"),
    ("oracle", "SELECT /*+ INDEX(v.j jhist_employee_ix (employee_id start_date)) */ * FROM v"),
    ("oracle", "SELECT /*+ USE_NL(A B C) */ A.COL_TEST FROM TABLE_A A, TABLE_B B, TABLE_C C"),
    ("oracle", "SELECT /*+ NO_INDEX(employees emp_empid) */ employee_id FROM employees WHERE employee_id > 200"),
    ("oracle", "SELECT /*+ NO_INDEX_FFS(items item_order_ix) */ order_id FROM order_items items"),
    ("oracle", "SELECT /*+ LEADING(e j) */ * FROM employees e, departments d, job_history j WHERE e.department_id = d.department_id AND e.hire_date = j.start_date"),
    ("oracle", "SELECT /*+ LEADING(departments employees) USE_NL(employees) */ * FROM employees JOIN departments ON employees.department_id = departments.department_id"),
    ("oracle", "SELECT /*+ USE_NL(bbbbbbbbbbbbbbbbbbbbbbbb) LEADING(aaaaaaaaaaaaaaaaaaaaaaaa bbbbbbbbbbbbbbbbbbbbbbbb cccccccccccccccccccccccc dddddddddddddddddddddddd) INDEX(cccccccccccccccccccccccc) */ * FROM aaaaaaaaaaaaaaaaaaaaaaaa JOIN bbbbbbbbbbbbbbbbbbbbbbbb ON aaaaaaaaaaaaaaaaaaaaaaaa.id = bbbbbbbbbbbbbbbbbbbbbbbb.a_id JOIN cccccccccccccccccccccccc ON bbbbbbbbbbbbbbbbbbbbbbbb.id = cccccccccccccccccccccccc.b_id JOIN dddddddddddddddddddddddd ON cccccccccccccccccccccccc.id = dddddddddddddddddddddddd.c_id"),
    ("oracle", "SELECT /*+ USE_NL(bbbbbbbbbbbbbbbbbbbbbbbb) LEADING( aaaaaaaaaaaaaaaaaaaaaaaa bbbbbbbbbbbbbbbbbbbbbbbb cccccccccccccccccccccccc dddddddddddddddddddddddd ) INDEX(cccccccccccccccccccccccc) */ * FROM aaaaaaaaaaaaaaaaaaaaaaaa JOIN bbbbbbbbbbbbbbbbbbbbbbbb ON aaaaaaaaaaaaaaaaaaaaaaaa.id = bbbbbbbbbbbbbbbbbbbbbbbb.a_id JOIN cccccccccccccccccccccccc ON bbbbbbbbbbbbbbbbbbbbbbbb.id = cccccccccccccccccccccccc.b_id JOIN dddddddddddddddddddddddd ON cccccccccccccccccccccccc.id = dddddddddddddddddddddddd.c_id"),
    ("oracle", "SELECT /*+ LEADING(departments employees) USE_NL(employees) select where group by is order by */ * FROM employees JOIN departments ON employees.department_id = departments.department_id"),
    ("oracle", "SELECT /*+ LEADING(departments, employees) */ * FROM employees JOIN departments ON employees.department_id = departments.department_id"),
    ("oracle", "SELECT /*+ LEADING(departments select) */ * FROM employees JOIN departments ON employees.department_id = departments.department_id"),
    ("oracle", "SELECT NTH_VALUE(x, 2) FROM FIRST OVER (ORDER BY y) AS c FROM t"),
    ("oracle", "SELECT NTH_VALUE(x, 2) FROM LAST IGNORE NULLS OVER (ORDER BY y) AS c FROM t"),
    ("oracle", "SELECT NTH_VALUE(x, 2) FROM FIRST RESPECT NULLS OVER (ORDER BY y) AS c FROM t"),
    ("oracle", "SELECT NTH_VALUE(x, 2) FROM LAST OVER (ORDER BY y) AS c FROM t"),
    ("oracle", "SELECT (TIMESTAMP '2025-12-30 20:00:00' - TIMESTAMP '2025-12-29 14:30:00') DAY TO SECOND"),
    ("oracle", "SELECT (SYSTIMESTAMP - order_date) DAY(9) TO SECOND FROM orders"),
    ("oracle", "SELECT (SYSTIMESTAMP - order_date) DAY(9) TO SECOND(3) FROM orders"),
    ("oracle", "SELECT MIN(column_name) KEEP (DENSE_RANK FIRST ORDER BY column_name DESC) FROM table_name"),
    ("oracle", "SELECT last_name, department_id, salary, MIN(salary) KEEP (DENSE_RANK FIRST ORDER BY commission_pct) OVER (PARTITION BY department_id) AS \"Worst\", MAX(salary) KEEP (DENSE_RANK LAST ORDER BY commission_pct) OVER (PARTITION BY department_id) AS \"Best\" FROM employees"),
    ("oracle", "SELECT TRUNC(SYSDATE)"),
    ("oracle", "XMLELEMENT(EVALNAME foo + bar)"),
    ("oracle", "XMLELEMENT(\"ImageID\", image.id)"),
    ("oracle", "SELECT x.* FROM example t, XMLTABLE(XMLNAMESPACES(DEFAULT 'http://example.com/default', 'http://example.com/ns1' AS \"ns1\"), '/root/data' PASSING t.xml COLUMNS id NUMBER PATH '@id', value VARCHAR2(100) PATH 'ns1:value/text()') x"),
    ("oracle", "SELECT warehouse_name warehouse, warehouse2.\"Water\", warehouse2.\"Rail\" FROM warehouses, XMLTABLE('/Warehouse' PASSING warehouses.warehouse_spec COLUMNS \"Water\" varchar2(6) PATH 'WaterAccess', \"Rail\" varchar2(6) PATH 'RailAccess') warehouse2"),
    ("oracle", "SELECT table_name, column_name, data_default FROM xmltable('ROWSET/ROW' passing dbms_xmlgen.getxmltype('SELECT table_name, column_name, data_default FROM user_tab_columns') columns table_name VARCHAR2(128) PATH '*[1]', column_name VARCHAR2(128) PATH '*[2]', data_default VARCHAR2(2000) PATH '*[3]')"),
    ("oracle", "XMLTABLE('x')"),
    ("oracle", "XMLTABLE('x' RETURNING SEQUENCE BY REF)"),
    ("oracle", "XMLTABLE('x' PASSING y)"),
    ("oracle", "XMLTABLE('x' PASSING y RETURNING SEQUENCE BY REF)"),
    ("oracle", "XMLTABLE('x' RETURNING SEQUENCE BY REF COLUMNS a VARCHAR2, b FLOAT)"),
    ("oracle", "SELECT CONNECT_BY_ROOT x y"),
    ("oracle", "SELECT * FROM t START WITH col CONNECT BY NOCYCLE PRIOR col1 = col2"),
    ("oracle", "SELECT id FROM t START WITH (parent_id IS NULL) CONNECT BY PRIOR id = parent_id"),
    ("oracle", "SELECT id FROM t START WITH (x) CONNECT BY PRIOR id = parent_id"),
    ("oracle", "SELECT id, PRIOR name AS parent_name, name FROM tree CONNECT BY NOCYCLE PRIOR id = parent_id"),
    ("oracle", "SELECT last_name, employee_id, manager_id, LEVEL FROM employees START WITH employee_id = 100 CONNECT BY PRIOR employee_id = manager_id ORDER SIBLINGS BY last_name"),
    ("oracle", "SELECT TO_CHAR(-100, 'L99', 'NL_CURRENCY = '' AusDollars '' ')"),
    ("oracle", "TO_CHAR(x)"),
    ("oracle", 'INSERT ALL INTO dest_tab1 (id, description) VALUES (id, description) INTO dest_tab2 (id, description) VALUES (id, description) INTO dest_tab3 (id, description) VALUES (id, description) SELECT id, description FROM source_tab'),
    ("oracle", "INSERT ALL INTO pivot_dest (id, day, val) VALUES (id, 'mon', mon_val) INTO pivot_dest (id, day, val) VALUES (id, 'tue', tue_val) INTO pivot_dest (id, day, val) VALUES (id, 'wed', wed_val) INTO pivot_dest (id, day, val) VALUES (id, 'thu', thu_val) INTO pivot_dest (id, day, val) VALUES (id, 'fri', fri_val) SELECT * FROM pivot_source"),
    ("oracle", 'INSERT ALL WHEN id <= 3 THEN INTO dest_tab1 (id, description) VALUES (id, description) WHEN id BETWEEN 4 AND 7 THEN INTO dest_tab2 (id, description) VALUES (id, description) WHEN id >= 8 THEN INTO dest_tab3 (id, description) VALUES (id, description) SELECT id, description FROM source_tab'),
    ("oracle", 'INSERT ALL WHEN id <= 3 THEN INTO dest_tab1 (id, description) VALUES (id, description) WHEN id BETWEEN 4 AND 7 THEN INTO dest_tab2 (id, description) VALUES (id, description) WHEN 1 = 1 THEN INTO dest_tab3 (id, description) VALUES (id, description) SELECT id, description FROM source_tab'),
    ("oracle", 'INSERT FIRST WHEN id <= 3 THEN INTO dest_tab1 (id, description) VALUES (id, description) WHEN id <= 5 THEN INTO dest_tab2 (id, description) VALUES (id, description) ELSE INTO dest_tab3 (id, description) VALUES (id, description) SELECT id, description FROM source_tab'),
    ("oracle", 'INSERT FIRST WHEN id <= 3 THEN INTO dest_tab1 (id, description) VALUES (id, description) ELSE INTO dest_tab2 (id, description) VALUES (id, description) INTO dest_tab3 (id, description) VALUES (id, description) SELECT id, description FROM source_tab'),
    ("oracle", '/* COMMENT */ INSERT FIRST WHEN salary > 4000 THEN INTO emp2 WHEN salary > 5000 THEN INTO emp3 WHEN salary > 6000 THEN INTO emp4 SELECT salary FROM employees'),
    ("oracle", "SELECT department_id BULK COLLECT INTO v_department_ids FROM departments"),
    ("oracle", "SELECT department_id, department_name BULK COLLECT INTO v_department_ids, v_department_names FROM departments"),
    ("oracle", "SELECT * FROM JSON_TABLE(foo FORMAT JSON, 'bla' ERROR ON ERROR NULL ON EMPTY COLUMNS(foo PATH 'bar'))"),
    ("oracle", "SELECT\n  CASE WHEN DBMS_LOB.GETLENGTH(info) < 32000 THEN DBMS_LOB.SUBSTR(info) END AS info_txt,\n  info AS info_clob\nFROM schemaname.tablename ar\nINNER JOIN JSON_TABLE(:emps, '$[*]' COLUMNS(empno NUMBER PATH '$')) jt\n  ON ar.empno = jt.empno"),
    ("oracle", "SELECT * FROM JSON_TABLE(my_doc, '$.data[*]' COLUMNS(NAME VARCHAR2(200) PATH '$.name', DATA CLOB FORMAT JSON PATH '$.data')) j"),
    ("oracle", "SELECT JSON_ARRAYAGG(FOO() FORMAT JSON ORDER BY bar NULL ON NULL RETURNING CLOB STRICT)"),
    ("oracle", "SELECT /*+ ORDERED */*/* test */ FROM tbl"),
    ("oracle", "SELECT department_id, department_name INTO v_department_id, v_department_name FROM departments FETCH FIRST 1 ROWS ONLY"),
    ("oracle", "INSERT /*+ APPEND */ INTO IAP_TBL (id, col1) VALUES (2, 'test2')"),
    ("oracle", "INSERT /*+ APPEND_VALUES */ INTO dest_table VALUES (i, 'Value')"),
    ("oracle", "INSERT /*+ APPEND(d) */ INTO dest d VALUES (i, 'Value')"),
    ("oracle", "INSERT /*+ APPEND(d) */ INTO dest d (i, value) SELECT 1, 'value' FROM dual"),
    ("oracle", "SELECT JSON_OBJECT(k1: v1 FORMAT JSON, k2: v2 FORMAT JSON)"),
    ("oracle", "SELECT JSON_OBJECT(KEY 'key1' IS emp.column1, KEY 'key2' IS emp.column1) \"emp_key\" FROM emp"),
    ("oracle", "CAST(value AS NUMBER DEFAULT 0 ON CONVERSION ERROR)"),
    ("oracle", "SELECT CAST('January 15, 1989, 11:00 A.M.' AS DATE DEFAULT NULL ON CONVERSION ERROR, 'Month dd, YYYY, HH:MI A.M.') FROM DUAL"),
    ("oracle", "SELECT JSON_ARRAY(FOO() FORMAT JSON, BAR() NULL ON NULL RETURNING CLOB STRICT)"),
    ("oracle", "SELECT * FROM JSON_TABLE(foo FORMAT JSON, 'bla' ERROR ON ERROR NULL ON EMPTY COLUMNS foo PATH 'bar')"),
    ("oracle", "SELECT\n  *\nFROM JSON_TABLE(res, '$.info[*]' COLUMNS(\n  tempid NUMBER PATH '$.tempid',\n  NESTED PATH '$.calid[*]' COLUMNS(last_dt PATH '$.last_dt ')\n)) src"),
    ("oracle", "SELECT JSON_OBJECTAGG(KEY department_name VALUE department_id) FROM dep WHERE id <= 30"),
)


# Statements the reference accepts as Snowflake and this port already
# reads and writes back. The rest of tests/dialects/test_snowflake.py
# stays out until each mechanism lands.
SNOWFLAKE_CORPUS: tuple[tuple[str, str], ...] = (
    ("snowflake", 'SELECT session'),
    ("snowflake", 'SELECT HASH_AGG(a, b, c, d)'),
    ("snowflake", 'SELECT MAX(x)'),
    ("snowflake", 'SELECT COUNT(x)'),
    ("snowflake", 'SELECT MIN(amount)'),
    ("snowflake", 'SELECT MODE(x)'),
    ("snowflake", 'SELECT MODE(status) OVER (PARTITION BY region) FROM orders'),
    ("snowflake", 'SELECT MODE(x) FROM t'),
    ("snowflake", 'SELECT TAN(x)'),
    ("snowflake", 'SELECT COS(x)'),
    ("snowflake", 'SELECT SINH(1.5)'),
    ("snowflake", 'SELECT MOD(x, y)'),
    ("snowflake", 'SELECT FLOOR(x)'),
    ("snowflake", 'SELECT FLOOR(135.135, 1)'),
    ("snowflake", 'SELECT FLOOR(x, -1)'),
    ("snowflake", 'SELECT APPROX_TOP_K(category, 3) FROM t'),
    ("snowflake", 'SELECT MINHASH(5, col)'),
    ("snowflake", 'SELECT MINHASH(5, col1, col2)'),
    ("snowflake", 'SELECT MINHASH(5, *)'),
    ("snowflake", 'SELECT MINHASH_COMBINE(minhash_col)'),
    ("snowflake", 'SELECT APPROXIMATE_SIMILARITY(minhash_col)'),
    ("snowflake", 'SELECT APPROXIMATE_JACCARD_INDEX(minhash_col)'),
    ("snowflake", 'SELECT APPROX_PERCENTILE_ACCUMULATE(col)'),
    ("snowflake", 'SELECT APPROX_PERCENTILE_ESTIMATE(state, 0.5)'),
    ("snowflake", 'SELECT APPROX_TOP_K_ACCUMULATE(col, 10)'),
    ("snowflake", 'SELECT APPROX_TOP_K_COMBINE(state, 2)'),
    ("snowflake", 'SELECT APPROX_TOP_K_COMBINE(state)'),
    ("snowflake", 'SELECT APPROX_TOP_K_ESTIMATE(state_column, 4)'),
    ("snowflake", 'SELECT APPROX_TOP_K_ESTIMATE(state_column)'),
    ("snowflake", 'SELECT APPROX_PERCENTILE_COMBINE(state_column)'),
    ("snowflake", 'SELECT EQUAL_NULL(1, 2)'),
    ("snowflake", 'SELECT EXP(1)'),
    ("snowflake", 'SELECT FACTORIAL(5)'),
    ("snowflake", "SELECT BIT_LENGTH('abc')"),
    ("snowflake", 'SELECT BITMAP_BIT_POSITION(10)'),
    ("snowflake", 'SELECT BITMAP_BUCKET_NUMBER(32769)'),
    ("snowflake", 'SELECT BITMAP_CONSTRUCT_AGG(value)'),
    ("snowflake", 'SELECT BITMAP_CONSTRUCT_AGG(v) FROM t'),
    ("snowflake", "SELECT TO_BOOLEAN('true')"),
    ("snowflake", 'SELECT TO_BOOLEAN(1)'),
    ("snowflake", 'SELECT TO_VARIANT(123)'),
    ("snowflake", "SELECT RTRIMMED_LENGTH(' ABCD ')"),
    ("snowflake", "SELECT HEX_DECODE_STRING('48656C6C6F')"),
    ("snowflake", 'SELECT IFNULL(col1, col2)'),
    ("snowflake", "SELECT NEXT_DAY('2025-10-15', 'FRIDAY')"),
    ("snowflake", 'SELECT NVL2(col1, col2, col3)'),
    ("snowflake", 'SELECT NVL(col1, col2)'),
    ("snowflake", 'SELECT CHR(8364)'),
    ("snowflake", 'SELECT CHECK_JSON(x)'),
    ("snowflake", 'SELECT CHECK_JSON(\'{"key": "value"}\')'),
    ("snowflake", 'SELECT CHECK_XML(\'<root><key attribute="attr">value</key></root>\')'),
    ("snowflake", 'SELECT CHECK_XML(\'<root><key attribute="attr">value</key></root>\', TRUE)'),
    ("snowflake", "SELECT COMPRESS('Hello World', 'ZLIB')"),
    ("snowflake", "SELECT DECOMPRESS_BINARY('compressed_data', 'SNAPPY')"),
    ("snowflake", "SELECT DECOMPRESS_STRING('compressed_data', 'ZSTD')"),
    ("snowflake", "SELECT LPAD('Hello', 10, '*')"),
    ("snowflake", 'SELECT LPAD(tbl.bin_col, 10)'),
    ("snowflake", "SELECT RPAD('Hello', 10, '*')"),
    ("snowflake", 'SELECT RPAD(tbl.bin_col, 10)'),
    ("snowflake", "SELECT RPAD('test', 10, 'ab')"),
    ("snowflake", "SELECT RPAD('data', 8)"),
    ("snowflake", "SELECT RPAD('exact', 5, '*')"),
    ("snowflake", "SELECT RPAD(TO_BINARY('Hi', 'UTF8'), 10, TO_BINARY('_', 'UTF8'))"),
    ("snowflake", 'SELECT SOUNDEX(column_name)'),
    ("snowflake", 'SELECT SOUNDEX_P123(column_name)'),
    ("snowflake", 'SELECT ABS(x)'),
    ("snowflake", 'SELECT ASIN(0.5)'),
    ("snowflake", 'SELECT ASINH(0.5)'),
    ("snowflake", 'SELECT ATAN(0.5)'),
    ("snowflake", 'SELECT ATAN2(0.5, 0.3)'),
    ("snowflake", 'SELECT ATANH(0.5)'),
    ("snowflake", 'SELECT CBRT(27.0)'),
    ("snowflake", 'SELECT POW(2, 3)'),
    ("snowflake", 'SELECT POW(2.5, 3.0)'),
    ("snowflake", 'SELECT SIGN(x)'),
    ("snowflake", 'SELECT COSH(1.5)'),
    ("snowflake", 'SELECT TANH(0.5)'),
    ("snowflake", "SELECT TRANSLATE(column_name, 'abc', '123')"),
    ("snowflake", 'SELECT WIDTH_BUCKET(col, 0, 100, 10)'),
    ("snowflake", 'SELECT PI()'),
    ("snowflake", 'SELECT DEGREES(PI() / 3)'),
    ("snowflake", 'SELECT DEGREES(1)'),
    ("snowflake", 'SELECT RADIANS(180)'),
    ("snowflake", 'SELECT REGR_VALX(y, x)'),
    ("snowflake", 'SELECT REGR_VALY(y, x)'),
    ("snowflake", 'SELECT REGR_AVGX(y, x)'),
    ("snowflake", 'SELECT REGR_AVGY(y, x)'),
    ("snowflake", 'SELECT REGR_COUNT(y, x)'),
    ("snowflake", 'SELECT REGR_INTERCEPT(y, x)'),
    ("snowflake", 'SELECT REGR_R2(y, x)'),
    ("snowflake", 'SELECT REGR_SXX(y, x)'),
    ("snowflake", 'SELECT REGR_SXY(y, x)'),
    ("snowflake", 'SELECT REGR_SYY(y, x)'),
    ("snowflake", 'SELECT REGR_SLOPE(y, x)'),
    ("snowflake", "SELECT IS_ARRAY(PARSE_JSON('[1,2,3]'))"),
    ("snowflake", 'SELECT VAR_SAMP(x)'),
    ("snowflake", 'SELECT VAR_POP(x)'),
    ("snowflake", 'SELECT RANDSTR(123, 456)'),
    ("snowflake", 'IS_NULL_VALUE(x)'),
    ("snowflake", 'SELECT RANDSTR(10, 123)'),
    ("snowflake", 'SELECT ZIPF(1, 10, 1234)'),
    ("snowflake", 'SELECT GROUPING_ID(a, b) AS g_id FROM x GROUP BY ROLLUP (a, b)'),
    ("snowflake", "PARSE_URL('https://example.com/path')"),
    ("snowflake", "SELECT XMLGET(object_col, 'level2')"),
    ("snowflake", "SELECT XMLGET(object_col, 'level3', 1)"),
    ("snowflake", 'SELECT a, b, COUNT(*) FROM x GROUP BY ALL LIMIT 100'),
    ("snowflake", "STRTOK_TO_ARRAY('a.b.c', '.')"),
    ("snowflake", "INTERVAL '4 years, 5 months, 3 hours'"),
    ("snowflake", 'SELECT rename, replace'),
    ("snowflake", 'SELECT TO_TIMESTAMP(x) FROM t'),
    ("snowflake", 'SELECT TO_TIMESTAMP_NTZ(x) FROM t'),
    ("snowflake", 'SELECT TO_TIMESTAMP_LTZ(x) FROM t'),
    ("snowflake", 'SELECT TO_TIMESTAMP_TZ(x) FROM t'),
    ("snowflake", "TO_DECFLOAT('123.456')"),
    ("snowflake", "TO_DECFLOAT('1,234.56', '999,999.99')"),
    ("snowflake", "TRY_TO_DECFLOAT('123.456')"),
    ("snowflake", "TRY_TO_DECFLOAT('1,234.56', '999,999.99')"),
    ("snowflake", 'TO_FILE(object_col)'),
    ("snowflake", "TO_FILE('file.csv')"),
    ("snowflake", "TO_FILE('file.csv', 'relativepath/')"),
    ("snowflake", 'ALTER TABLE authors ADD CONSTRAINT c1 UNIQUE (id, email)'),
    ("snowflake", 'SELECT TIMESTAMP_FROM_PARTS(2024, 5, 9, 14, 30, 45)'),
    ("snowflake", 'SELECT TIMESTAMP_FROM_PARTS(2024, 5, 9, 14, 30, 45, 123)'),
    ("snowflake", 'SELECT TIMESTAMP_LTZ_FROM_PARTS(2013, 4, 5, 12, 00, 00)'),
    ("snowflake", 'SELECT TIMESTAMP_TZ_FROM_PARTS(2013, 4, 5, 12, 00, 00)'),
    ("snowflake", "SELECT TIMESTAMP_TZ_FROM_PARTS(2013, 4, 5, 12, 00, 00, 0, 'America/Los_Angeles')"),
    ("snowflake", 'SELECT ARRAY_UNIQUE_AGG(x)'),
    ("snowflake", 'SELECT ARRAY_UNIQUE_AGG(col) FROM t'),
    ("snowflake", 'SELECT ARRAY_UNIQUE_AGG(col) OVER (PARTITION BY grp) FROM t'),
    ("snowflake", "SELECT AI_AGG(review, 'Summarize the reviews')"),
    ("snowflake", 'SELECT AI_SUMMARIZE_AGG(review)'),
    ("snowflake", 'SELECT CURRENT_ACCOUNT()'),
    ("snowflake", 'SELECT CURRENT_ACCOUNT_NAME()'),
    ("snowflake", 'SELECT CURRENT_AVAILABLE_ROLES()'),
    ("snowflake", 'SELECT CURRENT_CLIENT()'),
    ("snowflake", 'SELECT CURRENT_IP_ADDRESS()'),
    ("snowflake", 'SELECT CURRENT_DATABASE()'),
    ("snowflake", 'SELECT CURRENT_SCHEMAS()'),
    ("snowflake", 'SELECT CURRENT_SECONDARY_ROLES()'),
    ("snowflake", 'SELECT CURRENT_SESSION()'),
    ("snowflake", 'SELECT CURRENT_STATEMENT()'),
    ("snowflake", 'SELECT CURRENT_VERSION()'),
    ("snowflake", 'SELECT CURRENT_TRANSACTION()'),
    ("snowflake", 'SELECT CURRENT_WAREHOUSE()'),
    ("snowflake", 'SELECT CURRENT_ORGANIZATION_USER()'),
    ("snowflake", 'SELECT CURRENT_REGION()'),
    ("snowflake", 'SELECT CURRENT_ROLE()'),
    ("snowflake", 'SELECT CURRENT_ROLE_TYPE()'),
    ("snowflake", 'SELECT DAY(CURRENT_TIMESTAMP())'),
    ("snowflake", 'SELECT MONTH(CURRENT_TIMESTAMP())'),
    ("snowflake", 'SELECT QUARTER(CURRENT_TIMESTAMP())'),
    ("snowflake", 'SELECT WEEK(CURRENT_TIMESTAMP())'),
    ("snowflake", 'SELECT YEAR(CURRENT_TIMESTAMP())'),
    ("snowflake", 'SELECT SUM(amount) FROM mytable GROUP BY ALL'),
    ("snowflake", 'SELECT STDDEV(x)'),
    ("snowflake", 'SELECT STDDEV(x) OVER (PARTITION BY 1)'),
    ("snowflake", 'SELECT STDDEV_POP(x)'),
    ("snowflake", 'SELECT STDDEV_POP(x) OVER (PARTITION BY 1)'),
    ("snowflake", 'SELECT KURTOSIS(x)'),
    ("snowflake", 'SELECT KURTOSIS(x) OVER (PARTITION BY 1)'),
    ("snowflake", 'SELECT HLL(*)'),
    ("snowflake", 'SELECT HLL(a)'),
    ("snowflake", 'SELECT HLL(DISTINCT t.a)'),
    ("snowflake", 'SELECT HLL(a, b, c)'),
    ("snowflake", 'SELECT HLL(DISTINCT a, b, c)'),
    ("snowflake", 'a$b'),
    ("snowflake", 'SELECT CURRENT_ORGANIZATION_NAME()'),
    ("snowflake", 'SELECT OBJECT_AGG(key, value) FROM tbl'),
    ("snowflake", '1 /* /* */'),
    ("snowflake", "SELECT PARSE_IP('192.168.1.1', 'INET')"),
    ("snowflake", "SELECT PARSE_IP('192.168.1.1', 'INET', 0)"),
    ("snowflake", 'SELECT a, exclude, b FROM xxx'),
    ("snowflake", 'SELECT BOOLXOR_AGG(col) FROM tbl'),
    ("snowflake", "SELECT * FROM table AT (TIMESTAMP => '2024-07-24') UNPIVOT(a FOR b IN (c)) AS pivot_table"),
    ("snowflake", 'MERGE INTO my_db AS ids USING (SELECT new_id FROM my_model WHERE NOT col IS NULL) AS new_ids ON ids.type = new_ids.type AND ids.source = new_ids.source WHEN NOT MATCHED THEN INSERT VALUES (new_ids.new_id)'),
    ("snowflake", 'WITH t (SELECT 1 AS c) SELECT c FROM t'),
    ("snowflake", 'TO_GEOGRAPHY(x, y)'),
    ("snowflake", 'TO_GEOMETRY(x, y)'),
    ("snowflake", 'SELECT * FROM s WHERE c NOT IN (1, 2, 3)'),
    ("snowflake", 'SELECT state, city, SUM(retail_price * quantity) AS gross_revenue FROM sales GROUP BY ALL'),
    ("snowflake", 'SELECT CEIL(3.14)'),
    ("snowflake", 'SELECT CEIL(3.14, 1)'),
    ("snowflake", 'CAST(x AS CHAR VARYING)'),
    ("snowflake", 'CAST(x AS CHARACTER VARYING)'),
    ("snowflake", 'SELECT * FROM xxx, yyy, zzz'),
    ("snowflake", 'SELECT * FROM t1 AS t1 CROSS JOIN t2 AS t2 LEFT JOIN t3 AS t3 ON t1.a = t3.i'),
    ("snowflake", 'ARRAY_CONSTRUCT_COMPACT(1, null, 2)'),
    ("snowflake", 'ARRAY_COMPACT(arr)'),
    ("snowflake", 'SELECT TIMESTAMP_FROM_PARTS(2013, 4, 5, 12, 00, 00)'),
    ("snowflake", 'SELECT TIMESTAMP_LTZ_FROM_PARTS(2023, 6, 15, 14, 30, 45)'),
    ("snowflake", "SELECT TIMESTAMP_TZ_FROM_PARTS(2023, 6, 15, 14, 30, 45, 0, 'America/Los_Angeles')"),
    ("snowflake", "SELECT To_BOOLEAN('T')"),
    ("snowflake", 'SELECT NTH_VALUE(a, 2) FROM t'),
    ("snowflake", 'SELECT NTH_VALUE(is_deleted, 2) OVER (PARTITION BY id ROWS BETWEEN 1 PRECEDING AND 1 FOLLOWING) AS nth_is_deleted FROM my_table'),
    ("snowflake", 'SELECT LEAD(is_deleted, 2, -10) RESPECT NULLS OVER (PARTITION BY id) AS nth_is_deleted FROM my_table'),
    ("snowflake", 'SELECT LEAD(is_deleted, 2) OVER (PARTITION BY id) AS nth_is_deleted FROM my_table'),
    ("snowflake", 'SELECT BOOLXOR_AGG(c1) FROM test'),
    ("snowflake", 'POWER(x, 2)'),
    ("snowflake", 'SELECT * FROM a INTERSECT ALL SELECT * FROM b'),
    ("snowflake", 'SELECT * FROM a EXCEPT ALL SELECT * FROM b'),
    ("snowflake", 'SELECT ARRAY_UNION_AGG(a)'),
    ("snowflake", "trim(date_column, 'UTC')"),
    ("snowflake", 'trim(date_column)'),
    ("snowflake", 'MINHASH(100, col1)'),
    ("snowflake", 'MINHASH(100, col1, col2)'),
    ("snowflake", 'MINHASH(4, col1)'),
    ("snowflake", 'MINHASH_COMBINE(sig_col)'),
    ("snowflake", 'APPROXIMATE_SIMILARITY(sig_col)'),
    ("snowflake", 'SELECT CAST(1 AS DOUBLE), CAST(1 AS DOUBLE)'),
    ("snowflake", "LAST_DAY(CAST('2023-04-15' AS DATE))"),
    ("snowflake", 'SELECT ST_DISTANCE(a, b)'),
    ("snowflake", "SELECT HOUR(CAST('08:50:57' AS TIME))"),
    ("snowflake", "SELECT MINUTE(CAST('08:50:57' AS TIME))"),
    ("snowflake", "SELECT SECOND(CAST('08:50:57' AS TIME))"),
    ("snowflake", "SELECT HOUR(CAST('2024-05-09 08:50:57' AS TIMESTAMP))"),
    ("snowflake", "SELECT PREVIOUS_DAY(CAST('2024-05-09' AS DATE), 'MONDAY')"),
    ("snowflake", "SELECT MONTHS_BETWEEN(CAST('2019-03-15' AS DATE), CAST('2019-02-15' AS DATE))"),
    ("snowflake", "SELECT MONTHS_BETWEEN(CAST('2019-03-01 02:00:00' AS TIMESTAMP), CAST('2019-02-15 01:00:00' AS TIMESTAMP))"),
    ("snowflake", "SELECT BASE64_ENCODE('Hello World')"),
    ("snowflake", 'SELECT BASE64_ENCODE(x)'),
    ("snowflake", 'SELECT BASE64_ENCODE(x, 76)'),
    ("snowflake", "SELECT BASE64_ENCODE(x, 76, '+/=')"),
    ("snowflake", "SELECT BASE64_DECODE_STRING('U25vd2ZsYWtl')"),
    ("snowflake", "SELECT BASE64_DECODE_STRING('U25vd2ZsYWtl', '-_+')"),
    ("snowflake", 'SELECT BASE64_DECODE_BINARY(x)'),
    ("snowflake", "SELECT BASE64_DECODE_BINARY(x, '-_+')"),
    ("snowflake", "SELECT TRY_HEX_DECODE_BINARY('48656C6C6F')"),
    ("snowflake", "SELECT TRY_HEX_DECODE_STRING('48656C6C6F')"),
    ("snowflake", 'UNIFORM(1, 10, 5)'),
    ("snowflake", 'NORMAL(0, 1, 42)'),
    ("snowflake", 'EQUAL_NULL(a, b)'),
    ("snowflake", 'SELECT CURRENT_SCHEMA()'),
    ("snowflake", "SELECT 1 WHERE 'abc' ILIKE ANY('%a%')"),
    ("snowflake", "SELECT 1 WHERE 'abc' LIKE ALL ('%a%')"),
    ("snowflake", "SELECT 'he%lo' LIKE ANY ('he#%lo', 'hello') ESCAPE '#'"),
    ("snowflake", "SELECT 'he%lo' LIKE ALL ('he#%lo', 'he#%lo2') ESCAPE '#'"),
    ("snowflake", "SELECT 'he%lo' ILIKE ANY ('he#%lo', 'hello') ESCAPE '#'"),
    ("snowflake", "SELECT 1 WHERE 'he%lo' LIKE ANY ('he#%lo', 'hello') ESCAPE '#' AND x = 1"),
    ("snowflake", "SELECT 1 WHERE 'he%lo' LIKE ALL ('he#%lo', 'he#%lo2') ESCAPE '#' OR x = 1"),
    ("snowflake", 'SELECT * FROM t UNPIVOT(a FOR b IN (c, d)) UNPIVOT(e FOR f IN (g, h))'),
    ("snowflake", "SELECT CAST('12:00:00' AS TIME)"),
    ("snowflake", 'CAST(a AS TIMESTAMP_NTZ)'),
    ("snowflake", 'CAST(a AS TIMESTAMP_LTZ)'),
    ("snowflake", 'SELECT a::TIMESTAMP_LTZ(9)'),
    ("snowflake", 'SELECT a::TIMESTAMPLTZ'),
    ("snowflake", 'SELECT a::TIMESTAMP WITH LOCAL TIME ZONE'),
    ("snowflake", "DATE_TRUNC('YEAR', CAST('2024-06-15' AS DATE))"),
    ("snowflake", 'SELECT CAST(a AS VARIANT)'),
    ("snowflake", 'SELECT CAST(a AS ARRAY)'),
    ("snowflake", 'SELECT a::VARIANT'),
    ("snowflake", 'SELECT a::OBJECT'),
    ("snowflake", "SELECT NEXT_DAY(CAST('2024-01-01' AS DATE), 'Monday')"),
    ("snowflake", "SELECT NEXT_DAY(CAST('2024-01-05' AS DATE), 'Friday')"),
    ("snowflake", "SELECT NEXT_DAY(CAST('2024-01-05' AS DATE), 'WE')"),
    ("snowflake", "SELECT NEXT_DAY(CAST('2024-01-01 10:30:45' AS TIMESTAMP), 'Friday')"),
    ("snowflake", "SELECT NEXT_DAY(CAST('2024-01-01' AS DATE), day_column)"),
    ("snowflake", "SELECT PREVIOUS_DAY(DATE '2024-01-15', 'Monday')"),
    ("snowflake", "SELECT PREVIOUS_DAY(DATE '2024-01-15', 'Fr')"),
    ("snowflake", "SELECT PREVIOUS_DAY(TIMESTAMP '2024-01-15 10:30:45', 'Monday')"),
    ("snowflake", "SELECT PREVIOUS_DAY(DATE '2024-01-15', day_column)"),
    ("snowflake", 'SELECT * FROM my_table AT (OFFSET => -60 * 5)'),
    ("snowflake", "SELECT * FROM my_table AT (STATEMENT => '8e5d0ca9-005e-44e6-b858-a8f5b37c5726')"),
    ("snowflake", "SELECT * FROM my_table AT (TIMESTAMP => 'Fri, 01 May 2015 16:20:00 -0700'::timestamp)"),
    ("snowflake", 'CREATE SECURE VIEW table1 AS (SELECT a FROM table2)'),
    ("snowflake", 'CREATE TABLE geospatial_table (id INT, g GEOGRAPHY)'),
    ("snowflake", 'CREATE SCHEMA mytestschema_clone CLONE testschema'),
    ("snowflake", 'CREATE OR REPLACE TABLE EXAMPLE_DB.DEMO.USERS (ID DECIMAL(38, 0) NOT NULL, PRIMARY KEY (ID), FOREIGN KEY (CITY_CODE) REFERENCES EXAMPLE_DB.DEMO.CITIES (CITY_CODE))'),
    ("snowflake", 'CREATE OR REPLACE FUNCTION ibis_udfs.public.object_values("obj" OBJECT) RETURNS ARRAY LANGUAGE JAVASCRIPT STRICT AS \' return Object.values(obj) \''),
    ("snowflake", 'CREATE TABLE orders_clone CLONE orders'),
    ("snowflake", 'CREATE OR REPLACE TRANSIENT TABLE a (id INT)'),
    ("snowflake", 'CREATE TABLE a (b INT)'),
    ("snowflake", "CREATE FUNCTION a() RETURNS TABLE (b INT) AS 'SELECT 1'"),
    ("snowflake", "CREATE FUNCTION a() RETURNS INT IMMUTABLE AS 'SELECT 1'"),
    ("snowflake", 'CALL a.b.c(x, y)'),
    ("snowflake", 'SELECT 1 EXCEPT SELECT 1'),
    ("snowflake", 'SELECT "c0", "c1" FROM (VALUES (1, 2), (3, 4)) AS "t0"("c0", "c1")'),
    ("snowflake", "SELECT SEARCH((play, line), 'dream')"),
    ("snowflake", "SELECT SEARCH(line, 'king')"),
    ("snowflake", "SELECT REGEXP_COUNT('hello world', 'l ')"),
    ("snowflake", "SELECT REGEXP_COUNT('hello world', 'l', 1)"),
    ("snowflake", "SELECT REGEXP_COUNT('hello world', 'l', 1, 'i')"),
    ("snowflake", "SELECT REGEXP_COUNT('hello', 'l')"),
    ("snowflake", "SELECT REGEXP_COUNT('hello world', 'l', 7)"),
    ("snowflake", "SELECT REGEXP_COUNT('Hello World', 'L', 1, 'im')"),
    ("snowflake", 'SELECT REGEXP_COUNT(subject, pattern)'),
    ("snowflake", "SELECT REGEXP_INSTR('abc', 'a')"),
    ("snowflake", "SELECT REGEXP_INSTR('abc', 'a', 1, 1, 0, 'i')"),
    ("snowflake", 'SELECT REGEXP_INSTR(subject, pattern)'),
    ("snowflake", 'SELECT REGEXP_INSTR(subject, pattern, 5)'),
    ("snowflake", 'SELECT REGEXP_INSTR(subject, pattern, 1, 2)'),
    ("snowflake", "SELECT REGEXP_INSTR(subject, pattern, 1, 1, 0, 'im')"),
    ("snowflake", 'REGEXP_REPLACE(subject, pattern, replacement)'),
    ("snowflake", 'REGEXP_REPLACE(subject, pattern, replacement, position)'),
    ("snowflake", "REGEXP_REPLACE(subject, pattern, replacement, position, occurrence, 'c')"),
    ("snowflake", "REGEXP_REPLACE(subject, pattern, replacement, 1, 0, 'c')"),
    ("snowflake", 'REGEXP_REPLACE(subject, pattern, replacement, 1, 1)'),
    ("snowflake", 'REGEXP_REPLACE(subject, pattern, replacement, 3, 0)'),
    ("snowflake", 'REGEXP_REPLACE(subject, pattern, replacement, 3, 1)'),
    ("snowflake", "REGEXP_REPLACE(subject, pattern, replacement, 1, 0, 'i')"),
    ("snowflake", 'REPLACE(subject, pattern, replacement)'),
    ("snowflake", 'CAST(5 + 5 AS VARCHAR)'),
    ("snowflake", 'SELECT CAST(1.5 AS DECFLOAT)'),
    ("snowflake", 'CREATE TABLE t (x DECFLOAT)'),
    ("snowflake", 'CREATE OR REPLACE VIEW FOO (A, B) AS SELECT A, B FROM TBL'),
    ("snowflake", 'CREATE OR REPLACE MATERIALIZED VIEW FOO (A, B) AS SELECT A, B FROM TBL'),
    ("snowflake", 'BITMAP_OR_AGG(x)'),
    ("snowflake", 'MD5(col)'),
    ("snowflake", 'SELECT GETBIT(11, 1)'),
    ("snowflake", 'GETBIT(11, 1)'),
    ("snowflake", "TO_BINARY('48454C50', 'HEX')"),
    ("snowflake", "TO_BINARY('48454C50')"),
    ("snowflake", "TO_BINARY('TEST', 'UTF-8')"),
    ("snowflake", "TO_BINARY('SEVMUA==', 'BASE64')"),
    ("snowflake", "REVERSE(TO_BINARY('ABC', 'UTF-8'))"),
    ("snowflake", "REVERSE(TO_BINARY('414243', 'HEX'))"),
    ("snowflake", "REVERSE('ABC')"),
    ("snowflake", 'SELECT FLOOR(1.753, 2)'),
    ("snowflake", 'SELECT FLOOR(123.45, -1)'),
    ("snowflake", 'SELECT FLOOR(a + b, 2)'),
    ("snowflake", 'SELECT FLOOR(1.234, 1.5)'),
    ("snowflake", 'SELECT SEQ1() FROM test'),
    ("snowflake", 'SELECT SEQ1(0) FROM test'),
    ("snowflake", 'SELECT SEQ1(1) FROM test'),
    ("snowflake", 'SELECT SEQ2() FROM test'),
    ("snowflake", 'SELECT SEQ2(0) FROM test'),
    ("snowflake", 'SELECT SEQ2(1) FROM test'),
    ("snowflake", 'SELECT SEQ4() FROM test'),
    ("snowflake", 'SELECT SEQ4(0) FROM test'),
    ("snowflake", 'SELECT SEQ4(1) FROM test'),
    ("snowflake", 'SELECT SEQ8() FROM test'),
    ("snowflake", 'SELECT SEQ8(0) FROM test'),
    ("snowflake", 'SELECT SEQ8(1) FROM test'),
    ("snowflake", 'SELECT CEIL(1.753, 2)'),
    ("snowflake", 'SELECT CEIL(123.45, -1)'),
    ("snowflake", 'SELECT CEIL(a + b, 2)'),
    ("snowflake", 'SELECT CEIL(1.234, 1.5)'),
    ("snowflake", "ENCRYPT(value, 'passphrase')"),
    ("snowflake", "ENCRYPT(value, 'passphrase', 'aad')"),
    ("snowflake", "ENCRYPT(value, 'passphrase', 'aad', 'AES-GCM')"),
    ("snowflake", 'ENCRYPT_RAW(value, key, iv)'),
    ("snowflake", 'ENCRYPT_RAW(value, key, iv, aad)'),
    ("snowflake", "ENCRYPT_RAW(value, key, iv, aad, 'AES-GCM')"),
    ("snowflake", "DECRYPT(encrypted, 'passphrase')"),
    ("snowflake", "DECRYPT(encrypted, 'passphrase', 'aad')"),
    ("snowflake", "DECRYPT(encrypted, 'passphrase', 'aad', 'AES-GCM')"),
    ("snowflake", 'DECRYPT_RAW(encrypted, key, iv)'),
    ("snowflake", 'DECRYPT_RAW(encrypted, key, iv, aad)'),
    ("snowflake", "DECRYPT_RAW(encrypted, key, iv, aad, 'AES-GCM')"),
    ("snowflake", "DECRYPT_RAW(encrypted, key, iv, aad, 'AES-GCM', aead)"),
    ("snowflake", 'UPDATE test SET t = 1 FROM t1'),
    ("snowflake", 'APPROX_TOP_K(C4, 3, 5)'),
    ("snowflake", 'TRY_TO_TIMESTAMP(foo)'),
    ("snowflake", "TRY_TO_TIMESTAMP('12345')"),
    ("snowflake", "TO_DATE('12345')"),
    ("snowflake", 'CREATE OR REPLACE FUNCTION ibis_udfs.public.object_values("obj" OBJECT) RETURNS ARRAY LANGUAGE JAVASCRIPT RETURNS NULL ON NULL INPUT AS \' return Object.values(obj) \''),
    ("snowflake", "CREATE FUNCTION a(x DOUBLE) RETURNS DOUBLE LANGUAGE SQL CALLED ON NULL INPUT AS ' x * 2 '"),
    ("snowflake", 'SELECT number'),
    ("snowflake", 'SELECT TO_TIMESTAMP(123.4)'),
    ("snowflake", "SELECT SEARCH_IP(col, '192.168.0.0')"),
    ("snowflake", 'WITH t AS (SELECT PARSE_JSON(\'{"level1": {"level2": {"level3": "value"}}}\') AS data) SELECT data:     level1  : level2 : level3::VARIANT FROM t'),
    ("snowflake", "SELECT p FROM t WHERE p:val NOT IN ('2')"),
    ("snowflake", 'SELECT PARSE_JSON(\'{"x": "hello"}\'):x LIKE \'hello\''),
    ("snowflake", "SELECT data:x LIKE 'hello' FROM some_table"),
    ("snowflake", 'SELECT v:attr[0].name FROM vartab'),
    ("snowflake", 'v:attr[0]:name'),
    ("snowflake", 'SELECT PARSE_JSON(\'{"food":{"fruit":"banana"}}\'):food.fruit::VARCHAR'),
    ("snowflake", 'SELECT PARSE_JSON(\'{"fruit":"banana"}\'):fruit'),
    ("snowflake", 'SELECT PARSE_JSON(\'{"a": {"b c": "foo"}}\'):a:"b c"'),
    ("snowflake", 'SELECT v:"fruit" FROM vartab'),
    ("snowflake", "SHOW USERS"),
    ("snowflake", "SHOW TERSE USERS"),
    ("snowflake", "SHOW USERS LIKE '_foo%' STARTS WITH 'bar' LIMIT 5 FROM 'baz'"),
    ("snowflake", "SHOW TERSE DATABASES"),
    ("snowflake", "SHOW TERSE DATABASES HISTORY LIKE 'foo' STARTS WITH 'bla' LIMIT 5 FROM 'bob' WITH PRIVILEGES USAGE, MODIFY"),
    ("snowflake", "SHOW FILE FORMATS"),
    ("snowflake", "SHOW FILE FORMATS LIKE 'foo' IN DATABASE db1"),
    ("snowflake", "SHOW FILE FORMATS LIKE 'foo' IN SCHEMA db1.schema1"),
    ("snowflake", "SHOW FUNCTIONS"),
    ("snowflake", "SHOW FUNCTIONS LIKE 'foo' IN CLASS bla"),
    ("snowflake", "SHOW PROCEDURES"),
    ("snowflake", "SHOW PROCEDURES LIKE 'foo' IN APPLICATION app"),
    ("snowflake", "SHOW PROCEDURES LIKE 'foo' IN APPLICATION PACKAGE pkg"),
    ("snowflake", "SHOW STAGES"),
    ("snowflake", "SHOW STAGES LIKE 'foo' IN DATABASE db1"),
    ("snowflake", "SHOW STAGES LIKE 'foo' IN SCHEMA db1.schema1"),
    ("snowflake", "SHOW WAREHOUSES"),
    ("snowflake", "SHOW WAREHOUSES LIKE 'foo' WITH PRIVILEGES USAGE, MODIFY"),
    ("snowflake", "show terse schemas in database db1 starts with 'a' limit 10 from 'b'"),
    ("snowflake", "show terse objects in schema db1.schema1 starts with 'a' limit 10 from 'b'"),
    ("snowflake", "show terse objects in db1.schema1 starts with 'a' limit 10 from 'b'"),
    ("snowflake", "SHOW COLUMNS"),
    ("snowflake", "SHOW COLUMNS IN TABLE dt_test"),
    ("snowflake", "SHOW COLUMNS LIKE '_foo%' IN TABLE dt_test"),
    ("snowflake", "SHOW COLUMNS IN VIEW"),
    ("snowflake", "SHOW COLUMNS LIKE '_foo%' IN VIEW dt_test"),
    ("snowflake", "SHOW TABLES LIKE 'line%' IN tpch.public"),
    ("snowflake", "SHOW TABLES HISTORY IN tpch.public"),
    ("snowflake", "show terse tables in schema db1.schema1 starts with 'a' limit 10 from 'b'"),
    ("snowflake", "show terse tables in db1.schema1 starts with 'a' limit 10 from 'b'"),
    ("snowflake", "SHOW ICEBERG TABLES IN db1.schema1"),
    ("snowflake", "SHOW TERSE ICEBERG TABLES IN db1.schema1"),
    ("snowflake", "SHOW PRIMARY KEYS"),
    ("snowflake", "SHOW PRIMARY KEYS IN ACCOUNT"),
    ("snowflake", "SHOW PRIMARY KEYS IN DATABASE"),
    ("snowflake", "SHOW PRIMARY KEYS IN DATABASE foo"),
    ("snowflake", "SHOW PRIMARY KEYS IN TABLE"),
    ("snowflake", "SHOW PRIMARY KEYS IN TABLE foo"),
    ("snowflake", "SHOW PRIMARY KEYS IN \"TEST\".\"PUBLIC\".\"foo\""),
    ("snowflake", "SHOW TERSE PRIMARY KEYS IN \"TEST\".\"PUBLIC\".\"foo\""),
    ("snowflake", "SHOW TERSE VIEWS"),
    ("snowflake", "SHOW VIEWS"),
    ("snowflake", "SHOW VIEWS LIKE 'foo%'"),
    ("snowflake", "SHOW VIEWS IN ACCOUNT"),
    ("snowflake", "SHOW VIEWS IN DATABASE"),
    ("snowflake", "SHOW VIEWS IN DATABASE foo"),
    ("snowflake", "SHOW VIEWS IN SCHEMA foo"),
    ("snowflake", "SHOW VIEWS IN foo"),
    ("snowflake", "SHOW UNIQUE KEYS"),
    ("snowflake", "SHOW UNIQUE KEYS IN ACCOUNT"),
    ("snowflake", "SHOW UNIQUE KEYS IN DATABASE"),
    ("snowflake", "SHOW UNIQUE KEYS IN DATABASE foo"),
    ("snowflake", "SHOW UNIQUE KEYS IN TABLE"),
    ("snowflake", "SHOW UNIQUE KEYS IN TABLE foo"),
    ("snowflake", "SHOW UNIQUE KEYS IN \"TEST\".\"PUBLIC\".\"foo\""),
    ("snowflake", "SHOW TERSE UNIQUE KEYS IN \"TEST\".\"PUBLIC\".\"foo\""),
    ("snowflake", "SHOW IMPORTED KEYS"),
    ("snowflake", "SHOW IMPORTED KEYS IN ACCOUNT"),
    ("snowflake", "SHOW IMPORTED KEYS IN DATABASE"),
    ("snowflake", "SHOW IMPORTED KEYS IN DATABASE foo"),
    ("snowflake", "SHOW IMPORTED KEYS IN TABLE"),
    ("snowflake", "SHOW IMPORTED KEYS IN TABLE foo"),
    ("snowflake", "SHOW IMPORTED KEYS IN \"TEST\".\"PUBLIC\".\"foo\""),
    ("snowflake", "SHOW TERSE IMPORTED KEYS IN \"TEST\".\"PUBLIC\".\"foo\""),
    ("snowflake", "SHOW TERSE SEQUENCES"),
    ("snowflake", "SHOW SEQUENCES"),
    ("snowflake", "SHOW SEQUENCES LIKE '_foo%' IN ACCOUNT"),
    ("snowflake", "SHOW SEQUENCES LIKE '_foo%' IN DATABASE"),
    ("snowflake", "SHOW SEQUENCES LIKE '_foo%' IN DATABASE foo"),
    ("snowflake", "SHOW SEQUENCES LIKE '_foo%' IN SCHEMA"),
    ("snowflake", "SHOW SEQUENCES LIKE '_foo%' IN SCHEMA foo"),
    ("snowflake", "SHOW SEQUENCES LIKE '_foo%' IN foo"),
    ("snowflake", "SELECT BIT_LENGTH(x'A1B2')"),
    ("snowflake", "INSERT INTO test VALUES (x'48FAF43B0AFCEF9B63EE3A93EE2AC2')"),
    ("snowflake", "SELECT x'ABCD'"),
    ("snowflake", "CREATE TABLE foo (bar DOUBLE AUTOINCREMENT START 0 INCREMENT 1)"),
    ("snowflake", "CREATE OR REPLACE TABLE x (y NUMBER(38, 0) NOT NULL AUTOINCREMENT START 1 INCREMENT 1 ORDER)"),
    ("snowflake", "CREATE OR REPLACE TABLE x (y NUMBER(38, 0) NOT NULL AUTOINCREMENT START 1 INCREMENT 1 NOORDER)"),
    ("snowflake", "CREATE TABLE x (y INT AUTOINCREMENT START 10)"),
    ("snowflake", "CREATE TABLE x (y INT AUTOINCREMENT INCREMENT 2)"),
    ("snowflake", "CREATE TABLE x (y INT AUTOINCREMENT ORDER)"),
    ("snowflake", "CREATE TABLE x (y INT AUTOINCREMENT NOORDER)"),
    ("snowflake", "CREATE TABLE x (y INT AUTOINCREMENT START 10 NOORDER)"),
    ("snowflake", "CREATE TABLE x (y INT AUTOINCREMENT INCREMENT 2 ORDER)"),
    ("snowflake", "CREATE TABLE x (y INT AUTOINCREMENT INCREMENT 2 START 10)"),
    ("snowflake", "CREATE TABLE x (y INT AUTOINCREMENT(0, 1) ORDER)"),
    ("snowflake", "CREATE TABLE c (pk BIGINT AUTOINCREMENT START 10)"),
    ("snowflake", "CREATE TABLE c (pk BIGINT AUTOINCREMENT INCREMENT -1)"),
    ("snowflake", "CREATE TABLE t (id INT PRIMARY KEY AUTOINCREMENT)"),
    ("snowflake", "SELECT * FROM foo WHERE 'str' IN (SELECT value FROM TABLE(FLATTEN(INPUT => vals)) AS _u(seq, key, path, index, value, this))"),
    ("snowflake", "SELECT * FROM TABLE(FLATTEN(input => parse_json('[1, ,77]'))) f"),
    ("snowflake", "SELECT * FROM TABLE(FLATTEN(input => parse_json('{\"a\":1, \"b\":[77,88]}'), path => 'b')) f"),
    ("snowflake", "SELECT * FROM TABLE(FLATTEN(input => parse_json('[]'))) f"),
    ("snowflake", "SELECT * FROM TABLE(FLATTEN(input => parse_json('{\"a\":1, \"b\":[77,88], \"c\": {\"d\":\"X\"}}'))) f"),
    ("snowflake", "SELECT * FROM TABLE(FLATTEN(input => parse_json('{\"a\":1, \"b\":[77,88], \"c\": {\"d\":\"X\"}}'), recursive => true)) f"),
    ("snowflake", "SELECT * FROM TABLE(FLATTEN(input => parse_json('{\"a\":1, \"b\":[77,88], \"c\": {\"d\":\"X\"}}'), recursive => true, mode => 'object')) f"),
    ("snowflake", "SELECT TIME_SLICE(CAST('2024-05-09 08:50:57.891' AS TIMESTAMP), 15, 'MINUTE')"),
    ("snowflake", "SELECT TIME_SLICE(CAST('2024-05-09' AS DATE), 1, 'DAY')"),
    ("snowflake", "SELECT TIME_SLICE(CAST('2024-05-09 08:50:57.891' AS TIMESTAMP), 1, 'HOUR', 'start')"),
    ("snowflake", "SELECT TIME_SLICE(TIMESTAMP '2024-03-15 14:37:42', 1, 'HOUR')"),
    ("snowflake", "SELECT TIME_SLICE(TIMESTAMP '2024-03-15 14:37:42', 1, 'HOUR', 'END')"),
    ("snowflake", "SELECT TIME_SLICE(DATE '2024-03-15', 1, 'DAY')"),
    ("snowflake", "SELECT TIME_SLICE(DATE '2024-03-15', 1, 'DAY', 'END')"),
    ("snowflake", "SELECT TIME_SLICE(TIMESTAMP '2024-03-15 14:37:42', 15, 'MINUTE')"),
    ("snowflake", "SELECT TIME_SLICE(TIMESTAMP '2024-03-15 14:37:42', 1, 'QUARTER')"),
    ("snowflake", "SELECT TIME_SLICE(DATE '2024-03-15', 1, 'WEEK', 'END')"),
    ("snowflake", "DATEDIFF(DAY, CAST('2007-12-25' AS DATE), CAST('2008-12-25' AS DATE))"),
    ("snowflake", "DATEDIFF(WEEK, '2024-12-13', '2024-12-17')"),
    ("snowflake", "DATEDIFF(WEEK, '2024-12-15', '2024-12-16')"),
    ("snowflake", "DATEDIFF(YEAR, '2020-01-15', '2023-06-20')"),
    ("snowflake", "DATEDIFF(MONTH, '2020-01-15', '2023-06-20')"),
    ("snowflake", "DATEDIFF(QUARTER, '2020-01-15', '2023-06-20')"),
    ("snowflake", "DATEDIFF(NANOSECOND, '2023-01-01 10:00:00.000000000', '2023-01-01 10:00:00.123456789')"),
    ("snowflake", "DATEDIFF(NANOSECOND, start_time, end_time)"),
    ("snowflake", "CREATE OR REPLACE TEMPORARY TABLE x (y NUMBER IDENTITY(0, 1))"),
    ("snowflake", "CREATE TEMPORARY TABLE x (y NUMBER AUTOINCREMENT(0, 1))"),
    ("snowflake", "CREATE TABLE x (y NUMBER IDENTITY START 0 INCREMENT 1)"),
    ("snowflake", "CREATE TABLE test_table (id NUMERIC NOT NULL AUTOINCREMENT)"),
    ("snowflake", "SHA1(x)"),
    ("snowflake", "SHA1('text')"),
    ("snowflake", "SHA1(X'002A'::BINARY)"),
    ("snowflake", "SHA1(123)"),
    ("snowflake", "SHA1(DATE '2024-01-15')"),
    ("snowflake", "SELECT RANDOM()"),
    ("snowflake", "SELECT RANDOM(123)"),
    ("snowflake", "SELECT RANDSTR(123, RANDOM())"),
    ("snowflake", "SELECT NORMAL(0, 1, RANDOM())"),
    ("snowflake", "SELECT RANDSTR(10, RANDOM(123))"),
    ("snowflake", "SELECT RANDSTR(10, RANDOM())"),
    ("snowflake", "SELECT ZIPF(2, 100, RANDOM())"),
    ("snowflake", "UNIFORM(1, 10, RANDOM(5))"),
    ("snowflake", "UNIFORM(1, 10, RANDOM())"),
    ("snowflake", "NORMAL(10.5, 2.5, RANDOM())"),
    ("snowflake", "NORMAL(10.5, 2.5, RANDOM(5))"),
    ("snowflake", "SELECT DAYOFYEAR(CURRENT_TIMESTAMP())"),
    ("snowflake", "SELECT YEAROFWEEK(CURRENT_TIMESTAMP())"),
    ("snowflake", "SELECT YEAROFWEEKISO(CURRENT_TIMESTAMP())"),
    ("snowflake", "SELECT YEAROFWEEK('2024-12-31'::DATE)"),
    ("snowflake", "SELECT YEAROFWEEKISO('2024-12-31'::DATE)"),
    ("snowflake", "DAYOFYEAR(foo)"),
)

def reference_commit(sqlglot_dir: pathlib.Path) -> str:
    out = subprocess.run(
        ["git", "-C", str(sqlglot_dir), "rev-parse", "HEAD"],
        capture_output=True, text=True, check=True,
    )
    return out.stdout.strip()


def pinned_commit(repo: pathlib.Path) -> str:
    """The commit NOTICE names. The oracle refuses to run against another."""
    for line in (repo / "NOTICE").read_text().splitlines():
        if "commit " in line:
            return line.split("commit ")[1].split()[0].rstrip("(")
    raise SystemExit("NOTICE does not name a reference commit")


def corpus_identity(sqlglot_dir: pathlib.Path) -> list[tuple[str, str]]:
    """(dialect, sql) for identity.sql -- dialect-neutral, parsed as each."""
    out = []
    for raw in (sqlglot_dir / "tests/fixtures/identity.sql").read_text().splitlines():
        line = raw.strip()
        if not line or line.startswith(("#", "--")):
            continue
        out.append(("", line))
    return out


def corpus_dialect(sqlglot_dir: pathlib.Path, dialects: tuple[str, ...]) -> list[tuple[str, str]]:
    """Every statement sqlglot pins for the dialects the executor configures.

    Read from the test sources rather than executed: the point is the strings,
    and importing the test modules would pull in unittest scaffolding for
    nothing.

    Two shapes are harvested, and the second is most of the corpus:

    * `validate_identity("...")` -- a statement the dialect round-trips. Only
      taken from that dialect's OWN file, since the call is implicitly about
      `self.dialect`.
    * `validate_all(..., read={"duckdb": "..."}, write={"tsql": "..."})` --
      how one concept is spelled in each dialect, keyed BY dialect and so
      readable from any file. Most DuckDB statements live in
      tests/dialects/test_snowflake.py, not test_duckdb.py, and the largest
      single source is tests/dialects/test_dialect.py -- 5,448 lines organised
      by concept (`test_cast`, `test_typeddiv`, `test_nullsafe_eq`) rather
      than by dialect. Reading only test_<dialect>.py missed all of it.

    A key like "duckdb, version=1.2" is SKIPPED: it pins behaviour that
    differs between engine versions, and the port has no version concept, so
    mapping it onto plain "duckdb" would ask the oracle a different question
    than the test does. Skipped statements are counted, not hidden, so the
    coverage number stays honest about what was sampled.

    These strings only decide WHICH statements get asked about. The
    expectation is always whatever the reference answers.
    """
    import ast

    wanted = set(dialects)
    out: list[tuple[str, str]] = []
    seen: set[tuple[str, str]] = set()
    skipped_versioned = 0

    def take(dialect: str, sql: str) -> None:
        if (dialect, sql) not in seen:
            seen.add((dialect, sql))
            out.append((dialect, sql))

    for path in sorted((sqlglot_dir / "tests/dialects").glob("test_*.py")):
        own = path.stem.removeprefix("test_")
        tree = ast.parse(path.read_text())
        for node in ast.walk(tree):
            if not isinstance(node, ast.Call):
                continue
            fn = node.func
            name = fn.attr if isinstance(fn, ast.Attribute) else getattr(fn, "id", "")
            if name not in ("validate_identity", "validate_all"):
                continue

            # The positional argument is written in the file's own dialect,
            # so it is only ours when the file is one of ours.
            if own in wanted and node.args:
                first = node.args[0]
                if isinstance(first, ast.Constant) and isinstance(first.value, str):
                    take(own, first.value)

            # read=/write= entries name their own dialect and travel.
            for kw in node.keywords:
                if kw.arg not in ("read", "write") or not isinstance(kw.value, ast.Dict):
                    continue
                for k, v in zip(kw.value.keys, kw.value.values):
                    if not isinstance(k, ast.Constant) or not isinstance(v, ast.Constant):
                        continue
                    if not isinstance(k.value, str) or not isinstance(v.value, str):
                        continue
                    if "version=" in k.value:
                        skipped_versioned += 1
                        continue
                    if k.value in wanted:
                        take(k.value, v.value)

    if skipped_versioned:
        print(f"  {skipped_versioned} version-pinned statement(s) skipped (the port has no versions)")
    return out


def dump(sql: str, dialect: str):
    import sqlglot

    tree = sqlglot.parse_one(sql, read=dialect or None)
    return tree.dump()


def render(sql: str, dialect: str) -> str:
    """What the reference emits for this statement, in its own dialect.

    The guard rewrites the tree -- it injects a row ceiling -- and then has to
    hand SQL back to the engine. Which means the port needs a generator, and
    the generator needs the same oracle as everything else: the string the
    reference produces, not merely something that parses.
    """
    import sqlglot

    return sqlglot.parse_one(sql, read=dialect or None).sql(dialect=dialect or None)


def dump_tokens(sql: str, dialect: str):
    """The reference's token stream, field for field.

    Positions are part of the contract, not incidental: the parser reports
    errors by them, and a port that gets the tokens right but the offsets wrong
    would pass a looser check and fail a user. So line, col, start, end and the
    attached comments are all recorded and all compared.
    """
    from sqlglot.dialects.dialect import Dialect

    d = Dialect.get_or_raise(dialect or None)
    tokens = d.tokenizer_class(d).tokenize(sql)
    return [
        {
            "t": tok.token_type.name,
            "x": tok.text,
            "l": tok.line,
            "c": tok.col,
            "s": tok.start,
            "e": tok.end,
            **({"o": list(tok.comments)} if tok.comments else {}),
        }
        for tok in tokens
    ]


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--sqlglot", required=True, type=pathlib.Path)
    ap.add_argument("--out", default="testdata/expected", type=pathlib.Path)
    ap.add_argument(
        "--candidates",
        type=pathlib.Path,
        help="build expectations for these statements (dialect<TAB>sql per line) "
        "instead of the reference corpus, for judging a fuzz session",
    )
    a = ap.parse_args()

    repo = pathlib.Path(__file__).resolve().parent.parent
    sys.path.insert(0, str(a.sqlglot))
    actual, pinned = reference_commit(a.sqlglot), pinned_commit(repo)
    if actual != pinned:
        raise SystemExit(
            f"reference checkout is at {actual[:12]} but NOTICE pins {pinned[:12]}.\n"
            "Either check out the pinned commit, or update NOTICE deliberately and "
            "commit the regenerated expectations with it."
        )

    if a.candidates:
        # A fuzz session's statements rather than the reference corpus. The
        # expectations land in a temp directory and the existing differential
        # is pointed at them; nothing about the committed corpus changes.
        # split("\n") rather than splitlines(): a fuzzer emits \v, \f and
        # \u2028 inside statements, and Python breaks lines on all of them.
        corpus = []
        for line in a.candidates.read_text().split("\n"):
            if not line.strip():
                continue
            dialect, tab, sql = line.partition("\t")
            if not tab:
                raise SystemExit(f"malformed candidate (want dialect<TAB>sql): {line!r}")
            corpus.append((dialect, sql))
    else:
        corpus = corpus_identity(a.sqlglot)
        corpus += corpus_dialect(a.sqlglot, DIALECTS)
        corpus += [(d, sql) for d, sql in EDGE_CORPUS]
        corpus += [(d, sql) for d, sql in DAX_CORPUS]
        corpus += [(d, sql) for d, sql in ORACLE_CORPUS]
        corpus += [(d, sql) for d, sql in SNOWFLAKE_CORPUS]

    a.out.mkdir(parents=True, exist_ok=True)
    for f in a.out.glob("*.json"):
        f.unlink()
    written, failed = 0, []
    index = []
    for dialect, sql in corpus:
        key = hashlib.sha1(f"{dialect}\x00{sql}".encode()).hexdigest()[:16]
        try:
            tree = dump(sql, dialect)
            tokens = dump_tokens(sql, dialect)
            rendered = render(sql, dialect)
        except Exception as e:  # noqa: BLE001 -- the reference's own failures are recorded, not hidden
            failed.append((dialect, sql, f"{type(e).__name__}: {e}"[:120]))
            continue
        (a.out / f"{key}.json").write_text(
            json.dumps(
                {
                    "dialect": dialect,
                    "sql": sql,
                    "rendered": rendered,
                    "tokens": tokens,
                    "tree": tree,
                },
                indent=1,
                sort_keys=True,
            )
        )
        index.append({"key": key, "dialect": dialect, "sql": sql})
        written += 1

    (a.out / "index.json").write_text(
        json.dumps(
            {"reference": actual, "count": written, "statements": index},
            indent=1,
        )
    )
    print(f"reference {actual[:12]}: {written} expectations written to {a.out}")
    by = {}
    for s in index:
        by[s["dialect"] or "neutral"] = by.get(s["dialect"] or "neutral", 0) + 1
    for d, n in sorted(by.items()):
        print(f"  {d:10} {n}")
    if failed:
        print(f"  {len(failed)} statement(s) the REFERENCE could not parse (recorded, not hidden):")
        for d, sql, err in failed[:5]:
            print(f"    [{d or 'neutral'}] {sql[:60]} -> {err}")
    return 0


if __name__ == "__main__":
    sys.exit(main())

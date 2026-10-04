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

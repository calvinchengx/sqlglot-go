-- Fixture tables for the execution oracle (harness/execute.py).
--
-- Most of the corpus is a transpiler's test suite, not a workload: it says
-- `SELECT x FROM t` and never creates `t`. Without a schema those statements
-- cannot run, so the oracle has nothing to compare and they are counted as
-- "the statement as written does not run".
--
-- The names here are not invented. They are the tables and columns the corpus
-- actually references, harvested by parsing every statement with sqlglot and
-- keeping the ones that name exactly one table -- so a column is only ever
-- attributed to a table that unambiguously owns it.
--
-- WHY THERE ARE ROWS, and why they vary
--
-- An EMPTY table would be worse than no table at all. Two different queries
-- over nothing both return nothing, so they would "agree" -- and the oracle
-- would report more coverage while proving less. Every table therefore has
-- several rows, with distinct values, NULLs, duplicates and both orderings of
-- magnitude, so that a rewrite which changes a filter, a join, an ordering or
-- a null-handling rule has something to expose. The harness also refuses to
-- count an agreement where BOTH sides returned no rows; see execute.py.
--
-- Portable across DuckDB and PostgreSQL on purpose: the fixture must not
-- change meaning with the engine. Plain types only -- no arrays, no structs.
-- Names are unquoted so PostgreSQL folds them to lower case and DuckDB
-- matches case-insensitively, which is what the corpus expects.

CREATE TABLE t (id INTEGER, a INTEGER, b INTEGER, c INTEGER, i INTEGER,
                n INTEGER, x INTEGER, y INTEGER, val INTEGER,
                col TEXT, js TEXT, l TEXT);
INSERT INTO t VALUES
  (1,  1,  10,  100, 1, 5,  3,  30, 7,    'alpha', '{"a": 1}', 'p'),
  (2,  2,  20,  200, 2, 5,  1,  10, 7,    'beta',  '{"a": 2}', 'q'),
  (3,  3,  NULL, 300, 3, 6, 2,  20, NULL, 'gamma', NULL,       'r'),
  (4,  NULL, 40, 400, 4, 6, 4, NULL, 9,   NULL,    '{"a": 4}', 's'),
  (5,  5,  50,  500, 5, 7, -1, 50, 9,     'beta',  '{"a": 5}', 't');

CREATE TABLE x (id INTEGER, a INTEGER, b INTEGER, x INTEGER, y INTEGER,
                z INTEGER, pos INTEGER, col INTEGER, col1 INTEGER,
                col2 INTEGER, foo TEXT);
INSERT INTO x VALUES
  (1, 1, 2, 3, 4, 5, 0, 11, 21, 31, 'one'),
  (2, 2, 3, 1, 5, 5, 1, 12, 22, 32, 'two'),
  (3, 3, NULL, 2, NULL, 6, 2, 13, 23, 33, NULL),
  (4, 4, 5, -1, 7, 6, 3, 14, 24, 34, 'two');

CREATE TABLE foo (id INTEGER, a INTEGER, b INTEGER, c INTEGER,
                  col INTEGER, bar INTEGER, bla TEXT);
INSERT INTO foo VALUES
  (1, 1, 10, 100, 5, 50, 'x'),
  (2, 2, 20, NULL, 6, 60, 'y'),
  (3, NULL, 30, 300, 7, NULL, 'y');

CREATE TABLE tbl (id INTEGER, a INTEGER, b INTEGER, x INTEGER, y INTEGER, name TEXT);
INSERT INTO tbl VALUES
  (1, 1, 2, 3, 4, 'ann'),
  (2, 2, 3, 4, NULL, 'bob'),
  (3, NULL, 4, 5, 6, 'ann');

CREATE TABLE test (a INTEGER, b INTEGER, c1 INTEGER, c2 INTEGER,
                   col1 INTEGER, foo TEXT);
INSERT INTO test VALUES
  (1, 10, 1, 2, 100, 'p'),
  (2, 20, 3, NULL, 200, 'q'),
  (3, NULL, 5, 6, 300, 'p');

CREATE TABLE t1 (col INTEGER, foo1 INTEGER, foo2 INTEGER);
INSERT INTO t1 VALUES (1, 10, 100), (2, 20, NULL), (3, NULL, 300);

CREATE TABLE t2 (col INTEGER, a INTEGER, b INTEGER);
INSERT INTO t2 VALUES (1, 1, 2), (2, 3, 4), (9, NULL, 6);

CREATE TABLE a (id INTEGER, b INTEGER);
INSERT INTO a VALUES (1, 10), (2, 20), (3, NULL);

CREATE TABLE b (id INTEGER, a INTEGER);
INSERT INTO b VALUES (1, 100), (2, 200), (4, NULL);

CREATE TABLE my_table (id INTEGER, y INTEGER, col_a INTEGER, col_b INTEGER,
                       is_deleted BOOLEAN, data TEXT);
INSERT INTO my_table VALUES
  (1, 5, 1, 2, FALSE, 'first'),
  (2, 6, 3, 4, TRUE,  'second'),
  (3, NULL, 5, NULL, FALSE, NULL);

CREATE TABLE table1 (col1 INTEGER, col2 INTEGER, score INTEGER,
                     price INTEGER, name TEXT, product TEXT);
INSERT INTO table1 VALUES
  (1, 10, 90, 5, 'ann', 'apple'),
  (2, 20, 75, 8, 'bob', 'pear'),
  (3, NULL, 90, NULL, 'ann', 'apple');

CREATE TABLE person (fname TEXT, lname TEXT, age INTEGER);
INSERT INTO person VALUES
  ('ann', 'smith', 30), ('bob', 'jones', 45), ('cal', NULL, 30);

CREATE TABLE people (id INTEGER, name TEXT, salary INTEGER);
INSERT INTO people VALUES (1, 'ann', 100), (2, 'bob', 200), (3, 'cal', NULL);

CREATE TABLE employees (id INTEGER, name TEXT, salary INTEGER, hire_date DATE);
INSERT INTO employees VALUES
  (1, 'ann', 100, DATE '2020-01-15'),
  (2, 'bob', 200, DATE '2021-06-30'),
  (3, 'cal', NULL, NULL);

CREATE TABLE users (id INTEGER, name TEXT, email TEXT, deleted BOOLEAN);
INSERT INTO users VALUES
  (1, 'ann', 'ann@example.com', FALSE),
  (2, 'bob', NULL, TRUE),
  (3, 'cal', 'cal@example.com', FALSE);

CREATE TABLE products (product_no INTEGER, product TEXT, category TEXT,
                       price INTEGER, cost INTEGER, supplier TEXT);
INSERT INTO products VALUES
  (1, 'apple', 'fruit', 10, 4, 'acme'),
  (2, 'pear',  'fruit', 12, NULL, 'acme'),
  (3, 'nail',  'tools', 3,  1, NULL);

CREATE TABLE numbers (id INTEGER, number INTEGER);
INSERT INTO numbers VALUES (1, 10), (2, -3), (3, NULL), (4, 10);

CREATE TABLE cities (name TEXT, country TEXT, year INTEGER, population INTEGER);
INSERT INTO cities VALUES
  ('Amsterdam', 'NL', 2000, 1005), ('Amsterdam', 'NL', 2010, 1065),
  ('Seattle',   'US', 2000, 564),  ('Seattle',   'US', 2010, NULL);

CREATE TABLE store (fruit TEXT, sold INTEGER);
INSERT INTO store VALUES ('apple', 10), ('pear', 20), ('apple', NULL);

CREATE TABLE data (name TEXT, department TEXT, fruit TEXT, sold INTEGER);
INSERT INTO data VALUES
  ('ann', 'sales', 'apple', 10), ('bob', 'ops', 'pear', 20),
  ('cal', 'sales', 'apple', NULL);

CREATE TABLE monthly_sales (empid INTEGER, dept TEXT, quarter TEXT,
                            month TEXT, sales INTEGER);
INSERT INTO monthly_sales VALUES
  (1, 'sales', 'Q1', 'jan', 100), (1, 'sales', 'Q2', 'apr', 200),
  (2, 'ops',   'Q1', 'jan', 50),  (2, 'ops',   'Q2', 'apr', NULL);

CREATE TABLE example (id INTEGER);
INSERT INTO example VALUES (1), (2), (3);

CREATE TABLE bar (id INTEGER, foo INTEGER);
INSERT INTO bar VALUES (1, 10), (2, 20), (3, NULL);

CREATE TABLE baz (id INTEGER, bar INTEGER);
INSERT INTO baz VALUES (1, 10), (2, NULL);

CREATE TABLE c (d INTEGER, e INTEGER, x INTEGER, y INTEGER);
INSERT INTO c VALUES (1, 2, 3, 4), (2, 3, 4, NULL);

CREATE TABLE y (id INTEGER, a INTEGER, b INTEGER);
INSERT INTO y VALUES (1, 1, 2), (2, 3, 4);

CREATE TABLE z (id INTEGER, a INTEGER, b INTEGER);
INSERT INTO z VALUES (1, 5, 6), (2, 7, NULL);

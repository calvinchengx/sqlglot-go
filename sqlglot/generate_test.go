package sqlglot

import (
	"errors"
	"strings"
	"testing"
)

func generate(t *testing.T, sql, dialect string) string {
	t.Helper()
	tree, err := ParseOne(sql, dialect)
	if err != nil {
		t.Fatalf("ParseOne(%q, %q): %v", sql, dialect, err)
	}
	out, err := Generate(tree, dialect)
	if err != nil {
		t.Fatalf("Generate(%q, %q): %v", sql, dialect, err)
	}
	return out
}

// The differential in harness/ holds the generator to the reference's output
// across the corpus. These pin the behaviours a reader would want stated, and
// the ones the corpus cannot reach.
func TestGenerateShapes(t *testing.T) {
	for _, c := range []struct{ name, sql, dialect, want string }{
		{"projections", "select a, b as x from t", "", "SELECT a, b AS x FROM t"},
		{"qualified", "select t.a from db.t", "", "SELECT t.a FROM db.t"},
		{"clauses", "select a from t where a > 1 group by a having a order by a limit 2 offset 3", "",
			"SELECT a FROM t WHERE a > 1 GROUP BY a HAVING a ORDER BY a LIMIT 2 OFFSET 3"},
		{"comma join", "select * from a, b", "", "SELECT * FROM a, b"},
		{"explicit join", "select * from a left outer join b on a.x = b.x", "",
			"SELECT * FROM a LEFT OUTER JOIN b ON a.x = b.x"},
		{"subquery", "select * from (select 1) as x", "", "SELECT * FROM (SELECT 1) AS x"},
		{"cte", "with x as (select 1) select * from x", "", "WITH x AS (SELECT 1) SELECT * FROM x"},
		{"union", "select 1 union all select 2", "", "SELECT 1 UNION ALL SELECT 2"},
		{"case", "select case when a then 1 else 2 end", "", "SELECT CASE WHEN a THEN 1 ELSE 2 END"},
		{"count distinct", "select count(distinct a) from t", "", "SELECT COUNT(DISTINCT a) FROM t"},
		{"window bare", "select row_number() over ()", "", "SELECT ROW_NUMBER() OVER ()"},
		{"interval", "select interval '1' day", "", "SELECT INTERVAL '1' DAY"},
		{"table alias columns", "select * from t as x(a, b)", "", "SELECT * FROM t AS x(a, b)"},
		{"struct literal", "select {'a': 1, 'b': x}", "duckdb", "SELECT {'a': 1, 'b': x}"},
		{"json extract", "select j -> '$.a'", "duckdb", "SELECT j -> '$.a'"},
		{"postgres folds a path into operators", "select j -> 'a' -> 'b'", "postgres",
			"SELECT j -> 'a' -> 'b'"},
		{"date add tsql reorders", "select dateadd(day, 1, d)", "tsql", "SELECT DATEADD(DAY, 1, d)"},
		{"quantifier all is spaced", "select x like all (array['a'])", "postgres",
			`SELECT x LIKE ALL (ARRAY['a'])`},
		{"quantifier any is not", "select x = any (array[1])", "postgres",
			"SELECT x = ANY(ARRAY[1])"},
		{"boolean value tsql", "select true", "tsql", "SELECT 1"},
		{"boolean condition tsql", "select a from t where true", "tsql", "SELECT a FROM t WHERE (1 = 1)"},
		{"boolean case condition tsql", "select case when false then 1 end", "tsql",
			"SELECT CASE WHEN (1 = 0) THEN 1 END"},
		{"boolean stays a word elsewhere", "select a from t where true", "duckdb",
			"SELECT a FROM t WHERE TRUE"},
		{"boolean join condition tsql", "select * from a join b on true join c on b.x = c.x", "tsql",
			"SELECT * FROM a JOIN b ON (1 = 1) JOIN c ON b.x = c.x"},
		{"boolean join condition false tsql", "select * from a join b on false", "tsql",
			"SELECT * FROM a JOIN b ON (1 = 0)"},
		{"array type suffixed", "cast(a as int[])", "duckdb", "CAST(a AS INT[])"},
		{"array type wrapped", "cast(a as int[])", "databricks", "CAST(a AS ARRAY<INT>)"},
		{"array type nested twice", "cast(a as int[][])", "databricks",
			"CAST(a AS ARRAY<ARRAY<INT>>)"},
		{"fixed size array", "cast(a as int[3])", "duckdb", "CAST(a AS INT[3])"},
		{"struct type", "cast(x as struct(a int))", "duckdb", "CAST(x AS STRUCT(a INT))"},
		{"struct type angle-bracketed", "cast(x as struct(a int))", "postgres",
			"CAST(x AS STRUCT<a INT>)"},
		{"struct field separator", "cast(x as struct(a int))", "databricks",
			"CAST(x AS STRUCT<a: INT>)"},
		{"map type", "cast(x as map(text, int))", "duckdb", "CAST(x AS MAP(TEXT, INT))"},
		{"word type parameter", "cast(a as varchar(max))", "tsql", "CAST(a AS VARCHAR(MAX))"},
		{"IN over a subquery", "select a from t where a in (select 1)", "",
			"SELECT a FROM t WHERE a IN (SELECT 1)"},
		{"IN over a doubly parenthesised subquery", "select a from t where a in ((select 1))", "",
			"SELECT a FROM t WHERE a IN ((SELECT 1))"},
		{"EXISTS over a query", "select a from t where exists(select 1)", "",
			"SELECT a FROM t WHERE EXISTS(SELECT 1)"},
		{"ANY keeps the subquery", "select a from t where a = any (select 1)", "",
			"SELECT a FROM t WHERE a = ANY (SELECT 1)"},
		{"ALL carries the select", "select a from t where a > all (select 1)", "",
			"SELECT a FROM t WHERE a > ALL (SELECT 1)"},
		{"parenthesised table", "select * from (x)", "", "SELECT * FROM (x)"},
		{"parenthesised table twice", "select * from ((x))", "", "SELECT * FROM ((x))"},
		{"parenthesised join tree", "select * from (a cross join b)", "",
			"SELECT * FROM (a CROSS JOIN b)"},
		{"parenthesised comma join", "select * from (a, b)", "", "SELECT * FROM (a, b)"},
		{"join outside the subquery parentheses", "select * from ((select 1 as x) cross join (select 2 as y)) as z", "",
			"SELECT * FROM ((SELECT 1 AS x) CROSS JOIN (SELECT 2 AS y)) AS z"},
		{"ORDER BY inside an aggregate", "select array_agg(x order by y)", "duckdb",
			"SELECT ARRAY_AGG(x ORDER BY y)"},
		{"ORDER BY inside an aggregate, descending", "select array_agg(x order by y desc)", "duckdb",
			"SELECT ARRAY_AGG(x ORDER BY y DESC)"},
		// DuckDB and PostgreSQL number from 1 and sqlglot's Bracket from 0, so
		// the parser subtracts and the writer adds back. Getting one side
		// without the other reads a[1] and writes a[0] -- a different element,
		// in SQL that runs.
		{"a subscript survives the shift", "SELECT a[1]", "duckdb", "SELECT a[1]"},
		{"and the one before it", "SELECT a[0]", "duckdb", "SELECT a[0]"},
		{"and in postgres", "SELECT a[2]", "postgres", "SELECT a[2]"},
		{"a dialect that numbers from 0 is untouched", "SELECT a[1]", "databricks",
			"SELECT a[1]"},
		// A COMPUTED index shifts too, and the shift only comes back out
		// where the sum still types as an integer. It did not: the offset
		// was built as the literal `-1`, which the port's annotator reads as
		// a DOUBLE, so the writer declined the shift and wrote a subscript
		// one element lower than the one it read. It is `Neg(1)` now, and
		// the two shifts fold against each other the way the reference's do.
		{"a computed index shifts and shifts back", "SELECT a[CAST(x AS INT)]",
			"postgres", "SELECT a[CAST(x AS INT) + 0]"},
		{"one already carrying an offset", "SELECT a[CAST(x AS INT) + 1]",
			"postgres", "SELECT a[CAST(x AS INT) + 1]"},
		{"and a binary index is parenthesised first", "SELECT a[1 # 2]",
			"postgres", "SELECT a[(1 # 2) + 0]"},
		{"a non-integer index is not shifted", "SELECT a['k']", "duckdb", "SELECT a['k']"},
		{"nor is a column index", "SELECT a[i]", "duckdb", "SELECT a[i]"},
		{"nor is a slice", "SELECT a[1:2]", "duckdb", "SELECT a[1:2]"},
		// PostgreSQL's range and JSONB operators. Each is a plain binary node
		// and the port used to refuse all but five of them.
		{"contains", "SELECT a @> b", "postgres", "SELECT a @> b"},
		{"contained by", "SELECT a <@ b", "postgres", "SELECT a <@ b"},
		{"overlaps", "SELECT a && b", "postgres", "SELECT a && b"},
		{"adjacent", "SELECT a -|- b", "postgres", "SELECT a -|- b"},
		{"extends right", "SELECT a &> b", "postgres", "SELECT a &> b"},
		{"extends left", "SELECT a &< b", "postgres", "SELECT a &< b"},
		{"case-insensitive regexp", "SELECT a ~* b", "postgres", "SELECT a ~* b"},
		{"jsonb has all keys", "SELECT j ?& b", "postgres", "SELECT j ?& b"},
		{"jsonb has any key", "SELECT j ?| b", "postgres", "SELECT j ?| b"},
		// A colon opens a slice in a SUBSCRIPT and a bound parameter in an
		// array LITERAL. Reading the second as the first built a Slice over a
		// column where the reference builds a placeholder.
		{"a colon in an array literal is a parameter", "SELECT [:a]", "databricks",
			"SELECT ARRAY(:a)"},
		{"a colon in a subscript is a slice", "SELECT x[:2]", "duckdb", "SELECT x[:2]"},
		// `->` binds LOOSER than arithmetic, a cast or a unary minus, and
		// TIGHTER than a comparison, IS, LIKE or AND. Having it at the tightest
		// level read `0 ^ 0 -> ''` as `0 ^ (0 -> '')`: same tokens, different
		// question, and no corpus statement had the combination.
		{"the arrow binds looser than power", "SELECT 0^0->''", "duckdb",
			"SELECT POWER(0, 0) -> '$'"},
		{"and looser than a cast", "SELECT a::INT->'x'", "duckdb",
			"SELECT CAST(a AS INT) -> '$.x'"},
		{"and looser than concatenation", "SELECT a||b->'x'", "duckdb",
			"SELECT a || b -> '$.x'"},
		// Parenthesised as an OPERAND, which these two expectations used to
		// deny. They were written by hand rather than read off the reference,
		// and no corpus statement has the shape, so nothing contradicted them
		// until the arrow moved to where the reference keeps it.
		{"but tighter than a comparison", "SELECT 1 WHERE a->'x'=b", "duckdb",
			"SELECT 1 WHERE (a -> '$.x') = b"},
		{"and tighter than AND", "SELECT 1 WHERE a->'x' AND b", "duckdb",
			"SELECT 1 WHERE (a -> '$.x') AND b"},
		{"and chains left", "SELECT a->'x'->'y'", "duckdb", "SELECT a -> '$.x' -> '$.y'"},
		// Neutral, T-SQL and Databricks read the arrows as accessors, tighter
		// than arithmetic. The duckdb cases above pin the other tier.
		{"the arrow binds tighter than addition", "SELECT 1 + x -> 'y'", "",
			"SELECT 1 + JSON_EXTRACT(x, '$.y')"},
		{"and tighter on the right too", "SELECT x -> 'y' + 1", "",
			"SELECT JSON_EXTRACT(x, '$.y') + 1"},
		{"and tighter than multiplication", "SELECT a -> b * c", "",
			"SELECT JSON_EXTRACT(a, b) * c"},
		{"databricks spells the tighter arrow as a colon", "SELECT 1 + x -> 'y'", "databricks",
			"SELECT 1 + x:y"},
		{"postgres swallows the addition", "SELECT 1 + x -> 'y'", "postgres",
			"SELECT 1 + x -> 'y'"},
		{"postgres keeps addition on the right in the path", "SELECT x -> 'y' + 1", "postgres",
			"SELECT x -> ('y' + 1)"},
		{"duckdb swallows the addition", "SELECT 1 + x -> 'y'", "duckdb",
			"SELECT 1 + x -> '$.y'"},
		// A subquery's alias names the subquery; the joins come after it.
		{"an alias before its joins", "SELECT 0 FROM ((A) A, A A)", "duckdb",
			"SELECT 0 FROM ((A) AS A, A AS A)"},
		// A bare ALL is a column; the quantifier needs something to quantify.
		{"a column called All", "SELECT All", "databricks", "SELECT All"},
		// ALL followed by a dot is a table named All qualifying a column, not
		// the quantifier -- the reference only takes the quantifier when a
		// dot does not follow.
		{"a table called All qualifying a column", "SELECT All.x FROM All", "",
			"SELECT All.x FROM All"},
		{"a dotted parameter", "SELECT :A.a", "databricks", "SELECT :A.a"},
		{"a string after the dot stays a string", "SELECT $0.'AS'", "duckdb",
			"SELECT $0.'AS'"},
		// ESCAPE wraps the comparison, and the NEGATION with it.
		{"like with an escape", "SELECT 1 WHERE x LIKE '%y%' ESCAPE '!'", "",
			"SELECT 1 WHERE x LIKE '%y%' ESCAPE '!'"},
		{"escape wraps the negation", "SELECT 1 WHERE x NOT ILIKE '%y' ESCAPE '#'", "postgres",
			"SELECT 1 WHERE x NOT ILIKE '%y' ESCAPE '#'"},
		// INTERVAL as a TYPE carries a unit, and the DataType's `this` is an
		// Interval node rather than a type name.
		{"interval type with a unit", "SELECT CAST('45' AS INTERVAL DAY)", "",
			"SELECT CAST('45' AS INTERVAL DAY)"},
		{"interval type with a span", "SELECT CAST('1 2' AS INTERVAL DAY TO HOUR)", "",
			"SELECT CAST('1 2' AS INTERVAL DAY TO HOUR)"},
		{"a bare interval type", "SELECT CAST('1 DAY' AS INTERVAL)", "postgres",
			"SELECT CAST('1 DAY' AS INTERVAL)"},
		// These names have a PARSER of their own in the reference, not a
		// signature. The port used to refuse them on sight; only the form that
		// needs the dedicated parser is refused now.
		{"if with parentheses is an ordinary call", "SELECT IF(x > 0, 1, 2)", "databricks",
			"SELECT IF(x > 0, 1, 2)"},
		{"map with parentheses too", "SELECT MAP([1], [2])", "duckdb", "SELECT MAP([1], [2])"},
		{"an empty argument list is an anonymous call", "SELECT 1 WHERE 0 < ALL()", "tsql",
			"SELECT 1 WHERE 0 < ALL()"},
		// DuckDB drops a ZERO group from REGEXP_EXTRACT entirely. That is a
		// rule to follow, not a reason to refuse -- and refusing it also
		// turned away every other literal in the same slot.
		{"a zero group is dropped", "SELECT REGEXP_EXTRACT(a, 'p(g)', 0)", "duckdb",
			"SELECT REGEXP_EXTRACT(a, 'p(g)')"},
		{"a non-zero group is kept", "SELECT REGEXP_EXTRACT(a, 'p(g)', 1)", "duckdb",
			"SELECT REGEXP_EXTRACT(a, 'p(g)', 1)"},
		{"and for extract-all too", "SELECT REGEXP_EXTRACT_ALL(a, 'p(g)', 0)", "duckdb",
			"SELECT REGEXP_EXTRACT_ALL(a, 'p(g)')"},
		// A wildcard stands where a name or an index would.
		{"a wildcard subscript", "SELECT x -> '$.y[*]'", "duckdb", "SELECT x -> '$.y[*]'"},
		{"a wildcard key", "SELECT x -> '$.y.*'", "duckdb", "SELECT x -> '$.y.*'"},
		// A path the port cannot read is handed back as the string it was
		// written as, which is what the reference does.
		{"an unreadable path stays a string", `SELECT 0 -> '[""@""]'`, "duckdb",
			`SELECT 0 -> '[""@""]'`},
		// The path is written INSIDE a string literal, so a quote in a key has
		// to be escaped for that literal or the statement ends early.
		{"a quote in a path key is escaped", `SELECT 0 -> '"a''b"'`, "duckdb",
			`SELECT 0 -> '$."a''b"'`},
		// In ARGUMENT position `x -> y` is a lambda, and the parameter can be
		// written as a number, a string or a quoted name. Outside a call the
		// same tokens are a JSON extraction, which is why this only matters
		// here. A string parameter comes back QUOTED.
		{"a string names a lambda parameter", "SELECT A('abc' -> x)", "databricks",
			"SELECT A(`abc` -> x)"},
		// A number names one too, and comes back unquoted.
		{"a number names a lambda parameter", "SELECT A(0 -> x)", "databricks",
			"SELECT A(0 -> x)"},
		{"an empty one too", "SELECT ``(\"\" -> \"\")", "databricks",
			"SELECT ``(`` -> '')"},
		{"and a quoted one reads back", "SELECT A(`abc` -> x)", "databricks",
			"SELECT A(`abc` -> x)"},
		// DuckDB's DATE_TRUNC builds a DateTrunc over a DATE and a
		// TimestampTrunc over anything else -- two different shapes. The type
		// is the one the argument CARRIES at parse time, so only an explicit
		// cast selects the first.
		{"date trunc over a date", "SELECT DATE_TRUNC('DAY', CAST(x AS DATE))", "duckdb",
			"SELECT DATE_TRUNC('DAY', CAST(x AS DATE))"},
		{"date trunc over a timestamp", "SELECT DATE_TRUNC('DAY', CAST(x AS TIMESTAMP))", "duckdb",
			"SELECT DATE_TRUNC('DAY', CAST(x AS TIMESTAMP))"},
		{"date trunc over an untyped column", "SELECT DATE_TRUNC('DAY', x)", "duckdb",
			"SELECT DATE_TRUNC('DAY', x)"},
		// A third argument -- PostgreSQL's own DATE_TRUNC takes a time zone
		// there -- is never read by this builder in any dialect that names
		// it this way, and is dropped rather than refused, the same as the
		// reference drops it.
		{"date trunc drops a time zone this builder never reads",
			"SELECT DATE_TRUNC('DAY', x, 'UTC')", "postgres",
			"SELECT DATE_TRUNC('DAY', x)"},
		// A date PLUS an interval is a date, but not until something annotates
		// it -- at parse time it is just a sum, and the default applies.
		{"a sum is not a date at parse time",
			"SELECT DATE_TRUNC('WEEK', CAST(x AS DATE) + INTERVAL '1' DAY)", "duckdb",
			"SELECT DATE_TRUNC('WEEK', CAST(x AS DATE) + INTERVAL '1' DAY)"},
		// A national string keeps its prefix, and its body is escaped like
		// any other string's -- a template would have written it verbatim.
		{"a national string", "SELECT N'abc'", "tsql", "SELECT N'abc'"},
		{"with a quote in it", "SELECT N'a''b'", "tsql", "SELECT N'a''b'"},
		{"json extract scalar", "select j ->> '$.a.b'", "duckdb", "SELECT j ->> '$.a.b'"},
		{"json subscript", "select j -> '$[0]'", "duckdb", "SELECT j -> '$[0]'"},
		{"json quoted key", `select j -> '$."a b"'`, "duckdb", `SELECT j -> '$."a b"'`},
		{"struct key needing quotes", "select {'a b': 1}", "duckdb", "SELECT {'a b': 1}"},
		{"filter", "select sum(x) filter(where x > 1) from t", "duckdb",
			"SELECT SUM(x) FILTER(WHERE x > 1) FROM t"},
		{"cte columns", "with t(a, b) as (select 1, 2) select * from t", "",
			"WITH t(a, b) AS (SELECT 1, 2) SELECT * FROM t"},
		{"at time zone", "select x at time zone 'UTC'", "", "SELECT x AT TIME ZONE 'UTC'"},
		{"at time zone chains left", "select x at time zone 'UTC' at time zone 'Y'", "",
			"SELECT x AT TIME ZONE 'UTC' AT TIME ZONE 'Y'"},
		{"nulls last is duckdb default", "select a from t order by a nulls last", "duckdb",
			"SELECT a FROM t ORDER BY a"},
		{"nulls first is written", "select a from t order by a nulls first", "duckdb",
			"SELECT a FROM t ORDER BY a NULLS FIRST"},
		{"tsql has no nulls clause", "select a from t order by a nulls first", "tsql",
			"SELECT a FROM t ORDER BY a"},
		{"unnest", "select a from unnest(x)", "", "SELECT a FROM UNNEST(x)"},
		{"unnest aliased", "select a from unnest(x) as t(c)", "", "SELECT a FROM UNNEST(x) AS t(c)"},
		{"convert", "select convert(varchar(10), x)", "tsql", "SELECT CONVERT(VARCHAR(10), x)"},
		{"convert with style", "select convert(int, x, 1)", "tsql", "SELECT CONVERT(INTEGER, x, 1)"},
		{"string agg", "select string_agg(x, ',')", "duckdb", "SELECT LISTAGG(x, ',')"},
		{"extract", "select extract(month from d)", "duckdb", "SELECT EXTRACT(MONTH FROM d)"},
		{"extract tsql spells it datepart", "select extract(month from d)", "tsql",
			"SELECT DATEPART(MONTH, d)"},
		// EXTRACT's unit is USUALLY a bare word, but it may be a call: ISO
		// week timing takes an argument, and the reference tries a function
		// before falling back to the word.
		{"extract over a call", "select extract(week(monday) from created_at)", "duckdb",
			"SELECT EXTRACT(WEEK(monday) FROM created_at)"},
		{"trim both", "select trim(both 'x' from s)", "postgres", "SELECT TRIM(BOTH 'x' FROM s)"},
		{"trim duckdb drops the position", "select trim(both 'x' from s)", "duckdb",
			"SELECT TRIM(s, 'x')"},
		{"substring from for", "select substring(x from 1 for 2)", "postgres",
			"SELECT SUBSTRING(x FROM 1 FOR 2)"},
		{"position duckdb", "select position(a in b)", "duckdb", "SELECT STRPOS(b, a)"},
		{"position tsql", "select position(a in b)", "tsql", "SELECT CHARINDEX(a, b)"},
		{"lambda one param", "select list_transform(a, x -> x + 1)", "duckdb",
			"SELECT LIST_TRANSFORM(a, x -> x + 1)"},
		{"lambda two params", "select list_reduce(a, (x, y) -> x + y)", "duckdb",
			"SELECT LIST_REDUCE(a, (x, y) -> x + y)"},
		// The same lambda spelled the other way DuckDB has for it. The form
		// is recorded on the node, so it comes back as it was written --
		// keyword, colon, and no parentheses however many parameters.
		{"colon lambda one param", "select list_transform(a, lambda x : x + 1)", "duckdb",
			"SELECT LIST_TRANSFORM(a, LAMBDA x : x + 1)"},
		{"colon lambda two params", "select list_reduce(a, lambda x, y : x + y)", "duckdb",
			"SELECT LIST_REDUCE(a, LAMBDA x, y : x + y)"},
		// A struct names its fields, and the two dialects disagree about
		// which half comes first.
		{"struct fields databricks", "select struct(1 as a, 'x' as b)", "databricks",
			"SELECT STRUCT(1 AS a, 'x' AS b)"},
		{"struct fields duckdb", "select struct(1 as a, 'x' as b)", "duckdb",
			"SELECT {'a': 1, 'b': 'x'}"},
		{"struct field needing quotes", "select struct(1 as `a b`)", "databricks",
			"SELECT STRUCT(1 AS `a b`)"},
		{"struct with an unnamed field", "select struct(x, x as y)", "databricks",
			"SELECT STRUCT(x, x AS y)"},
		// A field named with an equals sign is the same field: the reference
		// turns every key-value shape into one node before the builder runs,
		// so this comes back written the dialect's one way.
		{"struct field named with equals", "select struct(a = 1)", "databricks",
			"SELECT STRUCT(1 AS a)"},
		// A DuckDB arrow extraction is parenthesised wherever it is an
		// OPERAND, and a subscript and a NOT are two of the four parents the
		// reference counts as one. Without the parentheses the subscript
		// lands on the path rather than on the result.
		{"extraction under a subscript", "select json_extract(c, '$.a')[1]", "duckdb",
			"SELECT (c -> '$.a')[1]"},
		{"extraction under a NOT", "select not json_extract(c, '$.a')", "duckdb",
			"SELECT NOT (c -> '$.a')"},
		{"extraction under an IN", "select json_extract(c, '$.a') in (1)", "duckdb",
			"SELECT (c -> '$.a') IN (1)"},
		// Two builders that REWRITE a string argument rather than carrying
		// it. Probed with a column neither shows; probed with a string both
		// look like builders nobody can describe, which is why both names
		// used to be refused at every arity.
		{"a string cast by its builder", "select datetrunc(month, 'foo')", "tsql",
			"SELECT DATETRUNC(MONTH, CAST('foo' AS DATETIME2))"},
		{"a string left alone when it is not one", "select datetrunc(month, foo)", "tsql",
			"SELECT DATETRUNC(MONTH, foo)"},
		{"a step string that becomes an interval",
			"generate_series(a, b, '  2   days  ')", "postgres",
			"GENERATE_SERIES(a, b, INTERVAL '2 DAYS')"},
		{"a step written as an interval already",
			"generate_series(a, b, INTERVAL '2 DAYS')", "postgres",
			"GENERATE_SERIES(a, b, INTERVAL '2 DAYS')"},
		// Subscripts over things the port can now type. Each needs the base's
		// type before the index can be shifted between DuckDB's numbering and
		// the tree's.
		{"a subscript over a map", "select map([1, 2], ['a', 'b'])[2]", "duckdb",
			"SELECT MAP([1, 2], ['a', 'b'])[2]"},
		{"a subscript over a brace map", "select (map {'x': 1})['x']", "duckdb",
			"SELECT (MAP {'x': 1})['x']"},
		// The group argument is the builder's own default, so it comes back
		// written as the two-argument call the reference writes.
		{"a slice over a call nobody has a builder for",
			"select regexp_extract_all(s, 'pattern', 0)[2:]", "duckdb",
			"SELECT REGEXP_EXTRACT_ALL(s, 'pattern')[2:]"},
		// DuckDB's SUMMARIZE describes what is in something rather than
		// selecting from it, and it stands wherever a query stands.
		{"summarize a table", "summarize tbl", "duckdb", "SUMMARIZE tbl"},
		{"summarize a query", "summarize select * from tbl", "duckdb",
			"SUMMARIZE SELECT * FROM tbl"},
		{"summarize a file", "summarize table 'x.csv'", "duckdb", "SUMMARIZE TABLE 'x.csv'"},
		{"summarize inside a FROM", "select * from (summarize tbl)", "duckdb",
			"SELECT * FROM (SUMMARIZE tbl)"},
		// A CREATE whose body is a TABLE rather than a query. The name may be
		// a word that spells an option elsewhere.
		{"a create over a table", "create table t as other", "", "CREATE TABLE t AS other"},
		{"a create over a quoted name", `create table t as "minvalue"`, "",
			`CREATE TABLE t AS "minvalue"`},
		// A word that is a CALL in a projection and a NAME in a FROM.
		{"a no-paren name as a table", "select * from current_date", "",
			"SELECT * FROM current_date"},
		{"and the same word as a call", "select current_date", "", "SELECT CURRENT_DATE"},
		// A procedure: its name, its parameters in either shape, the words it
		// may say after WITH, and a body that is a block of statements.
		{"a procedure", "create procedure foo as select 1", "tsql",
			"CREATE PROCEDURE foo AS SELECT 1"},
		{"a procedure with no body at all", "create procedure foo", "tsql",
			"CREATE PROCEDURE foo"},
		{"a procedure with bare parameters", "create procedure foo @a int, @b int as select 1",
			"tsql", "CREATE PROCEDURE foo @a INTEGER, @b INTEGER AS SELECT 1"},
		// A parameter whose type carries parentheses of its own: the cut that
		// finds where the bare list ends has to look past them.
		{"a bare parameter with a sized type",
			"create procedure foo @a decimal(1, 2) as select 1", "tsql",
			"CREATE PROCEDURE foo @a NUMERIC(1, 2) AS SELECT 1"},
		{"a procedure with a parameter list", "create procedure foo(@a int = 1)", "tsql",
			"CREATE PROCEDURE foo(@a INTEGER = 1)"},
		{"a parameter whose type is introduced by AS", "create procedure foo(@a as int = 1)",
			"tsql", "CREATE PROCEDURE foo(@a INTEGER = 1)"},
		{"a procedure body in a block", "create procedure foo as begin select 1; end", "tsql",
			"CREATE PROCEDURE foo AS BEGIN SELECT 1; END"},
		{"a procedure body of several statements",
			"create procedure foo as begin select 1; select 2; end", "tsql",
			"CREATE PROCEDURE foo AS BEGIN SELECT 1; SELECT 2; END"},
		{"a procedure body written as a string",
			`create procedure a.b.c() as 'DECLARE BEGIN; END'`, "",
			`CREATE PROCEDURE a.b.c() AS 'DECLARE BEGIN; END'`},
		{"a procedure with a view attribute", "create procedure foo with encryption as select 1",
			"tsql", "CREATE PROCEDURE foo WITH ENCRYPTION AS SELECT 1"},
		{"a procedure with an option", "create procedure foo with recompile as select 1", "tsql",
			"CREATE PROCEDURE foo WITH RECOMPILE AS SELECT 1"},
		{"a procedure that says who runs it",
			"create procedure foo with execute as owner as select 1", "tsql",
			"CREATE PROCEDURE foo WITH EXECUTE AS OWNER AS SELECT 1"},
		{"a procedure that names who runs it",
			"create procedure foo with execute as 'username' as select 1", "tsql",
			"CREATE PROCEDURE foo WITH EXECUTE AS 'username' AS SELECT 1"},
		{"a procedure with several options",
			"create procedure foo with execute as owner, schemabinding as select 1", "tsql",
			"CREATE PROCEDURE foo WITH EXECUTE AS OWNER, SCHEMABINDING AS SELECT 1"},
		// A bare IF opens a STATEMENT in one dialect. Its block is written
		// with BEGIN whether or not one was written, and the END that closed
		// it is a statement of the block.
		{"an if statement", "if 1 = 1 select 1", "tsql", "IF 1 = 1 BEGIN SELECT 1"},
		{"an if with a parenthesised condition", "if (1 = 1) select 1", "tsql",
			"IF 1 = 1 BEGIN SELECT 1"},
		{"an if over a block", "if 1 = 1 begin drop table t; end", "tsql",
			"IF 1 = 1 BEGIN DROP TABLE t; END"},
		{"an if with an else", "if 1 = 1 select 1 else select 2", "tsql",
			"IF 1 = 1 BEGIN SELECT 1; ELSE BEGIN SELECT 2"},
		// The condition takes an implicit alias, and the word after EXISTS is
		// one. That is the reference being odd rather than this being
		// careless -- it writes the statement back this way too.
		{"an if whose condition swallows what follows it",
			"if not exists (select * from t) exec('create schema foo')", "tsql",
			"IF NOT EXISTS(SELECT * FROM t) AS exec BEGIN ('create schema foo')"},
		// A retyped column: both words that introduce the type are optional,
		// and the type may be followed by a collation and by whether the
		// column still takes nulls.
		{"a retyped column", "alter table a alter column b integer", "tsql",
			"ALTER TABLE a ALTER COLUMN b INTEGER"},
		{"a retyped column that takes no nulls", "alter table a alter column b int not null",
			"tsql", "ALTER TABLE a ALTER COLUMN b INTEGER NOT NULL"},
		{"a retyped column that takes nulls", "alter table a alter column b int null", "tsql",
			"ALTER TABLE a ALTER COLUMN b INTEGER NULL"},
		{"a retyped column with a collation",
			"alter table a alter column b varchar(10) collate Latin1_General_CI_AS not null",
			"tsql", "ALTER TABLE a ALTER COLUMN b VARCHAR(10) COLLATE Latin1_General_CI_AS NOT NULL"},
		{"a retyped column the long way", "alter table a alter column b set data type int",
			"postgres", "ALTER TABLE a ALTER COLUMN b SET DATA TYPE INT"},
		{"a constraint added but not checked",
			"alter table t add constraint c unique (a) not valid", "postgres",
			"ALTER TABLE t ADD CONSTRAINT c UNIQUE (a) NOT VALID"},
		// TRIM in its comma form. Which operand is the STRING and which the
		// characters depends on the separator and on the dialect.
		{"trim with a comma", "select trim(s, 'xy')", "duckdb", "SELECT TRIM(s, 'xy')"},
		{"trim from the left", "select ltrim(s, 'xy')", "duckdb", "SELECT LTRIM(s, 'xy')"},
		{"trim with the characters first", "trim('a', 'abc')", "databricks",
			"TRIM('a' FROM 'abc')"},
		{"trim with a position", "select trim(both 'x' from s)", "duckdb", "SELECT TRIM(s, 'x')"},
		// The two columns that say when a row was current, and the property
		// that turns the tracking on. The property carries the WITH it was
		// written inside and writes the word back itself.
		{"a table that tracks its own history",
			"create table t (a int, period for system_time (x, y)) with(system_versioning=on)",
			"tsql",
			"CREATE TABLE t (a INTEGER, PERIOD FOR SYSTEM_TIME (x, y)) WITH(SYSTEM_VERSIONING=ON)"},
		{"system versioning turned off", "create table t (a int) with(system_versioning=off)",
			"tsql", "CREATE TABLE t (a INTEGER) WITH(SYSTEM_VERSIONING=OFF)"},
		{"system versioning with a history table",
			"create table t (a int) with(system_versioning=on(history_table=db.h))", "tsql",
			"CREATE TABLE t (a INTEGER) WITH(SYSTEM_VERSIONING=ON(HISTORY_TABLE=db.h))"},
		// A chain naming a lambda parameter is rebuilt as plain identifiers,
		// with no column left in it -- but only where something ENCLOSES it.
		// At the top of the body the reference's replacement lands nowhere
		// and the column survives whole.
		{"a parameter alone", "filter(a, x -> x)", "", "FILTER(a, x -> x)"},
		{"a chain off a parameter", "filter(a, x -> x.b.c)", "", "FILTER(a, x -> x.b.c)"},
		{"the same chain inside a call", "filter(a, x -> foo(x.b.c))", "",
			"FILTER(a, x -> FOO(x.b.c))"},
		{"a chain past the column's four slots", "filter(a, x -> x.a.b.c.d.e)", "",
			"FILTER(a, x -> x.a.b.c.d.e)"},
		// A chain that hangs off something other than a column, and one whose
		// column names no parameter: neither is rewritten.
		{"a chain off a call", "filter(a, x -> foo(1).b)", "", "FILTER(a, x -> FOO(1).b)"},
		{"a chain off another name", "filter(a, x -> y.a.b.c.d.e)", "",
			"FILTER(a, x -> y.a.b.c.d.e)"},
		{"a star past a deep chain", "select a.b.c.d.e.*", "", "SELECT a.b.c.d.e.*"},
		// An arrow after parentheses is not enough to make a lambda: what is
		// inside them has to be a list of NAMES. `((A)) -> '$[0]'` is a JSON
		// extraction from a parenthesised column.
		{"an arrow after a parenthesised column", "@((A))->0", "duckdb",
			"ABS(((A)) -> '$[0]')"},
		{"and the same inside a call", "foo(((A)) -> 0)", "duckdb", "FOO(((A)) -> '$[0]')"},
		{"an arrow after a parenthesised sum", "foo((a + b) -> 0)", "duckdb",
			"FOO((a + b) -> '$[0]')"},
		{"an arrow after two names with no comma", "foo((a b) -> 0)", "duckdb",
			"FOO((a AS b) -> '$[0]')"},
		{"and a real parameter list beside them", "foo((a, b) -> 0)", "duckdb",
			"FOO((a, b) -> 0)"},
		// A path key holding the very quote that delimits it. The reference
		// escapes it with a backslash; without that the key ends early and
		// the port could not read back what it had just written.
		{"a path key holding its own quote", "SELECT c:`a\"b`", "databricks",
			"SELECT c:[\"a\\\"b\"]"},
		// Databricks spells the same tree as a colon path, which is not an
		// operator and takes no parentheses anywhere.
		{"colon path under a subscript", "select json_extract(c, '$.a')[1]", "databricks",
			"SELECT c:a[1]"},
		{"interval unit in string", "select interval '1 day'", "", "SELECT INTERVAL '1' DAY"},
		{"interval number becomes string", "select interval 1 day", "", "SELECT INTERVAL '1' DAY"},
		{"interval postgres folds the unit", "select interval '1' day", "postgres", "SELECT INTERVAL '1 DAY'"},
		{"interval span", "select interval '1' hour to second", "", "SELECT INTERVAL '1' HOUR TO SECOND"},
		{"interval many units stays whole", "select interval '1 year 2 months'", "",
			"SELECT INTERVAL '1 year 2 months'"},
		{"window partition order", "select sum(x) over (partition by a order by b)", "",
			"SELECT SUM(x) OVER (PARTITION BY a ORDER BY b)"},
		// The frame's own words keep the CASE they were written in, which is
		// what the reference keeps -- only the words the writer supplies
		// itself, BETWEEN and CURRENT ROW, are its own.
		{"window frame", "select sum(x) over (rows between unbounded preceding and current row)", "",
			"SELECT SUM(x) OVER (rows BETWEEN UNBOUNDED preceding AND CURRENT ROW)"},
		{"window single bound normalises", "select sum(x) over (rows 3 preceding)", "",
			"SELECT SUM(x) OVER (rows BETWEEN 3 preceding AND CURRENT ROW)"},
		{"window frame upper", "SELECT SUM(x) OVER (ROWS BETWEEN 3 PRECEDING AND CURRENT ROW)", "",
			"SELECT SUM(x) OVER (ROWS BETWEEN 3 PRECEDING AND CURRENT ROW)"},
		{"count distinct many", "select count(distinct a, b) from t", "", "SELECT COUNT(DISTINCT a, b) FROM t"},
		{"array literal", "select [1, 2, 3]", "duckdb", "SELECT [1, 2, 3]"},
		{"array keyword literal", "select array[1, 2]", "postgres", "SELECT ARRAY[1, 2]"},
		{"subscript", "select a[1]", "databricks", "SELECT a[1]"},
		{"cast", "select cast(a as varchar(10))", "", "SELECT CAST(a AS VARCHAR(10))"},
		{"negated like", "select * from t where a not like 'x'", "",
			"SELECT * FROM t WHERE a NOT LIKE 'x'"},
		// `NOT a IS NULL`, the reference's own spelling outside PostgreSQL.
		{"is not null", "select * from t where a is not null", "",
			"SELECT * FROM t WHERE NOT a IS NULL"},
		{"quoted string", "select 'it''s'", "", "SELECT 'it''s'"},
		{"double negation", "select - -5", "", "SELECT - -5"},
		{"explicit ascending", "select a from t order by a asc", "", "SELECT a FROM t ORDER BY a ASC"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := generate(t, c.sql, c.dialect); got != c.want {
				t.Errorf("Generate(%q)\n  want %s\n  got  %s", c.sql, c.want, got)
			}
		})
	}
}

// A quoted name is written with the delimiters the dialect writes, not the
// ones it happens to accept -- T-SQL reads both "x" and [x] and writes [x].
func TestGenerateQuotesIdentifiersPerDialect(t *testing.T) {
	for _, c := range []struct{ dialect, sql, want string }{
		{"", `SELECT "a b" FROM t`, `SELECT "a b" FROM t`},
		{"tsql", `SELECT "a b" FROM t`, "SELECT [a b] FROM t"},
		{"postgres", `SELECT "a b" FROM t`, `SELECT "a b" FROM t`},
		// Databricks reads double quotes as a string, so a name written that
		// way is a string there -- and the reference writes it back as one.
		{"databricks", "SELECT `a b` FROM t", "SELECT `a b` FROM t"},
		{"databricks", `SELECT "a b" FROM t`, `SELECT 'a b' FROM t`},
	} {
		if got := generate(t, c.sql, c.dialect); got != c.want {
			t.Errorf("[%s] %s\n  want %s\n  got  %s", c.dialect, c.sql, c.want, got)
		}
	}
	// A closing delimiter inside the name is doubled, or the name would end early.
	if got := generate(t, "SELECT [c]] d] FROM t", "tsql"); got != "SELECT [c]] d] FROM t" {
		t.Errorf("got %s", got)
	}
}

// This is the guard's whole operation: read a statement, put a ceiling on it,
// hand it back. The row limit is one node; where it lands in the text is the
// dialect's business, which is the reason the rewrite works at all.
func TestRewritingARowCeiling(t *testing.T) {
	for _, c := range []struct{ dialect, sql, want string }{
		{"tsql", "SELECT * FROM dbo.fct_sales", "SELECT TOP 500 * FROM dbo.fct_sales"},
		{"duckdb", "SELECT * FROM main.tickets", "SELECT * FROM main.tickets LIMIT 500"},
		{"postgres", "SELECT * FROM support.tickets", "SELECT * FROM support.tickets LIMIT 500"},
	} {
		t.Run(c.dialect, func(t *testing.T) {
			tree, err := ParseOne(c.sql, c.dialect)
			if err != nil {
				t.Fatal(err)
			}
			tree.Set("limit", New("Limit",
				Arg{"this", nil},
				Arg{"expression", New("Literal", Arg{"this", "500"}, Arg{"is_string", false})},
				Arg{"limit_options", nil},
				Arg{"expressions", nil},
			))
			got, err := Generate(tree, c.dialect)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("after the rewrite\n  want %s\n  got  %s", c.want, got)
			}
		})
	}
}

// Whatever the generator writes has to read back as the same tree. A rewrite
// that changed the statement's meaning on the way out would be worse than a
// refusal, because nothing downstream re-reads the original text.
//
// Not every statement is stable even in the reference: it upper-cases an
// anonymous call's name, so `read_csv_auto(…)` comes back as a differently
// spelled node. That is the reference's behaviour and the port matches it;
// what is asserted here is that the port is stable wherever the reference is.
func TestRoundTrip(t *testing.T) {
	for _, c := range []struct{ sql, dialect string }{
		{"SELECT a, b AS x FROM db.t WHERE a > 1 AND b IN (1, 2) ORDER BY a DESC LIMIT 10", ""},
		{"WITH x AS (SELECT a FROM t) SELECT COUNT(*) FROM x", "duckdb"},
		{"SELECT TOP 10 a FROM dbo.t ORDER BY a DESC", "tsql"},
		{"SELECT * FROM a, b JOIN c ON c.x = a.x", ""},
		{"SELECT CASE WHEN a THEN CAST(b AS INT) ELSE NULL END FROM t", ""},
		{"SELECT a FROM t1 UNION ALL SELECT b FROM t2 LIMIT 5", ""},
	} {
		first, err := ParseOne(c.sql, c.dialect)
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", c.sql, err)
		}
		written, err := Generate(first, c.dialect)
		if err != nil {
			t.Fatalf("Generate(%q): %v", c.sql, err)
		}
		second, err := ParseOne(written, c.dialect)
		if err != nil {
			t.Fatalf("the port could not read back what it wrote: %q -> %q: %v", c.sql, written, err)
		}
		if string(first.DumpJSON()) != string(second.DumpJSON()) {
			t.Errorf("round trip changed the tree\n  from %s\n  to   %s", c.sql, written)
		}
	}
}

func TestGenerateRefusals(t *testing.T) {
	// A node the generator has no writer for stops the rewrite.
	_, err := Generate(New("NotARealNode"), "")
	if err == nil {
		t.Fatal("an unknown node should not be written")
	}
	if !errors.Is(err, ErrGenerate) {
		t.Errorf("errors.Is(err, ErrGenerate) is false: %v", err)
	}
	var named *GenerateError
	if !errors.As(err, &named) || named.What != "NotARealNode" {
		t.Errorf("GenerateError.What = %#v, want NotARealNode", named)
	}
	if want := `sqlglot-go: cannot generate SQL for NotARealNode`; err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}

	_, err = Generate(New("Select"), "oracle")
	if err == nil {
		t.Fatal("an unknown dialect should not be written")
	}
	if errors.Is(err, ErrGenerate) {
		t.Errorf("a missing dialect is not a generate failure: %v", err)
	}

	// Nil is nothing, not an error: an absent clause writes as empty.
	if got, err := Generate(nil, ""); err != nil || got != "" {
		t.Errorf("Generate(nil) = %q, %v", got, err)
	}
}

// T-SQL will not accept an unnamed column in a derived table, so the reference
// synthesises the missing names on the way out. Both executors have to do it,
// or they send the engine different statements for the same question.
func TestNamingDerivedOutputs(t *testing.T) {
	for _, c := range []struct{ sql, want string }{
		{"SELECT * FROM (SELECT a) AS x", "SELECT * FROM (SELECT a AS a) AS x"},
		{"SELECT * FROM (SELECT 1) AS x", "SELECT * FROM (SELECT 1 AS [1]) AS x"},
		{`SELECT * FROM (SELECT "c") AS x`, "SELECT * FROM (SELECT [c] AS [c]) AS x"},
		// Nothing to name: a star stays a star, and an alias is left alone.
		{"SELECT * FROM (SELECT * FROM u) AS x", "SELECT * FROM (SELECT * FROM u) AS x"},
		{"SELECT * FROM (SELECT t.* FROM u AS t) AS x", "SELECT * FROM (SELECT t.* FROM u AS t) AS x"},
		{"SELECT * FROM (SELECT a AS b) AS x", "SELECT * FROM (SELECT a AS b) AS x"},
		// Nothing to name it after, so it is named by position.
		{"SELECT * FROM (SELECT COUNT(*) FROM u) AS x",
			"SELECT * FROM (SELECT COUNT(*) AS _col_0 FROM u) AS x"},
		{"WITH t AS (SELECT a) SELECT * FROM t", "WITH t AS (SELECT a AS a) SELECT * FROM t"},
		// A literal names itself where it can, and needs no quoting when the
		// name it produces would be a name anyway.
		{"SELECT * FROM (SELECT 'abc') AS x", "SELECT * FROM (SELECT 'abc' AS abc) AS x"},
		{"SELECT * FROM (SELECT '') AS x", "SELECT * FROM (SELECT '' AS _col_0) AS x"},
	} {
		if got := generate(t, c.sql, "tsql"); got != c.want {
			t.Errorf("Generate(%q, tsql)\n  want %s\n  got  %s", c.sql, c.want, got)
		}
	}

	// Every other dialect leaves them alone.
	if got := generate(t, "SELECT * FROM (SELECT a) AS x", ""); got != "SELECT * FROM (SELECT a) AS x" {
		t.Errorf("the neutral dialect should not add names: %s", got)
	}
}

func TestGenerateQualifiedCall(t *testing.T) {
	// Two qualifiers deep: the inner Dot joins two names, the outer joins a
	// name to the call.
	got := generate(t, "SELECT * FROM a CROSS APPLY x.y.f(1)", "tsql")
	if want := "SELECT * FROM a CROSS APPLY x.y.f(1)"; got != want {
		t.Errorf("\n  want %s\n  got  %s", want, got)
	}
}

func TestGenerateUnknownDataType(t *testing.T) {
	tree := New("Cast",
		Arg{"this", New("Column", Arg{"this", New("Identifier", Arg{"this", "a"}, Arg{"quoted", false})})},
		Arg{"to", New("DataType", Arg{"this", DataTypeKind("NOT_A_TYPE")})})
	if _, err := Generate(tree, ""); err == nil {
		t.Error("a type the dialect has no spelling for should not be written")
	} else if !strings.Contains(err.Error(), "NOT_A_TYPE") {
		t.Errorf("the error should name the type: %v", err)
	}
}

func TestFunctionName(t *testing.T) {
	for _, c := range []struct {
		name, sql, dialect, want string
		isFunc                   bool
	}{
		{"anonymous keeps its spelling", "openrowset(1)", "", "openrowset", true},
		{"a named function reports its keyword", "COUNT(a)", "", "COUNT", true},
		{"the keyword can depend on a flag", "COUNT_BIG(a)", "tsql", "COUNT_BIG", true},
		{"a column is not a function", "a", "", "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			tree := parse(t, c.sql, c.dialect)
			got, isFunc := FunctionName(tree, c.dialect)
			if got != c.want || isFunc != c.isFunc {
				t.Errorf("FunctionName(%q) = %q, %v; want %q, %v", c.sql, got, isFunc, c.want, c.isFunc)
			}
		})
	}
	if _, ok := FunctionName(nil, ""); ok {
		t.Error("nil is not a function")
	}
	if _, ok := FunctionName(New("Count"), "oracle"); ok {
		t.Error("an unknown dialect has no function names")
	}
}

// TestTopIsParenthesisedUnlessItIsANumber covers T-SQL's spelling of a row
// limit, which the generator fuzzer has now caught twice.
//
// A count that is not a plain number needs parentheses or the output is not
// T-SQL. The port used to test for a Literal, which let a STRING count
// through: it wrote `TOP ”` and could not read it back.
func TestTopIsParenthesisedUnlessItIsANumber(t *testing.T) {
	for _, tc := range []struct{ sql, want string }{
		{"SELECT x FROM t LIMIT 5", "SELECT TOP 5 x FROM t"},
		{"SELECT x FROM t LIMIT 1.5", "SELECT TOP 1.5 x FROM t"},
		{"SELECT x FROM t LIMIT ''", "SELECT TOP ('') x FROM t"},
		{"SELECT x FROM t LIMIT n", "SELECT TOP (n) x FROM t"},
		// The reference writes `TOP -1` and cannot read it back. This port
		// parenthesises rather than emit SQL nobody can parse; see
		// docs/upstream-issues.md.
		{"SELECT x FROM t LIMIT -1", "SELECT TOP (-1) x FROM t"},
	} {
		e, err := ParseOne(tc.sql, "")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		got, err := Generate(e, "tsql")
		if err != nil {
			t.Fatalf("Generate(%q): %v", tc.sql, err)
		}
		if got != tc.want {
			t.Errorf("%q wrote %q, want %q", tc.sql, got, tc.want)
		}
		// Everything this writes, it reads.
		if _, err := ParseOne(got, "tsql"); err != nil {
			t.Errorf("%q wrote %q, which it cannot read back: %v", tc.sql, got, err)
		}
	}
}

// A JSON operator written with one operand is refused rather than written as
// half an expression. The node cannot be PARSED that way -- the reference
// refuses `JSONB_EXTRACT(a)` too -- so this is reachable only from a tree
// built by hand, which is what the guard above this port does when it rewrites
// one.
func TestJSONOperatorNeedsTwoOperands(t *testing.T) {
	half := New("JSONBExtract", Arg{"this", New("Column",
		Arg{"this", New("Identifier", Arg{"this", "a"}, Arg{"quoted", false})})})
	if got, err := Generate(half, "postgres"); err == nil {
		t.Errorf("wrote %q for a JSONBExtract with one operand", got)
	}
}

// TestBareNameAfterADollar covers a name the port declines to write BARE
// because a dollar has already gone into the output.
//
// `$0 A$` is an alias in PostgreSQL. Written back, the two dollars pair: the
// parameter swallows the AS and the statement reads as three tokens where
// there were four. The reference does the same thing with its own output and
// is saved only by the comment it carries, which this port does not model --
// so the port declines rather than emit it.
func TestBareNameAfterADollar(t *testing.T) {
	e, err := ParseOne("$0 A$-- ", "postgres")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if got, err := Generate(e, "postgres"); err == nil {
		t.Errorf("wrote %q; it does not read back", got)
	}

	// A braced parameter takes ANY token as its name, including one that is
	// only a keyword by case-folding: `aſ` upper-cases to AS, so the
	// tokenizer hands it back as the ALIAS keyword and a name-shaped test
	// there could not read `${aſ}` -- which is what the port itself writes.
	for _, sql := range []string{"$aſ", "${aſ}", "${WHERE}"} {
		e, err := ParseOne(sql, "databricks")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", sql, err)
		}
		out, err := Generate(e, "databricks")
		if err != nil {
			t.Fatalf("Generate(%q): %v", sql, err)
		}
		if _, err := ParseOne(out, "databricks"); err != nil {
			t.Errorf("%q wrote %q, which does not read back: %v", sql, out, err)
		}
	}

	// The name alone is fine, and stays fine: it is the PAIR that is not.
	for _, tc := range []struct{ sql, dialect string }{
		{"SELECT 1 AS a$b", "duckdb"},
		{"x$", "postgres"},
	} {
		e, err := ParseOne(tc.sql, tc.dialect)
		if err != nil {
			t.Fatalf("[%s] ParseOne(%q): %v", tc.dialect, tc.sql, err)
		}
		if got, err := Generate(e, tc.dialect); err != nil || got != tc.sql {
			t.Errorf("[%s] %q wrote %q, %v", tc.dialect, tc.sql, got, err)
		}
	}
}

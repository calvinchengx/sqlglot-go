package sqlglot

import (
	"errors"
	"testing"
)

// A builder that supplies a CONSTANT node -- one holding no argument at all --
// rather than a scalar or a wrapper around an argument.
func TestBuildFunctionConstantNode(t *testing.T) {
	for _, tc := range []struct {
		name, dialect, sql, want string
	}{
		{"sha384 keeps its length", "postgres", "SHA384(x)", "SHA384(x)"},
		{"sha256 keeps its own", "postgres", "SHA256(x)", "SHA256(x)"},
		// The reference writes the base back out rather than keeping the name.
		{"log10 fills the base", "duckdb", "LOG10(x)", "LOG(10, x)"},
		{"a default group is not written out", "duckdb",
			"REGEXP_EXTRACT_ALL(x, 'a')", "REGEXP_EXTRACT_ALL(x, 'a')"},
		{"a given group is", "duckdb",
			"REGEXP_EXTRACT_ALL(x, 'a', 1)", "REGEXP_EXTRACT_ALL(x, 'a', 1)"},
		{"a constant picks the name", "duckdb",
			"ARRAY_REVERSE_SORT(x)", "ARRAY_REVERSE_SORT(x)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, tc.dialect)
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			got, err := Generate(e, tc.dialect)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// pythonInt is the test the reference applies to decide whether a folded JSON
// path segment is a subscript or a key, so it has to accept exactly what
// Python's int() accepts -- and nothing else.
func TestPythonInt(t *testing.T) {
	for _, tc := range []struct {
		text string
		want int
		ok   bool
	}{
		{"0", 0, true},
		{"00", 0, true},
		{"-2", -2, true},
		{"+7", 7, true},
		{" 5", 5, true},
		{"5 ", 5, true},
		{"1_0", 10, true},
		{"٥", 5, true}, // an Arabic-Indic five is a decimal digit
		{"", 0, false},
		{"1a", 0, false},
		{"a", 0, false},
		{"-", 0, false},
		{"_1", 0, false},
		{"1_", 0, false},
		{"1__0", 0, false},
		{"1.5", 0, false},
		{"²", 0, false}, // a SUPERSCRIPT two is not: int() rejects it
	} {
		got, ok := pythonInt(tc.text)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("pythonInt(%q) = %d, %v; want %d, %v", tc.text, got, ok, tc.want, tc.ok)
		}
	}
}

// The names that turn their arguments into a JSON path, in each of the shapes
// the probe distinguishes.
func TestJSONPathFunctions(t *testing.T) {
	for _, tc := range []struct {
		name, dialect, sql, want string
	}{
		{"a path string is parsed", "duckdb",
			"SELECT JSON_EXTRACT(x, '$.a')", "SELECT x -> '$.a'"},
		{"keys fold into one path", "postgres",
			"SELECT JSON_EXTRACT_PATH(x, 'y')", "SELECT JSON_EXTRACT_PATH(x, 'y')"},
		{"a column path is carried through", "databricks",
			"SELECT GET_JSON_OBJECT(col, path_col)",
			"SELECT GET_JSON_OBJECT(col, path_col)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, tc.dialect)
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			got, err := Generate(e, tc.dialect)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// What these names REFUSE, which matters more than what they read: a tree the
// port cannot be sure of is not built at all.
func TestJSONPathFunctionRefusals(t *testing.T) {
	for _, tc := range []struct{ name, dialect, sql string }{
		// The path grammar rejects a dash, where Databricks' own reader takes
		// it. Building a Literal instead would be a different tree.
		{"a key the path grammar cannot read", "databricks",
			"SELECT GET_JSON_OBJECT(c, '$.x-y')"},
		// A fold needs every key to be a LITERAL; handed a non-literal the
		// reference lays the arguments out positionally instead, which the
		// port now builds. A literal that is not a STRING is the case still
		// refused: folding a number needs the subscript rules and folding it
		// wrongly would build a path the reference did not.
		{"a fold over a literal that is not a string", "postgres",
			"SELECT JSON_EXTRACT_PATH(x, 1)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseOne(tc.sql, tc.dialect); err == nil {
				t.Errorf("ParseOne(%q) was read; it should be refused", tc.sql)
			}
		})
	}
}

// A JSON extraction written as an OPERAND is parenthesised where the dialect
// spells it as an operator, and left alone where it spells it as a call.
func TestJSONExtractAsOperand(t *testing.T) {
	for _, tc := range []struct{ name, dialect, sql, want string }{
		{"an arrow is parenthesised", "duckdb", "SELECT a -> 'b' & c",
			"SELECT (a -> '$.b') & c"},
		{"including inside IN", "duckdb", "SELECT 1 WHERE a ->> 'b' IN ('c')",
			"SELECT 1 WHERE (a ->> '$.b') IN ('c')"},
		{"the left of an arrow is not", "duckdb", "SELECT a -> 'b' -> 'c'",
			"SELECT a -> '$.b' -> '$.c'"},
		{"but its right is", "duckdb", "SELECT a -> b * c",
			"SELECT a -> (b * c)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, tc.dialect)
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			got, err := Generate(e, tc.dialect)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// Shapes the parser now READS and the writer still declines. They are recorded
// because a refusal is the safe outcome and a silent wrong spelling is not:
// each is a generator gap, not a parser one, and naming them here keeps the
// two apart.
func TestJSONPathFunctionsReadButNotWritten(t *testing.T) {
	for _, tc := range []struct{ name, dialect, sql, why string }{
		{"a subscript inside a folded path", "postgres",
			"SELECT JSON_EXTRACT_PATH(x, 'y', '0', 'z')",
			"the per-part form spells keys, not indexes"},
		{"the whole document", "tsql", "SELECT JSON_QUERY(x)",
			"T-SQL writes an extraction as two calls around an ISNULL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, tc.dialect)
			if err != nil {
				t.Fatalf("ParseOne(%q): %v -- it used to READ this", tc.sql, err)
			}
			if got, err := Generate(e, tc.dialect); err == nil {
				t.Errorf("Generate wrote %q; %s, so it should refuse", got, tc.why)
			}
		})
	}
}

// Databricks spells a JSON extraction with a colon. The port WROTE that form
// while refusing to read a single one, so every extraction it emitted for that
// dialect was SQL it could not read back.
func TestVariantExtractColon(t *testing.T) {
	for _, tc := range []struct{ name, sql, want string }{
		{"one key", "SELECT c1:price", "SELECT c1:price"},
		{"a dotted key", "SELECT c1:price.foo", "SELECT c1:price.foo"},
		{"a subscript between keys", "SELECT c1:item[1].price", "SELECT c1:item[1].price"},
		{"a wildcard subscript", "SELECT c1:item[*].price", "SELECT c1:item[*].price"},
		// A key the source quoted stays quoted, whatever it looks like.
		{"a bracketed key", "SELECT raw:store['bicycle']", "SELECT raw:store[\"bicycle\"]"},
		// The colon form is bare SQL, so a quote in a key is NOT doubled --
		// unlike a path written inside a string.
		{"a quote in a key", "SELECT col:`fr'uit`", "SELECT col:[\"fr'uit\"]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, "databricks")
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			got, err := Generate(e, "databricks")
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
			if _, err := ParseOne(got, "databricks"); err != nil {
				t.Errorf("wrote %q and cannot read it back: %v", got, err)
			}
		})
	}
}

// A colon with no key after it is not an extraction, and a path that would
// write nothing is refused rather than emitted.
func TestVariantExtractEdges(t *testing.T) {
	if _, err := ParseOne("SELECT c1:", "databricks"); err == nil {
		t.Error("`c1:` was read as an extraction; the reference leaves the colon")
	}
	// `0 -> ''` has a path of nothing but a root, which Databricks spells as
	// the empty string: writing it gives `0:`, which the port cannot read.
	e, err := ParseOne("SELECT 0 -> ''", "databricks")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if got, err := Generate(e, "databricks"); err == nil {
		t.Errorf("wrote %q; a path that writes nothing should be refused", got)
	}
}

// JSON_OBJECT builds its arguments in PAIRS, and the two spellings of a pair
// are per dialect: DuckDB writes a comma where the others write a colon.
func TestJSONObject(t *testing.T) {
	for _, tc := range []struct{ name, dialect, sql, want string }{
		{"no pairs at all", "", "JSON_OBJECT()", "JSON_OBJECT()"},
		{"every column", "", "JSON_OBJECT(*)", "JSON_OBJECT(*)"},
		{"colon pairs", "", "JSON_OBJECT('k': 1, 'j': TRUE)",
			"JSON_OBJECT('k': 1, 'j': TRUE)"},
		// DuckDB separates a key from its value with the same comma that
		// separates the pairs, so the two forms read into one node.
		{"comma pairs", "duckdb", "JSON_OBJECT('key_1', 'one', 'key_2', NULL)",
			"JSON_OBJECT('key_1', 'one', 'key_2', NULL)"},
		{"a colon pair written with a comma", "duckdb", "JSON_OBJECT('k': 1)",
			"JSON_OBJECT('k', 1)"},
		{"null handling", "", "JSON_OBJECT('x': NULL, 'y': 1 NULL ON NULL)",
			"JSON_OBJECT('x': NULL, 'y': 1 NULL ON NULL)"},
		{"unique keys", "", "JSON_OBJECT('x': 1 WITH UNIQUE KEYS)",
			"JSON_OBJECT('x': 1 WITH UNIQUE KEYS)"},
		{"both modifiers", "", "JSON_OBJECT('x': NULL ABSENT ON NULL WITH UNIQUE KEYS)",
			"JSON_OBJECT('x': NULL ABSENT ON NULL WITH UNIQUE KEYS)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, tc.dialect)
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			got, err := Generate(e, tc.dialect)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// RETURNING is refused rather than guessed at: it builds the return type out
// of an Anonymous call or a FormatJson, shapes this port does not model, and a
// wrong tree behind a statement that reads fine is the one thing to avoid.
func TestJSONObjectRefusesReturning(t *testing.T) {
	for _, sql := range []string{
		"JSON_OBJECT('x': 1 RETURNING VARCHAR(100))",
		"JSON_OBJECT('x': 1 RETURNING VARBINARY FORMAT JSON ENCODING UTF8)",
	} {
		if _, err := ParseOne(sql, ""); err == nil {
			t.Errorf("ParseOne(%q) was read; RETURNING should be refused", sql)
		}
	}
}

// FETCH lands in the same slot as LIMIT, because it is one: the reference
// keeps a Fetch under `limit`. Everything after the count is a set of FLAGS on
// a LimitOptions, which is why this clause needed the flag work before it.
func TestFetch(t *testing.T) {
	for _, tc := range []struct{ name, sql, want string }{
		{"no count at all", "SELECT * FROM test FETCH FIRST ROWS ONLY",
			"SELECT * FROM test FETCH FIRST ROWS ONLY"},
		{"a count", "SELECT * FROM test FETCH FIRST 1 ROWS ONLY",
			"SELECT * FROM test FETCH FIRST 1 ROWS ONLY"},
		{"with ties", "SELECT * FROM test ORDER BY id DESC FETCH FIRST 10 ROWS WITH TIES",
			"SELECT * FROM test ORDER BY id DESC FETCH FIRST 10 ROWS WITH TIES"},
		{"a percentage", "SELECT * FROM test FETCH FIRST 10 PERCENT ROWS WITH TIES",
			"SELECT * FROM test FETCH FIRST 10 PERCENT ROWS WITH TIES"},
		{"NEXT rather than FIRST", "SELECT * FROM test FETCH NEXT 1 ROWS ONLY",
			"SELECT * FROM test FETCH NEXT 1 ROWS ONLY"},
		// The direction is optional and defaults to FIRST, and the singular
		// ROW is the plural ROWS: both normalise on the way out.
		{"neither direction nor plural", "SELECT * FROM x FETCH 1 ROW",
			"SELECT * FROM x FETCH FIRST 1 ROWS ONLY"},
		// A FETCH is written AFTER the offset where a LIMIT is written before
		// it, and both live in the same slot.
		{"after an offset", "SELECT * FROM x OFFSET 5 FETCH NEXT 1 ROWS ONLY",
			"SELECT * FROM x OFFSET 5 FETCH NEXT 1 ROWS ONLY"},
		{"a limit still comes first", "SELECT * FROM t LIMIT 1 OFFSET 2",
			"SELECT * FROM t LIMIT 1 OFFSET 2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, "")
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			got, err := Generate(e, "")
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// What these two clauses REFUSE. A refusal is the documented outcome, so the
// branches that produce one are worth holding still.
func TestJSONObjectAndFetchRefusals(t *testing.T) {
	for _, tc := range []struct{ name, dialect, sql string }{
		{"a key with no value", "", "JSON_OBJECT('k')"},
		{"a pair list that never closes", "", "JSON_OBJECT('k': 1"},
		{"a star that never closes", "", "JSON_OBJECT(*"},
		{"WITH UNIQUE without KEYS", "", "JSON_OBJECT('k': 1 WITH UNIQUE)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseOne(tc.sql, tc.dialect); err == nil {
				t.Errorf("ParseOne(%q) was read; it should be refused", tc.sql)
			}
		})
	}
}

// A JSON path key inside a variant subscript that is neither an index nor a
// string, and a colon with nothing usable after it.
func TestVariantPathRefusals(t *testing.T) {
	for _, sql := range []string{
		"SELECT c1:item[1.5]",
		"SELECT c1:item[",
		"SELECT c1:item[1",
	} {
		if _, err := ParseOne(sql, "databricks"); err == nil {
			t.Errorf("ParseOne(%q) was read; it should be refused", sql)
		}
	}
}

// TABLESAMPLE hangs off the TABLE, after its alias, and two of its facts are
// per dialect rather than per statement.
func TestTableSample(t *testing.T) {
	for _, tc := range []struct{ name, dialect, sql, want string }{
		{"a bucket", "", "SELECT a FROM test TABLESAMPLE (BUCKET 1 OUT OF 5)",
			"SELECT a FROM test TABLESAMPLE (BUCKET 1 OUT OF 5)"},
		{"a bucket on a field", "", "SELECT a FROM test TABLESAMPLE (BUCKET 1 OUT OF 5 ON x)",
			"SELECT a FROM test TABLESAMPLE (BUCKET 1 OUT OF 5 ON x)"},
		{"after an alias", "", "SELECT a FROM test AS x TABLESAMPLE (BUCKET 1 OUT OF 5)",
			"SELECT a FROM test AS x TABLESAMPLE (BUCKET 1 OUT OF 5)"},
		{"a percentage", "", "SELECT a FROM test TABLESAMPLE (0.1 PERCENT)",
			"SELECT a FROM test TABLESAMPLE (0.1 PERCENT)"},
		{"a row count", "", "SELECT a FROM test TABLESAMPLE (100 ROWS)",
			"SELECT a FROM test TABLESAMPLE (100 ROWS)"},
		// The percent SIGN is the modulo token, so the count has to be read
		// tighter than an expression or `20%` swallows the parenthesis.
		{"a percent sign", "duckdb", "SELECT * FROM tbl TABLESAMPLE RESERVOIR(20%)",
			"SELECT * FROM tbl TABLESAMPLE RESERVOIR (20 PERCENT)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, tc.dialect)
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			got, err := Generate(e, tc.dialect)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// A bare count means different things in different dialects, and DuckDB names
// a method whether or not the statement did. Both are probed off the
// reference; neither is written down anywhere that could be read.
func TestTableSampleDialectDefaults(t *testing.T) {
	for _, tc := range []struct{ dialect, sql, wantKey string }{
		{"postgres", "SELECT * FROM t TABLESAMPLE SYSTEM (50)", "percent"},
		{"duckdb", "SELECT * FROM t TABLESAMPLE (3)", "size"},
		{"tsql", "SELECT * FROM t TABLESAMPLE (3)", "size"},
	} {
		t.Run(tc.dialect+" "+tc.wantKey, func(t *testing.T) {
			e, err := ParseOne(tc.sql, tc.dialect)
			if err != nil {
				t.Fatalf("ParseOne: %v", err)
			}
			table, _ := e.Args["from_"].(*Expression)
			inner, _ := table.Args["this"].(*Expression)
			sample, _ := inner.Args["sample"].(*Expression)
			if sample == nil {
				t.Fatal("no sample on the table")
			}
			if sample.Args[tc.wantKey] == nil {
				t.Errorf("no %s; got keys %v", tc.wantKey, keysOf(sample))
			}
		})
	}
	// DuckDB records RESERVOIR even where the statement names no method.
	// Checked on the TREE: whether that shape can be written back depends on
	// whether the corpus taught the writer a template for it, which is a
	// separate question from whether the parser got it right.
	e, err := ParseOne("SELECT * FROM t TABLESAMPLE (3 ROWS)", "duckdb")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	table, _ := e.Args["from_"].(*Expression)
	inner, _ := table.Args["this"].(*Expression)
	sample, _ := inner.Args["sample"].(*Expression)
	method, _ := sample.Args["method"].(*Expression)
	if method == nil || method.Name() != "RESERVOIR" {
		t.Errorf("method = %v, want RESERVOIR supplied by the dialect", method)
	}
}

func keysOf(e *Expression) []string {
	var out []string
	for k, v := range e.Args {
		if v != nil {
			out = append(out, k)
		}
	}
	return out
}

// The refusal branches of TABLESAMPLE. Each one is a form the port declines to
// guess at rather than build something plausible from.
func TestTableSampleRefusals(t *testing.T) {
	for _, tc := range []struct{ name, sql string }{
		{"nothing after the word", "SELECT a FROM t TABLESAMPLE"},
		{"no parentheses", "SELECT a FROM t TABLESAMPLE SYSTEM 5"},
		{"a bucket without OUT OF", "SELECT a FROM t TABLESAMPLE (BUCKET 1 OF 5)"},
		{"a specification that never closes", "SELECT a FROM t TABLESAMPLE (100 ROWS"},
		{"REPEATABLE without a seed", "SELECT a FROM t TABLESAMPLE (100 ROWS) REPEATABLE 5"},
		{"a seed that never closes", "SELECT a FROM t TABLESAMPLE (100 ROWS) REPEATABLE (5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseOne(tc.sql, ""); err == nil {
				t.Errorf("ParseOne(%q) was read; it should be refused", tc.sql)
			}
		})
	}
}

// DuckDB names a thing in FRONT of it, in both places a name can go. The very
// same characters are a JSON extraction in Databricks, so the dialect decides
// which colon this is -- not the shape of what follows it.
func TestPrefixAlias(t *testing.T) {
	for _, tc := range []struct{ name, dialect, sql, want string }{
		{"a projection", "duckdb", "SELECT foo: 1", "SELECT 1 AS foo"},
		{"a quoted name", "duckdb", `SELECT "foo": 1`, `SELECT 1 AS "foo"`},
		{"several", "duckdb", "SELECT foo: 1, bar: 2", "SELECT 1 AS foo, 2 AS bar"},
		{"over an expression", "duckdb", "SELECT e: 1 + 2", "SELECT 1 + 2 AS e"},
		{"over a subquery", "duckdb", "SELECT s: (SELECT 42)", "SELECT (SELECT 42) AS s"},
		{"a relation", "duckdb", "SELECT * FROM foo: bar", "SELECT * FROM bar AS foo"},
		{"a qualified relation", "duckdb", "SELECT * FROM foo: c.db.tbl",
			"SELECT * FROM c.db.tbl AS foo"},
		// The same text in Databricks, where a colon extracts rather than names.
		{"not in Databricks", "databricks", "SELECT c1:price", "SELECT c1:price"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, tc.dialect)
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			got, err := Generate(e, tc.dialect)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
	// And nowhere else: a colon after a name is not an alias in PostgreSQL.
	if _, err := ParseOne("SELECT foo: 1", "postgres"); err == nil {
		t.Error("`SELECT foo: 1` was read in PostgreSQL, which has no such form")
	}
	// A relation cannot be named twice.
	if _, err := ParseOne("SELECT * FROM foo: bar AS baz", "duckdb"); err == nil {
		t.Error("`FROM foo: bar AS baz` was read; it names the relation twice")
	}
}

// PIVOT hangs off a table or a subquery as a LIST, and carries more than the
// statement says: four dialect conventions, and output COLUMNS that are
// derived rather than read.
func TestPivot(t *testing.T) {
	for _, tc := range []struct{ name, dialect, sql, want string }{
		{"an aggregate over values", "", "SELECT a FROM test PIVOT(SUM(x) FOR y IN ('z', 'q'))",
			"SELECT a FROM test PIVOT(SUM(x) FOR y IN ('z', 'q'))"},
		{"an unknown aggregate", "", "SELECT a FROM test PIVOT(SOMEAGG(x, y, z) FOR q IN (1))",
			"SELECT a FROM test PIVOT(SOMEAGG(x, y, z) FOR q IN (1))"},
		{"a chain", "", "SELECT a FROM test PIVOT(SUM(x) FOR y IN ('z')) PIVOT(MAX(b) FOR c IN ('d'))",
			"SELECT a FROM test PIVOT(SUM(x) FOR y IN ('z')) PIVOT(MAX(b) FOR c IN ('d'))"},
		{"over a subquery", "", "SELECT a FROM (SELECT a, b FROM test) PIVOT(SUM(x) FOR y IN ('z'))",
			"SELECT a FROM (SELECT a, b FROM test) PIVOT(SUM(x) FOR y IN ('z'))"},
		{"an unpivot, which takes the alias", "", "SELECT a FROM test UNPIVOT(x FOR y IN (z, q)) AS x",
			"SELECT a FROM test UNPIVOT(x FOR y IN (z, q)) AS x"},
		{"one of each", "", "SELECT a FROM test PIVOT(SUM(x) FOR y IN ('z')) UNPIVOT(x FOR y IN (z)) AS x",
			"SELECT a FROM test PIVOT(SUM(x) FOR y IN ('z')) UNPIVOT(x FOR y IN (z)) AS x"},
		// DuckDB tolerates a trailing comma before FOR; it normalises away.
		{"a trailing comma", "duckdb", "SELECT * FROM t PIVOT(FIRST(t) AS t, FOR quarter IN ('Q1'))",
			"SELECT * FROM t PIVOT(FIRST(t) AS t FOR quarter IN ('Q1'))"},
		// The NULLS clause brings a space before the parenthesis with it.
		{"including nulls", "databricks",
			"SELECT * FROM sales UNPIVOT INCLUDE NULLS (sales FOR quarter IN (q1))",
			"SELECT * FROM sales UNPIVOT INCLUDE NULLS (sales FOR quarter IN (q1))"},
		{"excluding them", "databricks",
			"SELECT * FROM sales UNPIVOT EXCLUDE NULLS (sales FOR quarter IN (q1))",
			"SELECT * FROM sales UNPIVOT EXCLUDE NULLS (sales FOR quarter IN (q1))"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, tc.dialect)
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			got, err := Generate(e, tc.dialect)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// The output columns are DERIVED: the product of the IN values with the
// aliases of whichever aggregates carry one, joined by underscores, and
// quoted when the result could not be written bare.
func TestPivotDerivedColumns(t *testing.T) {
	for _, tc := range []struct {
		name, dialect, sql string
		want               []string
	}{
		{"no alias, one column per value", "", "SELECT a FROM t PIVOT(SUM(x) FOR y IN ('a', 'b'))",
			[]string{"a", "b"}},
		{"an alias suffixes each", "", "SELECT a FROM t PIVOT(SUM(x) AS s FOR y IN ('a'))",
			[]string{"a_s"}},
		{"several aliases multiply", "", "SELECT a FROM t PIVOT(SUM(x) AS s, MAX(z) AS m FOR y IN ('a'))",
			[]string{"a_s", "a_m"}},
		{"no alias among several is still one", "", "SELECT a FROM t PIVOT(SUM(x), MAX(z) FOR y IN ('a'))",
			[]string{"a"}},
		// An explicit `<value> AS <alias>` names the column outright.
		{"a named value wins", "duckdb",
			`SELECT * FROM produce PIVOT(SUM(sales) FOR quarter IN ('Q1' AS "'Q1'"))`,
			[]string{"'Q1'"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, tc.dialect)
			if err != nil {
				t.Fatalf("ParseOne: %v", err)
			}
			from, _ := e.Args["from_"].(*Expression)
			table, _ := from.Args["this"].(*Expression)
			pivots, _ := table.Args["pivots"].([]*Expression)
			columns, _ := pivots[0].Args["columns"].([]*Expression)
			var got []string
			for _, c := range columns {
				got = append(got, c.Name())
			}
			if len(got) != len(tc.want) {
				t.Fatalf("columns = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("columns = %v, want %v", got, tc.want)
					break
				}
			}
		})
	}
}

// What PIVOT refuses. DuckDB names its columns after the AGGREGATES once there
// is more than one, rendering each back to SQL to do it -- a derivation over
// generated text, refused rather than approximated.
func TestPivotRefusals(t *testing.T) {
	for _, tc := range []struct{ name, dialect, sql string }{
		{"several aggregates DuckDB would name", "duckdb",
			"SELECT * FROM t PIVOT(SUM(x), MAX(z) FOR y IN ('a'))"},
		{"a list that is not parenthesised", "duckdb",
			"SELECT * FROM t PIVOT(SUM(y) FOR foo IN y_enum)"},
		{"a GROUP BY inside", "duckdb",
			"SELECT * FROM cities PIVOT(SUM(population) FOR year IN (2000) GROUP BY country)"},
		{"no FOR at all", "", "SELECT a FROM t PIVOT(SUM(x))"},
		{"no specification", "", "SELECT a FROM t PIVOT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseOne(tc.sql, tc.dialect); err == nil {
				t.Errorf("ParseOne(%q) was read; it should be refused", tc.sql)
			}
		})
	}
}

// The rest of PIVOT's refusals -- the malformed shapes, which matter because
// a half-read pivot would put a wrong tree behind a statement that looks fine.
func TestPivotMalformed(t *testing.T) {
	for _, tc := range []struct{ name, sql string }{
		{"an unclosed value list", "SELECT a FROM t PIVOT(SUM(x) FOR y IN ('a'"},
		{"an unclosed pivot", "SELECT a FROM t PIVOT(SUM(x) FOR y IN ('a')"},
		{"no IN", "SELECT a FROM t PIVOT(SUM(x) FOR y)"},
		{"nothing before FOR", "SELECT a FROM t PIVOT(FOR y IN ('a'))"},
		{"an alias with no name", "SELECT a FROM t PIVOT(SUM(x) AS FOR y IN ('a'))"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseOne(tc.sql, ""); err == nil {
				t.Errorf("ParseOne(%q) was read; it should be refused", tc.sql)
			}
		})
	}
}

// DuckDB has a PIVOT that is a QUERY rather than something hanging off a
// table. A different grammar and a different shape: no derived columns and
// none of the dialect conventions, because nothing is being named after
// anything.
func TestStatementPivot(t *testing.T) {
	for _, tc := range []struct{ name, sql, want string }{
		{"the plain form", "PIVOT Cities ON Year USING SUM(Population)",
			"PIVOT Cities ON Year USING SUM(Population)"},
		{"with a group", "PIVOT Cities ON Year USING SUM(Population) GROUP BY Country",
			"PIVOT Cities ON Year USING SUM(Population) GROUP BY Country"},
		{"values named on the ON", "PIVOT Cities ON Year IN (2000, 2010) USING SUM(Population)",
			"PIVOT Cities ON Year IN (2000, 2010) USING SUM(Population)"},
		{"several ON columns", "PIVOT Cities ON Country, Name USING SUM(Population)",
			"PIVOT Cities ON Country, Name USING SUM(Population)"},
		{"several aggregates", "PIVOT Cities ON Year USING SUM(Population) AS total, MAX(Population) AS m",
			"PIVOT Cities ON Year USING SUM(Population) AS total, MAX(Population) AS m"},
		// PIVOT_WIDER is the same node and normalises to PIVOT.
		{"the wider spelling", "PIVOT_WIDER Cities ON Year USING SUM(Population)",
			"PIVOT Cities ON Year USING SUM(Population)"},
		{"an unpivot with INTO", "UNPIVOT monthly_sales ON jan, feb INTO NAME month VALUE sales",
			"UNPIVOT monthly_sales ON jan, feb INTO NAME month VALUE sales"},
		{"an unpivot without", "UNPIVOT (SELECT 1 AS col1, 2 AS col2) ON foo, bar",
			"UNPIVOT (SELECT 1 AS col1, 2 AS col2) ON foo, bar"},
		// It reaches the parser by every route a query does.
		{"as a FROM item", "SELECT * FROM (PIVOT Cities ON Year USING SUM(Population)) AS a",
			"SELECT * FROM (PIVOT Cities ON Year USING SUM(Population)) AS a"},
		{"in a CTE", "WITH a AS (PIVOT Cities ON Year USING SUM(Population)) SELECT * FROM a",
			"WITH a AS (PIVOT Cities ON Year USING SUM(Population)) SELECT * FROM a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, "duckdb")
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			got, err := Generate(e, "duckdb")
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// The same statement carries `unpivot` false when it is a FROM item and leaves
// the argument off when it is not. That is the reference disagreeing with
// itself rather than a distinction that means anything -- but an argument
// present-and-false is a different tree from one absent, so it is followed.
func TestStatementPivotUnpivotArgument(t *testing.T) {
	pivotIn := func(sql string) *Expression {
		e, err := ParseOne(sql, "duckdb")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", sql, err)
		}
		var found *Expression
		var walk func(*Expression)
		walk = func(n *Expression) {
			if n == nil || found != nil {
				return
			}
			if n.Class == "Pivot" {
				found = n
				return
			}
			for _, v := range n.Args {
				switch c := v.(type) {
				case *Expression:
					walk(c)
				case []*Expression:
					for _, x := range c {
						walk(x)
					}
				}
			}
		}
		walk(e)
		if found == nil {
			t.Fatalf("no pivot in %q", sql)
		}
		return found
	}

	bare := pivotIn("PIVOT Cities ON Year USING SUM(Population)")
	if v, ok := bare.Args["unpivot"]; ok && v != nil {
		t.Errorf("standalone: unpivot = %v, want it absent", v)
	}
	item := pivotIn("SELECT * FROM (PIVOT Cities ON Year USING SUM(Population)) AS a")
	if v, _ := item.Args["unpivot"].(bool); v {
		t.Error("as a FROM item: unpivot = true, want false")
	}
	if _, ok := item.Args["unpivot"]; !ok {
		t.Error("as a FROM item: unpivot absent, want it present and false")
	}
}

// What the statement form refuses.
func TestStatementPivotRefusals(t *testing.T) {
	for _, tc := range []struct{ name, sql string }{
		{"no ON at all", "PIVOT Cities USING SUM(Population)"},
		{"a value list that never closes", "PIVOT Cities ON Year IN (2000 USING SUM(x)"},
		{"a list that is not parenthesised", "PIVOT Cities ON Year IN 2000 USING SUM(x)"},
		{"INTO NAME without VALUE", "UNPIVOT t ON a INTO NAME month"},
		{"a GROUP BY this does not model", "PIVOT Cities ON Year USING SUM(x) GROUP BY ALL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseOne(tc.sql, "duckdb"); err == nil {
				t.Errorf("ParseOne(%q) was read; it should be refused", tc.sql)
			}
		})
	}
}

// The remaining malformed statement-level shapes, and the one the writer
// declines: a pivot statement whose combination of arguments the corpus never
// taught a spelling for is refused rather than written half-formed.
func TestStatementPivotMalformed(t *testing.T) {
	for _, tc := range []struct{ name, sql string }{
		{"an ON list that never ends", "PIVOT Cities ON"},
		{"USING with nothing after it", "PIVOT Cities ON Year USING"},
		{"an unpivot naming no value column", "UNPIVOT t ON a INTO NAME m VALUE"},
		{"a source that is not a table", "PIVOT ON Year USING SUM(x)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseOne(tc.sql, "duckdb"); err == nil {
				t.Errorf("ParseOne(%q) was read; it should be refused", tc.sql)
			}
		})
	}
}

// A parenthesised list of two or more is a ROW, not a grouping: `(a, b)` is
// the Tuple that the left of an IN compares and that OVERLAPS takes. One item
// is a Paren, whatever it holds -- and either may be NAMED.
func TestTuple(t *testing.T) {
	for _, tc := range []struct{ name, dialect, sql, want string }{
		{"on the left of IN", "", "SELECT a FROM test WHERE (a, b) IN (SELECT 1, 2)",
			"SELECT a FROM test WHERE (a, b) IN (SELECT 1, 2)"},
		{"as a projection", "duckdb", "SELECT (x, x + 1, y) FROM t",
			"SELECT (x, x + 1, y) FROM t"},
		{"named members", "", "(x AS y, y AS z)", "(x AS y, y AS z)"},
		{"a named tuple", "", "((a, b) AS c)", "((a, b) AS c)"},
		// One item stays a Paren, named or not.
		{"one named item", "", "(x AS y)", "(x AS y)"},
		{"one item", "", "(a)", "(a)"},
		{"one expression", "", "(a + b)", "(a + b)"},
		// A quantifier over a row is SPACED where one over a Paren is not.
		{"under a quantifier", "databricks", "c LIKE ANY ('a', 'b') AND other_cond",
			"c LIKE ANY ('a', 'b') AND other_cond"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, tc.dialect)
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			got, err := Generate(e, tc.dialect)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
	// A row that never closes is refused rather than half-read.
	for _, sql := range []string{"(a, b", "(a, )", "(a, b AS)"} {
		if _, err := ParseOne(sql, ""); err == nil {
			t.Errorf("ParseOne(%q) was read; it should be refused", sql)
		}
	}
}

// A quantifier is spaced by what follows it: tight against an operand that
// brings its own parentheses, spaced from one that does not. Both spellings
// are probed off the reference rather than inferred from each other.
func TestQuantifierSpacing(t *testing.T) {
	for _, tc := range []struct{ name, dialect, sql, want string }{
		{"over a row", "databricks", "c LIKE ANY ('a', 'b')", "c LIKE ANY ('a', 'b')"},
		// A bare query is spaced too; only a Paren is written tight.
		{"over a subquery", "", "SELECT 1 WHERE a = ANY(SELECT 1)",
			"SELECT 1 WHERE a = ANY (SELECT 1)"},
		{"ALL over a row", "databricks", "c LIKE ALL ('a', 'b')", "c LIKE ALL ('a', 'b')"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, tc.dialect)
			if err != nil {
				t.Skipf("ParseOne(%q): %v", tc.sql, err)
			}
			got, err := Generate(e, tc.dialect)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// FOR SYSTEM_TIME asks a temporal table what it held at a moment or over a
// range. It hangs off the table, before the alias.
func TestSystemTime(t *testing.T) {
	for _, tc := range []struct{ name, sql, want string }{
		{"a moment", "SELECT [x] FROM [a].[b] FOR SYSTEM_TIME AS OF 'foo'",
			"SELECT [x] FROM [a].[b] FOR SYSTEM_TIME AS OF 'foo'"},
		{"and an alias after it", "SELECT [x] FROM [a].[b] FOR SYSTEM_TIME AS OF 'foo' AS alias",
			"SELECT [x] FROM [a].[b] FOR SYSTEM_TIME AS OF 'foo' AS alias"},
		// The bounds of a range are held as a Tuple, which would write
		// `(c, d)`; the dialect writes the pair with a word between them.
		{"a range", "SELECT [x] FROM [a].[b] FOR SYSTEM_TIME FROM c TO d",
			"SELECT [x] FROM [a].[b] FOR SYSTEM_TIME FROM c TO d"},
		{"another range", "SELECT [x] FROM [a].[b] FOR SYSTEM_TIME BETWEEN c AND d",
			"SELECT [x] FROM [a].[b] FOR SYSTEM_TIME BETWEEN c AND d"},
		{"a contained range, which keeps the tuple",
			"SELECT [x] FROM [a].[b] FOR SYSTEM_TIME CONTAINED IN (c, d)",
			"SELECT [x] FROM [a].[b] FOR SYSTEM_TIME CONTAINED IN (c, d)"},
		{"no bound at all", "SELECT [x] FROM [a].[b] FOR SYSTEM_TIME ALL AS alias",
			"SELECT [x] FROM [a].[b] FOR SYSTEM_TIME ALL AS alias"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, "tsql")
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			got, err := Generate(e, "tsql")
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
	for _, sql := range []string{
		"SELECT x FROM b FOR SYSTEM_TIME",
		"SELECT x FROM b FOR SYSTEM_TIME FROM c d",
		"SELECT x FROM b FOR SYSTEM_TIME BETWEEN c d",
		"SELECT x FROM b FOR SYSTEM_TIME CONTAINED IN c",
		"SELECT x FROM b FOR SYSTEM_TIME CONTAINED IN (c)",
		"SELECT x FROM b FOR SYSTEM_TIME CONTAINED IN (c, d",
	} {
		if _, err := ParseOne(sql, "tsql"); err == nil {
			t.Errorf("ParseOne(%q) was read; it should be refused", sql)
		}
	}
}

// FOR XML, FOR JSON and the bare FOR BROWSE, plus the row locking that shares
// the keyword.
func TestForClauseAndLocks(t *testing.T) {
	for _, tc := range []struct{ name, dialect, sql, want string }{
		{"a json kind", "tsql", "SELECT * FROM t FOR JSON AUTO",
			"SELECT * FROM t FOR JSON AUTO"},
		{"a bare kind", "tsql", "SELECT * FROM t FOR BROWSE", "SELECT * FROM t FOR BROWSE"},
		// A word the vocabulary has takes a second word with it; one it does
		// not falls through to a key/value option -- which is why PATH is a
		// plain Var under JSON and a key/value option under XML.
		{"paired words", "tsql", "SELECT * FROM t FOR XML PATH, BINARY BASE64, ELEMENTS XSINIL",
			"SELECT * FROM t FOR XML PATH, BINARY BASE64, ELEMENTS XSINIL"},
		{"an option with a value", "tsql",
			"SELECT * FROM t FOR JSON PATH, ROOT('Root'), INCLUDE_NULL_VALUES",
			"SELECT * FROM t FOR JSON PATH, ROOT('Root'), INCLUDE_NULL_VALUES"},
		{"inside a subquery", "tsql", "SELECT j FROM (SELECT a AS j FROM t FOR JSON PATH) AS x",
			"SELECT j FROM (SELECT a AS j FROM t FOR JSON PATH) AS x"},
		{"locking for update", "postgres", "SELECT a FROM tbl FOR UPDATE",
			"SELECT a FROM tbl FOR UPDATE"},
		{"locking for share", "postgres", "SELECT a FROM tbl FOR SHARE",
			"SELECT a FROM tbl FOR SHARE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, tc.dialect)
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			got, err := Generate(e, tc.dialect)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
	for _, sql := range []string{
		"SELECT * FROM t FOR JSON",
		"SELECT * FROM t FOR JSON PATH, ROOT('Root'",
	} {
		if _, err := ParseOne(sql, "tsql"); err == nil {
			t.Errorf("ParseOne(%q) was read; it should be refused", sql)
		}
	}
}

// A FOR option that is a LITERAL rather than a word, and a locking clause
// written twice -- both of which the reference reads and this follows.
func TestForClauseEdges(t *testing.T) {
	e, err := ParseOne("SELECT * FROM t FOR JSON 'x'", "tsql")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if got, err := Generate(e, "tsql"); err != nil || got != "SELECT * FROM t FOR JSON 'x'" {
		t.Errorf("got %q (%v), want the literal kept as the option", got, err)
	}
	twice, err := ParseOne("SELECT a FROM tbl FOR UPDATE FOR SHARE", "postgres")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if got, _ := Generate(twice, "postgres"); got != "SELECT a FROM tbl FOR UPDATE FOR SHARE" {
		t.Errorf("got %q, want both locks kept", got)
	}
}

// The malformed FOR shapes, held still because a half-read clause would put a
// wrong tree behind a statement that reads fine.
func TestForClauseMalformed(t *testing.T) {
	for _, tc := range []struct{ dialect, sql string }{
		{"tsql", "SELECT * FROM t FOR XML"},
		{"tsql", "SELECT * FROM t FOR XML ELEMENTS,"},
		{"tsql", "SELECT * FROM t FOR JSON PATH,"},
		{"tsql", "SELECT x FROM b FOR SYSTEM_TIME AS OF"},
		{"tsql", "SELECT x FROM b FOR SYSTEM_TIME FROM c TO"},
		{"tsql", "SELECT x FROM b FOR SYSTEM_TIME BETWEEN c AND"},
	} {
		if _, err := ParseOne(tc.sql, tc.dialect); err == nil {
			t.Errorf("ParseOne(%q) was read; it should be refused", tc.sql)
		}
	}
}

// LATERAL goes where a table goes, so a comma join and an explicit JOIN reach
// it by the same route -- and the join around it is still the join's to write.
func TestLateral(t *testing.T) {
	for _, tc := range []struct{ name, dialect, sql, want string }{
		{"after a comma", "postgres", "SELECT * FROM foo, LATERAL (SELECT * FROM bar) AS ss",
			"SELECT * FROM foo, LATERAL (SELECT * FROM bar) AS ss"},
		{"in a join, with named columns", "postgres",
			"SELECT t.x, y.z FROM x INNER JOIN LATERAL TVFTEST(t.x) AS y(z) ON TRUE",
			"SELECT t.x, y.z FROM x INNER JOIN LATERAL TVFTEST(t.x) AS y(z) ON TRUE"},
		{"over a qualified call", "postgres",
			"SELECT t.x, y.z FROM x LEFT JOIN LATERAL a.b.tvfTest(t.x) AS y(z) ON TRUE",
			"SELECT t.x, y.z FROM x LEFT JOIN LATERAL a.b.tvfTest(t.x) AS y(z) ON TRUE"},
		// In this position UNNEST is a RELATION, not the function the
		// expression grammar maps it to.
		{"over an UNNEST", "postgres", "SELECT * FROM r CROSS JOIN LATERAL UNNEST(ARRAY[1]) AS s(location)",
			"SELECT * FROM r CROSS JOIN LATERAL UNNEST(ARRAY[1]) AS s(location)"},
		{"over a subquery in a join", "postgres",
			"SELECT x.a, t.v FROM x INNER JOIN LATERAL (SELECT v, y FROM t) AS t(v, y) ON TRUE",
			"SELECT x.a, t.v FROM x INNER JOIN LATERAL (SELECT v, y FROM t) AS t(v, y) ON TRUE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, tc.dialect)
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			got, err := Generate(e, tc.dialect)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
	if _, err := ParseOne("SELECT * FROM x, LATERAL 1", "postgres"); err == nil {
		t.Error("`LATERAL 1` was read; it should be refused")
	}
}

// LATERAL VIEW is Hive's, and a CLAUSE of the query rather than a relation in
// the FROM list. Its alias names the columns `t AS a, b` where a table alias
// would have written `t(a, b)`.
func TestLateralView(t *testing.T) {
	for _, tc := range []struct{ name, dialect, sql, want string }{
		{"no alias at all", "", "SELECT s FROM tests LATERAL VIEW EXPLODE(scores)",
			"SELECT s FROM tests LATERAL VIEW EXPLODE(scores)"},
		{"columns but no name", "", "SELECT s FROM tests LATERAL VIEW EXPLODE(scores) AS score",
			"SELECT s FROM tests LATERAL VIEW EXPLODE(scores) AS score"},
		{"a name and a column", "", "SELECT s FROM tests LATERAL VIEW EXPLODE(scores) t AS score",
			"SELECT s FROM tests LATERAL VIEW EXPLODE(scores) t AS score"},
		{"several columns", "", "SELECT s FROM tests LATERAL VIEW EXPLODE(scores) t AS score, name",
			"SELECT s FROM tests LATERAL VIEW EXPLODE(scores) t AS score, name"},
		{"the outer form", "", "SELECT s FROM tests LATERAL VIEW OUTER EXPLODE(scores) t AS score, name",
			"SELECT s FROM tests LATERAL VIEW OUTER EXPLODE(scores) t AS score, name"},
		{"a name but no columns", "", "SELECT tf.* FROM (SELECT 0) AS t LATERAL VIEW STACK(1, 2) tf",
			"SELECT tf.* FROM (SELECT 0) AS t LATERAL VIEW STACK(1, 2) tf"},
		{"another dialect's", "databricks", "SELECT a FROM x LATERAL VIEW POSEXPLODE(y) t AS pos, a",
			"SELECT a FROM x LATERAL VIEW POSEXPLODE(y) t AS pos, a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, tc.dialect)
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			got, err := Generate(e, tc.dialect)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
	for _, sql := range []string{
		"SELECT s FROM tests LATERAL VIEW",
		"SELECT s FROM tests LATERAL VIEW EXPLODE(scores) t AS",
		"SELECT s FROM tests LATERAL VIEW EXPLODE(scores) t AS a,",
	} {
		if _, err := ParseOne(sql, ""); err == nil {
			t.Errorf("ParseOne(%q) was read; it should be refused", sql)
		}
	}
}

// The remaining LATERAL edges: a subquery that never closes, an alias the
// lateral takes rather than the relation inside it, and the ordinality
// argument, which the reference records only over a plain CALL.
func TestLateralEdges(t *testing.T) {
	for _, sql := range []string{
		"SELECT * FROM x, LATERAL (SELECT 1",
		"SELECT * FROM x, LATERAL UNNEST(",
	} {
		if _, err := ParseOne(sql, "postgres"); err == nil {
			t.Errorf("ParseOne(%q) was read; it should be refused", sql)
		}
	}

	lateralIn := func(sql string) *Expression {
		e, err := ParseOne(sql, "postgres")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", sql, err)
		}
		joins, _ := e.Args["joins"].([]*Expression)
		if len(joins) == 0 {
			t.Fatalf("no join in %q", sql)
		}
		lateral, _ := joins[0].Args["this"].(*Expression)
		return lateral
	}

	// Over a plain call the reference records the answer -- false -- where
	// over a subquery or an UNNEST it leaves the argument off.
	call := lateralIn("SELECT * FROM x, LATERAL f(y) AS z")
	if v, ok := call.Args["ordinality"].(bool); !ok || v {
		t.Errorf("over a call: ordinality = %v, want false and present", call.Args["ordinality"])
	}
	sub := lateralIn("SELECT * FROM x, LATERAL (SELECT 1) AS z")
	if v := sub.Args["ordinality"]; v != nil {
		t.Errorf("over a subquery: ordinality = %v, want it absent", v)
	}
	// The alias belongs to the LATERAL, not to the subquery inside it.
	if inner, _ := sub.Args["this"].(*Expression); inner != nil {
		if a := inner.Args["alias"]; a != nil {
			t.Errorf("the subquery kept the alias %v; it belongs to the lateral", a)
		}
	}
	if sub.Args["alias"] == nil {
		t.Error("the lateral has no alias; it should have taken the subquery's")
	}
}

// `JOIN b USING (x, y)` joins on the columns both sides share, kept as bare
// identifiers rather than columns.
func TestJoinUsing(t *testing.T) {
	for _, tc := range []struct{ sql, want string }{
		{"SELECT 1 FROM a JOIN b USING (x)", "SELECT 1 FROM a JOIN b USING (x)"},
		{"SELECT 1 FROM a JOIN b USING (x, y, z)", "SELECT 1 FROM a JOIN b USING (x, y, z)"},
	} {
		e, err := ParseOne(tc.sql, "")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		got, err := Generate(e, "")
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if got != tc.want {
			t.Errorf("got %q, want %q", got, tc.want)
		}
	}
	for _, sql := range []string{
		"SELECT 1 FROM a JOIN b USING",
		"SELECT 1 FROM a JOIN b USING (x",
		"SELECT 1 FROM a JOIN b USING ()",
	} {
		if _, err := ParseOne(sql, ""); err == nil {
			t.Errorf("ParseOne(%q) was read; it should be refused", sql)
		}
	}
}

// USING SAMPLE is DuckDB's other sampling spelling, hanging off the QUERY
// where TABLESAMPLE hangs off the table -- the same node under a different
// word, and both words are probed.
func TestUsingSample(t *testing.T) {
	for _, tc := range []struct{ name, sql, want string }{
		// The default method is not one value but two: a percentage is
		// SYSTEM where a row count is RESERVOIR.
		{"a percentage", "SELECT * FROM tbl USING SAMPLE 10%",
			"SELECT * FROM tbl USING SAMPLE SYSTEM (10 PERCENT)"},
		{"a named method after the size", "SELECT * FROM tbl USING SAMPLE 10 PERCENT (bernoulli)",
			"SELECT * FROM tbl USING SAMPLE BERNOULLI (10 PERCENT)"},
		{"a seed beside the method", "SELECT * FROM tbl USING SAMPLE 10% (system, 377)",
			"SELECT * FROM tbl USING SAMPLE SYSTEM (10 PERCENT) REPEATABLE (377)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, "duckdb")
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			got, err := Generate(e, "duckdb")
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
	// A row count reads, and the writer declines: DuckDB forces RESERVOIR
	// there whatever the node says, so the shape has no template of its own.
	e, err := ParseOne("SELECT * FROM tbl USING SAMPLE 5", "duckdb")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if got, err := Generate(e, "duckdb"); err == nil {
		t.Errorf("wrote %q; the row-count shape has no spelling recorded", got)
	}
	for _, sql := range []string{
		"SELECT * FROM tbl USING SAMPLE",
		"SELECT * FROM tbl USING SAMPLE reservoir 5",
		"SELECT * FROM tbl USING SAMPLE 10% (",
		"SELECT * FROM tbl USING SAMPLE 5 REPEATABLE 3",
	} {
		if _, err := ParseOne(sql, "duckdb"); err == nil {
			t.Errorf("ParseOne(%q) was read; it should be refused", sql)
		}
	}
}

// The rest of USING SAMPLE's shapes and refusals.
func TestUsingSampleShapes(t *testing.T) {
	e, err := ParseOne("SELECT * FROM tbl USING SAMPLE reservoir(50 ROWS) REPEATABLE (100)", "duckdb")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	sample, _ := e.Args["sample"].(*Expression)
	if sample == nil {
		t.Fatal("no sample on the query")
	}
	if seed, _ := sample.Args["seed"].(*Expression); seed == nil || seed.Name() != "100" {
		t.Errorf("seed = %v, want 100", sample.Args["seed"])
	}
	// The parentheses without a seed still RECORD one, as false.
	bare, err := ParseOne("SELECT * FROM tbl USING SAMPLE 10 PERCENT (bernoulli)", "duckdb")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	inner, _ := bare.Args["sample"].(*Expression)
	if v, ok := inner.Args["seed"].(bool); !ok || v {
		t.Errorf("seed = %v, want false and present", inner.Args["seed"])
	}
	for _, sql := range []string{
		"SELECT * FROM tbl USING SAMPLE reservoir 5)",
		"SELECT * FROM tbl USING SAMPLE reservoir(50 ROWS",
		"SELECT * FROM tbl USING SAMPLE 5 REPEATABLE (3",
	} {
		if _, err := ParseOne(sql, "duckdb"); err == nil {
			t.Errorf("ParseOne(%q) was read; it should be refused", sql)
		}
	}
}

// RECURSIVE changes what a CTE means -- it may refer to itself -- and the
// reference records it as a flag on the WITH rather than on each CTE.
func TestWithRecursive(t *testing.T) {
	for _, tc := range []struct{ sql, want string }{
		{"WITH RECURSIVE t AS (SELECT 1 AS n) SELECT SUM(n) FROM t",
			"WITH RECURSIVE t AS (SELECT 1 AS n) SELECT SUM(n) FROM t"},
		{"WITH RECURSIVE t AS (SELECT 1 AS n UNION ALL SELECT n + 1 AS n FROM t WHERE n < 4) SELECT * FROM t",
			"WITH RECURSIVE t AS (SELECT 1 AS n UNION ALL SELECT n + 1 AS n FROM t WHERE n < 4) SELECT * FROM t"},
		{"WITH RECURSIVE t1 AS (SELECT 1 AS n), t2 AS (SELECT 2 AS n) SELECT 1",
			"WITH RECURSIVE t1 AS (SELECT 1 AS n), t2 AS (SELECT 2 AS n) SELECT 1"},
		// And the flag is left OFF where the word is absent, which is a
		// different tree from one carrying false.
		{"WITH t AS (SELECT 1 AS n) SELECT * FROM t",
			"WITH t AS (SELECT 1 AS n) SELECT * FROM t"},
	} {
		e, err := ParseOne(tc.sql, "postgres")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		got, err := Generate(e, "postgres")
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if got != tc.want {
			t.Errorf("got %q, want %q", got, tc.want)
		}
	}
	plain, err := ParseOne("WITH t AS (SELECT 1) SELECT * FROM t", "postgres")
	if err != nil {
		t.Fatal(err)
	}
	with, _ := plain.Args["with_"].(*Expression)
	if with.Args["recursive"] != nil {
		t.Errorf("recursive = %v on a plain WITH, want it absent", with.Args["recursive"])
	}
}

// CUBE, ROLLUP and GROUPING SETS look like calls but land on their OWN args of
// Group rather than in its expression list, and any of them may sit beside
// plain columns. The reference writes them in a fixed order whatever order
// they were written in.
func TestGroupings(t *testing.T) {
	for _, tc := range []struct{ name, sql, want string }{
		{"grouping sets", "SELECT a FROM t GROUP BY GROUPING SETS ((a), (b))",
			"SELECT a FROM t GROUP BY GROUPING SETS ((a), (b))"},
		{"a cube", "SELECT a FROM t GROUP BY CUBE(a, b)", "SELECT a FROM t GROUP BY CUBE (a, b)"},
		{"a rollup", "SELECT a FROM t GROUP BY ROLLUP(a)", "SELECT a FROM t GROUP BY ROLLUP (a)"},
		{"beside a column", "SELECT a FROM t GROUP BY a, ROLLUP(b)",
			"SELECT a FROM t GROUP BY a, ROLLUP (b)"},
		// Written first, but the columns still come first on the way out.
		{"before a column", "SELECT a FROM t GROUP BY ROLLUP(a), b",
			"SELECT a FROM t GROUP BY b, ROLLUP (a)"},
		{"all three", "SELECT a FROM t GROUP BY CUBE(a), ROLLUP(b), GROUPING SETS ((c))",
			"SELECT a FROM t GROUP BY GROUPING SETS ((c)), CUBE (a), ROLLUP (b)"},
		// `()` is the grouping that groups everything, and is a row of nothing.
		{"an empty grouping", "SELECT a FROM t GROUP BY GROUPING SETS (a, (b, c), ())",
			"SELECT a FROM t GROUP BY GROUPING SETS (a, (b, c), ())"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, "")
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			got, err := Generate(e, "")
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
	for _, sql := range []string{
		"SELECT a FROM t GROUP BY ROLLUP",
		"SELECT a FROM t GROUP BY CUBE(a",
		"SELECT a FROM t GROUP BY GROUPING SETS a",
	} {
		if _, err := ParseOne(sql, ""); err == nil {
			t.Errorf("ParseOne(%q) was read; it should be refused", sql)
		}
	}
}

// The grouping writers refuse what they cannot name, and a GROUP BY with
// nothing left to group by is refused rather than written bare.
func TestGroupingWriterEdges(t *testing.T) {
	if got, err := Generate(New("Group"), ""); err == nil {
		t.Errorf("wrote %q for a GROUP BY with nothing in it", got)
	}
	if got, err := Generate(New("GroupingSets"), ""); err != nil || got != "GROUPING SETS ()" {
		t.Errorf("got %q (%v), want the empty grouping written", got, err)
	}
	// A class this writer does not name is refused rather than guessed at:
	// it spells three groupings and nothing else.
	if got, err := Generate(New("Rollup"), ""); err != nil || got != "ROLLUP ()" {
		t.Errorf("got %q (%v), want ROLLUP ()", got, err)
	}
	if got, err := Generate(New("Cube", Arg{"expressions", []*Expression{}}), ""); err != nil || got != "CUBE ()" {
		t.Errorf("got %q (%v), want CUBE ()", got, err)
	}
	// An empty grouping list still closes its parentheses.
	e, err := ParseOne("SELECT a FROM t GROUP BY GROUPING SETS ()", "")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if got, _ := Generate(e, ""); got != "SELECT a FROM t GROUP BY GROUPING SETS ()" {
		t.Errorf("got %q", got)
	}
}

// `STRING_AGG(x, ',') WITHIN GROUP (ORDER BY y)` FOLDS: the builder swallows
// the clause rather than being wrapped by it, and the ordering takes the
// argument that was already there as its own.
func TestWithinGroupFold(t *testing.T) {
	e, err := ParseOne("SELECT STRING_AGG(x, ',') WITHIN GROUP (ORDER BY y DESC)", "")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	got, err := Generate(e, "")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if want := "SELECT GROUP_CONCAT(x ORDER BY y DESC, ',')"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// The same node unfolds again where the dialect spells it that way.
	tsql, err := ParseOne("STRING_AGG(x, ',') WITHIN GROUP (ORDER BY y DESC)", "tsql")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if got, _ := Generate(tsql, "tsql"); got != "STRING_AGG(x, ',') WITHIN GROUP (ORDER BY y DESC)" {
		t.Errorf("got %q, want it written back unfolded", got)
	}
	// It is the NAME that folds, not the class: Databricks reads LISTAGG into
	// the very same class and leaves the clause wrapping it.
	listagg, err := ParseOne("LISTAGG(x, z) WITHIN GROUP (ORDER BY y)", "databricks")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if listagg.Class != "WithinGroup" {
		t.Errorf("LISTAGG folded into %s; Databricks does not fold it", listagg.Class)
	}
	for _, sql := range []string{
		"SELECT STRING_AGG(x, ',') WITHIN GROUP (y)",
		"SELECT STRING_AGG(x, ',') WITHIN GROUP ORDER BY y",
		"SELECT STRING_AGG(x, ',') WITHIN GROUP (ORDER BY y",
	} {
		if _, err := ParseOne(sql, ""); err == nil {
			t.Errorf("ParseOne(%q) was read; it should be refused", sql)
		}
	}
}

// Where a dialect writes the folded ordering somewhere this port cannot spell
// -- DuckDB and PostgreSQL attach it to the SEPARATOR -- the node is refused
// rather than written in the wrong place.
func TestGroupConcatOrderPerDialect(t *testing.T) {
	e, err := ParseOne("SELECT STRING_AGG(x, ',') WITHIN GROUP (ORDER BY y DESC)", "")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	// Three arrangements, one per dialect: the ordering inside the first
	// argument, unfolded into a WITHIN GROUP again, or after the separator
	// and still inside the call. Which one is probed.
	for dialect, want := range map[string]string{
		"duckdb":   "LISTAGG(x, ',' ORDER BY y DESC)",
		"postgres": "STRING_AGG(x, ',' ORDER BY y DESC NULLS LAST)",
		"tsql":     "STRING_AGG(x, ',') WITHIN GROUP (ORDER BY y DESC)",
		"":         "GROUP_CONCAT(x ORDER BY y DESC, ',')",
	} {
		got, err := Generate(e, dialect)
		if err != nil {
			t.Errorf("[%s] refused: %v", dialect, err)
			continue
		}
		if got != "SELECT "+want {
			t.Errorf("[%s] wrote %q, want %q", dialect, got, "SELECT "+want)
		}
	}
	// And a GroupConcat with nothing folded in writes normally everywhere.
	plain, err := ParseOne("SELECT STRING_AGG(x, ',')", "")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	for _, dialect := range []string{"", "duckdb", "tsql"} {
		if _, err := Generate(plain, dialect); err != nil {
			t.Errorf("%s refused a plain GROUP_CONCAT: %v", dialect, err)
		}
	}
}

// `*` takes modifiers: EXCEPT drops columns, REPLACE swaps them. Both are
// lists on the Star itself, so `SELECT * EXCEPT (a)` is one projection --
// which is why reading EXCEPT as the SET OPERATION refused the whole
// statement for having no SELECT after it.
func TestStarModifiers(t *testing.T) {
	for _, tc := range []struct{ name, sql, want string }{
		{"except", "SELECT * EXCEPT (a, b)", "SELECT * EXCEPT (a, b)"},
		{"and a from", "SELECT * EXCEPT (a, b) FROM y", "SELECT * EXCEPT (a, b) FROM y"},
		{"and replace", "SELECT * EXCEPT (a, b) REPLACE (a AS b, b AS C)",
			"SELECT * EXCEPT (a, b) REPLACE (a AS b, b AS C)"},
		{"on a qualified star", "SELECT a.* EXCEPT (a, b), b.* REPLACE (a AS b, b AS C)",
			"SELECT a.* EXCEPT (a, b), b.* REPLACE (a AS b, b AS C)"},
		{"a qualified name inside", "SELECT A.* EXCEPT (A.COL_1) FROM TABLE_1 AS A",
			"SELECT A.* EXCEPT (A.COL_1) FROM TABLE_1 AS A"},
		// EXCEPT with no list after it is still the set operation.
		{"the set operation is untouched", "SELECT 1 EXCEPT SELECT 2", "SELECT 1 EXCEPT SELECT 2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, "")
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			got, err := Generate(e, "")
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
	if _, err := ParseOne("SELECT * EXCEPT (a", ""); err == nil {
		t.Error("an unclosed star modifier was read; it should be refused")
	}
}

// `MAP {k: v}` is DuckDB's map LITERAL, not a call. Its keys are expressions
// and stay as they were written, where the keys of a bare `{...}` struct
// literal become identifiers.
func TestMapLiteral(t *testing.T) {
	for _, tc := range []struct{ name, sql, want string }{
		{"a string key", "SELECT MAP {'x': 1}", "SELECT MAP {'x': 1}"},
		// A numeric key stays numeric; quoting it would be a different map.
		{"a numeric key", "MAP {1: 'a', 2: 'b'}", "MAP {1: 'a', 2: 'b'}"},
		{"a quoted numeric key", "MAP {'1': 'a', '2': 'b'}", "MAP {'1': 'a', '2': 'b'}"},
		{"an array key", "MAP {[1, 2]: 'a', [3, 4]: 'b'}", "MAP {[1, 2]: 'a', [3, 4]: 'b'}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, "duckdb")
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			got, err := Generate(e, "duckdb")
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
	for _, sql := range []string{"MAP {'x' 1}", "MAP {'x': 1", "MAP {'x': }"} {
		if _, err := ParseOne(sql, "duckdb"); err == nil {
			t.Errorf("ParseOne(%q) was read; it should be refused", sql)
		}
	}
	// The brace form is DuckDB's. Elsewhere MAP is an ordinary name and the
	// braces are something else, so reading it everywhere wrote SQL
	// PostgreSQL could not parse -- which the generator fuzzer found.
	if _, err := ParseOne("MAP {'x': 1}", "postgres"); err == nil {
		t.Error("`MAP {...}` was read in PostgreSQL, which has no such form")
	}
	// And `MAP(...)` with parentheses is still the ordinary call.
	if _, err := ParseOne("SELECT MAP([1], [2])", "duckdb"); err != nil {
		t.Errorf("the parenthesised MAP call was refused: %v", err)
	}
}

// The writers refuse what they cannot spell rather than writing part of it.
func TestStarAndMapWriterEdges(t *testing.T) {
	// A map over something that is not a struct of pairs.
	if got, err := Generate(New("ToMap", Arg{"this", New("Column")}), "duckdb"); err == nil {
		t.Errorf("wrote %q for a map over a column", got)
	}
	bad := New("ToMap", Arg{"this", New("Struct",
		Arg{"expressions", []*Expression{New("Column")}})})
	if got, err := Generate(bad, "duckdb"); err == nil {
		t.Errorf("wrote %q for a map entry that is not a pair", got)
	}
	// A star with a RENAME, which the reference also carries.
	star := New("Star", Arg{"rename", []*Expression{New("Column",
		Arg{"this", New("Identifier", Arg{"this", "a"}, Arg{"quoted", false})})}})
	if got, err := Generate(star, ""); err != nil || got != "* RENAME (a)" {
		t.Errorf("got %q (%v), want `* RENAME (a)`", got, err)
	}
	// A REPLACE with no list after it is not a modifier, and this port
	// refuses the statement. The reference reads it as `SELECT *`, silently
	// dropping the word -- narrower here is the safe direction, and no corpus
	// statement has the shape.
	if _, err := ParseOne("SELECT * REPLACE", ""); err == nil {
		t.Error("`SELECT * REPLACE` was read; this port refuses it")
	}
}

// `f(name := value)` is a NAMED ARGUMENT, and the reference records it as the
// same PropertyEQ a struct field uses. Which spelling comes back out depends
// on the PARENT: a field of a struct is `'k': v`, an argument of a call is
// `k := v`.
func TestNamedArguments(t *testing.T) {
	for _, tc := range []struct{ name, sql, want string }{
		{"a struct built from named arguments", "struct_pack(a := 1, b := 2)", "{'a': 1, 'b': 2}"},
		{"a quoted name", `STRUCT_PACK("a b" := 1)`, "{'a b': 1}"},
		{"an argument of a known call", "SELECT UNNEST(col, recursive := TRUE) FROM t",
			"SELECT UNNEST(col, recursive := TRUE) FROM t"},
		{"an argument of an unknown call", "SELECT STAR(tbl, exclude := [foo])",
			"SELECT STAR(tbl, exclude := [foo])"},
		// A struct nested inside a call keeps the FIELD spelling: the two are
		// decided by the immediate parent, not by how deep the call is.
		{"a struct inside a call", "SELECT CARDINALITY(CAST({'a': 1} AS MAP(TEXT, INT)))",
			"SELECT CARDINALITY(CAST({'a': 1} AS MAP(TEXT, INT)))"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, "duckdb")
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			got, err := Generate(e, "duckdb")
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
	// Outside an argument list `:=` is an assignment, which is a statement.
	if _, err := ParseOne("SELECT 1 FROM t WHERE a := 1", "duckdb"); err == nil {
		t.Error("`a := 1` was read outside a call; it should be refused")
	}
	// And a name that is not a name.
	if _, err := ParseOne("f(1 := 2)", "duckdb"); err == nil {
		t.Error("`1 := 2` was read; the name is not a name")
	}
}

// What can stand before a `:=` in an argument list.
func TestNamedArgumentNames(t *testing.T) {
	for _, tc := range []struct {
		sql  string
		read bool
	}{
		{"f(a := 1)", true},
		{`f("a b" := 1)`, true},
		{"f(1 := 2)", false},
		{"f('a' := 2)", false},
		{"f(a.b := 2)", true},
		{"f(a := )", false},
		{"f(a.b.c := 2)", true},
		{"f(x[1] := 2)", false},
	} {
		_, err := ParseOne(tc.sql, "duckdb")
		if tc.read && err != nil {
			t.Errorf("ParseOne(%q) was refused: %v", tc.sql, err)
		}
		if !tc.read && err == nil {
			t.Errorf("ParseOne(%q) was read; it should be refused", tc.sql)
		}
	}
}

// `a.b.C()` is a CALL under a chain of dots, not a column, and the chain may
// be any depth.
func TestQualifiedCall(t *testing.T) {
	for _, tc := range []struct{ sql, want string }{
		{"a.b.C()", "a.b.C()"},
		{"a.B()", "a.B()"},
		{"a.b.INT(1.234)", "a.b.INT(1.234)"},
		{"a.b.c.d.e.f.G()", "a.b.c.d.e.f.G()"},
		// And a plain qualified column is untouched.
		{"a.b.c", "a.b.c"},
	} {
		e, err := ParseOne(tc.sql, "")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		got, err := Generate(e, "")
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if got != tc.want {
			t.Errorf("got %q, want %q", got, tc.want)
		}
	}
	// A name AFTER a dot is not the builtin it spells: `x.EXTRACT(1)` is a
	// call to a function called EXTRACT in some schema, and needs none of the
	// grammar the bare EXTRACT does.
	e, err := ParseOne("x.EXTRACT(1)", "")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if got, err := Generate(e, ""); err != nil || got != "x.EXTRACT(1)" {
		t.Errorf("got %q (%v), want x.EXTRACT(1)", got, err)
	}
	// The same for a name whose bare form builds a node of its own: resolving
	// it here wrote `a.CASE WHEN 1 THEN 0 END`, which is not SQL at all.
	iff, err := ParseOne("SELECT a.IF(1, 0)", "duckdb")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if got, err := Generate(iff, "duckdb"); err != nil || got != "SELECT a.IF(1, 0)" {
		t.Errorf("got %q (%v), want the call written back", got, err)
	}
	// A quoted name after a dot keeps its quoting.
	q, err := ParseOne(`a."My Func"(1)`, "")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if got, err := Generate(q, ""); err != nil || got != `a."My Func"(1)` {
		t.Errorf("got %q (%v), want the quoted name kept", got, err)
	}
	for _, sql := range []string{"a.b.C(", "a.b.C(1,", "a.b.C(1"} {
		if _, err := ParseOne(sql, ""); err == nil {
			t.Errorf("ParseOne(%q) was read; it should be refused", sql)
		}
	}
}

// COLLATE names a collation, not an expression: three shapes for one slot,
// and the generic operand rule made a column of two of them.
func TestCollate(t *testing.T) {
	for _, tc := range []struct{ name, dialect, sql, want string }{
		{"a string", "", "SELECT a FROM x WHERE a COLLATE 'utf8_general_ci' = 'b'",
			"SELECT a FROM x WHERE a COLLATE 'utf8_general_ci' = 'b'"},
		{"a bare word", "databricks", "SELECT substring_index('5' COLLATE UTF8_BINARY, 'a', 2)",
			"SELECT SUBSTRING_INDEX('5' COLLATE UTF8_BINARY, 'a', 2)"},
		{"two of them", "databricks", "SELECT substring_index('A' COLLATE UTF8_LCASE, 'A' COLLATE UTF8_LCASE, 2)",
			"SELECT SUBSTRING_INDEX('A' COLLATE UTF8_LCASE, 'A' COLLATE UTF8_LCASE, 2)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, tc.dialect)
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			got, err := Generate(e, tc.dialect)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
	if _, err := ParseOne("SELECT a COLLATE FROM t", ""); err == nil {
		t.Error("`COLLATE` with no collation was read; it should be refused")
	}
	if _, err := ParseOne("SELECT a COLLATE 1", ""); err == nil {
		t.Error("`COLLATE 1` was read; a number is not a collation")
	}
	// A quoted name is an Identifier where a bare one is a Var.
	quoted, err := ParseOne(`SELECT a COLLATE "de_DE"`, "postgres")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	only, _ := quoted.Args["expressions"].([]*Expression)
	collate := only[0]
	name, _ := collate.Args["expression"].(*Expression)
	if name == nil || name.Class != "Identifier" {
		t.Errorf("a quoted collation is %v, want an Identifier", name)
	}
	if _, err := ParseOne("SELECT a COLLATE", ""); err == nil {
		t.Error("`COLLATE` at the end was read; it should be refused")
	}
}

// A named window is the same node an OVER builds, with the NAME where the call
// would be and no OVER -- so the body is read and written by the same code,
// and `over` is what tells the two apart.
func TestNamedWindowAndQualify(t *testing.T) {
	for _, tc := range []struct{ name, dialect, sql, want string }{
		{"one window", "", "SELECT a FROM t WINDOW w AS (PARTITION BY b)",
			"SELECT a FROM t WINDOW w AS (PARTITION BY b)"},
		{"two of them", "", "SELECT a FROM t WINDOW w AS (PARTITION BY b ORDER BY c), v AS (ORDER BY d)",
			"SELECT a FROM t WINDOW w AS (PARTITION BY b ORDER BY c), v AS (ORDER BY d)"},
		{"referred to by an OVER", "", "SELECT SUM(x) OVER w FROM t WINDOW w AS (PARTITION BY b)",
			"SELECT SUM(x) OVER w FROM t WINDOW w AS (PARTITION BY b)"},
		{"qualify", "duckdb", "SELECT a FROM t QUALIFY ROW_NUMBER() OVER (PARTITION BY b) = 1",
			"SELECT a FROM t QUALIFY ROW_NUMBER() OVER (PARTITION BY b) = 1"},
		// The WINDOW clause comes BEFORE the QUALIFY that refers to its names.
		{"both, in order", "duckdb",
			"SELECT a FROM t WINDOW w AS (PARTITION BY b) QUALIFY ROW_NUMBER() OVER w < 3",
			"SELECT a FROM t WINDOW w AS (PARTITION BY b) QUALIFY ROW_NUMBER() OVER w < 3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, tc.dialect)
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			got, err := Generate(e, tc.dialect)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
	for _, sql := range []string{
		"SELECT a FROM t WINDOW w (PARTITION BY b)",
		"SELECT a FROM t WINDOW w AS b",
		"SELECT a FROM t WINDOW",
		"SELECT a FROM t QUALIFY",
	} {
		if _, err := ParseOne(sql, ""); err == nil {
			t.Errorf("ParseOne(%q) was read; it should be refused", sql)
		}
	}
}

// CREATE TABLE, in the two shapes the corpus is mostly made of. The kind is a
// WORD on the node and the name is wrapped in a Schema when there are columns,
// so the shape of `this` is what says which spelling this is.
func TestCreateTable(t *testing.T) {
	for _, tc := range []struct{ name, dialect, sql, want string }{
		{"columns", "", "CREATE TABLE t (a INT, b TEXT)", "CREATE TABLE t (a INT, b TEXT)"},
		{"a query", "", "CREATE TABLE t AS SELECT 1", "CREATE TABLE t AS SELECT 1"},
		{"or replace", "", "CREATE OR REPLACE TABLE t (a INT)", "CREATE OR REPLACE TABLE t (a INT)"},
		{"if not exists", "", "CREATE TABLE IF NOT EXISTS t (a INT)", "CREATE TABLE IF NOT EXISTS t (a INT)"},
		{"a qualified name", "", "CREATE TABLE db.t (a INT)", "CREATE TABLE db.t (a INT)"},
		{"a sized type", "", "CREATE TABLE t (c VARCHAR(100), d DECIMAL(5, 3))",
			"CREATE TABLE t (c VARCHAR(100), d DECIMAL(5, 3))"},
		// A column of a table is `a STRING`; a FIELD of a struct is `a: STRING`
		// in Databricks, and the struct inside a column keeps its own.
		{"a struct column", "databricks", "CREATE TABLE t (a STRUCT<c: MAP<STRING, STRING>>)",
			"CREATE TABLE t (a STRUCT<c: MAP<STRING, STRING>>)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, tc.dialect)
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			got, err := Generate(e, tc.dialect)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
	// T-SQL has no CREATE TABLE AS SELECT and the reference REWRITES it into
	// `SELECT * INTO`. That is a transformation, not a spelling, so the port
	// reads the statement and declines to write it.
	e, err := ParseOne("CREATE TABLE t AS SELECT 1", "tsql")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if got, err := Generate(e, "tsql"); err == nil {
		t.Errorf("wrote %q; T-SQL writes this another way entirely", got)
	}
	// Anything the port cannot read WHOLE is refused, not read in part: a
	// dropped constraint changes what the table is.
	for _, sql := range []string{
		"CREATE TABLE t (a INT GENERATED ALWAYS AS ROW START)",
		"CREATE MATERIALIZED VIEW v AS SELECT 1",
		"CREATE TABLE t WITH (FORMAT='parquet')",
		"CREATE TABLE t (a INT",
		"CREATE TABLE t (a INT) PARTITIONED BY (b)",
	} {
		if _, err := ParseOne(sql, ""); err == nil {
			t.Errorf("ParseOne(%q) was read; it should be refused", sql)
		}
	}
}

// A VIEW names a query, and TEMPORARY says how long what is made lasts. The
// second is not a flag on the node: the reference keeps it as one of a LIST of
// properties, which is why it needs a node rather than a boolean.
//
// Both are also where a dialect stops writing what it read. T-SQL has no
// TEMPORARY and renames the object to `#t` instead; Databricks gives a
// temporary TABLE a storage format it was never given; PostgreSQL and DuckDB
// drop a view column's comment. Each is refused rather than written.
func TestCreateViewAndTemporary(t *testing.T) {
	for _, tc := range []struct{ name, dialect, sql, want string }{
		{"a view", "", "CREATE VIEW x AS SELECT a FROM b",
			"CREATE VIEW x AS SELECT a FROM b"},
		{"guarded", "duckdb", "CREATE VIEW IF NOT EXISTS x AS SELECT a FROM b",
			"CREATE VIEW IF NOT EXISTS x AS SELECT a FROM b"},
		{"replaced", "duckdb", "CREATE OR REPLACE VIEW x AS SELECT *",
			"CREATE OR REPLACE VIEW x AS SELECT *"},
		// A view's columns have no types, and a bare name is an Identifier
		// where a name with something said about it is a ColumnDef.
		{"named columns", "", "CREATE VIEW z (a, b COMMENT 'b') AS SELECT a, b FROM d",
			"CREATE VIEW z (a, b COMMENT 'b') AS SELECT a, b FROM d"},
		{"named columns, no query", "", "CREATE VIEW z (a, b)", "CREATE VIEW z (a, b)"},
		{"a temporary table", "duckdb", "CREATE TEMPORARY TABLE x (a INT)",
			"CREATE TEMPORARY TABLE x (a INT)"},
		{"a temporary view", "", "CREATE TEMPORARY VIEW x AS SELECT a FROM d",
			"CREATE TEMPORARY VIEW x AS SELECT a FROM d"},
		{"temporary and replaced", "databricks",
			"CREATE OR REPLACE TEMPORARY VIEW x AS SELECT *",
			"CREATE OR REPLACE TEMPORARY VIEW x AS SELECT *"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, tc.dialect)
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			if !IsWrite(e) {
				t.Errorf("IsWrite(%q) = false; it makes something", tc.sql)
			}
			got, err := Generate(e, tc.dialect)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
	// The dialects that write something else, each refused rather than
	// written as though it were the same statement.
	for _, tc := range []struct{ dialect, sql string }{
		{"tsql", "CREATE TEMPORARY TABLE x (a INT)"},
		{"tsql", "CREATE TEMPORARY VIEW x AS SELECT 1"},
		{"tsql", "CREATE VIEW IF NOT EXISTS x AS SELECT 1"},
		{"tsql", "CREATE TABLE IF NOT EXISTS x (a INT)"},
		{"databricks", "CREATE TEMPORARY TABLE x (a INT)"},
		{"postgres", "CREATE VIEW z (a, b COMMENT 'b')"},
		{"duckdb", "CREATE VIEW z (a, b COMMENT 'b')"},
	} {
		e, err := ParseOne(tc.sql, tc.dialect)
		if err != nil {
			t.Fatalf("ParseOne(%q, %s): %v", tc.sql, tc.dialect, err)
		}
		if got, err := Generate(e, tc.dialect); err == nil {
			t.Errorf("[%s] %q wrote %q; this dialect writes something else",
				tc.dialect, tc.sql, got)
		}
	}
}

// INSERT and DROP, which join CREATE in parsing rather than being refused for
// being unreadable. Both are still writes, and IsWrite says so.
func TestInsertAndDrop(t *testing.T) {
	for _, tc := range []struct{ name, dialect, sql, want string }{
		{"values", "", "INSERT INTO t VALUES (1, 2)", "INSERT INTO t VALUES (1, 2)"},
		{"named columns", "", "INSERT INTO t (a, b) VALUES (1, 2)", "INSERT INTO t (a, b) VALUES (1, 2)"},
		{"several rows", "", "INSERT INTO t VALUES (1, 2), (3, 4)", "INSERT INTO t VALUES (1, 2), (3, 4)"},
		{"a query", "", "INSERT INTO t SELECT 1", "INSERT INTO t SELECT 1"},
		{"overwrite", "", "INSERT OVERWRITE TABLE t SELECT 1", "INSERT OVERWRITE TABLE t SELECT 1"},
		// `DEFAULT` names the column's default rather than referring to
		// anything, so the reference keeps the WORD.
		{"a default", "duckdb", "INSERT INTO t (i) VALUES (DEFAULT)", "INSERT INTO t (i) VALUES (DEFAULT)"},
		// A WITH inside an INSERT is written in FRONT in some dialects and
		// left where it was written in others. Same tree, two spellings.
		{"a cte, left alone", "", "INSERT INTO x WITH y AS (SELECT 1) SELECT * FROM y",
			"INSERT INTO x WITH y AS (SELECT 1) SELECT * FROM y"},
		// T-SQL names an unnamed projection, which is why this one carries an
		// alias the neutral spelling does not.
		{"a cte, hoisted", "tsql", "INSERT INTO x WITH y AS (SELECT 1) SELECT * FROM y",
			"WITH y AS (SELECT 1 AS [1]) INSERT INTO x SELECT * FROM y"},
		{"a drop", "", "DROP TABLE t", "DROP TABLE t"},
		{"a drop if exists", "", "DROP TABLE IF EXISTS t", "DROP TABLE IF EXISTS t"},
		{"a view", "", "DROP VIEW v", "DROP VIEW v"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, tc.dialect)
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			if !IsWrite(e) {
				t.Errorf("IsWrite(%q) = false; it changes something", tc.sql)
			}
			got, err := Generate(e, tc.dialect)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
	// T-SQL writes only two parts of a three-part name in a DROP, which names
	// a different object. Read, and declined.
	e, err := ParseOne("DROP VIEW a.b.c", "tsql")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if got, err := Generate(e, "tsql"); err == nil {
		t.Errorf("wrote %q; T-SQL shortens the name here", got)
	}
	for _, sql := range []string{
		"INSERT INTO t", "INSERT t VALUES (1)", "INSERT INTO t VALUES 1",
		"INSERT INTO t (a VALUES (1)", "DROP TABLE", "DROP INDEX i",
		"DROP TABLE t CASCADE",
	} {
		if _, err := ParseOne(sql, ""); err == nil {
			t.Errorf("ParseOne(%q) was read; it should be refused", sql)
		}
	}
}

// What a column may say about itself. Each is a ColumnConstraint wrapping a
// node of its own kind: the wrapper is uniform and the kind carries the
// meaning.
func TestColumnConstraints(t *testing.T) {
	for _, tc := range []struct{ name, dialect, sql, want string }{
		{"not null", "", "CREATE TABLE z (a INT NOT NULL)", "CREATE TABLE z (a INT NOT NULL)"},
		// NULL is the same node with the flag set, not a different one.
		{"null", "", "CREATE TABLE z (a INT NULL)", "CREATE TABLE z (a INT NULL)"},
		{"a default", "", "CREATE TABLE z (a INT DEFAULT 1)", "CREATE TABLE z (a INT DEFAULT 1)"},
		{"a negative default", "", "CREATE TABLE z (a INT(11) NOT NULL DEFAULT -1)",
			"CREATE TABLE z (a INT(11) NOT NULL DEFAULT -1)"},
		{"a primary key", "", "CREATE TABLE z (a INT PRIMARY KEY)", "CREATE TABLE z (a INT PRIMARY KEY)"},
		// The direction is written only when the statement said one.
		{"and a direction", "", "CREATE TABLE foo (id INT PRIMARY KEY ASC)",
			"CREATE TABLE foo (id INT PRIMARY KEY ASC)"},
		{"unique", "", "CREATE TABLE z (a INT UNIQUE)", "CREATE TABLE z (a INT UNIQUE)"},
		{"a comment", "", "CREATE TABLE z (a INT COMMENT 'x')", "CREATE TABLE z (a INT COMMENT 'x')"},
		{"several at once", "", "CREATE TABLE z (a INT(11) NOT NULL COLLATE utf8_bin AUTO_INCREMENT)",
			"CREATE TABLE z (a INT(11) NOT NULL COLLATE utf8_bin AUTO_INCREMENT)"},
		{"a quoted collation", "postgres", `CREATE TABLE x (a TEXT COLLATE "de_DE")`,
			`CREATE TABLE x (a TEXT COLLATE "de_DE")`},
		// A type carrying PARAMETERS may take a different name from the bare
		// one: Databricks writes VARCHAR as STRING and VARCHAR(255) as itself.
		{"a sized type that keeps its name", "databricks",
			"CREATE TABLE `dbo`.`mytable` (`email` VARCHAR(255) NOT NULL)",
			"CREATE TABLE `dbo`.`mytable` (`email` VARCHAR(255) NOT NULL)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, tc.dialect)
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			got, err := Generate(e, tc.dialect)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
	// A constraint this port does not read is refused, not skipped: each of
	// these says something about the table that dropping it would lose.
	for _, sql := range []string{
		"CREATE TABLE z (a INT GENERATED ALWAYS AS ROW START)",
		"CREATE TABLE z (a INT GENERATED SOMEHOW AS IDENTITY)",
		"CREATE TABLE z (a INT GENERATED ALWAYS IDENTITY)",
		"CREATE TABLE z (a INT GENERATED BY DEFAULT AS (1))",
		"CREATE TABLE z (a INT GENERATED ALWAYS AS IDENTITY (WOBBLE))",
		"CREATE TABLE z (a INT CHECK a > 0)",
		"CREATE TABLE z (a INT CHECK (a > 0)",
		"CREATE TABLE z (a INT CHARACTER SET utf8)",
		"CREATE TABLE z (a INT COMMENT 1)",
		"CREATE TABLE z (a INT COLLATE)",
		"CREATE TABLE z (a INT REFERENCES p (b) ON DELETE PANIC)",
		"CREATE TABLE z (a INT REFERENCES p (b) NOT LIKELY)",
		"CREATE TABLE z (a INT REFERENCES p (b) INITIALLY)",
		"CREATE TABLE z (a INT CONSTRAINT c)",
	} {
		if _, err := ParseOne(sql, ""); err == nil {
			t.Errorf("ParseOne(%q) was read; it should be refused", sql)
		}
	}

	// What a column points AT. The referenced columns wrap the table in a
	// Schema, and what happens to this row when that one changes is kept as
	// the PHRASE rather than as a node -- one entry per option, in the order
	// they were written.
	for _, tc := range []struct{ sql, want string }{
		{"CREATE TABLE z (a INT REFERENCES parent)",
			"CREATE TABLE z (a INT REFERENCES parent)"},
		{"CREATE TABLE z (a INT REFERENCES parent (b, c))",
			"CREATE TABLE z (a INT REFERENCES parent (b, c))"},
		{"CREATE TABLE f (b INT REFERENCES z (i) ON DELETE SET NULL ON UPDATE NO ACTION)",
			"CREATE TABLE f (b INT REFERENCES z (i) ON DELETE SET NULL ON UPDATE NO ACTION)"},
		{"CREATE TABLE f (b INT REFERENCES z (i) ON DELETE CASCADE)",
			"CREATE TABLE f (b INT REFERENCES z (i) ON DELETE CASCADE)"},
		{"CREATE TABLE f (b INT REFERENCES z (i) ON UPDATE RESTRICT)",
			"CREATE TABLE f (b INT REFERENCES z (i) ON UPDATE RESTRICT)"},
		{"CREATE TABLE f (b INT REFERENCES z (i) ON DELETE SET DEFAULT)",
			"CREATE TABLE f (b INT REFERENCES z (i) ON DELETE SET DEFAULT)"},
		// The rest of the vocabulary: a word alone, or a word and the one
		// word that may follow it.
		{"CREATE TABLE foo (baz_id INT REFERENCES baz (id) DEFERRABLE)",
			"CREATE TABLE foo (baz_id INT REFERENCES baz (id) DEFERRABLE)"},
		{"CREATE TABLE foo (b INT REFERENCES z (i) NOT ENFORCED)",
			"CREATE TABLE foo (b INT REFERENCES z (i) NOT ENFORCED)"},
		{"CREATE TABLE foo (b INT REFERENCES z (i) DEFERRABLE INITIALLY DEFERRED)",
			"CREATE TABLE foo (b INT REFERENCES z (i) DEFERRABLE INITIALLY DEFERRED)"},
		{"CREATE TABLE foo (b INT REFERENCES z (i) MATCH FULL)",
			"CREATE TABLE foo (b INT REFERENCES z (i) MATCH FULL)"},
		// A NAMED constraint keeps its name on the wrapper, in front of the
		// kind it names.
		{"CREATE TABLE k (s INT CONSTRAINT k_fk REFERENCES szerzo)",
			"CREATE TABLE k (s INT CONSTRAINT k_fk REFERENCES szerzo)"},
		{"CREATE TABLE k (s INT CONSTRAINT k_nn NOT NULL)",
			"CREATE TABLE k (s INT CONSTRAINT k_nn NOT NULL)"},
	} {
		e, err := ParseOne(tc.sql, "")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		if got, gerr := Generate(e, ""); gerr != nil || got != tc.want {
			t.Errorf("%q wrote %q (%v), want %q", tc.sql, got, gerr, tc.want)
		}
	}
	// A bare VARCHAR still becomes STRING where the dialect says so.
	e, err := ParseOne("CREATE TABLE t (a VARCHAR)", "databricks")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if got, _ := Generate(e, "databricks"); got != "CREATE TABLE t (a STRING)" {
		t.Errorf("got %q, want the bare type mapped", got)
	}
}

// ALTER TABLE, in the three shapes the corpus mostly holds. The actions are a
// LIST because some dialects take several, and each is a node of its own -- an
// ADD is the very ColumnDef a CREATE builds, with one argument more.
func TestAlterTable(t *testing.T) {
	for _, tc := range []struct{ name, dialect, sql, want string }{
		{"add a column", "", "ALTER TABLE t ADD COLUMN k INT", "ALTER TABLE t ADD COLUMN k INT"},
		{"with a default", "", "ALTER TABLE integers ADD COLUMN l INT DEFAULT 10",
			"ALTER TABLE integers ADD COLUMN l INT DEFAULT 10"},
		{"drop a column", "", "ALTER TABLE t DROP COLUMN k", "ALTER TABLE t DROP COLUMN k"},
		{"if exists", "", "ALTER TABLE IF EXISTS t ADD COLUMN k INT",
			"ALTER TABLE IF EXISTS t ADD COLUMN k INT"},
		{"rename", "", "ALTER TABLE t RENAME TO u", "ALTER TABLE t RENAME TO u"},
		// How much of a qualified name a RENAME writes is per dialect: the new
		// table lives where the old one did, so DuckDB writes only the name.
		{"a qualified rename, kept", "databricks", "ALTER TABLE db.t1 RENAME TO db.t2",
			"ALTER TABLE db.t1 RENAME TO db.t2"},
		{"a qualified rename, shortened", "duckdb", "ALTER TABLE db.t1 RENAME TO db.t2",
			"ALTER TABLE db.t1 RENAME TO t2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, tc.dialect)
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			if !IsWrite(e) {
				t.Errorf("IsWrite(%q) = false; it changes something", tc.sql)
			}
			got, err := Generate(e, tc.dialect)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
	// T-SQL has no ALTER ... RENAME at all: the reference calls a stored
	// procedure instead, which is a transformation this port does not do.
	e, err := ParseOne("ALTER TABLE t RENAME TO u", "tsql")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if got, err := Generate(e, "tsql"); err == nil {
		t.Errorf("wrote %q; T-SQL calls sp_rename", got)
	}
	for _, sql := range []string{
		"ALTER TABLE t SET TBLPROPERTIES ('a' = 'b')",
		"ALTER TABLE t ADD CONSTRAINT c EXCLUDE USING gin(a WITH &&)",
		"ALTER TABLE t ALTER COLUMN a SET NOT NULL",
		"ALTER INDEX i RENAME TO j",
		"ALTER TABLE t",
	} {
		if _, err := ParseOne(sql, ""); err == nil {
			t.Errorf("ParseOne(%q) was read; it should be refused", sql)
		}
	}
}

// The statements that change ROWS. Each is read, named a write, and written
// back; the dialect facts among them get a case of their own.
func TestUpdateAndDelete(t *testing.T) {
	for _, tc := range []struct{ name, dialect, sql, want string }{
		{"a plain update", "", "UPDATE tbl SET foo = 123, bar = 345",
			"UPDATE tbl SET foo = 123, bar = 345"},
		{"qualified, with a where", "", "UPDATE db.t SET foo = 123 WHERE t.bar = 234",
			"UPDATE db.t SET foo = 123 WHERE t.bar = 234"},
		{"an aliased target", "postgres", "UPDATE prices AS p SET p.amount = p.amount * 0.9",
			"UPDATE prices AS p SET p.amount = p.amount * 0.9"},
		{"a second table to read from", "postgres",
			"UPDATE foo SET a = bar.a FROM bar WHERE foo.id = bar.id",
			"UPDATE foo SET a = bar.a FROM bar WHERE foo.id = bar.id"},
		{"returning", "postgres", "UPDATE tbl SET foo = 123 RETURNING a",
			"UPDATE tbl SET foo = 123 RETURNING a"},
		// T-SQL calls it OUTPUT and writes it in front of the FROM rather than
		// after the WHERE. Same node, two places.
		{"output", "tsql", "UPDATE x SET y = 1 OUTPUT x.a, x.b FROM y",
			"UPDATE x SET y = 1 OUTPUT x.a, x.b FROM y"},
		{"a plain delete", "", "DELETE FROM y", "DELETE FROM y"},
		{"with a where", "", "DELETE FROM x WHERE y > 1", "DELETE FROM x WHERE y > 1"},
		{"using", "", "DELETE FROM event USING sales AS s WHERE event.eventid = s.eventid",
			"DELETE FROM event USING sales AS s WHERE event.eventid = s.eventid"},
		// `USING a, b` is a comma JOIN on the first table, not a second entry.
		{"using two", "", "DELETE FROM event USING sales, bla WHERE event.eventid = sales.eventid",
			"DELETE FROM event USING sales, bla WHERE event.eventid = sales.eventid"},
		{"delete returning", "postgres", "DELETE FROM x WHERE y > 1 RETURNING a",
			"DELETE FROM x WHERE y > 1 RETURNING a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, tc.dialect)
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			if !IsWrite(e) {
				t.Errorf("IsWrite(%q) = false; it changes rows", tc.sql)
			}
			got, err := Generate(e, tc.dialect)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
	// A DELETE reads as much as a query does, and the guard above this port
	// has to see it: the source here is a subquery, not a table.
	e, err := ParseOne("DELETE FROM t WHERE id IN (SELECT id FROM secrets)", "postgres")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if len(e.FindAll("Select")) != 1 {
		t.Error("the subquery a DELETE reads from is not in the tree")
	}
	for _, sql := range []string{
		"UPDATE t1 AS a, t2 AS b SET a.id = 1",
		"UPDATE t WHERE a = 1",
		"DELETE x OUTPUT x.a FROM z",
		"UPDATE t SET a = 1 ORDER BY a",
	} {
		if _, err := ParseOne(sql, ""); err == nil {
			t.Errorf("ParseOne(%q) was read; it should be refused", sql)
		}
	}
}

// MERGE matches two relations and says what to do about each outcome, so the
// branches are where its meaning is.
func TestMerge(t *testing.T) {
	for _, tc := range []struct{ name, dialect, sql, want string }{
		{"update on match", "duckdb",
			"MERGE INTO x AS z USING (SELECT id) AS y ON a = b WHEN MATCHED THEN UPDATE SET a = y.b",
			"MERGE INTO x AS z USING (SELECT id) AS y ON a = b WHEN MATCHED THEN UPDATE SET a = y.b"},
		{"insert when not matched", "duckdb",
			"MERGE INTO x USING (SELECT id) AS y ON a = b WHEN NOT MATCHED THEN INSERT (a, b) VALUES (y.a, y.b)",
			"MERGE INTO x USING (SELECT id) AS y ON a = b WHEN NOT MATCHED THEN INSERT (a, b) VALUES (y.a, y.b)"},
		{"a guarded branch", "duckdb",
			"MERGE INTO t USING s ON a = b WHEN MATCHED AND x > 1 THEN DO NOTHING",
			"MERGE INTO t USING s ON a = b WHEN MATCHED AND x > 1 THEN DO NOTHING"},
		{"by source", "duckdb",
			"MERGE INTO t USING s ON a = b WHEN NOT MATCHED BY SOURCE THEN DELETE",
			"MERGE INTO t USING s ON a = b WHEN NOT MATCHED BY SOURCE THEN DELETE"},
		// DuckDB spells the match as a column list rather than a condition,
		// and it takes the place of the ON entirely.
		{"matched on columns", "duckdb",
			"MERGE INTO people USING (SELECT 1 AS id) AS su USING (id) WHEN MATCHED THEN UPDATE",
			"MERGE INTO people USING (SELECT 1 AS id) AS su USING (id) WHEN MATCHED THEN UPDATE"},
		// PostgreSQL leaves the target's own name off the side that assigns:
		// nothing else can be assigned there, so the qualifier is noise. The
		// right-hand side keeps its own.
		{"the target name dropped", "postgres",
			"MERGE INTO x AS z USING (SELECT id) AS y ON a = b WHEN MATCHED THEN UPDATE SET z.a = y.b",
			"MERGE INTO x AS z USING (SELECT id) AS y ON a = b WHEN MATCHED THEN UPDATE SET a = y.b"},
		// The comparison folds case where the dialect does: `X` names `x`.
		{"the target name folded", "postgres",
			"MERGE INTO x USING (SELECT id) AS y ON a = b WHEN MATCHED THEN UPDATE SET X.a = y.b",
			"MERGE INTO x USING (SELECT id) AS y ON a = b WHEN MATCHED THEN UPDATE SET a = y.b"},
		// And a dialect that does not drop it writes it.
		{"the target name kept", "duckdb",
			"MERGE INTO x USING (SELECT id) AS y ON a = b WHEN MATCHED THEN UPDATE SET x.a = y.b",
			"MERGE INTO x USING (SELECT id) AS y ON a = b WHEN MATCHED THEN UPDATE SET x.a = y.b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, tc.dialect)
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			if !IsWrite(e) {
				t.Errorf("IsWrite(%q) = false; it changes rows", tc.sql)
			}
			got, err := Generate(e, tc.dialect)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
	for _, sql := range []string{
		"MERGE INTO t USING s ON a = b",
		"MERGE INTO t USING s ON a = b WHEN MATCHED THEN TRUNCATE",
		"MERGE t USING s ON a = b WHEN MATCHED THEN DELETE",
		"MERGE INTO mytable WITH (HOLDLOCK) AS T USING m AS S ON T.id = S.id WHEN MATCHED THEN DELETE",
	} {
		if _, err := ParseOne(sql, ""); err == nil {
			t.Errorf("ParseOne(%q) was read; it should be refused", sql)
		}
	}
}

// The refusals and the corners of the DML writers.
//
// Some of these trees no statement in the corpus produces: a Delete naming its
// target twice, a Returning with an INTO. They are built here directly rather
// than left unrun, because the writer that refuses them is the only thing
// standing between such a tree and SQL that says the wrong thing.
func TestDMLCorners(t *testing.T) {
	for _, sql := range []string{
		"UPDATE t SET a",
		"MERGE INTO t USING s USING (1) WHEN MATCHED THEN DELETE",
		"MERGE INTO t USING s ON a = b WHEN MATCHED THEN DELETE LIMIT 1",
		"MERGE INTO t USING s ON a = b WHEN NOT x THEN DELETE",
		"MERGE INTO t USING s ON a = b WHEN MATCHED DELETE",
		"MERGE INTO t USING s ON a = b WHEN NOT MATCHED THEN INSERT",
	} {
		if _, err := ParseOne(sql, ""); err == nil {
			t.Errorf("ParseOne(%q) was read; it should be refused", sql)
		}
	}
	// T-SQL writes OUTPUT in front of the FROM, so a statement with one there
	// AND one at the end says the same thing twice.
	if _, err := ParseOne("UPDATE x SET y = 1 OUTPUT a WHERE b = 1 OUTPUT c", "tsql"); err == nil {
		t.Error("two OUTPUT clauses were read as one statement")
	}

	for _, tc := range []struct{ name, dialect, sql, want string }{
		// `BY TARGET` is the default and is recorded as nothing, so it is read
		// and then not written -- unlike BY SOURCE, which is a different set
		// of rows and keeps its words.
		{"by target", "duckdb",
			"MERGE INTO t USING s ON a = b WHEN NOT MATCHED BY TARGET THEN DELETE",
			"MERGE INTO t USING s ON a = b WHEN NOT MATCHED THEN DELETE"},
		{"insert everything", "duckdb",
			"MERGE INTO t USING s ON a = b WHEN NOT MATCHED THEN INSERT *",
			"MERGE INTO t USING s ON a = b WHEN NOT MATCHED THEN INSERT *"},
		{"insert values only", "duckdb",
			"MERGE INTO t USING s ON a = b WHEN NOT MATCHED THEN INSERT VALUES (1, 2)",
			"MERGE INTO t USING s ON a = b WHEN NOT MATCHED THEN INSERT VALUES (1, 2)"},
		// A subquery is its own scope: the target's name is not dropped inside
		// one, because in there it refers to something else.
		{"a subquery keeps its names", "postgres",
			"MERGE INTO x USING s AS y ON a = b WHEN MATCHED THEN UPDATE SET x.a = (SELECT x.c FROM z)",
			"MERGE INTO x USING s AS y ON a = b WHEN MATCHED THEN UPDATE SET a = (SELECT x.c FROM z)"},
		// An equality whose left side is not a column is left alone.
		{"an assignment of a comparison", "postgres",
			"MERGE INTO x USING s AS y ON a = b WHEN MATCHED THEN UPDATE SET a = (1 = 2)",
			"MERGE INTO x USING s AS y ON a = b WHEN MATCHED THEN UPDATE SET a = (1 = 2)"},
		// PostgreSQL folds a bare name and keeps a quoted one, so `"X"` names
		// a different table from `x` and its qualifier stays.
		{"a quoted qualifier is another table", "postgres",
			`MERGE INTO x USING s AS y ON a = b WHEN MATCHED THEN UPDATE SET "X".a = y.b`,
			`MERGE INTO x USING s AS y ON a = b WHEN MATCHED THEN UPDATE SET "X".a = y.b`},
		// And the INSERT column list is stripped the same way the assignments
		// are, while the VALUES keep their qualifiers.
		{"insert columns stripped", "postgres",
			"MERGE INTO x USING s AS y ON a = b WHEN NOT MATCHED THEN INSERT (x.a, x.b) VALUES (y.a, y.b)",
			"MERGE INTO x USING s AS y ON a = b WHEN NOT MATCHED THEN INSERT (a, b) VALUES (y.a, y.b)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, tc.dialect)
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			got, err := Generate(e, tc.dialect)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}

	// Trees the parser never builds, and the writers that refuse them.
	for _, tc := range []struct {
		name string
		node *Expression
	}{
		{"a delete with no table", New("Delete")},
		{"a delete naming its target twice", New("Delete",
			Arg{"this", New("Table", Arg{"this", New("Identifier", Arg{"this", "x"})})},
			Arg{"tables", []*Expression{New("Table",
				Arg{"this", New("Identifier", Arg{"this", "y"})})}})},
		{"a returning that also writes elsewhere", New("Delete",
			Arg{"this", New("Table", Arg{"this", New("Identifier", Arg{"this", "x"})})},
			Arg{"returning", New("Returning",
				Arg{"expressions", []*Expression{New("Star")}},
				Arg{"into", New("Table", Arg{"this", New("Identifier", Arg{"this", "t"})})})},
		)},
		{"a merge branch that does nothing at all", New("Merge",
			Arg{"this", New("Table", Arg{"this", New("Identifier", Arg{"this", "x"})})},
			Arg{"using", New("Table", Arg{"this", New("Identifier", Arg{"this", "y"})})},
			Arg{"whens", New("Whens", Arg{"expressions", []*Expression{
				New("When", Arg{"matched", true})}})})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := Generate(tc.node, ""); err == nil {
				t.Errorf("wrote %q; it should be refused", got)
			}
		})
	}

	// And a MERGE missing the parts the target-stripping walk reads: it looks
	// at each of them before it looks inside, so a tree without one is left
	// alone rather than crashing the writer.
	for _, node := range []*Expression{
		New("Merge", Arg{"using", New("Table")}),
		New("Merge", Arg{"this", New("Table",
			Arg{"this", New("Identifier", Arg{"this", "x"})})}),
		New("Merge",
			Arg{"this", New("Table", Arg{"this", New("Identifier", Arg{"this", "x"})})},
			Arg{"using", New("Table", Arg{"this", New("Identifier", Arg{"this", "y"})})},
			Arg{"whens", New("Whens", Arg{"expressions", []*Expression{
				New("When", Arg{"matched", true}),
				New("When", Arg{"matched", false},
					Arg{"then", New("Insert", Arg{"this", New("Star")})}),
			}})}),
	} {
		// PostgreSQL is the dialect that strips, so it is the one that walks.
		if _, err := Generate(node, "postgres"); err == nil {
			t.Error("a half-built MERGE was written")
		}
	}
}

// The DML readers hand back whatever the expression parser refuses, rather
// than swallowing it and reading half a statement. One malformed input per
// place a sub-parser is called.
func TestDMLRefusalsAreCarried(t *testing.T) {
	for _, tc := range []struct{ dialect, sql string }{
		{"", "UPDATE 1 SET a = 1"},
		{"", "UPDATE t SET FROM"},
		{"", "UPDATE t SET a = 1 FROM 1"},
		{"", "UPDATE t SET a = 1 WHERE FROM"},
		{"postgres", "UPDATE t SET a = 1 RETURNING FROM"},
		{"postgres", "UPDATE t SET a = 1 RETURNING a INTO b"},
		{"", "DELETE FROM 1"},
		{"", "DELETE FROM x USING 1"},
		{"", "DELETE FROM x WHERE FROM"},
		{"postgres", "DELETE FROM x RETURNING a INTO b"},
		{"", "MERGE INTO 1 USING s ON a = b WHEN MATCHED THEN DELETE"},
		{"", "MERGE INTO t USING 1 ON a = b WHEN MATCHED THEN DELETE"},
		{"", "MERGE INTO t USING s ON FROM WHEN MATCHED THEN DELETE"},
		{"", "MERGE INTO t USING s USING (FROM) WHEN MATCHED THEN DELETE"},
		{"", "MERGE INTO t USING s ON a = b WHEN MATCHED AND FROM THEN DELETE"},
		{"", "MERGE INTO t USING s ON a = b WHEN MATCHED THEN UPDATE SET FROM"},
		{"", "MERGE INTO t USING s ON a = b WHEN NOT MATCHED THEN INSERT (FROM) VALUES (1)"},
		{"", "MERGE INTO t USING s ON a = b WHEN NOT MATCHED THEN INSERT (a) VALUES (FROM)"},
		{"postgres", "MERGE INTO t USING s ON a = b WHEN MATCHED THEN DELETE RETURNING a INTO b"},
	} {
		if _, err := ParseOne(tc.sql, tc.dialect); err == nil {
			t.Errorf("ParseOne(%q) was read; it should be refused", tc.sql)
		}
	}
}

// `x -> y` means two different things, and which one it is depends on where
// it is written: in argument position it is a lambda, and anywhere else it is
// a JSON extraction. A QUALIFIED call is argument position too -- the port
// checked only the unqualified one, and Databricks wrote the JSON spelling of
// a lambda, `a.b(x:y)`, which is not SQL. The generator fuzzer found it.
func TestALambdaInAQualifiedCall(t *testing.T) {
	for _, tc := range []struct{ sql, want, class string }{
		{"A.A(A -> B)", "A.A(A -> B)", "Dot"},
		{"A.A(A -> A(0))", "A.A(A -> A(0))", "Dot"},
		{"F(A -> B)", "F(A -> B)", "Anonymous"},
		// And outside a call the same tokens still extract from JSON.
		{"A -> B", "A:B", "JSONExtract"},
	} {
		e, err := ParseOne(tc.sql, "databricks")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		if e.Class != tc.class {
			t.Errorf("%q is a %s, want %s", tc.sql, e.Class, tc.class)
		}
		got, err := Generate(e, "databricks")
		if err != nil {
			t.Fatalf("Generate(%q): %v", tc.sql, err)
		}
		if got != tc.want {
			t.Errorf("%q wrote %q, want %q", tc.sql, got, tc.want)
		}
	}
}

// Everything else an ALTER TABLE does, and the ALTER VIEW that gives a view a
// new query.
//
// The actions are a LIST, and the commas are not always between whole ones:
// T-SQL writes `ADD a INT, b INT`, where only the first says ADD and the rest
// continue it. The tree is the same list of definitions either way.
func TestAlterActions(t *testing.T) {
	for _, tc := range []struct{ name, dialect, sql, want string }{
		{"add if not exists", "", "ALTER TABLE t ADD COLUMN IF NOT EXISTS k INT",
			"ALTER TABLE t ADD COLUMN IF NOT EXISTS k INT"},
		{"add two", "", "ALTER TABLE t ADD COLUMN a TEXT, ADD COLUMN b INT",
			"ALTER TABLE t ADD COLUMN a TEXT, ADD COLUMN b INT"},
		// T-SQL says neither the word COLUMN nor a second ADD, reading and
		// writing the same list another way.
		{"add two, T-SQL", "tsql", "ALTER TABLE a ADD b INTEGER, c INTEGER",
			"ALTER TABLE a ADD b INTEGER, c INTEGER"},
		{"drop if exists", "", "ALTER TABLE t DROP COLUMN IF EXISTS k",
			"ALTER TABLE t DROP COLUMN IF EXISTS k"},
		{"drop cascade", "", "ALTER TABLE t DROP COLUMN k CASCADE",
			"ALTER TABLE t DROP COLUMN k CASCADE"},
		{"drop two", "", "ALTER TABLE t DROP COLUMN a, DROP COLUMN IF EXISTS b",
			"ALTER TABLE t DROP COLUMN a, DROP COLUMN IF EXISTS b"},
		{"rename a column", "", "ALTER TABLE t RENAME COLUMN c1 TO c2",
			"ALTER TABLE t RENAME COLUMN c1 TO c2"},
		{"rename a column if it is there", "", "ALTER TABLE t RENAME COLUMN IF EXISTS c1 TO c2",
			"ALTER TABLE t RENAME COLUMN IF EXISTS c1 TO c2"},
		// The phrase in front of a column's new type is the dialect's.
		{"a new type", "postgres", "ALTER TABLE t ALTER COLUMN i SET DATA TYPE VARCHAR",
			"ALTER TABLE t ALTER COLUMN i SET DATA TYPE VARCHAR"},
		{"a new type, Databricks", "databricks", "ALTER TABLE t ALTER COLUMN i TYPE BIGINT",
			"ALTER TABLE t ALTER COLUMN i TYPE BIGINT"},
		{"a new type, T-SQL", "tsql", "ALTER TABLE t ALTER COLUMN i SET DATA TYPE BIGINT",
			"ALTER TABLE t ALTER COLUMN i BIGINT"},
		{"a new type from the old", "databricks",
			"ALTER TABLE t ALTER COLUMN i TYPE STRING USING CONCAT(i, '_', j)",
			"ALTER TABLE t ALTER COLUMN i TYPE STRING USING CONCAT(i, '_', j)"},
		{"a new default", "", "ALTER TABLE t ALTER COLUMN i SET DEFAULT 10",
			"ALTER TABLE t ALTER COLUMN i SET DEFAULT 10"},
		{"no default", "", "ALTER TABLE t ALTER COLUMN i DROP DEFAULT",
			"ALTER TABLE t ALTER COLUMN i DROP DEFAULT"},
		{"a comment", "", "ALTER TABLE t ALTER COLUMN a COMMENT 'tablespoons'",
			"ALTER TABLE t ALTER COLUMN a COMMENT 'tablespoons'"},
		{"a view's new query", "", "ALTER VIEW v AS SELECT a, b FROM t",
			"ALTER VIEW v AS SELECT a, b FROM t"},
		{"a view's new union", "", "ALTER VIEW v AS SELECT a FROM t UNION ALL SELECT a FROM u",
			"ALTER VIEW v AS SELECT a FROM t UNION ALL SELECT a FROM u"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, tc.dialect)
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			if !IsWrite(e) {
				t.Errorf("IsWrite(%q) = false; it changes the table", tc.sql)
			}
			got, err := Generate(e, tc.dialect)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
	for _, sql := range []string{
		"ALTER TABLE t ALTER COLUMN i SET SOMETHING",
		"ALTER TABLE t RENAME COLUMN c1 c2",
		"ALTER TABLE t ADD COLUMN a INT, b",
		"ALTER TABLE t DROP PARTITION(dt = '2014-05-14')",
		"ALTER VIEW v",
		"ALTER TABLE t ALTER COLUMN i COMMENT 1",
	} {
		if _, err := ParseOne(sql, ""); err == nil {
			t.Errorf("ParseOne(%q) was read; it should be refused", sql)
		}
	}
	// An AlterColumn that says nothing at all is not a statement, and no
	// parser builds one -- but the writer is what stands between such a tree
	// and SQL that means something else.
	empty := New("Alter",
		Arg{"this", New("Table", Arg{"this", New("Identifier", Arg{"this", "t"})})},
		Arg{"kind", "TABLE"},
		Arg{"actions", []*Expression{New("AlterColumn",
			Arg{"this", New("Identifier", Arg{"this", "a"})})}})
	if got, err := Generate(empty, ""); err == nil {
		t.Errorf("wrote %q; the action says nothing", got)
	}
}

// A constraint on the TABLE rather than on one column: it stands where a
// column definition would and is told from one by the word it starts with.
//
// A NAMED table constraint is a Constraint holding a LIST of kinds -- a
// different node from the wrapper a named COLUMN constraint uses, though the
// two look alike in the text.
func TestTableConstraints(t *testing.T) {
	for _, tc := range []struct{ name, dialect, sql, want string }{
		{"a key over columns", "", "CREATE TABLE z (a INT, PRIMARY KEY (a, b))",
			"CREATE TABLE z (a INT, PRIMARY KEY (a, b))"},
		{"a foreign key", "", "CREATE TABLE z (a INT, FOREIGN KEY (a) REFERENCES parent (b, c))",
			"CREATE TABLE z (a INT, FOREIGN KEY (a) REFERENCES parent (b, c))"},
		{"a named check", "", "CREATE TABLE z (a INT, CONSTRAINT c CHECK (a > 0))",
			"CREATE TABLE z (a INT, CONSTRAINT c CHECK (a > 0))"},
		{"unique over columns", "postgres", "CREATE TABLE z (a INT, UNIQUE (a, b))",
			"CREATE TABLE z (a INT, UNIQUE (a, b))"},
		// T-SQL reads a key's columns the way it reads an index's, so a
		// member is an Ordered there and a bare name everywhere else.
		{"a key with a direction", "tsql",
			"CREATE TABLE db.t1 (a INTEGER, b INTEGER, CONSTRAINT c PRIMARY KEY (a DESC, b))",
			"CREATE TABLE db.t1 (a INTEGER, b INTEGER, CONSTRAINT c PRIMARY KEY (a DESC, b))"},
		{"added to a table", "", "ALTER TABLE t ADD CONSTRAINT c PRIMARY KEY (a, b)",
			"ALTER TABLE t ADD CONSTRAINT c PRIMARY KEY (a, b)"},
		{"a foreign key added", "", "ALTER TABLE t ADD CONSTRAINT c FOREIGN KEY (a) REFERENCES p (b)",
			"ALTER TABLE t ADD CONSTRAINT c FOREIGN KEY (a) REFERENCES p (b)"},
		{"a check added", "", "ALTER TABLE t ADD CONSTRAINT c CHECK (a > 0)",
			"ALTER TABLE t ADD CONSTRAINT c CHECK (a > 0)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, tc.dialect)
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			got, err := Generate(e, tc.dialect)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
	// Databricks has no UNIQUE at all, and the reference DROPS it -- silently
	// giving up the guarantee the statement was making. The port refuses
	// rather than writing a table that promises less than it was asked to.
	for _, sql := range []string{
		"CREATE TABLE z (a INT UNIQUE)",
		"CREATE TABLE z (a INT, UNIQUE (a))",
	} {
		e, err := ParseOne(sql, "databricks")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", sql, err)
		}
		if got, err := Generate(e, "databricks"); err == nil {
			t.Errorf("%q wrote %q; Databricks drops the constraint", sql, got)
		}
	}
	for _, sql := range []string{
		"CREATE TABLE z (a INT, PRIMARY KEY a)",
		"CREATE TABLE z (a INT, FOREIGN KEY (a))",
		"CREATE TABLE z (a INT, CHECK a > 0)",
		"CREATE TABLE z (a INT, CHECK (a > 0)",
		"CREATE TABLE z (a INT, EXCLUDE USING gin(a WITH &&))",
		"ALTER TABLE t ADD PRIMARY KEY (x, y) NOT ENFORCED",
	} {
		if _, err := ParseOne(sql, ""); err == nil {
			t.Errorf("ParseOne(%q) was read; it should be refused", sql)
		}
	}
}

// CREATE FUNCTION, which is the one CREATE whose parts are not in a fixed
// order: the properties may come before the body, after it, or on both sides,
// and the reference keeps them in the order they were WRITTEN.
func TestCreateFunction(t *testing.T) {
	for _, tc := range []struct{ name, dialect, sql, want string }{
		{"a bare name", "", "CREATE FUNCTION f", "CREATE FUNCTION f"},
		{"a string body", "", "CREATE FUNCTION f AS 'g'", "CREATE FUNCTION f AS 'g'"},
		{"parameters", "", "CREATE FUNCTION a(b INT, c VARCHAR) AS 'SELECT 1'",
			"CREATE FUNCTION a(b INT, c VARCHAR) AS 'SELECT 1'"},
		// `add(INT, INT)` names no parameters at all: each TYPE is kept as a
		// bare name, because that is all the statement said.
		{"unnamed parameters", "postgres", "CREATE FUNCTION add(INT, INT) RETURNS INT AS 'x'",
			"CREATE FUNCTION add(INT, INT) RETURNS INT AS 'x'"},
		{"a returned expression", "", "CREATE FUNCTION a.b(x INT) RETURNS INT AS RETURN x + 1",
			"CREATE FUNCTION a.b(x INT) RETURNS INT AS RETURN x + 1"},
		// Databricks writes no AS in front of a RETURN.
		{"returned, Databricks", "databricks",
			"CREATE FUNCTION a.b(x INT) RETURNS INT RETURN x + 1",
			"CREATE FUNCTION a.b(x INT) RETURNS INT RETURN x + 1"},
		{"properties in source order", "",
			"CREATE FUNCTION a.b(x TEXT) LANGUAGE SQL READS SQL DATA RETURNS TEXT AS RETURN x",
			"CREATE FUNCTION a.b(x TEXT) LANGUAGE SQL READS SQL DATA RETURNS TEXT AS RETURN x"},
		{"properties after the body", "postgres",
			"CREATE FUNCTION add(INT, INT) RETURNS INT LANGUAGE SQL IMMUTABLE STRICT AS 'x'",
			"CREATE FUNCTION add(INT, INT) RETURNS INT LANGUAGE SQL IMMUTABLE STRICT AS 'x'"},
		{"called on null input", "",
			"CREATE FUNCTION a(x INT) RETURNS INT LANGUAGE SQL CALLED ON NULL INPUT AS 'SELECT 1'",
			"CREATE FUNCTION a(x INT) RETURNS INT LANGUAGE SQL CALLED ON NULL INPUT AS 'SELECT 1'"},
		// A second RETURNS, which is how the reference records this phrase.
		{"returns null on null input", "postgres",
			"CREATE FUNCTION add(INT) RETURNS INT LANGUAGE SQL RETURNS NULL ON NULL INPUT AS 'x'",
			"CREATE FUNCTION add(INT) RETURNS INT LANGUAGE SQL RETURNS NULL ON NULL INPUT AS 'x'"},
		{"returning a table", "databricks",
			"CREATE OR REPLACE FUNCTION func(a BIGINT) RETURNS TABLE (a INT) RETURN SELECT a",
			"CREATE OR REPLACE FUNCTION func(a BIGINT) RETURNS TABLE (a INT) RETURN SELECT a"},
		// DuckDB writes the return type IN the body and no other property at
		// all, so the same node lands in a different place there.
		{"a table, DuckDB", "duckdb",
			"CREATE OR REPLACE FUNCTION func(a BIGINT) AS TABLE SELECT a",
			"CREATE OR REPLACE FUNCTION func(a BIGINT) AS TABLE SELECT a"},
		{"a temporary function", "duckdb", "CREATE TEMPORARY FUNCTION f1(a BIGINT) AS (a + b)",
			"CREATE TEMPORARY FUNCTION f1(a BIGINT) AS (a + b)"},
		// PostgreSQL writes a parameter's mode in front of it and spells both
		// directions as one word; everyone else writes it after the type, as
		// two.
		{"a mode, PostgreSQL", "postgres", "CREATE FUNCTION foo(INOUT a INT)",
			"CREATE FUNCTION foo(INOUT a INT)"},
		{"every mode", "postgres",
			"CREATE FUNCTION foo(a INT, OUT b INT, INOUT c VARCHAR, VARIADIC d INT[])",
			"CREATE FUNCTION foo(a INT, OUT b INT, INOUT c VARCHAR, VARIADIC d INT[])"},
		// And a mode word is only a mode when a name AND a type follow it:
		// this one names the parameter.
		{"a mode word as a name", "postgres", "CREATE FUNCTION foo(variadic INT[])",
			"CREATE FUNCTION foo(variadic INT[])"},
		{"a default", "postgres", "CREATE FUNCTION foo(OUT x INT DEFAULT 5)",
			"CREATE FUNCTION foo(OUT x INT DEFAULT 5)"},
		// T-SQL names parameters the way it names variables, and the marker
		// is part of the name rather than punctuation in front of it.
		{"a T-SQL parameter", "tsql", "CREATE FUNCTION foo(@bar INTEGER) RETURNS TABLE AS RETURN SELECT 1",
			"CREATE FUNCTION foo(@bar INTEGER) RETURNS TABLE AS RETURN SELECT 1"},
		// It also NAMES the table it returns, and the name sits on the
		// property rather than on the columns.
		{"a named table", "tsql",
			"CREATE FUNCTION foo(@bar INTEGER) RETURNS @foo TABLE (x INTEGER) AS RETURN SELECT 1",
			"CREATE FUNCTION foo(@bar INTEGER) RETURNS @foo TABLE (x INTEGER) AS RETURN SELECT 1"},
		{"a session setting", "postgres",
			"CREATE FUNCTION x(INT) RETURNS INT SET search_path TO 'public'",
			"CREATE FUNCTION x(INT) RETURNS INT SET search_path = 'public'"},
		// A body in another language is kept as text and never read. The TAG
		// between the dollars is not on the node, so it comes back bare.
		{"a foreign body", "postgres",
			"CREATE FUNCTION pymax(a INT) RETURNS INT LANGUAGE plpython3u AS $$return 1$$",
			"CREATE FUNCTION pymax(a INT) RETURNS INT LANGUAGE plpython3u AS $$return 1$$"},
		{"a tagged foreign body", "postgres",
			"CREATE FUNCTION pymax(a INT) RETURNS INT AS $FOO$return 1$FOO$",
			"CREATE FUNCTION pymax(a INT) RETURNS INT AS $$return 1$$"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, tc.dialect)
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			if !IsWrite(e) {
				t.Errorf("IsWrite(%q) = false; it makes something", tc.sql)
			}
			got, err := Generate(e, tc.dialect)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}

	// DuckDB writes no function property except TEMPORARY and the one return
	// type it spells in the body. A function carrying any other one cannot be
	// written there without saying less than the statement did.
	for _, sql := range []string{
		"CREATE FUNCTION f() LANGUAGE sql AS 'x'",
		"CREATE FUNCTION f() RETURNS INT AS 'x'",
		"CREATE FUNCTION f() IMMUTABLE AS 'x'",
	} {
		e, err := ParseOne(sql, "")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", sql, err)
		}
		if got, err := Generate(e, "duckdb"); err == nil {
			t.Errorf("%q wrote %q for DuckDB, which writes the property nowhere", sql, got)
		}
	}

	for _, tc := range []struct{ dialect, sql string }{
		// The reference reads a SET as a setting only when it ENDS the
		// statement, and otherwise swallows the rest as raw text.
		{"postgres", "CREATE FUNCTION x(INT) RETURNS INT SET search_path TO 'p' AS 'y'"},
		{"postgres", "CREATE FUNCTION x(INT) RETURNS INT SET foo FROM CURRENT"},
		{"databricks", "CREATE FUNCTION a() HANDLER 'handler_function'"},
		{"databricks", "CREATE FUNCTION a() PARAMETER STYLE PANDAS"},
		{"", "CREATE FUNCTION f() RETURNS @foo INT"},
		{"", "CREATE FUNCTION f(a INT"},
		{"", "CREATE FUNCTION f() AS 'x' AS 'y'"},
		{"", "CREATE FUNCTION f() LANGUAGE"},
	} {
		if _, err := ParseOne(tc.sql, tc.dialect); err == nil {
			t.Errorf("ParseOne(%q) was read; it should be refused", tc.sql)
		}
	}
}

// The corners of the function writers, and the trees no parser builds.
func TestCreateFunctionCorners(t *testing.T) {
	for _, tc := range []struct{ name, read, write, sql, want string }{
		// Databricks turns a table-valued function's body into a RETURN
		// before writing it, whatever it was written as.
		{"a table body becomes a return", "", "databricks",
			"CREATE FUNCTION f() RETURNS TABLE AS 1",
			"CREATE FUNCTION f() RETURNS TABLE RETURN 1"},
		// DuckDB writes no word in front of a returned expression at all.
		{"a return with no word", "", "duckdb",
			"CREATE FUNCTION f(a INT) AS RETURN a + 1",
			"CREATE FUNCTION f(a INT) AS a + 1"},
		// A mode is still a mode when a constraint follows the type.
		{"a mode before a constraint", "postgres", "postgres",
			"CREATE FUNCTION foo(OUT a INT NOT NULL)",
			"CREATE FUNCTION foo(OUT a INT NOT NULL)"},
		{"and after it elsewhere", "postgres", "databricks",
			"CREATE FUNCTION foo(OUT a INT NOT NULL)",
			"CREATE FUNCTION foo(a INT OUT NOT NULL)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, tc.read)
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			got, err := Generate(e, tc.write)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}

	name := New("UserDefinedFunction",
		Arg{"this", New("Table", Arg{"this", New("Identifier", Arg{"this", "f"})})},
		Arg{"wrapped", true})
	for _, tc := range []struct {
		name, dialect string
		node          *Expression
	}{
		// DuckDB spells a table return type in the body, so a function with
		// one and no body has nowhere to put it.
		{"a return type with no body", "duckdb", New("Create",
			Arg{"this", name}, Arg{"kind", "FUNCTION"},
			Arg{"properties", New("Properties", Arg{"expressions", []*Expression{
				New("ReturnsProperty",
					Arg{"this", New("Schema", Arg{"this", New("Var", Arg{"this", "TABLE"})})},
					Arg{"is_table", true})}})})},
		{"a stability with no word", "", New("Create",
			Arg{"this", name}, Arg{"kind", "FUNCTION"},
			Arg{"properties", New("Properties", Arg{"expressions", []*Expression{
				New("StabilityProperty")}})})},
		{"a setting that unsets", "", New("Create",
			Arg{"this", name}, Arg{"kind", "FUNCTION"},
			Arg{"properties", New("Properties", Arg{"expressions", []*Expression{
				New("SetConfigProperty", Arg{"this", New("Set",
					Arg{"expressions", []*Expression{}}, Arg{"unset", true})})}})})},
		{"a setting that is not one", "", New("Create",
			Arg{"this", name}, Arg{"kind", "FUNCTION"},
			Arg{"properties", New("Properties", Arg{"expressions", []*Expression{
				New("SetConfigProperty", Arg{"this", New("Set",
					Arg{"expressions", []*Expression{New("SetItem",
						Arg{"this", New("Star")})}})})}})})},
		{"a mode that says nothing", "postgres", New("Create",
			Arg{"this", New("UserDefinedFunction",
				Arg{"this", New("Table", Arg{"this", New("Identifier", Arg{"this", "f"})})},
				Arg{"expressions", []*Expression{New("ColumnDef",
					Arg{"this", New("Identifier", Arg{"this", "a"})},
					Arg{"kind", New("DataType", Arg{"this", DataTypeKind("INT")})},
					Arg{"constraints", []*Expression{New("InOutColumnConstraint")}})}},
				Arg{"wrapped", true})},
			Arg{"kind", "FUNCTION"})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := Generate(tc.node, tc.dialect); err == nil {
				t.Errorf("wrote %q; it should be refused", got)
			}
		})
	}
}

// The refusals in the DDL readers that no corpus statement reaches. Each is
// one malformed statement, so the reader that turns it away is run rather
// than shipped on trust.
func TestDDLRefusalsAreReached(t *testing.T) {
	for _, tc := range []struct{ dialect, sql string }{
		{"", "CREATE"},
		{"", "DROP"},
		{"", "ALTER"},
		{"", "CREATE TABLE a.b.c.d (a INT)"},
		{"", "CREATE TABLE z (a INT COLLATE)"},
		{"", "ALTER TABLE t ADD COLUMN a INT, DROP"},
		{"", "CREATE TABLE z (a INT, PRIMARY KEY (a"},
		{"", "CREATE TABLE z (a INT, CHECK (a > 0"},
		{"", "CREATE TABLE z (a INT, FOREIGN KEY (a) REFERENCES p (b) ON"},
		{"tsql", "CREATE TABLE t (a INT, CONSTRAINT c PRIMARY KEY (a"},
		{"tsql", "CREATE FUNCTION f(@)"},
	} {
		if _, err := ParseOne(tc.sql, tc.dialect); err == nil {
			t.Errorf("ParseOne(%q) was read; it should be refused", tc.sql)
		}
	}
	// The two directions a key's columns may be written in, one of which is
	// the default and still recorded.
	for _, tc := range []struct{ sql, want string }{
		{"CREATE TABLE t (a INT, CONSTRAINT c PRIMARY KEY (a ASC))",
			"CREATE TABLE t (a INTEGER, CONSTRAINT c PRIMARY KEY (a ASC))"},
		{"CREATE TABLE t (a INT, CONSTRAINT c PRIMARY KEY (a DESC))",
			"CREATE TABLE t (a INTEGER, CONSTRAINT c PRIMARY KEY (a DESC))"},
	} {
		e, err := ParseOne(tc.sql, "tsql")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		if got, _ := Generate(e, "tsql"); got != tc.want {
			t.Errorf("got %q, want %q", got, tc.want)
		}
	}
	// A column's own PRIMARY KEY takes a direction too.
	if e, err := ParseOne("CREATE TABLE t (a INT PRIMARY KEY DESC)", ""); err != nil {
		t.Fatalf("ParseOne: %v", err)
	} else if got, _ := Generate(e, ""); got != "CREATE TABLE t (a INT PRIMARY KEY DESC)" {
		t.Errorf("got %q", got)
	}
	// And a dropped column may be restricted rather than cascaded.
	if e, err := ParseOne("ALTER TABLE t DROP COLUMN k RESTRICT", ""); err != nil {
		t.Fatalf("ParseOne: %v", err)
	} else if got, _ := Generate(e, ""); got != "ALTER TABLE t DROP COLUMN k RESTRICT" {
		t.Errorf("got %q", got)
	}
}

// `:name` after a colon is a bound parameter, and a QUOTED name counts --
// unlike after `@`, where the quotes make it an Identifier instead. The colon
// form was reading `[:"a"]` as a slice with no lower bound and writing it back
// that way, for a tree the reference writes as `[$a]`.
func TestAQuotedColonParameter(t *testing.T) {
	for _, tc := range []struct{ dialect, sql, want string }{
		{"duckdb", `[:"a"]`, "[$a]"},
		{"postgres", `[:"a"]`, "ARRAY[%(a)s]"},
		// A quoted name in the OTHER position is still a column: only the
		// leading colon names a parameter.
		{"duckdb", `x[1:"a"]`, `x[1:"a"]`},
		{"duckdb", "[:1]", "[:1]"},
		{"duckdb", "[:x]", "[$x]"},
	} {
		e, err := ParseOne(tc.sql, tc.dialect)
		if err != nil {
			t.Fatalf("ParseOne(%q, %s): %v", tc.sql, tc.dialect, err)
		}
		got, err := Generate(e, tc.dialect)
		if err != nil {
			t.Fatalf("Generate(%q): %v", tc.sql, err)
		}
		if got != tc.want {
			t.Errorf("%q wrote %q, want %q", tc.sql, got, tc.want)
		}
	}
}

// A name the tokenizer would not give back is refused rather than written.
//
// The port builds one only from a token the tokenizer could not classify --
// a stray backtick among them -- and writing it bare produced SQL nothing
// could read: `[:CAST(` AS UNKNOWN)]`. The generator fuzzer found it.
func TestANameThatIsNotAName(t *testing.T) {
	e, err := ParseOne("[:`::`]", "duckdb")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if got, err := Generate(e, "duckdb"); err == nil {
		t.Errorf("wrote %q; a stray backtick is not a name", got)
	}
	// A name with a space in it is a name -- it is written in quotes, and
	// reads back as itself.
	q, err := ParseOne(`SELECT "we ird"`, "duckdb")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if got, err := Generate(q, "duckdb"); err != nil || got != `SELECT "we ird"` {
		t.Errorf("got %q (%v)", got, err)
	}
	if !readableAsABareName("a_1") || readableAsABareName("") || readableAsABareName("a b") {
		t.Error("readableAsABareName disagrees with what a bare name is")
	}
	// A PARAMETER's name is put back bare too, so it takes the same rule:
	// `:"//"` would write `$//`, which the port could not read.
	odd, err := ParseOne(`:"//"`, "duckdb")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if got, err := Generate(odd, "duckdb"); err == nil {
		t.Errorf("wrote %q; `//` is not a name", got)
	}
}

// CURRENT_DATE and friends may be written WITH empty parentheses, and the
// tree is the same either way. Databricks writes them, so a port that could
// not read them could not read back what it had just written: `NOW()` there
// becomes `CURRENT_TIMESTAMP()`, and that came back as trailing tokens. The
// generator fuzzer found it.
func TestNoParenFunctionWithParens(t *testing.T) {
	for _, tc := range []struct{ dialect, sql, want string }{
		{"databricks", "NOW()", "CURRENT_TIMESTAMP()"},
		{"databricks", "CURRENT_TIMESTAMP()", "CURRENT_TIMESTAMP()"},
		{"databricks", "CURRENT_TIMESTAMP", "CURRENT_TIMESTAMP()"},
		{"postgres", "CURRENT_TIMESTAMP()", "CURRENT_TIMESTAMP"},
		{"duckdb", "CURRENT_DATE()", "CURRENT_DATE"},
	} {
		e, err := ParseOne(tc.sql, tc.dialect)
		if err != nil {
			t.Fatalf("ParseOne(%q, %s): %v", tc.sql, tc.dialect, err)
		}
		got, err := Generate(e, tc.dialect)
		if err != nil {
			t.Fatalf("Generate(%q): %v", tc.sql, err)
		}
		if got != tc.want {
			t.Errorf("%q wrote %q, want %q", tc.sql, got, tc.want)
		}
		if _, err := ParseOne(got, tc.dialect); err != nil {
			t.Errorf("%q wrote %q, which it cannot read back: %v", tc.sql, got, err)
		}
	}
	// Parentheses holding something are the SAME node carrying an argument --
	// a precision -- not a different call and not an empty pair.
	e, err := ParseOne("CURRENT_TIMESTAMP(3)", "databricks")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if e.Class != "CurrentTimestamp" || e.This() == nil || e.This().Name() != "3" {
		t.Errorf("CURRENT_TIMESTAMP(3) is a %s over %v", e.Class, e.Args["this"])
	}
}

// A column the engine fills for itself: from a sequence, or by computing it.
//
// Three constructs share the word GENERATED, and what comes after AS decides
// which -- with one twist: `AS (x)` with no STORED is a computed column in
// Databricks and an identity CARRYING an expression everywhere else. One
// statement, two nodes.
func TestGeneratedColumns(t *testing.T) {
	for _, tc := range []struct{ name, read, write, sql, want string }{
		{"always", "", "", "CREATE TABLE t (x INT GENERATED ALWAYS AS IDENTITY)",
			"CREATE TABLE t (x INT GENERATED ALWAYS AS IDENTITY)"},
		{"by default", "", "", "CREATE TABLE t (x INT GENERATED BY DEFAULT AS IDENTITY)",
			"CREATE TABLE t (x INT GENERATED BY DEFAULT AS IDENTITY)"},
		{"on null", "", "", "CREATE TABLE t (x INT GENERATED BY DEFAULT ON NULL AS IDENTITY)",
			"CREATE TABLE t (x INT GENERATED BY DEFAULT ON NULL AS IDENTITY)"},
		{"a starting point", "postgres", "postgres",
			"CREATE TABLE t (x BIGINT GENERATED ALWAYS AS IDENTITY (START WITH 10 INCREMENT BY 2))",
			"CREATE TABLE t (x BIGINT GENERATED ALWAYS AS IDENTITY (START WITH 10 INCREMENT BY 2))"},
		{"wrapping round", "", "", "CREATE TABLE t (x BIGINT GENERATED ALWAYS AS IDENTITY (CYCLE))",
			"CREATE TABLE t (x BIGINT GENERATED ALWAYS AS IDENTITY (CYCLE))"},
		// A computed column, and the four spellings one node takes.
		{"computed, PostgreSQL", "postgres", "postgres",
			"CREATE TABLE t (a INT GENERATED ALWAYS AS (1 + 2) STORED)",
			"CREATE TABLE t (a INT GENERATED ALWAYS AS (1 + 2) STORED)"},
		{"computed, neutral", "postgres", "",
			"CREATE TABLE t (a INT GENERATED ALWAYS AS (1 + 2) STORED)",
			"CREATE TABLE t (a INT AS 1 + 2)"},
		{"computed, Databricks", "postgres", "databricks",
			"CREATE TABLE t (a INT GENERATED ALWAYS AS (1 + 2) STORED)",
			"CREATE TABLE t (a INT GENERATED ALWAYS AS (1 + 2))"},
		// T-SQL gives a computed column no declared type at all.
		{"computed, T-SQL drops the type", "postgres", "tsql",
			"CREATE TABLE t (a INT GENERATED ALWAYS AS (1 + 2) STORED)",
			"CREATE TABLE t (a AS 1 + 2)"},
		{"unstored, Databricks", "databricks", "databricks",
			"CREATE TABLE t (a INT GENERATED ALWAYS AS (1 + 2))",
			"CREATE TABLE t (a INT GENERATED ALWAYS AS (1 + 2))"},
		{"unstored, elsewhere", "postgres", "postgres",
			"CREATE TABLE t (a INT GENERATED ALWAYS AS (1 + 2))",
			"CREATE TABLE t (a INT GENERATED ALWAYS AS (1 + 2))"},
		// Databricks supports only BIGINT identity columns, and the reference
		// widens the declared type to match.
		{"widened to BIGINT", "", "databricks",
			"CREATE TABLE t (x INT GENERATED ALWAYS AS IDENTITY)",
			"CREATE TABLE t (x BIGINT GENERATED ALWAYS AS IDENTITY)"},
		// A condition on one column rather than on the table.
		{"a column check", "postgres", "postgres",
			"CREATE TABLE t (price DECIMAL CHECK (price > 0))",
			"CREATE TABLE t (price DECIMAL CHECK (price > 0))"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, tc.read)
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			got, err := Generate(e, tc.write)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
	// T-SQL has no such constraint and rewrites every identity column into
	// `IDENTITY(start, increment)`, which drops CYCLE and ON NULL with it.
	for _, sql := range []string{
		"CREATE TABLE t (x BIGINT GENERATED ALWAYS AS IDENTITY (CYCLE))",
		"CREATE TABLE t (x INT GENERATED BY DEFAULT ON NULL AS IDENTITY)",
	} {
		e, err := ParseOne(sql, "")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", sql, err)
		}
		if got, err := Generate(e, "tsql"); err == nil {
			t.Errorf("%q wrote %q for T-SQL, which writes IDENTITY(n, m)", sql, got)
		}
	}
}

// A WITH may stand in front of a statement that is not a query at all, and
// the guard above this port has to see through it: `WITH a AS (SELECT * FROM
// b) UPDATE a SET c = 1` reads from b and writes through a.
//
// The clause is written first and assigned LAST, which is where the reference
// puts it however early it appeared.
func TestACTEBeforeAWrite(t *testing.T) {
	for _, tc := range []struct{ dialect, sql, want string }{
		{"", "WITH a AS (SELECT * FROM b) UPDATE a SET col = 1",
			"WITH a AS (SELECT * FROM b) UPDATE a SET col = 1"},
		{"", "WITH a AS (SELECT * FROM b) DELETE FROM a",
			"WITH a AS (SELECT * FROM b) DELETE FROM a"},
		{"", "WITH a AS (SELECT 1) INSERT INTO b SELECT * FROM a",
			"WITH a AS (SELECT 1) INSERT INTO b SELECT * FROM a"},
		{"", "WITH a AS (SELECT * FROM b) CREATE TABLE b AS SELECT * FROM a",
			"WITH a AS (SELECT * FROM b) CREATE TABLE b AS SELECT * FROM a"},
		// And the parentheses around a CREATE's query are KEPT: a Subquery
		// where a bare one holds the Select itself.
		{"", "CREATE TABLE t1 AS (SELECT c FROM t2)", "CREATE TABLE t1 AS (SELECT c FROM t2)"},
		{"", "CREATE TABLE t1 AS SELECT c FROM t2", "CREATE TABLE t1 AS SELECT c FROM t2"},
		{"", "CREATE TABLE z AS (WITH cte AS (SELECT 1) SELECT * FROM cte)",
			"CREATE TABLE z AS (WITH cte AS (SELECT 1) SELECT * FROM cte)"},
	} {
		e, err := ParseOne(tc.sql, tc.dialect)
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		if !IsWrite(e) {
			t.Errorf("IsWrite(%q) = false; it changes something", tc.sql)
		}
		got, err := Generate(e, tc.dialect)
		if err != nil {
			t.Fatalf("Generate(%q): %v", tc.sql, err)
		}
		if got != tc.want {
			t.Errorf("%q wrote %q, want %q", tc.sql, got, tc.want)
		}
	}
	// The CTE's own query is in the tree, which is the point: a guard that
	// looked only at the target would not see what the statement reads.
	e, err := ParseOne("WITH a AS (SELECT * FROM secrets) UPDATE t SET c = 1", "")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if len(e.FindAll("Select")) != 1 {
		t.Error("the CTE's query is not in the tree")
	}
	if _, err := ParseOne("CREATE TABLE t AS (SELECT 1", ""); err == nil {
		t.Error("an unclosed query was read")
	}
}

// An index over a table's columns. The name is OPTIONAL -- PostgreSQL lets
// the server choose one -- and each column is an ORDERED member, whether or
// not it says anything about order.
func TestCreateIndex(t *testing.T) {
	for _, tc := range []struct{ name, dialect, sql, want string }{
		{"plain", "", "CREATE INDEX abc ON t(a)", "CREATE INDEX abc ON t(a)"},
		{"quoted", "", `CREATE INDEX "abc" ON t(a)`, `CREATE INDEX "abc" ON t(a)`},
		{"several columns", "", "CREATE INDEX abc ON t(a, b, b)", "CREATE INDEX abc ON t(a, b, b)"},
		{"unique", "", "CREATE UNIQUE INDEX abc ON t(a, b)", "CREATE UNIQUE INDEX abc ON t(a, b)"},
		{"guarded", "", "CREATE UNIQUE INDEX IF NOT EXISTS my_idx ON tbl(a, b)",
			"CREATE UNIQUE INDEX IF NOT EXISTS my_idx ON tbl(a, b)"},
		{"where the nulls go", "", "CREATE INDEX abc ON t(a NULLS LAST)",
			"CREATE INDEX abc ON t(a NULLS LAST)"},
		{"without blocking", "postgres", "CREATE INDEX CONCURRENTLY ix ON tbl(id)",
			"CREATE INDEX CONCURRENTLY ix ON tbl(id)"},
		// PostgreSQL lets the server name it.
		{"unnamed", "postgres", "CREATE INDEX IF NOT EXISTS ON t(c)",
			"CREATE INDEX IF NOT EXISTS ON t(c)"},
		// A quoted name that spells an option IS a name.
		{"a name that spells an option", "", `CREATE INDEX "concurrently" ON t(x)`,
			`CREATE INDEX "concurrently" ON t(x)`},
		// Databricks puts the word TABLE between the index and its table.
		{"on TABLE", "databricks", "CREATE INDEX abc ON t(a)", "CREATE INDEX abc ON TABLE t(a)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, tc.dialect)
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			if !IsWrite(e) {
				t.Errorf("IsWrite(%q) = false; it makes something", tc.sql)
			}
			got, err := Generate(e, tc.dialect)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
	// T-SQL has no `IF NOT EXISTS` on an index and rewrites the statement
	// into a conditional EXEC over sys.indexes.
	e, err := ParseOne("CREATE INDEX IF NOT EXISTS i ON t(a)", "")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if got, err := Generate(e, "tsql"); err == nil {
		t.Errorf("wrote %q for T-SQL, which writes a conditional EXEC", got)
	}
	for _, sql := range []string{
		"CREATE INDEX abc ON t USING GIST(a)",
		"CREATE INDEX abc ON t(a) WHERE a > 1",
		"CREATE INDEX abc ON t",
		"CREATE INDEX abc ON t(a",
		"CREATE TEMPORARY INDEX abc ON t(a)",
	} {
		if _, err := ParseOne(sql, ""); err == nil {
			t.Errorf("ParseOne(%q) was read; it should be refused", sql)
		}
	}
}

// The string classes the tokenizer already tells apart. Each is a node of its
// own rather than a Literal with a flag, because what a dialect WRITES for one
// has nothing to do with what it writes for another: `0x1F` is `x'1F'` in
// PostgreSQL, `0x1F` in T-SQL and `UNHEX('1F')` in DuckDB.
func TestQuotedStringClasses(t *testing.T) {
	for _, tc := range []struct{ name, dialect, sql, want string }{
		{"a dollar-quoted string", "duckdb", "SELECT $$foo$$", "SELECT 'foo'"},
		{"a tagged one", "postgres", "SELECT $tag$a b$tag$", "SELECT 'a b'"},
		{"a raw string", "databricks", `SELECT r"a\nb"`, `SELECT 'a\\nb'`},
		{"a byte string", "postgres", `SELECT e'foo bar'`, `SELECT e'foo bar'`},
		{"a unicode string", "postgres", `SELECT U&'a b'`, `SELECT U&'a b'`},
		{"a hex literal, PostgreSQL", "postgres", "SELECT 0x1F", "SELECT x'1F'"},
		{"a hex literal, T-SQL", "tsql", "SELECT 0x1F", "SELECT 0x1F"},
		{"a hex literal, Databricks", "databricks", "SELECT 0xFF", `SELECT X'FF'`},
		// DuckDB does not read `0xFF` as a hex literal at all -- it reads a
		// zero and an alias -- so the UNHEX spelling is reached by writing a
		// hex literal read elsewhere.
		{"a hex literal, DuckDB", "duckdb", "SELECT 0 AS xFF", "SELECT 0 AS xFF"},
		// A quote in the body is escaped the way this dialect escapes one,
		// because the spelling substitutes the body VERBATIM.
		{"a quote inside", "postgres", `SELECT U&'a''b''c'`, `SELECT U&'a''b''c'`},
		// A byte string goes further: a control character comes back as the
		// two characters that spell it.
		{"a tab inside", "postgres", `SELECT E'a\tb'`, `SELECT e'a\tb'`},
		{"a newline inside", "postgres", `SELECT E'a\nb'`, `SELECT e'a\nb'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, tc.dialect)
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			got, err := Generate(e, tc.dialect)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
	// The neutral dialect writes a byte string as `''` and a hex literal not
	// at all, so both are refused rather than written empty.
	for _, tc := range []struct{ read, sql string }{
		{"postgres", `SELECT e'foo'`},
		{"postgres", "SELECT 0x1F"},
	} {
		e, err := ParseOne(tc.sql, tc.read)
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		if got, err := Generate(e, ""); err == nil {
			t.Errorf("%q wrote %q for the neutral dialect, which loses the value", tc.sql, got)
		}
	}
	// A hex literal read in PostgreSQL becomes a call when written for DuckDB.
	if e, err := ParseOne("SELECT 0xFF", "postgres"); err != nil {
		t.Fatalf("ParseOne: %v", err)
	} else if got, _ := Generate(e, "duckdb"); got != `SELECT UNHEX('FF')` {
		t.Errorf("got %q", got)
	}
	if escapeControlCharacters("a\a\b\f\v\rb") != `a\a\b\f\v\rb` {
		t.Errorf("control characters: %q", escapeControlCharacters("a\a\b\f\v\rb"))
	}
}

// DuckDB lets the FROM come FIRST, with the projections after it or left out
// entirely: `FROM t` means `SELECT * FROM t`. Every dialect here reads it, and
// the tree is an ordinary Select -- with one difference that shows only in
// this order: a comma join hangs off the TABLE rather than off the query.
func TestAQueryThatStartsWithFrom(t *testing.T) {
	for _, tc := range []struct{ dialect, sql, want string }{
		{"duckdb", "FROM tbl", "SELECT * FROM tbl"},
		{"", "FROM tbl", "SELECT * FROM tbl"},
		{"duckdb", "FROM x SELECT x", "SELECT x FROM x"},
		{"duckdb", "FROM t1, t2 SELECT *", "SELECT * FROM t1, t2"},
		{"duckdb", "FROM (FROM tbl)", "SELECT * FROM (SELECT * FROM tbl)"},
		{"duckdb", "FROM x SELECT x UNION SELECT 1", "SELECT x FROM x UNION SELECT 1"},
		{"duckdb", "WITH t AS (SELECT 1) FROM t", "WITH t AS (SELECT 1) SELECT * FROM t"},
		{"duckdb", "FROM t SELECT DISTINCT x", "SELECT DISTINCT x FROM t"},
		{"duckdb", "FROM t WHERE x = 1", "SELECT * FROM t WHERE x = 1"},
	} {
		e, err := ParseOne(tc.sql, tc.dialect)
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		got, err := Generate(e, tc.dialect)
		if err != nil {
			t.Fatalf("Generate(%q): %v", tc.sql, err)
		}
		if got != tc.want {
			t.Errorf("%q wrote %q, want %q", tc.sql, got, tc.want)
		}
	}
	// The join hangs off the table here and off the query when the SELECT
	// comes first -- the reference makes that distinction, so the port does.
	first, err := ParseOne("FROM t1, t2 SELECT *", "duckdb")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if _, onQuery := first.Args["joins"]; onQuery {
		t.Error("the comma join hangs off the query; it belongs to the table")
	}
	usual, err := ParseOne("SELECT * FROM t1, t2", "duckdb")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if _, onQuery := usual.Args["joins"]; !onQuery {
		t.Error("the comma join does not hang off the query, where it belongs")
	}
	if _, err := ParseOne("FROM t SELECT DISTINCT ON (a) b", "duckdb"); err == nil {
		t.Error("DISTINCT ON was read")
	}
}

// Three small facts that only show at the edges of a query.
func TestOffsetRowsAndTypedLiterals(t *testing.T) {
	for _, tc := range []struct{ name, read, write, sql, want string }{
		// `OFFSET 2 ROWS` and `OFFSET 2` are the same tree; only T-SQL writes
		// the word, so it is dropped on the way in and put back per dialect.
		{"offset rows", "tsql", "tsql", "SELECT * FROM t ORDER BY 1 OFFSET 2 ROWS",
			"SELECT * FROM t ORDER BY 1 OFFSET 2 ROWS"},
		{"offset rows, elsewhere", "tsql", "postgres", "SELECT * FROM t ORDER BY 1 OFFSET 2 ROWS",
			"SELECT * FROM t ORDER BY 1 NULLS FIRST OFFSET 2"},
		{"and back again", "postgres", "tsql", "SELECT * FROM t ORDER BY 1 OFFSET 2",
			"SELECT * FROM t ORDER BY 1 OFFSET 2 ROWS"},
		// A FETCH shares the LIMIT slot but is not one: T-SQL writes a limit
		// as TOP and a fetch where it stands.
		{"a fetch is not a top", "tsql", "tsql",
			"SELECT * FROM taxi ORDER BY 1 OFFSET 0 ROWS FETCH NEXT 3 ROWS ONLY",
			"SELECT * FROM taxi ORDER BY 1 OFFSET 0 ROWS FETCH NEXT 3 ROWS ONLY"},
		// A TYPE in front of a string is a typed literal, which the reference
		// records as an ordinary CAST.
		{"a typed literal", "postgres", "postgres", "INET '127.0.0.1/32'",
			"CAST('127.0.0.1/32' AS INET)"},
		{"a timestamp literal", "postgres", "postgres",
			"SELECT TIMESTAMP '2020-01-01' + INTERVAL '500 us'",
			"SELECT CAST('2020-01-01' AS TIMESTAMP) + INTERVAL '500 MICROSECOND'"},
		// The word binds LOOSER than the cast operator, so this is a
		// timestamp OF a date and not the other way round.
		{"looser than a cast", "databricks", "databricks",
			"SELECT TIMESTAMP '2025-04-29 18.47.18'::DATE",
			"SELECT CAST(CAST('2025-04-29 18.47.18' AS DATE) AS TIMESTAMP)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, tc.read)
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			got, err := Generate(e, tc.write)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
	// The short spellings of an interval unit are NAMES for the long ones,
	// and which spellings normalise is probed rather than assumed: `usec` is
	// in the reference's own table and comes back unchanged.
	for _, tc := range []struct{ sql, want string }{
		{"SELECT INTERVAL '1 us'", "MICROSECOND"},
		{"SELECT INTERVAL '1' us", "MICROSECOND"},
		{"SELECT INTERVAL '1 ms'", "MILLISECOND"},
		{"SELECT INTERVAL '1 usec'", "USEC"},
		{"SELECT INTERVAL '1 hrs'", "HRS"},
		{"SELECT INTERVAL '1 day'", "DAY"},
	} {
		e, err := ParseOne(tc.sql, "postgres")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		unit, _ := e.Args["expressions"].([]*Expression)
		if len(unit) != 1 {
			t.Fatalf("%q: no projection", tc.sql)
		}
		got, _ := unit[0].Args["unit"].(*Expression)
		if got == nil || got.Name() != tc.want {
			t.Errorf("%q recorded %v, want %s", tc.sql, got, tc.want)
		}
	}
	// A type whose parentheses swallow the string has nothing left to be a
	// literal of.
	if _, err := ParseOne("SELECT VARCHAR('x')", "postgres"); err == nil {
		t.Log("VARCHAR('x') is read as a call, which is what it is")
	}
}

// T-SQL's locking hints: advice to the engine about HOW to read a table,
// written after the alias.
//
// Every other dialect drops them, and the port drops them too rather than
// refusing -- unlike the constraints it refuses to lose. A hint is not part of
// what the query returns, and losing one only ever makes the read stricter.
func TestTableHints(t *testing.T) {
	for _, tc := range []struct{ name, write, sql, want string }{
		{"one hint", "tsql", "SELECT x FROM a WITH (NOLOCK)", "SELECT x FROM a WITH (NOLOCK)"},
		{"after the alias", "tsql", "SELECT * FROM t AS b WITH (NOLOCK)",
			"SELECT * FROM t AS b WITH (NOLOCK)"},
		{"several", "tsql", "SELECT * FROM t WITH (TABLOCK, INDEX(myindex))",
			"SELECT * FROM t WITH (TABLOCK, INDEX(myindex))"},
		{"on an update", "tsql", "UPDATE start WITH (ROWLOCK) SET a = 1",
			"UPDATE start WITH (ROWLOCK) SET a = 1"},
		{"on a delete", "tsql", "DELETE FROM start WITH (ROWLOCK)",
			"DELETE FROM start WITH (ROWLOCK)"},
		// Elsewhere the hint is dropped, as the reference drops it.
		{"dropped", "postgres", "SELECT x FROM a WITH (NOLOCK)", "SELECT x FROM a"},
		{"dropped on an update", "duckdb", "UPDATE start WITH (ROWLOCK) SET a = 1",
			"UPDATE start SET a = 1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, "tsql")
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			got, err := Generate(e, tc.write)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
	if _, err := ParseOne("SELECT x FROM a WITH (NOLOCK", "tsql"); err == nil {
		t.Error("an unclosed hint was read")
	}
}

// APPLY runs a table function once per row, and the alias that names what it
// produced belongs to the LATERAL rather than to the call inside it. It may
// name the columns too, and it may be written with or without AS.
func TestApplyWithAnAlias(t *testing.T) {
	for _, tc := range []struct{ sql, want string }{
		// A name with no builder behind it is written upper-cased, which is
		// the reference's rule for one and not this test's business.
		{"SELECT t.x, y.z FROM x CROSS APPLY tvfTest(t.x) y(z)",
			"SELECT t.x, y.z FROM x CROSS APPLY TVFTEST(t.x) AS y(z)"},
		{"SELECT t.x, y.z FROM x OUTER APPLY tvfTest(t.x) AS y(z)",
			"SELECT t.x, y.z FROM x OUTER APPLY TVFTEST(t.x) AS y(z)"},
		{"SELECT t.x FROM x CROSS APPLY a.b.tvfTest(t.x) y",
			// A QUALIFIED call keeps its case, where a bare one does not.
			"SELECT t.x FROM x CROSS APPLY a.b.tvfTest(t.x) AS y"},
		// And with no alias at all, which was the only form read before.
		{"SELECT t.x FROM x CROSS APPLY tvfTest(t.x)",
			"SELECT t.x FROM x CROSS APPLY TVFTEST(t.x)"},
	} {
		e, err := ParseOne(tc.sql, "tsql")
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
	}
}

// The statements that are barely statements: a table emptied, a database
// chosen, a transaction opened or closed. None is a query, and none is
// read-only either -- a guard has to see them.
func TestTruncateUseAndTransactions(t *testing.T) {
	for _, tc := range []struct{ name, read, write, sql, want string }{
		{"truncate", "", "", "TRUNCATE TABLE t", "TRUNCATE TABLE t"},
		{"if it is there", "", "", "TRUNCATE TABLE IF EXISTS t", "TRUNCATE TABLE IF EXISTS t"},
		{"cascading", "postgres", "postgres", "TRUNCATE TABLE t1 CASCADE", "TRUNCATE TABLE t1 CASCADE"},
		{"restarting", "postgres", "postgres", "TRUNCATE TABLE t1 RESTART IDENTITY",
			"TRUNCATE TABLE t1 RESTART IDENTITY"},
		// ONLY is on the TABLE; the star that means the opposite is the
		// default and is written nowhere.
		{"only these tables", "postgres", "postgres",
			"TRUNCATE TABLE ONLY t1, t2*, ONLY t3 RESTART IDENTITY CASCADE",
			"TRUNCATE TABLE ONLY t1, t2, ONLY t3 RESTART IDENTITY CASCADE"},
		{"use", "", "", "USE db", "USE db"},
		{"use a schema", "", "", "USE SCHEMA x.y", "USE SCHEMA x.y"},
		// A quoted word that spells a kind is a NAME.
		{"use a name that spells a kind", "", "", `USE "role"`, `USE "role"`},
		{"begin", "", "", "BEGIN", "BEGIN"},
		{"commit", "", "", "COMMIT", "COMMIT"},
		{"rollback", "", "", "ROLLBACK", "ROLLBACK"},
		{"rollback to a savepoint", "", "", "ROLLBACK TO b", "ROLLBACK TO b"},
		// The word TRANSACTION says nothing the verb does not, and only
		// T-SQL writes it back.
		{"the word is optional", "", "", "COMMIT WORK", "COMMIT"},
		{"and T-SQL writes it", "tsql", "tsql", "COMMIT TRAN", "COMMIT TRANSACTION"},
		{"begin one, T-SQL", "tsql", "tsql", "BEGIN TRANSACTION", "BEGIN TRANSACTION"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, tc.read)
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			if !IsWrite(e) {
				t.Errorf("IsWrite(%q) = false; it is not a read", tc.sql)
			}
			got, err := Generate(e, tc.write)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
	// T-SQL's BEGIN opens a BLOCK, and it takes the word TRANSACTION to mean
	// the other thing. The reference keeps the block form as raw text.
	if _, err := ParseOne("BEGIN", "tsql"); err == nil {
		t.Error("a T-SQL BEGIN was read as a transaction")
	}
	// And T-SQL DROPS the name a transaction carries, so a rollback TO a
	// savepoint would roll back everything -- a different action.
	for _, sql := range []string{"ROLLBACK TO b", "COMMIT TRANSACTION n"} {
		e, err := ParseOne(sql, "")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", sql, err)
		}
		if got, err := Generate(e, "tsql"); err == nil {
			t.Errorf("%q wrote %q for T-SQL, which writes the name away", sql, got)
		}
	}
	for _, sql := range []string{
		"TRUNCATE t",
		"TRUNCATE TABLE t1 PARTITION(age = 10)",
		"USE",
	} {
		if _, err := ParseOne(sql, ""); err == nil {
			t.Errorf("ParseOne(%q) was read; it should be refused", sql)
		}
	}
}

// A permission handed over, and the REVOKE that takes it back. Neither is a
// query, and what they change is exactly what a caller is allowed to do next.
func TestGrantAndRevoke(t *testing.T) {
	for _, tc := range []struct{ name, sql, want string }{
		{"one privilege", "GRANT SELECT ON TABLE tbl TO user", "GRANT SELECT ON TABLE tbl TO user"},
		{"several", "GRANT SELECT, INSERT ON FUNCTION tbl TO user",
			"GRANT SELECT, INSERT ON FUNCTION tbl TO user"},
		{"no kind word", "GRANT SELECT ON orders TO ROLE PUBLIC",
			"GRANT SELECT ON orders TO ROLE PUBLIC"},
		{"with the right to pass it on", "GRANT SELECT ON TABLE t TO user WITH GRANT OPTION",
			"GRANT SELECT ON TABLE t TO user WITH GRANT OPTION"},
		{"revoking", "REVOKE SELECT ON TABLE tbl FROM user", "REVOKE SELECT ON TABLE tbl FROM user"},
		{"all of them", "REVOKE ALL PRIVILEGES ON TABLE forecasts FROM finance",
			"REVOKE ALL PRIVILEGES ON TABLE forecasts FROM finance"},
		// On a REVOKE the option comes FIRST and takes away the right to pass
		// the privilege on rather than the privilege itself.
		{"only the right to pass it on", "REVOKE GRANT OPTION FOR SELECT ON nation FROM alice",
			"REVOKE GRANT OPTION FOR SELECT ON nation FROM alice"},
		// `user` here NAMES a principal; reading it as the word USER took
		// RESTRICT for the name and the restriction with it.
		{"a principal called user", "REVOKE INSERT ON TABLE orders FROM user RESTRICT",
			"REVOKE INSERT ON TABLE orders FROM user RESTRICT"},
		{"a quoted principal", `GRANT SELECT ON TABLE t TO "role"`,
			`GRANT SELECT ON TABLE t TO "role"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, "")
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			if !IsWrite(e) {
				t.Errorf("IsWrite(%q) = false; it changes what a caller may do", tc.sql)
			}
			got, err := Generate(e, "")
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
	for _, sql := range []string{
		// `AS role` says WHO is doing the granting, and the reference gives
		// up on it and keeps the raw text.
		"GRANT EXECUTE ON TestProc TO User2 AS TesterRole",
		"GRANT SELECT ON TABLE t",
		"REVOKE SELECT ON TABLE t",
	} {
		if _, err := ParseOne(sql, ""); err == nil {
			t.Errorf("ParseOne(%q) was read; it should be refused", sql)
		}
	}
}

// A note left on a table, a view or a column. The name is a COLUMN where the
// kind says so and a table-shaped name otherwise -- the same words, two
// nodes, and the kind is what decides.
func TestCommentOn(t *testing.T) {
	for _, tc := range []struct{ sql, want string }{
		{"COMMENT ON TABLE my_schema.my_table IS 'Employee Information'",
			"COMMENT ON TABLE my_schema.my_table IS 'Employee Information'"},
		{"COMMENT ON VIEW foo.bat IS 'x'", "COMMENT ON VIEW foo.bat IS 'x'"},
		{"COMMENT ON COLUMN my_schema.my_table.my_column IS 'Employee ID number'",
			"COMMENT ON COLUMN my_schema.my_table.my_column IS 'Employee ID number'"},
		{"COMMENT ON TYPE mood IS 'x'", "COMMENT ON TYPE mood IS 'x'"},
		{"COMMENT ON SEQUENCE public.seq IS 'x'", "COMMENT ON SEQUENCE public.seq IS 'x'"},
	} {
		e, err := ParseOne(tc.sql, "")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		if !IsWrite(e) {
			t.Errorf("IsWrite(%q) = false; it changes the catalogue", tc.sql)
		}
		got, err := Generate(e, "")
		if err != nil {
			t.Fatalf("Generate(%q): %v", tc.sql, err)
		}
		if got != tc.want {
			t.Errorf("%q wrote %q, want %q", tc.sql, got, tc.want)
		}
	}
	for _, sql := range []string{
		// A PROCEDURE is named with its SIGNATURE, which is a third shape.
		"COMMENT ON PROCEDURE my_proc(integer, integer) IS 'Runs a report'",
		// And `IS NULL` removes the comment, which the reference does not
		// read either.
		"COMMENT ON TABLE t IS NULL",
		"COMMENT ON TABLE t",
		"COMMENT TABLE t IS 'x'",
	} {
		if _, err := ParseOne(sql, ""); err == nil {
			t.Errorf("ParseOne(%q) was read; it should be refused", sql)
		}
	}
}

// SET changes a setting, a session variable, or a global one -- and which of
// those it is depends on a word that not every dialect has.
func TestSetStatement(t *testing.T) {
	for _, tc := range []struct{ name, dialect, sql, want string }{
		{"a setting", "", "SET x = 1", "SET x = 1"},
		{"a string value", "duckdb", "SET memory_limit = '10GB'", "SET memory_limit = '10GB'"},
		// A bare word on the right refers to nothing, so it is a Var rather
		// than a column.
		{"a bare value", "", "SET variable = value", "SET variable = value"},
		{"a scope", "", "SET GLOBAL variable = value", "SET GLOBAL variable = value"},
		{"a session scope", "duckdb", "SET SESSION default_collation = 'nocase'",
			"SET SESSION default_collation = 'nocase'"},
		// `VAR` and `VARIABLE` are the same word; the reference records the
		// long one.
		{"a variable", "databricks", "SET VAR v = 5", "SET VARIABLE v = 5"},
		{"TO is the same as =", "duckdb", "SET VARIABLE my_var TO 30",
			"SET VARIABLE my_var = 30"},
		{"several at once", "databricks", "SET VARIABLE v1 = 1, v2 = '2'",
			"SET VARIABLE v1 = 1, v2 = '2'"},
		{"from a query", "duckdb", "SET VARIABLE location_map = (SELECT foo FROM bar)",
			"SET VARIABLE location_map = (SELECT foo FROM bar)"},
		{"several from one query", "databricks", "SET VARIABLE (v1, v2) = (SELECT 1, 2)",
			"SET VARIABLE (v1, v2) = (SELECT 1, 2)"},
		// T-SQL writes nothing between a setting and its value, and an equals
		// sign between a VARIABLE and its value -- two statements wearing the
		// same word.
		{"no sign at all", "tsql", "SET XACT_ABORT ON", "SET XACT_ABORT ON"},
		{"a T-SQL variable", "tsql", "SET @count = (SELECT COUNT(1) FROM x)",
			"SET @count = (SELECT COUNT(1) FROM x)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, tc.dialect)
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			if !IsWrite(e) {
				t.Errorf("IsWrite(%q) = false; it changes the session", tc.sql)
			}
			got, err := Generate(e, tc.dialect)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
	// A scope word a dialect does not have is refused rather than dropped: a
	// global setting applied to a session is a different effect.
	for _, sql := range []string{"SET GLOBAL variable = value", "SET LOCAL variable = value"} {
		e, err := ParseOne(sql, "")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", sql, err)
		}
		if got, err := Generate(e, "tsql"); err == nil {
			t.Errorf("%q wrote %q for T-SQL, which has no such scope", sql, got)
		}
	}
	if _, err := ParseOne("SET", ""); err == nil {
		t.Error("a bare SET was read")
	}
}

// PRAGMA reads or changes an engine setting, and what follows the word is an
// ordinary EXPRESSION -- a name, a qualified call, or an equality -- rather
// than a grammar of its own.
func TestPragma(t *testing.T) {
	for _, sql := range []string{
		"PRAGMA quick_check",
		"PRAGMA schema.quick_check",
		"PRAGMA QUICK_CHECK(0)",
		"PRAGMA schema.QUICK_CHECK(0)",
		"PRAGMA schema.synchronous = FULL",
	} {
		e, err := ParseOne(sql, "")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", sql, err)
		}
		if !IsWrite(e) {
			t.Errorf("IsWrite(%q) = false; it reaches past the query", sql)
		}
		got, err := Generate(e, "")
		if err != nil {
			t.Fatalf("Generate(%q): %v", sql, err)
		}
		if got != sql {
			t.Errorf("%q wrote %q", sql, got)
		}
	}
	if _, err := ParseOne("PRAGMA", ""); err == nil {
		t.Error("a bare PRAGMA was read")
	}
}

// `CREATE TABLE a` is a whole statement: it makes the name and nothing else.
//
// And T-SQL writes only two parts of a three-part name here, dropping the
// catalog -- which names a different object. The same rule turns a DROP away,
// and it belongs to the dialect's naming rather than to either statement.
func TestABareCreate(t *testing.T) {
	for _, tc := range []struct{ dialect, sql string }{
		{"", "CREATE TABLE a"},
		{"postgres", "CREATE TABLE a"},
		{"duckdb", "CREATE TABLE x"},
		{"tsql", "CREATE TABLE a"},
		{"databricks", "CREATE TABLE a"},
		{"", "CREATE TABLE a.b.c"},
	} {
		e, err := ParseOne(tc.sql, tc.dialect)
		if err != nil {
			t.Fatalf("ParseOne(%q, %s): %v", tc.sql, tc.dialect, err)
		}
		if !IsWrite(e) {
			t.Errorf("IsWrite(%q) = false; it makes something", tc.sql)
		}
		got, err := Generate(e, tc.dialect)
		if err != nil {
			t.Fatalf("Generate(%q): %v", tc.sql, err)
		}
		if got != tc.sql {
			t.Errorf("%q wrote %q", tc.sql, got)
		}
	}
	for _, sql := range []string{"CREATE TABLE a.b.c", "CREATE TABLE a.b.c (x INT)"} {
		e, err := ParseOne(sql, "")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", sql, err)
		}
		if got, err := Generate(e, "tsql"); err == nil {
			t.Errorf("%q wrote %q for T-SQL, which drops the catalog", sql, got)
		}
	}
}

// The refusals in the statement readers that no corpus statement reaches. One
// malformed input each, so the reader that turns it away is run rather than
// shipped on trust.
func TestStatementRefusalsAreReached(t *testing.T) {
	for _, tc := range []struct{ dialect, sql string }{
		{"", "USE db extra"},
		{"", "COMMIT TRANSACTION n extra"},
		{"", "COMMENT ON"},
		{"", "COMMENT ON TABLE t IS 'x' extra"},
		{"", "PRAGMA x extra"},
		{"", "SET x = 1 extra"},
		{"", "SET x ="},
		{"", "GRANT SELECT TO user"},
		{"", "GRANT"},
		{"", "CREATE INDEX abc t(a)"},
		{"", "CREATE TABLE z (a INT, FOREIGN KEY (a) REFERENCES p (b) NOT NULL)"},
		{"", "CREATE TABLE z (a INT GENERATED ALWAYS AS (1 + 2)"},
		{"", "CREATE TABLE z (a INT, CHECK (a > 0"},
		{"", "CREATE FUNCTION f(a INT"},
	} {
		if _, err := ParseOne(tc.sql, tc.dialect); err == nil {
			t.Errorf("ParseOne(%q) was read; it should be refused", tc.sql)
		}
	}
	// A key column may be written ascending or descending, and T-SQL is the
	// dialect that reads an index's order here.
	if e, err := ParseOne("CREATE INDEX i ON t(a DESC)", ""); err != nil {
		t.Fatalf("ParseOne: %v", err)
	} else if got, _ := Generate(e, ""); got != "CREATE INDEX i ON t(a DESC)" {
		t.Errorf("got %q", got)
	}
	if isBareWord("") || isBareWord("1a") || !isBareWord("a1") {
		t.Error("isBareWord disagrees with what a word is")
	}
}

// The sign-less SET is T-SQL's alone. Reading it everywhere let the port take
// `SET@0B` for a setting and write back SQL it could not read -- the
// generator fuzzer found 111 of those in a single run.
func TestSetNeedsASignExceptInTSQL(t *testing.T) {
	for _, d := range []string{"", "postgres", "duckdb", "databricks"} {
		if _, err := ParseOne("SET KEY VALUE", d); err == nil {
			t.Errorf("[%s] a sign-less SET was read", d)
		}
	}
	if _, err := ParseOne("SET KEY VALUE", "tsql"); err != nil {
		t.Errorf("T-SQL writes this form: %v", err)
	}
	// The parameter marker is the dialect's -- `@x` in T-SQL, `$x` in
	// PostgreSQL -- and the ordinary reader is asked which node it builds
	// rather than this statement guessing.
	for _, tc := range []struct{ dialect, sql, want string }{
		{"tsql", "SET @x = 1", "SET @x = 1"},
		{"postgres", "SET $x = 1", "SET $x = 1"},
		{"postgres", "SET $0 = 0", "SET $0 = 0"},
	} {
		e, err := ParseOne(tc.sql, tc.dialect)
		if err != nil {
			t.Fatalf("ParseOne(%q, %s): %v", tc.sql, tc.dialect, err)
		}
		if got, err := Generate(e, tc.dialect); err != nil || got != tc.want {
			t.Errorf("%q wrote %q (%v), want %q", tc.sql, got, err, tc.want)
		}
	}
	// And a PARAMETER's name is put back bare, so a name that is not a name
	// makes SQL nothing can read: PostgreSQL spells `@x` as `$x`, and a
	// dollar opens a quote that never closes.
	odd := New("Set",
		Arg{"expressions", []*Expression{New("SetItem",
			Arg{"this", New("EQ",
				Arg{"this", New("Parameter", Arg{"this", New("Var", Arg{"this", "a b"})})},
				Arg{"expression", New("Literal", Arg{"this", "1"}, Arg{"is_string", false})})})}},
		Arg{"unset", false}, Arg{"tag", false})
	if got, err := Generate(odd, "postgres"); err == nil {
		t.Errorf("wrote %q; `a b` is not a name", got)
	}
}

// What an INSERT hands back, and what it does when a row is already there.
//
// The conflict KEYS are ordered members -- the same shape an index keeps its
// columns in -- because the conflict is decided by an index and this names
// which one.
func TestInsertReturningAndConflict(t *testing.T) {
	for _, tc := range []struct{ name, dialect, sql, want string }{
		{"returning", "postgres", "INSERT INTO x VALUES (1, 'a', 2.0) RETURNING a, b",
			"INSERT INTO x VALUES (1, 'a', 2.0) RETURNING a, b"},
		{"returning everything", "postgres", "INSERT INTO x VALUES (1) RETURNING *",
			"INSERT INTO x VALUES (1) RETURNING *"},
		// T-SQL writes it in front of the query and calls it OUTPUT.
		{"output", "tsql", "INSERT INTO x (y) OUTPUT x.a, x.b SELECT * FROM z",
			"INSERT INTO x (y) OUTPUT x.a, x.b SELECT * FROM z"},
		{"do nothing", "postgres", "INSERT INTO x VALUES (1) ON CONFLICT(id) DO NOTHING",
			"INSERT INTO x VALUES (1) ON CONFLICT(id) DO NOTHING"},
		{"do update", "postgres", "INSERT INTO x VALUES (1) ON CONFLICT(id) DO UPDATE SET x.id = 1",
			"INSERT INTO x VALUES (1) ON CONFLICT(id) DO UPDATE SET x.id = 1"},
		{"a named constraint", "postgres",
			"INSERT INTO x VALUES (1) ON CONFLICT ON CONSTRAINT pkey DO NOTHING",
			"INSERT INTO x VALUES (1) ON CONFLICT ON CONSTRAINT pkey DO NOTHING"},
		// Two WHEREs, and they mean different things: the first picks which
		// index decides the conflict, the second which rows the update runs on.
		{"two wheres", "postgres",
			"INSERT INTO b (i) VALUES (1) ON CONFLICT(i) WHERE d IS NULL DO UPDATE SET t = 1 WHERE b.t < 1",
			"INSERT INTO b (i) VALUES (1) ON CONFLICT(i) WHERE d IS NULL DO UPDATE SET t = 1 WHERE b.t < 1"},
		// A key may be an EXPRESSION rather than a column: the index it names
		// is over the expression.
		{"a key that is an expression", "postgres",
			"INSERT INTO tbl (a, b) VALUES (1, 'x') ON CONFLICT(a, b || c) DO UPDATE SET b = excluded.b",
			"INSERT INTO tbl (a, b) VALUES (1, 'x') ON CONFLICT(a, b || c) DO UPDATE SET b = excluded.b"},
		{"both at once", "postgres",
			"INSERT INTO x VALUES (1) ON CONFLICT(id) DO NOTHING RETURNING *",
			"INSERT INTO x VALUES (1) ON CONFLICT(id) DO NOTHING RETURNING *"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, tc.dialect)
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			got, err := Generate(e, tc.dialect)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
	for _, sql := range []string{
		"INSERT INTO x VALUES (1) ON CONFLICT(id)",
		"INSERT INTO x VALUES (1) ON CONFLICT(id) DO UPDATE",
		"INSERT INTO x VALUES (1) ON CONFLICT(id) DO SOMETHING",
	} {
		if _, err := ParseOne(sql, "postgres"); err == nil {
			t.Errorf("ParseOne(%q) was read; it should be refused", sql)
		}
	}
}

// Which partition an INSERT writes, and whether the target has to be there
// already.
//
// The partition hangs off the TABLE and not off the INSERT -- the statement's
// own `partition` argument stays false. It is part of naming the target.
func TestInsertPartitionAndExists(t *testing.T) {
	for _, tc := range []struct{ name, write, sql, want string }{
		{"a partition", "", "INSERT OVERWRITE TABLE a.b PARTITION(ds) SELECT x FROM y",
			"INSERT OVERWRITE TABLE a.b PARTITION(ds) SELECT x FROM y"},
		{"several", "", "INSERT OVERWRITE TABLE a.b PARTITION(ds, hour) SELECT x FROM y",
			"INSERT OVERWRITE TABLE a.b PARTITION(ds, hour) SELECT x FROM y"},
		{"a named value", "", "INSERT OVERWRITE TABLE a.b PARTITION(ds = 'YYYY-MM-DD') SELECT x FROM y",
			"INSERT OVERWRITE TABLE a.b PARTITION(ds = 'YYYY-MM-DD') SELECT x FROM y"},
		{"and a column list", "", "INSERT INTO a.b PARTITION(DAY = '2024-04-14') (col1, col2) SELECT x FROM y",
			"INSERT INTO a.b PARTITION(DAY = '2024-04-14') (col1, col2) SELECT x FROM y"},
		// T-SQL wraps the clause in words of its own.
		{"T-SQL wraps it", "tsql", "INSERT OVERWRITE TABLE a.b PARTITION(ds) SELECT x FROM y",
			"INSERT OVERWRITE TABLE a.b WITH (PARTITIONS(ds)) SELECT x FROM y"},
		{"only if it is there", "", "INSERT OVERWRITE TABLE x IF EXISTS SELECT * FROM y",
			"INSERT OVERWRITE TABLE x IF EXISTS SELECT * FROM y"},
		{"into, if it is there", "", "INSERT INTO x.z IF EXISTS SELECT * FROM y",
			"INSERT INTO x.z IF EXISTS SELECT * FROM y"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, "")
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			got, err := Generate(e, tc.write)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
	// The partition is on the table, which is where a guard has to look for it.
	e, err := ParseOne("INSERT OVERWRITE TABLE a.b PARTITION(ds) SELECT 1", "")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if e.Args["partition"] != false {
		t.Errorf("the INSERT's own partition is %v; it belongs to the table", e.Args["partition"])
	}
	if len(e.FindAll("Partition")) != 1 {
		t.Error("no Partition on the table")
	}
	if _, err := ParseOne("INSERT OVERWRITE TABLE a.b PARTITION(ds SELECT 1", ""); err == nil {
		t.Error("an unclosed partition was read")
	}
}

// What an `ALTER TABLE ... SET` sets: a word, a tablespace, an access method,
// or a list of settings. Each lands in an argument of its own, because they
// are different things rather than different spellings.
func TestAlterTableSet(t *testing.T) {
	for _, tc := range []struct{ name, sql, want string }{
		{"logged", "ALTER TABLE t1 SET LOGGED", "ALTER TABLE t1 SET LOGGED"},
		{"unlogged", "ALTER TABLE t1 SET UNLOGGED", "ALTER TABLE t1 SET UNLOGGED"},
		{"without oids", "ALTER TABLE t1 SET WITHOUT OIDS", "ALTER TABLE t1 SET WITHOUT OIDS"},
		{"without cluster", "ALTER TABLE t1 SET WITHOUT CLUSTER", "ALTER TABLE t1 SET WITHOUT CLUSTER"},
		{"a tablespace", "ALTER TABLE t1 SET TABLESPACE tablespace",
			"ALTER TABLE t1 SET TABLESPACE tablespace"},
		{"an access method", "ALTER TABLE t1 SET ACCESS METHOD method",
			"ALTER TABLE t1 SET ACCESS METHOD method"},
		{"settings", "ALTER TABLE t1 SET (fillfactor = 5, autovacuum_enabled = TRUE)",
			"ALTER TABLE t1 SET (fillfactor = 5, autovacuum_enabled = TRUE)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, "postgres")
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			if !IsWrite(e) {
				t.Errorf("IsWrite(%q) = false; it changes the table", tc.sql)
			}
			got, err := Generate(e, "postgres")
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
	// Only PostgreSQL writes the words that say WHAT is set; everywhere else
	// the reference writes a bare `ALTER TABLE t SET`, which sets nothing at
	// all. The port refuses rather than writing that.
	for _, sql := range []string{"ALTER TABLE t1 SET LOGGED", "ALTER TABLE t1 SET TABLESPACE ts"} {
		e, err := ParseOne(sql, "postgres")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", sql, err)
		}
		for _, d := range []string{"", "duckdb", "databricks"} {
			if got, err := Generate(e, d); err == nil {
				t.Errorf("[%s] %q wrote %q, which sets nothing", d, sql, got)
			}
		}
	}
	// T-SQL reads the same parenthesised words as table PROPERTIES, each with
	// a class of its own, so the port refuses there rather than building an
	// equality the reference never makes.
	if _, err := ParseOne("ALTER TABLE tbl SET (SYSTEM_VERSIONING=OFF)", "tsql"); err == nil {
		t.Error("a T-SQL property list was read as settings")
	}
	if _, err := ParseOne("ALTER TABLE t1 SET SOMETHING", "postgres"); err == nil {
		t.Error("an unknown SET was read")
	}
}

// A fixed-size array is a type in every dialect where it is a COLUMN's type.
// Outside a column only the dialects that have them read `INT[3]` that way,
// which is why the position is recorded rather than asked of the type alone.
//
// The flag the port used to consult is the reference's own
// SUPPORTS_FIXED_SIZE_ARRAYS, and it is false for PostgreSQL -- which reads
// `INT[3]` as a type all the same, because the reference asks a different
// question inside a column list.
func TestSizedArrayTypes(t *testing.T) {
	for _, tc := range []struct{ dialect, sql, want string }{
		{"postgres", "CREATE TABLE t (col INT[3])", "CREATE TABLE t (col INT[3])"},
		{"postgres", "CREATE TABLE t (col INT[3][5])", "CREATE TABLE t (col INT[3][5])"},
		{"postgres", "CREATE TABLE t (col integer ARRAY)", "CREATE TABLE t (col INT[])"},
		{"postgres", "CREATE TABLE t (col integer ARRAY[3])", "CREATE TABLE t (col INT[3])"},
	} {
		e, err := ParseOne(tc.sql, tc.dialect)
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		got, err := Generate(e, tc.dialect)
		if err != nil {
			t.Fatalf("Generate(%q): %v", tc.sql, err)
		}
		if got != tc.want {
			t.Errorf("%q wrote %q, want %q", tc.sql, got, tc.want)
		}
	}
	// Outside a column, PostgreSQL reads the brackets as a subscript of the
	// cast rather than as a size -- the reference retreats there, and so does
	// the port.
	if e, err := ParseOne("SELECT CAST(x AS INT[3])", "postgres"); err == nil {
		t.Logf("read as %s", e.Class)
	}
}

// `WITH NO DATA` says the table is SHAPED by the query rather than filled
// from it -- the difference between a copy and an empty table.
func TestCreateTableWithData(t *testing.T) {
	for _, tc := range []struct{ sql, want string }{
		{"CREATE TABLE asd AS SELECT asd FROM asd WITH DATA",
			"CREATE TABLE asd AS SELECT asd FROM asd WITH DATA"},
		{"CREATE TABLE asd AS SELECT asd FROM asd WITH NO DATA",
			"CREATE TABLE asd AS SELECT asd FROM asd WITH NO DATA"},
		{"CREATE TABLE a.b AS SELECT 1 WITH DATA AND STATISTICS",
			"CREATE TABLE a.b AS SELECT 1 WITH DATA AND STATISTICS"},
		{"CREATE TABLE a.b AS SELECT 1 WITH NO DATA AND NO STATISTICS",
			"CREATE TABLE a.b AS SELECT 1 WITH NO DATA AND NO STATISTICS"},
	} {
		e, err := ParseOne(tc.sql, "")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		got, err := Generate(e, "")
		if err != nil {
			t.Fatalf("Generate(%q): %v", tc.sql, err)
		}
		if got != tc.want {
			t.Errorf("%q wrote %q, want %q", tc.sql, got, tc.want)
		}
	}
	// DuckDB and Databricks write the words nowhere, which would make a copy
	// where an empty table was asked for.
	e, err := ParseOne("CREATE TABLE t AS SELECT 1 WITH NO DATA", "")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	for _, d := range []string{"duckdb", "databricks"} {
		if got, err := Generate(e, d); err == nil {
			t.Errorf("[%s] wrote %q, which fills the table", d, got)
		}
	}
	if _, err := ParseOne("CREATE TABLE t AS SELECT 1 WITH DATA AND INDEXES", ""); err == nil {
		t.Error("WITH DATA AND INDEXES was read")
	}
}

// Where a newly added column goes among the ones already there, and the short
// spelling of an auto-numbering one.
func TestColumnPositionAndIdentity(t *testing.T) {
	for _, tc := range []struct{ name, read, write, sql, want string }{
		{"after another", "", "", "ALTER TABLE integers ADD COLUMN k INT AFTER m",
			"ALTER TABLE integers ADD COLUMN k INT AFTER m"},
		{"first", "", "", "ALTER TABLE integers ADD COLUMN k INT FIRST",
			"ALTER TABLE integers ADD COLUMN k INT FIRST"},
		// T-SQL's short spelling of an identity column is the same node the
		// long `GENERATED ... AS IDENTITY` makes, with its arguments read in
		// another order.
		{"a short identity", "tsql", "tsql",
			"CREATE TABLE tbl (id INTEGER NOT NULL IDENTITY(10, 1) PRIMARY KEY)",
			"CREATE TABLE tbl (id INTEGER NOT NULL IDENTITY(10, 1) PRIMARY KEY)"},
		{"and the long one", "tsql", "postgres",
			"CREATE TABLE tbl (id INTEGER IDENTITY(10, 1))",
			"CREATE TABLE tbl (id INT GENERATED BY DEFAULT AS IDENTITY (START WITH 10 INCREMENT BY 1))"},
		{"the other way round", "postgres", "tsql",
			"CREATE TABLE tbl (id INT GENERATED BY DEFAULT AS IDENTITY (START WITH 10 INCREMENT BY 1))",
			"CREATE TABLE tbl (id INTEGER IDENTITY(10, 1))"},
		// A bare IDENTITY is auto-numbering, which each dialect spells its
		// own way.
		{"bare, T-SQL", "tsql", "tsql", "CREATE TABLE tbl (id INTEGER IDENTITY PRIMARY KEY)",
			"CREATE TABLE tbl (id INTEGER IDENTITY PRIMARY KEY)"},
		{"bare, neutral", "tsql", "", "CREATE TABLE tbl (id INTEGER IDENTITY)",
			"CREATE TABLE tbl (id INT AUTO_INCREMENT)"},
		{"bare, PostgreSQL", "tsql", "postgres", "CREATE TABLE tbl (id INTEGER IDENTITY)",
			"CREATE TABLE tbl (id INT GENERATED BY DEFAULT AS IDENTITY NOT NULL)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, tc.read)
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			got, err := Generate(e, tc.write)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
	// The short form has nowhere to put CYCLE, ON NULL or ALWAYS, and DuckDB
	// writes an auto-numbering column nowhere at all.
	for _, tc := range []struct{ read, write, sql string }{
		{"", "tsql", "CREATE TABLE t (a INT GENERATED ALWAYS AS IDENTITY)"},
		{"", "tsql", "CREATE TABLE t (a INT GENERATED BY DEFAULT AS IDENTITY (CYCLE))"},
		{"", "tsql", "CREATE TABLE t (a INT GENERATED BY DEFAULT ON NULL AS IDENTITY)"},
		{"tsql", "duckdb", "CREATE TABLE t (a INT IDENTITY)"},
	} {
		e, err := ParseOne(tc.sql, tc.read)
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		if got, err := Generate(e, tc.write); err == nil {
			t.Errorf("[%s] %q wrote %q, which says less", tc.write, tc.sql, got)
		}
	}
}

// Rows written out where a table would go. The alias belongs to the VALUES
// rather than to anything wrapping it, and the parentheses are the writer's
// alone -- the reference keeps the same tree whether the source had them.
func TestValuesAsATable(t *testing.T) {
	for _, tc := range []struct{ name, read, write, sql, want string }{
		{"parenthesised", "duckdb", "duckdb",
			"SELECT col1, col2 FROM (VALUES (1, 2), (3, 4)) AS _t(col1, col2)",
			"SELECT col1, col2 FROM (VALUES (1, 2), (3, 4)) AS _t(col1, col2)"},
		// Databricks writes the form bare, and reads it that way too.
		{"bare", "databricks", "databricks", "SELECT c1 FROM VALUES ('x') AS T(c1)",
			"SELECT c1 FROM VALUES ('x') AS T(c1)"},
		{"bare in, wrapped out", "databricks", "postgres", "SELECT c1 FROM VALUES ('x') AS T(c1)",
			"SELECT c1 FROM (VALUES ('x')) AS T(c1)"},
		{"wrapped in, bare out", "duckdb", "databricks",
			"SELECT c FROM (VALUES (1)) AS t(c)", "SELECT c FROM VALUES (1) AS t(c)"},
		{"joined", "postgres", "postgres",
			"SELECT * FROM (VALUES (1)) AS t1(id) CROSS JOIN (VALUES (1)) AS t2(id)",
			"SELECT * FROM (VALUES (1)) AS t1(id) CROSS JOIN (VALUES (1)) AS t2(id)"},
		// Two of them in a DELETE's USING are two ENTRIES, where two ordinary
		// tables there would be one entry carrying a comma join.
		{"in a delete", "duckdb", "duckdb",
			"DELETE FROM t USING (VALUES (1)) AS t1(c), (VALUES (1), (2)) AS t2(c) WHERE t.c = t1.c",
			"DELETE FROM t USING (VALUES (1)) AS t1(c), (VALUES (1), (2)) AS t2(c) WHERE t.c = t1.c"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, tc.read)
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			got, err := Generate(e, tc.write)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
	// Inside a join TREE the rows are wrapped in a Table, where standing
	// alone as a FROM item they are not -- and the alias moves with them.
	nested, err := ParseOne(
		"SELECT 1 FROM ((VALUES (1)) AS vals(id) LEFT OUTER JOIN tbl ON vals.id = tbl.id)",
		"postgres")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if got, err := Generate(nested, "postgres"); err != nil {
		t.Fatalf("Generate: %v", err)
	} else if got != "SELECT 1 FROM ((VALUES (1)) AS vals(id) LEFT OUTER JOIN tbl ON vals.id = tbl.id)" {
		t.Errorf("got %q", got)
	}
	// And the body of an INSERT is still written bare.
	body, err := ParseOne("INSERT INTO x VALUES (1), (2)", "")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if got, _ := Generate(body, ""); got != "INSERT INTO x VALUES (1), (2)" {
		t.Errorf("got %q", got)
	}
}

// TestAttachDetach covers the two statements that open and close a database.
//
// Every case here writes back as itself EXCEPT the two the reference
// normalises: the optional word DATABASE is dropped on the way in, and DETACH
// puts it back only where IF EXISTS is written.
func TestAttachDetach(t *testing.T) {
	for sql, want := range map[string]string{
		"ATTACH 'file.db'":                            "ATTACH 'file.db'",
		"ATTACH 123":                                  "ATTACH 123",
		"ATTACH 'f' (a 1, b TRUE, c FALSE, d e)":      "ATTACH 'f' (a 1, b TRUE, c FALSE, d e)",
		"ATTACH ':memory:' AS db_alias":               "ATTACH ':memory:' AS db_alias",
		"ATTACH IF NOT EXISTS 'file.db' AS db":        "ATTACH IF NOT EXISTS 'file.db' AS db",
		"ATTACH 'file.db' AS db_alias (READ_ONLY)":    "ATTACH 'file.db' AS db_alias (READ_ONLY)",
		"ATTACH 'f' (READ_ONLY FALSE, TYPE sqlite)":   "ATTACH 'f' (READ_ONLY FALSE, TYPE sqlite)",
		"ATTACH 'f' (TYPE POSTGRES, SCHEMA 'public')": "ATTACH 'f' (TYPE POSTGRES, SCHEMA 'public')",
		// The word DATABASE is optional on the way in and is not kept.
		"ATTACH DATABASE 'file.db'": "ATTACH 'file.db'",
		"DETACH DATABASE db":        "DETACH db",
		"DETACH new_database":       "DETACH new_database",
		// ...and DETACH writes it where IF EXISTS is written, because DuckDB
		// requires it there.
		"DETACH IF EXISTS file":          "DETACH DATABASE IF EXISTS file",
		"DETACH DATABASE IF EXISTS file": "DETACH DATABASE IF EXISTS file",
	} {
		e, err := ParseOne(sql, "duckdb")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", sql, err)
		}
		if !IsWrite(e) {
			t.Errorf("IsWrite(%q) = false; it changes what the session can reach", sql)
		}
		got, err := Generate(e, "duckdb")
		if err != nil {
			t.Fatalf("Generate(%q): %v", sql, err)
		}
		if got != want {
			t.Errorf("%q wrote %q, want %q", sql, got, want)
		}
	}
}

// TestAttachRefusals covers what the port will not read.
//
// The alias must be EXPLICIT and a quoted name is refused, because the
// reference reads it as a bare word and writes the quotes away.
func TestAttachRefusals(t *testing.T) {
	for _, sql := range []string{
		"ATTACH 'file.db' db_alias",
		`DETACH "My DB"`,
		`ATTACH 'f' ("Q" 1)`,
		"ATTACH",
		"ATTACH 'f' (",
		"ATTACH 'f' (READ_ONLY",
		"DETACH db EXTRA",
		// The reference reads a NUMBER as the name of a setting, and an alias
		// written as one as a quoted identifier. Neither is a name this port
		// makes from a number.
		"ATTACH 'f' (1)",
		"ATTACH 'f' AS 1",
		"ATTACH 'f' (a b c)",
		"ATTACH 'f' (a *)",
		// The reference reads a parenthesised name here. The port's reader
		// for this position is narrower on purpose -- a string, a number or
		// a word -- so this is the port's own gap rather than a shared one.
		"ATTACH ('f')",
	} {
		if e, err := ParseOne(sql, "duckdb"); err == nil {
			t.Errorf("ParseOne(%q) read %s", sql, e.Class)
		}
	}
	// Only DuckDB has the statement at all; elsewhere the word is a name.
	e, err := ParseOne("DETACH db", "postgres")
	if err != nil {
		t.Fatalf(`ParseOne("DETACH db", "postgres"): %v`, err)
	}
	if e.Class == "Detach" {
		t.Error("PostgreSQL read a DETACH statement, which it does not have")
	}
}

// TestInstall covers `[FORCE] INSTALL <extension> [FROM <source>]`.
func TestInstall(t *testing.T) {
	for _, sql := range []string{
		"INSTALL httpfs",
		"INSTALL httpfs FROM community",
		"INSTALL httpfs FROM 'https://extensions.duckdb.org'",
		`INSTALL "http fs"`,
		"FORCE INSTALL httpfs",
		"FORCE INSTALL httpfs FROM community",
	} {
		e, err := ParseOne(sql, "duckdb")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", sql, err)
		}
		if !IsWrite(e) {
			t.Errorf("IsWrite(%q) = false; it loads code into the engine", sql)
		}
		got, err := Generate(e, "duckdb")
		if err != nil {
			t.Fatalf("Generate(%q): %v", sql, err)
		}
		if got != sql {
			t.Errorf("%q wrote %q", sql, got)
		}
		// No other dialect has a spelling for it, and the generator says so
		// rather than writing something that dialect cannot run.
		if _, err := Generate(e, "postgres"); err == nil {
			t.Errorf("PostgreSQL wrote %q, which it has no INSTALL for", sql)
		}
	}
	for _, sql := range []string{
		// FORCE stands in front of an INSTALL and of nothing else this port
		// reads; the reference keeps `FORCE CHECKPOINT` as raw text.
		"FORCE CHECKPOINT db",
		"FORCE",
		"INSTALL",
		"INSTALL x FROM y.z",
		// The reference reads a FROM with nothing after it and drops the
		// word, writing `INSTALL x`. Refusing beats writing a statement the
		// caller did not ask for.
		"INSTALL x FROM",
		"INSTALL x EXTRA",
		// A quoted source loses its quotes the same way a quoted database
		// name does; see docs/upstream-issues.md.
		`INSTALL x FROM "q"`,
	} {
		if e, err := ParseOne(sql, "duckdb"); err == nil {
			t.Errorf("ParseOne(%q) read %s", sql, e.Class)
		}
	}
}

// TestCommand covers the statements the TOKENIZER takes verbatim.
//
// Every one of them writes back as itself, because the payload was never
// read: it is carried as one string and put back where it was.
func TestCommand(t *testing.T) {
	for _, c := range []struct{ sql, dialect string }{
		{"EXPLAIN SELECT 1", ""},
		{"EXPLAIN", ""},
		{"SHOW TABLES", ""},
		{"VACUUM t", ""},
		{"CALL f(1)", ""},
		{"OPTIMIZE t", ""},
		{"PREPARE s AS SELECT 1", ""},
		{"EXECUTE p", ""},
		{"FETCH c", ""},
		{"RENAME TABLE a TO b", ""},
		// Each dialect adds its own and takes others away.
		{"PRINT @x", "tsql"},
		{"GO", "tsql"},
		{"END", "tsql"},
		{"RESET threads", "duckdb"},
		{"RESET GLOBAL memory_limit", "duckdb"},
		{"EXEC sp_rename 'a', 'b'", "postgres"},
	} {
		e, err := ParseOne(c.sql, c.dialect)
		if err != nil {
			t.Fatalf("ParseOne(%q, %q): %v", c.sql, c.dialect, err)
		}
		if e.Class != "Command" {
			t.Errorf("ParseOne(%q, %q) read %s, want Command", c.sql, c.dialect, e.Class)
		}
		// Nothing here understood the statement, so nothing here can say it
		// is read-only.
		if !IsWrite(e) {
			t.Errorf("IsWrite(%q) = false; a command is opaque, not harmless", c.sql)
		}
		got, err := Generate(e, c.dialect)
		if err != nil {
			t.Fatalf("Generate(%q): %v", c.sql, err)
		}
		if got != c.sql {
			t.Errorf("%q wrote %q", c.sql, got)
		}
	}
}

// TestCommandIsPerDialect covers the keywords one dialect takes verbatim and
// another reads, which is the reason the set is asked of the tokenizer rather
// than written down here.
func TestCommandIsPerDialect(t *testing.T) {
	for _, c := range []struct{ sql, dialect, why string }{
		// DuckDB reads SHOW as a statement of its own, so it is not one of
		// these -- and the port does not read that statement yet.
		{"SHOW TABLES", "duckdb", "DuckDB reads SHOW"},
		// T-SQL reads EXECUTE, and takes END verbatim where PostgreSQL reads
		// it as a COMMIT.
		{"EXECUTE p", "tsql", "T-SQL reads EXECUTE"},
		{"END", "postgres", "PostgreSQL reads END as a COMMIT"},
	} {
		if e, err := ParseOne(c.sql, c.dialect); err == nil && e.Class == "Command" {
			t.Errorf("ParseOne(%q, %q) made a Command; %s", c.sql, c.dialect, c.why)
		}
	}
	// Outside the dialects that have it, RESET is an ordinary name and the
	// statement is a column with an alias.
	e, err := ParseOne("RESET threads", "")
	if err != nil {
		t.Fatalf(`ParseOne("RESET threads", ""): %v`, err)
	}
	if e.Class == "Command" {
		t.Error("the neutral dialect took RESET verbatim; only DuckDB and PostgreSQL do")
	}
}

// TestCommandEndsAtTheSemicolon covers what the tokenizer leaves behind. It
// takes the payload up to the end of the statement and stops, so a command
// followed by a second statement is two statements, not one long payload.
func TestCommandEndsAtTheSemicolon(t *testing.T) {
	e, err := ParseOne("EXPLAIN;", "")
	if err != nil {
		t.Fatalf(`ParseOne("EXPLAIN;"): %v`, err)
	}
	if e.Class != "Command" {
		t.Errorf("read %s, want Command", e.Class)
	}
	if _, err := ParseOne("EXPLAIN; SELECT 1", ""); !errors.Is(err, ErrMultipleStatements) {
		t.Errorf("EXPLAIN followed by a query gave %v, want ErrMultipleStatements", err)
	}
}

// An empty statement is refused rather than read as nothing -- and it reaches
// every rule that asks what the current token is with no token there.
func TestEmptyStatement(t *testing.T) {
	for _, dialect := range []string{"", "tsql", "postgres", "duckdb", "databricks"} {
		if e, err := ParseOne("", dialect); err == nil {
			t.Errorf("ParseOne(\"\", %q) read %s", dialect, e.Class)
		}
	}
}

// TestCache covers holding a table in memory and letting it go again.
func TestCache(t *testing.T) {
	for sql, want := range map[string]string{
		"CACHE TABLE x":      "CACHE TABLE x",
		"CACHE LAZY TABLE x": "CACHE LAZY TABLE x",
		"CACHE TABLE a.b":    "CACHE TABLE a.b",
		// TABLE is optional going in and always written coming out.
		"CACHE x":      "CACHE TABLE x",
		"CACHE LAZY x": "CACHE LAZY TABLE x",
		"CACHE LAZY TABLE x OPTIONS('storageLevel' = 'value')": "CACHE LAZY TABLE x OPTIONS('storageLevel' = 'value')",
		// A national string keeps its prefix, so the option is not simply a
		// pair of Literals.
		"CACHE LAZY TABLE x OPTIONS(N'storageLevel' = 'value')": "CACHE LAZY TABLE x OPTIONS(N'storageLevel' = 'value')",
		"CACHE TABLE x AS SELECT 1":                             "CACHE TABLE x AS SELECT 1",
		// The parentheses are KEPT: they make a Subquery, which is a
		// different tree from the Select they wrap.
		"CACHE TABLE x AS (SELECT 1 AS y)":                                 "CACHE TABLE x AS (SELECT 1 AS y)",
		"CACHE TABLE x AS WITH a AS (SELECT 1) SELECT a.* FROM a":          "CACHE TABLE x AS WITH a AS (SELECT 1) SELECT a.* FROM a",
		"CACHE LAZY TABLE x OPTIONS('storageLevel' = 'value') AS SELECT 1": "CACHE LAZY TABLE x OPTIONS('storageLevel' = 'value') AS SELECT 1",
		"UNCACHE TABLE x":           "UNCACHE TABLE x",
		"UNCACHE TABLE IF EXISTS x": "UNCACHE TABLE IF EXISTS x",
	} {
		e, err := ParseOne(sql, "")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", sql, err)
		}
		if !IsWrite(e) {
			t.Errorf("IsWrite(%q) = false; it changes what the session holds", sql)
		}
		got, err := Generate(e, "")
		if err != nil {
			t.Fatalf("Generate(%q): %v", sql, err)
		}
		if got != want {
			t.Errorf("%q wrote %q, want %q", sql, got, want)
		}
	}
}

// TestCacheRefusals covers what the port will not read here, and why.
func TestCacheRefusals(t *testing.T) {
	for _, sql := range []string{
		// The reference refuses these too.
		"UNCACHE x",
		"UNCACHE TABLE",
		"CACHE TABLE 1",
		"UNCACHE TABLE 1",
		"CACHE TABLE x OPTIONS(k = 'v')",
		"CACHE TABLE x OPTIONS('k' = 1)",
		"CACHE TABLE x OPTIONS('k' = 'v', 'j' = 'w')",
		"CACHE",
		"CACHE TABLE x EXTRA",
		"UNCACHE TABLE x EXTRA",
		// The reference reads a setting with no value and writes
		// `OPTIONS('k' = )`, which is not SQL and which it cannot read back.
		"CACHE TABLE x OPTIONS('k')",
		"CACHE TABLE x OPTIONS",
		"CACHE TABLE x OPTIONS(",
		// ...and drops a dangling AS, writing back less than it was given.
		"CACHE TABLE x AS",
		// The reference reads an empty SELECT here and writes it back;
		// this port has no tree for a query that selects nothing.
		"CACHE TABLE x AS SELECT",
		// The reference reads a column list here as a Schema. This port's
		// reader for the position is a table name and nothing else.
		"CACHE TABLE x(a, b)",
	} {
		if e, err := ParseOne(sql, ""); err == nil {
			t.Errorf("ParseOne(%q) read %s", sql, e.Class)
		}
	}
}

// TestDescribe covers asking what something IS.
func TestDescribe(t *testing.T) {
	for _, c := range []struct{ sql, dialect string }{
		{"DESCRIBE x", ""},
		{"DESCRIBE a.b", ""},
		{"DESCRIBE EXTENDED a.b", ""},
		{"DESCRIBE FORMATTED a.b", ""},
		{"DESCRIBE ANALYZE x", ""},
		{"DESCRIBE SELECT 1", ""},
		{"DESCRIBE x AS JSON", ""},
		{"DESCRIBE EXTENDED staging.tbl AS JSON", "databricks"},
		{"DESCRIBE HISTORY a.b", "databricks"},
		// The same word, one token later, is a schema name -- and a QUOTED
		// one is a name wherever it stands.
		{"DESCRIBE history.tbl", "databricks"},
		{`DESCRIBE "history"`, ""},
	} {
		e, err := ParseOne(c.sql, c.dialect)
		if err != nil {
			t.Fatalf("ParseOne(%q, %q): %v", c.sql, c.dialect, err)
		}
		if e.Class != "Describe" {
			t.Errorf("ParseOne(%q) read %s, want Describe", c.sql, e.Class)
		}
		// Asking what a table is changes nothing.
		if IsWrite(e) {
			t.Errorf("IsWrite(%q) = true; it asks a question", c.sql)
		}
		got, err := Generate(e, c.dialect)
		if err != nil {
			t.Fatalf("Generate(%q): %v", c.sql, err)
		}
		if got != c.sql {
			t.Errorf("%q wrote %q", c.sql, got)
		}
	}

	if IsWrite(nil) {
		t.Error("IsWrite(nil) = true")
	}

	// ...but what it names may be a statement, and then the statement is
	// what a guard has to answer about.
	e, err := ParseOne("DESCRIBE INSERT INTO t VALUES (1)", "")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if !IsWrite(e) {
		t.Error("IsWrite over a DESCRIBE of an INSERT = false; the INSERT is a write")
	}
	if got, _ := Generate(e, ""); got != "DESCRIBE INSERT INTO t VALUES (1)" {
		t.Errorf("wrote %q", got)
	}

	for _, sql := range []string{
		// The reference reads the KIND and then writes the statement without
		// it -- `DESCRIBE VIEW x` comes back as `DESCRIBE x`, which asks
		// about whatever object holds the name.
		"DESCRIBE TABLE x",
		"DESCRIBE VIEW x",
		// Not read: none is in the corpus and each is a shape to guess at.
		"DESCRIBE x PARTITION(a = 1)",
		"DESCRIBE FORMAT=JSON x",
		"DESCRIBE",
		"DESCRIBE x EXTRA",
	} {
		if e, err := ParseOne(sql, ""); err == nil {
			t.Errorf("ParseOne(%q) read %s", sql, e.Class)
		}
	}
}

// TestAnalyze covers gathering the statistics a planner uses -- one statement
// spelled three ways by three dialects.
func TestAnalyze(t *testing.T) {
	for _, c := range []struct{ sql, dialect string }{
		// DuckDB's whole statement.
		{"ANALYZE", "duckdb"},
		// PostgreSQL: options in front, then a list of tables.
		{"ANALYZE TBL", "postgres"},
		{"ANALYZE t1, t2", "postgres"},
		{"ANALYZE VERBOSE t1, t2", "postgres"},
		{"ANALYZE VERBOSE SKIP_LOCKED TBL", "postgres"},
		{"ANALYZE BUFFER_USAGE_LIMIT 1337 TBL", "postgres"},
		// A column list makes the table a CALL, which is the reference's
		// reading of the position.
		{"ANALYZE TBL(col1, col2)", "postgres"},
		{"ANALYZE VERBOSE SKIP_LOCKED TBL(col1, col2)", "postgres"},
		// A quoted name that spells an option is a name.
		{`ANALYZE "verbose"`, ""},
		{`ANALYZE "tables"`, ""},
		{`ANALYZE "database"`, ""},
		// Databricks: a kind, then a COMPUTE clause.
		{"ANALYZE TABLE ctlg.db.tbl COMPUTE DELTA STATISTICS NOSCAN", "databricks"},
		{"ANALYZE TABLE tbl COMPUTE DELTA STATISTICS FOR ALL COLUMNS", "databricks"},
		{"ANALYZE TABLE tbl COMPUTE DELTA STATISTICS FOR COLUMNS foo, bar", "databricks"},
		{"ANALYZE TABLE ctlg.db.tbl PARTITION(foo = 'foo', bar = 'bar') COMPUTE STATISTICS NOSCAN", "databricks"},
		// TABLES with no FROM or IN keeps the kind and names nothing.
		{"ANALYZE TABLES COMPUTE STATISTICS NOSCAN", "databricks"},
		{"ANALYZE TABLES FROM db COMPUTE STATISTICS", "databricks"},
		{"ANALYZE TABLES IN db COMPUTE STATISTICS", "databricks"},
		{"ANALYZE TABLE tbl ESTIMATE STATISTICS", "databricks"},
	} {
		e, err := ParseOne(c.sql, c.dialect)
		if err != nil {
			t.Fatalf("ParseOne(%q, %q): %v", c.sql, c.dialect, err)
		}
		if e.Class != "Analyze" {
			t.Errorf("ParseOne(%q) read %s, want Analyze", c.sql, e.Class)
		}
		// It writes the statistics the planner will read next.
		if !IsWrite(e) {
			t.Errorf("IsWrite(%q) = false; it stores what it gathers", c.sql)
		}
		got, err := Generate(e, c.dialect)
		if err != nil {
			t.Fatalf("Generate(%q): %v", c.sql, err)
		}
		if got != c.sql {
			t.Errorf("%q wrote %q", c.sql, got)
		}
	}
}

// TestAnalyzeRefusals covers the shapes this port declines to guess at.
func TestAnalyzeRefusals(t *testing.T) {
	for _, sql := range []string{
		// Each reads its subject a different way and none is in the corpus.
		"ANALYZE INDEX i",
		"ANALYZE DATABASE db",
		"ANALYZE CLUSTER c",
		// The other words the reference accepts where COMPUTE stands, each
		// of which builds a node of its own.
		"ANALYZE TABLE t DROP HISTOGRAM ON c",
		"ANALYZE TABLE t UPDATE HISTOGRAM ON c",
		"ANALYZE TABLE t DELETE STATISTICS",
		"ANALYZE TABLE t COMPUTE STATISTICS SAMPLE 5 PERCENT",
		"ANALYZE TABLE t COMPUTE STATISTICS FOR SOMETHING",
		"ANALYZE TABLE t COMPUTE THINGS",
		"ANALYZE BUFFER_USAGE_LIMIT TBL",
		"ANALYZE TABLE t EXTRA EXTRA",
		"ANALYZE a.b(c)",
		// Error paths: a bad table name, a bad partition, a bad column list.
		"ANALYZE 1",
		"ANALYZE t1, 2",
		"ANALYZE TABLES IN 1 COMPUTE STATISTICS",
		"ANALYZE TABLE t PARTITION(",
		"ANALYZE TABLE t COMPUTE STATISTICS FOR COLUMNS 1",
		"ANALYZE TABLE t COMPUTE STATISTICS FOR COLUMNS a,",
		"ANALYZE TBL(",
	} {
		if e, err := ParseOne(sql, "databricks"); err == nil {
			t.Errorf("ParseOne(%q) read %s", sql, e.Class)
		}
	}
}

// TestChainedAccess covers reaching into nested data: a subscript, then a
// field, then another subscript, as far as the statement goes.
func TestChainedAccess(t *testing.T) {
	for _, sql := range []string{
		"a[0].b[1]",
		"a[0].b.c['d']",
		"a['x'].C()",
		"a['x'].b.C()",
		"a[0][0].b.c[1].d.e.f[1][1]",
		"X((y AS z)).1",
		"a[b].C()",
		"a.b[c].D()",
		"x.y.FOO()",
		// A string, a national string and a number stand where a name would.
		"a[0].'x'",
		"a[0].N'x'",
		"a[0].1",
		"a[0].b.'c'",
	} {
		e, err := ParseOne(sql, "")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", sql, err)
		}
		got, err := Generate(e, "")
		if err != nil {
			t.Fatalf("Generate(%q): %v", sql, err)
		}
		if got != sql {
			t.Errorf("%q wrote %q", sql, got)
		}
	}
}

// A chain that cannot go on is refused for what follows the dot, not read as
// far as it goes and the rest dropped.
func TestChainedAccessRefusals(t *testing.T) {
	for _, sql := range []string{
		"a[0].",
		"a[0].+",
		"a[0].b.",
	} {
		if e, err := ParseOne(sql, ""); err == nil {
			t.Errorf("ParseOne(%q) read %s", sql, e.Class)
		}
	}
}

// TestChainEndingInACall covers what a trailing call does to the names in
// front of it.
//
// `a[b].c` reads a and b as COLUMNS. `a[b].C()` reads the same two as
// IDENTIFIERS, because the chain turns out to name a function and everything
// leading to it is part of that name.
func TestChainEndingInACall(t *testing.T) {
	column, err := ParseOne("a[b].c", "")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	inner, _ := column.Args["this"].(*Expression)
	base, _ := inner.Args["this"].(*Expression)
	if base.Class != "Column" {
		t.Errorf("a[b].c read its base as %s, want Column", base.Class)
	}

	call, err := ParseOne("a[b].C()", "")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	bracket, _ := call.Args["this"].(*Expression)
	base, _ = bracket.Args["this"].(*Expression)
	if base.Class != "Identifier" {
		t.Errorf("a[b].C() read its base as %s, want Identifier", base.Class)
	}
	// The index is rewritten too: the transform runs over the whole chain,
	// not only over the names on its spine.
	index, _ := bracket.Args["expressions"].([]*Expression)
	if len(index) != 1 || index[0].Class != "Identifier" {
		t.Errorf("a[b].C() read its index as %v, want one Identifier", index)
	}
}

// TestTsqlTemporaryTables covers the # T-SQL writes in front of a temporary
// table's name, and the ## it writes in front of a global one.
//
// The mark is not part of the name. The reference takes it off and records a
// flag on the Identifier, and the writer puts it back -- which is why the
// port refused all of this until the rule was worked out rather than
// reproducing half of it.
func TestTsqlTemporaryTables(t *testing.T) {
	for _, sql := range []string{
		"SELECT * FROM #foo",
		"SELECT * FROM ##foo",
		"SELECT #x",
		"SELECT ##x",
		"SELECT a FROM #t AS x",
		"CREATE TABLE #mytemptable (a INTEGER)",
		"WITH t(c) AS (SELECT 1) SELECT c INTO #foo FROM t",
		// Written inside the quoting, and read back out of it.
		"SELECT * FROM [#temp_table]",
		"SELECT * FROM [##temp_table]",
		"CREATE TABLE [#temptest] (name INTEGER)",
		// A COLUMN is never temporary, so a quoted name keeps its mark: the
		// stripping happens where the name is known to be a table's.
		"SELECT [#a]",
		"SELECT a#b",
		"SELECT * FROM ##g.t",
	} {
		e, err := ParseOne(sql, "tsql")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", sql, err)
		}
		got, err := Generate(e, "tsql")
		if err != nil {
			t.Fatalf("Generate(%q): %v", sql, err)
		}
		if got != sql {
			t.Errorf("%q wrote %q", sql, got)
		}
	}
}

// TestTsqlTemporaryIsRecordedTwice covers where the mark ends up: on the name,
// and again on the node above it.
func TestTsqlTemporaryIsRecordedTwice(t *testing.T) {
	e, err := ParseOne("SELECT * FROM #foo", "tsql")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	from, _ := e.Args["from_"].(*Expression)
	table, _ := from.Args["this"].(*Expression)
	name, _ := table.Args["this"].(*Expression)
	if got, _ := name.Args["this"].(string); got != "foo" {
		t.Errorf("the name is %q; the mark is not part of it", got)
	}
	if name.Args["temporary"] != true {
		t.Error("the name does not carry the mark")
	}

	// A SELECT INTO promotes it onto the Into.
	into, err := ParseOne("SELECT c INTO #foo FROM t", "tsql")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	node, _ := into.Args["into"].(*Expression)
	if node.Args["temporary"] != true {
		t.Error("the INTO does not carry the mark")
	}

	// ...and a CREATE gains a TemporaryProperty beside it, which is what
	// lets a dialect that spells it with a word write the same tree.
	create, err := ParseOne("CREATE TABLE #t (a INT)", "tsql")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	props, _ := create.Args["properties"].(*Expression)
	if props == nil {
		t.Fatal("the CREATE has no properties")
	}
	items, _ := props.Args["expressions"].([]*Expression)
	if len(items) != 1 || items[0].Class != "TemporaryProperty" {
		t.Errorf("the CREATE's properties are %v, want one TemporaryProperty", items)
	}
}

// TestTsqlTableVariables covers `@name` standing where a table name goes.
//
// T-SQL declares a table VARIABLE and then reads and writes it by name. The
// relation is the parameter itself -- the reference puts a Parameter where
// the name would be -- rather than a table that happens to be called
// @MyTableVar.
func TestTsqlTableVariables(t *testing.T) {
	for _, sql := range []string{
		"SELECT * FROM @x",
		"SELECT Employee_ID FROM @MyTableVar",
		"SELECT x FROM @MyTableVar AS m JOIN Employee ON m.EmployeeID = Employee.EmployeeID",
		"INSERT INTO @TestTable VALUES (1, 'Value1', 12, 20)",
		"DELETE FROM @x",
		"UPDATE @x SET a = 1",
	} {
		e, err := ParseOne(sql, "tsql")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", sql, err)
		}
		got, err := Generate(e, "tsql")
		if err != nil {
			t.Fatalf("Generate(%q): %v", sql, err)
		}
		if got != sql {
			t.Errorf("%q wrote %q", sql, got)
		}
	}

	// It is the PARAMETER that names the relation, not an identifier.
	e, err := ParseOne("SELECT * FROM @x", "tsql")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	from, _ := e.Args["from_"].(*Expression)
	table, _ := from.Args["this"].(*Expression)
	name, _ := table.Args["this"].(*Expression)
	if name == nil || name.Class != "Parameter" {
		t.Errorf("the table is named by %v, want a Parameter", name)
	}

	// Only where `@name` is a PARAMETER, which is what the dialect's own
	// table says. DuckDB spells a parameter `$x` and reads `@x` here as one
	// too; this port does not build that shape, and refuses rather than
	// naming the relation something the reference did not.
	if e, err := ParseOne("SELECT * FROM @x", "duckdb"); err == nil {
		t.Errorf("DuckDB read %s; the port has no tree for a placeholder table", e.Class)
	}
}

// TestParameterInATablePositionReadsEverySpelling covers the property the
// generator fuzzer checks, in the position that broke it.
//
// A parameter naming a relation has to be readable in whatever the dialect
// WRITES it as. Reading only T-SQL's `@name` let `USE @0` parse in Databricks
// and come back as `USE ${0}`, which nothing could read again -- a thousand
// findings from one shortcut.
func TestParameterInATablePositionReadsEverySpelling(t *testing.T) {
	for _, dialect := range []string{"", "tsql", "postgres", "duckdb", "databricks"} {
		for _, sql := range []string{"USE @0", "SELECT * FROM @x", "SELECT * FROM $x", "SELECT * FROM ${x}"} {
			e, err := ParseOne(sql, dialect)
			if err != nil {
				continue // refusing is allowed; writing something unreadable is not
			}
			got, err := Generate(e, dialect)
			if err != nil {
				continue
			}
			if _, err := ParseOne(got, dialect); err != nil {
				t.Errorf("[%s] %q wrote %q, which it cannot read back: %v", dialect, sql, got, err)
			}
		}
	}
}

// TestPostgresJSONOperators covers the three JSON operators PostgreSQL reads
// level with `||` rather than as accessors.
func TestPostgresJSONOperators(t *testing.T) {
	for _, tc := range []struct{ sql, want string }{
		{"x #> 'y'", "x #> 'y'"},
		{"x #>> 'y'", "x #>> 'y'"},
		{"x ? y", "x ? y"},
		{"x ? 'x'", "x ? 'x'"},
		// The right-hand side is parenthesised where it is an operator of
		// its own, and the parentheses are the WRITER's: the same node
		// written as a call carries none.
		{"SELECT a #> (n IN (1, 2))", "SELECT a #> (n IN (1, 2))"},
		{"SELECT JSONB_EXTRACT(a, n IN (1, 2))", "SELECT a #> (n IN (1, 2))"},
		// The tier shows in the asymmetry: the sum on the left is swallowed,
		// the one on the right is not.
		{"1 + x #> 'y'", "1 + x #> 'y'"},
		{"x #> 'y' + 1", "x #> ('y' + 1)"},
		{"x #> NOT y", "x #> (NOT y)"},
	} {
		e, err := ParseOne(tc.sql, "postgres")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		got, err := Generate(e, "postgres")
		if err != nil {
			t.Fatalf("Generate(%q): %v", tc.sql, err)
		}
		if got != tc.want {
			t.Errorf("%q wrote %q, want %q", tc.sql, got, tc.want)
		}
	}

	// `1 + x #> 'y'` is `(1 + x) #> 'y'` HERE and `1 + (x #> 'y')` in a
	// dialect that reads the operator as an accessor. The port reads it in
	// PostgreSQL alone rather than one tier out everywhere.
	e, err := ParseOne("1 + x #> 'y'", "postgres")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if e.Class != "JSONBExtract" {
		t.Errorf("PostgreSQL read %s on top, want JSONBExtract", e.Class)
	}
	for _, dialect := range []string{"", "tsql", "duckdb", "databricks"} {
		if e, err := ParseOne("x #> 'y'", dialect); err == nil {
			t.Errorf("[%s] read %s; the operator is PostgreSQL's here", dialect, e.Class)
		}
	}
}

// TestParenthesisedQuery covers a query in parentheses standing where a query
// stands: as a whole statement, on either side of a set operation, carrying
// modifiers of its own, and as a FROM item.
//
// Every pair of parentheses is recorded. `((SELECT 1))` is a Subquery inside a
// Subquery, not one with a spare set of brackets, and the modifiers go
// OUTSIDE them -- `(SELECT 1) ORDER BY x` orders the subquery rather than the
// SELECT inside it.
func TestParenthesisedQuery(t *testing.T) {
	for _, sql := range []string{
		"(SELECT 1)",
		"((SELECT 1))",
		"((SELECT 1)) LIMIT 1",
		"(SELECT 1) UNION SELECT 2",
		"(SELECT 1) UNION (SELECT 2)",
		"(SELECT 1) UNION SELECT 2 ORDER BY x",
		"(SELECT 1) ORDER BY x LIMIT 1 OFFSET 1",
		"(SELECT 1 UNION SELECT 2) ORDER BY x LIMIT 1 OFFSET 1",
		"(SELECT 1 UNION SELECT 2) UNION (SELECT 2 UNION ALL SELECT 3)",
		"SELECT * FROM ((SELECT 1) UNION SELECT 2) AS t",
		"SELECT * FROM ((SELECT 1)) AS t",
		// A parenthesised JOIN TREE begins the same way and is not a query.
		"SELECT * FROM ((SELECT 1 AS x) CROSS JOIN (SELECT 2 AS y)) AS z",
		"SELECT * FROM (a CROSS JOIN b)",
		"SELECT * FROM ((x))",
		// ...and so does an EXPRESSION whose first operand is a query.
		"SELECT ((SELECT 1) + 1)",
		"(SELECT 1) + 1",
		"(SELECT 1) % (SELECT 2)",
		"1 UNION SELECT 2",
	} {
		e, err := ParseOne(sql, "")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", sql, err)
		}
		got, err := Generate(e, "")
		if err != nil {
			t.Fatalf("Generate(%q): %v", sql, err)
		}
		if got != sql {
			t.Errorf("%q wrote %q", sql, got)
		}
	}

	// The modifiers of a set operation land on the OPERATION, not on the
	// query that was written last.
	e, err := ParseOne("(SELECT 1) UNION SELECT 2 ORDER BY x", "")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if e.Class != "Union" {
		t.Fatalf("read %s, want Union", e.Class)
	}
	if _, ok := e.Args["order"].(*Expression); !ok {
		t.Error("the ORDER BY is not on the union")
	}
	// ...and a modified subquery keeps them on itself.
	e, err = ParseOne("(SELECT 1) ORDER BY x LIMIT 1 OFFSET 1", "")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if e.Class != "Subquery" {
		t.Fatalf("read %s, want Subquery", e.Class)
	}
	for _, key := range []string{"order", "limit", "offset"} {
		if _, ok := e.Args[key].(*Expression); !ok {
			t.Errorf("the subquery has no %s", key)
		}
	}
}

// A parenthesis that opens neither a query nor a balanced group is refused
// rather than scanned past the end of the statement.
func TestUnclosedParenthesisedQuery(t *testing.T) {
	for _, sql := range []string{
		"SELECT * FROM ((SELECT 1) UNION",
		"SELECT * FROM ((SELECT 1)",
		"SELECT * FROM ((SELECT 1))) AS t",
		"SELECT * FROM ((SELECT 1",
		"SELECT * FROM ((SELECT 1)",
		"(SELECT 1",
		"SELECT ((",
		"SELECT ((SELECT 1)",
	} {
		if e, err := ParseOne(sql, ""); err == nil {
			t.Errorf("ParseOne(%q) read %s", sql, e.Class)
		}
	}
}

// TestVariadic covers PostgreSQL's VARIADIC, which spreads an array over a
// call's parameters.
func TestVariadic(t *testing.T) {
	for _, sql := range []string{
		"SELECT MLEAST(VARIADIC ARRAY[10, -1, 5, 4.4])",
		"SELECT F(VARIADIC a)",
		"SELECT F(VARIADIC a || b)",
	} {
		e, err := ParseOne(sql, "postgres")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", sql, err)
		}
		got, err := Generate(e, "postgres")
		if err != nil {
			t.Fatalf("Generate(%q): %v", sql, err)
		}
		if got != sql {
			t.Errorf("%q wrote %q", sql, got)
		}
	}
	// Only PostgreSQL has the word; elsewhere it is not a no-paren function
	// and the port has no tree for it.
	for _, dialect := range []string{"", "tsql", "duckdb", "databricks"} {
		if e, err := ParseOne("SELECT F(VARIADIC a)", dialect); err == nil {
			t.Errorf("[%s] read %s; VARIADIC is PostgreSQL's", dialect, e.Class)
		}
	}
}

// TestJSONPathFunctionPositionalForm covers what a path-folding function does
// with an argument it cannot fold into a path.
//
// The reference lays the arguments out where they were written rather than
// building a path, and the port now reads that shape. It does not WRITE it:
// PostgreSQL spells the extraction one argument per part and quotes each one,
// so a part that is a column has no form to go into.
func TestJSONPathFunctionPositionalForm(t *testing.T) {
	for _, sql := range []string{
		"SELECT JSON_EXTRACT_PATH(x, k1, k2) FROM t",
		"SELECT JSON_EXTRACT_PATH(x, k1, 'k2') FROM t",
		"SELECT JSON_EXTRACT_PATH(a, VARIADIC '{}') FROM t",
		"SELECT JSON_EXTRACT_PATH_TEXT(x, k1, k2) FROM t",
	} {
		e, err := ParseOne(sql, "postgres")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", sql, err)
		}
		// The arguments stay where they were written: no path is built, and
		// the constants the folded form carries are absent rather than
		// present-and-false.
		if _, ok := e.Args["expressions"]; !ok {
			t.Fatalf("%q: no projections", sql)
		}
		if _, refused := Generate(e, "postgres"); refused == nil {
			t.Errorf("%q was written back; the port has no form for it", sql)
		}
	}
}

// TestStringAggDistinct covers the two things that hang off STRING_AGG's
// FIRST argument however they are written.
//
// DISTINCT wraps it, and an ORDER BY -- written after the SEPARATOR -- wraps
// whatever that produced. `STRING_AGG(DISTINCT x, ',' ORDER BY y)` is
// GroupConcat(Order(Distinct(x), y), ',').
func TestStringAggDistinct(t *testing.T) {
	for _, sql := range []string{
		"STRING_AGG(x, ',')",
		"STRING_AGG(DISTINCT x, ',')",
		"STRING_AGG(x, ',' ORDER BY y DESC)",
		"STRING_AGG(DISTINCT x, ',' ORDER BY y DESC NULLS LAST)",
		"STRING_AGG(DISTINCT a || b || c, '')",
		"STRING_AGG(DISTINCT a || b || c, '' ORDER BY d NULLS FIRST)",
	} {
		e, err := ParseOne(sql, "postgres")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", sql, err)
		}
		got, err := Generate(e, "postgres")
		if err != nil {
			t.Fatalf("Generate(%q): %v", sql, err)
		}
		if got != sql {
			t.Errorf("%q wrote %q", sql, got)
		}
	}

	e, err := ParseOne("STRING_AGG(DISTINCT x, ',' ORDER BY y)", "postgres")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	order, _ := e.Args["this"].(*Expression)
	if order == nil || order.Class != "Order" {
		t.Fatalf("the first argument is %v, want an Order", order)
	}
	distinct, _ := order.Args["this"].(*Expression)
	if distinct == nil || distinct.Class != "Distinct" {
		t.Errorf("the ordering is over %v, want a Distinct", distinct)
	}

	// The overflow behaviour is read by the reference and then written away,
	// so a third argument is refused rather than dropped.
	if e, err := ParseOne("STRING_AGG(DISTINCT x, y, z)", "postgres"); err == nil {
		t.Errorf("read %s; the third argument would be written away", e.Class)
	}
}

// TestLoadData covers Hive's LOAD DATA, which puts a file's rows into a table.
func TestLoadData(t *testing.T) {
	for _, sql := range []string{
		"LOAD DATA INPATH 'x' INTO TABLE y",
		"LOAD DATA LOCAL INPATH 'x' INTO TABLE y",
		"LOAD DATA INPATH 'x' OVERWRITE INTO TABLE y",
		"LOAD DATA INPATH 'x' INTO TABLE y.b INPUTFORMAT 'y' SERDE 'z'",
		"LOAD DATA INPATH 'x' INTO TABLE y PARTITION(ds = 'yyyy')",
		"LOAD DATA LOCAL INPATH 'x' INTO TABLE y PARTITION(ds = 'yyyy') INPUTFORMAT 'y' SERDE 'z'",
	} {
		e, err := ParseOne(sql, "")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", sql, err)
		}
		if !IsWrite(e) {
			t.Errorf("IsWrite(%q) = false; it fills a table", sql)
		}
		got, err := Generate(e, "")
		if err != nil {
			t.Fatalf("Generate(%q): %v", sql, err)
		}
		if got != sql {
			t.Errorf("%q wrote %q", sql, got)
		}
	}

	// Three arguments are FALSE when the clause is absent rather than
	// missing, which is what the reference's `matched and value` yields.
	e, err := ParseOne("LOAD DATA INPATH 'x' INTO TABLE y", "")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	for _, key := range []string{"files", "input_format", "serde"} {
		if e.Args[key] != false {
			t.Errorf("%s is %v, want false", key, e.Args[key])
		}
	}

	for _, sql := range []string{
		// The reference reads TEMPORARY and writes the statement without it,
		// loading the file into a table that outlives the session.
		"LOAD DATA INPATH 'x' INTO TEMPORARY TABLE y",
		// Refused by the reference too: a LOAD DATA has to say where it
		// loads to.
		"LOAD DATA INPATH 'x'",
		// The reference reads a missing path as nothing and writes
		// `LOAD DATA INPATH  INTO TABLE y` -- a statement that names no
		// file, which is not the one that was written.
		"LOAD DATA INTO TABLE y",
		// Kept as raw text by the reference, which is not a tree this port
		// builds.
		"LOAD INDEX INTO CACHE t",
		"LOAD DATA INPATH 'x' INTO TABLE y EXTRA",
		"LOAD DATA INPATH 'x' INTO TABLE y INPUTFORMAT z",
		"LOAD DATA INPATH 'x' INTO TABLE y SERDE z",
		"LOAD DATA LOCAL",
		"LOAD DATA INPATH x INTO TABLE y",
		"LOAD DATA INPATH 'x' INTO y",
		"LOAD DATA INPATH 'x' INTO TABLE 1",
		"LOAD DATA INPATH 'x' INTO TABLE y PARTITION(",
	} {
		if e, err := ParseOne(sql, ""); err == nil {
			t.Errorf("ParseOne(%q) read %s", sql, e.Class)
		}
	}
}

// TestWithOrdinality covers numbering the rows a relation returns.
//
// Three nodes carry it and each puts it in a slightly different place. On a
// TABLE the words come after everything and take the alias with them; on an
// UNNEST they come before the alias is read; on a LATERAL they sit between
// the relation and its alias.
func TestWithOrdinality(t *testing.T) {
	for _, c := range []struct{ sql, dialect string }{
		{"SELECT * FROM F(x) WITH ORDINALITY", ""},
		{"SELECT * FROM F(x) WITH ORDINALITY AS t(a, b)", ""},
		{"SELECT * FROM JSON_ARRAY_ELEMENTS('[1]') WITH ORDINALITY", "postgres"},
		{"SELECT * FROM UNNEST(x) WITH ORDINALITY", ""},
		{"SELECT * FROM UNNEST(x) WITH ORDINALITY", "postgres"},
		{"SELECT * FROM UNNEST(x) WITH ORDINALITY AS t(a, b)", ""},
		{"SELECT * FROM UNNEST(x) WITH ORDINALITY AS t(a, b)", "postgres"},
		{"SELECT * FROM UNNEST(x) WITH ORDINALITY AS t(a, b)", "duckdb"},
		{"SELECT * FROM test_data, LATERAL JSONB_ARRAY_ELEMENTS(data) WITH ORDINALITY AS elem(value, ordinality)", "postgres"},
	} {
		e, err := ParseOne(c.sql, c.dialect)
		if err != nil {
			t.Fatalf("ParseOne(%q, %q): %v", c.sql, c.dialect, err)
		}
		got, err := Generate(e, c.dialect)
		if err != nil {
			t.Fatalf("Generate(%q): %v", c.sql, err)
		}
		if got != c.sql {
			t.Errorf("%q wrote %q", c.sql, got)
		}
	}

	// An UNNEST with MORE alias columns than unnested expressions names the
	// ordinality column with the last one: `t(a, b)` numbers into b and
	// leaves a for the values.
	e, err := ParseOne("SELECT * FROM UNNEST(x) WITH ORDINALITY AS t(a, b)", "")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	from, _ := e.Args["from_"].(*Expression)
	unnest, _ := from.Args["this"].(*Expression)
	offset, _ := unnest.Args["offset"].(*Expression)
	if offset == nil || offset.Class != "Identifier" {
		t.Fatalf("the ordinality is %v, want an Identifier", unnest.Args["offset"])
	}
	if name, _ := offset.Args["this"].(string); name != "b" {
		t.Errorf("the ordinality column is %q, want b", name)
	}
	alias, _ := unnest.Args["alias"].(*Expression)
	columns, _ := alias.Args["columns"].([]*Expression)
	if len(columns) != 1 {
		t.Errorf("the alias keeps %d columns, want 1", len(columns))
	}

	// Databricks writes an aliased UNNEST as EXPLODE with the alias among
	// the arguments, and one that numbers its rows as EXPLODE too -- shapes
	// this port does not build, so it refuses rather than write them.
	for _, sql := range []string{
		"SELECT * FROM UNNEST(x) AS t(a)",
		"SELECT * FROM UNNEST(x) WITH ORDINALITY AS t(a, b)",
	} {
		e, err := ParseOne(sql, "databricks")
		if err != nil {
			t.Fatalf("ParseOne(%q, databricks): %v", sql, err)
		}
		if got, err := Generate(e, "databricks"); err == nil {
			t.Errorf("Databricks wrote %q; it spells this as EXPLODE", got)
		}
	}

	// `WITH OFFSET` names the same column another way and the reference
	// writes every spelling of it back as WITH ORDINALITY, dropping the name.
	for _, sql := range []string{
		"SELECT * FROM UNNEST(x) WITH OFFSET",
		"SELECT * FROM UNNEST(x) WITH OFFSET AS o",
	} {
		if e, err := ParseOne(sql, ""); err == nil {
			t.Errorf("ParseOne(%q) read %s; the name would be written away", sql, e.Class)
		}
	}
}

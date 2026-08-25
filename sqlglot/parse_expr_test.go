package sqlglot

import "testing"

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
		// A fold needs every key to be a string; handed anything else the
		// reference lays the arguments out positionally instead.
		{"a fold over something that is not a string", "postgres",
			"SELECT JSON_EXTRACT_PATH(x, k1, k2)"},
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

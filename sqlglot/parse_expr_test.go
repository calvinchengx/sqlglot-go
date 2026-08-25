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
func TestGroupConcatOrderRefused(t *testing.T) {
	e, err := ParseOne("SELECT STRING_AGG(x, ',') WITHIN GROUP (ORDER BY y DESC)", "")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	for _, dialect := range []string{"duckdb", "postgres"} {
		if got, err := Generate(e, dialect); err == nil {
			t.Errorf("%s wrote %q; it puts the ordering elsewhere", dialect, got)
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
		"CREATE TABLE t (a INT PRIMARY KEY)",
		"CREATE TABLE t (a INT DEFAULT 1)",
		"CREATE TEMPORARY TABLE t (a INT)",
		"CREATE VIEW v AS SELECT 1",
		"CREATE TABLE t",
		"CREATE TABLE t (a INT",
		"CREATE TABLE t (a INT) PARTITIONED BY (b)",
	} {
		if _, err := ParseOne(sql, ""); err == nil {
			t.Errorf("ParseOne(%q) was read; it should be refused", sql)
		}
	}
}

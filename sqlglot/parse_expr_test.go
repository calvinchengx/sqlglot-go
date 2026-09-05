package sqlglot

import (
	"errors"
	"strings"
	"testing"
	"time"
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
		// JSON_KEYS with no path hands the reference's own `to_json_path` a
		// missing second argument, which it hands straight back as absent --
		// not a ROOT path the way another builder here defaults a missing
		// one, so the call carries no `expression` at all.
		{"JSON_KEYS with no path argument at all", "databricks",
			"SELECT JSON_KEYS(foo)", "SELECT JSON_OBJECT_KEYS(foo)"},
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
		{"a key the path grammar cannot read, over a different call",
			"databricks", "SELECT JSON_EXTRACT(a, '$.x-y')"},
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
		{"accessor: multiplication stays outside", "", "SELECT a -> b * c",
			"SELECT JSON_EXTRACT(a, b) * c"},
		{"accessor: a bracket wraps the extract", "", "SELECT x -> y[1]",
			"SELECT JSON_EXTRACT(x, y)[1]"},
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

// A dialect with no ONE call that reads both an object and a scalar out of
// JSON asks both and takes whichever is not null.
//
// `ISNULL(JSON_QUERY(x, p), JSON_VALUE(x, p))` is one node written as two
// calls, so the value and the path each appear twice. That makes reading such
// a statement back give a COALESCE of two extractions rather than the one it
// was written from -- and each of those is then written as a pair of its own.
// The reference does the same; the doubling is the spelling, not a fault in
// it.
func TestJSONExtractWrittenTwice(t *testing.T) {
	for _, tc := range []struct{ sql, want string }{
		{"SELECT JSON_QUERY(x)", "SELECT ISNULL(JSON_QUERY(x, '$'), JSON_VALUE(x, '$'))"},
		{"SELECT JSON_VALUE(x, '$.y')", "SELECT ISNULL(JSON_QUERY(x, '$.y'), JSON_VALUE(x, '$.y'))"},
		{`SELECT JSON_QUERY(x, '$."a b"')`,
			`SELECT ISNULL(JSON_QUERY(x, '$."a b"'), JSON_VALUE(x, '$."a b"'))`},
		{"SELECT JSON_QUERY(x, '$.y[0].z')",
			"SELECT ISNULL(JSON_QUERY(x, '$.y[0].z'), JSON_VALUE(x, '$.y[0].z'))"},
		// A mode word is not path syntax. The reference keeps the string,
		// and the first consumer writes `lax $.b`.
		{"SELECT JSON_QUERY(a, 'lax $.b') FROM dbo.t",
			"SELECT ISNULL(JSON_QUERY(a, 'lax $.b'), JSON_VALUE(a, 'lax $.b')) FROM dbo.t"},
		{"SELECT JSON_VALUE(a, 'strict $.b') FROM dbo.t",
			"SELECT ISNULL(JSON_QUERY(a, 'strict $.b'), JSON_VALUE(a, 'strict $.b')) FROM dbo.t"},
	} {
		e, err := ParseOne(tc.sql, "tsql")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		if got, err := Generate(e, "tsql"); err != nil || got != tc.want {
			t.Errorf("%q wrote %q (%v), want %q", tc.sql, got, err, tc.want)
		}
	}
	// The mode word stays a string, not a path: folding `lax` into a key
	// would be a different extraction.
	lax, err := ParseOne("SELECT JSON_QUERY(a, 'lax $.b') FROM dbo.t", "tsql")
	if err != nil {
		t.Fatalf("ParseOne(lax): %v", err)
	}
	extracts := lax.FindAll("JSONExtract")
	if len(extracts) != 1 {
		t.Fatalf("JSONExtract count = %d, want 1", len(extracts))
	}
	if path, _ := extracts[0].Args["expression"].(*Expression); path == nil ||
		path.Class != "Literal" || path.Args["this"] != "lax $.b" {
		t.Errorf("the path is %v, want the string lax $.b", extracts[0].Args["expression"])
	}

	// An extraction with no path at all has nothing to write into either
	// call, so the pair is refused rather than written half-formed.
	bare := New("JSONExtract",
		Arg{"this", New("Column", Arg{"this", New("Identifier", Arg{"this", "x"})})})
	if got, err := Generate(bare, "tsql"); err == nil {
		t.Errorf("wrote %q with no path", got)
	}
}

// Shapes the parser now READS and the writer still declines. They are recorded
// because a refusal is the safe outcome and a silent wrong spelling is not:
// each is a generator gap, not a parser one, and naming them here keeps the
// two apart.
func TestJSONPathFunctionsReadButNotWritten(t *testing.T) {
	for _, tc := range []struct{ name, dialect, sql, why string }{} {
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

// A path SUBSCRIPT names a position in an array, and the two forms spell it
// differently: the call quotes it like any other part and the operator chain
// writes it bare. `x -> 'y' -> 0 -> 'z'` and
// `JSON_EXTRACT_PATH(x, 'y', '0', 'z')` are the same path in the two forms.
func TestJSONPathSubscript(t *testing.T) {
	for _, tc := range []struct{ sql, want string }{
		{"x -> 'y' -> 0 -> 'z'", "x -> 'y' -> 0 -> 'z'"},
		{"JSON_EXTRACT_PATH(x, 'y', '0', 'z')", "JSON_EXTRACT_PATH(x, 'y', '0', 'z')"},
		{"'[1,2,3]'::json->2", "CAST('[1,2,3]' AS JSON) -> 2"},
		{"'[1,2,3]'::json->>2", "CAST('[1,2,3]' AS JSON) ->> 2"},
		// A path naming only the ROOT has no parts to spread, and the call
		// takes an empty array in their place.
		{"SELECT JSON_EXTRACT_SCALAR(a, '$') FROM t",
			"SELECT JSON_EXTRACT_PATH_TEXT(a, VARIADIC '{}') FROM t"},
	} {
		e, err := ParseOne(tc.sql, "postgres")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		if got, err := Generate(e, "postgres"); err != nil || got != tc.want {
			t.Errorf("%q wrote %q (%v), want %q", tc.sql, got, err, tc.want)
		}
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
// TestJSONObjectReturning covers RETURNING, read as an ordinary expression
// rather than a type: `VARCHAR(100)` is an Anonymous call, and a bare word
// with no parentheses a Column, which FORMAT JSON then wraps -- the
// reference's own reading, not a type the port would have to model.
func TestJSONObjectReturning(t *testing.T) {
	for _, sql := range []string{
		"JSON_OBJECT('x': 1 RETURNING VARCHAR(100))",
		"JSON_OBJECT('x': 1 RETURNING VARBINARY FORMAT JSON ENCODING UTF8)",
	} {
		tree, err := ParseOne(sql, "")
		if err != nil {
			t.Errorf("ParseOne(%q): %v", sql, err)
			continue
		}
		if got, err := Generate(tree, ""); err != nil || got != sql {
			t.Errorf("%s wrote %q (%v)", sql, got, err)
		}
	}
	// RETURNING with nothing after it is left to name itself: the reference
	// silently reads the statement as if RETURNING had not been written at
	// all, a leniency this port does not reproduce.
	if _, err := ParseOne("JSON_OBJECT('x': 1 RETURNING)", ""); err == nil {
		t.Error("JSON_OBJECT('x': 1 RETURNING) was read; it should be refused")
	}
}

// TestMatchAgainst covers MySQL's full-text search predicate, read the same
// way in every dialect: the columns by name, not as general expressions,
// and the term as a plain string.
func TestMatchAgainst(t *testing.T) {
	sql := `SELECT MATCH("table") AGAINST('x') FROM t`
	tree, err := ParseOne(sql, "")
	if err != nil {
		t.Fatalf("ParseOne(%q): %v", sql, err)
	}
	if got, err := Generate(tree, ""); err != nil || got != sql {
		t.Errorf("%s wrote %q (%v)", sql, got, err)
	}
	sql2 := `SELECT MATCH(a, b) AGAINST('x') FROM t`
	tree2, err := ParseOne(sql2, "")
	if err != nil {
		t.Fatalf("ParseOne(%q): %v", sql2, err)
	}
	if got, err := Generate(tree2, ""); err != nil || got != sql2 {
		t.Errorf("%s wrote %q (%v)", sql2, got, err)
	}
	// SingleStore's `MATCH(TABLE t)` and AGAINST's own modifier phrase are
	// not read: no corpus statement has needed either, and this port has no
	// template to write a modifier back with.
	for _, s := range []string{
		"SELECT MATCH(TABLE t) AGAINST('x')",
		"SELECT MATCH(a) AGAINST('x' IN NATURAL LANGUAGE MODE)",
		"SELECT MATCH(a) AGAINST('x' IN BOOLEAN MODE)",
		"SELECT MATCH(a) AGAINST('x' WITH QUERY EXPANSION)",
		// One malformed input per place a sub-parser is called.
		"SELECT MATCH(a",
		"SELECT MATCH(a)",
		"SELECT MATCH(a) AGAINST",
		"SELECT MATCH(a) AGAINST(1)",
		// An unclosed AGAINST is a leniency the reference has and this port
		// does not reproduce: `AGAINST('x'` with nothing more reads fine
		// there, matching the term to the call's own closing parenthesis.
		"SELECT MATCH(a) AGAINST('x'",
	} {
		if _, err := ParseOne(s, ""); err == nil {
			t.Errorf("ParseOne(%q) was read; it should be refused", s)
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

// T-SQL's OPTION (...) is advice about how to run the query, and it hangs
// off the query the way FOR JSON does -- after every clause that names
// rows. The first consumer writes `WITH (NOLOCK) OPTION (RECOMPILE)`; the
// reference's own fixture is `OPTION(LABEL = 'foo')`.
func TestQueryHintOptions(t *testing.T) {
	for _, tc := range []struct{ name, sql, want string }{
		{"a flag", "SELECT col FROM t OPTION(RECOMPILE)",
			"SELECT col FROM t OPTION(RECOMPILE)"},
		{"a labelled query", "SELECT col FROM t OPTION(LABEL = 'foo')",
			"SELECT col FROM t OPTION(LABEL = 'foo')"},
		{"a value without an equals", "SELECT col FROM t OPTION(MAXDOP 2)",
			"SELECT col FROM t OPTION(MAXDOP 2)"},
		{"paired words", "SELECT col FROM t OPTION(HASH JOIN)",
			"SELECT col FROM t OPTION(HASH JOIN)"},
		{"several", "SELECT col FROM t OPTION(RECOMPILE, MAXDOP 2)",
			"SELECT col FROM t OPTION(RECOMPILE, MAXDOP 2)"},
		{"three words", "SELECT col FROM t OPTION(OPTIMIZE FOR UNKNOWN)",
			"SELECT col FROM t OPTION(OPTIMIZE FOR UNKNOWN)"},
		{"after a locking hint", "SELECT * FROM dbo.t WITH (NOLOCK) OPTION (RECOMPILE)",
			"SELECT * FROM dbo.t WITH (NOLOCK) OPTION(RECOMPILE)"},
		{"on an update", "UPDATE t SET c = 1 OPTION(RECOMPILE)",
			"UPDATE t SET c = 1 OPTION(RECOMPILE)"},
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
	e, err := ParseOne("SELECT col FROM t OPTION(LABEL = 'foo')", "tsql")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	options, _ := e.Args["options"].([]*Expression)
	if len(options) != 1 || options[0].Class != "QueryOption" {
		t.Fatalf("options = %v, want one QueryOption", options)
	}
	if this := options[0].This(); this == nil || this.Class != "Var" || this.Args["this"] != "LABEL" {
		t.Errorf("the option is %v, want a Var named LABEL", options[0].This())
	}
	if value, _ := options[0].Args["expression"].(*Expression); value == nil ||
		value.Class != "Literal" || value.Args["this"] != "foo" {
		t.Errorf("the value is %v, want the string foo", options[0].Args["expression"])
	}
	// A dialect that has no OPTION writes nothing of the kind, and refusing
	// is better than dropping the clause.
	if _, err := Generate(e, "postgres"); err == nil {
		t.Error("PostgreSQL wrote a T-SQL OPTION; it has none")
	}
	for _, sql := range []string{
		"SELECT 1 OPTION",
		"SELECT 1 OPTION (",
		"SELECT 1 OPTION (ZZZUNKNOWN)",
		"SELECT 1 OPTION (RECOMPILE",
	} {
		if _, err := ParseOne(sql, "tsql"); err == nil {
			t.Errorf("ParseOne(%q) was read; it should be refused", sql)
		}
	}
}

// The malformed OPTION shapes, held still because a half-read hint would put
// a wrong tree behind a statement that reads fine.
func TestQueryHintOptionsMalformed(t *testing.T) {
	for _, sql := range []string{
		"SELECT col FROM t OPTION",
		"SELECT col FROM t OPTION()",
		"SELECT col FROM t OPTION(RECOMPILE",
		"SELECT col FROM t OPTION(NOT_A_HINT)",
		"SELECT col FROM t OPTION(HASH)",
	} {
		if _, err := ParseOne(sql, "tsql"); err == nil {
			t.Errorf("ParseOne(%q) was read; it should be refused", sql)
		}
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
		// A second USING with nothing between it and the first belongs to
		// the OUTER join, not a repeat of the inner one: `a JOIN (b JOIN c
		// USING (id)) USING (id)`, read left to right with no parentheses at
		// all in the SQL itself.
		{"SELECT * FROM a JOIN b JOIN c USING (id) USING (id)",
			"SELECT * FROM a JOIN b JOIN c USING (id) USING (id)"},
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

// The same nesting TestJoinUsing covers for USING happens for ON: a second
// ON with nothing between it and the first belongs to the join that reads it
// SECOND -- the outer one -- not a repeat of the inner join's own condition.
func TestJoinOnNesting(t *testing.T) {
	sql := "SELECT 1 FROM a JOIN b JOIN c ON b.id = c.id ON a.id = b.id"
	e, err := ParseOne(sql, "")
	if err != nil {
		t.Fatalf("ParseOne(%q): %v", sql, err)
	}
	if got, err := Generate(e, ""); err != nil || got != sql {
		t.Errorf("got %q (%v), want %q", got, err, sql)
	}
	// The nested reading is tried and abandoned here: only one ON follows
	// the whole chain, which the INNER join keeps, leaving nothing for the
	// outer one -- so Databricks falls back to its own bare-join spelling
	// rather than the nested one this shape usually takes.
	bare := "SELECT 1 FROM a JOIN b JOIN c ON b.id = c.id"
	wantBare := "SELECT 1 FROM a JOIN b ON TRUE JOIN c ON b.id = c.id"
	eBare, err := ParseOne(bare, "databricks")
	if err != nil {
		t.Fatalf("ParseOne(%q): %v", bare, err)
	}
	if got, err := Generate(eBare, "databricks"); err != nil || got != wantBare {
		t.Errorf("got %q (%v), want %q", got, err, wantBare)
	}
}

// CONNECT BY walks a hierarchy, and START WITH may stand in front of it or
// after -- either way the reference dumps it first, and NOCYCLE stops the
// walk from looping rather than erroring over a cycle in it.
func TestConnectBy(t *testing.T) {
	for _, tc := range []struct{ sql, want string }{
		{`SELECT * FROM t CONNECT BY "nocycle" = 1`,
			`SELECT * FROM t CONNECT BY "nocycle" = 1`},
		{"SELECT * FROM t START WITH id = 1 CONNECT BY x = y",
			"SELECT * FROM t START WITH id = 1 CONNECT BY x = y"},
		// START WITH written AFTER CONNECT BY still dumps first.
		{"SELECT * FROM t CONNECT BY NOCYCLE x = y START WITH id = 1",
			"SELECT * FROM t START WITH id = 1 CONNECT BY NOCYCLE x = y"},
		// START is never an implicit alias when WITH follows -- that would
		// swallow the beginning of this very clause.
		{"SELECT * FROM t AS START START WITH id = 1 CONNECT BY x = y",
			"SELECT * FROM t AS START START WITH id = 1 CONNECT BY x = y"},
		// The same guard applies to a subquery's own WIDE alias reading.
		{"SELECT * FROM (SELECT 1) START WITH id = 1 CONNECT BY x = y",
			"SELECT * FROM (SELECT 1) START WITH id = 1 CONNECT BY x = y"},
	} {
		e, err := ParseOne(tc.sql, "")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		if got, err := Generate(e, ""); err != nil || got != tc.want {
			t.Errorf("%q wrote %q (%v), want %q", tc.sql, got, err, tc.want)
		}
	}
	// START WITH names where the walk begins; it does not say how to
	// continue it, and CONNECT BY is required even where START WITH stood
	// first. Each of the three conditions this clause takes is refused,
	// whole, where it is not one.
	for _, sql := range []string{
		"SELECT * FROM t START WITH id = 1",
		"SELECT * FROM t START WITH ) CONNECT BY x = y",
		"SELECT * FROM t CONNECT BY )",
		"SELECT * FROM t CONNECT BY x = y START WITH )",
	} {
		if _, err := ParseOne(sql, ""); err == nil {
			t.Errorf("ParseOne(%q) was read; it should be refused", sql)
		}
	}
}

// The SQL/JSON IS JSON predicate: a kind, and WITH/WITHOUT UNIQUE KEYS, each
// read as false or absent -- not defaulted -- where the word was not
// written. The kind specifically dumps as `false`, not as nothing, since the
// reference's own match-and-return short-circuits to the boolean itself.
func TestIsJSON(t *testing.T) {
	for _, tc := range []struct{ sql, want string }{
		{`SELECT js, js IS JSON AS "json?"`,
			`SELECT js, js IS JSON AS "json?"`},
		{`SELECT js IS JSON VALUE AS "scalar?"`,
			`SELECT js IS JSON VALUE AS "scalar?"`},
		{`SELECT js IS JSON ARRAY WITH UNIQUE KEYS AS "array w. UK?"`,
			`SELECT js IS JSON ARRAY WITH UNIQUE KEYS AS "array w. UK?"`},
		{"SELECT js IS JSON WITHOUT UNIQUE KEYS", "SELECT js IS JSON WITHOUT UNIQUE KEYS"},
		// The reference itself writes the negation this way: NOT wrapping
		// the whole Is, not IS NOT JSON.
		{"SELECT js IS NOT JSON", "SELECT NOT js IS JSON"},
	} {
		e, err := ParseOne(tc.sql, "")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		if got, err := Generate(e, ""); err != nil || got != tc.want {
			t.Errorf("%q wrote %q (%v), want %q", tc.sql, got, err, tc.want)
		}
	}
}

// A set operation's OWN operand may carry an alias, same as any other
// subquery does: `(SELECT 1) AS a UNION ALL (SELECT 2) AS b` names each side
// on its own, and the whole union is what a further wrap of parentheses
// would alias, not either side alone.
func TestSetOperationOperandAlias(t *testing.T) {
	sql := "SELECT * FROM ((SELECT 1) AS a UNION ALL (SELECT 2) AS b)"
	e, err := ParseOne(sql, "")
	if err != nil {
		t.Fatalf("ParseOne(%q): %v", sql, err)
	}
	if got, err := Generate(e, ""); err != nil || got != sql {
		t.Errorf("got %q (%v), want %q", got, err, sql)
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
	// A discrete count of ROWS, which DuckDB writes with the method spelled
	// out and the word ROWS after the number.
	for _, tc := range []struct{ sql, want string }{
		{"SELECT * FROM tbl USING SAMPLE 5", "SELECT * FROM tbl USING SAMPLE RESERVOIR (5 ROWS)"},
		{"SELECT * FROM tbl USING SAMPLE reservoir(50 ROWS) REPEATABLE (100)",
			"SELECT * FROM tbl USING SAMPLE RESERVOIR (50 ROWS) REPEATABLE (100)"},
		// A sample hanging off the TABLE is the same node under the other
		// word: DuckDB says USING SAMPLE for the query and TABLESAMPLE here.
		{"SELECT * FROM example TABLESAMPLE RESERVOIR (3 ROWS) REPEATABLE (82)",
			"SELECT * FROM example TABLESAMPLE RESERVOIR (3 ROWS) REPEATABLE (82)"},
	} {
		e, err := ParseOne(tc.sql, "duckdb")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		if got, err := Generate(e, "duckdb"); err != nil || got != tc.want {
			t.Errorf("%q wrote %q (%v), want %q", tc.sql, got, err, tc.want)
		}
	}
	// A row count can only be taken one way, so a method named beside one is
	// REPLACED by the reference -- which says something the statement did
	// not, and the port refuses instead.
	e, err := ParseOne("SELECT * FROM tbl USING SAMPLE 5 (bernoulli)", "duckdb")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if got, err := Generate(e, "duckdb"); err == nil {
		t.Errorf("wrote %q; DuckDB counts rows by RESERVOIR whatever it was told", got)
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

// SEARCH and CYCLE choose how a recursive CTE's rows are walked and how a
// repeat among them is caught, and share one node told apart by `kind`:
// CYCLE spells only the word, where SEARCH spells its kind and FIRST BY too.
func TestRecursiveWithSearchAndCycle(t *testing.T) {
	for _, sql := range []string{
		"WITH RECURSIVE search_tree(id, link, data) AS (SELECT 1, 2, 3) " +
			"SEARCH BREADTH FIRST BY id SET ordercol SELECT * FROM search_tree",
		"WITH RECURSIVE search_tree(id, link, data) AS (SELECT 1, 2, 3) " +
			"SEARCH DEPTH FIRST BY id SET ordercol SELECT * FROM search_tree",
		"WITH RECURSIVE search_graph(id, link, data, depth) AS " +
			"(SELECT g.id, g.link, g.data, 1 FROM graph AS g UNION ALL " +
			"SELECT g.id, g.link, g.data, sg.depth + 1 FROM graph AS g, search_graph AS sg WHERE g.id = sg.link) " +
			"CYCLE id SET is_cycle USING path SELECT * FROM search_graph",
	} {
		e, err := ParseOne(sql, "postgres")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", sql, err)
		}
		if got, err := Generate(e, "postgres"); err != nil || got != sql {
			t.Errorf("got %q (%v), want %q", got, err, sql)
		}
	}
	// Every name a CYCLE clause reads is refused, whole, where it is not one.
	for _, sql := range []string{
		"WITH RECURSIVE t(a) AS (SELECT 1) CYCLE 123 SET is_cycle SELECT * FROM t",
		"WITH RECURSIVE t(a) AS (SELECT 1) CYCLE id SET 123 SELECT * FROM t",
		"WITH RECURSIVE t(a) AS (SELECT 1) CYCLE id SET is_cycle USING 123 SELECT * FROM t",
	} {
		if _, err := ParseOne(sql, "postgres"); err == nil {
			t.Errorf("ParseOne(%q) was read; it should be refused", sql)
		}
	}
}

// PostgreSQL's MATERIALIZED/NOT MATERIALIZED hints whether a CTE is its own
// optimisation fence or inlined; the reference stores three states -- true,
// false and absent -- not two, so leaving the word out must dump differently
// from writing NOT MATERIALIZED.
func TestCTEMaterialized(t *testing.T) {
	for _, tc := range []struct{ sql, want string }{
		{"WITH t AS MATERIALIZED (SELECT 1) SELECT * FROM t",
			"WITH t AS MATERIALIZED (SELECT 1) SELECT * FROM t"},
		{"WITH t AS NOT MATERIALIZED (SELECT 1) SELECT * FROM t",
			"WITH t AS NOT MATERIALIZED (SELECT 1) SELECT * FROM t"},
		{"WITH t1 AS MATERIALIZED (SELECT 1), t2 AS NOT MATERIALIZED (SELECT 2) SELECT * FROM t1, t2",
			"WITH t1 AS MATERIALIZED (SELECT 1), t2 AS NOT MATERIALIZED (SELECT 2) SELECT * FROM t1, t2"},
		{"WITH t AS (SELECT 1) SELECT * FROM t",
			"WITH t AS (SELECT 1) SELECT * FROM t"},
	} {
		e, err := ParseOne(tc.sql, "postgres")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		if got, err := Generate(e, "postgres"); err != nil || got != tc.want {
			t.Errorf("%q wrote %q (%v), want %q", tc.sql, got, err, tc.want)
		}
	}
	plain, err := ParseOne("WITH t AS (SELECT 1) SELECT * FROM t", "postgres")
	if err != nil {
		t.Fatal(err)
	}
	with, _ := plain.Args["with_"].(*Expression)
	cte := with.Args["expressions"].([]*Expression)[0]
	if cte.Args["materialized"] != nil {
		t.Errorf("materialized = %v on a plain CTE, want it absent", cte.Args["materialized"])
	}
}

// TestCTEWithUsingKey covers DuckDB's dedup key on a RECURSIVE CTE, written
// between the alias's own column list and AS: `tbl(a, b) USING KEY (a)`.
func TestCTEWithUsingKey(t *testing.T) {
	for _, sql := range []string{
		"WITH RECURSIVE tbl(a, b) USING KEY (a) AS (SELECT a, b FROM x) SELECT * FROM tbl",
		"WITH RECURSIVE tbl(a, b) USING KEY (a, b) AS (SELECT a, b FROM x) SELECT * FROM tbl",
	} {
		e, err := ParseOne(sql, "duckdb")
		if err != nil {
			t.Errorf("ParseOne(%q): %v", sql, err)
			continue
		}
		if got, err := Generate(e, "duckdb"); err != nil || got != sql {
			t.Errorf("%s wrote %q (%v)", sql, got, err)
		}
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
		// A field with no name of its own takes the key from its OWN `this`
		// when that is an Identifier -- a bare column names itself -- and a
		// positional one, `_0`, `_1`..., when it has none at all: a literal,
		// or `*COLUMNS(...)`, DuckDB's own unpack of every column a pattern
		// matches into the call it stands inside.
		{"a bare column names itself", "SELECT STRUCT(x, x AS y)", "SELECT {'x': x, 'y': x}"},
		{"a bare qualified column names itself", "SELECT STRUCT(a.b, 1)", "SELECT {'b': a.b, '_1': 1}"},
		{"a bare literal has no name of its own", "SELECT STRUCT_PACK(1)", "SELECT {'_0': 1}"},
		{"COLUMNS unpacked into a call", "SELECT COALESCE(*COLUMNS(*)) FROM t",
			"SELECT COALESCE(*COLUMNS(*)) FROM t"},
		{"COLUMNS unpacked into a struct field", `SELECT STRUCT_PACK(*COLUMNS('m\d')) FROM m`,
			`SELECT {'_0': *COLUMNS('m\d')} FROM m`},
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
	// The same again where the dot follows something that is NOT a plain
	// name -- NULL, a bracket, a call's own result -- which is a different
	// code path from the one above. The generator fuzzer found this one
	// writing `NULL.CASE WHEN ... END`, also not SQL.
	iffAfterNull, err := ParseOne("SELECT NULL.IF(1, 0)", "duckdb")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if got, err := Generate(iffAfterNull, "duckdb"); err != nil || got != "SELECT NULL.IF(1, 0)" {
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
		// The base window may also be EXTENDED, inside the parens rather than
		// bare: the name comes first, ahead of whatever this instance adds
		// of its own.
		{"an OVER extends the named window", "duckdb",
			"SELECT SUM(x) OVER (w ORDER BY y) FROM t WINDOW w AS (PARTITION BY z)",
			"SELECT SUM(x) OVER (w ORDER BY y) FROM t WINDOW w AS (PARTITION BY z)"},
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
		// Postgres slices a table by an EXPRESSION over the key rather than
		// by a list of columns, so the property need carry no list at all.
		{"partitioned by a field", "", "CREATE TABLE t (a INT) PARTITIONED BY b",
			"CREATE TABLE t (a INT) WITH (PARTITIONED_BY=b)"},
		// A table made from another table rather than from columns or a
		// query. SHALLOW says the rows are shared until one side writes.
		{"a shallow clone", "databricks", "CREATE TABLE target SHALLOW CLONE source",
			"CREATE TABLE target SHALLOW CLONE source"},
		{"a clone", "databricks", "CREATE TABLE target CLONE source",
			"CREATE TABLE target CLONE source"},
		// Postgres's `PARTITION OF` says the table holds one slice of
		// another's rows, in one of three spellings for which slice.
		{"a default partition", "postgres", "CREATE TABLE p PARTITION OF cities DEFAULT",
			"CREATE TABLE p PARTITION OF cities DEFAULT"},
		{"a hash partition", "postgres",
			"CREATE TABLE p PARTITION OF customers FOR VALUES WITH (MODULUS 3, REMAINDER 2)",
			"CREATE TABLE p PARTITION OF customers FOR VALUES WITH (MODULUS 3, REMAINDER 2)"},
		{"a range partition", "postgres",
			"CREATE TABLE p PARTITION OF m FOR VALUES FROM ('2016-07-01') TO ('2016-08-01')",
			"CREATE TABLE p PARTITION OF m FOR VALUES FROM ('2016-07-01') TO ('2016-08-01')"},
		// The ends of the key's ORDER rather than values in it: words, not
		// columns, so that they refer to nothing.
		{"a range with open ends", "postgres",
			"CREATE TABLE p PARTITION OF m FOR VALUES FROM (MINVALUE, MINVALUE) TO (2016, 11)",
			"CREATE TABLE p PARTITION OF m FOR VALUES FROM (MINVALUE, MINVALUE) TO (2016, 11)"},
		{"a range with a closed top", "postgres",
			"CREATE TABLE p PARTITION OF m FOR VALUES FROM (2016, 11) TO (MAXVALUE, MAXVALUE)",
			"CREATE TABLE p PARTITION OF m FOR VALUES FROM (2016, 11) TO (MAXVALUE, MAXVALUE)"},
		{"a list partition", "postgres",
			"CREATE TABLE p PARTITION OF cities FOR VALUES IN ('a', 'b')",
			"CREATE TABLE p PARTITION OF cities FOR VALUES IN ('a', 'b')"},
		// The parent may be named with the columns this slice OVERRIDES --
		// a name and a constraint, with no type restated.
		{"a partition that overrides a column", "postgres",
			"CREATE TABLE p PARTITION OF m (unitsales DEFAULT 0) FOR VALUES IN (1)",
			"CREATE TABLE p PARTITION OF m (unitsales DEFAULT 0) FOR VALUES IN (1)"},
		{"a partition that adds a constraint", "postgres",
			"CREATE TABLE p PARTITION OF cities (CONSTRAINT c CHECK (city_id <> 0)) " +
				"FOR VALUES IN ('a', 'b')",
			"CREATE TABLE p PARTITION OF cities (CONSTRAINT c CHECK (city_id <> 0)) " +
				"FOR VALUES IN ('a', 'b')"},
		// A table may be a slice of its parent AND be sliced further itself.
		{"a partition that is partitioned", "postgres",
			"CREATE TABLE p PARTITION OF cities FOR VALUES IN ('a') PARTITION BY RANGE(population)",
			"CREATE TABLE p PARTITION OF cities FOR VALUES IN ('a') PARTITION BY RANGE(population)"},
		// A column need not name a type at all.
		{"a typeless column", "postgres", "CREATE TABLE t (a DEFAULT 0)",
			"CREATE TABLE t (a DEFAULT 0)"},
		// A TYPE names a shape rather than a place to put rows: the values it
		// may take, or the fields it is made of.
		{"an enum type", "postgres", "CREATE TYPE mood AS ENUM ('sad', 'ok', 'happy')",
			"CREATE TYPE mood AS ENUM ('sad', 'ok', 'happy')"},
		// PostgreSQL writes an ENUM apart from every other parameterised
		// type: a space in front of the list, and the parentheses even when
		// the list is empty.
		{"an enum of nothing", "postgres", "CREATE TYPE mood AS ENUM ()",
			"CREATE TYPE mood AS ENUM ()"},
		{"a qualified enum", "postgres", "CREATE TYPE public.mood AS ENUM ('sad', 'ok')",
			"CREATE TYPE public.mood AS ENUM ('sad', 'ok')"},
		// Everywhere else, a bare ENUM with no members is a type like any
		// other with nothing between its parentheses: no parentheses at all,
		// not the empty ones PostgreSQL's CREATE TYPE always carries. The
		// generator fuzzer found this port writing `ENUM()` here too, which
		// this port's own CAST parser cannot read back.
		{"a bare enum cast, no members", "databricks", "SELECT CAST(0 AS ENUM)",
			"SELECT CAST(0 AS ENUM)"},
		{"a composite type", "postgres",
			"CREATE TYPE inventory_item AS (name TEXT, supplier_id INT, price DECIMAL)",
			"CREATE TYPE inventory_item AS (name TEXT, supplier_id INT, price DECIMAL)"},
		// DuckDB's MACRO is a function under another word, and carries one
		// thing a function does not: several bodies, one per parameter list.
		{"a macro", "duckdb", "CREATE MACRO foo(a TINYINT) AS a = 127",
			"CREATE MACRO foo(a TINYINT) AS a = 127"},
		{"a macro with two bodies", "duckdb", "CREATE MACRO add_x (a, b) AS a + b, (a, b, c) AS a + b + c",
			"CREATE MACRO add_x (a, b) AS a + b, (a, b, c) AS a + b + c"},
		// The parameters written after the NAME move onto the overload that
		// used them, leaving the macro naming itself and nothing else -- so
		// it is written without parentheses, not with empty ones.
		{"a macro with three", "duckdb",
			"CREATE OR REPLACE MACRO foo (a) AS a, (a, b) AS a + b, (a, b, c) AS a + b + c",
			"CREATE OR REPLACE MACRO foo (a) AS a, (a, b) AS a + b, (a, b, c) AS a + b + c"},
		{"overloads that differ by type", "duckdb",
			"CREATE MACRO foo (a TINYINT) AS a + 1, (a INT) AS a + 2",
			"CREATE MACRO foo (a TINYINT) AS a + 1, (a INT) AS a + 2"},
		// A body written as a TABLE is a query, and the parentheses around it
		// stay the Subquery they were written as.
		{"overloads over tables", "duckdb",
			"CREATE MACRO tbl (a) AS TABLE (SELECT a AS x), (a, b) AS TABLE (SELECT a AS x, b AS y)",
			"CREATE MACRO tbl (a) AS TABLE (SELECT a AS x), (a, b) AS TABLE (SELECT a AS x, b AS y)"},
		// A trigger names itself and then says everything about itself in
		// properties: when it fires, on what, over which rows, and what it
		// runs. The word after EXECUTE is not kept -- PROCEDURE and FUNCTION
		// name the same thing, and the reference writes only the one.
		{"a trigger", "postgres",
			"CREATE TRIGGER t BEFORE INSERT ON users FOR EACH ROW EXECUTE PROCEDURE log_changes()",
			"CREATE TRIGGER t BEFORE INSERT ON users FOR EACH ROW EXECUTE FUNCTION LOG_CHANGES()"},
		{"a trigger with quoted names", "postgres",
			`CREATE TRIGGER "MyTrigger" BEFORE INSERT ON "MyTable" FOR EACH ROW ` +
				"EXECUTE FUNCTION MYFUNCTION()",
			`CREATE TRIGGER "MyTrigger" BEFORE INSERT ON "MyTable" FOR EACH ROW ` +
				"EXECUTE FUNCTION MYFUNCTION()"},
		// Several events joined by OR, an UPDATE naming the columns it
		// watches, and a condition on which rows it fires for.
		{"a trigger over several events", "postgres",
			"CREATE TRIGGER t AFTER UPDATE OF a, b OR DELETE ON x FOR EACH STATEMENT " +
				"WHEN (a > 1) EXECUTE FUNCTION f()",
			"CREATE TRIGGER t AFTER UPDATE OF a, b OR DELETE ON x FOR EACH STATEMENT " +
				"WHEN (a > 1) EXECUTE FUNCTION F()"},
		{"a trigger instead of", "postgres",
			"CREATE TRIGGER t INSTEAD OF INSERT ON v FOR EACH ROW EXECUTE FUNCTION f()",
			"CREATE TRIGGER t INSTEAD OF INSERT ON v FOR EACH ROW EXECUTE FUNCTION F()"},
		// A column may be COMPUTED from the others rather than stored. It
		// names no type -- the type is whatever the expression yields.
		{"a computed column", "tsql", "CREATE TABLE t (a INT, b AS (a * 2) PERSISTED NOT NULL)",
			"CREATE TABLE t (a INTEGER, b AS (a * 2) PERSISTED NOT NULL)"},
		{"computed columns, persisted and not", "tsql",
			"CREATE TABLE tbl (a AS (x + 1) PERSISTED, b AS (y + 2), c AS (y / 3) PERSISTED NOT NULL)",
			"CREATE TABLE tbl (a AS (x + 1) PERSISTED, b AS (y + 2), c AS (y / 3) PERSISTED NOT NULL)"},
		// T-SQL brackets a TYPE name the same way it brackets a column name,
		// so the word arrives as an identifier. Quoted or bare, it names the
		// same type -- and it is written back bare.
		{"a bracketed type", "tsql", "CREATE TABLE t([a] [int])", "CREATE TABLE t ([a] INTEGER)"},
		{"bracketed types with a size", "tsql",
			"CREATE TABLE test_table([ID] [BIGINT] NOT NULL,[EffectiveFrom] [DATETIME2] (3) NOT NULL)",
			"CREATE TABLE test_table ([ID] BIGINT NOT NULL, [EffectiveFrom] DATETIME2(3) NOT NULL)"},
		{"a bracketed type in a cast", "tsql", "SELECT CAST(x AS [int])", "SELECT CAST(x AS INTEGER)"},
		// The members of a nested type may name the collation their values
		// are compared under, and a struct's fields are column definitions,
		// so they may say what a column may.
		{"a collated array member", "databricks",
			"CREATE TABLE foo (my_arr ARRAY<STRING COLLATE UTF8_BINARY>)",
			"CREATE TABLE foo (my_arr ARRAY<STRING COLLATE UTF8_BINARY>)"},
		{"a collated map member", "databricks",
			"CREATE TABLE foo (m MAP<STRING, STRING COLLATE UTF8_BINARY>)",
			"CREATE TABLE foo (m MAP<STRING, STRING COLLATE UTF8_BINARY>)"},
		{"a struct field with a comment", "databricks",
			"CREATE TABLE t (c STRUCT<interval: DOUBLE COMMENT 'aaa'>)",
			"CREATE TABLE t (c STRUCT<interval: DOUBLE COMMENT 'aaa'>)"},
		{"a struct field that may not be null", "databricks",
			"CREATE TABLE t (a STRUCT<x: INT NOT NULL>)",
			"CREATE TABLE t (a STRUCT<x: INT NOT NULL>)"},
		// VIRTUAL is the opposite of PERSISTED -- the value is recomputed --
		// and neither word is written where the flag is false.
		{"a computed column that is virtual", "tsql", "CREATE TABLE t (b AS (a) VIRTUAL)",
			"CREATE TABLE t (b AS (a))"},
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
	// T-SQL has no CREATE TABLE AS SELECT and says the same thing another
	// way: `SELECT * INTO t FROM (<query>) AS temp`. The query is wrapped so
	// the columns it names are the ones the new table gets, and the wrapper
	// takes a name the reference supplies rather than one the statement
	// carried.
	for _, tc := range []struct{ sql, want string }{
		{"CREATE TABLE t AS SELECT 1", "SELECT * INTO t FROM (SELECT 1 AS [1]) AS temp"},
		{"CREATE TABLE schema.table AS SELECT a, id FROM (SELECT a, id FROM t) x",
			"SELECT * INTO schema.table FROM (SELECT a AS a, id AS id FROM " +
				"(SELECT a AS a, id AS id FROM t) AS x) AS temp"},
	} {
		e, err := ParseOne(tc.sql, "tsql")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		if got, err := Generate(e, "tsql"); err != nil || got != tc.want {
			t.Errorf("%q wrote %q (%v), want %q", tc.sql, got, err, tc.want)
		}
	}
	// A dialect that HAS the statement writes it as it stands.
	e, err := ParseOne("CREATE TABLE t AS SELECT 1", "tsql")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if got, err := Generate(e, "duckdb"); err != nil || got != "CREATE TABLE t AS SELECT 1" {
		t.Errorf("DuckDB wrote %q (%v)", got, err)
	}
	// Anything the port cannot read WHOLE is refused, not read in part: a
	// dropped constraint changes what the table is.
	for _, sql := range []string{
		"CREATE TABLE z (a INT GENERATED ALWAYS AS SOMETHING)",
		"CREATE TABLE t (a INT",
		"CREATE TABLE t (a INT) DISTRIBUTED BY HASH (b)",
		// Every way a property can run out or say something the port cannot
		// read. Each is refused whole rather than read in part.
		"CREATE TABLE t (a INT) FORMAT",
		"CREATE TABLE t (a INT) FORMAT = 1",
		"CREATE TABLE t (a INT) CLUSTER BY c",
		"CREATE TABLE t (a INT) CLUSTER BY (c",
		"CREATE TABLE t (a INT) INHERITS (t1",
		"CREATE TABLE t (a INT) WITH (a = 1",
		"CREATE TABLE t (a INT) WITH (1 = 1)",
		"CREATE TABLE t (a INT) WITH (a)",
		"CREATE TABLE t (a INT) WITH (a =",
		"CREATE TABLE t (a INT) WITH (",
	} {
		if _, err := ParseOne(sql, ""); err == nil {
			t.Errorf("ParseOne(%q) was read; it should be refused", sql)
		}
	}
}

// TestTableProperties covers the words a CREATE may say about the thing it
// makes. Each word's node and the shape of what follows it are generated, so
// what is pinned here is the four shapes and the two places they are written.
func TestTableProperties(t *testing.T) {
	for _, c := range []struct{ name, sql, dialect, want string }{
		// POST_WITH: gathered into one wrapped list, under a word that is the
		// dialect's own.
		{"a value inside a WITH list", "CREATE TABLE z WITH (FORMAT='parquet') AS SELECT 1", "",
			"CREATE TABLE z WITH (FORMAT='parquet') AS SELECT 1"},
		{"a key and value with no word of its own", "CREATE TABLE t TBLPROPERTIES ('a.b'=15)",
			"databricks", "CREATE TABLE t TBLPROPERTIES ('a.b'=15)"},
		{"a schema inside a WITH list", "CREATE TABLE z (z INT) WITH (PARTITIONED_BY=(x INT))", "",
			"CREATE TABLE z (z INT) WITH (PARTITIONED_BY=(x INT))"},
		// POST_SCHEMA: each stands on its own after the columns.
		{"a wrapped list of columns", "CREATE TABLE t CLUSTER BY (col1, col2)", "databricks",
			"CREATE TABLE t CLUSTER BY (col1, col2)"},
		// AUTO and NONE take the place of the column list entirely -- the
		// word IS the clustering, not a name it sorts by.
		{"cluster by auto", "CREATE TABLE t CLUSTER BY AUTO", "databricks",
			"CREATE TABLE t CLUSTER BY AUTO"},
		{"cluster by none", "CREATE TABLE t CLUSTER BY NONE", "databricks",
			"CREATE TABLE t CLUSTER BY NONE"},
		{"a wrapped list of tables", "CREATE TABLE t (c CHAR(2)) INHERITS (t1, t2)", "postgres",
			"CREATE TABLE t (c CHAR(2)) INHERITS (t1, t2)"},
		// A name written with no type keeps no ColumnDef around it: the same
		// property is a Schema of Identifiers here and of ColumnDefs above.
		// The word it comes back under is the dialect's, not the one it was
		// written with -- both spellings are the same property.
		{"a schema of bare names", "CREATE TABLE t (a INT) PARTITIONED BY (b)", "",
			"CREATE TABLE t (a INT) WITH (PARTITIONED_BY=(b))"},
		{"a table name", "CREATE TABLE t LIKE other", "", "CREATE TABLE t LIKE other"},
	} {
		t.Run(c.name, func(t *testing.T) {
			tree, err := ParseOne(c.sql, c.dialect)
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", c.sql, err)
			}
			got, gerr := Generate(tree, c.dialect)
			if gerr != nil {
				t.Fatalf("Generate(%q): %v", c.sql, gerr)
			}
			if got != c.want {
				t.Errorf("%s\n got  %s\n want %s", c.sql, got, c.want)
			}
		})
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
	// Databricks gives a temporary TABLE a storage format it was never given,
	// and writes it a SECOND time where the statement supplied one. That is
	// the whole of the difference, so it is written rather than refused.
	for _, tc := range []struct{ sql, want string }{
		{"CREATE TEMPORARY TABLE x (a INT)", "CREATE TEMPORARY TABLE x (a INT) USING PARQUET"},
		{"CREATE TEMPORARY TABLE t (a INT) USING PARQUET",
			"CREATE TEMPORARY TABLE t (a INT) USING PARQUET USING PARQUET"},
		// The format is a WORD in Databricks and a value elsewhere.
		{"CREATE TABLE t (a INT) USING PARQUET", "CREATE TABLE t (a INT) USING PARQUET"},
	} {
		e, err := ParseOne(tc.sql, "databricks")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		if got, err := Generate(e, "databricks"); err != nil || got != tc.want {
			t.Errorf("%q wrote %q (%v), want %q", tc.sql, got, err, tc.want)
		}
	}
	if got, err := Generate(ParseOrFail(t, "CREATE TABLE z WITH (FORMAT='parquet') AS SELECT 1", ""),
		""); err != nil || got != "CREATE TABLE z WITH (FORMAT='parquet') AS SELECT 1" {
		t.Errorf("neutral wrote %q (%v)", got, err)
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
		// Databricks overwrites a slice of the table rather than appending
		// to it, naming the slice by a condition or by the columns that
		// identify a row.
		{"replace where", "databricks", "INSERT INTO a REPLACE WHERE cond VALUES (1), (2)",
			"INSERT INTO a REPLACE WHERE cond VALUES (1), (2)"},
		{"replace where, over a query", "databricks",
			"INSERT INTO t REPLACE WHERE a = 2 (SELECT * FROM src)",
			"INSERT INTO t REPLACE WHERE a = 2 (SELECT * FROM src)"},
		{"replace using", "databricks",
			"INSERT INTO target REPLACE USING (c1, c2) SELECT c1, c2 FROM source",
			"INSERT INTO target REPLACE USING (c1, c2) SELECT c1, c2 FROM source"},
		{"replace where, behind named columns", "databricks",
			"INSERT INTO t (a) REPLACE WHERE a = 1 VALUES (1)",
			"INSERT INTO t (a) REPLACE WHERE a = 1 VALUES (1)"},
		{"replace where, behind a cte", "databricks",
			"WITH s AS (SELECT * FROM src) INSERT INTO t REPLACE WHERE a = 1 SELECT * FROM s",
			"WITH s AS (SELECT * FROM src) INSERT INTO t REPLACE WHERE a = 1 SELECT * FROM s"},
		// DuckDB matches the query's columns to the target's by name.
		{"by name", "duckdb", "INSERT INTO x BY NAME SELECT 1 AS y",
			"INSERT INTO x BY NAME SELECT 1 AS y"},
		// `DEFAULT VALUES` writes a row that names no values, so the
		// statement has no body at all -- the flag stands in place of one.
		{"default values", "postgres", "INSERT INTO t DEFAULT VALUES",
			"INSERT INTO t DEFAULT VALUES"},
		{"default values, with a returning", "duckdb",
			"INSERT INTO t DEFAULT VALUES RETURNING (c1)",
			"INSERT INTO t DEFAULT VALUES RETURNING (c1)"},
		// But the reference only WRITES the flag where RETURNING comes last,
		// and drops it silently everywhere else. T-SQL loses the whole
		// clause, and the port loses it too.
		{"default values, dropped", "tsql", "INSERT INTO t DEFAULT VALUES", "INSERT INTO t"},
		// T-SQL's OUTPUT writes the rows somewhere as well as returning
		// them: a table variable, read the same way one is anywhere else,
		// or a plain name. No other dialect's RETURNING takes one at all.
		{"output into a table variable", "tsql",
			"UPDATE x SET y = 1 OUTPUT x.a, x.b INTO @y FROM y",
			"UPDATE x SET y = 1 OUTPUT x.a, x.b INTO @y FROM y"},
		{"output into a plain name", "tsql",
			"INSERT INTO x (y) OUTPUT x.a, x.b INTO l SELECT * FROM z",
			"INSERT INTO x (y) OUTPUT x.a, x.b INTO l SELECT * FROM z"},
		// Postgres names the target again so ON CONFLICT can refer to it.
		{"an aliased target", "postgres",
			"INSERT INTO newtable AS t(a, b, c) VALUES (1, 2, 3) " +
				"ON CONFLICT(c) DO UPDATE SET a = t.a + 1 WHERE t.a < 1",
			"INSERT INTO newtable AS t(a, b, c) VALUES (1, 2, 3) " +
				"ON CONFLICT(c) DO UPDATE SET a = t.a + 1 WHERE t.a < 1"},
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
	// T-SQL writes only two parts of a three-part name, dropping the catalog.
	// It is the dialect's naming rule rather than the statement's, and the
	// same rule applies to a CREATE.
	for _, tc := range []struct{ sql, want string }{
		{"DROP VIEW a.b.c", "DROP VIEW b.c"},
		{"DROP VIEW a.b.c, a.b.d", "DROP VIEW b.c, b.d"},
	} {
		e, err := ParseOne(tc.sql, "tsql")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		if got, err := Generate(e, "tsql"); err != nil || got != tc.want {
			t.Errorf("%q wrote %q (%v), want %q", tc.sql, got, err, tc.want)
		}
	}
	for _, sql := range []string{
		"INSERT INTO t", "INSERT t VALUES (1)", "INSERT INTO t VALUES 1",
		"INSERT INTO t (a VALUES (1)", "DROP TABLE",
		// A kind the reference has no CREATABLE for is raw text there and a
		// refusal here.
		"DROP NOTHING x",
		"DROP TABLE t EXTRA",
	} {
		if _, err := ParseOne(sql, ""); err == nil {
			t.Errorf("ParseOne(%q) was read; it should be refused", sql)
		}
	}
	// OUTPUT's INTO with nothing after it is left to name itself: the
	// reference silently drops the whole clause and reads as if INTO had
	// not been written at all, a leniency this port does not reproduce.
	if _, err := ParseOne("UPDATE x SET y = 1 OUTPUT x.a INTO", "tsql"); err == nil {
		t.Error("UPDATE x SET y = 1 OUTPUT x.a INTO was read; it should be refused")
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
		"CREATE TABLE z (a INT GENERATED SOMEHOW AS IDENTITY)",
		"CREATE TABLE z (a INT GENERATED ALWAYS IDENTITY)",
		"CREATE TABLE z (a INT GENERATED BY DEFAULT AS (1))",
		"CREATE TABLE z (a INT GENERATED ALWAYS AS IDENTITY (WOBBLE))",
		"CREATE TABLE z (a INT CHECK a > 0)",
		"CREATE TABLE z (a INT CHECK (a > 0)",
		"CREATE TABLE z (a INT CHARACTER SET)",
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
	// A column that says what it TRACKS rather than how it is filled: the two
	// ends of the period a row was current for, and whether the column is
	// kept out of a `SELECT *`.
	for _, c := range []struct{ sql, want string }{
		{"CREATE TABLE t (a DATETIME2(2) GENERATED ALWAYS AS ROW START NOT NULL)",
			"CREATE TABLE t (a DATETIME2(2) GENERATED ALWAYS AS ROW START NOT NULL)"},
		{"CREATE TABLE t (a DATETIME2(2) GENERATED ALWAYS AS ROW END NOT NULL)",
			"CREATE TABLE t (a DATETIME2(2) GENERATED ALWAYS AS ROW END NOT NULL)"},
		{"CREATE TABLE t (a DATETIME2(2) GENERATED ALWAYS AS ROW START HIDDEN NOT NULL)",
			"CREATE TABLE t (a DATETIME2(2) GENERATED ALWAYS AS ROW START HIDDEN NOT NULL)"},
	} {
		tree, err := ParseOne(c.sql, "tsql")
		if err != nil {
			t.Errorf("ParseOne(%q): %v", c.sql, err)
			continue
		}
		got, gerr := Generate(tree, "tsql")
		if gerr != nil {
			t.Errorf("Generate(%q): %v", c.sql, gerr)
			continue
		}
		if got != c.want {
			t.Errorf("%s\n got  %s\n want %s", c.sql, got, c.want)
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
	idx, err := ParseOne("ALTER INDEX i RENAME TO j", "postgres")
	if err != nil {
		t.Fatalf("ALTER INDEX: %v", err)
	}
	if got, err := Generate(idx, "postgres"); err != nil || got != "ALTER INDEX i RENAME TO j" {
		t.Errorf("ALTER INDEX wrote %q (%v)", got, err)
	}
	for _, sql := range []string{
		"ALTER TABLE t SET TBLPROPERTIES ('a' = 'b')",
		"ALTER TABLE t ALTER COLUMN a SET NOT NULL",
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
		// MySQL's Multiple-Table UPDATE reads the target exactly as a FROM
		// clause does -- a comma is a join like any other -- and joins land
		// on the first table rather than on the statement itself.
		{"multiple tables, comma and explicit joined", "",
			"UPDATE t1 AS a, t2 AS b, t3 AS c LEFT JOIN t4 AS d ON c.id = d.id SET a.x = 1",
			"UPDATE t1 AS a, t2 AS b, t3 AS c LEFT JOIN t4 AS d ON c.id = d.id SET a.x = 1"},
		{"a plain delete", "", "DELETE FROM y", "DELETE FROM y"},
		{"with a where", "", "DELETE FROM x WHERE y > 1", "DELETE FROM x WHERE y > 1"},
		{"using", "", "DELETE FROM event USING sales AS s WHERE event.eventid = s.eventid",
			"DELETE FROM event USING sales AS s WHERE event.eventid = s.eventid"},
		// `USING a, b` is a comma JOIN on the first table, not a second entry.
		{"using two", "", "DELETE FROM event USING sales, bla WHERE event.eventid = sales.eventid",
			"DELETE FROM event USING sales, bla WHERE event.eventid = sales.eventid"},
		{"delete returning", "postgres", "DELETE FROM x WHERE y > 1 RETURNING a",
			"DELETE FROM x WHERE y > 1 RETURNING a"},
		// MySQL's Multiple-Table DELETE names, before the FROM, which of the
		// tables joined there actually lose rows. T-SQL's OUTPUT sits in
		// that very spot too -- straight after the verb -- and is the one
		// place T-SQL puts it that its own parser can read back.
		{"multiple-table names the target before FROM", "", "DELETE x, y FROM t",
			"DELETE x, y FROM t"},
		{"output straight after the verb", "tsql", "DELETE x OUTPUT x.a FROM z",
			"DELETE x OUTPUT x.a FROM z"},
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
		{"", "DELETE FROM 1"},
		{"", "DELETE FROM x USING 1"},
		{"", "DELETE FROM x WHERE FROM"},
		{"postgres", "DELETE FROM x RETURNING FROM"},
		{"", "MERGE INTO 1 USING s ON a = b WHEN MATCHED THEN DELETE"},
		{"", "MERGE INTO t USING 1 ON a = b WHEN MATCHED THEN DELETE"},
		{"", "MERGE INTO t USING s ON FROM WHEN MATCHED THEN DELETE"},
		{"", "MERGE INTO t USING s USING (FROM) WHEN MATCHED THEN DELETE"},
		{"", "MERGE INTO t USING s ON a = b WHEN MATCHED AND FROM THEN DELETE"},
		{"", "MERGE INTO t USING s ON a = b WHEN MATCHED THEN UPDATE SET FROM"},
		{"", "MERGE INTO t USING s ON a = b WHEN NOT MATCHED THEN INSERT (FROM) VALUES (1)"},
		{"", "MERGE INTO t USING s ON a = b WHEN NOT MATCHED THEN INSERT (a) VALUES (FROM)"},
		{"postgres", "MERGE INTO t USING s ON a = b WHEN MATCHED THEN DELETE RETURNING FROM"},
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
		// CHECK is the one keyword here that is also an ordinary word: only
		// a `(` right after it opens the constraint, and a bare `check` --
		// with no parenthesised condition following -- names a COLUMN.
		{"check names a column, not a constraint", "", "CREATE TABLE t (a VARCHAR, check INT)",
			"CREATE TABLE t (a VARCHAR, check INT)"},
		{"check as the only column", "", "CREATE TABLE t (check VARCHAR)",
			"CREATE TABLE t (check VARCHAR)"},
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
		"ALTER TABLE t ADD PRIMARY KEY (x, y) NOT ENFORCEABLE",
	} {
		if _, err := ParseOne(sql, ""); err == nil {
			t.Errorf("ParseOne(%q) was read; it should be refused", sql)
		}
	}
	// The words a key may carry after it, and the condition a CHECK may say
	// is enforced. Both are read by the vocabulary the reference keeps for a
	// REFERENCES, which is where the port already had the reader.
	for _, c := range []struct{ sql, want string }{
		{"ALTER TABLE t ADD PRIMARY KEY (x, y) NOT ENFORCED",
			"ALTER TABLE t ADD PRIMARY KEY (x, y) NOT ENFORCED"},
		{"ALTER TABLE t ADD CONSTRAINT c PRIMARY KEY (x) NOT ENFORCED DEFERRABLE INITIALLY DEFERRED NORELY",
			"ALTER TABLE t ADD CONSTRAINT c PRIMARY KEY (x) NOT ENFORCED DEFERRABLE INITIALLY DEFERRED NORELY"},
		{"ALTER TABLE t ADD CONSTRAINT c CHECK (id > 1) ENFORCED",
			"ALTER TABLE t ADD CONSTRAINT c CHECK (id > 1) ENFORCED"},
	} {
		tree, err := ParseOne(c.sql, "")
		if err != nil {
			t.Errorf("ParseOne(%q): %v", c.sql, err)
			continue
		}
		got, gerr := Generate(tree, "")
		if gerr != nil {
			t.Errorf("Generate(%q): %v", c.sql, gerr)
			continue
		}
		if got != c.want {
			t.Errorf("%s\n got  %s\n want %s", c.sql, got, c.want)
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
		{"no sql", "databricks",
			"CREATE FUNCTION f() RETURNS INT NO SQL RETURN 1",
			"CREATE FUNCTION f() RETURNS INT NO SQL RETURN 1"},
		// DETERMINISTIC is the standard SQL word for what IMMUTABLE says, and
		// the reference folds it into that property rather than keeping a
		// class of its own -- so it does not write back as itself.
		{"deterministic folds into immutable", "databricks",
			"CREATE FUNCTION f() RETURNS INT DETERMINISTIC RETURN 1",
			"CREATE FUNCTION f() RETURNS INT IMMUTABLE RETURN 1"},
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

// TestAQuotedAtOrDollarParameter covers the same quoted-name shape one level
// up: `@"x"` and `$"foo"` open a parameter or a placeholder exactly as their
// bare-word spellings do, the quotes just dropped -- or kept as an
// Identifier's own, where the class is one that carries one at all.
func TestAQuotedAtOrDollarParameter(t *testing.T) {
	for _, tc := range []struct{ dialect, sql, want string }{
		{"postgres", `SELECT @"x"`, `SELECT $"x"`},
		{"duckdb", `SELECT $"foo"`, "SELECT $foo"},
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
		"CREATE INDEX abc ON t",
		"CREATE INDEX abc ON t(a",
		"CREATE INDEX abc ON t USING",
		"CREATE TEMPORARY INDEX abc ON t(a)",
	} {
		if _, err := ParseOne(sql, ""); err == nil {
			t.Errorf("ParseOne(%q) was read; it should be refused", sql)
		}
	}
	// The method an index is built with, the rows it covers, and the operator
	// class one of its columns is indexed with.
	for _, c := range []struct{ sql, want string }{
		{"CREATE INDEX abc ON t USING GIST(a)", "CREATE INDEX abc ON t USING GIST(a)"},
		{"CREATE INDEX abc ON t USING btree(a) WHERE a > 1",
			"CREATE INDEX abc ON t USING btree(a) WHERE a > 1"},
		{"CREATE INDEX abc ON t USING btree(col1 varchar_pattern_ops ASC, col2)",
			"CREATE INDEX abc ON t USING btree(col1 varchar_pattern_ops ASC, col2)"},
	} {
		tree, err := ParseOne(c.sql, "postgres")
		if err != nil {
			t.Errorf("ParseOne(%q): %v", c.sql, err)
			continue
		}
		got, gerr := Generate(tree, "postgres")
		if gerr != nil {
			t.Errorf("Generate(%q): %v", c.sql, gerr)
			continue
		}
		if got != c.want {
			t.Errorf("%s\n got  %s\n want %s", c.sql, got, c.want)
		}
	}
}

// TestDeepNestingIsLinear pins the cost of rewriting a column chain into dots.
// The rewrite copies the node it is at and then recurses into the children; a
// DEEP copy at each level copies the whole subtree again one level down, which
// is quadratic in the depth. A few thousand nested operators took a second,
// and the generator fuzzer's own worker was killed for hanging on one.
func TestDeepNestingIsLinear(t *testing.T) {
	cost := func(depth int) time.Duration {
		sql := "(" + strings.Repeat("!", depth) + "(0)).A()"
		start := time.Now()
		if _, err := ParseOne(sql, "tsql"); err != nil {
			t.Fatalf("ParseOne at depth %d: %v", depth, err)
		}
		return time.Since(start)
	}
	// Warm, then compare a doubling. Linear work doubles; the quadratic
	// version took four times as long, and a factor of three leaves room for
	// a slow machine without letting that back in.
	cost(400)
	small, large := cost(800), cost(1600)
	if large > 3*small+10*time.Millisecond {
		t.Errorf("doubling the depth cost %v against %v: more than linear", large, small)
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
		// UESCAPE names the character that introduces an escape, instead of
		// the backslash otherwise read: `!0061` rather than `\0061`.
		{"a unicode string with its own escape character", "postgres",
			`SELECT U&'d!0061t!+000061' UESCAPE '!' AS label`,
			`SELECT U&'d!0061t!+000061' UESCAPE '!' AS label`},
		{"UESCAPE beside a quote inside the body", "postgres",
			`SELECT U&'can''t !0061' UESCAPE '!' AS label`,
			`SELECT U&'can''t !0061' UESCAPE '!' AS label`},
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
	// UESCAPE with nothing after it is left to name itself: the reference
	// silently drops the whole clause and reads as if UESCAPE had not been
	// written at all, a leniency this port does not reproduce.
	if e, err := ParseOne("SELECT U&'a' UESCAPE", "postgres"); err == nil {
		t.Errorf("UESCAPE without a string was read: %v", e)
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
	// Two different names, and the dialects disagree about them in opposite
	// directions. T-SQL drops the SAVEPOINT a rollback names -- which would
	// roll back everything rather than to it, a different action -- and it
	// alone keeps the name the TRANSACTION itself carries, which everyone
	// else writes away.
	savepoint, err := ParseOne("ROLLBACK TO b", "")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if got, err := Generate(savepoint, "tsql"); err == nil {
		t.Errorf("T-SQL wrote %q; it rolls back everything there", got)
	}
	for _, tc := range []struct{ sql, want string }{
		{"BEGIN TRANSACTION n", "BEGIN TRANSACTION n"},
		{"COMMIT TRANSACTION n", "COMMIT TRANSACTION n"},
		{"ROLLBACK TRANSACTION n", "ROLLBACK TRANSACTION n"},
	} {
		e, err := ParseOne(tc.sql, "tsql")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		if got, err := Generate(e, "tsql"); err != nil || got != tc.want {
			t.Errorf("%q wrote %q (%v), want %q", tc.sql, got, err, tc.want)
		}
		// Everywhere else the name goes, so the statement is refused.
		if got, err := Generate(e, "postgres"); err == nil {
			t.Errorf("PostgreSQL wrote %q; it writes the name away", got)
		}
	}
	for _, sql := range []string{
		"TRUNCATE t",
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
		// A FUNCTION securable names a CALL, not a bare name, and is read
		// through the general table parser -- which reads a call too --
		// rather than a name-only one.
		{"a function securable, with its own arguments", "GRANT EXECUTE ON FUNCTION calculate_bonus(integer) TO analyst",
			"GRANT EXECUTE ON FUNCTION CALCULATE_BONUS(integer) TO analyst"},
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
	for _, tc := range []struct{ sql, want string }{
		{"CREATE TABLE a.b.c", "CREATE TABLE b.c"},
		{"CREATE TABLE a.b.c (x INT)", "CREATE TABLE b.c (x INTEGER)"},
	} {
		e, err := ParseOne(tc.sql, "")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		if got, err := Generate(e, "tsql"); err != nil || got != tc.want {
			t.Errorf("%q wrote %q (%v), want %q", tc.sql, got, err, tc.want)
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
	// T-SQL puts the whole SET inside parentheses of the syntax's own, and
	// reads what is inside them as table PROPERTIES -- each with a class of
	// its own -- rather than as a list of equalities. A name it has no
	// property for stays the equality it was written as.
	for _, c := range []struct{ sql, want string }{
		{"ALTER TABLE tbl SET (SYSTEM_VERSIONING=OFF)",
			"ALTER TABLE tbl SET (SYSTEM_VERSIONING=OFF)"},
		{"ALTER TABLE tbl SET (DATA_DELETION=ON)", "ALTER TABLE tbl SET (DATA_DELETION=ON)"},
		{"ALTER TABLE tbl SET (DATA_DELETION=ON(FILTER_COLUMN=col, RETENTION_PERIOD=5 MONTHS))",
			"ALTER TABLE tbl SET (DATA_DELETION=ON(FILTER_COLUMN=col, RETENTION_PERIOD=5 MONTHS))"},
		{"ALTER TABLE tbl SET (FILESTREAM_ON = 'test')",
			"ALTER TABLE tbl SET (FILESTREAM_ON = 'test')"},
		{"ALTER TABLE tbl SET (SYSTEM_VERSIONING=ON(HISTORY_TABLE=db.h, HISTORY_RETENTION_PERIOD=INFINITE))",
			"ALTER TABLE tbl SET (SYSTEM_VERSIONING=ON(HISTORY_TABLE=db.h, HISTORY_RETENTION_PERIOD=INFINITE))"},
		{"ALTER TABLE tbl SET (DATA_DELETION=OFF)", "ALTER TABLE tbl SET (DATA_DELETION=OFF)"},
	} {
		tree, err := ParseOne(c.sql, "tsql")
		if err != nil {
			t.Errorf("ParseOne(%q): %v", c.sql, err)
			continue
		}
		got, gerr := Generate(tree, "tsql")
		if gerr != nil {
			t.Errorf("Generate(%q): %v", c.sql, gerr)
			continue
		}
		if got != c.want {
			t.Errorf("%s\n got  %s\n want %s", c.sql, got, c.want)
		}
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
// building a path, and writes them back the same way: an extraction whose key
// is a COLUMN has no parts to spread across one argument each, so the call is
// written over whatever the node carries.
func TestJSONPathFunctionPositionalForm(t *testing.T) {
	for _, sql := range []string{
		"SELECT JSON_EXTRACT_PATH(x, k1, k2) FROM t",
		"SELECT JSON_EXTRACT_PATH(x, k1, 'k2') FROM t",
		"SELECT JSON_EXTRACT_PATH(x, 'k1', k2) FROM t",
		"SELECT JSON_EXTRACT_PATH(a, VARIADIC '{}') FROM t",
		"SELECT JSON_EXTRACT_PATH_TEXT(x, k1, k2) FROM t",
		"SELECT JSON_EXTRACT_PATH_TEXT(a, VARIADIC '{}') FROM t",
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
		got, err := Generate(e, "postgres")
		if err != nil {
			t.Fatalf("Generate(%q): %v", sql, err)
		}
		if got != sql {
			t.Errorf("got %q, want %q", got, sql)
		}
	}
	// A single key that is not a path at all keeps the OPERATOR: there are no
	// parts to spread, so the call has nothing to do with it.
	for _, tc := range []struct{ sql, want string }{
		{"SELECT a -> (1 + 2)", "SELECT a -> (1 + 2)"},
		{"SELECT a -> (NOT x)", "SELECT a -> (NOT x)"},
		{"SELECT a -> ('x' || 'y')", "SELECT a -> ('x' || 'y')"},
		{"SELECT JSON_EXTRACT(a, 1 + 2)", "SELECT a -> (1 + 2)"},
	} {
		e, err := ParseOne(tc.sql, "postgres")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		if got, err := Generate(e, "postgres"); err != nil || got != tc.want {
			t.Errorf("%q wrote %q (%v), want %q", tc.sql, got, err, tc.want)
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

// TestRowsFrom covers PostgreSQL's ROWS FROM (f(), g()), which zips
// table functions into one relation. The functions, their column aliases
// and WITH ORDINALITY all hang off a Table whose name is the clause.
func TestRowsFrom(t *testing.T) {
	for _, sql := range []string{
		`SELECT * FROM ROWS FROM (FUNC1(col1, col2))`,
		`SELECT * FROM ROWS FROM (FUNC1(col1) AS alias1("col1" TEXT), FUNC2(col2) AS alias2("col2" INT)) WITH ORDINALITY`,
		`SELECT * FROM table1, ROWS FROM (FUNC1(col1) AS alias1("col1" TEXT)) WITH ORDINALITY AS alias3("col3" INT, "col4" TEXT)`,
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
	if _, err := ParseOne("SELECT * FROM ROWS FROM (1+", "postgres"); err == nil {
		t.Error("a malformed member of ROWS FROM was read; it should be refused")
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

// TestNamedArgument covers `name => value` inside a call.
//
// The reference reads the pair where it reads a LAMBDA -- `->` and `=>` sit
// in one table there -- which is why one name followed by an arrow is either
// a lambda's parameter or an argument's name, decided by which arrow.
func TestNamedArgument(t *testing.T) {
	for _, c := range []struct{ sql, dialect, want string }{
		{"SELECT F(a => 1)", "postgres", "SELECT F(a => 1)"},
		{"SELECT MAKE_INTERVAL(years => 1)", "postgres", "SELECT MAKE_INTERVAL(years => 1)"},
		{"SELECT MAKE_INTERVAL(years => 1, months => 2, days => 3)", "postgres",
			"SELECT MAKE_INTERVAL(years => 1, months => 2, days => 3)"},
		{"SELECT UNNEST(x, max_depth => 2)", "duckdb", "SELECT UNNEST(x, max_depth => 2)"},
		// The other arrow in the same position is still a lambda.
		{"SELECT LIST_TRANSFORM(x, y -> y + 1)", "duckdb", "SELECT LIST_TRANSFORM(x, y -> y + 1)"},
	} {
		e, err := ParseOne(c.sql, c.dialect)
		if err != nil {
			t.Fatalf("ParseOne(%q, %q): %v", c.sql, c.dialect, err)
		}
		got, err := Generate(e, c.dialect)
		if err != nil {
			t.Fatalf("Generate(%q): %v", c.sql, err)
		}
		if got != c.want {
			t.Errorf("%q wrote %q, want %q", c.sql, got, c.want)
		}
	}

	// The name becomes a VAR, not the column the left side was parsed as.
	e, err := ParseOne("SELECT F(a => 1)", "postgres")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	call, _ := e.Args["expressions"].([]*Expression)
	kwarg, _ := call[0].Args["expressions"].([]*Expression)
	if len(kwarg) != 1 || kwarg[0].Class != "Kwarg" {
		t.Fatalf("the argument is %v, want a Kwarg", kwarg)
	}
	name, _ := kwarg[0].Args["this"].(*Expression)
	if name == nil || name.Class != "Var" {
		t.Errorf("the name is %v, want a Var", name)
	}

	// A QUOTED name loses its quotes there, so it is refused rather than
	// written back as a different name.
	if e, err := ParseOne(`SELECT F("a" => 1)`, "postgres"); err == nil {
		t.Errorf("read %s; the quotes would be written away", e.Class)
	}
	// ...and so is a value that is not an expression at all.
	if e, err := ParseOne("SELECT F(a => )", "postgres"); err == nil {
		t.Errorf("read %s for a named argument with no value", e.Class)
	}
}

// TestFormatBuilder covers T-SQL's FORMAT, whose builder reads the format it
// is handed rather than only placing it.
func TestFormatBuilder(t *testing.T) {
	for _, tc := range []struct {
		sql   string
		class string
		// The format the tree ends up carrying, "" for none at all.
		format string
	}{
		// A date field anywhere makes it a time format, rewritten into the
		// reference's own spelling.
		{"SELECT FORMAT(a, 'yyyy-MM-dd HH:mm:ss.ffffff')", "TimeToStr", "%Y-%m-%d %H:%M:%S.%f"},
		{"SELECT FORMAT(a, 'MMMM')", "TimeToStr", "%B"},
		{"SELECT FORMAT(a, 'dddd', 'de-CH')", "TimeToStr", "%A"},
		// One character is read through a table of its own: `m` is a whole
		// month-and-day format, not the minutes `mm` stands for.
		{"SELECT FORMAT(a, 'm')", "TimeToStr", "%B %-d"},
		{"SELECT FORMAT(a, 'd')", "TimeToStr", "%m/%d/%Y"},
		// No date field at all: a number format, kept as written.
		{"SELECT FORMAT(12345, '###.###.###')", "NumberToStr", "###.###.###"},
		{"SELECT FORMAT(a, 'f')", "NumberToStr", "f"},
		// N and C are number formats even though C would otherwise read as
		// nothing and N holds no date field either -- the reference names
		// them explicitly, and both land the same way here.
		{"SELECT FORMAT(a, 'N', 'en-us')", "NumberToStr", "N"},
		{"SELECT FORMAT(a, 'C')", "NumberToStr", "C"},
		// A format that is not a literal has no name to read, which is no
		// date field, so it stays where it was written.
		{"SELECT FORMAT(a, CONCAT('yyyy', 'MM'))", "NumberToStr", ""},
	} {
		e, err := ParseOne(tc.sql, "tsql")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		call, _ := e.Args["expressions"].([]*Expression)
		if len(call) != 1 || call[0].Class != tc.class {
			t.Fatalf("%q read as %v, want a %s", tc.sql, call, tc.class)
		}
		format, _ := call[0].Args["format"].(*Expression)
		got := ""
		if format != nil && format.Class == "Literal" {
			got, _ = format.Args["this"].(string)
		}
		if tc.format != "" && got != tc.format {
			t.Errorf("%q carries format %q, want %q", tc.sql, got, tc.format)
		}
	}

	// Both nodes require a format, and the reference rejects a call without
	// one rather than building a node that has none.
	if e, err := ParseOne("SELECT FORMAT(a)", "tsql"); err == nil {
		t.Errorf("read %v; the reference rejects a FORMAT with no format", e)
	}

	// The format goes back out in T-SQL's spelling, not the tree's -- and a
	// one-character format is written out in full, which is the reference's
	// own lossy round trip rather than a divergence.
	for sql, want := range map[string]string{
		"SELECT FORMAT(a, 'yyyy-MM-dd HH:mm:ss.ffffff')": "SELECT FORMAT(a, 'yyyy-MM-dd HH:mm:ss.ffffff')",
		"SELECT FORMAT(a, 'm')":                          "SELECT FORMAT(a, 'MMMM d')",
		"SELECT FORMAT(a, 'N', 'en-us')":                 "SELECT FORMAT(a, 'N', 'en-us')",
		"SELECT FORMAT(12345, '###.###.###')":            "SELECT FORMAT(12345, '###.###.###')",
	} {
		e, err := ParseOne(sql, "tsql")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", sql, err)
		}
		got, err := Generate(e, "tsql")
		if err != nil {
			t.Fatalf("Generate(%q): %v", sql, err)
		}
		if got != want {
			t.Errorf("%q wrote %q, want %q", sql, got, want)
		}
	}

	// Elsewhere FORMAT is an ordinary variadic call and none of this applies.
	e, err := ParseOne("SELECT FORMAT(a, 'yyyy')", "duckdb")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	call, _ := e.Args["expressions"].([]*Expression)
	if len(call) != 1 || call[0].Class != "Format" {
		t.Errorf("duckdb read FORMAT as %v, want a Format", call)
	}
}

// TestCeilFloor covers the grammar CEIL and FLOOR are given for the sake of
// one spelling: the unit they round TO.
func TestCeilFloor(t *testing.T) {
	for _, sql := range []string{
		"SELECT CEIL(a)",
		"SELECT FLOOR(a)",
		"SELECT CEIL(a, b)",
		"SELECT FLOOR(a, b)",
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

	// An absent second argument is ABSENT, not present and empty.
	e, err := ParseOne("SELECT FLOOR(a)", "")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	call := e.Args["expressions"].([]*Expression)[0]
	if _, ok := call.Args["decimals"]; ok {
		t.Errorf("a one-argument FLOOR carries decimals = %v", call.Args["decimals"])
	}

	// The unit is a Var built from the word as written, and it is not one of
	// the arguments -- `FLOOR(x TO DAY)` is a Floor of ONE thing.
	e, err = ParseOne("SELECT FLOOR(a TO DAY)", "")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	call = e.Args["expressions"].([]*Expression)[0]
	unit, _ := call.Args["to"].(*Expression)
	if unit == nil || unit.Class != "Var" || unit.Args["this"] != "DAY" {
		t.Errorf("the unit is %v, want Var(DAY)", unit)
	}
	if got, err := Generate(e, ""); err != nil || got != "SELECT FLOOR(a TO DAY)" {
		t.Errorf("wrote %q, %v", got, err)
	}

	for _, sql := range []string{
		// TO wants a word, and a string is not one.
		"SELECT FLOOR(a TO 'DAY')",
		"SELECT FLOOR(a TO)",
		// Two is all the reference reads; a third would be dropped.
		"SELECT FLOOR(a, b, c)",
		"SELECT FLOOR(a",
	} {
		if e, err := ParseOne(sql, ""); err == nil {
			t.Errorf("read %q as %v, want a refusal", sql, e)
		}
	}
}

// TestOverlay covers OVERLAY, which replaces part of a string and can be
// written with words or with commas between the same four pieces.
func TestOverlay(t *testing.T) {
	for _, sql := range []string{
		"SELECT OVERLAY(a PLACING b FROM 1)",
		"SELECT OVERLAY(a PLACING b FROM 1 FOR 1)",
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

	// Commas and words build the same node, which is why the comma form is
	// written back out in the word form.
	commas, err := ParseOne("SELECT OVERLAY('Spark SQL', 'ANSI ', 7, 0)", "databricks")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	words, err := ParseOne("SELECT OVERLAY('Spark SQL' PLACING 'ANSI ' FROM 7 FOR 0)", "databricks")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if !commas.Equal(words) {
		t.Errorf("the comma form read as a different tree:\n%v\n%v", commas, words)
	}

	// A call may stop after any of the pieces, and the ones it did not reach
	// are absent rather than empty.
	e, err := ParseOne("SELECT OVERLAY(a PLACING b FROM 1)", "postgres")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	call := e.Args["expressions"].([]*Expression)[0]
	if _, ok := call.Args["for_"]; ok {
		t.Errorf("a three-piece OVERLAY carries for_ = %v", call.Args["for_"])
	}

	if e, err := ParseOne("SELECT OVERLAY(a PLACING b FROM 1", "postgres"); err == nil {
		t.Errorf("read an unclosed OVERLAY as %v", e)
	}
}

// TestDistinctArgFunction covers the calls whose DISTINCT belongs to one
// argument rather than to the call.
func TestDistinctArgFunction(t *testing.T) {
	for _, tc := range []struct{ sql, dialect, class string }{
		{"SELECT ARG_MAX(a, b)", "", "ArgMax"},
		{"SELECT MAX_BY(a, b)", "", "ArgMax"},
		{"SELECT ARG_MIN(a, b)", "", "ArgMin"},
		{"SELECT MIN_BY(a, b)", "", "ArgMin"},
		{"SELECT QUANTILE(a, 0.5)", "duckdb", "Quantile"},
		{"SELECT APPROX_QUANTILE(a, 0.5)", "duckdb", "ApproxQuantile"},
		{"SELECT QUANTILE_CONT(a, 0.5)", "duckdb", "PercentileCont"},
		{"SELECT QUANTILE_DISC(a, 0.5)", "duckdb", "PercentileDisc"},
		{"SELECT REGR_SXY(a, b)", "databricks", "RegrSxy"},
	} {
		e, err := ParseOne(tc.sql, tc.dialect)
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		call := e.Args["expressions"].([]*Expression)[0]
		if call.Class != tc.class {
			t.Errorf("%q read as %s, want %s", tc.sql, call.Class, tc.class)
		}
		// The arguments land on the class's OWN keys, in order.
		if this, _ := call.Args["this"].(*Expression); this == nil {
			t.Errorf("%q has no first argument", tc.sql)
		}
	}

	// The DISTINCT wraps the argument the reference names, which is the
	// second one for three of the five regressions and the first everywhere
	// else -- so the same word lands in two different places.
	for _, tc := range []struct {
		sql, dialect string
		index        int
	}{
		{"SELECT ARG_MAX(DISTINCT a, b)", "", 0},
		{"SELECT REGR_SXY(DISTINCT a, b)", "databricks", 0},
		{"SELECT REGR_SXX(DISTINCT a, b)", "databricks", 1},
		{"SELECT REGR_SYY(DISTINCT a, b)", "databricks", 1},
	} {
		e, err := ParseOne(tc.sql, tc.dialect)
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		call := e.Args["expressions"].([]*Expression)[0]
		keys := []string{"this", "expression"}
		got, _ := call.Args[keys[tc.index]].(*Expression)
		if got == nil || got.Class != "Distinct" {
			t.Errorf("%q put the DISTINCT somewhere other than %s: %v",
				tc.sql, keys[tc.index], call)
		}
	}

	// ALL is the no-op modifier and leaves the argument alone.
	all, err := ParseOne("SELECT ARG_MAX(ALL a, b)", "")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	plain, err := ParseOne("SELECT ARG_MAX(a, b)", "")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if !all.Equal(plain) {
		t.Errorf("ALL changed the tree:\n%v\n%v", all, plain)
	}

	// More arguments than the class has keys are dropped by the reference's
	// own zip, so they are dropped here too rather than refused.
	if _, err := ParseOne("SELECT ARG_MAX(a, b, c, d)", ""); err != nil {
		t.Errorf("ParseOne: %v", err)
	}
	if e, err := ParseOne("SELECT ARG_MAX(a, b", ""); err == nil {
		t.Errorf("read an unclosed ARG_MAX as %v", e)
	}
}

// TestXMLElement covers XMLELEMENT, whose tag is a NAME rather than an
// expression -- and whose empty content is recorded as false, not as nothing.
func TestXMLElement(t *testing.T) {
	for _, sql := range []string{
		"SELECT XMLELEMENT(NAME foo)",
		"SELECT XMLELEMENT(NAME foo, XMLATTRIBUTES('xyz' AS bar))",
		`SELECT XMLELEMENT(NAME "foo$bar", XMLATTRIBUTES('xyz' AS "a&b"))`,
		"SELECT XMLELEMENT(NAME foo, XMLATTRIBUTES('xyz' AS bar), 'cont', 'ent')",
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

	// No content at all is FALSE rather than absent, which is what the
	// reference's `matched and value` yields -- and the spelling for the
	// short form is chosen by that very flag.
	e, err := ParseOne("SELECT XMLELEMENT(NAME foo)", "postgres")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	call := e.Args["expressions"].([]*Expression)[0]
	if call.Args["expressions"] != false {
		t.Errorf("an empty XMLELEMENT carries expressions = %v", call.Args["expressions"])
	}
	if _, ok := call.Args["evalname"]; ok {
		t.Errorf("a NAME form carries evalname = %v", call.Args["evalname"])
	}

	// EVALNAME computes the tag instead of naming it, and is recorded.
	e, err = ParseOne("SELECT XMLELEMENT(EVALNAME a || b, 1)", "postgres")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	call = e.Args["expressions"].([]*Expression)[0]
	if call.Args["evalname"] != true {
		t.Errorf("EVALNAME was not recorded: %v", call.Args)
	}
	if this, _ := call.Args["this"].(*Expression); this == nil || this.Class != "DPipe" {
		t.Errorf("the computed tag is %v, want the concatenation", call.Args["this"])
	}

	if e, err := ParseOne("SELECT XMLELEMENT(NAME foo", "postgres"); err == nil {
		t.Errorf("read an unclosed XMLELEMENT as %v", e)
	}
}

// TestAliasedFunctionArgument covers an argument that names itself, which the
// reference allows only where it has no node for the call.
func TestAliasedFunctionArgument(t *testing.T) {
	e, err := ParseOne("SELECT F(x AS a)", "")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	call := e.Args["expressions"].([]*Expression)[0]
	if call.Class != "Anonymous" {
		t.Fatalf("F read as %s", call.Class)
	}
	arg := call.Args["expressions"].([]*Expression)[0]
	if arg.Class != "Alias" {
		t.Errorf("the argument is a %s, want an Alias", arg.Class)
	}

	for _, sql := range []string{
		// Only the WRITTEN alias counts: a bare word after an argument is a
		// stray word, and the reference refuses it too.
		"SELECT F(x a)",
		// A name the reference HAS a node for takes no alias either.
		"SELECT ABS(x AS a)",
	} {
		if e, err := ParseOne(sql, ""); err == nil {
			t.Errorf("read %q as %v, want a refusal", sql, e)
		}
	}
}

// TestConvertIsATSQLCall covers CONVERT, which is a Convert in T-SQL and a
// CAST written another way everywhere else.
func TestConvertIsATSQLCall(t *testing.T) {
	e, err := ParseOne("SELECT CONVERT(INTEGER, x)", "tsql")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	call := e.Args["expressions"].([]*Expression)[0]
	if call.Class != "Convert" {
		t.Errorf("T-SQL read CONVERT as %s", call.Class)
	}
	if _, ok := call.Args["safe"]; ok {
		t.Errorf("a plain CONVERT carries safe = %v", call.Args["safe"])
	}

	// TRY_CONVERT is the same node with the flag SET, which is why an
	// ordinary one does not carry the key at all.
	e, err = ParseOne("SELECT TRY_CONVERT(INTEGER, x)", "tsql")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	call = e.Args["expressions"].([]*Expression)[0]
	if call.Class != "Convert" || call.Args["safe"] != true {
		t.Errorf("TRY_CONVERT read as %s with safe = %v", call.Class, call.Args["safe"])
	}
	if got, err := Generate(e, "tsql"); err != nil || got != "SELECT TRY_CONVERT(INTEGER, x)" {
		t.Errorf("wrote %q, %v", got, err)
	}

	// Elsewhere the reference builds a Cast whose arguments come out in an
	// order this port has no grammar for -- `CONVERT(INT, x)` becomes
	// `CAST(INT AS x)`. Refused rather than built as a Convert.
	for _, dialect := range []string{"", "postgres", "duckdb", "databricks"} {
		if e, err := ParseOne("SELECT CONVERT(INTEGER, x)", dialect); err == nil {
			t.Errorf("[%s] read CONVERT as %v; only T-SQL builds one", dialect, e)
		}
	}
}

// TestGroupConcatSpellings covers the four names DuckDB gives one aggregate.
func TestGroupConcatSpellings(t *testing.T) {
	first, err := ParseOne("SELECT STRING_AGG(x, ', ')", "duckdb")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	for _, name := range []string{"GROUP_CONCAT", "LISTAGG", "STRINGAGG"} {
		e, err := ParseOne("SELECT "+name+"(x, ', ')", "duckdb")
		if err != nil {
			t.Fatalf("ParseOne(%s): %v", name, err)
		}
		if !e.Equal(first) {
			t.Errorf("%s read as a different tree from STRING_AGG", name)
		}
	}
}

// TestChr covers CHR and T-SQL's CHAR, one node under two names.
func TestChr(t *testing.T) {
	for _, tc := range []struct{ sql, dialect string }{
		{"SELECT CHR(97)", ""},
		{"SELECT CHR(97, 98)", ""},
		{"SELECT CHAR(10)", "tsql"},
	} {
		e, err := ParseOne(tc.sql, tc.dialect)
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		call := e.Args["expressions"].([]*Expression)[0]
		if call.Class != "Chr" {
			t.Fatalf("%q read as %s", tc.sql, call.Class)
		}
		// The character set is recorded as FALSE when it is not written.
		if call.Args["charset"] != false {
			t.Errorf("%q carries charset = %v", tc.sql, call.Args["charset"])
		}
		if got, err := Generate(e, tc.dialect); err != nil || got != tc.sql {
			t.Errorf("%q wrote %q, %v", tc.sql, got, err)
		}
	}

	if e, err := ParseOne("SELECT CHR(97 USING 'utf8')", ""); err == nil {
		t.Errorf("read %v; USING wants a bare word", e)
	}
}

// TestJSONAggregates covers the two ways the same aggregate is read: PostgreSQL
// folds an ORDER BY into the argument, T-SQL keeps it in a slot of its own.
func TestJSONAggregates(t *testing.T) {
	e, err := ParseOne("SELECT JSON_AGG(c1 ORDER BY c1)", "postgres")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	call := e.Args["expressions"].([]*Expression)[0]
	if call.Class != "JSONArrayAgg" {
		t.Fatalf("JSON_AGG read as %s", call.Class)
	}
	if _, ok := call.Args["order"]; ok {
		t.Errorf("PostgreSQL put the ordering in the slot: %v", call.Args["order"])
	}
	if this, _ := call.Args["this"].(*Expression); this == nil || this.Class != "Order" {
		t.Errorf("the ordering did not wrap the argument: %v", call.Args["this"])
	}

	// DISTINCT wraps the argument, and an ORDER BY wraps THAT.
	e, err = ParseOne("SELECT JSON_AGG(DISTINCT c1 ORDER BY c1)", "postgres")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	call = e.Args["expressions"].([]*Expression)[0]
	order, _ := call.Args["this"].(*Expression)
	if order == nil || order.Class != "Order" {
		t.Fatalf("the argument is %v", call.Args["this"])
	}
	if inner, _ := order.Args["this"].(*Expression); inner == nil || inner.Class != "Distinct" {
		t.Errorf("the DISTINCT is not inside the ordering: %v", order.Args["this"])
	}

	// T-SQL reads a plain expression, so the SAME clause lands in the slot.
	e, err = ParseOne("SELECT JSON_ARRAYAGG(c1 ORDER BY c1)", "tsql")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	call = e.Args["expressions"].([]*Expression)[0]
	if slot, _ := call.Args["order"].(*Expression); slot == nil || slot.Class != "Order" {
		t.Errorf("T-SQL did not put the ordering in the slot: %v", call.Args)
	}
	if got, err := Generate(e, "tsql"); err != nil || got != "SELECT JSON_ARRAYAGG(c1 ORDER BY c1)" {
		t.Errorf("wrote %q, %v", got, err)
	}

	// T-SQL's aggregate also reads how nulls are handled, which is a phrase
	// rather than a flag.
	for _, sql := range []string{
		"SELECT JSON_ARRAYAGG(c1 NULL ON NULL)",
		"SELECT JSON_ARRAYAGG(c1 ABSENT ON NULL)",
	} {
		e, err := ParseOne(sql, "tsql")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", sql, err)
		}
		if got, err := Generate(e, "tsql"); err != nil || got != sql {
			t.Errorf("%q wrote %q, %v", sql, got, err)
		}
	}
	if e, err := ParseOne("SELECT JSON_ARRAYAGG(c1", "tsql"); err == nil {
		t.Errorf("read an unclosed JSON_ARRAYAGG as %v", e)
	}
	if e, err := ParseOne("SELECT JSON_AGG(c1", "postgres"); err == nil {
		t.Errorf("read an unclosed JSON_AGG as %v", e)
	}

	// A path folded from a string, and false where there is no path at all.
	e, err = ParseOne(`SELECT JSONB_EXISTS('{"a": 1}', 'a')`, "postgres")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	call = e.Args["expressions"].([]*Expression)[0]
	if path, _ := call.Args["path"].(*Expression); path == nil || path.Class != "JSONPath" {
		t.Errorf("the path is %v", call.Args["path"])
	}
	e, err = ParseOne(`SELECT JSONB_EXISTS('{"a": 1}')`, "postgres")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	call = e.Args["expressions"].([]*Expression)[0]
	if call.Args["path"] != false {
		t.Errorf("a one-argument JSONB_EXISTS carries path = %v", call.Args["path"])
	}
}

// TestDatePart covers the two spellings of an Extract with the unit in front,
// which differ in whether the unit is normalised.
func TestDatePart(t *testing.T) {
	// T-SQL takes a bare word and normalises it through the reference's own
	// table: mm is MONTH, q is QUARTER.
	for _, tc := range []struct{ sql, unit string }{
		{"SELECT DATEPART(month, x)", "month"},
		{"SELECT DATEPART(mm, x)", "MONTH"},
		{"SELECT DATEPART(q, x)", "QUARTER"},
		// A part the table does not name keeps its spelling, case and all.
		{"SELECT DATEPART(nanosecond, x)", "nanosecond"},
	} {
		e, err := ParseOne(tc.sql, "tsql")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		call := e.Args["expressions"].([]*Expression)[0]
		unit, _ := call.Args["this"].(*Expression)
		if call.Class != "Extract" || unit == nil || unit.Args["this"] != tc.unit {
			t.Errorf("%q read as %s with unit %v, want Extract of %q",
				tc.sql, call.Class, unit, tc.unit)
		}
	}

	// PostgreSQL takes a STRING and keeps whatever was inside it -- the same
	// `mm` that T-SQL normalises stays `mm` here.
	e, err := ParseOne("SELECT DATE_PART('mm', x)", "postgres")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	call := e.Args["expressions"].([]*Expression)[0]
	unit, _ := call.Args["this"].(*Expression)
	if unit == nil || unit.Class != "Var" || unit.Args["this"] != "mm" {
		t.Errorf("PostgreSQL normalised the unit: %v", unit)
	}
	// And it writes back as an EXTRACT, which is what the reference emits.
	if got, err := Generate(e, "postgres"); err != nil || got != "SELECT EXTRACT(mm FROM x)" {
		t.Errorf("wrote %q, %v", got, err)
	}

	// A unit that is neither a word nor a string is kept as it parsed.
	e, err = ParseOne("SELECT DATE_PART('isodow'::VARCHAR(6), x)", "postgres")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	call = e.Args["expressions"].([]*Expression)[0]
	if unit, _ := call.Args["this"].(*Expression); unit == nil || unit.Class != "Cast" {
		t.Errorf("the cast was turned into %v", call.Args["this"])
	}

	// T-SQL reads the value only after a comma, and records FALSE where there
	// is none; PostgreSQL reads one either way, so the same shortened call is
	// a refusal there.
	e, err = ParseOne("SELECT DATEPART(month)", "tsql")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	call = e.Args["expressions"].([]*Expression)[0]
	if call.Args["expression"] != false {
		t.Errorf("a one-argument DATEPART carries expression = %v", call.Args["expression"])
	}

	for _, tc := range []struct{ sql, dialect string }{
		{"SELECT DATEPART('month', x)", "tsql"},
		{"SELECT DATE_PART('month')", "postgres"},
	} {
		if e, err := ParseOne(tc.sql, tc.dialect); err == nil {
			t.Errorf("read %q as %v, want a refusal", tc.sql, e)
		}
	}
}

// TestOpenJSON covers OPENJSON, whose column list is written OUTSIDE the
// call's own parentheses.
func TestOpenJSON(t *testing.T) {
	for _, sql := range []string{
		"SELECT * FROM OPENJSON(@json)",
		`SELECT * FROM OPENJSON(@json, '$.a.b')`,
		"SELECT * FROM OPENJSON(@a) WITH (month VARCHAR(3), temp INTEGER) AS months",
		`SELECT * FROM OPENJSON(@a) WITH (month_id TINYINT '$.sql:identity()')`,
		"SELECT * FROM OPENJSON(@a) WITH (doc VARCHAR(3) AS JSON)",
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

	// No path is FALSE rather than absent, and the short spelling is chosen
	// by that very flag.
	e, err := ParseOne("SELECT * FROM OPENJSON(@json)", "tsql")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	from, _ := e.Args["from_"].(*Expression)
	table, _ := from.Args["this"].(*Expression)
	call, _ := table.Args["this"].(*Expression)
	if call == nil || call.Class != "OpenJSON" || call.Args["path"] != false {
		t.Errorf("OPENJSON read as %v", call)
	}

	for _, sql := range []string{
		// The path is a string and nothing else.
		"SELECT * FROM OPENJSON(@json, x)",
		"SELECT * FROM OPENJSON(@a) WITH month VARCHAR(3)",
		"SELECT * FROM OPENJSON(@a) WITH (month VARCHAR(3)",
	} {
		if e, err := ParseOne(sql, "tsql"); err == nil {
			t.Errorf("read %q as %v, want a refusal", sql, e)
		}
	}
}

// TestUpperLower covers two of the commonest functions there are, which had no
// signature at ANY arity because their builder branches on one argument's
// class -- LOWER over a HEX is a LowerHex, not a Lower.
//
// A branch is not an undescribable builder: the position is recorded
// separately, and only the calls that actually take the branch are refused.
func TestUpperLower(t *testing.T) {
	for _, dialect := range []string{"", "tsql", "postgres", "duckdb", "databricks"} {
		for _, tc := range []struct{ sql, class string }{
			{"SELECT UPPER(x)", "Upper"},
			{"SELECT LOWER(x)", "Lower"},
			{"SELECT UPPER('hello')", "Upper"},
		} {
			e, err := ParseOne(tc.sql, dialect)
			if err != nil {
				t.Fatalf("[%s] ParseOne(%q): %v", dialect, tc.sql, err)
			}
			call := e.Args["expressions"].([]*Expression)[0]
			if call.Class != tc.class {
				t.Errorf("[%s] %q read as %s", dialect, tc.sql, call.Class)
			}
			if got, err := Generate(e, dialect); err != nil || got != tc.sql {
				t.Errorf("[%s] %q wrote %q, %v", dialect, tc.sql, got, err)
			}
		}

		// A HEX argument builds a class of its own: LOWER(HEX(x)) is a
		// LowerHex, and UPPER(HEX(x)) simplifies straight to a bare Hex,
		// since HEX already writes uppercase.
		for _, tc := range []struct{ sql, want string }{
			{"SELECT LOWER(HEX(x))", "SELECT LOWER(HEX(x))"},
			{"SELECT UPPER(HEX(x))", "SELECT HEX(x)"},
		} {
			e, err := ParseOne(tc.sql, dialect)
			if err != nil {
				t.Errorf("[%s] ParseOne(%q): %v", dialect, tc.sql, err)
				continue
			}
			if got, err := Generate(e, dialect); err != nil || got != tc.want {
				t.Errorf("[%s] %q wrote %q (%v), want %q", dialect, tc.sql, got, err, tc.want)
			}
		}
	}
}

// TestNestedBuilder covers builders that put the arguments inside a node
// WRAPPED in another one, which the signature probe used to refuse outright.
func TestNestedBuilder(t *testing.T) {
	for _, tc := range []struct {
		sql, dialect string
		// The chain of classes from the call down to the argument.
		chain []string
	}{
		// DuckDB's ANY_VALUE is an IgnoreNulls over an AnyValue: the wrapper
		// is fixed and the arguments belong to the node inside it.
		{"SELECT ANY_VALUE(x)", "duckdb", []string{"IgnoreNulls", "AnyValue", "Column"}},
		// T-SQL's date parts coerce first, and the coercion carries a default
		// date the builder always supplies.
		{"SELECT YEAR(y)", "tsql", []string{"Year", "TsOrDsToDate", "Column"}},
		{"SELECT MONTH(y)", "tsql", []string{"Month", "TsOrDsToDate", "Column"}},
		{"SELECT DAY(y)", "tsql", []string{"Day", "TsOrDsToDate", "Column"}},
		// EOMONTH is a LastDay over the same coercion, and its optional lag
		// puts a DateAdd in between.
		{"SELECT EOMONTH(x)", "tsql", []string{"LastDay", "TsOrDsToDate", "Column"}},
		{"SELECT EOMONTH(x, -1)", "tsql", []string{"LastDay", "DateAdd", "TsOrDsToDate", "Column"}},
		{"SELECT YEAR(y)", "databricks", []string{"Year", "TsOrDsToDate", "Column"}},
	} {
		e, err := ParseOne(tc.sql, tc.dialect)
		if err != nil {
			t.Fatalf("[%s] ParseOne(%q): %v", tc.dialect, tc.sql, err)
		}
		node := e.Args["expressions"].([]*Expression)[0]
		for _, want := range tc.chain {
			if node == nil || node.Class != want {
				t.Fatalf("[%s] %q: expected a %s, got %v", tc.dialect, tc.sql, want, node)
			}
			node, _ = node.Args["this"].(*Expression)
		}
	}

	// The T-SQL coercion carries a default date, which is a constant node the
	// builder always supplies rather than anything the call was given.
	e, err := ParseOne("SELECT YEAR(y)", "tsql")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	inner, _ := e.Args["expressions"].([]*Expression)[0].Args["this"].(*Expression)
	deflt, _ := inner.Args["default_date"].(*Expression)
	if deflt == nil || deflt.Args["this"] != "1900-01-01" {
		t.Errorf("the coercion's default date is %v", inner.Args["default_date"])
	}

	// Databricks writes the coercion away again under the classes that imply
	// it -- a rule about the PARENT, which a probe that renders a node on its
	// own cannot see.
	e, err = ParseOne("SELECT YEAR(y)", "databricks")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if got, err := Generate(e, "databricks"); err != nil || got != "SELECT YEAR(y)" {
		t.Errorf("wrote %q, %v", got, err)
	}
	// Written out where nothing implies it.
	e, err = ParseOne("SELECT TO_DATE(y)", "databricks")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if got, err := Generate(e, "databricks"); err != nil || got != "SELECT TO_DATE(y)" {
		t.Errorf("wrote %q, %v", got, err)
	}
}

// TestIgnoreNullsOnTrue covers Databricks' FIRST, LAST, FIRST_VALUE and
// LAST_VALUE, whose builder reads its own second argument rather than
// declaring it: literally TRUE wraps the call in IgnoreNulls, and the
// wrapper -- not itself a Func -- is what lets the extra argument past the
// count check that catches every other value there.
func TestIgnoreNullsOnTrue(t *testing.T) {
	for _, tc := range []struct{ sql, class, word string }{
		{"SELECT FIRST(x, TRUE)", "First", "FIRST"},
		{"SELECT LAST(x, TRUE)", "Last", "LAST"},
		{"SELECT FIRST_VALUE(x, TRUE)", "FirstValue", "FIRST_VALUE"},
		{"SELECT LAST_VALUE(x, TRUE)", "LastValue", "LAST_VALUE"},
	} {
		e, err := ParseOne(tc.sql, "databricks")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		call := e.Args["expressions"].([]*Expression)[0]
		if call.Class != "IgnoreNulls" {
			t.Fatalf("%q read as %s, want IgnoreNulls", tc.sql, call.Class)
		}
		inner, _ := call.Args["this"].(*Expression)
		if inner == nil || inner.Class != tc.class {
			t.Errorf("%q wrapped a %v, want %s", tc.sql, inner, tc.class)
		}
		want := "SELECT " + tc.word + "(x) IGNORE NULLS"
		if got, err := Generate(e, "databricks"); err != nil || got != want {
			t.Errorf("%q wrote %q (%v), want %q", tc.sql, got, err, want)
		}
	}
	// Every other value past the first argument is still a refusal: the
	// reference's own arg-count check runs against the bare call, which IS a
	// Func, whenever the wrap does not happen.
	for _, sql := range []string{
		"SELECT FIRST_VALUE(x, FALSE)",
		"SELECT FIRST_VALUE(x, y)",
	} {
		if _, err := ParseOne(sql, "databricks"); err == nil {
			t.Errorf("ParseOne(%q) was read; it should be refused", sql)
		}
	}
}

// TestCurdate covers Databricks' CURDATE, which takes NO argument at all --
// with or without empty parentheses -- and errors on one rather than keeping
// it, unlike an ordinary call the probe would otherwise describe it as.
func TestCurdate(t *testing.T) {
	for _, sql := range []string{"SELECT CURDATE", "SELECT CURDATE()"} {
		e, err := ParseOne(sql, "databricks")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", sql, err)
		}
		call := e.Args["expressions"].([]*Expression)[0]
		if call.Class != "CurrentDate" {
			t.Errorf("%q read as %s, want CurrentDate", sql, call.Class)
		}
		if got, err := Generate(e, "databricks"); err != nil || got != "SELECT CURRENT_DATE" {
			t.Errorf("%q wrote %q (%v), want SELECT CURRENT_DATE", sql, got, err)
		}
	}
	if _, err := ParseOne("SELECT CURDATE(x)", "databricks"); err == nil {
		t.Error("ParseOne(\"SELECT CURDATE(x)\") was read; it should be refused")
	}
}

// TestReducerClauses covers Hive's three ways of saying how rows reach the
// reducers, which sit between HAVING and ORDER BY.
func TestReducerClauses(t *testing.T) {
	for _, sql := range []string{
		"SELECT * FROM x CLUSTER BY a",
		"SELECT * FROM x CLUSTER BY a, b",
		"SELECT * FROM x DISTRIBUTE BY a",
		"SELECT * FROM x SORT BY a",
		"SELECT * FROM x SORT BY a DESC",
		"SELECT * FROM x WHERE a GROUP BY a HAVING b SORT BY s ORDER BY c LIMIT d",
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

	// CLUSTER BY takes plain columns and the other two take ORDERED items --
	// the reference's own asymmetry, and the reason a direction may be
	// written after one but not the other.
	e, err := ParseOne("SELECT * FROM x CLUSTER BY a", "")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	cluster, _ := e.Args["cluster"].(*Expression)
	items, _ := cluster.Args["expressions"].([]*Expression)
	if len(items) != 1 || items[0].Class != "Column" {
		t.Errorf("CLUSTER BY holds %v, want a bare column", items)
	}
	e, err = ParseOne("SELECT * FROM x SORT BY a", "")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	sort, _ := e.Args["sort"].(*Expression)
	items, _ = sort.Args["expressions"].([]*Expression)
	if len(items) != 1 || items[0].Class != "Ordered" {
		t.Errorf("SORT BY holds %v, want an ordering", items)
	}
}

// TestLimitOptions covers what may follow a LIMIT's count.
func TestLimitOptions(t *testing.T) {
	for _, sql := range []string{
		"SELECT * FROM t LIMIT 10 PERCENT",
		"SELECT * FROM t LIMIT 10 PERCENT OFFSET 1",
	} {
		e, err := ParseOne(sql, "duckdb")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", sql, err)
		}
		if got, err := Generate(e, "duckdb"); err != nil || got != sql {
			t.Errorf("%q wrote %q, %v", sql, got, err)
		}
	}

	// `%` is the same percent flag. The generator writes the word, which is
	// what the reference writes too.
	for _, tc := range []struct{ sql, want string }{
		{"SELECT * FROM t LIMIT 10%", "SELECT * FROM t LIMIT 10 PERCENT"},
		{"SELECT * FROM t LIMIT 10% OFFSET 1", "SELECT * FROM t LIMIT 10 PERCENT OFFSET 1"},
	} {
		e, err := ParseOne(tc.sql, "duckdb")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		limit, _ := e.Args["limit"].(*Expression)
		opts, _ := limit.Args["limit_options"].(*Expression)
		if opts == nil || opts.Args["percent"] != true {
			t.Errorf("%q did not record percent: %v", tc.sql, opts)
		}
		if got, err := Generate(e, "duckdb"); err != nil || got != tc.want {
			t.Errorf("%q wrote %q, want %q (%v)", tc.sql, got, tc.want, err)
		}
	}

	// ROWS, ONLY and WITH TIES are recorded too, each in its own slot.
	for _, sql := range []string{
		"SELECT * FROM t LIMIT 10 ROWS ONLY",
		"SELECT * FROM t LIMIT 10 WITH TIES",
	} {
		e, err := ParseOne(sql, "duckdb")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", sql, err)
		}
		if got, err := Generate(e, "duckdb"); err != nil || got != sql {
			t.Errorf("%q wrote %q, %v", sql, got, err)
		}
	}

	// Nothing written is no options node at all, rather than one with three
	// falses in it.
	e, err := ParseOne("SELECT * FROM t LIMIT 10", "duckdb")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	limit, _ := e.Args["limit"].(*Expression)
	if opts, _ := limit.Args["limit_options"].(*Expression); opts != nil {
		t.Errorf("a plain LIMIT carries options: %v", opts)
	}
}

// TestStarExcludeAndNotNull covers two spellings the port could not read, each
// of which was hiding a divergence behind the refusal.
func TestStarExcludeAndNotNull(t *testing.T) {
	// EXCEPT and EXCLUDE are one list under two words, and the word written
	// back is the DIALECT's -- DuckDB says EXCLUDE.
	for _, tc := range []struct{ sql, dialect, want string }{
		{"SELECT * EXCLUDE (a, b) FROM x", "duckdb", "SELECT * EXCLUDE (a, b) FROM x"},
		{"SELECT * EXCEPT (a, b) FROM x", "duckdb", "SELECT * EXCLUDE (a, b) FROM x"},
		{"SELECT * EXCEPT (a, b) FROM x", "", "SELECT * EXCEPT (a, b) FROM x"},
		{"SELECT * EXCLUDE (a) REPLACE (c AS d) FROM x", "duckdb",
			"SELECT * EXCLUDE (a) REPLACE (c AS d) FROM x"},
		{"SELECT * RENAME (a AS b) FROM x", "duckdb", "SELECT * RENAME (a AS b) FROM x"},
	} {
		e, err := ParseOne(tc.sql, tc.dialect)
		if err != nil {
			t.Fatalf("[%s] ParseOne(%q): %v", tc.dialect, tc.sql, err)
		}
		if got, err := Generate(e, tc.dialect); err != nil || got != tc.want {
			t.Errorf("[%s] %q wrote %q, want %q (%v)", tc.dialect, tc.sql, got, tc.want, err)
		}
	}

	// `x NOTNULL` is one word and two trees: PostgreSQL keeps a NEGATED Is,
	// and the dialects that normalise it wrap the Is in a Not.
	e, err := ParseOne("SELECT r NOTNULL FROM t", "postgres")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	is := e.Args["expressions"].([]*Expression)[0]
	if is.Class != "Is" || is.Args["negate"] != true {
		t.Errorf("PostgreSQL read NOTNULL as %v", is)
	}
	e, err = ParseOne("SELECT r NOTNULL FROM t", "")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	not := e.Args["expressions"].([]*Expression)[0]
	if not.Class != "Not" {
		t.Errorf("the neutral dialect read NOTNULL as %v", not)
	}
	e, err = ParseOne("SELECT r ISNULL FROM t", "postgres")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if got, err := Generate(e, "postgres"); err != nil || got != "SELECT r IS NULL FROM t" {
		t.Errorf("wrote %q, %v", got, err)
	}
}

// TestTimeZoneTypes covers `TIMESTAMP WITH TIME ZONE` and its relatives, which
// name a type of their own rather than flag the plain one.
func TestTimeZoneTypes(t *testing.T) {
	for _, tc := range []struct{ sql, want string }{
		{"SELECT CAST(x AS TIMESTAMP WITHOUT TIME ZONE)", "SELECT CAST(x AS TIMESTAMP)"},
		{"SELECT CAST(x AS TIMESTAMP WITH TIME ZONE)", "SELECT CAST(x AS TIMESTAMPTZ)"},
		{"SELECT CAST(x AS TIME WITHOUT TIME ZONE)", "SELECT CAST(x AS TIME)"},
		{"SELECT CAST(x AS TIME WITH TIME ZONE)", "SELECT CAST(x AS TIMETZ)"},
		// The size survives the rename.
		{"SELECT CAST(x AS TIMESTAMP(3) WITH TIME ZONE)", "SELECT CAST(x AS TIMESTAMPTZ(3))"},
		{"SELECT x::TIMESTAMP WITH TIME ZONE", "SELECT CAST(x AS TIMESTAMPTZ)"},
		{"SELECT CAST(x AS TIMESTAMP WITH LOCAL TIME ZONE)", "SELECT CAST(x AS TIMESTAMPLTZ)"},
	} {
		e, err := ParseOne(tc.sql, "postgres")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		if got, err := Generate(e, "postgres"); err != nil || got != tc.want {
			t.Errorf("%q wrote %q, want %q (%v)", tc.sql, got, tc.want, err)
		}
	}
}

// TestDeclare covers the statement that makes a variable, whose commas mean
// two different things depending on what follows them.
func TestDeclare(t *testing.T) {
	for _, tc := range []struct{ sql, dialect, want string }{
		{"DECLARE @X INT", "tsql", "DECLARE @X INTEGER"},
		{"DECLARE @X INT = 1", "tsql", "DECLARE @X INTEGER = 1"},
		// Two variables of two types.
		{"DECLARE @X INT, @Y VARCHAR(10)", "tsql", "DECLARE @X INTEGER, @Y VARCHAR(10)"},
		{"DECLARE @v1 AS INTEGER = 1, @v2 AS CHAR(1) = 'c'", "tsql",
			"DECLARE @v1 INTEGER = 1, @v2 CHAR(1) = 'c'"},
		{"DECLARE @X INT = (SELECT col FROM t WHERE id = 1)", "tsql",
			"DECLARE @X INTEGER = (SELECT col FROM t WHERE id = 1)"},
		{"DECLARE @X TABLE (Id INT NOT NULL, Name VARCHAR(100) NOT NULL)", "tsql",
			"DECLARE @X TABLE (Id INTEGER NOT NULL, Name VARCHAR(100) NOT NULL)"},
		{"DECLARE x INT", "databricks", "DECLARE x INT"},
		// VAR and VARIABLE say nothing and are written back away.
		{"DECLARE VAR x INT", "databricks", "DECLARE x INT"},
		{"DECLARE VARIABLE x INT", "databricks", "DECLARE x INT"},
		{"DECLARE OR REPLACE x INT = 1", "databricks", "DECLARE OR REPLACE x INT = 1"},
		// Three variables of ONE type: the same comma, read the other way.
		{"DECLARE x, y, z INT DEFAULT 1", "databricks", "DECLARE x, y, z INT = 1"},
	} {
		e, err := ParseOne(tc.sql, tc.dialect)
		if err != nil {
			t.Fatalf("[%s] ParseOne(%q): %v", tc.dialect, tc.sql, err)
		}
		if got, err := Generate(e, tc.dialect); err != nil || got != tc.want {
			t.Errorf("[%s] %q wrote %q, want %q (%v)", tc.dialect, tc.sql, got, tc.want, err)
		}
	}

	// One item for three names, two items for two names and two types.
	e, err := ParseOne("DECLARE x, y, z INT", "databricks")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	items, _ := e.Args["expressions"].([]*Expression)
	if len(items) != 1 {
		t.Fatalf("three names read as %d items", len(items))
	}
	names, _ := items[0].Args["this"].([]*Expression)
	if len(names) != 3 {
		t.Errorf("one item holds %d names", len(names))
	}
	e, err = ParseOne("DECLARE @X INT, @Y INT", "tsql")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if items, _ = e.Args["expressions"].([]*Expression); len(items) != 2 {
		t.Errorf("two types read as %d items", len(items))
	}

	// No initial value is FALSE rather than absent.
	if items[0].Args["default"] != false {
		t.Errorf("a bare DECLARE carries default = %v", items[0].Args["default"])
	}

	for _, tc := range []struct{ sql, dialect string }{
		// A cursor is not a type. The reference keeps the whole statement as
		// raw text rather than building a Declare, and so does anything the
		// item parser cannot finish -- which is a shape this port does not
		// make, so it declines instead.
		{"DECLARE vendor_cursor CURSOR FOR SELECT a FROM b", "tsql"},
		{"DECLARE @X INT extra", "tsql"},
		// The reference reads TABLE with no columns as no type at all and
		// writes `DECLARE @X`, dropping the word. Refused rather than
		// reproduced.
		{"DECLARE @X TABLE", "tsql"},
	} {
		if e, err := ParseOne(tc.sql, tc.dialect); err == nil {
			t.Errorf("read %q as %v, want a refusal", tc.sql, e)
		}
	}
}

// TestKillAndChain covers two small statements: stopping something the server
// is doing, and saying what happens after a COMMIT.
func TestKillAndChain(t *testing.T) {
	for _, sql := range []string{
		"KILL '123'",
		"KILL CONNECTION 123",
		"KILL QUERY '123'",
	} {
		e, err := ParseOne(sql, "")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", sql, err)
		}
		if got, err := Generate(e, ""); err != nil || got != sql {
			t.Errorf("%q wrote %q, %v", sql, got, err)
		}
	}
	for _, sql := range []string{"KILL CONNECTION 123 extra", "KILL", "KILL QUERY"} {
		if e, err := ParseOne(sql, ""); err == nil {
			t.Errorf("read %q as %v, want a refusal", sql, e)
		}
	}

	// PostgreSQL reads a bare END as COMMIT; everywhere else the word names
	// something or closes a block.
	for _, tc := range []struct{ sql, want string }{
		{"END WORK AND NO CHAIN", "COMMIT AND NO CHAIN"},
		{"END AND CHAIN", "COMMIT AND CHAIN"},
		{"COMMIT AND CHAIN", "COMMIT AND CHAIN"},
		{"COMMIT", "COMMIT"},
	} {
		e, err := ParseOne(tc.sql, "postgres")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		if got, err := Generate(e, "postgres"); err != nil || got != tc.want {
			t.Errorf("%q wrote %q, want %q (%v)", tc.sql, got, tc.want, err)
		}
	}
	// A COMMIT that says nothing about it carries no such key.
	e, err := ParseOne("COMMIT", "postgres")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if _, ok := e.Args["chain"]; ok {
		t.Errorf("a plain COMMIT carries chain = %v", e.Args["chain"])
	}
	if e, err := ParseOne("COMMIT AND NOT CHAIN", "postgres"); err == nil {
		t.Errorf("read %v, want a refusal", e)
	}
}

// TestPrefixOperators covers the operators written in FRONT of their operand,
// which are a table rather than a fixed five: PostgreSQL reads `~` as its
// binary regexp operator, so the prefix form arrives under a different token
// there and a port matching on TILDE alone read `~x` as nothing at all.
func TestPrefixOperators(t *testing.T) {
	for _, tc := range []struct{ sql, dialect, class string }{
		{"SELECT -x", "", "Neg"},
		{"SELECT ~x", "", "BitwiseNot"},
		{"SELECT ~x", "postgres", "BitwiseNot"},
		{"SELECT |/ x", "postgres", "Sqrt"},
		{"SELECT ||/ x", "postgres", "Cbrt"},
		{"SELECT NOT x", "", "Not"},
	} {
		e, err := ParseOne(tc.sql, tc.dialect)
		if err != nil {
			t.Fatalf("[%s] ParseOne(%q): %v", tc.dialect, tc.sql, err)
		}
		got := e.Args["expressions"].([]*Expression)[0]
		if got.Class != tc.class {
			t.Errorf("[%s] %q read as %s, want %s", tc.dialect, tc.sql, got.Class, tc.class)
		}
	}

	// The same character is still the BINARY operator when it follows an
	// operand rather than opening one.
	e, err := ParseOne("SELECT a ~ b", "postgres")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if got := e.Args["expressions"].([]*Expression)[0]; got.Class == "BitwiseNot" {
		t.Errorf("the binary form read as a prefix: %v", got)
	}

	// Unary plus is a no-op: it yields the operand itself.
	plus, err := ParseOne("SELECT +x", "")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	bare, err := ParseOne("SELECT x", "")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if !plus.Equal(bare) {
		t.Errorf("unary plus changed the tree")
	}

	// NOT takes an EQUALITY as its operand, so it negates the comparison
	// rather than the column.
	not, err := ParseOne("SELECT NOT a = b", "")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	inner, _ := not.Args["expressions"].([]*Expression)[0].Args["this"].(*Expression)
	if inner == nil || inner.Class != "EQ" {
		t.Errorf("NOT wrapped %v, want the comparison", inner)
	}
}

// TestTopOptions covers T-SQL's TOP, which takes the same words after its
// count as a LIMIT does -- and a whole query as that count.
func TestTopOptions(t *testing.T) {
	for _, sql := range []string{
		"SELECT TOP 10 PERCENT * FROM t",
		"SELECT TOP 10 PERCENT WITH TIES * FROM t",
		"SELECT TOP (SELECT 1) * FROM t",
		// A statement that stops there selects nothing, and the reference
		// records no projections rather than an empty list.
		"SELECT TOP 10 PERCENT",
		"SELECT",
	} {
		e, err := ParseOne(sql, "tsql")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", sql, err)
		}
		if got, err := Generate(e, "tsql"); err != nil || got != sql {
			t.Errorf("%q wrote %q, %v", sql, got, err)
		}
	}

	// The query is the count ITSELF: the parentheses belong to the TOP, so
	// there is no Subquery around it.
	e, err := ParseOne("SELECT TOP (SELECT 1) * FROM t", "tsql")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	limit, _ := e.Args["limit"].(*Expression)
	count, _ := limit.Args["expression"].(*Expression)
	if count == nil || count.Class != "Select" {
		t.Errorf("the count is %v, want the query itself", count)
	}
	// A SELECT that names nothing is a tree in its own right, whether it
	// stops there or goes on to a clause -- `SELECT FROM t` is what the
	// reference writes for a query with no projections, so the port has to
	// read its own output back.
	for _, sql := range []string{"SELECT", "SELECT FROM a", "SELECT WHERE x"} {
		e, err := ParseOne(sql, "")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", sql, err)
		}
		if items, _ := e.Args["expressions"].([]*Expression); len(items) != 0 {
			t.Errorf("%q names %d projections", sql, len(items))
		}
		if got, err := Generate(e, ""); err != nil || got != sql {
			t.Errorf("%q wrote %q, %v", sql, got, err)
		}
	}
	// A projection that starts and then fails is still an error, not an
	// empty list.
	for _, sql := range []string{"SELECT 1 +", "SELECT )"} {
		if e, err := ParseOne(sql, ""); err == nil {
			t.Errorf("read %q as %v", sql, e)
		}
	}
}

// TestTrimWithoutCharacters covers TRIM told WHERE to trim but not WHAT.
func TestTrimWithoutCharacters(t *testing.T) {
	for _, tc := range []struct{ sql, want string }{
		{"SELECT TRIM(LEADING FROM ' XXX ')", "SELECT LTRIM(' XXX ')"},
		{"SELECT TRIM(TRAILING FROM ' XXX ')", "SELECT RTRIM(' XXX ')"},
		{"SELECT TRIM(FROM ' XXX ')", "SELECT TRIM(' XXX ')"},
		// The form that says both still reads both.
		{"SELECT TRIM(BOTH 'x' FROM ' XXX ')", "SELECT TRIM(BOTH 'x' FROM ' XXX ')"},
	} {
		e, err := ParseOne(tc.sql, "postgres")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		if got, err := Generate(e, "postgres"); err != nil || got != tc.want {
			t.Errorf("%q wrote %q, want %q (%v)", tc.sql, got, tc.want, err)
		}
	}

	// The characters are ABSENT rather than empty when only a position was
	// written.
	e, err := ParseOne("SELECT TRIM(LEADING FROM ' XXX ')", "postgres")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	trim := e.Args["expressions"].([]*Expression)[0]
	if chars, _ := trim.Args["expression"].(*Expression); chars != nil {
		t.Errorf("the characters are %v, want none", chars)
	}
}

// TestExecute covers running a stored procedure, which a guard has to notice:
// the procedure runs whatever is in it, and sp_executesql runs a string.
func TestExecute(t *testing.T) {
	for _, tc := range []struct{ sql, want string }{
		{"EXEC sp_rename 'db.t1', 't2'", "EXECUTE sp_rename 'db.t1', 't2'"},
		{"EXEC MyProc @id = 7, @name = 'x'", "EXECUTE MyProc @id = 7, @name = 'x'"},
		{"EXECUTE @return_status = dbo.MyProc @a, @b",
			"EXECUTE @return_status = dbo.MyProc @a, @b"},
		{"EXEC @RC = dbo.MyProc @id = 7", "EXECUTE @RC = dbo.MyProc @id = 7"},
	} {
		e, err := ParseOne(tc.sql, "tsql")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		if !IsWrite(e) {
			t.Errorf("IsWrite(%q) = false; it runs whatever the procedure holds", tc.sql)
		}
		if got, err := Generate(e, "tsql"); err != nil || got != tc.want {
			t.Errorf("%q wrote %q, want %q (%v)", tc.sql, got, tc.want, err)
		}
	}

	// The status variable and the procedure's name look alike where the first
	// is read, so a parameter with no `=` after it is put back.
	e, err := ParseOne("EXEC @RC = dbo.MyProc @id = 7", "tsql")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if status, _ := e.Args["return_status"].(*Expression); status == nil {
		t.Errorf("the status variable was not read: %v", e.Args)
	}
	e, err = ParseOne("EXEC MyProc @a", "tsql")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if _, ok := e.Args["return_status"]; ok {
		t.Errorf("a first argument was read as a status variable: %v", e.Args)
	}

	// One procedure has a class of its own, because it runs a string.
	e, err = ParseOne("EXEC sp_executesql @payload", "tsql")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if e.Class != "ExecuteSql" {
		t.Errorf("sp_executesql read as %s", e.Class)
	}

	// Only T-SQL names a procedure this way; elsewhere the word opens a
	// statement the reference keeps as raw text.
	for _, dialect := range []string{"", "postgres", "duckdb", "databricks"} {
		if e, err := ParseOne("EXECUTE p", dialect); err == nil && e.Class == "Execute" {
			t.Errorf("[%s] read EXECUTE as a procedure call", dialect)
		}
	}
}

// TestShow covers the phrases a dialect gives SHOW a statement for.
func TestShow(t *testing.T) {
	for _, sql := range []string{
		"SHOW TABLES",
		"SHOW ALL TABLES",
		"SHOW TABLES FROM my_schema",
		"SHOW TABLES FROM my_database.my_schema",
	} {
		e, err := ParseOne(sql, "duckdb")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", sql, err)
		}
		if got, err := Generate(e, "duckdb"); err != nil || got != sql {
			t.Errorf("%q wrote %q, %v", sql, got, err)
		}
	}

	// The longer phrase wins: ALL TABLES is not TABLES.
	e, err := ParseOne("SHOW ALL TABLES", "duckdb")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if e.Args["this"] != "ALL TABLES" {
		t.Errorf("SHOW ALL TABLES named %v", e.Args["this"])
	}

	for _, tc := range []struct{ sql, dialect string }{
		// A phrase outside the table is text the reference keeps rather than
		// a tree, and is refused here.
		{"SHOW COLUMNS FROM t", "duckdb"},
		// No dialect but DuckDB reads SHOW as a statement at all.
		{"SHOW TABLES", "postgres"},
	} {
		if e, err := ParseOne(tc.sql, tc.dialect); err == nil && e.Class == "Show" {
			t.Errorf("[%s] read %q as a Show", tc.dialect, tc.sql)
		}
	}
}

// TestCopy covers moving rows between a table and a FILE, which is the shape a
// guard above this port exists to notice: the file is outside the database.
func TestCopy(t *testing.T) {
	for _, tc := range []struct{ sql, dialect string }{
		{"COPY tbl (col1, col2) FROM 'file' WITH (FORMAT format, HEADER MATCH, FREEZE TRUE)", "postgres"},
		{"COPY tbl (col1, col2) TO 'file' WITH (FORMAT format)", "postgres"},
		{"COPY (SELECT * FROM t) TO 'file' WITH (FORMAT format)", "postgres"},
		{"COPY lineitem (l_orderkey) TO 'orderkey.tbl' WITH (DELIMITER '|')", "duckdb"},
		{"COPY lineitem FROM 'x.ndjson' WITH (FORMAT JSON, AUTO_DETECT TRUE, FORCE_NOT_NULL (col1, col2))", "duckdb"},
		// T-SQL writes an `=` between a setting and its value; the others do
		// not, and the tree is the same either way.
		{"COPY INTO test_1 FROM 'path' WITH (FORMAT_NAME = test, FILE_TYPE = 'CSV')", "tsql"},
	} {
		e, err := ParseOne(tc.sql, tc.dialect)
		if err != nil {
			t.Fatalf("[%s] ParseOne(%q): %v", tc.dialect, tc.sql, err)
		}
		if !IsWrite(e) {
			t.Errorf("IsWrite(%q) = false; it reaches a file", tc.sql)
		}
		if got, err := Generate(e, tc.dialect); err != nil || got != tc.sql {
			t.Errorf("[%s] %q wrote %q, %v", tc.dialect, tc.sql, got, err)
		}
	}

	// The direction is a FLAG rather than a word on the node.
	load, err := ParseOne("COPY t FROM 'file'", "postgres")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	unload, err := ParseOne("COPY t TO 'file'", "postgres")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if load.Args["kind"] != true || unload.Args["kind"] != false {
		t.Errorf("the direction is %v and %v", load.Args["kind"], unload.Args["kind"])
	}
	// And the credentials node is built whether or not any were written.
	if creds, _ := load.Args["credentials"].(*Expression); creds == nil ||
		creds.Class != "Credentials" {
		t.Errorf("no credentials node: %v", load.Args["credentials"])
	}

	// A setting's value is whatever stands there: a number, a keyword, a
	// quoted name, or nothing at all.
	values := `COPY t FROM 'f' WITH (MAXERRORS 10, A NULL, B FALSE, C "q")`
	e, err := ParseOne(values, "duckdb")
	if err != nil {
		t.Fatalf("ParseOne(%q): %v", values, err)
	}
	if got, err := Generate(e, "duckdb"); err != nil || got != values {
		t.Errorf("%q wrote %q, %v", values, got, err)
	}

	// A Credentials the port did not build is one it cannot write: it reads
	// none of them, so an empty node is the only one it can spell.
	for _, key := range []string{"region", "encryption"} {
		withCreds, err := ParseOne("COPY t FROM 'f' WITH (FORMAT x)", "postgres")
		if err != nil {
			t.Fatalf("ParseOne: %v", err)
		}
		creds, _ := withCreds.Args["credentials"].(*Expression)
		value := New("Literal", Arg{"this", "eu"}, Arg{"is_string", true})
		if key == "encryption" {
			creds.Set(key, []*Expression{value})
		} else {
			creds.Set(key, value)
		}
		if got, err := Generate(withCreds, "postgres"); err == nil {
			t.Errorf("wrote credentials the port does not read: %q", got)
		}
	}

	// A word with an argument list after it is a CALL in a file position, not
	// a name followed by the statement's own parameter list. The port read
	// the brackets as parameters and could not read back what it wrote; the
	// generator fuzzer found it.
	// Three things about the writing are the DIALECT's rather than the
	// statement's: whether INTO is written, whether the settings are wrapped,
	// and what separates them. Databricks writes them bare and separated by
	// SPACES, where everyone else wraps them and uses commas.
	for _, tc := range []struct{ sql, dialect string }{
		{"COPY INTO t FROM 'f' FORMAT = JSON DELIM = ','", "databricks"},
		{"COPY tbl FROM 'file' WITH (FORMAT format, HEADER MATCH)", "postgres"},
	} {
		e, err := ParseOne(tc.sql, tc.dialect)
		if err != nil {
			t.Fatalf("[%s] ParseOne(%q): %v", tc.dialect, tc.sql, err)
		}
		if got, err := Generate(e, tc.dialect); err != nil || got != tc.sql {
			t.Errorf("[%s] %q wrote %q, %v", tc.dialect, tc.sql, got, err)
		}
	}

	// A national string is a STRING, not a word: its text is what was inside
	// the quotes, and reading `N'CopY A'` as a word wrote it back without
	// them. The generator fuzzer found it.
	national, err := ParseOne("COPY A FROM N'x'", "duckdb")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if got, err := Generate(national, "duckdb"); err != nil || got != "COPY A FROM N'x'" {
		t.Errorf("wrote %q, %v", got, err)
	}

	call, err := ParseOne("COPY t FROM f(1)", "databricks")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	files, _ := call.Args["files"].([]*Expression)
	if len(files) != 1 || files[0].Class != "Anonymous" {
		t.Errorf("the file is %v, want a call", files)
	}

	for _, tc := range []struct{ sql, dialect string }{
		// A setting whose value is a LIST is read another way again.
		{"COPY t FROM 'f' WITH (FORMAT_OPTIONS ('a'='b'))", "databricks"},
		{"COPY t FROM 'f' WITH (FORMAT JSON", "duckdb"},
		{"COPY t FROM 'f' WITH (FORMAT JSON) EXTRA", "duckdb"},
	} {
		if e, err := ParseOne(tc.sql, tc.dialect); err == nil {
			t.Errorf("[%s] read %q as %v", tc.dialect, tc.sql, e)
		}
	}
}

// TestPosition covers POSITION, which says the same thing two ways round.
func TestPosition(t *testing.T) {
	// `POSITION(a IN b)` and `POSITION(a, b)` both look for a in b, so the
	// same tree comes out of both -- and only the comma form takes a third
	// argument saying where in b to start.
	in, err := ParseOne("SELECT POSITION(a IN b)", "")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	comma, err := ParseOne("SELECT POSITION(a, b)", "")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if !in.Equal(comma) {
		t.Errorf("the two spellings read differently:\n%v\n%v", in, comma)
	}
	for _, tc := range []struct{ sql, dialect, want string }{
		{"SELECT POSITION(a IN b)", "", "SELECT STR_POSITION(b, a)"},
		{"SELECT POSITION(a, b, 3)", "", "SELECT STR_POSITION(b, a, 3)"},
		{"SELECT POSITION(a, b)", "tsql", "SELECT CHARINDEX(a, b)"},
		{"SELECT POSITION(a IN b)", "postgres", "SELECT POSITION(a IN b)"},
	} {
		e, err := ParseOne(tc.sql, tc.dialect)
		if err != nil {
			t.Fatalf("[%s] ParseOne(%q): %v", tc.dialect, tc.sql, err)
		}
		if got, err := Generate(e, tc.dialect); err != nil || got != tc.want {
			t.Errorf("[%s] %q wrote %q, want %q (%v)", tc.dialect, tc.sql, got, tc.want, err)
		}
	}
	if e, err := ParseOne("SELECT POSITION(a IN b", ""); err == nil {
		t.Errorf("read an unclosed POSITION as %v", e)
	}
}

// TestPercentPlaceholder covers PostgreSQL's `%(name)s`, which only that
// dialect reads -- everywhere else a percent sign there is arithmetic with a
// missing left-hand side.
func TestPercentPlaceholder(t *testing.T) {
	for _, sql := range []string{
		"SELECT %(name)s",
		// The name may be a keyword: the parentheses delimit it.
		"SELECT %(select)s",
		"SELECT %(1)s",
		"SELECT %s",
		// A placeholder can be qualified like anything else.
		"SELECT %(name)s.a",
		// A percent that opens nothing a placeholder could be named by
		// falls back to arithmetic, and each of these is read that way.
		"SELECT a % (b)",
		"SELECT a % b",
	} {
		e, err := ParseOne(sql, "postgres")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", sql, err)
		}
		if got, err := Generate(e, "postgres"); err != nil || got != sql {
			t.Errorf("%q wrote %q, %v", sql, got, err)
		}
	}

	// A percent that is not a placeholder is still arithmetic.
	if e, err := ParseOne("SELECT a % (b)", "postgres"); err != nil {
		t.Errorf("ParseOne: %v", err)
	} else if got, _ := Generate(e, "postgres"); got != "SELECT a % (b)" {
		t.Errorf("wrote %q", got)
	}
	// The suffix is required here: the reference reads `%(a)x` as a
	// placeholder followed by an alias, which is a shape this port declines
	// rather than reads two ways.
	if e, err := ParseOne("SELECT %(a)x", "postgres"); err == nil {
		t.Errorf("read %v", e)
	}
	// And no other dialect reads any of it.
	for _, dialect := range []string{"", "tsql", "duckdb", "databricks"} {
		if e, err := ParseOne("SELECT %(name)s", dialect); err == nil {
			t.Errorf("[%s] read %v", dialect, e)
		}
	}
}

// TestConvertStyle covers T-SQL's CONVERT with the style number that says how
// to read the value.
func TestConvertStyle(t *testing.T) {
	for _, sql := range []string{
		"SELECT CONVERT(VARCHAR(10), x, 120)",
		"SELECT CONVERT(INTEGER, x)",
		"SELECT TRY_CONVERT(VARCHAR(10), x, 120)",
	} {
		e, err := ParseOne(sql, "tsql")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", sql, err)
		}
		if got, err := Generate(e, "tsql"); err != nil || got != sql {
			t.Errorf("%q wrote %q, %v", sql, got, err)
		}
	}
	for _, sql := range []string{
		"SELECT CONVERT(INTEGER)",
		"SELECT CONVERT(INTEGER, x",
	} {
		if e, err := ParseOne(sql, "tsql"); err == nil {
			t.Errorf("read %q as %v", sql, e)
		}
	}
}

// TestDropMore covers what a DROP may say beyond the name: several names at
// once, and the words on either side of the kind.
func TestDropMore(t *testing.T) {
	for _, tc := range []struct{ sql, dialect string }{
		// A TABLE or a VIEW may name several at once; everything else names
		// exactly one.
		{"DROP TABLE a, b", ""},
		{"DROP TABLE a.b, c.d", ""},
		{"DROP TABLE IF EXISTS a, b CASCADE", ""},
		{"DROP TABLE a CASCADE", ""},
		{"DROP TABLE a RESTRICT", ""},
		{"DROP TABLE s_hajo CASCADE CONSTRAINTS", ""},
		{"DROP TABLE a PURGE", ""},
		{"DROP TEMPORARY TABLE a", ""},
		{"DROP MATERIALIZED VIEW a", ""},
		// An INDEX is a kind like any other, and PostgreSQL drops one without
		// locking the table -- said BEFORE the IF EXISTS.
		{"DROP INDEX a.b.c", ""},
		{`DROP INDEX "concurrently"`, ""},
		{"DROP INDEX ix_table_id", "postgres"},
		{"DROP INDEX CONCURRENTLY IF EXISTS ix_table_id", "postgres"},
		{"DROP SCHEMA x", ""},
	} {
		e, err := ParseOne(tc.sql, tc.dialect)
		if err != nil {
			t.Fatalf("[%s] ParseOne(%q): %v", tc.dialect, tc.sql, err)
		}
		if !IsWrite(e) {
			t.Errorf("IsWrite(%q) = false", tc.sql)
		}
		if got, err := Generate(e, tc.dialect); err != nil || got != tc.sql {
			t.Errorf("[%s] %q wrote %q, %v", tc.dialect, tc.sql, got, err)
		}
	}

	// A word in front of a name is a name, not a flag: `DROP INDEX
	// "concurrently"` drops an index called that.
	e, err := ParseOne(`DROP INDEX "concurrently"`, "postgres")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if e.Args["concurrently"] != false {
		t.Errorf("a quoted name was read as the flag")
	}

	// Only a TABLE and a VIEW take a list; a second name after anything else
	// is left over.
	if e, err := ParseOne("DROP INDEX a, b", ""); err == nil {
		t.Errorf("read %v; only a TABLE or a VIEW names several", e)
	}
}

// TestCreateSchema covers making a schema, whose name is a DATABASE reference
// rather than a table in one.
func TestCreateSchema(t *testing.T) {
	for _, tc := range []struct{ sql, dialect string }{
		{"CREATE SCHEMA x", ""},
		{"CREATE SCHEMA IF NOT EXISTS y", ""},
		{"CREATE SCHEMA x", "duckdb"},
		{"CREATE SCHEMA testSchema", "tsql"},
		{"CREATE SCHEMA a.b", ""},
	} {
		e, err := ParseOne(tc.sql, tc.dialect)
		if err != nil {
			t.Fatalf("[%s] ParseOne(%q): %v", tc.dialect, tc.sql, err)
		}
		if got, err := Generate(e, tc.dialect); err != nil || got != tc.sql {
			t.Errorf("[%s] %q wrote %q, %v", tc.dialect, tc.sql, got, err)
		}
	}

	// The name lands on `db` and leaves `this` empty, which is how the
	// reference tells a schema from a table.
	e, err := ParseOne("CREATE SCHEMA x", "")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	table, _ := e.Args["this"].(*Expression)
	if table == nil || table.Args["this"] != nil {
		t.Fatalf("the schema names a table: %v", table)
	}
	if db, _ := table.Args["db"].(*Expression); db == nil || db.Args["this"] != "x" {
		t.Errorf("the schema's name is %v", table.Args["db"])
	}
}

// TestCreateSequence covers the statement that makes a source of numbers. Its
// settings are all optional and come in the order the statement writes them,
// so the parser reads until it meets a word it does not know.
func TestCreateSequence(t *testing.T) {
	for _, tc := range []struct{ sql, want string }{
		// START and START WITH are the same thing; the reference writes the
		// longer form back.
		{"CREATE SEQUENCE serial START 101", "CREATE SEQUENCE serial START WITH 101"},
		{"CREATE SEQUENCE serial START WITH 1 INCREMENT BY 2",
			"CREATE SEQUENCE serial START WITH 1 INCREMENT BY 2"},
		{"CREATE SEQUENCE serial START WITH 99 INCREMENT BY -1 MAXVALUE 99",
			"CREATE SEQUENCE serial START WITH 99 INCREMENT BY -1 MAXVALUE 99"},
		// `NO CYCLE` is ONE option, not two.
		{"CREATE SEQUENCE serial START WITH 1 MAXVALUE 10 NO CYCLE",
			"CREATE SEQUENCE serial START WITH 1 MAXVALUE 10 NO CYCLE"},
		{"CREATE SEQUENCE serial START WITH 1 MAXVALUE 10 CYCLE",
			"CREATE SEQUENCE serial START WITH 1 MAXVALUE 10 CYCLE"},
		{"CREATE SEQUENCE seq MINVALUE 1", "CREATE SEQUENCE seq MINVALUE 1"},
		// A sequence that says nothing about its numbers carries no
		// properties at all.
		{"CREATE SEQUENCE seq", "CREATE SEQUENCE seq"},
	} {
		e, err := ParseOne(tc.sql, "duckdb")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		if !IsWrite(e) {
			t.Errorf("IsWrite(%q) = false", tc.sql)
		}
		if got, err := Generate(e, "duckdb"); err != nil || got != tc.want {
			t.Errorf("%q wrote %q, want %q (%v)", tc.sql, got, tc.want, err)
		}
	}

	bare, err := ParseOne("CREATE SEQUENCE seq", "duckdb")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if props, _ := bare.Args["properties"].(*Expression); props != nil {
		t.Errorf("a bare sequence carries %v", props)
	}

	// A valueless option is a Var holding both its words.
	e, err := ParseOne("CREATE SEQUENCE seq NO CYCLE", "duckdb")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	props, _ := e.Args["properties"].(*Expression)
	seq := props.Args["expressions"].([]*Expression)[0]
	options, _ := seq.Args["options"].([]*Expression)
	if len(options) != 1 || options[0].Args["this"] != "NO CYCLE" {
		t.Errorf("the options are %v", options)
	}

	// A cache size, and the column the numbers belong to.
	for _, sql := range []string{
		"CREATE SEQUENCE s CACHE 5",
		"CREATE SEQUENCE s START WITH 1 OWNED BY t.c",
	} {
		e, err := ParseOne(sql, "duckdb")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", sql, err)
		}
		if got, err := Generate(e, "duckdb"); err != nil || got != sql {
			t.Errorf("%q wrote %q, %v", sql, got, err)
		}
	}
	// `OWNED BY NONE` is the default and records nothing.
	e, err = ParseOne("CREATE SEQUENCE s OWNED BY NONE", "duckdb")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	props, _ = e.Args["properties"].(*Expression)
	seq = props.Args["expressions"].([]*Expression)[0]
	if _, ok := seq.Args["owned"]; ok {
		t.Errorf("OWNED BY NONE recorded %v", seq.Args["owned"])
	}

	if e, err := ParseOne("CREATE SEQUENCE seq NONSENSE", "duckdb"); err == nil {
		t.Errorf("read %v", e)
	}
}

// TestNamesThatAreNotIdentifiers covers the two places the reference takes a
// name from a token that is not one.
func TestNamesThatAreNotIdentifiers(t *testing.T) {
	// After a WRITTEN `AS`, any token that is not reserved is the name.
	for _, tc := range []struct{ sql, want string }{
		{"SELECT 1 AS delete, 2 AS alter", "SELECT 1 AS delete, 2 AS alter"},
		{"SELECT x AS INTO FROM bla", "SELECT x AS INTO FROM bla"},
		// A STRING there is a QUOTED name rather than a literal.
		{"SELECT a AS 'b'", `SELECT a AS "b"`},
	} {
		e, err := ParseOne(tc.sql, "")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		if got, err := Generate(e, ""); err != nil || got != tc.want {
			t.Errorf("%q wrote %q, want %q (%v)", tc.sql, got, tc.want, err)
		}
	}
	// The handful the reference reserves is still reserved.
	if e, err := ParseOne("SELECT a AS SELECT", ""); err == nil {
		t.Errorf("read %v", e)
	}
	// And without the word, only an identifier will do -- except in T-SQL,
	// which reads a bare STRING immediately after an expression as an
	// implicit alias too: `SELECT 1 'foo'` names the column foo, the same
	// as a plain word would, just already quoted.
	if e, err := ParseOne("SELECT 1 delete", ""); err == nil {
		t.Errorf("read %v", e)
	}
	if e, err := ParseOne("SELECT 1 'foo'", "duckdb"); err == nil {
		t.Errorf("a bare string named a column outside T-SQL: %v", e)
	}
	if e, err := ParseOne("SELECT 1 'foo'", "tsql"); err != nil {
		t.Errorf("ParseOne: %v", err)
	} else if got, err := Generate(e, "tsql"); err != nil || got != "SELECT 1 AS [foo]" {
		t.Errorf("SELECT 1 'foo' wrote %q (%v)", got, err)
	}

	// A table, a CTE and a created table are all named the same way, so a
	// STRING is a quoted name in each.
	for _, tc := range []struct{ sql, want string }{
		{"SELECT * FROM 'x.y'", `SELECT * FROM "x.y"`},
		{"WITH 'x' AS (SELECT 1) SELECT * FROM x", `WITH "x" AS (SELECT 1) SELECT * FROM x`},
		{"CREATE TEMPORARY TABLE 'temptest' (name INTEGER)",
			`CREATE TEMPORARY TABLE "temptest" (name INT)`},
	} {
		e, err := ParseOne(tc.sql, "duckdb")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		if got, err := Generate(e, "duckdb"); err != nil || got != tc.want {
			t.Errorf("%q wrote %q, want %q (%v)", tc.sql, got, tc.want, err)
		}
	}
}

// TestOnly covers PostgreSQL's ONLY, which says not to read the tables that
// inherit from this one -- and which sits on the TABLE in a FROM and on the
// STATEMENT in an ALTER.
func TestOnly(t *testing.T) {
	for _, sql := range []string{
		"SELECT * FROM ONLY t1",
		"SELECT * FROM ONLY a.b",
		"ALTER TABLE ONLY a ADD CONSTRAINT c UNIQUE (x)",
		"TRUNCATE TABLE ONLY t1, t2 RESTART IDENTITY CASCADE",
	} {
		e, err := ParseOne(sql, "postgres")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", sql, err)
		}
		if got, err := Generate(e, "postgres"); err != nil || got != sql {
			t.Errorf("%q wrote %q, %v", sql, got, err)
		}
	}

	from, err := ParseOne("SELECT * FROM ONLY t1", "postgres")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	fromClause, _ := from.Args["from_"].(*Expression)
	table, _ := fromClause.Args["this"].(*Expression)
	if table.Args["only"] != true {
		t.Errorf("the flag is not on the table: %v", table.Args)
	}
	alter, err := ParseOne("ALTER TABLE ONLY a ADD CONSTRAINT c UNIQUE (x)", "postgres")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if alter.Args["only"] != true {
		t.Errorf("the flag is not on the statement: %v", alter.Args["only"])
	}
	if target, _ := alter.Args["this"].(*Expression); target.Args["only"] != nil {
		t.Errorf("the flag is on the table too: %v", target.Args["only"])
	}
}

// TestInsertParenthesisedQuery covers an INSERT whose parentheses belong to
// the QUERY rather than to a column list -- the two look identical until the
// token after the bracket says which.
func TestInsertParenthesisedQuery(t *testing.T) {
	for _, sql := range []string{
		"INSERT INTO x (SELECT * FROM y)",
		"INSERT INTO y (SELECT 1) UNION (SELECT 2)",
		"INSERT INTO r (WITH t AS (SELECT * FROM s) SELECT * FROM t)",
		// And the column list still reads as one.
		"INSERT INTO x (a, b) VALUES (1, 2)",
		"INSERT INTO x (a) SELECT 1",
	} {
		e, err := ParseOne(sql, "")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", sql, err)
		}
		if got, err := Generate(e, ""); err != nil || got != sql {
			t.Errorf("%q wrote %q, %v", sql, got, err)
		}
	}

	// The query keeps its parentheses: the reference wraps it in a Subquery.
	e, err := ParseOne("INSERT INTO x (SELECT 1)", "")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if inner, _ := e.Args["expression"].(*Expression); inner == nil || inner.Class != "Subquery" {
		t.Errorf("the query is %v, want a Subquery", e.Args["expression"])
	}
	// And the target keeps no column list.
	if target, _ := e.Args["this"].(*Expression); target == nil || target.Class != "Table" {
		t.Errorf("the target is %v, want a bare table", e.Args["this"])
	}
}

// TestCreateTableLike covers `LIKE other` in a column list, which copies
// another table's shape -- and whose parentheses depend on its PARENT.
func TestCreateTableLike(t *testing.T) {
	for _, tc := range []struct{ sql, dialect string }{
		{"CREATE TABLE t1 (LIKE t2)", "postgres"},
		{"CREATE TABLE t1 (col VARCHAR, LIKE t2)", "postgres"},
		{"CREATE TABLE A (LIKE B INCLUDING CONSTRAINT INCLUDING COMPRESSION EXCLUDING COMMENTS)",
			"postgres"},
		{"CREATE TABLE a (LIKE b)", "databricks"},
	} {
		e, err := ParseOne(tc.sql, tc.dialect)
		if err != nil {
			t.Fatalf("[%s] ParseOne(%q): %v", tc.dialect, tc.sql, err)
		}
		if got, err := Generate(e, tc.dialect); err != nil || got != tc.sql {
			t.Errorf("[%s] %q wrote %q, %v", tc.dialect, tc.sql, got, err)
		}
	}

	// DuckDB has no such word: the reference writes the copy as a query
	// instead, which is a different statement and is refused here.
	if e, err := ParseOne("CREATE TABLE a (LIKE b)", "duckdb"); err == nil {
		if got, gerr := Generate(e, "duckdb"); gerr == nil {
			t.Errorf("wrote %q; DuckDB writes a query instead", got)
		}
	}

	// The copy is a property among the columns rather than a column, and what
	// it includes is upper-cased into a bare word.
	e, err := ParseOne("CREATE TABLE A (LIKE B including constraint)", "postgres")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	schema, _ := e.Args["this"].(*Expression)
	items, _ := schema.Args["expressions"].([]*Expression)
	if len(items) != 1 || items[0].Class != "LikeProperty" {
		t.Fatalf("the column list holds %v", items)
	}
	options, _ := items[0].Args["expressions"].([]*Expression)
	if len(options) != 1 {
		t.Fatalf("the options are %v", options)
	}
	if value, _ := options[0].Args["value"].(*Expression); value == nil ||
		value.Args["this"] != "CONSTRAINT" {
		t.Errorf("the option names %v", options[0].Args["value"])
	}
}

// TestComputedColumns covers a column whose value is an expression over the
// others. One statement, two nodes, and the DIALECT decides which: without
// STORED it is a computed column in Databricks and an identity carrying an
// expression everywhere else.
func TestComputedColumns(t *testing.T) {
	for _, tc := range []struct{ sql, dialect string }{
		{"CREATE TABLE t (a INT, b INT GENERATED ALWAYS AS (a + 1))", "duckdb"},
		{"CREATE TABLE t (a INT, b INT GENERATED ALWAYS AS (a + 1))", "databricks"},
		{"CREATE TABLE t (a INT, b INT GENERATED ALWAYS AS (a + 1) STORED)", "postgres"},
	} {
		e, err := ParseOne(tc.sql, tc.dialect)
		if err != nil {
			t.Fatalf("[%s] ParseOne(%q): %v", tc.dialect, tc.sql, err)
		}
		if got, err := Generate(e, tc.dialect); err != nil || got != tc.sql {
			t.Errorf("[%s] %q wrote %q, %v", tc.dialect, tc.sql, got, err)
		}
	}

	// STORED makes it a computed column in every dialect; without it, only
	// where the dialect says so.
	stored, err := ParseOne(
		"CREATE TABLE t (a INT, b INT GENERATED ALWAYS AS (a + 1) STORED)", "postgres")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if kind := constraintKind(stored); kind != "ComputedColumnConstraint" {
		t.Errorf("STORED read as %s", kind)
	}
	bare, err := ParseOne(
		"CREATE TABLE t (a INT, b INT GENERATED ALWAYS AS (a + 1))", "postgres")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if kind := constraintKind(bare); kind != "GeneratedAsIdentityColumnConstraint" {
		t.Errorf("the bare form read as %s", kind)
	}
	databricks, err := ParseOne(
		"CREATE TABLE t (a INT, b INT GENERATED ALWAYS AS (a + 1))", "databricks")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if kind := constraintKind(databricks); kind != "ComputedColumnConstraint" {
		t.Errorf("Databricks read the bare form as %s", kind)
	}
}

// constraintKind names the constraint on the SECOND column of a CREATE TABLE.
func constraintKind(create *Expression) string {
	schema, _ := create.Args["this"].(*Expression)
	columns, _ := schema.Args["expressions"].([]*Expression)
	if len(columns) < 2 {
		return ""
	}
	constraints, _ := columns[1].Args["constraints"].([]*Expression)
	if len(constraints) == 0 {
		return ""
	}
	kind, _ := constraints[0].Args["kind"].(*Expression)
	if kind == nil {
		return constraints[0].Class
	}
	return kind.Class
}

// TestArrayOverAQuery covers the one thing an array literal is not.
func TestArrayOverAQuery(t *testing.T) {
	// An array built from a QUERY is a different thing from a list of
	// values, and is written the same way everywhere -- `ARRAY(...)` --
	// whatever the dialect brackets a list with. DuckDB writes `[1, 2]` for
	// the list and `ARRAY(SELECT 1)` for the query, so the two spellings of
	// the query form meet in the middle.
	for _, tc := range []struct{ sql, want string }{
		{"SELECT [(SELECT 1)]", "SELECT ARRAY((SELECT 1))"},
		{"SELECT ARRAY((SELECT 1))", "SELECT ARRAY((SELECT 1))"},
		{"SELECT ARRAY(SELECT id FROM t)", "SELECT ARRAY(SELECT id FROM t)"},
	} {
		e, err := ParseOne(tc.sql, "duckdb")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		if got, err := Generate(e, "duckdb"); err != nil || got != tc.want {
			t.Errorf("%q wrote %q (%v), want %q", tc.sql, got, err, tc.want)
		}
	}
	// And a list of values keeps the dialect's own brackets.
	for _, tc := range []struct{ dialect, sql string }{
		{"duckdb", "SELECT [1, 2]"},
		{"postgres", "SELECT ARRAY[1, 2]"},
	} {
		e, err := ParseOne(tc.sql, tc.dialect)
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		if got, err := Generate(e, tc.dialect); err != nil || got != tc.sql {
			t.Errorf("%q wrote %q (%v)", tc.sql, got, err)
		}
	}
	// A bracketed expression that is not a query is written as it stands.
	for _, sql := range []string{"SELECT [1, 2]", "SELECT [(1 + 2)]"} {
		e, err := ParseOne(sql, "duckdb")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", sql, err)
		}
		if got, err := Generate(e, "duckdb"); err != nil || got != sql {
			t.Errorf("%q wrote %q, %v", sql, got, err)
		}
	}
}

// TestPrefixCall covers punctuation that NAMES a function rather than being an
// operator: DuckDB's `@x` is ABS(x).
func TestPrefixCall(t *testing.T) {
	for _, tc := range []struct{ sql, want string }{
		{"SELECT @col FROM t", "SELECT ABS(col) FROM t"},
		// It takes a whole arithmetic expression, not just the next operand:
		// `@col + 1` is ABS(col + 1).
		{"SELECT @col + 1 FROM t", "SELECT ABS(col + 1) FROM t"},
		{"SELECT @(-1)", "SELECT ABS((-1))"},
		{"SELECT @(-1) + 1", "SELECT ABS((-1) + 1)"},
		// Bracketed, it stops where the brackets do.
		{"SELECT (@-1) + 1", "SELECT (ABS(-1)) + 1"},
	} {
		e, err := ParseOne(tc.sql, "duckdb")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		if got, err := Generate(e, "duckdb"); err != nil || got != tc.want {
			t.Errorf("%q wrote %q, want %q (%v)", tc.sql, got, tc.want, err)
		}
	}

	// It is read wherever an expression is, including on the left of a SET --
	// where DuckDB's `SET @x = 1` is `SET ABS(x) = 1`, and a reader that only
	// took names there could not read back what the port itself wrote.
	for _, tc := range []struct{ sql, want string }{
		{"SET @0 = 0", "SET ABS(0) = 0"},
		{"SET ABS(0) = 0", "SET ABS(0) = 0"},
	} {
		e, err := ParseOne(tc.sql, "duckdb")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		if got, err := Generate(e, "duckdb"); err != nil || got != tc.want {
			t.Errorf("%q wrote %q, want %q (%v)", tc.sql, got, tc.want, err)
		}
	}

	// The same TOKEN is the parameter marker in the same dialect, which is
	// why this is keyed by the characters rather than by the token type.
	e, err := ParseOne("SELECT $1", "duckdb")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if got, _ := Generate(e, "duckdb"); got != "SELECT $1" {
		t.Errorf("wrote %q", got)
	}
	// And elsewhere `@x` is a parameter, not a call.
	e, err = ParseOne("SELECT @x", "tsql")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if call := e.Args["expressions"].([]*Expression)[0]; call.Class != "Parameter" {
		t.Errorf("T-SQL read @x as %s", call.Class)
	}
}

// TestNextValueFor covers the sequence's next number, whose name is three
// words and whose ordering is recorded as FALSE when it is not written.
func TestNextValueFor(t *testing.T) {
	for _, sql := range []string{
		"SELECT NEXT VALUE FOR db.schema.sequence_name",
		"SELECT NEXT VALUE FOR db.schema.sequence_name OVER (ORDER BY foo), col",
	} {
		e, err := ParseOne(sql, "tsql")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", sql, err)
		}
		if got, err := Generate(e, "tsql"); err != nil || got != sql {
			t.Errorf("%q wrote %q, %v", sql, got, err)
		}
	}

	e, err := ParseOne("SELECT NEXT VALUE FOR s", "tsql")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if call := e.Args["expressions"].([]*Expression)[0]; call.Args["order"] != false {
		t.Errorf("an unordered NEXT VALUE FOR carries order = %v", call.Args["order"])
	}

	for _, sql := range []string{
		"SELECT NEXT VALUE FOR s OVER (foo)",
		"SELECT NEXT VALUE FOR s OVER (ORDER BY foo",
		"SELECT NEXT VALUE FOR s OVER foo",
	} {
		if e, err := ParseOne(sql, "tsql"); err == nil {
			t.Errorf("read %q as %v", sql, e)
		}
	}
}

// TestIntervalIsAlsoAName covers the word INTERVAL standing where a column
// would. The reference reads what follows it and puts everything back where
// there was no quantity there -- so `SELECT interval` selects a column and
// `INTERVAL '1' DAY` is a quantity, from the same word.
func TestIntervalIsAlsoAName(t *testing.T) {
	for _, tc := range []struct{ sql, want string }{
		{"SELECT interval", "SELECT interval"},
		{"SELECT * WHERE interval IS NULL", "SELECT * WHERE interval IS NULL"},
		{"SELECT * WHERE NOT interval IS NULL", "SELECT * WHERE NOT interval IS NULL"},
		{"SELECT interval <> 'foo'", "SELECT interval <> 'foo'"},
		// A bare column IS a quantity where a unit follows it.
		{"SELECT INTERVAL x DAY", "SELECT INTERVAL x DAY"},
		// And anything that is not a bare column is a quantity whatever
		// follows: `interval + 1` is one interval, not a column plus one.
		{"SELECT interval + 1", "SELECT INTERVAL '1'"},
		{"SELECT INTERVAL '1' DAY", "SELECT INTERVAL '1' DAY"},
		{"SELECT INTERVAL 1 DAY", "SELECT INTERVAL '1' DAY"},
		{"SELECT INTERVAL '1 day'", "SELECT INTERVAL '1' DAY"},
		{"SELECT INTERVAL '1' DAY TO HOUR", "SELECT INTERVAL '1' DAY TO HOUR"},
		// The TYPE is untouched by any of this.
		{"SELECT CAST(x AS INTERVAL DAY)", "SELECT CAST(x AS INTERVAL DAY)"},
	} {
		e, err := ParseOne(tc.sql, "")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		if got, err := Generate(e, ""); err != nil || got != tc.want {
			t.Errorf("%q wrote %q, want %q (%v)", tc.sql, got, tc.want, err)
		}
	}

	// The name is a Column, not an Interval carrying nothing.
	e, err := ParseOne("SELECT interval", "")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if got := e.Args["expressions"].([]*Expression)[0]; got.Class != "Column" {
		t.Errorf("a bare INTERVAL read as %s", got.Class)
	}
}

// TestCastAfterIs covers a `::` that follows a null test. The reference runs
// the column operators after an IS as well as after a column, so the cast
// takes the whole TEST rather than the NULL beside it.
func TestCastAfterIs(t *testing.T) {
	for _, tc := range []struct{ sql, dialect, want string }{
		{"SELECT col IS NULL::BOOLEAN", "duckdb", "SELECT CAST(col IS NULL AS BOOLEAN)"},
		{"SELECT col IS NULL::BOOLEAN", "postgres", "SELECT CAST(col IS NULL AS BOOLEAN)"},
		// The negated test carries the cast too -- around whichever of the
		// two shapes the dialect makes of it.
		{"SELECT col IS NOT NULL::BOOLEAN", "duckdb", "SELECT CAST(NOT col IS NULL AS BOOLEAN)"},
		{"SELECT col IS NOT NULL::BOOLEAN", "postgres", "SELECT CAST(col IS NOT NULL AS BOOLEAN)"},
	} {
		e, err := ParseOne(tc.sql, tc.dialect)
		if err != nil {
			t.Fatalf("[%s] ParseOne(%q): %v", tc.dialect, tc.sql, err)
		}
		if got, err := Generate(e, tc.dialect); err != nil || got != tc.want {
			t.Errorf("[%s] %q wrote %q, want %q (%v)", tc.dialect, tc.sql, got, tc.want, err)
		}
	}

	// The cast is OUTSIDE the test, not around the NULL.
	e, err := ParseOne("SELECT col IS NULL::BOOLEAN", "duckdb")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	cast := e.Args["expressions"].([]*Expression)[0]
	if cast.Class != "Cast" {
		t.Fatalf("read as %s", cast.Class)
	}
	if inner, _ := cast.Args["this"].(*Expression); inner == nil || inner.Class != "Is" {
		t.Errorf("the cast wraps %v", cast.Args["this"])
	}

	// A test with nothing after it is unchanged.
	for _, tc := range []struct{ sql, dialect, want string }{
		{"SELECT a IS NULL", "", "SELECT a IS NULL"},
		{"SELECT a IS NOT NULL", "", "SELECT NOT a IS NULL"},
		{"SELECT a IS NOT NULL", "postgres", "SELECT a IS NOT NULL"},
	} {
		e, err := ParseOne(tc.sql, tc.dialect)
		if err != nil {
			t.Fatalf("[%s] ParseOne(%q): %v", tc.dialect, tc.sql, err)
		}
		if got, err := Generate(e, tc.dialect); err != nil || got != tc.want {
			t.Errorf("[%s] %q wrote %q, want %q (%v)", tc.dialect, tc.sql, got, tc.want, err)
		}
	}
}

// TestTimeFormatArguments covers the functions whose second argument is a TIME
// FORMAT, which the builder rewrites into the reference's own spelling on the
// way in and the writer spells back on the way out.
func TestTimeFormatArguments(t *testing.T) {
	for _, tc := range []struct{ sql, dialect string }{
		{"SELECT TO_DATE('05 12 2000', 'DD MM YYYY')", "postgres"},
		{"SELECT TO_DATE('01/01/2000', 'MM/DD/YYYY')", "postgres"},
		{"TO_TIMESTAMP('2020-01-01', 'YYYY-MM-DD')", "postgres"},
		{"TO_CHAR(x, 'YYYY-MM-DD')", "postgres"},
		{"TO_CHAR(x, 'YY-FMMM-SS')", "postgres"},
		// A format that is not a literal is left where it stands.
		{"SELECT TO_CHAR(foo, bar)", "postgres"},
	} {
		e, err := ParseOne(tc.sql, tc.dialect)
		if err != nil {
			t.Fatalf("[%s] ParseOne(%q): %v", tc.dialect, tc.sql, err)
		}
		if got, err := Generate(e, tc.dialect); err != nil || got != tc.sql {
			t.Errorf("[%s] %q wrote %q, %v", tc.dialect, tc.sql, got, err)
		}
	}

	// The tree stores the reference's spelling, not the dialect's.
	e, err := ParseOne("TO_CHAR(x, 'YYYY-MM-DD')", "postgres")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	format, _ := e.Args["format"].(*Expression)
	if format == nil || format.Args["this"] != "%Y-%m-%d" {
		t.Errorf("the stored format is %v", e.Args["format"])
	}

	// Databricks spells a TO_DATE format one way and a DATE_FORMAT one
	// another, both from the same stored text -- more than one table of them,
	// where this port has one. A format is written only where the port's own
	// table means the same thing read back, which mapping out and in again
	// is the test of.
	for _, tc := range []struct{ sql, want string }{
		{"TO_DATE(x, 'yyyy')", "TO_DATE(x, 'yyyy')"},
		{"SELECT TO_DATE(x, 'MM/dd/yyyy')", "SELECT TO_DATE(x, 'MM/dd/yyyy')"},
		{"TO_DATE('1992-01', 'yyyy-d')", "TO_DATE('1992-01', 'yyyy-d')"},
		{"TO_DATE(x, 'MMMM')", "TO_DATE(x, 'MMMM')"},
	} {
		e, err := ParseOne(tc.sql, "databricks")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		if got, err := Generate(e, "databricks"); err != nil || got != tc.want {
			t.Errorf("%q wrote %q (%v), want %q", tc.sql, got, err, tc.want)
		}
	}

	// And one that does NOT come back the same is refused: the port has one
	// table where the dialect has several, so a spelling it cannot verify is
	// a spelling that may say something else.
	e, err = ParseOne("TO_DATE(x, 'q')", "databricks")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if got, err := Generate(e, "databricks"); err == nil {
		t.Errorf("wrote %q; that format does not come back the same", got)
	}

	// A format that IS the dialect's own default is written as nothing.
	e, err = ParseOne(
		"SELECT TIMESTAMPDIFF(MINUTE, CAST(FROM_UNIXTIME(0) AS TIMESTAMP), "+
			"CAST(FROM_UNIXTIME(60) AS TIMESTAMP))", "databricks")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	want := "SELECT TIMESTAMPDIFF(MINUTE, CAST(FROM_UNIXTIME(0) AS TIMESTAMP), " +
		"CAST(FROM_UNIXTIME(60) AS TIMESTAMP))"
	if got, err := Generate(e, "databricks"); err != nil || got != want {
		t.Errorf("wrote %q, %v", got, err)
	}
}

// A parameter whose name is QUOTED keeps its quotes, and one whose bare name
// holds a dollar is refused where the spelling puts a dollar in front of it.
//
// The two are the same defect from either side. `${`$$`}` was read as a Var
// and written back as `${$$}`, which the port could no longer read; `@A$`
// written for PostgreSQL is `$A$`, which is not a parameter at all but a
// dollar-quote tag, and what follows it is swallowed until a matching tag that
// never comes. Both were found by the generator fuzzer.
func TestAParameterWhoseNameHoldsADollar(t *testing.T) {
	// The quotes are what make the name readable, so they survive.
	e, err := ParseOne("${`$$`}", "databricks")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	name, _ := e.Args["this"].(*Expression)
	if name == nil || name.Class != "Identifier" {
		t.Fatalf("the name is %v, want a quoted Identifier", name)
	}
	if quoted, _ := name.Args["quoted"].(bool); !quoted {
		t.Error("the name lost its quotes")
	}
	got, err := Generate(e, "databricks")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if want := "${`$$`}"; got != want {
		t.Errorf("wrote %q, want %q", got, want)
	}

	// A name written as a STRING keeps its quotes too, for the same reason.
	e, err = ParseOne("${'######'}", "databricks")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	name, _ = e.Args["this"].(*Expression)
	if name == nil || name.Class != "Literal" || name.Args["is_string"] != true {
		t.Fatalf("the name is %v, want a string Literal", name)
	}
	if got, err := Generate(e, "databricks"); err != nil || got != "${'######'}" {
		t.Errorf("wrote %q (%v), want ${'######'}", got, err)
	}

	// A bare name holding a dollar is fine where the spelling writes an @,
	// and refused where it writes a $.
	e, err = ParseOne("@A$", "tsql")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if got, err := Generate(e, "tsql"); err != nil || got != "@A$" {
		t.Errorf("T-SQL wrote %q (%v), want %q", got, err, "@A$")
	}
	if got, err := Generate(e, "postgres"); err == nil {
		t.Errorf("PostgreSQL wrote %q; $A$ opens a quote that never closes", got)
	}
}

// A join may name HOW the engine should do it, and one join carries its own
// JOIN in the word itself.
//
// The hint stands between the kind and the JOIN -- `INNER HASH JOIN` -- and
// only T-SQL has any. A dialect that names none writes none: the reference
// drops the word rather than writing one the target does not have, which is
// why the same tree comes back without it elsewhere.
func TestJoinHintsAndStraightJoin(t *testing.T) {
	for _, hint := range []string{"HASH", "LOOP", "MERGE", "REMOTE"} {
		sql := "SELECT x FROM a INNER " + hint + " JOIN b ON b.id = a.id"
		e, err := ParseOne(sql, "tsql")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", sql, err)
		}
		got, err := Generate(e, "tsql")
		if err != nil {
			t.Fatalf("Generate(%q): %v", sql, err)
		}
		if got != sql {
			t.Errorf("got %q, want %q", got, sql)
		}
		// The same tree written for a dialect with no hints loses the word.
		if got, err := Generate(e, "postgres"); err != nil ||
			got != "SELECT x FROM a INNER JOIN b ON b.id = a.id" {
			t.Errorf("PostgreSQL wrote %q (%v); it has no join hints", got, err)
		}
	}

	// STRAIGHT_JOIN is the word AND the join: no second JOIN follows it, and
	// none is written back.
	e, err := ParseOne("SELECT * FROM a STRAIGHT_JOIN b", "")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	got, err := Generate(e, "")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if want := "SELECT * FROM a STRAIGHT_JOIN b"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// PERSISTED says the computed value is written down rather than recomputed,
// and NOT NULL says it may not be null. Only the dialects that spell the
// constraint with a bare AS have anywhere to put the words.
//
// The reference wraps the expression in a GENERATED of its own elsewhere and
// drops them silently, which changes what the column IS rather than how it is
// spelled. The port refuses instead.
func TestAComputedColumnThatIsPersisted(t *testing.T) {
	e, err := ParseOne("CREATE TABLE t (b AS (a * 2) PERSISTED NOT NULL)", "tsql")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	for _, dialect := range []string{"tsql", "duckdb", ""} {
		got, err := Generate(e, dialect)
		if err != nil {
			t.Errorf("Generate(%q): %v", dialect, err)
			continue
		}
		if want := "CREATE TABLE t (b AS (a * 2) PERSISTED NOT NULL)"; got != want {
			t.Errorf("%q wrote %q, want %q", dialect, got, want)
		}
	}
	// PostgreSQL and Databricks write a GENERATED with no room for the words.
	for _, dialect := range []string{"postgres", "databricks"} {
		if got, err := Generate(e, dialect); err == nil {
			t.Errorf("%q wrote %q; it has nowhere to say PERSISTED", dialect, got)
		}
	}
	// A computed column that says neither is written everywhere.
	e, err = ParseOne("CREATE TABLE t (b AS (a * 2))", "tsql")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if got, err := Generate(e, "postgres"); err != nil ||
		got != "CREATE TABLE t (b GENERATED ALWAYS AS ((a * 2)) STORED)" {
		t.Errorf("PostgreSQL wrote %q (%v)", got, err)
	}
}

// A name written in quotes is a name, never punctuation.
//
// DuckDB spells ABS as a `@` in front of its argument, and the port matches
// that by the CHARACTERS because the same token type is the parameter marker.
// A quoted `"@"` has the same characters and is a column: reading it as the
// operator turned `"@":x` into ABS of a parameter, which the port could not
// read back. The generator fuzzer found it.
func TestAQuotedNameThatSpellsAnOperator(t *testing.T) {
	e, err := ParseOne(`SELECT "@"`, "duckdb")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	got, err := Generate(e, "duckdb")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if want := `SELECT "@"`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// The bare form is still the operator, and still takes a whole
	// arithmetic expression.
	for _, tc := range []struct{ sql, want string }{
		{"@x", "ABS(x)"},
		{"SELECT @a + 1", "SELECT ABS(a + 1)"},
	} {
		e, err := ParseOne(tc.sql, "duckdb")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		if got, err := Generate(e, "duckdb"); err != nil || got != tc.want {
			t.Errorf("%q wrote %q (%v), want %q", tc.sql, got, err, tc.want)
		}
	}
}

// The ordering an ordered-set aggregate is computed over, and where each
// dialect puts it.
//
// Almost everywhere it stands outside the call in a WITHIN GROUP of its own.
// DuckDB folds it into the call, and for a percentile also moves the order key
// into the first argument and slides the fraction right. Databricks turns the
// pair into a different function altogether, which is a rewrite rather than a
// spelling, and is refused.
func TestWithinGroup(t *testing.T) {
	for _, tc := range []struct{ name, dialect, sql, want string }{
		{"a percentile", "", "SELECT PERCENTILE_CONT(0.5) WITHIN GROUP (ORDER BY x)",
			"SELECT PERCENTILE_CONT(0.5) WITHIN GROUP (ORDER BY x)"},
		{"a listagg", "", "SELECT LISTAGG(x) WITHIN GROUP (ORDER BY x) AS y",
			"SELECT LISTAGG(x) WITHIN GROUP (ORDER BY x) AS y"},
		{"a mode", "postgres", "SELECT MODE() WITHIN GROUP (ORDER BY x) FROM t",
			"SELECT MODE() WITHIN GROUP (ORDER BY x) FROM t"},
		// DuckDB's percentile takes the order key first and the fraction
		// second, with the ordering still inside the call.
		{"a percentile, folded in", "duckdb",
			"SELECT PERCENTILE_CONT(0.25) WITHIN GROUP (ORDER BY y DESC) FROM t",
			"SELECT QUANTILE_CONT(y, 0.25 ORDER BY y DESC) FROM t"},
		// A call with no arguments leaves the space its empty list had, which
		// is what the reference writes too.
		{"a mode, folded in", "duckdb",
			"SELECT MODE() WITHIN GROUP (ORDER BY col) FILTER (WHERE b < 3) FROM t",
			"SELECT MODE( ORDER BY col) FILTER(WHERE b < 3) FROM t"},
		{"a listagg, left alone", "databricks", "LISTAGG(x, z) WITHIN GROUP (ORDER BY y)",
			"LISTAGG(x, z) WITHIN GROUP (ORDER BY y)"},
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
	// Databricks writes PERCENTILE_APPROX instead, which is a different
	// function rather than a different spelling.
	e, err := ParseOne("SELECT PERCENTILE_CONT(0.5) WITHIN GROUP (ORDER BY x)", "")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if got, err := Generate(e, "databricks"); err == nil {
		t.Errorf("Databricks wrote %q; it writes another function there", got)
	}
}

// What a call does with the rows whose value is missing, and where the words
// go.
//
// Almost everywhere they follow the call. DuckDB writes them INSIDE the
// argument list, and only for the window functions that take them: a call that
// ignores nulls anyway loses the words with nothing else changed, and every
// other use it calls unsupported. The port refuses exactly there, because
// dropping the words off a SUM changes what the call counts.
func TestNullTreatment(t *testing.T) {
	for _, tc := range []struct{ name, dialect, sql, want string }{
		{"a first value", "duckdb",
			"SELECT FIRST_VALUE(c IGNORE NULLS) OVER (PARTITION BY gb ORDER BY ob) FROM t",
			"SELECT FIRST_VALUE(c IGNORE NULLS) OVER (PARTITION BY gb ORDER BY ob) FROM t"},
		{"an nth value", "duckdb",
			"SELECT NTH_VALUE(is_deleted, 2 IGNORE NULLS) OVER (PARTITION BY id) AS n FROM t",
			"SELECT NTH_VALUE(is_deleted, 2 IGNORE NULLS) OVER (PARTITION BY id) AS n FROM t"},
		// The words go after an ordering that is inside the call with them.
		{"a last value over an ordering", "duckdb",
			"SELECT LAST_VALUE(x ORDER BY x IGNORE NULLS) OVER (ORDER BY x) FROM t",
			"SELECT LAST_VALUE(x ORDER BY x IGNORE NULLS) OVER (ORDER BY x) FROM t"},
		// ANY_VALUE ignores nulls of its own accord, so the node is there
		// whether or not the words were written -- and they are not written
		// back, because the call already says it.
		{"a call that ignores them anyway", "duckdb", "SELECT ANY_VALUE(sample_col)",
			"SELECT ANY_VALUE(sample_col)"},
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
	// A dialect that writes the words NOWHERE on an ordinary aggregate drops
	// them: DuckDB takes them only on the window functions that accept them.
	e, err := ParseOne("SELECT SUM(x IGNORE NULLS) AS x", "duckdb")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if got, err := Generate(e, "duckdb"); err != nil || got != "SELECT SUM(x) AS x" {
		t.Errorf("DuckDB wrote %q (%v)", got, err)
	}
	// Everywhere else the words simply follow the call.
	if got, err := Generate(e, ""); err != nil || got != "SELECT SUM(x) IGNORE NULLS AS x" {
		t.Errorf("neutral wrote %q (%v)", got, err)
	}
}

// The guards on both writers, over trees no statement produces.
//
// A WITHIN GROUP or a null treatment stands over a CALL, and the writers reach
// inside the call's parentheses to put the words among its arguments. A node
// carrying something else -- or nothing -- has no parentheses to reach into,
// and is refused rather than written into the middle of whatever is there.
func TestOrderedSetGuards(t *testing.T) {
	column := New("Column", Arg{"this", New("Identifier", Arg{"this", "x"}, Arg{"quoted", false})})
	order := New("Order", Arg{"expressions", []*Expression{New("Ordered", Arg{"this", column})}})

	for _, tc := range []struct {
		name string
		node *Expression
	}{
		{"a within group over nothing", New("WithinGroup", Arg{"expression", order})},
		{"a within group over a column",
			New("WithinGroup", Arg{"this", column}, Arg{"expression", order})},
		{"a null treatment over nothing", New("IgnoreNulls")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := Generate(tc.node, "duckdb"); err == nil {
				t.Errorf("wrote %q; there is no call to write into", got)
			}
		})
	}
	// A percentile whose ordering names no key, or none at all, keeps its
	// arguments where they were: there is nothing to move into the first one.
	for _, tc := range []struct {
		name, want string
		node       *Expression
	}{
		{"no ordering at all", "QUANTILE_CONT(x )",
			New("WithinGroup", Arg{"this", New("PercentileCont", Arg{"this", column})})},
		{"an ordering with no members", "QUANTILE_CONT(x ORDER BY )",
			New("WithinGroup", Arg{"this", New("PercentileCont", Arg{"this", column})},
				Arg{"expression", New("Order")})},
		{"an ordering that names no key", "QUANTILE_CONT(x ORDER BY )",
			New("WithinGroup", Arg{"this", New("PercentileCont", Arg{"this", column})},
				Arg{"expression", New("Order",
					Arg{"expressions", []*Expression{New("Ordered")}})})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := Generate(tc.node, "duckdb"); err != nil || got != tc.want {
				t.Errorf("wrote %q (%v), want %q", got, err, tc.want)
			}
		})
	}
}

// The flags an EXTRACTION carries, which live under a different key from a
// replacement's and are written the same way.
//
// An argument the dialect would leave out of a shorter call has to stay while
// the flags are appended: DuckDB drops a zero group from
// `REGEXP_EXTRACT(a, p)` and keeps it in `REGEXP_EXTRACT(a, p, 0, 'i')`,
// because the flags come after it and something has to hold its place.
func TestRegexpExtractFlags(t *testing.T) {
	for _, tc := range []struct{ sql, want string }{
		{"SELECT REGEXP_EXTRACT(a, 'pattern', 2, 'i')", "SELECT REGEXP_EXTRACT(a, 'pattern', 2, 'i')"},
		{"SELECT REGEXP_EXTRACT(a, 'pattern', 0, 'i')", "SELECT REGEXP_EXTRACT(a, 'pattern', 0, 'i')"},
		{"SELECT REGEXP_EXTRACT(a, 'pattern', 1, 'i')", "SELECT REGEXP_EXTRACT(a, 'pattern', 1, 'i')"},
		// Without the flags the zero group goes, which is the spelling the
		// dialect records for the shorter call.
		{"SELECT REGEXP_EXTRACT(a, 'pattern', 0)", "SELECT REGEXP_EXTRACT(a, 'pattern')"},
		{"SELECT REGEXP_EXTRACT_ALL(s, 'pattern', 0, 'i')",
			"SELECT REGEXP_EXTRACT_ALL(s, 'pattern', 0, 'i')"},
	} {
		e, err := ParseOne(tc.sql, "duckdb")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		if got, err := Generate(e, "duckdb"); err != nil || got != tc.want {
			t.Errorf("%q wrote %q (%v), want %q", tc.sql, got, err, tc.want)
		}
	}
}

// The flags a REGEXP_REPLACE carries, and what each dialect does with them.
//
// A flag says what the replacement DOES -- whether it replaces every match,
// ignores case -- so where one cannot be written the statement is refused
// rather than written without it.
func TestRegexpReplaceFlags(t *testing.T) {
	for _, tc := range []struct{ name, dialect, sql, want string }{
		{"replace every match", "duckdb", "REGEXP_REPLACE(this, pattern, replacement, 'g')",
			"REGEXP_REPLACE(this, pattern, replacement, 'g')"},
		{"several flags", "duckdb", "REGEXP_REPLACE(this, pattern, replacement, 'ims')",
			"REGEXP_REPLACE(this, pattern, replacement, 'ims')"},
		{"an empty replacement", "duckdb", "SELECT REGEXP_REPLACE('mr .', '[^a-zA-Z]', '', 'g')",
			"SELECT REGEXP_REPLACE('mr .', '[^a-zA-Z]', '', 'g')"},
		// No flags at all: the ordinary spelling serves.
		{"no flags", "duckdb", "REGEXP_REPLACE(a, b, c)", "REGEXP_REPLACE(a, b, c)"},
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

	flagged, err := ParseOne("REGEXP_REPLACE(a, b, c, 'g')", "duckdb")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	// Databricks writes no flags at all, so the statement it would write is a
	// different one.
	if got, err := Generate(flagged, "databricks"); err == nil {
		t.Errorf("Databricks wrote %q; it writes no regexp flags", got)
	}
	// A flag DuckDB does not know is dropped by the reference, and refused
	// here.
	unknown := ParseOrFail(t, "REGEXP_REPLACE(a, b, c, 'z')", "duckdb")
	if got, err := Generate(unknown, "duckdb"); err == nil {
		t.Errorf("DuckDB wrote %q; it does not know the flag z", got)
	}
	// Elsewhere the flags are written through, whatever they are.
	if got, err := Generate(unknown, "postgres"); err != nil ||
		got != "REGEXP_REPLACE(a, b, c, 'z')" {
		t.Errorf("PostgreSQL wrote %q (%v)", got, err)
	}
	// DuckDB reads its flags out of a STRING, so a column standing there is a
	// flag it cannot read.
	column := New("RegexpReplace",
		Arg{"this", New("Column", Arg{"this", New("Identifier", Arg{"this", "a"})})},
		Arg{"expression", New("Column", Arg{"this", New("Identifier", Arg{"this", "b"})})},
		Arg{"replacement", New("Column", Arg{"this", New("Identifier", Arg{"this", "c"})})},
		Arg{"modifiers", New("Column", Arg{"this", New("Identifier", Arg{"this", "f"})})},
		// The flag every parsed replacement carries: without it the
		// reference writes a second `g` of its own, which is a different
		// statement and not this one written another way.
		Arg{"single_replace", true})
	if got, err := Generate(column, "duckdb"); err == nil {
		t.Errorf("DuckDB wrote %q; it cannot read flags out of a column", got)
	}
	if got, err := Generate(column, "postgres"); err != nil ||
		got != "REGEXP_REPLACE(a, b, c, f)" {
		t.Errorf("PostgreSQL wrote %q (%v)", got, err)
	}
}

// ParseOrFail parses or ends the test.
func ParseOrFail(t *testing.T, sql, dialect string) *Expression {
	t.Helper()
	e, err := ParseOne(sql, dialect)
	if err != nil {
		t.Fatalf("ParseOne(%q, %q): %v", sql, dialect, err)
	}
	return e
}

// The node that says "read a date out of this", which has no spelling of its
// own outside the dialects that have a call for it.
//
// It becomes the cast it stands for, nothing at all where what it wraps is a
// date already, and nothing at all again under a call that reads a date out of
// its argument anyway -- T-SQL writes YEAR(x) where the tree says
// YEAR(CAST(x AS DATE)).
func TestTsOrDsToDate(t *testing.T) {
	for _, tc := range []struct{ name, dialect, sql, want string }{
		{"under the calls that imply it", "tsql", "SELECT DAY(x), MONTH(x), YEAR(x)",
			"SELECT DAY(x), MONTH(x), YEAR(x)"},
		// EOMONTH is not one of them, so the cast is written.
		{"under one that does not", "tsql", "EOMONTH(GETDATE())",
			"EOMONTH(CAST(GETDATE() AS DATE))"},
		// Already a date: the cast would say nothing the value does not.
		{"over a date already", "tsql", "EOMONTH(CAST(GETDATE() AS DATE))",
			"EOMONTH(CAST(GETDATE() AS DATE))"},
		{"under an argument of one", "tsql", "EOMONTH(GETDATE(), -1)",
			"EOMONTH(DATEADD(MONTH, -1, CAST(GETDATE() AS DATE)))"},
		{"in a computed column", "tsql", "CREATE TABLE foo (x AS YEAR(y))",
			"CREATE TABLE foo (x AS YEAR(y))"},
		// Databricks has a call for it and writes that.
		{"where the dialect has a call", "databricks", "SELECT YEAR(x)", "SELECT YEAR(x)"},
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

// The guards on both writers, over trees the corpus does not reach.
func TestDateAndRegexpGuards(t *testing.T) {
	column := func(name string) *Expression {
		return New("Column", Arg{"this", New("Identifier", Arg{"this", name})})
	}
	regexp := func(extra ...Arg) *Expression {
		args := []Arg{
			{"this", column("a")}, {"expression", column("b")},
			{"modifiers", New("Literal", Arg{"this", "g"}, Arg{"is_string", true})},
		}
		return New("RegexpReplace", append(args, extra...)...)
	}
	// A position or an occurrence says WHICH match to replace, and the port
	// has nowhere to put either alongside the flags.
	for _, key := range []string{"position", "occurrence"} {
		node := regexp(Arg{"replacement", column("c")},
			Arg{key, New("Literal", Arg{"this", "2"}, Arg{"is_string", false})})
		if got, err := Generate(node, "duckdb"); err == nil {
			t.Errorf("wrote %q with a %s", got, key)
		}
	}
	// Flags but nothing to put in place of the match.
	if got, err := Generate(regexp(), "duckdb"); err == nil {
		t.Errorf("wrote %q with nothing to replace the match with", got)
	}
	// A date read out of nothing, and one over a TRY_CAST that already gives
	// a date.
	if got, err := Generate(New("TsOrDsToDate"), "tsql"); err == nil {
		t.Errorf("wrote %q over nothing", got)
	}
	tried := New("TsOrDsToDate", Arg{"this", New("TryCast",
		Arg{"this", column("x")},
		Arg{"to", New("DataType", Arg{"this", DataTypeKind("DATE")})})})
	if got, err := Generate(tried, "tsql"); err != nil || got != "TRY_CAST(x AS DATE)" {
		t.Errorf("wrote %q (%v), want TRY_CAST(x AS DATE)", got, err)
	}
	// A cast to something else is not a date, so the coercion stays.
	other := New("TsOrDsToDate", Arg{"this", New("Cast",
		Arg{"this", column("x")},
		Arg{"to", New("DataType", Arg{"this", DataTypeKind("TEXT")})})})
	if got, err := Generate(other, "tsql"); err != nil ||
		got != "CAST(CAST(x AS VARCHAR(MAX)) AS DATE)" {
		t.Errorf("wrote %q (%v)", got, err)
	}
	// And one asked to be safe becomes a TRY_CAST of its own.
	safe := New("TsOrDsToDate", Arg{"this", column("x")}, Arg{"safe", true})
	if got, err := Generate(safe, "tsql"); err != nil || got != "TRY_CAST(x AS DATE)" {
		t.Errorf("wrote %q (%v), want TRY_CAST(x AS DATE)", got, err)
	}
}

// The guards on the JSON extraction writers, over shapes the corpus does not
// reach.
func TestJSONExtractGuards(t *testing.T) {
	column := func(name string) *Expression {
		return New("Column", Arg{"this", New("Identifier", Arg{"this", name})})
	}
	path := func(parts ...*Expression) *Expression {
		return New("JSONPath", Arg{"expressions", parts})
	}
	extract := func(this, p *Expression) *Expression {
		return New("JSONExtract", Arg{"this", this}, Arg{"expression", p})
	}
	// A path part the port has no spelling for.
	weird := extract(column("x"), path(New("JSONPathWildcard")))
	if got, err := Generate(weird, "postgres"); err == nil {
		t.Errorf("wrote %q over a wildcard", got)
	}
	// A subscript naming no position.
	empty := extract(column("x"), path(New("JSONPathSubscript")))
	if got, err := Generate(empty, "postgres"); err == nil {
		t.Errorf("wrote %q over a subscript naming nothing", got)
	}
	// A path with no parts at all.
	none := extract(column("x"), path())
	if got, err := Generate(none, "postgres"); err == nil {
		t.Errorf("wrote %q over a path with no parts", got)
	}
	// A left side that is neither an atom nor a call.
	loose := extract(New("Or",
		Arg{"this", column("a")}, Arg{"expression", column("b")}), path(
		New("JSONPathKey", Arg{"this", "k"})))
	if got, err := Generate(loose, "postgres"); err == nil {
		t.Errorf("wrote %q over a connector", got)
	}
	// And one whose path is a compound expression rather than a path.
	compound := New("JSONExtract", Arg{"this", column("a")},
		Arg{"expression", New("Or",
			Arg{"this", column("b")}, Arg{"expression", column("c")})})
	if got, err := Generate(compound, "postgres"); err == nil {
		t.Errorf("wrote %q over a compound path", got)
	}
	// A JSON extraction with no path at all.
	if got, err := Generate(New("JSONExtract", Arg{"this", column("a")}), "duckdb"); err == nil {
		t.Errorf("wrote %q with no path", got)
	}
}

// A slot is not sensitive to casting as such, but to being cast to particular
// TYPES -- and a literal in a sensitive slot may still be one the dialect has
// a name for.
//
// Both guards exist because the reference sometimes rewrites a call around an
// argument whose type it can see. Refusing every cast and every literal in
// those slots turned away calls that render exactly as they were written.
func TestArgumentShapeGuards(t *testing.T) {
	for _, tc := range []struct{ name, dialect, sql, want string }{
		// DuckDB wraps a non-text argument to a string function in a cast to
		// TEXT, and leaves one that is already TEXT alone.
		{"a cast the call does not mind", "duckdb", "SELECT UPPER(CAST('true' AS TEXT))",
			"SELECT UPPER(CAST('true' AS TEXT))"},
		{"a replace over text", "duckdb", "SELECT REPLACE(CAST(x AS TEXT), '-', '')",
			"SELECT REPLACE(CAST(x AS TEXT), '-', '')"},
		{"a hash over a blob", "duckdb", "SELECT SHA1(CAST(UNHEX('002A') AS BLOB))",
			"SELECT SHA1(CAST(UNHEX('002A') AS BLOB))"},
		// PostgreSQL's ROUND wraps a DOUBLE in a cast to DECIMAL and leaves a
		// DECIMAL alone.
		{"a round over a decimal", "postgres", "SELECT ROUND(CAST(x AS DECIMAL), 4)",
			"SELECT ROUND(CAST(x AS DECIMAL), 4)"},
		{"a round over a sized decimal", "postgres", "SELECT ROUND(CAST(x AS DECIMAL(18, 3)), 4)",
			"SELECT ROUND(CAST(x AS DECIMAL(18, 3)), 4)"},
		// The scale of a unix timestamp picks the call's NAME where the
		// dialect has one for it.
		{"a scale with a name", "duckdb", "SELECT EPOCH_MS(10) AS t", "SELECT EPOCH_MS(10) AS t"},
		{"another scale with a name", "duckdb", "SELECT MAKE_TIMESTAMP(10) AS t",
			"SELECT MAKE_TIMESTAMP(10) AS t"},
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
	// A cast to a type the call DOES mind takes the WRAPPER the dialect puts
	// round it, which differs by type: DuckDB rounds a float into a BIT_OR
	// and casts a decimal without rounding.
	for _, tc := range []struct{ sql, want string }{
		{"SELECT BIT_OR(CAST(val AS FLOAT)) FROM t",
			"SELECT BIT_OR(CAST(ROUND(CAST(val AS REAL)) AS INT)) FROM t"},
		{"SELECT BIT_AND(CAST(val AS DOUBLE)) FROM t",
			"SELECT BIT_AND(CAST(ROUND(CAST(val AS DOUBLE)) AS INT)) FROM t"},
		{"SELECT BIT_OR(CAST(val AS DECIMAL(10, 2))) FROM t",
			"SELECT BIT_OR(CAST(CAST(val AS DECIMAL(10, 2)) AS INT)) FROM t"},
		// And an INTEGER needs none of it.
		{"SELECT BIT_OR(CAST(val AS INT)) FROM t", "SELECT BIT_OR(CAST(val AS INT)) FROM t"},
	} {
		e, err := ParseOne(tc.sql, "duckdb")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		if got, err := Generate(e, "duckdb"); err != nil || got != tc.want {
			t.Errorf("%q wrote %q (%v), want %q", tc.sql, got, err, tc.want)
		}
	}
	// The wrapper belongs to an ARITY as well as a type: PostgreSQL's ROUND
	// casts its argument only when a number of decimals was given, and takes
	// an early exit with one argument.
	for _, tc := range []struct{ sql, want string }{
		{"ROUND(x::DOUBLE, 4)", "ROUND(CAST(CAST(x AS DOUBLE PRECISION) AS DECIMAL), 4)"},
		{"ROUND(x::DOUBLE)", "ROUND(CAST(x AS DOUBLE PRECISION))"},
	} {
		e, err := ParseOne(tc.sql, "postgres")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		if got, err := Generate(e, "postgres"); err != nil || got != tc.want {
			t.Errorf("%q wrote %q (%v), want %q", tc.sql, got, err, tc.want)
		}
	}
}

// A call whose only argument is a LIST takes as many members as it is given,
// and each count may be spelled differently.
//
// T-SQL writes `CONCAT(a, b)` for two and just `a` for one -- the call
// disappears there, which is a rewrite rather than a spelling -- so the call
// form belongs to the wider counts alone and the narrow one is refused.
func TestConcatByArgumentCount(t *testing.T) {
	for _, tc := range []struct{ dialect, sql, want string }{
		{"tsql", "SELECT CONCAT(column1, column2)", "SELECT CONCAT(column1, column2)"},
		{"tsql", "SELECT CONCAT(a, b, c)", "SELECT CONCAT(a, b, c)"},
		{"databricks", "SELECT CONCAT(a, b)", "SELECT CONCAT(a, b)"},
	} {
		e, err := ParseOne(tc.sql, tc.dialect)
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		if got, err := Generate(e, tc.dialect); err != nil || got != tc.want {
			t.Errorf("%q wrote %q (%v), want %q", tc.sql, got, err, tc.want)
		}
	}
	// One argument is written WITHOUT the call: T-SQL concatenates nothing
	// with nothing there, so the value stands on its own.
	e, err := ParseOne("CONCAT(a)", "tsql")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if got, err := Generate(e, "tsql"); err != nil || got != "a" {
		t.Errorf("wrote %q (%v), want %q", got, err, "a")
	}
}

// How much of a table a query reads. Every part of the spelling is the
// dialect's: the words that open it, whether the sampling METHOD is written,
// whether a bare number counts rows or a percentage, and what the repeatable
// seed is called.
func TestTableSampleSpelling(t *testing.T) {
	for _, tc := range []struct{ dialect, sql, want string }{
		{"postgres", "SELECT * FROM t TABLESAMPLE SYSTEM (10)",
			"SELECT * FROM t TABLESAMPLE SYSTEM (10)"},
		{"tsql", "SELECT * FROM t TABLESAMPLE (10 PERCENT)",
			"SELECT * FROM t TABLESAMPLE (10 PERCENT)"},
	} {
		e, err := ParseOne(tc.sql, tc.dialect)
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		if got, err := Generate(e, tc.dialect); err != nil || got != tc.want {
			t.Errorf("%q wrote %q (%v), want %q", tc.sql, got, err, tc.want)
		}
	}
}

// A name written in quotes is a name, never the word it spells.
//
// T-SQL brackets a name the same way it brackets any other, so `[IF]` is a
// column called IF -- and reading it as the keyword that opens an IF statement
// left the port unable to read back what it had written. The generator fuzzer
// found it.
func TestAQuotedNameThatSpellsAKeyword(t *testing.T) {
	for _, sql := range []string{"[IF]", `"IF"`, "[SELECT]", "[FROM]", "[WHERE]"} {
		e, err := ParseOne(sql, "tsql")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", sql, err)
		}
		got, err := Generate(e, "tsql")
		if err != nil {
			t.Fatalf("Generate(%q): %v", sql, err)
		}
		if _, err := ParseOne(got, "tsql"); err != nil {
			t.Errorf("%q wrote %q, which reads back as: %v", sql, got, err)
		}
	}
	// The bare word still opens the statement it names.
	if e, err := ParseOne("IF 1 = 1 SELECT 1", "tsql"); err != nil {
		t.Errorf(`ParseOne("IF 1 = 1 SELECT 1"): %v`, err)
	} else if e.Class != "IfBlock" {
		t.Errorf("a bare IF read as %s, want IfBlock", e.Class)
	}
}

// The pair spelling writes its operand TWICE, so an operand that is itself
// written twice doubles again.
//
// A chain of twenty extractions would come out a million times over. The
// reference does exactly that and the fuzzer ran out of memory on it; the port
// declines rather than emitting SQL whose size grows like that.
func TestJSONExtractOverAnother(t *testing.T) {
	e, err := ParseOne("''->''->''->''->''", "tsql")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if got, err := Generate(e, "tsql"); err == nil {
		t.Errorf("wrote %d characters for a chain of five", len(got))
	}
	// One on its own is still written.
	one, err := ParseOne("SELECT JSON_VALUE(x, '$.y')", "tsql")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if got, err := Generate(one, "tsql"); err != nil ||
		got != "SELECT ISNULL(JSON_QUERY(x, '$.y'), JSON_VALUE(x, '$.y'))" {
		t.Errorf("wrote %q (%v)", got, err)
	}
}

// A JSON path may be a CALL or an array LITERAL rather than a leaf: one builds
// the path and the other names several. Both carry their own delimiters, so
// neither needs brackets beside an operator.
func TestJSONPathThatIsNotALeaf(t *testing.T) {
	for _, tc := range []struct{ dialect, sql, want string }{
		{"databricks", "SELECT GET_JSON_OBJECT(col, CONCAT('$.', field_name))",
			"SELECT GET_JSON_OBJECT(col, CONCAT('$.', field_name))"},
		{"duckdb", "SELECT '{}' ->> ['$.a', '$.b']", "SELECT '{}' ->> ['$.a', '$.b']"},
		{"duckdb", "SELECT JSON_EXTRACT_STRING('{}', ['$.a', '$.b'])",
			"SELECT '{}' ->> ['$.a', '$.b']"},
	} {
		e, err := ParseOne(tc.sql, tc.dialect)
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		if got, err := Generate(e, tc.dialect); err != nil || got != tc.want {
			t.Errorf("%q wrote %q (%v), want %q", tc.sql, got, err, tc.want)
		}
	}
	// A path that is neither -- a connector, which brackets by precedence --
	// is still refused.
	column := func(name string) *Expression {
		return New("Column", Arg{"this", New("Identifier", Arg{"this", name})})
	}
	loose := New("JSONExtract", Arg{"this", column("a")},
		Arg{"expression", New("Or", Arg{"this", column("b")}, Arg{"expression", column("c")})})
	if got, err := Generate(loose, "duckdb"); err == nil {
		t.Errorf("wrote %q over a connector", got)
	}
	// And a list that is not one whole group.
	if writesAsAList("[a], [b]") {
		t.Error(`"[a], [b]" is two lists, not one`)
	}
	if !writesAsAList("[[a], [b]]") {
		t.Error(`"[[a], [b]]" is one list`)
	}
}

// Three narrowings, each a fact the port had refused for want of asking.
func TestRefusalsNarrowed(t *testing.T) {
	for _, tc := range []struct{ name, dialect, sql, want string }{
		// A format written the dialect's way, kept where mapping out and
		// back in again lands on what was stored.
		{"a parse format", "databricks", "SELECT TO_TIMESTAMP('2016-12-31', 'yyyy-MM-dd')",
			"SELECT TO_TIMESTAMP('2016-12-31', 'yyyy-MM-dd')"},
		{"a longer parse format", "databricks",
			"SELECT TO_TIMESTAMP('2016-12-31 03:04:05', 'yyyy-MM-dd HH:mm:ss')",
			"SELECT TO_TIMESTAMP('2016-12-31 03:04:05', 'yyyy-MM-dd HH:mm:ss')"},
		// A NOT binds looser than the arrow, so it takes brackets on the
		// right the same way an operator does.
		{"a negated path", "postgres", "SELECT JSON_EXTRACT(a, NOT x)", "SELECT a -> (NOT x)"},
		// Every kind of CREATE carries the guard, not only the four a table
		// takes: the probe had asked about TABLE, VIEW, INDEX and SCHEMA and
		// nothing else, so a PROCEDURE was refused for a fact nobody had
		// looked up.
		{"a guarded procedure", "", "CREATE PROCEDURE IF NOT EXISTS a.b.c() AS 'DECLARE BEGIN; END'",
			"CREATE PROCEDURE IF NOT EXISTS a.b.c() AS 'DECLARE BEGIN; END'"},
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

// A coercion the dialect applies whatever it is given is IDEMPOTENT: an
// argument already carrying that cast leaves it nothing to add.
//
// DuckDB writes `BOOL_OR(CAST(x AS BOOLEAN))` for any argument at all, so the
// probe that feeds it a plain column records a cast it would then apply a
// second time -- which is why the port used to refuse the whole class. Probed
// again with the cast already in place, the spelling that comes back is exact
// for exactly those arguments, and is written only for them.
func TestAnIdempotentCoercion(t *testing.T) {
	for _, tc := range []struct{ sql, want string }{
		{"SELECT BOOL_OR(CAST(c1 AS BOOLEAN)), BOOL_OR(CAST(c2 AS BOOLEAN)) FROM test",
			"SELECT BOOL_OR(CAST(c1 AS BOOLEAN)), BOOL_OR(CAST(c2 AS BOOLEAN)) FROM test"},
		{"SELECT BOOL_AND(CAST(c1 AS BOOLEAN)) FROM test",
			"SELECT BOOL_AND(CAST(c1 AS BOOLEAN)) FROM test"},
		{"SELECT EPOCH(CAST('2009-02-13' AS TIMESTAMP))",
			"SELECT EPOCH(CAST('2009-02-13' AS TIMESTAMP))"},
	} {
		e, err := ParseOne(tc.sql, "duckdb")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		if got, err := Generate(e, "duckdb"); err != nil || got != tc.want {
			t.Errorf("%q wrote %q (%v), want %q", tc.sql, got, err, tc.want)
		}
	}
	// A slot the dialect coerces but the node does not fill asks nothing of
	// the argument that is not there: an empty LOGICAL_OR is written by its
	// own spelling rather than refused for a cast it never had.
	if got, err := Generate(New("LogicalOr"), "duckdb"); err != nil || got != "BOOL_OR()" {
		t.Errorf("an argumentless BOOL_OR wrote %q (%v)", got, err)
	}

	// And an argument with NO cast to absorb it is GIVEN one: the dialect
	// adds it whatever it is handed, so adding it and writing the plain
	// spelling says exactly what the dialect says.
	for _, tc := range []struct{ sql, want string }{
		{"SELECT a, LOGICAL_OR(b) FROM foo GROUP BY a",
			"SELECT a, BOOL_OR(CAST(b AS BOOLEAN)) FROM foo GROUP BY a"},
		{"SELECT TO_VARIANT('1')", "SELECT CAST('1' AS VARIANT)"},
		{"SELECT TIME_STR_TO_UNIX('2009-02-13 23:31:30')",
			"SELECT EPOCH(CAST('2009-02-13 23:31:30' AS TIMESTAMP))"},
	} {
		e, err := ParseOne(tc.sql, "duckdb")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		if got, err := Generate(e, "duckdb"); err != nil || got != tc.want {
			t.Errorf("%q wrote %q (%v), want %q", tc.sql, got, err, tc.want)
		}
	}
	// The slot absorbs a FAMILY of types, not one: Databricks leaves both a
	// TIMESTAMP and a TIMESTAMPTZ alone and wraps everything else.
	tz, err := ParseOne("SELECT DATE_FORMAT(CAST('2024-07-08 13:45:00' AS TIMESTAMP), 'EEE')",
		"databricks")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if got, err := Generate(tz, "databricks"); err != nil ||
		got != "SELECT DATE_FORMAT(CAST('2024-07-08 13:45:00' AS TIMESTAMP), 'EEE')" {
		t.Errorf("wrote %q (%v)", got, err)
	}
}

// Where a dialect spells a node as an OPERATOR it brackets an operand by
// PRECEDENCE, and which operands those are is measured beside the template.
//
// DuckDB writes `INTERVAL 1 DAY` for a literal and `INTERVAL (a + b) DAY` for
// anything else. One probe alone recorded whichever form it happened to see
// and wrote that for both; two probes tell the operand's own brackets from the
// template's, and the writer puts them back where the operand needs them.
func TestAnOperandBracketedByPrecedence(t *testing.T) {
	// Which keys a template brackets is a list beside it, and the writer asks
	// that list per key. No dialect the port carries reaches this through a
	// template today -- the classes that need it have writers of their own --
	// so the question itself is asked here.
	if !bracketsByPrecedence([]string{"this", "expression"}, "expression") {
		t.Error("a key in the list is bracketed")
	}
	if bracketsByPrecedence([]string{"this"}, "expression") {
		t.Error("a key not in the list is not")
	}
	if bracketsByPrecedence(nil, "this") {
		t.Error("a template that brackets nothing brackets nothing")
	}

	for _, tc := range []struct{ sql, want string }{
		{"SELECT INTERVAL (a + b) DAY", "SELECT INTERVAL (a + b) DAY"},
		{"SELECT INTERVAL 1 DAY", "SELECT INTERVAL '1' DAY"},
		{"SELECT INTERVAL a DAY", "SELECT INTERVAL a DAY"},
	} {
		e, err := ParseOne(tc.sql, "duckdb")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		if got, err := Generate(e, "duckdb"); err != nil || got != tc.want {
			t.Errorf("%q wrote %q (%v), want %q", tc.sql, got, err, tc.want)
		}
	}
}

// A dialect with no boolean type compares values into conditions.
//
// T-SQL writes `NOT c <> 0` for `NOT c`, and `a = 1` for `a IS TRUE` -- the
// negation of the second moving outside the comparison rather than into the
// operator. Both are the dialect's own way of saying what the statement said,
// so the port writes them rather than refusing.
func TestBooleanCoercion(t *testing.T) {
	for _, tc := range []struct{ sql, want string }{
		{"a IS TRUE", "a = 1"},
		{"a IS NOT FALSE", "NOT a = 0"},
		{"SELECT * FROM t WHERE NOT c", "SELECT * FROM t WHERE NOT c <> 0"},
		{"1 AND true", "1 <> 0 AND (1 = 1)"},
		{"CAST(x AS int) OR y", "CAST(x AS INTEGER) <> 0 OR y <> 0"},
		// A value that is ALREADY a condition is left as it stands.
		{"a = 1 AND b > 2", "a = 1 AND b > 2"},
		// WHERE and HAVING coerce their own operand, the same as a bare
		// operand of AND/OR/NOT does -- not just what sits under one.
		{"SELECT 1 FROM t WHERE x", "SELECT 1 FROM t WHERE x <> 0"},
		{"SELECT 1 FROM t GROUP BY x HAVING x", "SELECT 1 FROM t GROUP BY x HAVING x <> 0"},
		{"SELECT 1 FROM t WHERE x = 1", "SELECT 1 FROM t WHERE x = 1"},
		// A JOIN's ON, unlike WHERE and HAVING, is left alone at its own top
		// level -- only a nested AND/OR/NOT operand is compared into one.
		{"SELECT 1 FROM t JOIN u ON x", "SELECT 1 FROM t JOIN u ON x"},
		{"SELECT 1 FROM t JOIN u ON x AND y", "SELECT 1 FROM t JOIN u ON x <> 0 AND y <> 0"},
	} {
		e, err := ParseOne(tc.sql, "tsql")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		if got, err := Generate(e, "tsql"); err != nil || got != tc.want {
			t.Errorf("%q wrote %q (%v), want %q", tc.sql, got, err, tc.want)
		}
	}
	// A dialect that HAS a boolean writes the same tree without any of it.
	for _, sql := range []string{
		"SELECT * FROM t WHERE NOT c",
		"SELECT 1 FROM t WHERE x",
		"SELECT 1 FROM t GROUP BY x HAVING x",
	} {
		e, err := ParseOne(sql, "tsql")
		if err != nil {
			t.Fatalf("ParseOne: %v", err)
		}
		if got, err := Generate(e, "duckdb"); err != nil || got != sql {
			t.Errorf("DuckDB wrote %q (%v), want %q", got, err, sql)
		}
	}
}

// A comma join over an UNNEST is written as an explicit JOIN where the dialect
// needs one: the comma form does not bind the unnested rows to the row they
// came from, so `ON TRUE` says what the comma left unsaid.
func TestCommaJoinOverAnUnnest(t *testing.T) {
	e, err := ParseOne(`SELECT * FROM t1, UNNEST("t1") "t1" ("col")`, "duckdb")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if got, err := Generate(e, "duckdb"); err != nil ||
		got != `SELECT * FROM t1 JOIN UNNEST("t1") AS "t1"("col") ON TRUE` {
		t.Errorf("DuckDB wrote %q (%v)", got, err)
	}
	// PostgreSQL keeps the comma.
	p, err := ParseOne("SELECT id FROM example_table, UNNEST(scores) AS t(s)", "postgres")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if got, err := Generate(p, "postgres"); err != nil ||
		got != "SELECT id FROM example_table, UNNEST(scores) AS t(s)" {
		t.Errorf("PostgreSQL wrote %q (%v)", got, err)
	}
}

// A dollar already written pairs with the next one and opens a quote, so what
// a writer records while rendering depends on the ORDER it renders in.
//
// PostgreSQL's `?` operator rendered its right operand before its left, so a
// name ending in a dollar was written before the parameter whose dollar it
// would pair with -- and `$00 ? ݖ$` came back as an unterminated quote. The
// generator fuzzer found it.
func TestOperandsAreWrittenLeftToRight(t *testing.T) {
	e, err := ParseOne("$ 00?ݖ$-- ", "postgres")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if got, err := Generate(e, "postgres"); err == nil {
		t.Errorf("wrote %q, whose dollars pair into a quote", got)
	}
	// The same name with nothing to pair with is written as it stands.
	alone, err := ParseOne("SELECT a$", "postgres")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if got, err := Generate(alone, "postgres"); err != nil || got != "SELECT a$" {
		t.Errorf("wrote %q (%v)", got, err)
	}
}

// An empty string after a dollar already written is not a string to the
// tokenizer: `$1 = ”` reads the two quotes as the tag of a dollar-quote that
// never closes, and the reference cannot read it back either.
func TestAnEmptyStringAfterADollar(t *testing.T) {
	e, err := ParseOne("SET@0==''", "postgres")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if got, err := Generate(e, "postgres"); err == nil {
		t.Errorf("wrote %q, whose quotes read as a dollar-quote tag", got)
	}
	// A string with something in it is fine, and so is an empty one with no
	// dollar in front of it.
	for _, tc := range []struct{ sql, want string }{
		{"SET @1 = 'a'", "SET $1 = 'a'"},
		{"SELECT ''", "SELECT ''"},
	} {
		e, err := ParseOne(tc.sql, "postgres")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		if got, err := Generate(e, "postgres"); err != nil || got != tc.want {
			t.Errorf("%q wrote %q (%v), want %q", tc.sql, got, err, tc.want)
		}
	}
}

// What a dialect has nowhere to say, it says nothing about.
//
// DuckDB writes no storage format and no column comment in a CREATE at all,
// and the reference drops both rather than inventing a place for them.
func TestWhatADialectWritesNowhere(t *testing.T) {
	for _, tc := range []struct{ sql, want string }{
		{"CREATE TABLE IF NOT EXISTS t (cola INT, colb STRING) USING ICEBERG PARTITIONED BY (colb)",
			"CREATE TABLE IF NOT EXISTS t (cola INT, colb TEXT)"},
		{"CREATE TABLE IF NOT EXISTS t (cola INT COMMENT 'cola', colb STRING) USING ICEBERG " +
			"PARTITIONED BY (colb)",
			"CREATE TABLE IF NOT EXISTS t (cola INT, colb TEXT)"},
	} {
		e, err := ParseOne(tc.sql, "duckdb")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		if got, err := Generate(e, "duckdb"); err != nil || got != tc.want {
			t.Errorf("%q wrote %q (%v), want %q", tc.sql, got, err, tc.want)
		}
	}
	// Databricks keeps the comment, which is why the flag is asked per
	// dialect rather than assumed.
	e, err := ParseOne("CREATE TABLE t (a INT COMMENT 'hi')", "databricks")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if got, err := Generate(e, "databricks"); err != nil ||
		got != "CREATE TABLE t (a INT COMMENT 'hi')" {
		t.Errorf("Databricks wrote %q (%v)", got, err)
	}
}

// A coercion the dialect applies across SLOTS is absorbed when every one of
// them already carries it: Databricks casts both ends of a SEQUENCE to DATE,
// and a call whose ends are both DATE casts leaves it nothing to add.
func TestACoercionAcrossSlots(t *testing.T) {
	sql := "SELECT * FROM EXPLODE(SEQUENCE(CAST('2020-01-01' AS DATE), " +
		"CAST('2020-02-01' AS DATE), INTERVAL '1' WEEK))"
	e, err := ParseOne(sql, "databricks")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if got, err := Generate(e, "databricks"); err != nil || got != sql {
		t.Errorf("wrote %q (%v)", got, err)
	}
}

// The slot-wide absorption asks about EVERY coerced slot, so a call with only
// one of them, or with one still uncast, is not absorbed.
func TestACoercionAcrossSlotsIsAllOrNothing(t *testing.T) {
	// One end cast and the other not: Databricks would cast the second, which
	// the plain spelling does not say.
	partial := New("GenerateSeries",
		Arg{"start", New("Cast",
			Arg{"this", New("Literal", Arg{"this", "2020-01-01"}, Arg{"is_string", true})},
			Arg{"to", New("DataType", Arg{"this", DataTypeKind("DATE")})})},
		Arg{"end", New("Column", Arg{"this", New("Identifier", Arg{"this", "e"})})})
	if got, err := Generate(partial, "databricks"); err == nil {
		t.Errorf("wrote %q; the other end would be cast too", got)
	}
	// A slot the node leaves EMPTY asks nothing of it.
	half := New("GenerateSeries", Arg{"start", New("Cast",
		Arg{"this", New("Literal", Arg{"this", "2020-01-01"}, Arg{"is_string", true})},
		Arg{"to", New("DataType", Arg{"this", DataTypeKind("DATE")})})})
	if _, err := Generate(half, "databricks"); err != nil {
		t.Errorf("a series with one end refused: %v", err)
	}
}

// A STRING LITERAL is coerced by what it IS rather than by a cast it carries.
//
// DuckDB reads a date written as text and casts it, and takes the words about
// missing rows only on the window functions that accept them -- dropping them
// elsewhere rather than refusing the call.
func TestCoercionsByWhatTheArgumentIs(t *testing.T) {
	for _, tc := range []struct{ sql, want string }{
		{"SELECT DATE_DIFF('QUARTER', '2009-02-13', '2013-09-01')",
			"SELECT DATE_DIFF('QUARTER', CAST('2009-02-13' AS DATE), " +
				"CAST('2013-09-01' AS DATE))"},
		{"SELECT SUM(x IGNORE NULLS) AS x", "SELECT SUM(x) AS x"},
	} {
		e, err := ParseOne(tc.sql, "duckdb")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		if got, err := Generate(e, "duckdb"); err != nil || got != tc.want {
			t.Errorf("%q wrote %q (%v), want %q", tc.sql, got, err, tc.want)
		}
	}
	// A dialect that HAS the words writes them.
	e, err := ParseOne("SELECT SUM(x IGNORE NULLS) AS x", "duckdb")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	if got, err := Generate(e, "databricks"); err != nil ||
		got != "SELECT SUM(x) IGNORE NULLS AS x" {
		t.Errorf("Databricks wrote %q (%v)", got, err)
	}
}

// The remaining guards on the property placer and the regexp writer, over
// shapes the corpus does not reach.
func TestPlacementGuards(t *testing.T) {
	// A property the port has nowhere to put is refused rather than dropped.
	unplaceable := New("Create",
		Arg{"this", New("Table", Arg{"this", New("Identifier", Arg{"this", "t"})})},
		Arg{"kind", "TABLE"},
		Arg{"properties", New("Properties", Arg{"expressions", []*Expression{
			New("NotAPropertyThisPortPlaces"),
		}})})
	if got, err := Generate(unplaceable, "duckdb"); err == nil {
		t.Errorf("wrote %q for a property with no place", got)
	}
	// A regexp call whose spelling is not a call has no closing parenthesis
	// to put the flags inside.
	flagged := New("RegexpExtract",
		Arg{"this", New("Column", Arg{"this", New("Identifier", Arg{"this", "a"})})},
		Arg{"parameters", New("Literal", Arg{"this", "i"}, Arg{"is_string", true})})
	if got, err := Generate(flagged, "duckdb"); err == nil {
		t.Errorf("wrote %q for a call with no arguments to flag", got)
	}
}

// A date shifted by an interval is written with an OPERATOR where the dialect
// spells it that way.
//
// The amount carries the unit: an interval is written as it stands, and a bare
// number takes the unit the shift names, defaulting to days -- which is what
// the reference supplies when the statement named none. A date written as TEXT
// is cast first, because a string and an interval do not add.
func TestDateShiftedByAnInterval(t *testing.T) {
	for _, tc := range []struct{ dialect, sql, want string }{
		{"duckdb", "SELECT DATE_ADD(d, INTERVAL 1 DAY) FROM t",
			"SELECT d + INTERVAL '1' DAY FROM t"},
		{"duckdb", "SELECT DATE_ADD(CAST('2020-01-01' AS DATE), INTERVAL 1 DAY)",
			"SELECT CAST('2020-01-01' AS DATE) + INTERVAL '1' DAY"},
		{"postgres", "SELECT date_add(current_date, interval '7' day)",
			"SELECT CURRENT_DATE + INTERVAL '7 DAY'"},
		// A bare number takes the unit the shift names, or days where it
		// named none.
		{"duckdb", "SELECT DATE_ADD(d, 3)", "SELECT d + INTERVAL 3 DAY"},
		// And a date written as TEXT is cast, because a string and an
		// interval do not add.
		{"duckdb", "SELECT DATE_ADD('2020-01-01', 5)",
			"SELECT CAST('2020-01-01' AS DATE) + INTERVAL 5 DAY"},
	} {
		e, err := ParseOne(tc.sql, tc.dialect)
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		if got, err := Generate(e, tc.dialect); err != nil || got != tc.want {
			t.Errorf("%q wrote %q (%v), want %q", tc.sql, got, err, tc.want)
		}
	}
	// A shift with nothing to shift by has no interval to write.
	if got, err := Generate(New("DateAdd",
		Arg{"this", New("Column", Arg{"this", New("Identifier", Arg{"this", "d"})})}),
		"duckdb"); err == nil {
		t.Errorf("wrote %q with nothing to shift by", got)
	}
}

// The storage format guards: a dialect that writes it nowhere, and a property
// naming no format at all.
func TestFileFormatGuards(t *testing.T) {
	format := New("FileFormatProperty", Arg{"this", New("Var", Arg{"this", "PARQUET"})})
	if got, err := Generate(format, "duckdb"); err != nil || got != "FORMAT=PARQUET" {
		t.Errorf("DuckDB wrote %q (%v)", got, err)
	}
	if got, err := Generate(New("FileFormatProperty"), "databricks"); err == nil {
		t.Errorf("wrote %q for a property naming no format", got)
	}
}

// T-SQL has no SPLIT_PART: it counts the pieces of a dotted name from the
// other end with PARSENAME, and writes NOTHING for a split it cannot count
// that way -- which is exactly what the reference does, unreadable output
// included. Everywhere else the call keeps its own spelling.
func TestSplitPartCountsBackwards(t *testing.T) {
	for _, c := range []struct {
		sql     string
		dialect string
		want    string
	}{
		{"SELECT SPLIT_PART('a.b.c', '.', 1)", "tsql", "SELECT PARSENAME('a.b.c', 3)"},
		{"SELECT SPLIT_PART('a.b.c', '.', 3)", "tsql", "SELECT PARSENAME('a.b.c', 1)"},
		{"SELECT SPLIT_PART('a.b.c', '.', 1)", "postgres", "SELECT SPLIT_PART('a.b.c', '.', 1)"},
		// A comma is not a name separator, the column is not a literal, and
		// five parts are more than PARSENAME counts. All three write nothing.
		{"SELECT SPLIT_PART('1,2,3', ',', 1)", "tsql", "SELECT"},
		{"SELECT SPLIT_PART(col, '.', 1)", "tsql", "SELECT"},
		{"SELECT SPLIT_PART('a.b.c.d.e', '.', 1)", "tsql", "SELECT"},
	} {
		tree, err := ParseOne(c.sql, c.dialect)
		if err != nil {
			t.Fatalf("%s: %v", c.sql, err)
		}
		got, err := Generate(tree, c.dialect)
		if err != nil || got != c.want {
			t.Errorf("[%s] %s wrote %q (%v), want %q",
				c.dialect, c.sql, got, err, c.want)
		}
	}
	// An index that is a literal but not a number is nothing to count to.
	odd := New("SplitPart",
		Arg{"this", New("Literal", Arg{"this", "a.b"}, Arg{"is_string", true})},
		Arg{"delimiter", New("Literal", Arg{"this", "."}, Arg{"is_string", true})},
		Arg{"part_index", New("Literal", Arg{"this", "x"}, Arg{"is_string", true})})
	if got, err := Generate(odd, "tsql"); err != nil || got != "" {
		t.Errorf("counted to a name: %q (%v)", got, err)
	}
}

// T-SQL has no CREATE TABLE ... AS SELECT and writes SELECT * INTO instead.
// The pieces it needs -- a query, and something to name -- have to be there.
func TestCreateAsSelectIntoGuards(t *testing.T) {
	if got, err := Generate(New("Create", Arg{"kind", "TABLE"},
		Arg{"expression", New("Select",
			Arg{"expressions", []*Expression{New("Star")}})}), "tsql"); err == nil {
		t.Errorf("wrote %q for a CREATE naming no table", got)
	}
	// A query already wrapped keeps its parentheses rather than gaining a
	// second pair.
	tree, err := ParseOne("CREATE TABLE t AS (SELECT 1 AS a)", "tsql")
	if err != nil {
		t.Fatal(err)
	}
	got, err := Generate(tree, "tsql")
	if err != nil || strings.Contains(got, "((") {
		t.Errorf("wrapped a wrapped query: %q (%v)", got, err)
	}
	// Nothing, and a Schema naming nothing, name no catalog.
	if namesACatalog(nil) || namesACatalog(New("Schema")) {
		t.Error("found a catalog where there is no table")
	}
}

// Two predicates the JSON writers lean on, exercised directly because the
// corpus reaches only the branches its own statements happen to take.
func TestJSONWriterPredicates(t *testing.T) {
	extract := New("JSONExtract",
		Arg{"this", New("Column",
			Arg{"this", New("Identifier", Arg{"this", "j"}, Arg{"quoted", false})})})
	for _, c := range []struct {
		name string
		node *Expression
		want bool
	}{
		{"nothing", nil, false},
		{"the extraction itself", extract, true},
		// Buried one level down, as a lone child and as one of a list.
		{"under a child", New("Paren", Arg{"this", extract}), true},
		{"in a list", New("Select",
			Arg{"expressions", []*Expression{New("Star"), extract}}), true},
		{"a list with none", New("Select",
			Arg{"expressions", []*Expression{New("Star")}}), false},
	} {
		if got := holdsAnExtraction(c.node); got != c.want {
			t.Errorf("%s: holdsAnExtraction = %v, want %v", c.name, got, c.want)
		}
	}

	cfg, ok := ConfigFor("postgres")
	if !ok {
		t.Fatal("no postgres generator")
	}
	g := &generator{cfg: cfg, tables: cfg.Tables, dialect: "postgres"}
	// An operator on the left of another operator has to be parenthesised to
	// keep its meaning; a plain column does not.
	column := New("Column",
		Arg{"this", New("Identifier", Arg{"this", "a"}, Arg{"quoted", false})})
	for _, c := range []struct {
		name string
		node *Expression
		want bool
	}{
		{"a column", column, true},
		{"a binary operator", New("Add", Arg{"this", column}, Arg{"expression", column}), false},
		{"a unary operator", New("Not", Arg{"this", column}), false},
	} {
		if got := isSafeLeadingOperand(g, c.node); got != c.want {
			t.Errorf("%s: isSafeLeadingOperand = %v, want %v", c.name, got, c.want)
		}
	}
}

// A builder's wrapper with an EXCEPTION, read at parse time and taken back off
// at write time. T-SQL's LEN counts a cast to TEXT -- unless what it counts is
// already a string -- and writes the call back without the cast either way.
func TestWrapperExceptions(t *testing.T) {
	for _, c := range []struct {
		sql     string
		dialect string
	}{
		{"LEN(x)", "tsql"}, {"LEN('x')", "tsql"}, {"LEN(1)", "tsql"},
		{"LEFT(x, 1)", "tsql"}, {"RIGHT(x, 1)", "tsql"},
		// A cast the argument ALREADY carries is not applied twice.
		{"FROM_UTC_TIMESTAMP(CAST(x AS TIMESTAMP), tz)", "databricks"},
		// And a cast that is never written around what it holds.
		{"1 / DIV(4, 2)", "postgres"},
	} {
		tree, err := ParseOne(c.sql, c.dialect)
		if err != nil {
			t.Errorf("[%s] %s: %v", c.dialect, c.sql, err)
			continue
		}
		got, err := Generate(tree, c.dialect)
		if err != nil || got != c.sql {
			t.Errorf("[%s] %s wrote %q (%v)", c.dialect, c.sql, got, err)
		}
	}

	// The tree the port builds is the reference's, cast and all -- what the
	// writer removes is not what the parser leaves out.
	tree, err := ParseOne("LEN(x)", "tsql")
	if err != nil {
		t.Fatal(err)
	}
	held, _ := tree.Args["this"].(*Expression)
	if held == nil || held.Class != "Cast" {
		t.Fatalf("LEN(x) built %v, want a cast inside", tree.Args["this"])
	}
	if held.Type == nil {
		t.Error("the cast the builder made carries no annotation")
	}

	// Nothing is a cast, and a cast names its target.
	if castTarget(New("Cast")) != "" || castsTo(nil) != "" || castsTo(New("Column")) != "" {
		t.Error("found a cast target where there is no cast")
	}
	if argumentKind(nil) != "" || argumentKind(New("Subquery")) != "subquery" ||
		argumentKind(New("Count")) != "call" {
		t.Error("named an argument kind wrongly")
	}
	// A wrapper over SEVERAL arguments has no one argument to excuse it.
	if _, ok := heldAlone([]FuncArg{{Index: 0}, {Index: 1}}); ok {
		t.Error("excused a wrapper holding two arguments")
	}
}

// A class chosen by the WORD in an argument. T-SQL has no digest classes of
// its own: it spells them all HASHBYTES, and the word in front says which.
func TestValueDispatch(t *testing.T) {
	for _, c := range []struct{ sql, class string }{
		{"HASHBYTES('SHA1', x)", "SHA"},
		{"HASHBYTES('MD5', 'x')", "MD5"},
		{"HASHBYTES('SHA2_256', x)", "SHA2"},
		{"HASHBYTES('SHA2_512', x)", "SHA2"},
		// A digest it does not know stays the plain call it was written as.
		{"HASHBYTES('MD2', 'x')", "Anonymous"},
		// And so does one whose first argument is not a word at all.
		{"HASHBYTES(kind, x)", "Anonymous"},
	} {
		tree, err := ParseOne(c.sql, "tsql")
		if err != nil {
			t.Errorf("%s: %v", c.sql, err)
			continue
		}
		if tree.Class != c.class {
			t.Errorf("%s built %s, want %s", c.sql, tree.Class, c.class)
		}
		got, err := Generate(tree, "tsql")
		if err != nil || got != c.sql {
			t.Errorf("%s wrote %q (%v)", c.sql, got, err)
		}
	}
	// Two words for one class, so the pair does not round-trip: SHA and SHA1
	// are both an SHA, and an SHA is written SHA1. That is the reference's
	// own normalisation, not a loss here.
	tree, err := ParseOne("HASHBYTES('SHA', x)", "tsql")
	if err != nil || tree.Class != "SHA" {
		t.Fatalf("HASHBYTES('SHA', x) built %v (%v)", tree, err)
	}
	if got, _ := Generate(tree, "tsql"); got != "HASHBYTES('SHA1', x)" {
		t.Errorf("SHA wrote %q", got)
	}
}

// One arity, two shapes, told apart by the KIND of the last argument.
// PostgreSQL reads REGEXP_REPLACE's trailing string as flags -- unless it
// spells an integer, when it is a position.
func TestArityKindSpecs(t *testing.T) {
	for _, c := range []struct{ sql, key string }{
		{"REGEXP_REPLACE(a, b, c, 'g')", "modifiers"},
		{"REGEXP_REPLACE(a, b, c, 1)", "position"},
		{"REGEXP_REPLACE(a, b, c, '1')", "position"},
		{"REGEXP_REPLACE(a, b, c, d, 'g')", "modifiers"},
	} {
		tree, err := ParseOne(c.sql, "postgres")
		if err != nil {
			t.Errorf("%s: %v", c.sql, err)
			continue
		}
		if _, ok := tree.Args[c.key]; !ok {
			t.Errorf("%s built %v, want a %s", c.sql, keysOf(tree), c.key)
		}
		got, err := Generate(tree, "postgres")
		if err != nil || got != c.sql {
			t.Errorf("%s wrote %q (%v)", c.sql, got, err)
		}
	}
	// A string that spells a number is its own kind, and only that one
	// builder tells it from any other string.
	if argumentKind(New("Literal", Arg{"this", "12"}, Arg{"is_string", true})) != "digits" {
		t.Error("a string of digits was not read as one")
	}
	if !isDigits("0") || isDigits("") || isDigits("1a") {
		t.Error("isDigits disagrees with itself")
	}
}

// The words between CREATE and what it creates: a bare property of the
// reference's own, a kind in its own right, or a flag that `OR` turns on.
// Which is which is the dialect's, and is read from its table.
func TestCreateWords(t *testing.T) {
	for _, c := range []struct{ sql, dialect string }{
		{"CREATE MATERIALIZED VIEW x.y.z AS SELECT a FROM b", ""},
		{"CREATE UNLOGGED TABLE foo AS SELECT 1", "postgres"},
		{"CREATE DATABASE x", ""},
		{"CREATE DATABASE IF NOT EXISTS y", ""},
		{"CREATE PROC foo AS SELECT 1", "tsql"},
		// Read from one word and written as another: T-SQL takes OR REPLACE
		// and OR ALTER alike, and writes OR ALTER.
		{"CREATE OR ALTER VIEW a.b AS SELECT 1", "tsql"},
	} {
		tree, err := ParseOne(c.sql, c.dialect)
		if err != nil {
			t.Errorf("[%s] %s: %v", c.dialect, c.sql, err)
			continue
		}
		got, err := Generate(tree, c.dialect)
		if err != nil || got != c.sql {
			t.Errorf("[%s] %s wrote %q (%v)", c.dialect, c.sql, got, err)
		}
	}
	// A word no dialect knows here is still refused.
	if _, err := ParseOne("CREATE ZZWAT TABLE t (a INT)", ""); err == nil {
		t.Error("a word nobody knows was read as a modifier")
	}
	// And a DATABASE with more than a name after it.
	if _, err := ParseOne("CREATE DATABASE x (a INT)", ""); err == nil {
		t.Error("a database with columns was read")
	}
}

// What an ALTER drops need not be a column: a CONSTRAINT is named the same
// way and a PARTITION is a slice of the rows rather than part of the shape.
func TestAlterDrop(t *testing.T) {
	for _, c := range []struct{ sql, dialect string }{
		{"ALTER TABLE orders DROP PARTITION(dt = '2014-05-14', country = 'IN')", ""},
		{"ALTER TABLE orders DROP IF EXISTS PARTITION(dt = '2014-05-14')", ""},
		{"ALTER TABLE orders DROP PARTITION(dt = '1'), PARTITION(dt = '2')", ""},
		{"ALTER TABLE dbo.DocExe DROP CONSTRAINT FK_Column_B", "tsql"},
		{"ALTER TABLE t DROP COLUMN a", ""},
		// A slice of the rows, which a TRUNCATE that lost it would not leave.
		{"TRUNCATE TABLE t1 PARTITION(age = 10, name = 'test', address)", "databricks"},
		{"TRUNCATE TABLE t1 PARTITION(age = 10, city LIKE 'LA')", "databricks"},
	} {
		tree, err := ParseOne(c.sql, c.dialect)
		if err != nil {
			t.Errorf("[%s] %s: %v", c.dialect, c.sql, err)
			continue
		}
		got, err := Generate(tree, c.dialect)
		if err != nil || got != c.sql {
			t.Errorf("[%s] %s wrote %q (%v)", c.dialect, c.sql, got, err)
		}
	}
	// A PARTITION naming nothing is not a partition.
	if _, err := ParseOne("ALTER TABLE t DROP PARTITION", ""); err == nil {
		t.Error("a partition with no values was read")
	}
}

// A COMMENT names what it is left on, and the note itself is a string in any
// spelling the tokenizer tells apart.
func TestCommentForms(t *testing.T) {
	for _, c := range []struct{ sql, want, dialect string }{
		{"COMMENT ON MATERIALIZED VIEW my_view IS 'this'", "", "postgres"},
		{"COMMENT ON TABLE my_schema.my_table IS N'National String'", "", ""},
		{"COMMENT ON PROCEDURE my_proc(integer, integer) IS 'Runs a report'", "", ""},
		// A dollar-quoted note is written back as an ordinary one, which is
		// what the reference writes too.
		{"COMMENT ON TABLE mytable IS $$doc this$$", "COMMENT ON TABLE mytable IS 'doc this'", "postgres"},
	} {
		want := c.want
		if want == "" {
			want = c.sql
		}
		tree, err := ParseOne(c.sql, c.dialect)
		if err != nil {
			t.Errorf("[%s] %s: %v", c.dialect, c.sql, err)
			continue
		}
		got, err := Generate(tree, c.dialect)
		if err != nil || got != want {
			t.Errorf("[%s] %s wrote %q (%v)", c.dialect, c.sql, got, err)
		}
	}
	// `IS NULL` takes the comment off, which the reference does not read.
	if _, err := ParseOne("COMMENT ON TABLE t IS NULL", ""); err == nil {
		t.Error("a comment of nothing was read")
	}
	if _, err := ParseOne("COMMENT ON MATERIALIZED", ""); err == nil {
		t.Error("MATERIALIZED naming no kind was read")
	}
}

// What a function may say about itself beyond its body, and the two shapes a
// WITH takes on a view: a list of settings, or one bare word.
func TestFunctionAndViewProperties(t *testing.T) {
	for _, c := range []struct{ sql, want, dialect string }{
		{"CREATE FUNCTION a() HANDLER 'handler_function'", "", "databricks"},
		{"CREATE FUNCTION a() PARAMETER STYLE PANDAS", "", "databricks"},
		{`CREATE FUNCTION a() ENVIRONMENT (dependencies = '["foo1==1"]')`, "", "databricks"},
		{"CREATE VIEW start WITH SCHEMABINDING AS SELECT a FROM x", "", "tsql"},
		{"CREATE VIEW v WITH ENCRYPTION AS SELECT a FROM x", "", "tsql"},
		// The reference does not finish reading ALTER VIEW WITH either, and
		// falls back to a Command the same way; matching that tree is the
		// only match, since a finished Alter would be a different one.
		{"ALTER VIEW v WITH SCHEMABINDING AS SELECT * FROM foo WHERE c > 10", "", "tsql"},
		{"ALTER VIEW v WITH ENCRYPTION AS SELECT * FROM foo WHERE c > 10", "", "tsql"},
		{"ALTER VIEW v WITH VIEW_METADATA AS SELECT * FROM foo WHERE c > 10", "", "tsql"},
		// A CTE need not write its AS; the reference reads it and puts the
		// word back.
		{"WITH x (select 1) SELECT * FROM x", "WITH x AS (SELECT 1) SELECT * FROM x", "databricks"},
	} {
		want := c.want
		if want == "" {
			want = c.sql
		}
		tree, err := ParseOne(c.sql, c.dialect)
		if err != nil {
			t.Errorf("[%s] %s: %v", c.dialect, c.sql, err)
			continue
		}
		got, err := Generate(tree, c.dialect)
		if err != nil || got != want {
			t.Errorf("[%s] %s wrote %q (%v)", c.dialect, c.sql, got, err)
		}
	}
	for _, c := range []struct{ sql, dialect string }{
		{"CREATE FUNCTION a() HANDLER", "databricks"},
		{"CREATE FUNCTION a() HANDLER x", "databricks"},
		{"CREATE FUNCTION a() PARAMETER STYLE", "databricks"},
		{"CREATE FUNCTION a() ENVIRONMENT x", "databricks"},
		{"CREATE VIEW v WITH AS SELECT 1", "tsql"},
	} {
		if _, err := ParseOne(c.sql, c.dialect); err == nil {
			t.Errorf("[%s] %s was read; it should be refused", c.dialect, c.sql)
		}
	}
}

// Three things said about a column that the port used to refuse: a rule about
// how NULLs compare, a T-SQL word about replication, and a type that names the
// values it may take rather than a size.
func TestMoreColumnConstraints(t *testing.T) {
	for _, c := range []struct{ sql, dialect string }{
		{"CREATE TABLE tbl (col INT UNIQUE NULLS NOT DISTINCT DEFAULT 9.99)", "postgres"},
		{"CREATE TABLE tbl (col INT UNIQUE)", "postgres"},
		{"CREATE TABLE x (a VARCHAR(5) NULL NOT FOR REPLICATION)", "tsql"},
		{"CREATE TABLE color (name ENUM('RED', 'GREEN', 'BLUE'))", "duckdb"},
	} {
		tree, err := ParseOne(c.sql, c.dialect)
		if err != nil {
			t.Errorf("[%s] %s: %v", c.dialect, c.sql, err)
			continue
		}
		got, err := Generate(tree, c.dialect)
		if err != nil || got != c.sql {
			t.Errorf("[%s] %s wrote %q (%v)", c.dialect, c.sql, got, err)
		}
	}
	// The words have to be all three, and an ENUM takes strings.
	for _, c := range []struct{ sql, dialect string }{
		{"CREATE TABLE t (a INT UNIQUE NULLS DISTINCT)", "postgres"},
		{"CREATE TABLE t (a INT NOT FOR)", "tsql"},
		{"CREATE TABLE t (a ENUM(1, 2))", "duckdb"},
	} {
		if _, err := ParseOne(c.sql, c.dialect); err == nil {
			t.Errorf("[%s] %s was read; it should be refused", c.dialect, c.sql)
		}
	}
}

// Everything that says HOW an index is built -- and an EXCLUDE constraint,
// which is an index by another name and reads the same parts.
func TestIndexParameters(t *testing.T) {
	for _, c := range []struct{ sql, dialect string }{
		{"CREATE INDEX et_vid_idx ON et(vid) INCLUDE (fid)", "postgres"},
		{"CREATE INDEX [x] ON [y]([z] ASC) WITH (allow_page_locks=on) ON PRIMARY", "tsql"},
		{"CREATE INDEX [x] ON [y]([z] ASC) WITH (allow_page_locks=on) ON X([y])", "tsql"},
		{"CREATE INDEX i ON t(a)", "postgres"},
		{"CREATE INDEX i ON t USING gin(a) WHERE b > 1", "postgres"},
		{"CREATE TABLE t (i INT, EXCLUDE USING gin(col1 WITH &&, col2 WITH ||) USING INDEX TABLESPACE tablespace WHERE (id > 5))", "postgres"},
		{"CREATE TABLE t (i INT, EXCLUDE USING btree(INT4RANGE(vid, nid, '[]') ASC NULLS FIRST WITH &&) INCLUDE (col1, col2))", "postgres"},
		// No method, so the columns follow the word with a space; with one
		// they follow the method with none.
		{"CREATE TABLE t (i INT, EXCLUDE (a WITH &&))", "postgres"},
	} {
		tree, err := ParseOne(c.sql, c.dialect)
		if err != nil {
			t.Errorf("[%s] %s: %v", c.dialect, c.sql, err)
			continue
		}
		got, err := Generate(tree, c.dialect)
		if err != nil || got != c.sql {
			t.Errorf("[%s] %s wrote %q (%v)", c.dialect, c.sql, got, err)
		}
	}
	for _, c := range []struct{ sql, dialect string }{
		{"CREATE INDEX i ON t(a) INCLUDE", "postgres"},
		{"CREATE INDEX i ON t(a) USING INDEX TABLESPACE", "postgres"},
		{"CREATE INDEX i ON t(a) ON", "tsql"},
		{"CREATE TABLE t (i INT, EXCLUDE USING gin(a WITH))", "postgres"},
	} {
		if _, err := ParseOne(c.sql, c.dialect); err == nil {
			t.Errorf("[%s] %s was read; it should be refused", c.dialect, c.sql)
		}
	}
}

// Three things a query may say that the port used to refuse: which rows a
// DISTINCT keeps, how a join matches its rows, and a frame bound that names
// only a distance.
func TestDistinctOnNaturalAndFrames(t *testing.T) {
	for _, sql := range []string{
		"SELECT DISTINCT ON (x) x, y FROM z",
		"SELECT DISTINCT ON (x, y + 1) * FROM z",
		"SELECT DISTINCT ON (x.y) * FROM z",
		"SELECT DISTINCT x FROM z",
		"SELECT 1 FROM a NATURAL JOIN b",
		"SELECT 1 FROM a NATURAL LEFT JOIN b",
		"SELECT 1 FROM a NATURAL LEFT OUTER JOIN b",
		// `BETWEEN 1 AND 3` names two distances and no direction, so the
		// bound has to be read below AND or the first would swallow the rest.
		"SELECT SUM(x) OVER (PARTITION BY a RANGE BETWEEN 1 AND 3)",
		"SELECT SUM(x) OVER (PARTITION BY a RANGE BETWEEN 1 FOLLOWING AND 3)",
		"SELECT SUM(x) OVER (PARTITION BY a ROWS BETWEEN UNBOUNDED PRECEDING AND PRECEDING)",
	} {
		tree, err := ParseOne(sql, "")
		if err != nil {
			t.Errorf("%s: %v", sql, err)
			continue
		}
		got, err := Generate(tree, "")
		if err != nil || got != sql {
			t.Errorf("%s wrote %q (%v)", sql, got, err)
		}
	}
	for _, sql := range []string{
		"SELECT DISTINCT ON x FROM z",
		"SELECT 1 FROM a NATURAL",
		// UNBOUNDED says a direction and nothing else, so it still needs one.
		"SELECT SUM(x) OVER (PARTITION BY a ROWS BETWEEN UNBOUNDED AND 1 FOLLOWING)",
	} {
		if _, err := ParseOne(sql, ""); err == nil {
			t.Errorf("%s was read; it should be refused", sql)
		}
	}
}

// A number without its leading zero, a rule named before its columns, a
// partition scheme named before the column it splits by, and two operator
// words used as call names.
func TestSmallSyntaxGaps(t *testing.T) {
	for _, c := range []struct{ sql, want, dialect string }{
		{"SELECT .5, 1.5", "SELECT 0.5, 1.5", ""},
		{`CREATE TABLE t (a INT, UNIQUE "key" (a))`, "", ""},
		{"CREATE TABLE tbl (col UUID, UNIQUE NULLS NOT DISTINCT (col))", "", "postgres"},
		{"ALTER TABLE s_ut ADD CONSTRAINT s_ut_uq UNIQUE hajo", "", ""},
		{"CREATE TABLE x (a INT) ON b (c)", "CREATE TABLE x (a INTEGER) ON b (c)", "tsql"},
		{"CREATE TABLE x (a INT) ON PRIMARY", "CREATE TABLE x (a INTEGER) ON PRIMARY", "tsql"},
		{"ILIKE(x, 'z')", "", ""},
		{"XOR(a, b)", "", "duckdb"},
		{"SELECT a ILIKE 'z'", "", "postgres"},
	} {
		want := c.want
		if want == "" {
			want = c.sql
		}
		tree, err := ParseOne(c.sql, c.dialect)
		if err != nil {
			t.Errorf("[%s] %s: %v", c.dialect, c.sql, err)
			continue
		}
		got, err := Generate(tree, c.dialect)
		if err != nil || got != want {
			t.Errorf("[%s] %s wrote %q (%v)", c.dialect, c.sql, got, err)
		}
	}
	// A dot with no number after it is not a number.
	if _, err := ParseOne("SELECT .", ""); err == nil {
		t.Error("a lone dot was read as a number")
	}
	// And a word that cannot name a call still does not.
	if _, err := ParseOne("SELECT AND(a, b)", ""); err == nil {
		t.Error("AND was read as a call")
	}
}

// What stands on the right of a JSON arrow: a whole number is a POSITION and
// is written bare, and anything else is a KEY and is quoted. A number that is
// not whole used to be carried through untouched, which wrote `0 -> 0.5` --
// a number again on the way back in, not the key it names. The generator
// fuzzer found it, once a leading-dot number could be read at all.
func TestJSONArrowKeys(t *testing.T) {
	for _, c := range []struct{ sql, want string }{
		{"0->5", "0 -> 5"},
		{"0->.5", "0 -> '0.5'"},
		{"0->1.5", "0 -> '1.5'"},
		{"0->1e5", "0 -> '1e5'"},
		{"0->-1", "0 -> -1"},
		{"0->'a'", "0 -> 'a'"},
	} {
		tree, err := ParseOne(c.sql, "postgres")
		if err != nil {
			t.Errorf("%s: %v", c.sql, err)
			continue
		}
		got, err := Generate(tree, "postgres")
		if err != nil || got != c.want {
			t.Errorf("%s wrote %q (%v), want %q", c.sql, got, err, c.want)
			continue
		}
		// And what it wrote reads back, which is the property that failed.
		if _, err := ParseOne(got, "postgres"); err != nil {
			t.Errorf("%s wrote %q, which cannot be read back: %v", c.sql, got, err)
		}
	}
	if _, err := ParseOne("SELECT x ->", "postgres"); err == nil {
		t.Error("`x ->` with nothing after it was read; it should be refused")
	}
}

// Four more: a collation on the type inside a CAST, a stream of a table, a
// TRIM that says which ends and nothing to trim from, and a hint.
func TestCollateStreamTrimAndHints(t *testing.T) {
	for _, c := range []struct{ sql, want, dialect string }{
		{`CAST('a' AS TEXT COLLATE "de_DE")`, "", "postgres"},
		{"SELECT CAST('a' AS VARCHAR COLLATE foo)", "", "postgres"},
		{"SELECT CAST('a' AS STRING COLLATE UTF8_BINARY)", "", "databricks"},
		{"SELECT * FROM STREAM t", "", "databricks"},
		// A bare `stream` is a table called stream, which is how the
		// reference reads it too.
		{"SELECT * FROM stream", "", "databricks"},
		// The reference keeps the position and writes it nowhere.
		{"SELECT TRIM(BOTH ' XXX ')", "SELECT TRIM(' XXX ')", "postgres"},
		{"SELECT TRIM(BOTH 'x' FROM y)", "", "postgres"},
		{"SELECT /*+ SOME_HINT(foo) */ 1", "", ""},
		{"SELECT /*+ REBALANCE */ * FROM foo", "", ""},
		{"SELECT /*+ HINT(t) */ 1 FROM t", "", ""},
	} {
		want := c.want
		if want == "" {
			want = c.sql
		}
		tree, err := ParseOne(c.sql, c.dialect)
		if err != nil {
			t.Errorf("[%s] %s: %v", c.dialect, c.sql, err)
			continue
		}
		got, err := Generate(tree, c.dialect)
		if err != nil || got != want {
			t.Errorf("[%s] %s wrote %q (%v)", c.dialect, c.sql, got, err)
		}
	}
	for _, c := range []struct{ sql, dialect string }{
		{"SELECT /*+ */ 1", ""},
		{"SELECT /*+ a b */ 1", ""},
		{"CAST('a' AS TEXT COLLATE)", "postgres"},
	} {
		if _, err := ParseOne(c.sql, c.dialect); err == nil {
			t.Errorf("[%s] %s was read; it should be refused", c.dialect, c.sql)
		}
	}
}

// The kinds a CREATE may name that carry nothing but a name.
func TestCreateNamespace(t *testing.T) {
	for _, c := range []struct{ sql, want, dialect string }{
		{"CREATE NAMESPACE x", "", ""},
		{"CREATE DATABASE IF NOT EXISTS y", "", ""},
		{"CREATE OR REFRESH STREAMING TABLE t AS SELECT 1", "", "databricks"},
	} {
		want := c.want
		if want == "" {
			want = c.sql
		}
		tree, err := ParseOne(c.sql, c.dialect)
		if err != nil {
			t.Errorf("[%s] %s: %v", c.dialect, c.sql, err)
			continue
		}
		got, err := Generate(tree, c.dialect)
		if err != nil || got != want {
			t.Errorf("[%s] %s wrote %q (%v)", c.dialect, c.sql, got, err)
		}
	}
	// REFRESH is Databricks' word alone; elsewhere the OR is left standing.
	if _, err := ParseOne("CREATE OR REFRESH VIEW v AS SELECT 1", "postgres"); err == nil {
		t.Error("OR REFRESH was read in a dialect that has no such word")
	}
}

// Four shapes that are the same text meaning different things: an equals in a
// projection, a hash before a number, strings written next to each other, and
// a star after a table name.
func TestDialectShapedSyntax(t *testing.T) {
	for _, c := range []struct{ sql, want, dialect string }{
		// T-SQL names the column with the LEFT-hand side.
		{"SELECT a = 1", "SELECT 1 AS a", "tsql"},
		{"SELECT a = 1 UNION ALL SELECT a = b", "SELECT 1 AS a UNION ALL SELECT b AS a", "tsql"},
		// Only where the LEFT is a bare name: a chain of equals is a chain of
		// comparisons in the reference too. The generator fuzzer found this.
		{"SELECT A = A = 0", "", "tsql"},
		{"SELECT A = 0 - A = L", "", "tsql"},
		// Everywhere else the same text is a comparison.
		{"SELECT a = 1", "", "postgres"},
		// DuckDB reads `#2` as the second output column.
		{"SELECT #2, #1 FROM (VALUES (1, 'foo'))", "", "duckdb"},
		{"SELECT #2 AS a, #1 AS b FROM (VALUES (1, 'foo'))", "", "duckdb"},
		// Strings written next to each other are one string.
		{"'x' 'y' 'z'", "CONCAT('x', 'y', 'z')", "postgres"},
		{"SELECT 'x'", "", "postgres"},
		// The star says "and everything that inherits from it", which is the
		// default -- so it is read and written nowhere.
		{"SELECT * FROM t1*", "SELECT * FROM t1", "postgres"},
		// T-SQL skips the schema by writing nothing between two dots: the
		// db comes back as the EMPTY STRING, not absent the way a plain
		// `a.b` leaves it, so the dot it named is still there on the way out.
		{"SELECT * FROM a..b", "", "tsql"},
		// `AS (a, b)` names MULTIPLE columns at once: POSEXPLODE returns
		// more than one, and the reference builds an Aliases node rather
		// than a plain Alias for any expression at all.
		{`SELECT POSEXPLODE("x") AS ("a", "b")`, "SELECT POSEXPLODE('x') AS (`a`, `b`)", "databricks"},
		{`SELECT POSEXPLODE("x") AS ("a", "b", "c")`, "SELECT POSEXPLODE('x') AS (`a`, `b`, `c`)", "databricks"},
		{"SELECT f(x) AS (a, b)", "SELECT F(x) AS (a, b)", "databricks"},
		// A notebook WIDGET, `{name}`, is a Placeholder wherever a value or
		// a table goes -- the one dialect where a brace opens this instead
		// of a struct literal.
		{"SELECT {df}", "", "databricks"},
		{"SELECT * FROM {df}", "", "databricks"},
		{"SELECT * FROM {df} WHERE id > :foo", "", "databricks"},
		{"SELECT {'a': 1}", "SELECT STRUCT(1 AS a)", "databricks"},
		// The WHERE is not always written: `SUM(x) FILTER (x = 1)` names
		// the condition straight after the parenthesis, and the reference
		// still wraps it in a Where.
		{"SELECT SUM(x) FILTER (x = 1)", "SELECT SUM(x) FILTER(WHERE x = 1)", ""},
		// A bare field with no name of its own is written positionally only
		// where the whole struct is spelled `{'k': v}` -- DuckDB's own
		// template. Elsewhere it is `STRUCT(v AS k, ...)`, and a field with
		// no name stays bare rather than being given one it never had.
		{"SELECT STRUCT(x, x AS y)", "", ""},
		{"SELECT ARRAY_AGG(STRUCT(x, x AS y) ORDER BY z DESC) AS x", "", ""},
	} {
		want := c.want
		if want == "" {
			want = c.sql
		}
		tree, err := ParseOne(c.sql, c.dialect)
		if err != nil {
			t.Errorf("[%s] %s: %v", c.dialect, c.sql, err)
			continue
		}
		got, err := Generate(tree, c.dialect)
		if err != nil || got != want {
			t.Errorf("[%s] %s wrote %q (%v)", c.dialect, c.sql, got, err)
		}
	}
	// A hash with no number after it names no position.
	if _, err := ParseOne("SELECT #a FROM t", "duckdb"); err == nil {
		t.Error("a hash naming no position was read")
	}
}

// What may follow a relation: a keyword that names it, a sample of it, and
// two ways of asking for the state it was in at some point.
func TestTableClauses(t *testing.T) {
	for _, c := range []struct{ sql, want, dialect string }{
		// More keywords may name a SUBQUERY than may name a column.
		{"SELECT * FROM (SELECT 1 AS col) apply", "SELECT * FROM (SELECT 1 AS col) AS apply", "postgres"},
		{"SELECT * FROM (SELECT 1 AS col) apply", "SELECT * FROM (SELECT 1 AS col) AS apply", "duckdb"},
		// A subquery may be sampled like any other relation.
		{"SELECT * FROM (SELECT * FROM t) AS t1 TABLESAMPLE RESERVOIR (1 ROWS)", "", "duckdb"},
		// The state a table was in, before the alias and after it.
		{"SELECT * FROM foo TIMESTAMP AS OF '2020-01-01' AS bar", "", "databricks"},
		{"SELECT * FROM t1 AS a AT (VERSION => 3)", "", "duckdb"},
		{"SELECT * FROM t1 AS a AT (TIMESTAMP => CAST('2024-01-01' AS TIMESTAMP))", "", "duckdb"},
		// A word that opens a clause here still opens one.
		{"SELECT a FROM (SELECT a, b FROM test) PIVOT(SUM(x) FOR y IN ('z'))", "", "duckdb"},
	} {
		want := c.want
		if want == "" {
			want = c.sql
		}
		tree, err := ParseOne(c.sql, c.dialect)
		if err != nil {
			t.Errorf("[%s] %s: %v", c.dialect, c.sql, err)
			continue
		}
		got, err := Generate(tree, c.dialect)
		if err != nil || got != want {
			t.Errorf("[%s] %s wrote %q (%v)", c.dialect, c.sql, got, err)
		}
	}
	for _, c := range []struct{ sql, dialect string }{
		{"SELECT * FROM t AT (VERSION 3)", "duckdb"},
		{"SELECT * FROM t AT (VERSION =>)", "duckdb"},
		{"SELECT * FROM t AT (VERSION => 3", "duckdb"},
	} {
		if _, err := ParseOne(c.sql, c.dialect); err == nil {
			t.Errorf("[%s] %s was read; it should be refused", c.dialect, c.sql)
		}
	}
}

// Which rows near the current one a frame leaves out, and ANY wherever it
// stands rather than only after a comparison.
func TestFrameExclusionAndAny(t *testing.T) {
	for _, c := range []struct{ sql, want, dialect string }{
		{"SELECT SUM(X) OVER (PARTITION BY x RANGE BETWEEN 1 PRECEDING AND CURRENT ROW EXCLUDE NO OTHERS)", "", "postgres"},
		{"SELECT SUM(X) OVER (ROWS BETWEEN 1 PRECEDING AND CURRENT ROW EXCLUDE TIES)", "", "postgres"},
		{"SELECT SUM(X) OVER (ROWS BETWEEN 1 PRECEDING AND CURRENT ROW EXCLUDE GROUP)", "", "postgres"},
		{"SELECT SUM(X) OVER (ROWS BETWEEN 1 PRECEDING AND CURRENT ROW EXCLUDE CURRENT ROW)", "", "postgres"},
		{"SELECT SUM(X) OVER (ROWS BETWEEN 1 PRECEDING AND CURRENT ROW)", "", "postgres"},
		// GROUPS, unlike ROWS and RANGE, is not a keyword of its own -- it
		// arrives as a plain word, matched by text the same way frameKind
		// reads it.
		{"SELECT SUM(X) OVER (ORDER BY y GROUPS BETWEEN 1 PRECEDING AND CURRENT ROW)", "", "duckdb"},
		// ANY quantifies whatever follows it, parenthesised or not.
		{"ANY(x) OVER (PARTITION BY x)", "", ""},
		{"SELECT * FROM x WHERE name LIKE ANY XXX('a', 'b')", "", ""},
		{"SELECT * FROM x WHERE name ILIKE ANY XXX('a', 'b')", "", ""},
		{"any(ARRAY[1]) <> x", "ANY(ARRAY[1]) <> x", "postgres"},
		{"SELECT * FROM x WHERE a = ANY (SELECT 1)", "", ""},
		{"SELECT ANY(col) FROM VALUES (TRUE), (FALSE) AS tab(col)", "", "databricks"},
	} {
		want := c.want
		if want == "" {
			want = c.sql
		}
		tree, err := ParseOne(c.sql, c.dialect)
		if err != nil {
			t.Errorf("[%s] %s: %v", c.dialect, c.sql, err)
			continue
		}
		got, err := Generate(tree, c.dialect)
		if err != nil || got != want {
			t.Errorf("[%s] %s wrote %q (%v)", c.dialect, c.sql, got, err)
		}
	}
	for _, c := range []struct{ sql, dialect string }{
		{"SELECT SUM(x) OVER (ROWS BETWEEN 1 PRECEDING AND CURRENT ROW EXCLUDE)", "postgres"},
		{"SELECT SUM(x) OVER (ROWS BETWEEN 1 PRECEDING AND CURRENT ROW EXCLUDE WAT)", "postgres"},
		// ANY with nothing to quantify is an error in the reference too.
		{"SELECT ANY", ""},
	} {
		if _, err := ParseOne(c.sql, c.dialect); err == nil {
			t.Errorf("[%s] %s was read; it should be refused", c.dialect, c.sql)
		}
	}
}

// What a key may carry beyond its columns: an index's own vocabulary, a
// member that holds the time a row belongs to, and how the index behind it is
// built -- which the reference records as a second constraint, not as
// anything on the key.
func TestKeyConstraints(t *testing.T) {
	for _, c := range []struct{ sql, want, dialect string }{
		{"CREATE TABLE t (i INT, a TEXT, PRIMARY KEY (i) INCLUDE (a))", "", "postgres"},
		{"CREATE TABLE t (a INT, b TIMESTAMP, PRIMARY KEY (a, b TIMESERIES))", "", "databricks"},
		{"CREATE TABLE t (a INT, PRIMARY KEY (a))", "", "postgres"},
		{"CREATE TABLE t (a INT, CONSTRAINT c PRIMARY KEY NONCLUSTERED (a DESC))",
			"CREATE TABLE t (a INTEGER, CONSTRAINT c PRIMARY KEY NONCLUSTERED (a DESC))", "tsql"},
		// One ADD may name several, and the comma is part of the ADD.
		{"ALTER TABLE tbl ADD CONSTRAINT cnstr PRIMARY KEY CLUSTERED (ID), CONSTRAINT cnstr2 UNIQUE CLUSTERED (ID)", "", "tsql"},
		{"ALTER TABLE tbl ADD CONSTRAINT a PRIMARY KEY (x)", "", "tsql"},
		// T-SQL orders a key's members, so a TIMESERIES one is read on that
		// path as well as on the plain one.
		{"CREATE TABLE t (a INT, b DATETIME2, PRIMARY KEY (a, b TIMESERIES))",
			"CREATE TABLE t (a INTEGER, b DATETIME2, PRIMARY KEY (a, b TIMESERIES))", "tsql"},
		// And the same words on a COLUMN rather than in a named constraint.
		{"CREATE TABLE t (a INT PRIMARY KEY CLUSTERED (a))",
			"CREATE TABLE t (a INTEGER PRIMARY KEY CLUSTERED (a))", "tsql"},
	} {
		want := c.want
		if want == "" {
			want = c.sql
		}
		tree, err := ParseOne(c.sql, c.dialect)
		if err != nil {
			t.Errorf("[%s] %s: %v", c.dialect, c.sql, err)
			continue
		}
		got, err := Generate(tree, c.dialect)
		if err != nil || got != want {
			t.Errorf("[%s] %s wrote %q (%v)", c.dialect, c.sql, got, err)
		}
	}
	for _, c := range []struct{ sql, dialect string }{
		{"CREATE TABLE t (a INT, CONSTRAINT c PRIMARY KEY CLUSTERED)", "tsql"},
		{"CREATE TABLE t (a INT, UNIQUE CLUSTERED)", "tsql"},
		{"CREATE TABLE t (a INT, PRIMARY KEY (a", "postgres"},
		{"CREATE TABLE t (a INT, PRIMARY KEY a)", "postgres"},
		{"CREATE TABLE t (a INT, PRIMARY KEY a)", "tsql"},
		{"CREATE TABLE t (a INT, PRIMARY KEY (a", "tsql"},
		{"CREATE TABLE t (a INT, CONSTRAINT c PRIMARY KEY CLUSTERED a)", "tsql"},
	} {
		if _, err := ParseOne(c.sql, c.dialect); err == nil {
			t.Errorf("[%s] %s was read; it should be refused", c.dialect, c.sql)
		}
	}
}

// Three ALTER actions, and an INSERT whose target is FILES rather than a
// table.
func TestAlterActionsAndDirectories(t *testing.T) {
	for _, c := range []struct{ sql, dialect string }{
		{"ALTER TABLE mydataset.mytable DELETE WHERE x = 1", ""},
		{"ALTER TABLE t CLUSTER BY NONE", "databricks"},
		{"ALTER TABLE t1 WITH CHECK ADD CONSTRAINT ctr FOREIGN KEY (c1) REFERENCES t2 (c2)", "tsql"},
		{"ALTER TABLE t1 WITH NOCHECK ADD CONSTRAINT c FOREIGN KEY (a) REFERENCES u (b)", "tsql"},
		{"ALTER TABLE t ADD COLUMN a INT", "postgres"},
		{"INSERT OVERWRITE DIRECTORY 'x' SELECT 1", ""},
		{"INSERT OVERWRITE LOCAL DIRECTORY 'x' SELECT 1", ""},
		{"INSERT OVERWRITE LOCAL DIRECTORY 'x' ROW FORMAT DELIMITED FIELDS TERMINATED BY '1' COLLECTION ITEMS TERMINATED BY '2' MAP KEYS TERMINATED BY '3' LINES TERMINATED BY '4' NULL DEFINED AS '5' SELECT 1", ""},
		{"INSERT INTO t SELECT 1", ""},
	} {
		tree, err := ParseOne(c.sql, c.dialect)
		if err != nil {
			t.Errorf("[%s] %s: %v", c.dialect, c.sql, err)
			continue
		}
		got, err := Generate(tree, c.dialect)
		if err != nil || got != c.sql {
			t.Errorf("[%s] %s wrote %q (%v)", c.dialect, c.sql, got, err)
		}
	}
	for _, c := range []struct{ sql, dialect string }{
		{"ALTER TABLE t DELETE", ""},
		{"ALTER TABLE t CLUSTER BY a", "databricks"},
		{"ALTER TABLE t CLUSTER BY", "databricks"},
		{"INSERT OVERWRITE DIRECTORY SELECT 1", ""},
		{"INSERT OVERWRITE DIRECTORY 'x' ROW FORMAT DELIMITED FIELDS TERMINATED BY SELECT 1", ""},
	} {
		if _, err := ParseOne(c.sql, c.dialect); err == nil {
			t.Errorf("[%s] %s was read; it should be refused", c.dialect, c.sql)
		}
	}
}

// What a CREATE TABLE may carry beyond its columns: settings between the
// columns and the query, two words whose branches the property table cannot
// describe, and a query that is a set operation.
func TestCreateTableTails(t *testing.T) {
	for _, sql := range []string{
		"CREATE TABLE z (z INT) WITH (PARTITIONED_BY=(x INT)) AS SELECT 1",
		"CREATE TABLE a (b INT) ON COMMIT PRESERVE ROWS",
		"CREATE TABLE a (b INT) ON COMMIT DELETE ROWS",
		"CREATE TABLE a.b AS (SELECT 1) NO PRIMARY INDEX",
		"CREATE TABLE foo AS (SELECT 1) UNION ALL (SELECT 2)",
		"CREATE TABLE foo AS (SELECT 1)",
	} {
		tree, err := ParseOne(sql, "")
		if err != nil {
			t.Errorf("%s: %v", sql, err)
			continue
		}
		got, err := Generate(tree, "")
		if err != nil || got != sql {
			t.Errorf("%s wrote %q (%v)", sql, got, err)
		}
	}
	// The word ON still names a filegroup on its other branch, where the
	// dialect writes one.
	if tree, err := ParseOne("CREATE TABLE x (a INTEGER) ON b (c)", "tsql"); err != nil {
		t.Errorf("a filegroup was refused: %v", err)
	} else if got, _ := Generate(tree, "tsql"); got != "CREATE TABLE x (a INTEGER) ON b (c)" {
		t.Errorf("a filegroup wrote %q", got)
	}
}

// An ORDER BY with nothing in front of it IS the argument, where one that
// follows an argument orders that argument instead. The reference writes the
// first with the leading space its own clause carries.
func TestOrderAsACallArgument(t *testing.T) {
	for _, c := range []struct{ sql, want string }{
		{"SELECT RANK( ORDER BY foo) OVER (ORDER BY 1) FROM (SELECT 1 AS foo)", ""},
		{"SELECT CUME_DIST( ORDER BY foo) OVER (ORDER BY 1) FROM (SELECT 1 AS foo)", ""},
		{"SELECT PERCENT_RANK( ORDER BY foo) OVER (ORDER BY 1) FROM (SELECT 1 AS foo)", ""},
		{"SELECT ARRAY_AGG(ORDER BY b, x) FROM t", "SELECT ARRAY_AGG( ORDER BY b, x) FROM t"},
		// One that FOLLOWS an argument orders it, and takes no space.
		{"SELECT ARRAY_AGG(x ORDER BY y) FROM t", ""},
		// And a query's own ORDER BY is not in a call at all.
		{"SELECT x FROM t ORDER BY y", ""},
		// An ordering that IS the argument and is followed by nothing.
		{"SELECT ARRAY_AGG( ORDER BY b) FROM t", ""},
	} {
		want := c.want
		if want == "" {
			want = c.sql
		}
		tree, err := ParseOne(c.sql, "duckdb")
		if err != nil {
			t.Errorf("%s: %v", c.sql, err)
			continue
		}
		got, err := Generate(tree, "duckdb")
		if err != nil || got != want {
			t.Errorf("%s wrote %q (%v)", c.sql, got, err)
		}
	}
}

// Six more things said about a column, and a set operation whose right side
// is parenthesised -- where a trailing ORDER BY belongs to the operation
// rather than to the query inside the parentheses.
func TestTeradataConstraintsAndSetOps(t *testing.T) {
	for _, sql := range []string{
		"CREATE TABLE foo (baz CHAR(4) CHARACTER SET LATIN UPPERCASE NOT CASESPECIFIC COMPRESS 'a')",
		"CREATE TABLE db.foo (id INT NOT NULL, valid_date DATE FORMAT 'YYYY-MM-DD', measurement INT COMPRESS)",
		"CREATE TABLE foo (baz DATE FORMAT 'YYYY/MM/DD' TITLE 'title' INLINE LENGTH 1 COMPRESS ('a', 'b'))",
		"CREATE TABLE t (a INT CASESPECIFIC)",
		"CREATE TABLE t (a INT COMPRESS 5)",
		// A bare word rather than a quoted one, which the reference takes in
		// these slots too.
		"CREATE TABLE t (a DATE FORMAT YYYY)",
		"CREATE TABLE t (a INT TITLE why)",
		"SELECT ((SELECT 0) UNION (SELECT 1) ORDER BY 1)",
		"SELECT ((SELECT 0) UNION (SELECT 1))",
		"(SELECT 0) UNION (SELECT 1) ORDER BY 1",
	} {
		tree, err := ParseOne(sql, "")
		if err != nil {
			t.Errorf("%s: %v", sql, err)
			continue
		}
		got, err := Generate(tree, "")
		if err != nil || got != sql {
			t.Errorf("%s wrote %q (%v)", sql, got, err)
		}
	}
	for _, sql := range []string{
		"CREATE TABLE t (a INT INLINE LENGTH)",
		"CREATE TABLE t (a INT INLINE LENGTH x)",
		"CREATE TABLE t (a INT CHARACTER SET)",
		"CREATE TABLE t (a INT FORMAT)",
		"CREATE TABLE t (a INT COMPRESS (",
		"CREATE TABLE t (a INT COMPRESS",
		"CREATE TABLE t (a INT FORMAT",
		"(SELECT 0) UNION (SELECT 1) ORDER BY",
	} {
		if _, err := ParseOne(sql, ""); err == nil {
			t.Errorf("%s was read; it should be refused", sql)
		}
	}
}

// TestBetweenSymmetric covers SYMMETRIC and ASYMMETRIC, read by TEXT in every
// dialect regardless of whether that dialect can write the word back:
// PostgreSQL writes it as it stands, and elsewhere ASYMMETRIC -- the
// default meaning either way -- is simply dropped. A TRUE SYMMETRIC
// elsewhere would need expanding into two BETWEENs joined by OR, which is
// not yet ported, so it stays refused there instead.
func TestBetweenSymmetric(t *testing.T) {
	for _, c := range []struct{ sql, want, dialect string }{
		{"SELECT x BETWEEN ASYMMETRIC 10 AND 2", "", "postgres"},
		{"SELECT x BETWEEN SYMMETRIC 10 AND 2", "", "postgres"},
		{"SELECT x BETWEEN 1 AND 2", "", "postgres"},
		{"SELECT x BETWEEN ASYMMETRIC 10 AND 2", "SELECT x BETWEEN 10 AND 2", "duckdb"},
	} {
		want := c.want
		if want == "" {
			want = c.sql
		}
		tree, err := ParseOne(c.sql, c.dialect)
		if err != nil {
			t.Errorf("[%s] %s: %v", c.dialect, c.sql, err)
			continue
		}
		if got, err := Generate(tree, c.dialect); err != nil || got != want {
			t.Errorf("[%s] %s wrote %q (%v)", c.dialect, c.sql, got, err)
		}
	}
	// A word read as a name, not a keyword, where it is quoted -- and a TRUE
	// SYMMETRIC where the dialect cannot write it back, needing the OR
	// expansion this port does not have.
	for _, c := range []struct{ sql, dialect string }{
		{`SELECT a BETWEEN "symmetric" AND b`, "duckdb"},
		{"SELECT x BETWEEN SYMMETRIC 10 AND 2", "duckdb"},
	} {
		if _, err := ParseOne(c.sql, c.dialect); err != nil {
			t.Errorf("[%s] %s was refused: %v", c.dialect, c.sql, err)
		}
	}
	if _, err := Generate(ParseOrFail(t, "SELECT x BETWEEN SYMMETRIC 10 AND 2", "duckdb"), "duckdb"); err == nil {
		t.Error("a TRUE SYMMETRIC was written where the dialect cannot write the word back")
	}
}

// Three shapes a range operator, a join and an IN may take beyond the plain
// one: operands the other way round and held as a list of one, a join matched
// by position, and an IN over a column that holds the list.
func TestRangeOpsJoinsAndIn(t *testing.T) {
	for _, c := range []struct{ sql, want, dialect string }{
		{"x @@ y", "", "postgres"},
		{"(col1 @@ 'abc' OR col2 @@ 'abc' OR col3 @@ 'abc')", "", "postgres"},
		{"x @> y", "", "postgres"},
		{"SELECT df1.*, df2.* FROM df1 POSITIONAL JOIN df2", "", "duckdb"},
		{"SELECT * FROM foo ASOF LEFT JOIN bar ON a = b", "", "duckdb"},
		{"'red' IN flags", "", "duckdb"},
		{"'red' IN tbl.flags", "", "duckdb"},
		{"'red' IN (1, 2)", "", "duckdb"},
		// DuckDB's `~` is a single CHARACTER, read through the tokenizer's
		// SINGLE_TOKENS table rather than a multi-character keyword -- which
		// is why the probe that found every other range operator missed it.
		{"SELECT x ~ y", "SELECT REGEXP_FULL_MATCH(x, y)", "duckdb"},
		{"SELECT x !~ y", "SELECT NOT REGEXP_FULL_MATCH(x, y)", "duckdb"},
		// The very same character is a UNARY operator elsewhere -- bitwise
		// NOT -- and stays one; only the binary, infix spelling is DuckDB's.
		{"SELECT ~x", "", "duckdb"},
	} {
		want := c.want
		if want == "" {
			want = c.sql
		}
		tree, err := ParseOne(c.sql, c.dialect)
		if err != nil {
			t.Errorf("[%s] %s: %v", c.dialect, c.sql, err)
			continue
		}
		got, err := Generate(tree, c.dialect)
		if err != nil || got != want {
			t.Errorf("[%s] %s wrote %q (%v)", c.dialect, c.sql, got, err)
		}
	}
	// An IN over something that is not a name is refused.
	for _, c := range []struct{ sql, dialect string }{
		{"a IN 1", ""},
	} {
		if _, err := ParseOne(c.sql, c.dialect); err == nil {
			t.Errorf("[%s] %s was read; it should be refused", c.dialect, c.sql)
		}
	}
}

// A DROP named by its signature, a SELECT INTO that names the kind of table it
// writes, and a COMMIT that says whether it waits for the log.
func TestDropSignatureIntoKindAndDurability(t *testing.T) {
	for _, c := range []struct{ sql, dialect string }{
		{"DROP FUNCTION a.b.c (INT)", ""},
		{"DROP PROCEDURE a.b.c (INT)", ""},
		{"DROP TABLE t", ""},
		{"WITH t(c) AS (SELECT 1) SELECT c INTO TEMPORARY foo FROM t", "postgres"},
		{"SELECT c INTO foo FROM t", "postgres"},
		{"SELECT c INTO UNLOGGED foo FROM t", "postgres"},
		// T-SQL says the kind in the NAME, and the name writes it itself.
		{"WITH t(c) AS (SELECT 1) SELECT c INTO #foo FROM t", "tsql"},
		{"COMMIT TRANSACTION transaction_name WITH (DELAYED_DURABILITY = OFF)", "tsql"},
		{"COMMIT TRANSACTION @tran_name_variable WITH (DELAYED_DURABILITY = ON)", "tsql"},
		// A signature naming several types.
		{"DROP FUNCTION a (INT, TEXT)", ""},
		{"DROP FUNCTION a (some_udt)", ""},
	} {
		tree, err := ParseOne(c.sql, c.dialect)
		if err != nil {
			t.Errorf("[%s] %s: %v", c.dialect, c.sql, err)
			continue
		}
		got, err := Generate(tree, c.dialect)
		if err != nil || got != c.sql {
			t.Errorf("[%s] %s wrote %q (%v)", c.dialect, c.sql, got, err)
		}
	}
	for _, c := range []struct{ sql, dialect string }{
		{"DROP FUNCTION a (INT", ""},
		{"SELECT * INTO TEMPORARY FROM t1", "postgres"},
		{"COMMIT TRANSACTION a WITH", "tsql"},
		{"COMMIT TRANSACTION a WITH (SOMETHING = ON)", "tsql"},
		{"COMMIT TRANSACTION a WITH (DELAYED_DURABILITY = MAYBE)", "tsql"},
		{"COMMIT TRANSACTION a WITH (DELAYED_DURABILITY = ON", "tsql"},
		{"COMMIT TRANSACTION a WITH (DELAYED_DURABILITY)", "tsql"},
		// A durability with no name in front of it, which the reference
		// refuses too.
		{"COMMIT WITH (DELAYED_DURABILITY = ON)", "tsql"},
	} {
		if _, err := ParseOne(c.sql, c.dialect); err == nil {
			t.Errorf("[%s] %s was read; it should be refused", c.dialect, c.sql)
		}
	}
}

// A word the dialect has no type for is a USER-DEFINED one named by itself,
// which is how a schema's own types reach a cast. A handful of words are read
// specially and are refused rather than guessed at.
func TestUserDefinedTypes(t *testing.T) {
	for _, c := range []struct{ sql, dialect string }{
		{"CAST(x AS some_udt)", "postgres"},
		{"CAST(x AS some_udt)", "tsql"},
		{"CAST(x AS FOO)", "duckdb"},

		{"CREATE TABLE t (a wat)", ""},
		{"CAST(x AS INT)", ""},
		// A typeless column with a default is not a column of type DEFAULT.
		{"CREATE TABLE t (a DEFAULT 0)", ""},
		{"CAST(a AS wat)", ""},
		// A user-defined type in a T-SQL declaration, and one named with
		// brackets rather than a bare word.
		{"DECLARE @X UserDefinedTableType", "tsql"},
		{`CAST(x AS "quoted udt")`, "postgres"},
		// A user-defined type may be SCHEMA-QUALIFIED here too, kept as a
		// chain of DOTS over identifiers rather than joined into one string.
		{"CAST(x AS sch.udt)", "postgres"},
		{"CAST(x AS a.b.c)", "postgres"},

		// T-SQL's brackets are a name's, and the reference drops them here.
	} {
		tree, err := ParseOne(c.sql, c.dialect)
		if err != nil {
			t.Errorf("[%s] %s: %v", c.dialect, c.sql, err)
			continue
		}
		got, err := Generate(tree, c.dialect)
		if err != nil || got != c.sql {
			t.Errorf("[%s] %s wrote %q (%v)", c.dialect, c.sql, got, err)
		}
	}
	// A keyword or a number where a type goes is neither a type nor a name.
	for _, c := range []struct{ sql, dialect string }{
		{"SELECT CAST(x AS SELECT)", "tsql"},
		{"SELECT CAST(x AS 3)", "tsql"},
	} {
		if _, err := ParseOne(c.sql, c.dialect); err == nil {
			t.Errorf("[%s] %s was read as a name; it should be refused", c.dialect, c.sql)
		}
	}
	// A cast written with the short spelling comes back the long way, which
	// is what the reference writes too.
	// A quoted name of several WORDS is still a name, and DuckDB writes it
	// back without the quotes -- as the reference does.
	if tree, err := ParseOne(`CAST(x AS "a b")`, "duckdb"); err != nil {
		t.Errorf(`CAST(x AS "a b") was refused: %v`, err)
	} else if got, _ := Generate(tree, "duckdb"); got != "CAST(x AS a b)" {
		t.Errorf(`CAST(x AS "a b") wrote %q`, got)
	}
	// A quoted name holding something the tokenizer cannot read at all names
	// nothing, and the reference falls back to the UNKNOWN type. The
	// generator fuzzer found the port writing the characters back out.
	if tree, err := ParseOne("0::\"``\"", "duckdb"); err != nil {
		t.Errorf("a quoted name of backticks was refused: %v", err)
	} else if got, _ := Generate(tree, "duckdb"); got != "CAST(0 AS UNKNOWN)" {
		t.Errorf("a quoted name of backticks wrote %q", got)
	}
	if tree, err := ParseOne("SELECT CAST(x AS [nope])", "tsql"); err != nil {
		t.Errorf("a bracketed type name was refused: %v", err)
	} else if got, _ := Generate(tree, "tsql"); got != "SELECT CAST(x AS nope)" {
		t.Errorf("a bracketed type name wrote %q", got)
	}
	if tree, err := ParseOne(`1::"udt"`, "postgres"); err != nil {
		t.Errorf("a quoted user-defined type was refused: %v", err)
	} else if got, _ := Generate(tree, "postgres"); got != `CAST(1 AS "udt")` {
		t.Errorf(`1::"udt" wrote %q`, got)
	}
	// A user-defined type may be SCHEMA-QUALIFIED -- `a.b.c` -- and the
	// reference keeps the whole dotted name as one string, joining each
	// part's own text whether it was quoted or not. The port wrote this
	// dotted name back out and could not read it again: the generator
	// fuzzer found it.
	for _, c := range []struct{ sql, dialect, want string }{
		{`CAST(0 AS d_.NNNN_)`, "duckdb", ""},
		{`CAST(0 AS d_.NNNN_)`, "tsql", ""},
		{`0::"d_.NNNN_"`, "duckdb", "CAST(0 AS d_.NNNN_)"},
		{`CAST(0 AS a.b.c)`, "duckdb", ""},
	} {
		want := c.want
		if want == "" {
			want = c.sql
		}
		tree, err := ParseOne(c.sql, c.dialect)
		if err != nil {
			t.Errorf("[%s] %s: %v", c.dialect, c.sql, err)
			continue
		}
		if got, err := Generate(tree, c.dialect); err != nil || got != want {
			t.Errorf("[%s] %s wrote %q (%v), want %q", c.dialect, c.sql, got, err, want)
		}
	}
}

// TestNamedTypeWords covers the type words that are not USER-DEFINED types at
// all: PostgreSQL's OID and its `reg*` family name a slot in the catalog
// (ObjectIdentifier), CSTRING is a PseudoType one level down, and
// Databricks reads VOID as the ordinary NULL type -- neither carries a
// DataType.Type member of its own.
func TestNamedTypeWords(t *testing.T) {
	for _, c := range []struct{ sql, want, dialect string }{
		{"x::cstring", "CAST(x AS CSTRING)", "postgres"},
		{"x::oid", "CAST(x AS OID)", "postgres"},
		{"x::regclass", "CAST(x AS REGCLASS)", "postgres"},
		{"x::regcollation", "CAST(x AS REGCOLLATION)", "postgres"},
		{"x::regconfig", "CAST(x AS REGCONFIG)", "postgres"},
		{"x::regdictionary", "CAST(x AS REGDICTIONARY)", "postgres"},
		{"x::regnamespace", "CAST(x AS REGNAMESPACE)", "postgres"},
		{"x::regoper", "CAST(x AS REGOPER)", "postgres"},
		{"x::regoperator", "CAST(x AS REGOPERATOR)", "postgres"},
		{"x::regproc", "CAST(x AS REGPROC)", "postgres"},
		{"x::regprocedure", "CAST(x AS REGPROCEDURE)", "postgres"},
		{"x::regrole", "CAST(x AS REGROLE)", "postgres"},
		{"x::regtype", "CAST(x AS REGTYPE)", "postgres"},
		{"SELECT CAST(NULL AS VOID)", "SELECT CAST(NULL AS VOID)", "databricks"},
		// A quoted spelling re-lexes to the same keyword, in whichever
		// dialect has it -- DuckDB has none of PostgreSQL's OID family, so
		// the quoted word names a USER-DEFINED type there instead.
		{`x::"oid"`, "CAST(x AS OID)", "postgres"},
		{`x::"regclass"`, "CAST(x AS REGCLASS)", "postgres"},
		{`x::"cstring"`, "CAST(x AS CSTRING)", "postgres"},
		{`x::"oid"`, "CAST(x AS oid)", "duckdb"},
	} {
		tree, err := ParseOne(c.sql, c.dialect)
		if err != nil {
			t.Errorf("[%s] %s: %v", c.dialect, c.sql, err)
			continue
		}
		if got, err := Generate(tree, c.dialect); err != nil || got != c.want {
			t.Errorf("[%s] %s wrote %q (%v)", c.dialect, c.sql, got, err)
		}
	}
}

// TestDateDiffAndDateName covers T-SQL's DATEDIFF, DATEDIFF_BIG and DATENAME,
// each read by a builder of its own rather than a generic signature: the
// probe driving builders with placeholder columns could not see a decision
// this only makes once it has real arguments in hand.
func TestDateDiffAndDateName(t *testing.T) {
	// DATENAME's own spelling, `mm` for a full month name and `dw` for a
	// full weekday, is rewritten into the reference's FORMAT on the way in
	// -- there is no DateName node to hold it in.
	for _, c := range []struct{ sql, want string }{
		{"SELECT DATEDIFF(WEEK, d2, d1)", "SELECT DATEDIFF(WEEK, CAST(d2 AS DATETIME2), CAST(d1 AS DATETIME2))"},
		{"SELECT DATEDIFF(YEAR, 'start', 'end')", "SELECT DATEDIFF(YEAR, CAST('start' AS DATETIME2), CAST('end' AS DATETIME2))"},
		{"SELECT DATEDIFF(WEEK, '2009-01-01', '2009-12-31')", "SELECT DATEDIFF(WEEK, CAST('2009-01-01' AS DATETIME2), CAST('2009-12-31' AS DATETIME2))"},
		{"SELECT DATEDIFF(HOUR, 1.5, '2021-01-01')", "SELECT DATEDIFF(HOUR, 1.5, '2021-01-01')"},
		{"SELECT DATEDIFF_BIG(WEEK, d2, d1)", "SELECT DATEDIFF_BIG(WEEK, CAST(d2 AS DATETIME2), CAST(d1 AS DATETIME2))"},
		{"SELECT DATEDIFF(zzz, d2, d1)", "SELECT DATEDIFF(ZZZ, CAST(d2 AS DATETIME2), CAST(d1 AS DATETIME2))"},
		{"SELECT DATENAME(mm, '1970-01-01')", "SELECT FORMAT(CAST('1970-01-01' AS DATETIME2), 'MMMM')"},
		{"SELECT DATENAME(dw, '1970-01-01')", "SELECT FORMAT(CAST('1970-01-01' AS DATETIME2), 'dddd')"},
		// The IGNORE/RESPECT NULLS modifier is read after any call, this one
		// included, and wraps the node the builder returns rather than
		// being dropped on the floor.
		{"SELECT DATEDIFF(WEEK, d2, d1) IGNORE NULLS", "SELECT DATEDIFF(WEEK, CAST(d2 AS DATETIME2), CAST(d1 AS DATETIME2)) IGNORE NULLS"},
		{"SELECT DATENAME(mm, x) RESPECT NULLS", "SELECT FORMAT(CAST(x AS DATETIME2), 'MMMM') RESPECT NULLS"},
	} {
		tree, err := ParseOne(c.sql, "tsql")
		if err != nil {
			t.Errorf("%s: %v", c.sql, err)
			continue
		}
		if got, err := Generate(tree, "tsql"); err != nil || got != c.want {
			t.Errorf("%s wrote %q (%v)", c.sql, got, err)
		}
	}
	// DATEDIFF's start date, read as an INTEGER rather than a date-like
	// string, is a shape the reference builds by counting days from 1900:
	// not yet implemented, and refused rather than approximated.
	if _, err := ParseOne("SELECT DATEDIFF(HOUR, 1, '2021-01-01')", "tsql"); err == nil {
		t.Error("DATEDIFF over an integer date was read; it should be refused")
	}
	for _, c := range []struct{ sql, dialect string }{
		{"SELECT DATEDIFF(WEEK, d2)", "tsql"},
		{"SELECT DATENAME(mm)", "tsql"},
	} {
		if _, err := ParseOne(c.sql, c.dialect); err == nil {
			t.Errorf("[%s] %s was read with too few arguments; it should be refused", c.dialect, c.sql)
		}
	}
}

// Four shapes read the other way round or one level deeper than the plain
// one: a temporal clause where an alias would go, a length before a start, a
// set operation inside an IN, and an argument with no name to take.
func TestReorderedAndNestedShapes(t *testing.T) {
	for _, c := range []struct{ sql, want, dialect string }{
		{"SELECT * FROM my_ducklake.demo AT (VERSION => 2)", "", "duckdb"},
		{"SELECT * FROM t1 AS a AT (VERSION => 3)", "", "duckdb"},
		{"SELECT * FROM t AS at", "", "duckdb"},
		// The reference writes the two the usual way round.
		{"SELECT SUBSTRING('Thomas' FOR 3 FROM 2)", "SELECT SUBSTRING('Thomas' FROM 2 FOR 3)", "postgres"},
		{"SELECT SUBSTRING('Thomas' FOR 3)", "SELECT SUBSTRING('Thomas' FROM 1 FOR 3)", "postgres"},
		{"SELECT SUBSTRING('Thomas' FROM 2 FOR 3)", "", "postgres"},
		{"SELECT * FROM x WHERE y IN ((SELECT 1) EXCEPT (SELECT 2))", "", ""},
		{"SELECT * FROM x WHERE y IN ((SELECT 1) UNION (SELECT 2) OFFSET 2)", "", ""},
		{"SELECT * FROM x WHERE y IN (SELECT 1)", "", ""},
		{"SELECT * FROM x WHERE y IN ((1), (2))", "", ""},
		// A wrapped slot keeps the NODE where the argument has no name.
		{"SELECT DATE_BIN('30 days', a, (SELECT 1)) FROM t", "", "postgres"},
		{"SELECT DATEADD(DAY, 1, a)", "", "tsql"},
	} {
		want := c.want
		if want == "" {
			want = c.sql
		}
		tree, err := ParseOne(c.sql, c.dialect)
		if err != nil {
			t.Errorf("[%s] %s: %v", c.dialect, c.sql, err)
			continue
		}
		got, err := Generate(tree, c.dialect)
		if err != nil || got != want {
			t.Errorf("[%s] %s wrote %q (%v)", c.dialect, c.sql, got, err)
		}
	}
	for _, c := range []struct{ sql, dialect string }{
		{"SELECT SUBSTRING('a' FOR", "postgres"},
		{"SELECT SUBSTRING('a' FOR 1 FROM", "postgres"},
		{"SELECT SUBSTRING('a' FOR 1 FROM 2", "postgres"},
		{"SELECT * FROM t AT (", "duckdb"},
	} {
		if _, err := ParseOne(c.sql, c.dialect); err == nil {
			t.Errorf("[%s] %s was read; it should be refused", c.dialect, c.sql)
		}
	}
}

// An alias whose columns name their types, a VALUES that is a query on its
// own, and a FROM-first query on either side of a set operation.
func TestTypedAliasesAndBodylessQueries(t *testing.T) {
	for _, c := range []struct{ sql, want, dialect string }{
		{`SELECT * FROM JSON_TO_RECORDSET(z) AS y("rank" INT)`, "", "postgres"},
		{"SELECT * FROM t AS y(a, b)", "", "postgres"},
		{"WITH t AS (VALUES ('foo_val') AS t(foo1)) SELECT foo1 FROM t",
			"WITH t AS (SELECT * FROM VALUES ('foo_val') AS t(foo1)) SELECT foo1 FROM t", "databricks"},
		{"FROM (FROM t1 UNION FROM t2)",
			"SELECT * FROM (SELECT * FROM t1 UNION SELECT * FROM t2)", "duckdb"},
		{"SELECT 1 UNION SELECT 2", "", ""},
		// A bracketed type name is lexed again, and the FIRST token settles
		// it -- `[INT 0]` is an INT, with the number written nowhere.
		{"CAST(0 AS [INT 0])", "CAST(0 AS INTEGER)", "tsql"},
		{"CAST(0 AS [a b])", "CAST(0 AS a b)", "tsql"},
		// A VALUES that carries its own modifiers.
		{"WITH t AS (VALUES (1), (2) ORDER BY 1) SELECT * FROM t",
			"WITH t AS (SELECT * FROM VALUES (1), (2) ORDER BY 1) SELECT * FROM t", "databricks"},
	} {
		want := c.want
		if want == "" {
			want = c.sql
		}
		tree, err := ParseOne(c.sql, c.dialect)
		if err != nil {
			t.Errorf("[%s] %s: %v", c.dialect, c.sql, err)
			continue
		}
		got, err := Generate(tree, c.dialect)
		if err != nil || got != want {
			t.Errorf("[%s] %s wrote %q (%v)", c.dialect, c.sql, got, err)
		}
	}
	for _, c := range []struct{ sql, dialect string }{
		// A VALUES on the far side of a set operation is a shape of its own
		// -- the reference gives it an alias of its own making -- and stays
		// refused rather than half-built.
		{"WITH t AS (VALUES (1) UNION VALUES (2)) SELECT * FROM t", "databricks"},
		{`SELECT * FROM t AS y(a wat wat)`, "postgres"},
		{`SELECT * FROM t AS y(a 3)`, "postgres"},
		{"SELECT 1 UNION UPDATE t SET a = 1", ""},
		{"WITH t AS (VALUES (1)", "databricks"},
		{"WITH t AS (VALUES (1) ORDER BY", "databricks"},
		{"FROM t1 SELECT )", "duckdb"},
		{"FROM (FROM t1 UNION FROM", "duckdb"},
		{"CAST(0 AS [", "tsql"},
	} {
		if _, err := ParseOne(c.sql, c.dialect); err == nil {
			t.Errorf("[%s] %s was read; it should be refused", c.dialect, c.sql)
		}
	}
}

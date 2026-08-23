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
		{"interval unit in string", "select interval '1 day'", "", "SELECT INTERVAL '1' DAY"},
		{"interval number becomes string", "select interval 1 day", "", "SELECT INTERVAL '1' DAY"},
		{"interval postgres folds the unit", "select interval '1' day", "postgres", "SELECT INTERVAL '1 DAY'"},
		{"interval span", "select interval '1' hour to second", "", "SELECT INTERVAL '1' HOUR TO SECOND"},
		{"interval many units stays whole", "select interval '1 year 2 months'", "",
			"SELECT INTERVAL '1 year 2 months'"},
		{"window partition order", "select sum(x) over (partition by a order by b)", "",
			"SELECT SUM(x) OVER (PARTITION BY a ORDER BY b)"},
		{"window frame", "select sum(x) over (rows between unbounded preceding and current row)", "",
			"SELECT SUM(x) OVER (ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW)"},
		{"window single bound normalises", "select sum(x) over (rows 3 preceding)", "",
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
	// A dialect with no boolean type writes `x <> 0` for a value used as a
	// condition. The port does not do that rewrite, and the uncoerced form is
	// a statement T-SQL rejects -- so it refuses rather than emit it.
	tree, err := ParseOne("SELECT * FROM t WHERE NOT c", "tsql")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := Generate(tree, "tsql"); err == nil {
		t.Errorf("wrote %q where the dialect needs a coercion the port does not do", got)
	}

	// A node the generator has no writer for stops the rewrite.
	if _, err := Generate(New("NotARealNode"), ""); err == nil {
		t.Error("an unknown node should not be written")
	} else if !strings.Contains(err.Error(), "NotARealNode") {
		t.Errorf("the error should name the node: %v", err)
	}

	if _, err := Generate(New("Select"), "oracle"); err == nil {
		t.Error("an unknown dialect should not be written")
	}

	// Nil is nothing, not an error: an absent clause writes as empty.
	if got, err := Generate(nil, ""); err != nil || got != "" {
		t.Errorf("Generate(nil) = %q, %v", got, err)
	}
	_ = errors.New
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

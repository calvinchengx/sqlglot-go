package sqlglot

import (
	"errors"
	"strings"
	"testing"
)

// classes returns the node classes of a tree in walk order, which is enough to
// pin a rule's shape here. Exactness is the differential harness's job: it
// compares every tree in the corpus against the reference field for field.
func classes(e *Expression) string {
	var out []string
	e.Walk(func(n *Expression) bool {
		out = append(out, n.Class)
		return true
	})
	return strings.Join(out, " ")
}

func parse(t *testing.T, sql, dialect string) *Expression {
	t.Helper()
	tree, err := ParseOne(sql, dialect)
	if err != nil {
		t.Fatalf("ParseOne(%q, %q): %v", sql, dialect, err)
	}
	return tree
}

func TestParseShapes(t *testing.T) {
	for _, c := range []struct{ name, sql, want string }{
		{"literal", "SELECT 1", "Select Literal"},
		{"string", "SELECT 'a'", "Select Literal"},
		{"boolean", "SELECT TRUE", "Select Boolean"},
		{"null", "SELECT NULL", "Select Null"},
		{"star", "SELECT *", "Select Star"},
		{"column", "SELECT a", "Select Column Identifier"},
		{"qualified column", "SELECT t.a", "Select Column Identifier Identifier"},
		{"qualified star", "SELECT t.*", "Select Column Star Identifier"},
		{"alias", "SELECT a AS b", "Select Alias Column Identifier Identifier"},
		{"implicit alias", "SELECT a b", "Select Alias Column Identifier Identifier"},
		{"negation", "SELECT -1", "Select Neg Literal"},
		{"parens", "SELECT (1)", "Select Paren Literal"},
		{"disjunction", "SELECT 1 WHERE a OR b", "Select Literal Where Or Column Identifier Column Identifier"},
		{"conjunction", "SELECT 1 WHERE a AND b", "Select Literal Where And Column Identifier Column Identifier"},
		{"negated predicate", "SELECT 1 WHERE NOT a", "Select Literal Where Not Column Identifier"},
		{"comparison", "SELECT 1 WHERE a > 1", "Select Literal Where GT Column Identifier Literal"},
		{"arithmetic", "SELECT 1 + 2 * 3", "Select Add Literal Mul Literal Literal"},
		{"division", "SELECT 1 / 2", "Select Div Literal Literal"},
		{"table", "SELECT * FROM t", "Select Star From Table Identifier"},
		{"qualified table", "SELECT * FROM c.d.t", "Select Star From Table Identifier Identifier Identifier"},
		{"table alias", "SELECT * FROM t AS x", "Select Star From Table Identifier TableAlias Identifier"},
		{"group and having", "SELECT a FROM t GROUP BY a HAVING a",
			"Select Column Identifier From Table Identifier Group Column Identifier Having Column Identifier"},
		{"order", "SELECT a FROM t ORDER BY a DESC",
			"Select Column Identifier From Table Identifier Order Ordered Column Identifier"},
		{"limit and offset", "SELECT a FROM t LIMIT 1 OFFSET 2",
			"Select Column Identifier Limit Literal From Table Identifier Offset Literal"},
		{"trailing semicolon", "SELECT 1;", "Select Literal"},
		{"anonymous call", "SELECT my_func(a, 1)", "Select Anonymous Column Identifier Literal"},
		{"anonymous call with no arguments", "SELECT my_func()", "Select Anonymous"},
		{"named function", "SELECT ABS(a)", "Select Abs Column Identifier"},
		{"variadic named function", "SELECT ARRAYS_ZIP(a, b, c)",
			"Select ArraysZip Column Identifier Column Identifier Column Identifier"},
		{"keyword as a column name", "SELECT update", "Select Column Identifier"},
		{"keyword as a table name", "SELECT * FROM update", "Select Star From Table Identifier"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := classes(parse(t, c.sql, "")); got != c.want {
				t.Errorf("ParseOne(%q)\n  want %s\n  got  %s", c.sql, c.want, got)
			}
		})
	}
}

// Clause order is the order the clauses appear in, because that is the order
// the reference assigns them -- and the dump follows assignment order, so a
// port that normalised them would mismatch on every statement.
// Most of sqlglot's own fixture corpus is expressions rather than whole
// statements, and the reference parses them as such.
func TestBareExpressionsAtTheTopLevel(t *testing.T) {
	for _, c := range []struct{ sql, want string }{
		{"1", "Literal"},
		{"a + 1", "Add Column Identifier Literal"},
		{"my_func(a)", "Anonymous Column Identifier"},
		{"a AND b", "And Column Identifier Column Identifier"},
	} {
		if got := classes(parse(t, c.sql, "")); got != c.want {
			t.Errorf("ParseOne(%q)\n  want %s\n  got  %s", c.sql, c.want, got)
		}
	}
}

func TestClauseOrderFollowsTheSource(t *testing.T) {
	tree := parse(t, "SELECT a FROM t WHERE a ORDER BY a LIMIT 1", "")
	// limit sits in the slot Select reserves for it at construction, before
	// from_ -- not where it appears in the SQL.
	want := "kind hint distinct expressions limit exclude operation_modifiers from_ where order"
	if got := strings.Join(tree.Keys, " "); got != want {
		t.Errorf("Select args\n  want %s\n  got  %s", want, got)
	}
}

func TestRefusals(t *testing.T) {
	for _, c := range []struct{ name, sql, dialect string }{
		{"a statement that is not a SELECT", "DELETE FROM t", ""},
		{"function with a builder of its own", "SELECT COUNT(*)", ""},
		{"function with a custom argument shape", "SELECT TIMESTAMP_TRUNC(t, MONTH)", ""},
		{"no-paren function", "SELECT CURRENT_DATE", "duckdb"},
		{"no-paren function named, not tokenized", "CURDATE", "databricks"},
		{"DISTINCT inside a call", "SELECT f(DISTINCT a)", ""},
		{"INTERVAL", "INTERVAL '1' DAY", ""},
		{"GROUP BY ROLLUP", "SELECT a FROM t GROUP BY ROLLUP (a)", ""},
		{"STREAM table", "SELECT * FROM STREAM t", "databricks"},
		{"qualified function call", "SELECT a.f(1)", ""},
		{"table function", "SELECT * FROM read_csv('x')", ""},
		{"cross apply", "SELECT * FROM a CROSS APPLY f(1)", "tsql"},
		{"parenthesised table", "SELECT 1 FROM (t)", ""},
		{"natural join", "SELECT 1 FROM a NATURAL JOIN b", ""},
		{"USING", "SELECT 1 FROM a JOIN b USING (x)", ""},
		{"a side with no JOIN", "SELECT 1 FROM a LEFT b", ""},
		{"unclosed subquery", "SELECT 1 FROM (SELECT 1", ""},
		{"assignment", "SELECT 1 FROM t WHERE a := 1", ""},
		{"COLLATE", "SELECT a COLLATE utf8 FROM t", ""},
		{"CTE", "WITH c AS (SELECT 1) SELECT * FROM c", ""},
		{"set operation", "SELECT 1 UNION SELECT 2", ""},
		{"SELECT ALL", "SELECT ALL a FROM t", ""},
		{"DISTINCT ON", "SELECT DISTINCT ON (a) a FROM t", ""},
		{"hint", "SELECT /*+ x */ 1 FROM t", ""},
		{"nulls ordering", "SELECT a FROM t ORDER BY a NULLS FIRST", ""},
		{"over-qualified column", "SELECT a.b.c.d.e", ""},
		{"over-qualified table", "SELECT 1 FROM a.b.c.d", ""},
		{"alias with a column list", "SELECT * FROM t AS x (a, b)", ""},
		{"trailing tokens", "SELECT 1 FROM t )", ""},
		{"unclosed parenthesis", "SELECT (1", ""},
		{"empty expression", "SELECT", ""},
		{"T-SQL alias assignment", "SELECT a = 1", "tsql"},
		{"T-SQL temp table", "SELECT * FROM #t", "tsql"},
		{"T-SQL temp table as a column", "SELECT [#a]", "tsql"},
		{"T-SQL hash inside a name", "SELECT a#b", "tsql"},
		{"dangling HAVING", "SELECT a FROM t HAVING", ""},
		{"IN list", "SELECT 1 WHERE a IN (1, 2)", ""},
		{"CAST", "SELECT CAST(a AS INT)", ""},
		{"dangling conjunction", "SELECT 1 WHERE a AND", ""},
		{"dangling disjunction", "SELECT 1 WHERE a OR", ""},
		{"dangling comparison", "SELECT 1 WHERE a >", ""},
		{"dangling negation", "SELECT -", ""},
		{"dangling qualifier", "SELECT a.", ""},
		{"dangling alias", "SELECT * FROM t AS", ""},
		{"dangling FROM", "SELECT * FROM", ""},
		{"dangling ORDER BY", "SELECT a FROM t ORDER BY", ""},
		{"dangling GROUP BY", "SELECT a FROM t GROUP BY", ""},
		{"dangling addition", "a +", ""},
		{"dangling bitwise and", "a &", ""},
		{"dangling concatenation", "a ||", ""},
		{"dangling shift", "a <<", ""},
		{"dangling bitwise not", "~", ""},
		{"dangling NOT", "NOT", ""},
		{"dangling JOIN", "SELECT 1 FROM a JOIN", ""},
		{"dangling ON", "SELECT 1 FROM a JOIN b ON", ""},
		{"dangling comma join", "SELECT 1 FROM a,", ""},
		{"dangling subquery alias", "SELECT 1 FROM (SELECT 1) AS", ""},
		{"over-qualified star", "SELECT a.b.c.d.*", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			tree, err := ParseOne(c.sql, c.dialect)
			if err == nil {
				t.Fatalf("ParseOne(%q, %q) returned a tree instead of refusing:\n%s", c.sql, c.dialect, tree.DumpJSON())
			}
			if !errors.Is(err, ErrUnsupported) {
				t.Errorf("ParseOne(%q) failed with %v, want ErrUnsupported", c.sql, err)
			}
		})
	}
}

func TestRepeatedClauseIsRefused(t *testing.T) {
	// The reference raises rather than letting the later clause win, and
	// silently dropping one would change what the statement means.
	for _, sql := range []string{
		"SELECT a FROM t WHERE a WHERE b",
		"SELECT a FROM t GROUP BY a GROUP BY b",
		"SELECT a FROM t HAVING a HAVING b",
		"SELECT a FROM t ORDER BY a ORDER BY b",
		"SELECT a FROM t LIMIT 1 LIMIT 2",
		"SELECT a FROM t OFFSET 1 OFFSET 2",
	} {
		if _, err := ParseOne(sql, ""); err == nil {
			t.Errorf("ParseOne(%q) should refuse a repeated clause", sql)
		}
	}
}

func TestParseErrorsFromTheTokenizer(t *testing.T) {
	if _, err := ParseOne("SELECT 'unterminated", ""); err == nil {
		t.Error("an unlexable statement should not parse")
	}
	var te *TokenError
	_, err := ParseOne("SELECT 'unterminated", "")
	if !errors.As(err, &te) {
		t.Errorf("want the tokenizer's error to surface, got %T", err)
	}
}

func TestParseUnknownDialect(t *testing.T) {
	_, err := ParseOne("SELECT 1", "oracle")
	if err == nil {
		t.Fatal("want an error for a dialect the port does not configure")
	}
	if !strings.Contains(err.Error(), "oracle") {
		t.Errorf("the error should name the dialect asked for: %s", err)
	}
}

// Precedence is not a detail. Each of these pairs is a level the reference
// keeps separate and a port could plausibly collapse -- and collapsing one
// parses most statements correctly and a few silently wrong.
func TestPrecedenceLevelsThatAreEasyToCollapse(t *testing.T) {
	for _, c := range []struct{ name, sql, dialect, want string }{
		// EQUALITY is looser than COMPARISON: a = (b > c), not (a = b) > c.
		{"equality below comparison", "a = b > c", "", "EQ Column Identifier GT Column Identifier Column Identifier"},
		// MOD is an additive operator, so a % (b * c), not (a % b) * c.
		{"modulo binds like addition", "a % b * c", "", "Mod Column Identifier Mul Column Identifier Column Identifier"},
		// BITWISE sits between comparison and addition.
		{"bitwise below addition", "a & b + c", "", "BitwiseAnd Column Identifier Add Column Identifier Column Identifier"},
		{"multiplication binds tighter", "a + b * c", "", "Add Column Identifier Mul Column Identifier Column Identifier"},
		// NOT takes an equality, so it negates the comparison.
		{"NOT covers the comparison", "NOT a = b", "", "Not EQ Column Identifier Column Identifier"},
		// DuckDB reads ^ as exponentiation where the default reads xor.
		{"caret is xor by default", "a ^ b", "", "BitwiseXor Column Identifier Column Identifier"},
		{"caret is power in DuckDB", "a ^ b", "duckdb", "Pow Column Identifier Column Identifier"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := classes(parse(t, c.sql, c.dialect)); got != c.want {
				t.Errorf("ParseOne(%q, %q)\n  want %s\n  got  %s", c.sql, c.dialect, c.want, got)
			}
		})
	}
}

func TestOperators(t *testing.T) {
	for _, c := range []struct{ name, sql, want string }{
		{"bitwise or", "a | b", "BitwiseOr Column Identifier Column Identifier"},
		{"left shift", "a << b", "BitwiseLeftShift Column Identifier Column Identifier"},
		{"right shift", "a >> b", "BitwiseRightShift Column Identifier Column Identifier"},
		{"concatenation", "a || b", "DPipe Column Identifier Column Identifier"},
		{"bitwise not", "~a", "BitwiseNot Column Identifier"},
		{"unary plus is a no-op", "+a", "Column Identifier"},
		{"integer division", "a DIV b", "IntDiv Column Identifier Column Identifier"},
		{"null-safe equality", "a <=> b", "NullSafeEQ Column Identifier Column Identifier"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := classes(parse(t, c.sql, "")); got != c.want {
				t.Errorf("ParseOne(%q)\n  want %s\n  got  %s", c.sql, c.want, got)
			}
		})
	}
}

// The division flags are read off the dialect, not defaulted -- T-SQL and
// PostgreSQL divide with integer semantics and the node records it.
func TestDivisionRecordsDialectSemantics(t *testing.T) {
	for dialect, typed := range map[string]bool{"": false, "tsql": true, "postgres": true, "duckdb": false} {
		div := parse(t, "a / b", dialect)
		if got := div.Args["typed"]; got != typed {
			t.Errorf("Div.typed for %q = %v, want %v", dialect, got, typed)
		}
	}
}

// A comma join is a join. Reading `FROM a, b` as `FROM a` is the bypass that
// motivated this port: the guard saw one table and the engine read two.
func TestCommaJoinIsAJoin(t *testing.T) {
	tree := parse(t, "SELECT * FROM a, other.secrets", "")
	tables := tree.FindAll("Table")
	if len(tables) != 2 {
		t.Fatalf("found %d tables, want 2 -- a comma join must not disappear", len(tables))
	}
	if got := tables[1].Name(); got != "secrets" {
		t.Errorf("the second table is %q, want secrets", got)
	}
	if got := tables[1].Args["db"].(*Expression).Name(); got != "other" {
		t.Errorf("the second table's schema is %q, want other", got)
	}
}

func TestJoins(t *testing.T) {
	for _, c := range []struct{ name, sql, want string }{
		{"plain", "SELECT 1 FROM a JOIN b ON x", "Select Literal From Table Identifier Join Table Identifier Column Identifier"},
		{"comma", "SELECT 1 FROM a, b", "Select Literal From Table Identifier Join Table Identifier"},
		{"cross", "SELECT 1 FROM a CROSS JOIN b", "Select Literal From Table Identifier Join Table Identifier"},
		{"several", "SELECT 1 FROM a, b, c",
			"Select Literal From Table Identifier Join Table Identifier Join Table Identifier"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := classes(parse(t, c.sql, "")); got != c.want {
				t.Errorf("ParseOne(%q)\n  want %s\n  got  %s", c.sql, c.want, got)
			}
		})
	}

	// The side and kind are recorded as the reference records them: upper-cased
	// text on the Join node, not folded into one field.
	j := parse(t, "SELECT 1 FROM a LEFT OUTER JOIN b ON x", "").FindAll("Join")[0]
	if j.Args["side"] != "LEFT" || j.Args["kind"] != "OUTER" {
		t.Errorf("LEFT OUTER JOIN recorded side=%v kind=%v", j.Args["side"], j.Args["kind"])
	}
}

func TestSubqueryInFrom(t *testing.T) {
	tree := parse(t, "SELECT a FROM (SELECT a FROM t) AS x", "")
	want := "Select Column Identifier From Subquery Select Column Identifier From Table Identifier TableAlias Identifier"
	if got := classes(tree); got != want {
		t.Errorf("ParseOne\n  want %s\n  got  %s", want, got)
	}
	// The inner table is still reachable, which is what a guard walking the
	// tree for table references depends on.
	if n := len(tree.FindAll("Table")); n != 1 {
		t.Errorf("found %d tables inside the subquery, want 1", n)
	}
}

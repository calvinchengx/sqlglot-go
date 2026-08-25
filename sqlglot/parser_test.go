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
		{"STRING_AGG with an ORDER BY", "SELECT STRING_AGG(x, ',' ORDER BY y)", "duckdb"},
		{"no-paren function named, not tokenized", "CURDATE", "databricks"},
		{"ORDER BY with nothing to order", "SELECT RANK(ORDER BY b) OVER ()", "duckdb"},
		{"non-numeric type parameter", "a::VARCHAR('x')", "tsql"},
		// HASHBYTES decides its class by inspecting its first argument, which
		// the port refuses rather than half-implement -- so it also refuses to
		// WRITE a node whose only spelling is that call. See
		// generator.parserWouldRefuse.
		{"a builder that inspects its arguments", "HASHBYTES('SHA1', x)", "tsql"},
		{"the ALL quantifier, which the port does not carry", "SELECT ALL x", "databricks"},
		{"fixed-size array where the dialect has none", "CAST(a AS INT[3])", "databricks"},
		{"unknown type", "CAST(a AS wat)", ""},
		{"CAST without AS", "CAST(a INT)", ""},
		{"unclosed CAST", "CAST(a AS INT", ""},
		{"unclosed type parameters", "CAST(a AS VARCHAR(10)", ""},
		{"CASE without WHEN", "CASE a END", ""},
		{"WHEN without THEN", "CASE WHEN a END", ""},
		{"CASE without END", "CASE WHEN a THEN 1", ""},
		{"dangling CASE subject", "CASE", ""},
		{"IN without parentheses", "a IN b", ""},
		{"unclosed IN list", "a IN (1", ""},
		{"dangling IS", "a IS", ""},
		{"dangling BETWEEN", "a BETWEEN", ""},
		{"dangling type", "a::", ""},
		{"dangling NOT at the range level", "a NOT", ""},
		{"NOT that introduces nothing", "a NOT b", ""},
		{"dangling BETWEEN bound", "a BETWEEN 1 AND", ""},
		{"unclosed type parameters", "a::VARCHAR(10", ""},
		{"a grouping with no arguments", "SELECT a FROM t GROUP BY ROLLUP", ""},
		{"STREAM table", "SELECT * FROM STREAM t", "databricks"},
		{"qualified function call", "SELECT a.f(1)", ""},
		{"statement where a table goes", "SELECT 1 FROM (DESCRIBE t)", "duckdb"},
		{"natural join", "SELECT 1 FROM a NATURAL JOIN b", ""},
		{"USING with no columns", "SELECT 1 FROM a JOIN b USING", ""},
		{"a side with no JOIN", "SELECT 1 FROM a LEFT b", ""},
		{"unclosed subquery", "SELECT 1 FROM (SELECT 1", ""},
		{"assignment", "SELECT 1 FROM t WHERE a := 1", ""},
		{"COLLATE", "SELECT a COLLATE utf8 FROM t", ""},
		{"SELECT ALL", "SELECT ALL a FROM t", ""},
		{"DISTINCT ON", "SELECT DISTINCT ON (a) a FROM t", ""},
		{"hint", "SELECT /*+ x */ 1 FROM t", ""},
		{"WITH FILL", "SELECT a FROM t ORDER BY a WITH FILL", ""},
		{"WITHIN GROUP with no ORDER BY", "SELECT STRING_AGG(x, ',') WITHIN GROUP (y)", ""},
		{"over-qualified column", "SELECT a.b.c.d.e", ""},
		{"over-qualified table", "SELECT 1 FROM a.b.c.d", ""},
		{"trailing tokens", "SELECT 1 FROM t )", ""},
		{"unclosed parenthesis", "SELECT (1", ""},
		{"empty expression", "SELECT", ""},
		{"T-SQL alias assignment", "SELECT a = 1", "tsql"},
		{"T-SQL temp table", "SELECT * FROM #t", "tsql"},
		{"T-SQL temp table as a column", "SELECT [#a]", "tsql"},
		{"T-SQL hash inside a name", "SELECT a#b", "tsql"},
		{"dangling HAVING", "SELECT a FROM t HAVING", ""},
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
		{"APPLY over a column", "SELECT * FROM a CROSS APPLY b", "tsql"},
		{"APPLY over a literal", "SELECT * FROM a CROSS APPLY 1", "tsql"},
		{"LEFT APPLY", "SELECT * FROM a LEFT OUTER APPLY (SELECT 1) t", "tsql"},
		{"SELECT INTO a temporary table", "SELECT * INTO TEMPORARY t2 FROM t1", "postgres"},
		{"over-qualified table function", "SELECT * FROM a.b.c.f()", ""},
		{"APPLY over an unclosed subquery", "SELECT * FROM a CROSS APPLY (SELECT 1", "tsql"},
		{"APPLY over a function the port cannot build", "SELECT * FROM a CROSS APPLY OVERLAY(a PLACING b FROM 1)", "tsql"},
		{"APPLY over a qualified name that is not a call", "SELECT * FROM a CROSS APPLY b.c", "tsql"},
		{"APPLY over a qualifier followed by a number", "SELECT * FROM a CROSS APPLY dbo.1", "tsql"},
		{"SELECT INTO nothing", "SELECT * INTO FROM t1", "tsql"},
		{"a CTE with no body", "WITH a AS SELECT 1 SELECT 2", ""},
		{"CTE without AS", "WITH a (SELECT 1) SELECT 2", ""},
		{"CTE without a query", "WITH a AS SELECT 1", ""},
		{"unclosed CTE", "WITH a AS (SELECT 1 SELECT 2", ""},
		{"set operation over a value", "SELECT 1 UNION 2", ""},
		{"UNION BY NAME", "SELECT 1 UNION BY NAME SELECT 2", "duckdb"},
		{"TOP without a count", "SELECT TOP a FROM t", "tsql"},
		{"a call named by a word that cannot name one", "SELECT asc(1)", ""},
		{"unclosed scalar subquery", "SELECT (SELECT 1", ""},
		{"a scalar subquery that does not parse", "SELECT (SELECT)", ""},
		{"CTE named by nothing", "WITH AS (SELECT 1) SELECT 2", ""},
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

func TestRangeOperators(t *testing.T) {
	for _, c := range []struct{ name, sql, want string }{
		{"is null", "a IS NULL", "Is Column Identifier Null"},
		// Not(Is(...)), which is what the reference builds in every dialect but
		// PostgreSQL. This case previously asserted the Is-with-negate shape --
		// the port's own behaviour rather than the reference's -- so it passed
		// while the two disagreed about one of the commonest predicates there
		// is. The PostgreSQL half is pinned in TestIsNotNullShapeIsPerDialect.
		{"is not null", "a IS NOT NULL", "Not Is Column Identifier Null"},
		{"in", "a IN (1, 2)", "In Column Identifier Literal Literal"},
		{"not in", "a NOT IN (1)", "Not In Column Identifier Literal"},
		{"between", "a BETWEEN 1 AND 2", "Between Column Identifier Literal Literal"},
		{"like", "a LIKE 'x'", "Like Column Identifier Literal"},
		{"ilike", "a ILIKE 'x'", "ILike Column Identifier Literal"},
		{"rlike", "a RLIKE 'x'", "RegexpLike Column Identifier Literal"},
		{"is not a value", "a IS NOT b", "Not Is Column Identifier Column Identifier"},
		// A negated range followed by another is parenthesised, so the two
		// cannot re-associate into something the reference never meant.
		{"negated range then another", "a NOT LIKE 'x' NOT LIKE 'y'",
			"Like Paren Like Column Identifier Literal Literal"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := classes(parse(t, c.sql, "")); got != c.want {
				t.Errorf("ParseOne(%q)\n  want %s\n  got  %s", c.sql, c.want, got)
			}
		})
	}

	// IS NOT NULL flags the Is node IN POSTGRESQL and wraps in a Not
	// everywhere else; NOT LIKE flags the Like node; NOT IN wraps in a Not.
	// The reference distinguishes all of these and so must this.
	if got := parse(t, "a IS NOT NULL", "postgres").Args["negate"]; got != true {
		t.Errorf("IS NOT NULL did not set negate in postgres: %v", got)
	}
	if got := parse(t, "a IS NOT NULL", "").Class; got != "Not" {
		t.Errorf("IS NOT NULL outside postgres should wrap in a Not, got %v", got)
	}
	if got := parse(t, "a NOT LIKE 'x'", "").Args["negate"]; got != true {
		t.Errorf("NOT LIKE did not set negate: %v", got)
	}
	if got := parse(t, "a NOT IN (1)", "").Class; got != "Not" {
		t.Errorf("NOT IN produced %s, want a Not wrapping the In", got)
	}
}

func TestCase(t *testing.T) {
	for _, c := range []struct{ name, sql, want string }{
		{"searched", "CASE WHEN a THEN 1 END", "Case If Column Identifier Literal"},
		{"with a subject", "CASE a WHEN 1 THEN 2 END", "Case Column Identifier If Literal Literal"},
		{"with an else", "CASE WHEN a THEN 1 ELSE 2 END", "Case If Column Identifier Literal Literal"},
		{"several whens", "CASE WHEN a THEN 1 WHEN b THEN 2 END",
			"Case If Column Identifier Literal If Column Identifier Literal"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := classes(parse(t, c.sql, "")); got != c.want {
				t.Errorf("ParseOne(%q)\n  want %s\n  got  %s", c.sql, c.want, got)
			}
		})
	}
}

func TestCasts(t *testing.T) {
	for _, c := range []struct{ name, sql, want string }{
		{"cast", "CAST(a AS INT)", "Cast Column Identifier DataType"},
		{"try cast", "TRY_CAST(a AS INT)", "TryCast Column Identifier DataType"},
		{"double colon", "a::INT", "Cast Column Identifier DataType"},
		{"sized type", "CAST(a AS VARCHAR(10))", "Cast Column Identifier DataType DataTypeParam Literal"},
		{"two parameters", "CAST(a AS DECIMAL(10, 2))",
			"Cast Column Identifier DataType DataTypeParam Literal DataTypeParam Literal"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := classes(parse(t, c.sql, "")); got != c.want {
				t.Errorf("ParseOne(%q)\n  want %s\n  got  %s", c.sql, c.want, got)
			}
		})
	}

	// A cast reports the type it casts to, which the reference dumps as a
	// nested record list of its own. Omitting it mismatches on every cast.
	cast := parse(t, "CAST(a AS INT)", "")
	if cast.Type == nil || cast.Type.Class != "DataType" {
		t.Fatalf("cast has no type annotation: %v", cast.Type)
	}
	if !strings.Contains(string(cast.DumpJSON()), `"t"`) {
		t.Error("the type annotation did not reach the dump")
	}
	if parse(t, "TRY_CAST(a AS INT)", "").Args["safe"] != true {
		t.Error("TRY_CAST should be flagged safe")
	}
}

func TestNoParenFunctions(t *testing.T) {
	if got := classes(parse(t, "CURRENT_DATE", "duckdb")); got != "CurrentDate" {
		t.Errorf("CURRENT_DATE parsed as %s", got)
	}
}

func TestTopIsARowLimit(t *testing.T) {
	tree := parse(t, "SELECT TOP 10 a FROM t", "tsql")
	limit, _ := tree.Args["limit"].(*Expression)
	if limit == nil || limit.Class != "Limit" {
		t.Fatalf("TOP did not produce a Limit: %v", tree.Args["limit"])
	}
	if got := limit.Args["expression"].(*Expression).Name(); got != "10" {
		t.Errorf("TOP 10 recorded %q", got)
	}
	// The guard has to SEE a percentage to say that a proportion is not a row
	// ceiling. Refusing to parse it would refuse the statement for the wrong
	// reason, and the conformance suite checks the reason.
	pct := parse(t, "SELECT TOP 100 PERCENT a FROM t", "tsql")
	opts, _ := pct.Args["limit"].(*Expression).Args["limit_options"].(*Expression)
	if opts == nil || opts.Args["percent"] != true {
		t.Errorf("TOP 100 PERCENT did not record the percentage: %v", opts)
	}
}

func TestCommonTableExpressions(t *testing.T) {
	tree := parse(t, "WITH x AS (SELECT a FROM t) SELECT * FROM x", "")
	with, _ := tree.Args["with_"].(*Expression)
	if with == nil || with.Class != "With" {
		t.Fatalf("no WITH clause on the query: %v", tree.Args["with_"])
	}
	if n := len(with.Args["expressions"].([]*Expression)); n != 1 {
		t.Errorf("%d CTEs, want 1", n)
	}
	// The guard walks the tree for table references; the one inside the CTE
	// has to be reachable or a CTE would hide a table from it.
	names := []string{}
	for _, tbl := range tree.FindAll("Table") {
		names = append(names, tbl.Name())
	}
	if strings.Join(names, ",") != "x,t" {
		t.Errorf("tables reachable from the query: %v, want both x and t", names)
	}

	if n := len(parse(t, "WITH a AS (SELECT 1), b AS (SELECT 2) SELECT 3", "").
		Args["with_"].(*Expression).Args["expressions"].([]*Expression)); n != 2 {
		t.Errorf("%d CTEs, want 2", n)
	}
}

func TestSetOperations(t *testing.T) {
	for _, c := range []struct {
		name, sql, class string
		distinct         bool
	}{
		{"union", "SELECT 1 UNION SELECT 2", "Union", true},
		{"union all", "SELECT 1 UNION ALL SELECT 2", "Union", false},
		{"except", "SELECT 1 EXCEPT SELECT 2", "Except", true},
		{"intersect", "SELECT 1 INTERSECT SELECT 2", "Intersect", true},
		{"union distinct", "SELECT 1 UNION DISTINCT SELECT 2", "Union", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			tree := parse(t, c.sql, "")
			if tree.Class != c.class {
				t.Errorf("ParseOne(%q) = %s, want %s", c.sql, tree.Class, c.class)
			}
			if tree.Args["distinct"] != c.distinct {
				t.Errorf("distinct = %v, want %v", tree.Args["distinct"], c.distinct)
			}
		})
	}

	// A trailing LIMIT belongs to the union, not to the query on its right.
	u := parse(t, "SELECT x FROM t1 UNION ALL SELECT x FROM t2 LIMIT 1", "")
	if u.Args["limit"] == nil {
		t.Error("the union did not take the trailing LIMIT")
	}
	if right := u.Args["expression"].(*Expression); right.Args["limit"] != nil {
		t.Error("the right-hand query kept the LIMIT")
	}

	// A WITH clause in front of a set operation belongs to the whole thing.
	w := parse(t, "WITH a AS (SELECT 1) SELECT 1 UNION SELECT 2", "")
	if w.Class != "Union" || w.Args["with_"] == nil {
		t.Errorf("WITH before a union landed on %s (with_=%v)", w.Class, w.Args["with_"])
	}
}

func TestScalarSubquery(t *testing.T) {
	tree := parse(t, "SELECT (SELECT 1) AS x", "")
	if got := classes(tree); got != "Select Alias Subquery Select Literal Identifier" {
		t.Errorf("got %s", got)
	}
}

// A name after a dot is a name, even when the word is otherwise a value.
func TestKeywordAfterADot(t *testing.T) {
	for _, sql := range []string{"t.null", "t.true", "t.false"} {
		tree := parse(t, sql, "")
		if got := classes(tree); got != "Column Identifier Identifier" {
			t.Errorf("ParseOne(%q) = %s", sql, got)
		}
	}
}

func TestCountKeepsItsFlag(t *testing.T) {
	// COUNT is not a call named COUNT: the reference builds a Count that is
	// always flagged big_int, whatever it was called with. The spec for that
	// comes from running the reference's own builder, not from transcription.
	c := parse(t, "COUNT(a)", "")
	if c.Class != "Count" || c.Args["big_int"] != true {
		t.Errorf("COUNT(a) = %s, big_int=%v", c.Class, c.Args["big_int"])
	}
	if c2 := parse(t, "COUNT(a, b)", ""); len(c2.Args["expressions"].([]*Expression)) != 1 {
		t.Errorf("COUNT(a, b) did not collect its tail: %v", c2.Args["expressions"])
	}
}

// A write is refused for being a write, not for being unreadable. The guard
// above this parser reports "read-only" on the strength of that distinction,
// and the service's conformance suite checks the wording.
func TestWritesAreNamedNotParsed(t *testing.T) {
	for _, c := range []struct{ sql, kind string }{
		{"DROP TABLE dbo.fct_sales", "DROP"},
		{"DELETE FROM dbo.fct_sales", "DELETE"},
		{"UPDATE dbo.fct_sales SET amount_usd = 0", "UPDATE"},
		{"INSERT INTO dbo.fct_sales VALUES (1)", "INSERT"},
		{"TRUNCATE TABLE dbo.fct_sales", "TRUNCATE"},
		{"CREATE TABLE t (a INT)", "CREATE"},
		{"EXEC xp_cmdshell 'dir'", "EXEC"},
	} {
		_, err := ParseOne(c.sql, "tsql")
		if !errors.Is(err, ErrNotAQuery) {
			t.Errorf("ParseOne(%q) failed with %v, want ErrNotAQuery", c.sql, err)
			continue
		}
		var named *NotAQueryError
		if !errors.As(err, &named) || named.Kind != c.kind {
			t.Errorf("ParseOne(%q) did not name the statement: %v", c.sql, err)
		}
	}
}

// Two statements is its own refusal: a guard that permitted the first and
// ignored the rest would be no guard at all.
func TestMoreThanOneStatement(t *testing.T) {
	_, err := ParseOne("SELECT 1; DROP TABLE dbo.fct_sales", "tsql")
	if !errors.Is(err, ErrMultipleStatements) {
		t.Errorf("failed with %v, want ErrMultipleStatements", err)
	}
	// One statement with a trailing semicolon is still one statement.
	if _, err := ParseOne("SELECT 1;", ""); err != nil {
		t.Errorf("a trailing semicolon should be fine: %v", err)
	}
}

// A callable in a FROM clause is a relation the schema check would never see
// if the parser dropped it. `main.read_csv_auto('/etc/passwd')` reads a local
// file through a SELECT; the guard can only refuse what it can see.
func TestTableFunctionsAreVisible(t *testing.T) {
	for _, c := range []struct{ name, sql, dialect string }{
		{"unqualified", "SELECT * FROM read_csv_auto('/etc/passwd')", "duckdb"},
		{"schema-qualified", "SELECT * FROM main.read_csv_auto('/etc/passwd')", "duckdb"},
		{"named by an operator word", "SELECT * FROM main.glob('/**')", "duckdb"},
		{"beside a real table", "SELECT * FROM main.tickets, main.read_csv_auto('/etc/passwd')", "duckdb"},
	} {
		t.Run(c.name, func(t *testing.T) {
			tree := parse(t, c.sql, c.dialect)
			found := false
			for _, tbl := range tree.FindAll("Table") {
				if this := tbl.This(); this != nil && this.Class != "Identifier" {
					found = true
				}
			}
			if !found {
				t.Errorf("no table function in the tree for %q:\n%s", c.sql, tree.DumpJSON())
			}
		})
	}
}

func TestApply(t *testing.T) {
	// Over a subquery: the legitimate form, and the alias belongs to the
	// Lateral rather than to the subquery inside it.
	tree := parse(t, "SELECT * FROM a CROSS APPLY (SELECT 1) t", "tsql")
	joins := tree.FindAll("Join")
	if len(joins) != 1 {
		t.Fatalf("%d joins, want 1", len(joins))
	}
	lat := joins[0].This()
	if lat == nil || lat.Class != "Lateral" {
		t.Fatalf("APPLY produced %v, want a Lateral", lat)
	}
	if lat.Args["cross_apply"] != true {
		t.Error("CROSS APPLY was not flagged cross_apply")
	}
	if lat.Args["alias"] == nil {
		t.Error("the alias did not land on the Lateral")
	}
	if out := parse(t, "SELECT * FROM a OUTER APPLY (SELECT 1) t", "tsql").
		FindAll("Lateral")[0]; out.Args["cross_apply"] != false {
		t.Error("OUTER APPLY should not be flagged cross_apply")
	}

	// Over a function: the form that has been used to get past the guard.
	// It has to parse for the guard to say "function used as a table".
	fn := parse(t, "SELECT * FROM a CROSS APPLY other.f(1)", "tsql")
	if n := len(fn.FindAll("Anonymous")); n != 1 {
		t.Errorf("the applied function is not in the tree: %s", fn.DumpJSON())
	}
}

// SELECT … INTO is a query that writes, and only the tree says so.
func TestSelectInto(t *testing.T) {
	tree := parse(t, "SELECT * INTO dbo.copy FROM dbo.fct_sales", "tsql")
	into, _ := tree.Args["into"].(*Expression)
	if into == nil || into.Class != "Into" {
		t.Fatalf("no INTO clause: %v", tree.Args["into"])
	}
	if got := into.This().Name(); got != "copy" {
		t.Errorf("INTO target is %q, want copy", got)
	}
}

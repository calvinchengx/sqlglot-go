package sqlglot

import "testing"

// TestRewriteBetweenAndDeMorganEndToEnd pins two features landed together
// because the second needs the first to have anything to work on: BETWEEN
// rewritten to a plain range (so every other comparison-range rule in this
// file can see it), and De Morgan's law distributing NOT through a
// parenthesised AND/OR -- which is exactly the shape a negated BETWEEN, and
// a negated DATE_TRUNC EQ/NEQ/IN fold, both produce. Every value here is
// read directly from the reference.
func TestRewriteBetweenAndDeMorganEndToEnd(t *testing.T) {
	for _, tc := range []struct{ name, sql, want string }{
		{"BETWEEN rewrites to a plain range",
			"SELECT * WHERE x BETWEEN 0 AND 5", "SELECT * WHERE x <= 5 AND x >= 0"},
		{"the rewritten range joins an existing AND",
			"SELECT * WHERE x BETWEEN 0 AND 5 AND y = 1",
			"SELECT * WHERE x <= 5 AND x >= 0 AND y = 1"},
		{"a 3-operand chain contradicts across operands the direct pair never compares",
			"SELECT * WHERE x > 1 AND x < 2 AND x > 3", "SELECT * WHERE FALSE"},
		{"BETWEEN joins a tighter existing bound",
			"SELECT * WHERE x BETWEEN 0 AND 5 AND x > 3", "SELECT * WHERE x <= 5 AND x > 3"},
		{"a bare comparison, a flipped comparison, and BETWEEN all compare against each other",
			"SELECT * WHERE x > 3 AND 5 > x AND x BETWEEN 0 AND 10",
			"SELECT * WHERE x < 5 AND x > 3"},
		{"the same, the other direction",
			"SELECT * WHERE x > 3 AND 5 < x AND x BETWEEN 9 AND 10",
			"SELECT * WHERE x <= 10 AND x >= 9"},
		{"NOT BETWEEN rewrites under NOT, keeping its own parens, then De Morgan distributes it",
			"SELECT * WHERE NOT x BETWEEN 0 AND 1", "SELECT * WHERE x < 0 OR x > 1"},
		{"De Morgan distributes NOT through a parenthesised AND",
			"SELECT 1 WHERE NOT (x AND y)", "SELECT 1 WHERE NOT x OR NOT y"},
		{"and through a parenthesised OR",
			"SELECT 1 WHERE NOT (x OR y)", "SELECT 1 WHERE NOT x AND NOT y"},
		{"distributing into two comparisons complements each one instead of leaving a bare NOT",
			"SELECT * WHERE NOT (x > 1 AND x < 5)", "SELECT * WHERE x <= 1 OR x >= 5"},
		{"NOT (NULL) is NULL-shaped the same way a bare NOT NULL is",
			"SELECT * WHERE NOT (NULL)", "SELECT * WHERE NULL AND TRUE"},
		{"a coalesce fold's own NOT distributes once its other branch reduces away",
			"SELECT * WHERE NOT COALESCE(x, 1) = 2", "SELECT * WHERE x <> 2 OR x IS NULL"},
		{"the same, joining an existing AND",
			"SELECT * WHERE NOT COALESCE(x, 1) = 2 AND y = 3",
			"SELECT * WHERE (x <> 2 OR x IS NULL) AND y = 3"},
		{"NOT of a DATE_TRUNC EQ fold distributes into NEQ's own shape",
			"SELECT * WHERE NOT DATE_TRUNC('year', x) = CAST('2021-01-01' AS DATE)",
			"SELECT * WHERE x < CAST('2021-01-01' AS DATE) OR x >= CAST('2022-01-01' AS DATE)"},
		{"and NOT of a NEQ fold into EQ's",
			"SELECT * WHERE NOT DATE_TRUNC('year', x) <> CAST('2021-01-01' AS DATE)",
			"SELECT * WHERE x < CAST('2022-01-01' AS DATE) AND x >= CAST('2021-01-01' AS DATE)"},
		{"NOT of an IN fold distributes through both merged ranges",
			"SELECT * WHERE NOT DATE_TRUNC('year', x) IN (CAST('2021-01-01' AS DATE), CAST('2023-01-01' AS DATE))",
			"SELECT * WHERE (x < CAST('2021-01-01' AS DATE) OR x >= CAST('2022-01-01' AS DATE)) AND (x < CAST('2023-01-01' AS DATE) OR x >= CAST('2024-01-01' AS DATE))"},
		{"negating a quantified comparison flips the quantifier too",
			"SELECT * WHERE NOT (2 <> ALL (SELECT x FROM t) OR y = 1)",
			"SELECT * WHERE 2 = ANY(SELECT x FROM t) AND y <> 1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, "")
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			got, err := Generate(Simplify(e, ""), "")
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("Simplify(%q)\n  want %s\n  got  %s", tc.sql, tc.want, got)
			}
			if _, err := ParseOne(got, ""); err != nil {
				t.Fatalf("the fold's own output %q does not parse back: %v", got, err)
			}
		})
	}
}

func TestRewriteBetweenDeclinesIncompleteNode(t *testing.T) {
	// Between with a missing arg cannot happen from the parser, but
	// rewriteBetween is a plain function: it should decline rather than
	// panic on a hand-built node missing one.
	e := New("Between", Arg{"this", New("Column", Arg{"this", New("Identifier", Arg{"this", "x"})})})
	if got := rewriteBetween(e, nil); got != e {
		t.Errorf("rewriteBetween on an incomplete node should return it unchanged")
	}
}

func TestNegateComplementsAComparison(t *testing.T) {
	lt := New("LT",
		Arg{"this", New("Column", Arg{"this", New("Identifier", Arg{"this", "x"})})},
		Arg{"expression", New("Literal", Arg{"this", "1"})})
	got := negate(lt, "")
	if got.Class != "GTE" {
		t.Errorf("negate(LT) = %s, want GTE", got.Class)
	}
}

func TestNegateCancelsADoubleNegation(t *testing.T) {
	is := New("Is",
		Arg{"this", New("Column", Arg{"this", New("Identifier", Arg{"this", "x"})})},
		Arg{"expression", New("Null")})
	not := New("Not", Arg{"this", is})
	got := negate(not, "")
	if got != is {
		t.Errorf("negate(Not(Is)) should cancel to the bare Is, got %s", got.Class)
	}
}

func TestNegateDistributesIntoAnOrOperandDirectly(t *testing.T) {
	x := New("Column", Arg{"this", New("Identifier", Arg{"this", "x"})})
	y := New("Column", Arg{"this", New("Identifier", Arg{"this", "y"})})
	or := New("Or", Arg{"this", x}, Arg{"expression", y})
	got := negate(or, "")
	if got.Class != "Paren" {
		t.Fatalf("negate(Or) = %s, want Paren", got.Class)
	}
	inner := childOf(got, "this")
	if inner.Class != "And" {
		t.Errorf("negate(Or)'s own Paren wraps %s, want And", inner.Class)
	}
}

func TestNegateFallsBackToABareNotForAnUnrecognisedShape(t *testing.T) {
	col := New("Column", Arg{"this", New("Identifier", Arg{"this", "x"})})
	got := negate(col, "")
	if got.Class != "Not" {
		t.Errorf("negate(Column) = %s, want Not", got.Class)
	}
}

func TestFlatMergeReturnsNilWhenNothingCombines(t *testing.T) {
	a := New("Column", Arg{"this", New("Identifier", Arg{"this", "a"})})
	b := New("Column", Arg{"this", New("Identifier", Arg{"this", "b"})})
	got := flatMerge([]*Expression{a, b}, "And", func(x, y *Expression) *Expression { return nil })
	if got != nil {
		t.Errorf("flatMerge with a merge func that never combines should return nil, got %v", got)
	}
}

func TestFlatFoldComparisonsDeclinesUnderTwoOperands(t *testing.T) {
	e, err := ParseOne("SELECT * WHERE x > 1", "")
	if err != nil {
		t.Fatal(err)
	}
	gt := e.FindAll("GT")[0]
	if got := flatFoldComparisons(gt); got != nil {
		t.Errorf("flatFoldComparisons on a single comparison (not even a chain) should return nil")
	}
}

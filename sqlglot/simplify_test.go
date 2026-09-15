package sqlglot

import (
	"strings"
	"testing"
	"time"
)

// TestSimplifyShapes pins the rules by example, so a regression names itself
// rather than showing up as a number moving in the contract harness.
func TestSimplifyShapes(t *testing.T) {
	for _, c := range []struct{ name, sql, dialect, want string }{
		{"and with true", "SELECT 1 WHERE x AND TRUE", "", "SELECT 1 WHERE x AND TRUE"},
		{"and with false", "SELECT 1 WHERE x AND FALSE", "", "SELECT 1 WHERE FALSE"},
		{"or with true", "SELECT 1 WHERE x OR TRUE", "", "SELECT 1"},
		// FILTER's own WHERE is not optional -- there is no such thing as a
		// bare FILTER() -- so this is one of the few places the port declines
		// to reproduce the reference, which writes exactly that. See
		// docs/upstream-issues.md.
		{"a FILTER's own WHERE TRUE is kept, not dropped",
			"SELECT AVG(x) FILTER (WHERE TRUE) FROM t", "duckdb",
			"SELECT AVG(x) FILTER(WHERE TRUE) FROM t"},
		{"not true", "SELECT 1 WHERE NOT TRUE", "", "SELECT 1 WHERE FALSE"},
		{"not equal folds to complement", "SELECT 1 WHERE NOT x = 1", "", "SELECT 1 WHERE x <> 1"},
		{"literal arithmetic", "SELECT 1 + 1", "", "SELECT 2"},
		{"a folded negative is a Neg, not a literal", "SELECT 1 - 2", "", "SELECT -1"},
		{"integer division is left alone", "SELECT 3 / 2", "", "SELECT 3 / 2"},
		{"literal comparison", "SELECT 1 WHERE 2 > 2.5", "", "SELECT 1 WHERE FALSE"},
		{"string comparison", "SELECT 1 WHERE 'x' = 'y'", "", "SELECT 1 WHERE FALSE"},
		{"string ordering", "SELECT 1 WHERE 'a' < 'b'", "", "SELECT 1"},
		{"string ordering the other way", "SELECT 1 WHERE 'b' <= 'a'", "", "SELECT 1 WHERE FALSE"},
		{"string inequality", "SELECT 1 WHERE 'a' <> 'b'", "", "SELECT 1"},
		{"string greater or equal", "SELECT 1 WHERE 'b' >= 'b'", "", "SELECT 1"},
		{"string greater", "SELECT 1 WHERE 'b' > 'a'", "", "SELECT 1"},
		{"numeric inequality", "SELECT 1 WHERE 1 <> 2", "", "SELECT 1"},
		{"numeric greater or equal", "SELECT 1 WHERE 2 >= 2", "", "SELECT 1"},
		{"numeric less or equal", "SELECT 1 WHERE 2 <= 1", "", "SELECT 1 WHERE FALSE"},
		{"multiplication", "SELECT 2 * 3", "", "SELECT 6"},
		{"null is not null over a constant", "SELECT 1 WHERE NULL IS NOT NULL", "",
			"SELECT 1 WHERE FALSE"},
		{"a column's nullness is not knowable", "SELECT 1 WHERE x IS NULL", "",
			"SELECT 1 WHERE x IS NULL"},
		{"division by zero is left alone", "SELECT 1.0 / 0", "", "SELECT 1.0 / 0"},
		// A Neg over a literal counts as a number, which is what lets the
		// subscript shift fold itself back: reading a[0] gives Neg(1), and
		// writing it asks for Neg(1) + 1.
		{"a negative literal folds", "SELECT -1 + 1", "", "SELECT 0"},
		{"and subtracts", "SELECT -1 - 1", "", "SELECT -2"},
		{"and multiplies", "SELECT -2 * 3", "", "SELECT -6"},
		{"a negative compares", "SELECT 1 WHERE -1 < 0", "", "SELECT 1"},
		// Two comparisons of the same value decide each other.
		{"contradictory range", "SELECT 1 WHERE x > 1 AND x < 1", "", "SELECT 1 WHERE FALSE"},
		{"equality against a range", "SELECT 1 WHERE x = 1 AND x >= 2", "",
			"SELECT 1 WHERE FALSE"},
		{"the tighter bound wins under AND", "SELECT 1 WHERE x < 1 AND x < 2", "",
			"SELECT 1 WHERE x < 1"},
		{"the looser bound wins under OR", "SELECT 1 WHERE x < 1 OR x < 2", "",
			"SELECT 1 WHERE x < 2"},
		{"a satisfiable range is left alone", "SELECT 1 WHERE x > 1 AND x < 5", "",
			"SELECT 1 WHERE x < 5 AND x > 1"},
		// Never TRUE: the shared operand may be NULL, so an OR of complements
		// is not a tautology.
		{"complementary bounds under OR are not TRUE", "SELECT 1 WHERE x > 1 OR x <= 1", "",
			"SELECT 1 WHERE x <= 1 OR x > 1"},
		{"two different columns decide nothing", "SELECT 1 WHERE x > 1 AND y < 1", "",
			"SELECT 1 WHERE x > 1 AND y < 1"},
		// A widening cast of a byte-sized integer cannot change the value, so
		// a comparison sees through it.
		{"a widening cast is transparent", "SELECT 1 WHERE CAST(1 AS BIGINT) >= 0", "",
			"SELECT 1"},
		{"including unsigned", "SELECT 1 WHERE CAST(1 AS UINT) >= 0", "", "SELECT 1"},
		{"and nested casts", "SELECT 1 WHERE CAST(CAST(-1 AS INT) AS INT) = -1", "",
			"SELECT 1"},
		// A negative does not fit an unsigned type, so that cast stays.
		{"a negative into unsigned keeps its cast", "SELECT 1 WHERE CAST(-1 AS UINT) = -1", "",
			"SELECT 1 WHERE CAST(-1 AS UINT) = -1"},
		// Outside a byte the cast could overflow, and what an engine does
		// then is its own business.
		{"a large value keeps its cast", "SELECT 1 WHERE CAST(300 AS TINYINT) = 300", "",
			"SELECT 1 WHERE CAST(300 AS TINYINT) = 300"},
		{"a non-integer cast is untouched", "SELECT 1 WHERE CAST(1 AS TEXT) = '1'", "",
			"SELECT 1 WHERE CAST(1 AS TEXT) = '1'"},
		{"a cast of a non-literal keeps its cast", "SELECT 1 WHERE CAST(x AS INT) = 1", "",
			"SELECT 1 WHERE CAST(x AS INT) = 1"},
		{"an IS over a column is left alone", "SELECT 1 WHERE x IS NOT NULL", "",
			"SELECT 1 WHERE NOT x IS NULL"},
		// PostgreSQL records the negation ON the Is node, and a negated Is is
		// the one comparison the range rule refuses to reason about.
		{"a negated IS blocks the range rule", "SELECT 1 WHERE x > 1 AND x IS NOT NULL",
			"postgres", "SELECT 1 WHERE x > 1 AND x IS NOT NULL"},
		{"a random operand is not the same value twice",
			"SELECT 1 WHERE RAND() > 1 AND RAND() < 1", "duckdb",
			"SELECT 1 WHERE RANDOM() < 1 AND RANDOM() > 1"},
		{"is null over a constant", "SELECT 1 WHERE 1 IS NULL", "", "SELECT 1 WHERE FALSE"},
		{"absorption", "SELECT 1 WHERE a AND (a OR b)", "", "SELECT 1 WHERE a AND TRUE"},
		{"absorption the other way", "SELECT 1 WHERE a OR (a AND b)", "", "SELECT 1 WHERE a AND TRUE"},
		// `x AND TRUE` is the reference's own way of keeping a value in
		// CONDITION position when it stands alone -- but once it is an
		// operand of a connector one level up, that connector IS the
		// predicate, and the value goes back bare rather than wrapped again.
		{"a fold to a value stays bare as an operand of its parent connector",
			"SELECT 1 WHERE y = 1 AND (x AND x)", "", "SELECT 1 WHERE x AND y = 1"},
		{"a fold to NULL stays bare as an operand of its parent connector too",
			"SELECT 1 WHERE x = 1 AND (1 AND NULL)", "", "SELECT 1 WHERE NULL AND x = 1"},
		// Every operand of the parenthesised OR drops against a sibling IS
		// NULL it negates, leaving nothing of it behind. The reference folds
		// this all the way to FALSE; the port stops one step short of that,
		// which still means what the statement meant.
		{"every operand of the absorbed side drops",
			"SELECT 1 WHERE x IS NULL AND y IS NULL AND (NOT x IS NULL OR NOT y IS NULL)", "",
			"SELECT 1 WHERE x IS NULL AND y IS NULL"},
		// Two operands are left of the absorbed side rather than none or one,
		// and what is left of it is the OPPOSITE connector to the chain it
		// rejoins: spliced in bare, an Or inside an And reads back with AND
		// binding tighter, which is a different statement than the one
		// meant. The parentheses this needs are not optional. uniqSort then
		// sorts the rebuilt chain by each operand's own generated SQL with
		// its OWN parentheses stripped for the comparison, so "(w OR z)"
		// sorts as "w OR z" would -- before "x IS NULL", not after it.
		{"what is left of the absorbed side keeps its own parentheses",
			"SELECT 1 WHERE x IS NULL AND y IS NULL AND (NOT x IS NULL OR NOT y IS NULL OR z OR w)", "",
			"SELECT 1 WHERE (w OR z) AND x IS NULL AND y IS NULL"},
		// The one that matters most: AND binds tighter than OR, so these
		// parentheses carry meaning and dropping them re-associates the
		// statement into (a AND a) OR b.
		{"parentheses that carry precedence stay", "SELECT 1 WHERE a AND (b OR c)", "",
			"SELECT 1 WHERE a AND (b OR c)"},
		{"parentheses that do not are dropped", "SELECT 1 WHERE a AND (b AND c)", "",
			"SELECT 1 WHERE a AND b AND c"},
		// Add/Add and Mul/Mul are associative, so a grouping around either
		// side carries no meaning once its own contents are done folding.
		{"nested Add parens fold across", "SELECT x + (1 + 2)", "", "SELECT x + 3"},
		{"nested Add parens fold across the other side", "SELECT (x + 1) + 2", "", "SELECT x + 3"},
		{"nested Mul parens fold across", "SELECT (x * 2) * 4", "", "SELECT x * 8"},
		// Mul binds tighter than Add or Sub, so its own parentheses are
		// always redundant there, whatever is inside them.
		{"Mul parens are redundant under Add", "SELECT a + (b * c)", "", "SELECT a + b * c"},
		{"Mul parens are redundant under Sub", "SELECT a - (b * c)", "", "SELECT a - b * c"},
		// A bare literal has no operator of its own, so parentheses around
		// one are always redundant under arithmetic too, the same as under
		// a connector.
		{"a literal's parens are always redundant", "SELECT x * (5)", "", "SELECT x * 5"},
		{"a column's parens are always redundant too", "SELECT x + (y)", "", "SELECT x + y"},
		{"deeply nested column parens all drop",
			"SELECT 1 WHERE (((((A) AND B)) AND C)) AND D", "",
			"SELECT 1 WHERE A AND B AND C AND D"},
		// A Predicate is NOT atomic to an arithmetic parent the way it is to
		// a Connector: `<` binds LOOSER than `-`, so `a - (b < c)` dropped to
		// `a - b < c` would read back as `(a - b) < c`, a different
		// statement. The arithmetic switch never reaches the Predicate case
		// the Connector one uses, on purpose.
		{"a predicate keeps its parens under Sub", "SELECT a - (b < c)", "", "SELECT a - (b < c)"},
		// De Morgan is the reference's own way through this -- `NOT (x AND
		// y)` becomes `NOT x OR NOT y` there -- and is not ported, so the port
		// keeps the parentheses NOT needs around a connector of a different
		// class rather than rewrite the structure.
		{"a connector under NOT keeps its parentheses", "SELECT 1 WHERE NOT (x AND y)", "",
			"SELECT 1 WHERE NOT (x AND y)"},
		// `NOT NULL` is itself NULL AND TRUE-shaped once the inner NOT has
		// already run, and the outer NOT has to see through that wrapper the
		// same way it would see a bare NULL, or the double negation never
		// re-collapses.
		{"double negation of NULL collapses through its own wrapper",
			"SELECT 1 WHERE NOT NOT NULL", "", "SELECT 1 WHERE NULL AND TRUE"},
		{"double negation of NULL stays bare under a connector",
			"SELECT 1 WHERE x = 1 OR NOT NOT NULL", "", "SELECT 1 WHERE NULL OR x = 1"},
		// Double negation of a KNOWN boolean -- a comparison -- collapses,
		// where the dialect says the elimination is safe.
		{"double negation of a comparison collapses",
			"SELECT 1 WHERE NOT NOT (a = b)", "", "SELECT 1 WHERE a = b"},
		{"coalesce against a constant that the fallback cannot satisfy",
			"SELECT 1 WHERE COALESCE(x, 1) = 2", "",
			"SELECT 1 WHERE NOT x IS NULL AND x = 2"},
		{"coalesce against a constant the fallback can satisfy",
			"SELECT 1 WHERE COALESCE(x, 1) = 1", "",
			"SELECT 1 WHERE x = 1 OR x IS NULL"},
		// Verified against the reference exactly, now that the AND's operands
		// are sorted by their own generated SQL rather than pinned in the
		// port's own construction order.
		{"coalesce with more than one argument before the constant",
			"SELECT 1 WHERE COALESCE(x, y, 1) = 2", "",
			"SELECT 1 WHERE COALESCE(x, y) = 2 AND NOT COALESCE(x, y) IS NULL"},
		{"coalesce of a lone argument is the argument",
			"SELECT COALESCE(x)", "", "SELECT x"},
		{"coalesce of a non-null constant is that constant",
			"SELECT COALESCE(1, 2)", "", "SELECT 1"},
		{"coalesce that can still be null is left as a comparison of null",
			"SELECT 1 WHERE COALESCE(x, 1) IS NULL", "",
			"SELECT 1 WHERE FALSE"},
		{"equality moves a constant across addition",
			"SELECT 1 WHERE x + 1 = 3", "", "SELECT 1 WHERE x = 2"},
		{"and across a subtraction",
			"SELECT 1 WHERE x - 1 = 3", "", "SELECT 1 WHERE x = 4"},
		{"a constant minus a column inverts the comparison",
			"SELECT 1 WHERE 5 - x > 2", "", "SELECT 1 WHERE x < 3"},
		{"a negated constant still sorts to the right",
			"SELECT 1 WHERE -500 <= CAST(x AS INT)", "",
			"SELECT 1 WHERE CAST(x AS INT) >= -500"},
		{"double negation of a number folds",
			"SELECT 1 WHERE CAST(x AS INT) >= - -500", "",
			"SELECT 1 WHERE CAST(x AS INT) >= 500"},
		{"startswith of two literals", "SELECT STARTS_WITH('foo', 'f')", "", "SELECT TRUE"},
		{"startswith that does not match", "SELECT STARTS_WITH('foo', 'g')", "", "SELECT FALSE"},
	} {
		t.Run(c.name, func(t *testing.T) {
			tree, err := ParseOne(c.sql, c.dialect)
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", c.sql, err)
			}
			got, err := Generate(Simplify(tree, c.dialect), c.dialect)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != c.want {
				t.Errorf("Simplify(%q)\n  want %s\n  got  %s", c.sql, c.want, got)
			}
		})
	}
}

// TestFlatFold covers folding constants out of a chain of one associative
// operator, wherever in the chain they sit -- and NOT out of a chain that
// mixes in an operator which is not associative.
func TestFlatFold(t *testing.T) {
	for _, tc := range []struct{ name, sql, want string }{
		{"a sum folds past a column", "y + 2 + 3", "y + 5"},
		{"and so does a product", "y * 2 * 3", "y * 6"},
		{"the offset a subscript carries folds itself out", "y + -1 + 1", "y + 0"},
		// The folded value goes back to the FRONT of the chain, which is
		// where the reference puts it: `1 + y + 2` is `3 + y`, not `y + 3`.
		{"constants at either end of the chain", "1 + y + 2", "3 + y"},
		{"and constants in front of it", "1 + 2 + y", "3 + y"},
		{"a chain of columns is left as it was", "y + z + w", "y + z + w"},
		{"a subtraction in the chain stops it", "y - 2 + 3", "y - 2 + 3"},
		{"and a division does too", "y / 2 * 3", "y / 2 * 3"},
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
				t.Errorf("%q simplified to %q, want %q", tc.sql, got, tc.want)
			}
		})
	}
}

// TestDeepChainDoesNotBlowUp guards the cost of simplifying a long chain of
// one operator, which is what a subscript's index can be.
//
// simplifyNode used to copy each node's whole subtree and then replace every
// child of the copy -- n^2 nodes copied per pass, up to 32 passes. Two
// thousand terms took over three seconds to parse, nearly all of it garbage
// collection, and the generator fuzzer found it as a worker that stopped
// responding rather than as a wrong answer.
//
// The annotator was the other half of the same cost, and for the same reason:
// a binary operator worked its type out by annotating both operands again,
// however deep. Together the two took a 2000-term index from over three
// seconds to under ten milliseconds.
//
// The bound is loose on purpose: the point is the SHAPE of the cost, and
// either regression is a hundred times slower than this on any machine that
// can run the rest of the suite.
func TestDeepChainDoesNotBlowUp(t *testing.T) {
	// PostgreSQL numbers subscripts from 1, so reading one shifts the index
	// -- which is what puts the simplifier in front of the whole chain.
	sql := "A[" + strings.Repeat("0*", 2000) + "0]"
	start := time.Now()
	if _, err := ParseOne(sql, "postgres"); err != nil {
		t.Fatalf("ParseOne of a 2000-term index: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("a 2000-term index took %v to parse", elapsed)
	}
}

// A number too big to be a float is not one this can write down.
//
// `1E70 * 1E300` overflows, and folding it produced the literal `+Inf.0` --
// SQL nothing could read, including this port. The reference declines to fold
// these too. The generator fuzzer found it.
func TestArithmeticThatOverflows(t *testing.T) {
	for _, sql := range []string{
		"SELECT 1E70 * 1E300",
		"SELECT 1E300 + 1E300 + 1E300 + 1E300 + 1E300 + 1E300 + 1E300 + 1E300 + 1E300 + 1E300",
		"SELECT -1E70 * 1E300",
		"SELECT 1E308 / 1E-10",
	} {
		e, err := ParseOne(sql, "duckdb")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", sql, err)
		}
		got, err := Generate(Simplify(e, "duckdb"), "duckdb")
		if err != nil {
			t.Fatalf("Generate(%q): %v", sql, err)
		}
		if strings.Contains(got, "Inf") || strings.Contains(got, "NaN") {
			t.Errorf("%q folded to %q, which is not a number any engine reads", sql, got)
		}
		// And whatever it wrote, the port can read it back.
		if _, err := ParseOne(got, "duckdb"); err != nil {
			t.Errorf("%q wrote %q, which reads back as: %v", sql, got, err)
		}
	}
}

// TestSimplifyAlwaysTrueJoin covers the corners of turning a JOIN whose ON is
// always true into a CROSS JOIN: the gates that keep it from firing (a USING
// clause, a join method, a side/kind the reference does not special-case),
// as well as the successful rewrite in more than one of its shapes.
func TestSimplifyAlwaysTrueJoin(t *testing.T) {
	for _, tc := range []struct{ name, sql, dialect, want string }{
		{"a plain JOIN becomes CROSS", "SELECT x FROM y JOIN z ON TRUE", "",
			"SELECT x FROM y CROSS JOIN z"},
		{"an explicit INNER becomes CROSS", "SELECT x FROM y INNER JOIN z ON TRUE", "",
			"SELECT x FROM y CROSS JOIN z"},
		{"RIGHT becomes CROSS", "SELECT x FROM y RIGHT JOIN z ON TRUE", "",
			"SELECT x FROM y CROSS JOIN z"},
		{"RIGHT OUTER becomes CROSS", "SELECT x FROM y RIGHT OUTER JOIN z ON TRUE", "",
			"SELECT x FROM y CROSS JOIN z"},
		// LEFT keeps rows that fail to match, which CROSS does not, so it is
		// not in the set the reference rewrites.
		{"LEFT is left alone", "SELECT x FROM y LEFT JOIN z ON TRUE", "",
			"SELECT x FROM y LEFT JOIN z ON TRUE"},
		{"FULL is left alone", "SELECT x FROM y FULL JOIN z ON TRUE", "",
			"SELECT x FROM y FULL JOIN z ON TRUE"},
		// ASOF matches by nearness rather than by the ON condition, so the
		// condition being always-true does not make it a CROSS.
		{"a join method blocks the rewrite", "SELECT x FROM y ASOF JOIN z ON TRUE", "duckdb",
			"SELECT x FROM y ASOF JOIN z ON TRUE"},
		// An ON that is not always true never reaches the rewrite at all.
		{"a real condition is untouched", "SELECT x FROM y JOIN z ON y.a = z.a", "",
			"SELECT x FROM y JOIN z ON y.a = z.a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, tc.dialect)
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			got, err := Generate(Simplify(e, tc.dialect), tc.dialect)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("Simplify(%q)\n  want %s\n  got  %s", tc.sql, tc.want, got)
			}
		})
	}
	// USING names the columns to match on instead of an ON condition, so
	// there is no ON to have been always true in the first place -- covered
	// separately since it takes a different grammar shape than the cases
	// above (no ON at all, rather than one that folds to TRUE).
	e, err := ParseOne("SELECT x FROM y JOIN z USING (a)", "")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	got, err := Generate(Simplify(e, ""), "")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if want := "SELECT x FROM y JOIN z USING (a)"; got != want {
		t.Errorf("Simplify(JOIN USING)\n  want %s\n  got  %s", want, got)
	}
}

// TestSimplifyConcat covers folding adjacent string literals in CONCAT,
// CONCAT_WS, and || -- both the merges and the gates that keep one from
// happening: a non-literal CONCAT_WS separator, and an already-alternating
// argument list with nothing adjacent to join.
func TestSimplifyConcat(t *testing.T) {
	for _, tc := range []struct{ name, sql, want string }{
		{"CONCAT of all literals is one literal",
			"SELECT CONCAT('a', 'b', 'c')", "SELECT 'abc'"},
		{"CONCAT_WS of all literals joins with the separator and drops the call",
			"SELECT CONCAT_WS('-', 'a', 'b', 'c')", "SELECT 'a-b-c'"},
		{"CONCAT merges only the adjacent runs, columns stay where they were",
			"SELECT CONCAT('a', x, y, 'b', 'c')", "SELECT CONCAT('a', x, y, 'bc')"},
		{"CONCAT_WS merges only the adjacent runs, the separator stays first",
			"SELECT CONCAT_WS('-', 'a', x, y, 'b', 'c')",
			"SELECT CONCAT_WS('-', 'a', x, y, 'b-c')"},
		{"|| of two literals is one literal", "SELECT 'a' || 'b'", "SELECT 'ab'"},
		{"|| merges its own leading run and keeps the rest of the chain",
			"SELECT 'a' || 'b' || x", "SELECT 'ab' || x"},
		{"|| merges a trailing run too", "SELECT x || 'a' || 'b'", "SELECT x || 'ab'"},
		{"a lone CONCAT_WS argument needs no merge to still go bare",
			"SELECT CONCAT_WS('-', 'a')", "SELECT 'a'"},
		// Nothing adjacent to merge: every argument is left exactly as it
		// was, and the call itself is untouched rather than rebuilt for no
		// reason.
		{"CONCAT with no two literals together is untouched",
			"SELECT CONCAT(x, 'a', y, 'b')", "SELECT CONCAT(x, 'a', y, 'b')"},
		{"CONCAT of only columns is untouched", "SELECT CONCAT(x, y)", "SELECT CONCAT(x, y)"},
		// CONCAT_WS cannot join anything without knowing the separator, so a
		// non-literal one blocks the rewrite entirely -- even the literal
		// arguments stay unjoined.
		{"a non-literal CONCAT_WS separator blocks the rewrite",
			"SELECT CONCAT_WS(x, 'a', 'b')", "SELECT CONCAT_WS(x, 'a', 'b')"},
		// A middle run merges and leaves a THIRD result operand on each
		// side of it, so the || rebuild has to run its own loop more than
		// once rather than just join two things back together.
		{"|| merges more than one separate run in the same chain",
			"SELECT 'a' || 'b' || x || 'c' || 'd'", "SELECT 'ab' || x || 'cd'"},
		// The port reads this where the reference refuses it outright
		// (`CONCAT` needs at least one argument there) -- an existing,
		// separate leniency, not something this fold introduces. What
		// matters here is that an empty argument list does not crash it.
		{"CONCAT with no arguments is left alone", "SELECT CONCAT()", "SELECT CONCAT()"},
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
		})
	}
}

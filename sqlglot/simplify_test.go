package sqlglot

import "testing"

// TestSimplifyShapes pins the rules by example, so a regression names itself
// rather than showing up as a number moving in the contract harness.
func TestSimplifyShapes(t *testing.T) {
	for _, c := range []struct{ name, sql, dialect, want string }{
		{"and with true", "SELECT 1 WHERE x AND TRUE", "", "SELECT 1 WHERE x AND TRUE"},
		{"and with false", "SELECT 1 WHERE x AND FALSE", "", "SELECT 1 WHERE FALSE"},
		{"or with true", "SELECT 1 WHERE x OR TRUE", "", "SELECT 1 WHERE TRUE"},
		{"not true", "SELECT 1 WHERE NOT TRUE", "", "SELECT 1 WHERE FALSE"},
		{"not equal folds to complement", "SELECT 1 WHERE NOT x = 1", "", "SELECT 1 WHERE x <> 1"},
		{"literal arithmetic", "SELECT 1 + 1", "", "SELECT 2"},
		{"a folded negative is a Neg, not a literal", "SELECT 1 - 2", "", "SELECT -1"},
		{"integer division is left alone", "SELECT 3 / 2", "", "SELECT 3 / 2"},
		{"literal comparison", "SELECT 1 WHERE 2 > 2.5", "", "SELECT 1 WHERE FALSE"},
		{"string comparison", "SELECT 1 WHERE 'x' = 'y'", "", "SELECT 1 WHERE FALSE"},
		{"string ordering", "SELECT 1 WHERE 'a' < 'b'", "", "SELECT 1 WHERE TRUE"},
		{"string ordering the other way", "SELECT 1 WHERE 'b' <= 'a'", "", "SELECT 1 WHERE FALSE"},
		{"string inequality", "SELECT 1 WHERE 'a' <> 'b'", "", "SELECT 1 WHERE TRUE"},
		{"string greater or equal", "SELECT 1 WHERE 'b' >= 'b'", "", "SELECT 1 WHERE TRUE"},
		{"string greater", "SELECT 1 WHERE 'b' > 'a'", "", "SELECT 1 WHERE TRUE"},
		{"numeric inequality", "SELECT 1 WHERE 1 <> 2", "", "SELECT 1 WHERE TRUE"},
		{"numeric greater or equal", "SELECT 1 WHERE 2 >= 2", "", "SELECT 1 WHERE TRUE"},
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
		{"a negative compares", "SELECT 1 WHERE -1 < 0", "", "SELECT 1 WHERE TRUE"},
		// Two comparisons of the same value decide each other.
		{"contradictory range", "SELECT 1 WHERE x > 1 AND x < 1", "", "SELECT 1 WHERE FALSE"},
		{"equality against a range", "SELECT 1 WHERE x = 1 AND x >= 2", "",
			"SELECT 1 WHERE FALSE"},
		{"the tighter bound wins under AND", "SELECT 1 WHERE x < 1 AND x < 2", "",
			"SELECT 1 WHERE x < 1"},
		{"the looser bound wins under OR", "SELECT 1 WHERE x < 1 OR x < 2", "",
			"SELECT 1 WHERE x < 2"},
		{"a satisfiable range is left alone", "SELECT 1 WHERE x > 1 AND x < 5", "",
			"SELECT 1 WHERE x > 1 AND x < 5"},
		// Never TRUE: the shared operand may be NULL, so an OR of complements
		// is not a tautology.
		{"complementary bounds under OR are not TRUE", "SELECT 1 WHERE x > 1 OR x <= 1", "",
			"SELECT 1 WHERE x > 1 OR x <= 1"},
		{"two different columns decide nothing", "SELECT 1 WHERE x > 1 AND y < 1", "",
			"SELECT 1 WHERE x > 1 AND y < 1"},
		// A widening cast of a byte-sized integer cannot change the value, so
		// a comparison sees through it.
		{"a widening cast is transparent", "SELECT 1 WHERE CAST(1 AS BIGINT) >= 0", "",
			"SELECT 1 WHERE TRUE"},
		{"including unsigned", "SELECT 1 WHERE CAST(1 AS UINT) >= 0", "", "SELECT 1 WHERE TRUE"},
		{"and nested casts", "SELECT 1 WHERE CAST(CAST(-1 AS INT) AS INT) = -1", "",
			"SELECT 1 WHERE TRUE"},
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
			"SELECT 1 WHERE RANDOM() > 1 AND RANDOM() < 1"},
		{"is null over a constant", "SELECT 1 WHERE 1 IS NULL", "", "SELECT 1 WHERE FALSE"},
		{"absorption", "SELECT 1 WHERE a AND (a OR b)", "", "SELECT 1 WHERE a AND TRUE"},
		{"absorption the other way", "SELECT 1 WHERE a OR (a AND b)", "", "SELECT 1 WHERE a AND TRUE"},
		// The one that matters most: AND binds tighter than OR, so these
		// parentheses carry meaning and dropping them re-associates the
		// statement into (a AND a) OR b.
		{"parentheses that carry precedence stay", "SELECT 1 WHERE a AND (b OR c)", "",
			"SELECT 1 WHERE a AND (b OR c)"},
		{"parentheses that do not are dropped", "SELECT 1 WHERE a AND (b AND c)", "",
			"SELECT 1 WHERE a AND b AND c"},
		{"a connector under NOT keeps its parentheses", "SELECT 1 WHERE NOT NOT NULL", "",
			"SELECT 1 WHERE NOT (NULL AND TRUE)"},
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

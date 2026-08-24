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

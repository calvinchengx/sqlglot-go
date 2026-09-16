package sqlglot

import "testing"

// TestIsKnownNonnullBinaryOfConstants extends isKnownNonnull -- already
// covering IS, a literal, a boolean, and NOT of one of those -- to a Binary
// operator whose BOTH operands are non-null constants: `'abc' ~ 'a'` can
// never itself be NULL, since nothing in it could produce one, so its
// complement removes the same way `x IS NULL AND NOT x IS NULL` already
// does. Every value here is read directly from the reference.
func TestIsKnownNonnullBinaryOfConstants(t *testing.T) {
	for _, tc := range []struct{ name, sql, want string }{
		{"a REGEXP between two literals contradicts its own negation",
			"'abc' ~ 'a' AND NOT 'abc' ~ 'a'", "FALSE"},
		{"and tautologises with OR",
			"'abc' ~ 'a' OR NOT 'abc' ~ 'a'", "TRUE"},
		{"a REGEXP against a COLUMN is not known non-null, so nothing folds",
			"x ~ 'a' AND NOT x ~ 'a'", "NOT x ~ 'a' AND x ~ 'a'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, "postgres")
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			got, err := Generate(Simplify(e, "postgres"), "postgres")
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("Simplify(%q)\n  want %s\n  got  %s", tc.sql, tc.want, got)
			}
			if _, err := ParseOne(got, "postgres"); err != nil {
				t.Fatalf("the fold's own output %q does not parse back: %v", got, err)
			}
		})
	}
}

func TestIsKnownNonnullBinary(t *testing.T) {
	abc := New("Literal", Arg{"this", "abc"}, Arg{"is_string", true})
	a := New("Literal", Arg{"this", "a"}, Arg{"is_string", true})
	col := New("Column", Arg{"this", New("Identifier", Arg{"this", "x"})})

	litRegexp := New("RegexpLike", Arg{"this", abc}, Arg{"expression", a})
	if !isKnownNonnull(litRegexp) {
		t.Errorf("isKnownNonnull(RegexpLike(literal, literal)) = false, want true")
	}

	colRegexp := New("RegexpLike", Arg{"this", col}, Arg{"expression", a})
	if isKnownNonnull(colRegexp) {
		t.Errorf("isKnownNonnull(RegexpLike(column, literal)) = true, want false")
	}

	if isKnownNonnull(col) {
		t.Errorf("isKnownNonnull(Column) = true, want false")
	}
}

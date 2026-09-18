package sqlglot

import "testing"

// TestAnonymizeEdgeCases covers what the identity/dialect corpus never
// exercises: it is all valid SQL, so it never runs anonymizeTokenize's own
// error-recovery path, and it rarely (if ever) carries a hint. Lifted from
// the reference's own tests/test_anonymize.py, which is the oracle here the
// same as everywhere else in this port.
func TestAnonymizeEdgeCases(t *testing.T) {
	for _, tc := range []struct{ sql, want, dialect string }{
		// A hint's body is blanked whether the tokenizer reads the whole
		// thing as a HINT token or leaves it as a comment in a gap.
		{"SELECT /*+ INDEX(customers ssn_idx) */ a FROM t",
			"SELECT /*+ ............... ........ */ a FROM b", ""},
		{"SELECT a /*+ INDEX(secret) */ b", "SELECT a /*+ ............. */ b", ""},
		{"SELECT /*+ INDEX(customers)\n           MORE(ssn) */ a FROM t",
			"SELECT /*+ ................\n           ......... */ a FROM b", ""},
		// A hint's body lives in Token.comments, not a gap of its own --
		// pairing gap markers with recorded bodies positionally would drift
		// once a hint (no gap marker) is followed by a real comment.
		{"SELECT /*+ x */ a -- password is hunter2\nFROM t",
			"SELECT /*+ . */ a -- ........ .. .......\nFROM b", ""},
		// A comment with nothing for the tokenizer to attach it to.
		{"-- top secret", "-- ... ......", ""},
		{"/* COMMENT */", "/* ....... */", ""},
		{"/*", "/*", ""},
		// The first two characters of an unreadable remainder survive, so
		// the delimiter the tokenizer choked on is still identifiable.
		{"SELECT a, 'unterminated string", "SELECT a, 'u..................", ""},
		{"SELECT a, /* unterminated comment", "SELECT a, /*.....................", ""},
		{`SELECT a, "unterminated ident`, `SELECT a, "u.................`, ""},
		{"SELECT a, $$unterminated heredoc", "SELECT a, $$....................", "postgres"},
		{"'unterminated secret", "'u..................", ""},
		{"SELECT a, '", "SELECT a, '", ""}, // remainder shorter than the delimiter
		// A quoted identifier with an embedded space keeps its OWN run of
		// letters per word, the space staying in place rather than folding
		// the two words into one run.
		{`SELECT "a b", "a c"`, `SELECT "a a", "a b"`, ""},
		// A number and a string holding the same text alias separately --
		// they are not the same KEY, or the string would leak the number's
		// shape and vice versa.
		{"SELECT 123, '123'", "SELECT 100, 'aab'", ""},
		{"SELECT a -- secret comment\nFROM t", "SELECT a -- ...... .......\nFROM b", ""},
	} {
		got := Anonymize(tc.sql, tc.dialect)
		if got != tc.want {
			t.Errorf("Anonymize(%q, %q):\n got  %q\n want %q", tc.sql, tc.dialect, got, tc.want)
		}
		if len(got) != len(tc.sql) {
			t.Errorf("Anonymize(%q) changed length: %d -> %d", tc.sql, len(tc.sql), len(got))
		}
	}
}

// TestAnonymizeHugeNumber covers _number_alias's OTHER path: a literal over
// 4000 characters is not given a matching fake number at all -- only its
// first digit becomes `1` and the rest `0`, rather than computing a
// same-width replacement the way every other number gets.
func TestAnonymizeHugeNumber(t *testing.T) {
	digits := make([]byte, 4500)
	for i := range digits {
		digits[i] = '1'
	}
	sql := "SELECT " + string(digits)
	got := Anonymize(sql, "")
	if len(got) != len(sql) {
		t.Fatalf("length changed: %d -> %d", len(sql), len(got))
	}
	number := got[len("SELECT "):]
	if number[0] != '1' {
		t.Errorf("first digit = %q, want '1'", number[0])
	}
	for i := 1; i < len(number); i++ {
		if number[i] != '0' {
			t.Errorf("digit %d = %q, want '0'", i, number[i])
			break
		}
	}
}

func TestAnonymizeEmptyInput(t *testing.T) {
	if got := Anonymize("", ""); got != "" {
		t.Errorf("Anonymize(%q) = %q, want empty", "", got)
	}
	if got := Anonymize("   ", ""); got != "   " {
		t.Errorf("Anonymize(%q) = %q, want unchanged", "   ", got)
	}
}

package sqlglot

import (
	"errors"
	"strings"
	"testing"
)

// The construct has to survive as a FIELD. The whole point of the type is that
// a consumer deciding what to build next reads a label rather than scraping a
// sentence, so a refactor that kept the message and dropped the field would
// pass every other test here.
func TestAnUnsupportedStatementNamesItsConstruct(t *testing.T) {
	for _, tc := range []struct {
		sql, dialect, construct string
	}{
		{"SELECT a FROM t1 NATURAL JOIN t2", "tsql", "join method NATURAL"},
		{"SELECT a FROM t1 JOIN t2 USING (a)", "tsql", "USING"},
		{"SELECT a FROM t GROUP BY ROLLUP(a)", "tsql", "GROUP BY ROLLUP"},
	} {
		_, err := ParseOne(tc.sql, tc.dialect)
		if err == nil {
			t.Errorf("%q parsed; expected it to be unsupported", tc.sql)
			continue
		}
		var unsupported *UnsupportedError
		if !errors.As(err, &unsupported) {
			t.Errorf("%q: error is not an *UnsupportedError: %v", tc.sql, err)
			continue
		}
		if unsupported.Construct != tc.construct {
			t.Errorf("%q: construct = %q, want %q", tc.sql, unsupported.Construct, tc.construct)
		}
		// The old sentinel must keep working, or every existing caller changes.
		if !errors.Is(err, ErrUnsupported) {
			t.Errorf("%q: errors.Is(err, ErrUnsupported) is false", tc.sql)
		}
	}
}

// The message is what a human reads in a refusal, and callers already match on
// it. Changing the error to a struct must not have changed a byte of it.
func TestTheUnsupportedMessageIsUnchanged(t *testing.T) {
	err := &UnsupportedError{Construct: "PIVOT", Token: "PIVOT"}
	want := `sqlglot-go: unsupported statement: PIVOT at "PIVOT"`
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}

// Label narrows the construct with the token when, and only when, the token is
// dialect vocabulary. Without this a window function and a PIVOT are both
// "trailing tokens", which tells whoever is deciding what to build next
// nothing at all.
func TestLabelNamesTheKeywordThatStoppedIt(t *testing.T) {
	for _, tc := range []struct{ sql, want string }{
		{"SELECT ROW_NUMBER() OVER (ORDER BY a) FROM t", "trailing tokens at OVER"},
		{"SELECT a FROM t PIVOT (SUM(b) FOR c IN ('x'))", "trailing tokens at PIVOT"},
	} {
		_, err := ParseOne(tc.sql, "tsql")
		var u *UnsupportedError
		if !errors.As(err, &u) {
			t.Errorf("%q: not unsupported: %v", tc.sql, err)
			continue
		}
		if u.Label() != tc.want {
			t.Errorf("%q: Label() = %q, want %q", tc.sql, u.Label(), tc.want)
		}
	}
}

// The privacy property, as a test rather than as a comment. Label is written
// to logs by consumers, so an identifier or a literal reaching it is a leak --
// and the tempting "just append the token" change would cause exactly that.
func TestLabelNeverCarriesWhatTheCallerWrote(t *testing.T) {
	secrets := []string{"patient_ssn_table", "someone@real.example", "hunter2"}
	for _, sql := range []string{
		"SELECT * FROM t WHERE email = 'someone@real.example' garbage_word",
		"SELECT * FROM patient_ssn_table zzz qqq",
		"SELECT 'hunter2' 'hunter2' 'hunter2'",
	} {
		_, err := ParseOne(sql, "tsql")
		var u *UnsupportedError
		if !errors.As(err, &u) {
			t.Fatalf("%q should be unsupported, so this test proves nothing: %v", sql, err)
		}
		for _, secret := range secrets {
			if strings.Contains(strings.ToLower(u.Label()), strings.ToLower(secret)) {
				t.Errorf("Label() = %q leaked %q", u.Label(), secret)
			}
		}
	}
}

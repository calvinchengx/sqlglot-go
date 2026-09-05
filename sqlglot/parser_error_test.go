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
		{"CREATE TYPE widget", "postgres", "CREATE TYPE without AS"},
		{"SELECT a FROM t1 JOIN t2 USING", "tsql", "USING without a column list"},
		{"SELECT a FROM t GROUP BY ROLLUP", "tsql", "a grouping without its arguments"},
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

// Error() is Label() with the sentinel in front. Token stays on the struct
// for a caller that has a reason to inspect it; putting it in the message
// would undo Label().
func TestUnsupportedErrorMessageIsTheLabel(t *testing.T) {
	err := &UnsupportedError{Construct: "PIVOT", Token: "PIVOT"}
	want := "sqlglot-go: unsupported statement: PIVOT"
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
	narrow := &UnsupportedError{Construct: "trailing tokens", Token: "OVER", TokenIsKeyword: true}
	if got, want := narrow.Error(), "sqlglot-go: unsupported statement: trailing tokens at OVER"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

// Label narrows the construct with the token when, and only when, the token is
// dialect vocabulary. Without this a FETCH and a WINDOW are both "trailing
// tokens", which tells whoever is deciding what to build next nothing at all.
//
// The window function this used to name was the original example, and it now
// parses -- which is the point of counting them. TABLESAMPLE and PIVOT have
// both followed it since, for the same reason and by the same route: the label
// said what to build, so the label stopped being true. CLUSTER BY and
// DISTRIBUTE BY were the third pair to go the same way. The cases here are
// whatever is still refused, and they are meant to keep being replaced.
func TestLabelNamesTheKeywordThatStoppedIt(t *testing.T) {
	for _, tc := range []struct{ sql, want string }{
		{"SELECT * FROM tbl GROUP BY GROUPING SETS (GROUPING SETS (course))", "expression at GROUPING SETS"},
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
		"SELECT 'hunter2' FROM patient_ssn_table PIVOT(SUM(y) FOR foo IN y_enum)",
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
			if strings.Contains(strings.ToLower(u.Error()), strings.ToLower(secret)) {
				t.Errorf("Error() = %q leaked %q", u.Error(), secret)
			}
		}
	}
}

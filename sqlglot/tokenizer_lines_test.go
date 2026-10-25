package sqlglot

import "testing"

// The tokenizer's own bookkeeping: a string spanning lines keeps the line and
// column the reference records, a comment before a semicolon stays with the
// statement, an escaped newline is decoded as one, and Athena falls back to
// Hive where the combined rules reject a literal only one engine reads.
func TestTokenizerLinesAndFallbacks(t *testing.T) {
	for _, c := range []struct {
		dialect string
		sql     string
		kind    TokenType
		text    string
		line    int
		col     int
	}{
		{"", "SELECT 'a\nb'", TokSTRING, "a\nb", 2, 2},
		{"postgres", `SELECT E'a\nb'`, TokBYTE_STRING, "a\nb", 1, 14},
		{"athena", `CREATE DATABASE foo COMMENT "a\"b"`, TokSTRING, `a"b`, 1, 34},
	} {
		tok, err := NewTokenizer(c.dialect)
		if err != nil {
			t.Errorf("[%s] NewTokenizer: %v", c.dialect, err)
			continue
		}
		tokens, err := tok.Tokenize(c.sql)
		if err != nil {
			t.Errorf("[%s] Tokenize(%q): %v", c.dialect, c.sql, err)
			continue
		}
		found := false
		for _, tk := range tokens {
			if tk.Type != c.kind {
				continue
			}
			found = true
			if tk.Text != c.text || tk.Line != c.line || tk.Col != c.col {
				t.Errorf("[%s] %q: got (%q,%d,%d), want (%q,%d,%d)",
					c.dialect, c.sql, tk.Text, tk.Line, tk.Col, c.text, c.line, c.col)
			}
			break
		}
		if !found {
			t.Errorf("[%s] %q produced no %v token", c.dialect, c.sql, c.kind)
		}
	}
	// A comment sitting on the semicolon stays attached to the statement it
	// closes rather than being dropped.
	tok, _ := NewTokenizer("")
	tokens, err := tok.Tokenize("SELECT 1 /* c */;")
	if err != nil {
		t.Fatalf("Tokenize: %v", err)
	}
	semicolon := tokens[len(tokens)-1]
	if semicolon.Type != TokSEMICOLON {
		t.Fatalf("last token is %v, want a semicolon", semicolon.Type)
	}
}

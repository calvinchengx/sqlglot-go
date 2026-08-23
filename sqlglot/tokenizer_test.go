package sqlglot

import (
	"errors"
	"strings"
	"testing"
)

func mustTokens(t *testing.T, sql, dialect string) []Token {
	t.Helper()
	toks, err := Tokenize(sql, dialect)
	if err != nil {
		t.Fatalf("tokenize %q as %q: %v", sql, dialect, err)
	}
	return toks
}

func summary(toks []Token) string {
	var b strings.Builder
	for i, tok := range toks {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(strings.TrimPrefix(tok.Type.String(), "TokenType."))
		b.WriteString("(")
		b.WriteString(tok.Text)
		b.WriteString(")")
	}
	return b.String()
}

// The differential run in harness/ is what proves fidelity across the corpus.
// These cases pin the behaviours a reader of this package would want stated
// outright, and the ones a corpus of valid SQL cannot reach: errors, empty
// input, unknown dialects.
func TestTokenizeShapes(t *testing.T) {
	for _, c := range []struct{ name, sql, dialect, want string }{
		{"select", "SELECT 1", "", "SELECT(SELECT) NUMBER(1)"},
		{"qualified", "SELECT a.b", "", "SELECT(SELECT) VAR(a) DOT(.) VAR(b)"},
		{"string escape", "SELECT 'it''s'", "", "SELECT(SELECT) STRING(it's)"},
		{"quoted identifier", `SELECT "x y"`, "", `SELECT(SELECT) IDENTIFIER(x y)`},
		{"bracket identifier", "SELECT [x y]", "tsql", "SELECT(SELECT) IDENTIFIER(x y)"},
		{"backtick identifier", "SELECT `x y`", "databricks", "SELECT(SELECT) IDENTIFIER(x y)"},
		{"multiword keyword", "SELECT 1 GROUP BY a", "", "SELECT(SELECT) NUMBER(1) GROUP_BY(GROUP BY) VAR(a)"},
		{"hex string", "SELECT 0x1F", "tsql", "SELECT(SELECT) HEX_STRING(1F)"},
		{"hex fallback", "SELECT 0xZZ", "tsql", "SELECT(SELECT) IDENTIFIER(0xZZ)"},
		{"bit string", "SELECT 0b1010", "postgres", "SELECT(SELECT) BIT_STRING(1010)"},
		{"bit fallback", "SELECT 0b12", "postgres", "SELECT(SELECT) IDENTIFIER(0b12)"},
		{"no hex strings here", "SELECT 0xZZ", "duckdb", "SELECT(SELECT) NUMBER(0) VAR(xZZ)"},
		{"underscore separated", "SELECT 100_000", "duckdb", "SELECT(SELECT) NUMBER(100000)"},
		{"numeric literal suffix", "SELECT 1L", "databricks", "SELECT(SELECT) NUMBER(1) DCOLON(::) BIGINT(L)"},
		{"heredoc", "SELECT $$x$$", "postgres", "SELECT(SELECT) HEREDOC_STRING(x)"},
		{"tagged heredoc", "SELECT $t$x$t$", "postgres", "SELECT(SELECT) HEREDOC_STRING(x)"},
		{"scientific", "SELECT 1e-5", "", "SELECT(SELECT) NUMBER(1e-5)"},
		{"command payload", "EXPLAIN SELECT 1", "", "COMMAND(EXPLAIN) STRING(SELECT 1)"},
		{"empty", "", "", ""},
		{"only whitespace", "   \t  ", "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := summary(mustTokens(t, c.sql, c.dialect)); got != c.want {
				t.Errorf("Tokenize(%q, %q)\n  want %s\n  got  %s", c.sql, c.dialect, c.want, got)
			}
		})
	}
}

func TestPositionsAreRuneOffsets(t *testing.T) {
	// A multi-byte identifier must not shift the offsets of what follows: the
	// reference counts characters, and a byte-counting port would drift here
	// and nowhere a pure-ASCII corpus could see it.
	toks := mustTokens(t, `SELECT "café", x`, "")
	last := toks[len(toks)-1]
	if last.Text != "x" {
		t.Fatalf("last token is %s, want x", last)
	}
	if last.Start != 15 || last.End != 15 {
		t.Errorf("x is at runes [%d..%d], want [15..15]", last.Start, last.End)
	}
}

func TestCommentsAttachToTokens(t *testing.T) {
	t.Run("trailing attaches to the preceding token", func(t *testing.T) {
		toks := mustTokens(t, "SELECT 1 -- why\nFROM t", "")
		if len(toks[1].Comments) != 1 || toks[1].Comments[0] != " why" {
			t.Errorf("NUMBER token has comments %q, want [\" why\"]", toks[1].Comments)
		}
	})
	t.Run("leading attaches to the following token", func(t *testing.T) {
		toks := mustTokens(t, "-- why\nSELECT 1", "")
		if len(toks[0].Comments) != 1 || toks[0].Comments[0] != " why" {
			t.Errorf("SELECT token has comments %q, want [\" why\"]", toks[0].Comments)
		}
	})
	// A comment on the same line as the token before it is that token's,
	// even when it reads as introducing what follows.
	t.Run("block comment", func(t *testing.T) {
		toks := mustTokens(t, "SELECT /* a */ 1", "")
		if len(toks[0].Comments) != 1 || toks[0].Comments[0] != " a " {
			t.Errorf("SELECT token has comments %q, want [\" a \"]", toks[0].Comments)
		}
	})
	t.Run("nested block comment", func(t *testing.T) {
		toks := mustTokens(t, "SELECT /* a /* b */ c */ 1", "postgres")
		if len(toks[0].Comments) != 1 || toks[0].Comments[0] != " a /* b */ c " {
			t.Errorf("SELECT token has comments %q", toks[0].Comments)
		}
	})
}

func TestLineAndColumnTracking(t *testing.T) {
	toks := mustTokens(t, "SELECT 1\nFROM t\r\nWHERE x", "")
	lines := map[string]int{}
	for _, tok := range toks {
		lines[tok.Text] = tok.Line
	}
	for text, want := range map[string]int{"SELECT": 1, "FROM": 2, "WHERE": 3} {
		if lines[text] != want {
			t.Errorf("%s is on line %d, want %d", text, lines[text], want)
		}
	}
}

func TestTokenizeErrors(t *testing.T) {
	t.Run("unterminated string", func(t *testing.T) {
		toks, err := Tokenize("SELECT 'oops", "")
		if err == nil {
			t.Fatalf("want an error, got %d tokens", len(toks))
		}
		if toks != nil {
			t.Errorf("tokens returned alongside an error: %v", toks)
		}
		var te *TokenError
		if !errors.As(err, &te) {
			t.Fatalf("want a *TokenError, got %T", err)
		}
		if !strings.Contains(te.Error(), "Error tokenizing") {
			t.Errorf("unhelpful message: %s", te.Error())
		}
		if te.Start < 0 || te.End < te.Start {
			t.Errorf("nonsensical range [%d..%d]", te.Start, te.End)
		}
	})

	t.Run("unterminated identifier", func(t *testing.T) {
		if _, err := Tokenize(`SELECT "oops`, ""); err == nil {
			t.Error("want an error for an unterminated quoted identifier")
		}
	})

	t.Run("unknown dialect", func(t *testing.T) {
		_, err := Tokenize("SELECT 1", "oracle")
		if err == nil {
			t.Fatal("want an error for a dialect the port does not configure")
		}
		if !strings.Contains(err.Error(), "oracle") || !strings.Contains(err.Error(), "duckdb") {
			t.Errorf("the error should name both the ask and what is available: %s", err)
		}
	})
}

func TestReuseAcrossStatements(t *testing.T) {
	tk, err := NewTokenizer("")
	if err != nil {
		t.Fatal(err)
	}
	first := summary(tokenizeWith(t, tk, "SELECT 1"))
	if _, err := tk.Tokenize("SELECT 'oops"); err == nil {
		t.Fatal("want an error on the second statement")
	}
	// State from a failed statement must not leak into the next one.
	if got := summary(tokenizeWith(t, tk, "SELECT 1")); got != first {
		t.Errorf("after a failure the tokenizer returned %s, want %s", got, first)
	}
	if tk.Config().Name != "" {
		t.Errorf("Config().Name = %q, want the neutral dialect", tk.Config().Name)
	}
}

func must(t *testing.T, toks []Token, err error) []Token {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	return toks
}

func tokenizeWith(t *testing.T, tk *Tokenizer, sql string) []Token {
	t.Helper()
	toks, err := tk.Tokenize(sql)
	return must(t, toks, err)
}

func TestDialectsAreConfigured(t *testing.T) {
	want := []string{"", "tsql", "postgres", "duckdb", "databricks"}
	got := Dialects()
	if len(got) != len(want) {
		t.Fatalf("Dialects() = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Dialects()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	for _, d := range got {
		cfg, ok := ConfigFor(d)
		if !ok {
			t.Fatalf("no config for %q", d)
		}
		if cfg.trie == nil {
			t.Errorf("%q: keyword trie was not built", d)
		}
		if len(cfg.Keywords) == 0 || len(cfg.SingleTokens) == 0 {
			t.Errorf("%q: empty tables", d)
		}
	}
	if _, ok := ConfigFor("mysql"); ok {
		t.Error("ConfigFor reported a dialect the port does not have")
	}
}

func TestTokenTypeNames(t *testing.T) {
	if got := TokSELECT.String(); got != "TokenType.SELECT" {
		t.Errorf("TokSELECT.String() = %q", got)
	}
	if got := TokenType(-1).String(); got != "TokenType(?)" {
		t.Errorf("unknown token type renders as %q", got)
	}
	tt, ok := TokenTypeByName("SELECT")
	if !ok || tt != TokSELECT {
		t.Errorf("TokenTypeByName(SELECT) = %v, %v", tt, ok)
	}
	if _, ok := TokenTypeByName("NOT_A_TOKEN"); ok {
		t.Error("TokenTypeByName invented a token type")
	}
}

func TestTokenString(t *testing.T) {
	tok := Token{Type: TokVAR, Text: "x", Line: 2, Col: 3, Start: 4, End: 4}
	want := "<Token token_type: TokenType.VAR, text: x, line: 2, col: 3, start: 4, end: 4>"
	if got := tok.String(); got != want {
		t.Errorf("Token.String()\n  want %s\n  got  %s", want, got)
	}
}

// A command's payload is swallowed as a string only where a command can start.
// These two cases sit either side of that rule and neither is a whole statement
// the reference parser would accept, so the corpus cannot reach them.
func TestCommandPayloadOnlyInCommandPosition(t *testing.T) {
	for _, c := range []struct{ name, sql, want string }{
		{"after a semicolon the payload is taken", "BEGIN; EXPLAIN SELECT 1",
			"BEGIN(BEGIN) SEMICOLON(;) COMMAND(EXPLAIN) STRING(SELECT 1)"},
		{"mid-statement it is just a token", "SELECT EXPLAIN",
			"SELECT(SELECT) COMMAND(EXPLAIN)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := summary(mustTokens(t, c.sql, "")); got != c.want {
				t.Errorf("Tokenize(%q)\n  want %s\n  got  %s", c.sql, c.want, got)
			}
		})
	}
}

// Lexical corners that are not whole statements the reference parser accepts,
// so the corpus -- which only holds parseable SQL -- cannot reach them. Each
// expectation here was taken from the reference's own tokenizer.
func TestLexicalCornersOutsideTheCorpus(t *testing.T) {
	for _, c := range []struct{ name, sql, dialect, want string }{
		{"command payload stops at a semicolon", "EXPLAIN a; SELECT 1", "",
			"COMMAND(EXPLAIN) STRING(a) SEMICOLON(;) SELECT(SELECT) NUMBER(1)"},
		{"command with an empty payload", "EXPLAIN;", "", "COMMAND(EXPLAIN) SEMICOLON(;)"},
		{"repeated space inside a multi-word keyword", "SELECT 1 GROUP  BY a", "",
			"SELECT(SELECT) NUMBER(1) GROUP_BY(GROUP BY) VAR(a)"},
		{"a decimal point after a parameter is not part of the number", "SELECT $1.5", "postgres",
			"SELECT(SELECT) PARAMETER($) NUMBER(1) DOT(.) NUMBER(5)"},
		{"backslash escaping a backslash", `SELECT 'a\\b'`, "databricks", `SELECT(SELECT) STRING(a\b)`},
		{"backslash escaping a quote", `SELECT 'a\''`, "databricks", "SELECT(SELECT) STRING(a')"},
		{"a backslash is literal where it is not an escape", `SELECT 'a\\b'`, "duckdb", `SELECT(SELECT) STRING(a\\b)`},
		{"a raw string keeps its backslashes", `SELECT r'a\\b'`, "databricks", `SELECT(SELECT) RAW_STRING(a\\b)`},
		{"a doubled quote ends a raw string", "SELECT r'a''b'", "databricks",
			"SELECT(SELECT) RAW_STRING(a) STRING(b)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := summary(mustTokens(t, c.sql, c.dialect)); got != c.want {
				t.Errorf("Tokenize(%q, %q)\n  want %s\n  got  %s", c.sql, c.dialect, c.want, got)
			}
		})
	}
}

// A comment that is not on the previous token's line stays pending, and gets
// attached when the statement ends -- or, if a semicolon comes first, there.
func TestPendingCommentsAreNeverDropped(t *testing.T) {
	for _, c := range []struct{ name, sql string }{
		{"at the end of the statement", "SELECT 1\n-- alone"},
		{"before a semicolon", "SELECT 1\n-- alone\n;"},
	} {
		t.Run(c.name, func(t *testing.T) {
			toks := mustTokens(t, c.sql, "")
			if len(toks[1].Comments) != 1 || toks[1].Comments[0] != " alone" {
				t.Errorf("NUMBER token has comments %q, want [\" alone\"]", toks[1].Comments)
			}
		})
	}
}

func TestUnlexableStatements(t *testing.T) {
	for _, c := range []struct{ name, sql, dialect string }{
		{"a hex string that is not hex", `SELECT x'ZZ'`, "postgres"},
		{"an unterminated heredoc", "SELECT $t$abc", "postgres"},
		{"an escape with nothing after it", `SELECT 'a\\`, "databricks"},
		{"an escaped quote inside a raw string", `SELECT r'a\'b'`, "databricks"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if toks, err := Tokenize(c.sql, c.dialect); err == nil {
				t.Errorf("Tokenize(%q, %q) should have failed, got %s", c.sql, c.dialect, summary(toks))
			}
		})
	}
}

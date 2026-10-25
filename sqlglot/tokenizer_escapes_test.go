package sqlglot

import "testing"

// The numeric escapes each dialect's tokenizer decodes, and the ones it
// leaves as written: a hex escape is ClickHouse's, a \u escape is
// Snowflake's, and MySQL drops the backslash of one it does not know. The
// expected text is the reference's own tokenizer output.
func TestTokenizerNumericEscapes(t *testing.T) {
	for _, c := range []struct{ dialect, sql, want string }{
		{"clickhouse", `'\x41'`, "A"},
		{"clickhouse", `'\x4'`, `\x4`},
		{"clickhouse", `'\u00'`, `\u00`},
		{"clickhouse", `'\u0041'`, `\u0041`},
		{"snowflake", `'\u0041'`, "A"},
		{"snowflake", `'\uD83D\uDE00'`, "\U0001F600"},
		{"snowflake", `'\uD83D'`, "uD83D"},
		{"mysql", `'\x41'`, "x41"},
		{"duckdb", `'\101'`, `\101`},
		// A multi-byte character spelled as several BYTE escapes reads back
		// as itself, where the dialect's escapes encode bytes at all.
		{"clickhouse", `'\xC3\xA9'`, "é"},
		{"snowflake", `'\xC3\xA9'`, "Ã©"},
		{"duckdb", `'\xC3\xA9'`, `\xC3\xA9`},
		// Three bytes at once: the run is decoded only where it is valid
		// UTF-8 as a whole.
		{"clickhouse", `'\xE2\x82\xAC'`, "€"},
		// A surrogate PAIR is one character; a lone high half is kept as
		// written where the dialect has no replacement for it.
		{"snowflake", `'\uD83D\uDE00'`, "\U0001F600"},
		// A lone high surrogate with text after it is kept as written where
		// the dialect has no replacement to make.
		{"snowflake", `'\uD83Dx'`, "uD83Dx"},
		{"postgres", `'\x41'`, `\x41`},
	} {
		tok, err := NewTokenizer(c.dialect)
		if err != nil {
			t.Errorf("[%s] NewTokenizer: %v", c.dialect, err)
			continue
		}
		tokens, err := tok.Tokenize("SELECT " + c.sql)
		if err != nil {
			t.Errorf("[%s] Tokenize(%q): %v", c.dialect, c.sql, err)
			continue
		}
		var got string
		found := false
		for _, tk := range tokens {
			if tk.Type == TokSTRING {
				got, found = tk.Text, true
				break
			}
		}
		if !found {
			t.Errorf("[%s] %q produced no string token", c.dialect, c.sql)
			continue
		}
		if got != c.want {
			t.Errorf("[%s] %q is %q, want %q", c.dialect, c.sql, got, c.want)
		}
	}
}

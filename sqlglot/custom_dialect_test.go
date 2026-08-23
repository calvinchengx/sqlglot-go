package sqlglot

import "testing"

// The reference's tokenizer supports escape mechanisms that none of the five
// dialects this port configures actually use: identifier escapes, custom
// backslash escapes gated on the character that follows, and raw strings that
// still honour escapes. They are ported because a dialect added later will
// need them, and they are exercised here against a configuration built for the
// purpose -- otherwise the code would ship unrun, which is how a port acquires
// a bug that only appears the day someone adds Snowflake.
//
// The expectations came from the reference, not from reasoning about the port:
// harness/synthetic_check.py builds the same configurations in Python and
// prints what sqlglot produces for exactly these statements.
func customTokenizer(t *testing.T, adjust func(*Config)) *Tokenizer {
	t.Helper()
	base, ok := ConfigFor("")
	if !ok {
		t.Fatal("no neutral config to build on")
	}
	cfg := *base
	cfg.Name = "test"
	cfg.trie = nil
	adjust(&cfg)
	cfg.trie = cfg.buildTrie()
	tk := &Tokenizer{cfg: &cfg}
	tk.reset()
	return tk
}

func TestIdentifierEscapes(t *testing.T) {
	tk := customTokenizer(t, func(c *Config) {
		c.IdentifierEscapes = set{"\\": {}}
	})
	toks, err := tk.Tokenize(`SELECT "a\"b"`)
	if err != nil {
		t.Fatalf("tokenize: %v", err)
	}
	if got := summary(toks); got != `SELECT(SELECT) IDENTIFIER(a"b)` {
		t.Errorf("got %s", got)
	}
}

func TestCustomEscapeFollowChars(t *testing.T) {
	// With a follow-char set, a backslash before anything OUTSIDE that set is
	// a custom escape and drops out of the text; a backslash before a
	// backslash is an ordinary escaped escape and both characters stay.
	tk := customTokenizer(t, func(c *Config) {
		c.StringEscapes = set{"\\": {}, "'": {}}
		c.EscapeFollowChars = set{"n": {}}
		c.UnescapedSequences = nil
	})
	for _, c := range []struct{ sql, want string }{
		{`SELECT 'a\xb'`, `SELECT(SELECT) STRING(axb)`},
		{`SELECT 'a\\b'`, `SELECT(SELECT) STRING(a\\b)`},
		// A backslash before a follow char is not a custom escape at all.
		{`SELECT 'a\nb'`, `SELECT(SELECT) STRING(a\nb)`},
	} {
		toks, err := tk.Tokenize(c.sql)
		if err != nil {
			t.Fatalf("tokenize %q: %v", c.sql, err)
		}
		if got := summary(toks); got != c.want {
			t.Errorf("Tokenize(%q)\n  want %s\n  got  %s", c.sql, c.want, got)
		}
	}
}

func TestEscapeAtEndOfInputIsAnError(t *testing.T) {
	tk := customTokenizer(t, func(c *Config) {
		c.StringEscapes = set{"\\": {}, "'": {}}
		c.EscapeFollowChars = set{"n": {}}
		c.UnescapedSequences = nil
	})
	if toks, err := tk.Tokenize(`SELECT 'a\`); err == nil {
		t.Errorf("an escape with no character after it should fail, got %s", summary(toks))
	}
}

func TestRawStringsThatStillHonourEscapes(t *testing.T) {
	// A raw string keeps the backslash AND the character it escaped -- that is
	// what makes it raw -- but the escape still stops the quote from closing
	// the string early.
	tk := customTokenizer(t, func(c *Config) {
		c.StringEscapes = set{"\\": {}}
		c.UnescapedSequences = nil
		c.FormatStrings = map[string]FormatString{"r'": {End: "'", Type: TokRAW_STRING}}
		c.StringEscapesAllowedInRawStrings = true
	})
	toks, err := tk.Tokenize(`SELECT r'a\'b'`)
	if err != nil {
		t.Fatalf("tokenize: %v", err)
	}
	if got := summary(toks); got != `SELECT(SELECT) RAW_STRING(a\'b)` {
		t.Errorf("got %s", got)
	}
}

// Not every dialect reads || as concatenation; the reference has a flag for it
// and none of the five configured here turns it off, so the branch would ship
// unrun otherwise.
func TestDoublePipeIsNotAlwaysConcatenation(t *testing.T) {
	base, ok := ConfigFor("")
	if !ok {
		t.Fatal("no neutral config")
	}
	tables := *base.Tables
	tables.DPipeIsStringConcat = false

	toks, err := Tokenize("a || b", "")
	if err != nil {
		t.Fatal(err)
	}
	p := &parser{tokens: toks, cfg: base, tables: &tables}
	if tree, err := p.parseOne(); err == nil {
		t.Errorf("with || not a concatenation the statement should not parse, got %s", tree.Class)
	}
}

// Not every dialect lifts a trailing LIMIT onto a set operation, and all five
// configured here do -- so the branch that leaves it where it was parsed would
// otherwise never run.
func TestSetOpModifiersAreNotAlwaysLifted(t *testing.T) {
	base, ok := ConfigFor("")
	if !ok {
		t.Fatal("no neutral config")
	}
	tables := *base.Tables
	tables.ModifiersAttachedToSetOp = false

	toks, err := Tokenize("SELECT x FROM t1 UNION ALL SELECT x FROM t2 LIMIT 1", "")
	if err != nil {
		t.Fatal(err)
	}
	p := &parser{tokens: toks, cfg: base, tables: &tables}
	tree, err := p.parseOne()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if tree.Args["limit"] != nil {
		t.Error("the union took the LIMIT even though this dialect does not lift it")
	}
	if right := tree.Args["expression"].(*Expression); right.Args["limit"] == nil {
		t.Error("the LIMIT should have stayed on the query it was written after")
	}
}

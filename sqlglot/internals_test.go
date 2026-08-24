package sqlglot

import (
	"strings"
	"testing"
)

// The tokenizer leans on a handful of Python semantics that Go does not share.
// Each is small, each is load-bearing, and a differential corpus only exercises
// the cases the corpus happens to contain -- so they are pinned here directly.

func TestUpperASCIIIsNotAUnicodeFold(t *testing.T) {
	for in, want := range map[rune]rune{'a': 'A', 'z': 'Z', 'A': 'A', '_': '_', '1': '1', 'é': 'é', 'İ': 'İ'} {
		if got := upperASCII(in); got != want {
			t.Errorf("upperASCII(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPySliceBounds(t *testing.T) {
	for _, c := range []struct{ i, n, want int }{
		{0, 5, 0}, {3, 5, 3}, {5, 5, 5}, {9, 5, 5},
		{-1, 5, 4}, {-5, 5, 0}, {-9, 5, 0},
	} {
		if got := pyBound(c.i, c.n); got != c.want {
			t.Errorf("pyBound(%d, %d) = %d, want %d", c.i, c.n, got, c.want)
		}
	}

	// The distinction pySliceEnd exists for: a stop of 0 means index zero,
	// where a negative stop counts back from the end. Getting this wrong
	// would silently mangle every block comment's text.
	for _, c := range []struct{ stop, n, want int }{
		{0, 5, 0}, {-1, 5, 4}, {-5, 5, 0}, {-9, 5, 0}, {3, 5, 3}, {9, 5, 5},
	} {
		if got := pySliceEnd(c.stop, c.n); got != c.want {
			t.Errorf("pySliceEnd(%d, %d) = %d, want %d", c.stop, c.n, got, c.want)
		}
	}
}

func TestParseIntLiteralMatchesPythonInt(t *testing.T) {
	for _, c := range []struct {
		text string
		base int
		ok   bool
		want string
	}{
		{"0x1F", 16, true, "31"},
		{"0X1f", 16, true, "31"},
		{"1F", 16, true, "31"},
		{"0b1010", 2, true, "10"},
		{"1010", 2, true, "10"},
		{"0b12", 2, false, ""},
		{"0xZZ", 16, false, ""},
		{"1_0", 2, true, "2"},
		{"0x", 16, false, ""},
		{"", 16, false, ""},
		// Python's int is unbounded, so the port's must be too.
		{strings.Repeat("f", 40), 16, true, "1461501637330902918203684832716283019655932542975"},
	} {
		got, ok := parseIntLiteral(c.text, c.base)
		if ok != c.ok {
			t.Errorf("parseIntLiteral(%q, %d) ok = %v, want %v", c.text, c.base, ok, c.ok)
			continue
		}
		if ok && got.String() != c.want {
			t.Errorf("parseIntLiteral(%q, %d) = %s, want %s", c.text, c.base, got, c.want)
		}
	}
}

func TestTrimPrefixRunes(t *testing.T) {
	for _, c := range []struct {
		in   string
		n    int
		want string
	}{{"0x1F", 2, "1F"}, {"0b", 2, ""}, {"0", 2, ""}, {"ééab", 2, "ab"}} {
		if got := trimPrefixRunes(c.in, c.n); got != c.want {
			t.Errorf("trimPrefixRunes(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}

func TestRuneSearchHelpers(t *testing.T) {
	s := []rune("a\nb\nc")
	if got := indexRuneFrom(s, '\n', 0); got != 1 {
		t.Errorf("indexRuneFrom = %d, want 1", got)
	}
	if got := indexRuneFrom(s, '\n', -5); got != 1 {
		t.Errorf("indexRuneFrom with a negative start = %d, want 1", got)
	}
	if got := indexRuneFrom(s, 'z', 0); got != -1 {
		t.Errorf("indexRuneFrom for an absent rune = %d, want -1", got)
	}
	if got := indexRuneRange(s, '\n', 2, 99); got != 3 {
		t.Errorf("indexRuneRange = %d, want 3", got)
	}
	if got := indexRuneRange(s, '\n', -1, 1); got != -1 {
		t.Errorf("indexRuneRange over an empty span = %d, want -1", got)
	}
	if got := lastIndexRuneRange(s, '\n', 0, 99); got != 3 {
		t.Errorf("lastIndexRuneRange = %d, want 3", got)
	}
	if got := lastIndexRuneRange(s, 'z', -1, 99); got != -1 {
		t.Errorf("lastIndexRuneRange for an absent rune = %d, want -1", got)
	}
	if got := countRuneRange(s, '\n', -1, 99); got != 2 {
		t.Errorf("countRuneRange = %d, want 2", got)
	}
}

func TestPythonCharacterPredicates(t *testing.T) {
	if !isAlnum('9') || !isAlnum('é') || isAlnum('-') {
		t.Error("isAlnum does not match Python's str.isalnum")
	}
	if !isIdentifierChar('_') || !isIdentifierChar('é') || isIdentifierChar('-') {
		t.Error("isIdentifierChar does not match Python's str.isidentifier")
	}
	if !isDigitChar('0', true) || isDigitChar('0', false) || isDigitChar('٩', true) {
		t.Error("isDigitChar should be ASCII digits only, and only when a character is set")
	}
	if allDigits("") || !allDigits("123") || allDigits("12a") {
		t.Error("allDigits does not match Python's str.isdigit")
	}
	if !hasSpace("a b") || hasSpace("ab") {
		t.Error("hasSpace is wrong")
	}
}

func TestTrieOnNilNode(t *testing.T) {
	var n *trieNode
	if n.get('A') != nil {
		t.Error("a nil trie node should have no children")
	}
}

func TestKeywordTrieAdmissionRule(t *testing.T) {
	cfg, ok := ConfigFor("")
	if !ok {
		t.Fatal("no neutral config")
	}
	// Multi-word keywords need the trie -- a word scan stops at the space.
	if cfg.trie.get('G').get('R').get('O').get('U').get('P').get(' ') == nil {
		t.Error(`"GROUP BY" is not in the trie`)
	}
	// A plain word does not, and is deliberately left out -- other keys may
	// share its prefix, but nothing marks SELECT itself as ending here.
	n := cfg.trie
	for _, r := range "SELECT" {
		if n = n.get(r); n == nil {
			return
		}
	}
	if n.word {
		t.Error(`"SELECT" should not be in the trie; the word scanner finds it`)
	}
}

func TestIndexingPanicsOutOfRange(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("indexing past the end should panic, so Tokenize reports it as an error")
		}
	}()
	tk := &Tokenizer{sql: []rune("ab"), size: 2}
	tk.at(5)
}

func TestFreshTokenizerHasNoCurrentCharacter(t *testing.T) {
	tk, err := NewTokenizer("")
	if err != nil {
		t.Fatal(err)
	}
	if got := tk.charStr(); got != "" {
		t.Errorf("a tokenizer that has not advanced reports the character %q", got)
	}
}

func TestNegativeIndexWrapsLikePython(t *testing.T) {
	// The reference backs up over a numeric literal's suffix with a negative
	// advance, and Python's indexing wraps rather than failing. A Go port that
	// clamped instead would read the wrong character, silently.
	tk := &Tokenizer{sql: []rune("abc"), size: 3}
	if r, _ := tk.at(-1); r != 'c' {
		t.Errorf("at(-1) = %q, want 'c'", r)
	}
	if r, _ := tk.at(0); r != 'a' {
		t.Errorf("at(0) = %q, want 'a'", r)
	}
}

// TestHeredocTagIsDigit covers the rule the reference spells `tag.isdigit()`.
//
// Go's unicode.IsDigit is category Nd; Python's isdigit is wider and takes in
// the superscript and subscript forms. `$¹$` is therefore not a heredoc tag,
// and reading it as one sent the tokenizer looking for a closing `$¹$` that
// was never written. It does NOT take in the other numerics: Python calls
// `½` numeric but not a digit.
func TestHeredocTagIsDigit(t *testing.T) {
	for _, c := range []struct {
		tag  string
		want bool
	}{
		{"1", true},
		{"123", true},
		{"¹", true},
		{"²³", true},
		{"₇", true},
		{"½", false},
		{"a", false},
		{"1a", false},
		{"", false},
	} {
		if got := allDigits(c.tag); got != c.want {
			t.Errorf("allDigits(%q) = %v, want %v", c.tag, got, c.want)
		}
	}
}

// TestParserWouldRefuse covers the generator's mirror of the parser's own
// refusal: a name with a builder that inspects its arguments is one the port
// must not WRITE either, or it emits SQL it cannot read back.
func TestParserWouldRefuse(t *testing.T) {
	cfg, ok := ConfigFor("tsql")
	if !ok {
		t.Fatal("no tsql config")
	}
	g := &generator{cfg: cfg, tables: cfg.Tables, dialect: "tsql"}
	for _, c := range []struct {
		name string
		want bool
	}{
		{"HASHBYTES", true},
		{"", false},
		{"CONVERT", false}, // a syntax of its own, which the parser handles
		{"ABS", false},     // a plain signature
		{"NOT_A_FUNCTION_ANYWHERE", false},
		// A name whose shape depends on its argument COUNT is handled too,
		// and must not be caught by the guard.
		{"CONVERT_TIMEZONE", false},
	} {
		if got := g.parserWouldRefuse(c.name); got != c.want {
			t.Errorf("parserWouldRefuse(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestTemplateName reads the function name out of a syntax template, which is
// what the guard above is applied to.
func TestTemplateName(t *testing.T) {
	for _, c := range []struct{ template, want string }{
		{"HASHBYTES('SHA1', {this})", "HASHBYTES"},
		{"hashbytes({this})", "HASHBYTES"},
		{"{this} + {expression}", ""},
		{"NOPARENS", ""},
		{"", ""},
		{"NOT A NAME({this})", ""},
	} {
		if got := templateName(c.template); got != c.want {
			t.Errorf("templateName(%q) = %q, want %q", c.template, got, c.want)
		}
	}
}

// TestCompareConstants covers the ordering the range rule reasons with, and
// the cases where it declines: two constants that are not both numbers or
// both strings have no order here. Dates fall there on purpose.
func TestCompareConstants(t *testing.T) {
	num := func(v string) *Expression {
		return New("Literal", Arg{"this", v}, Arg{"is_string", false})
	}
	str := func(v string) *Expression {
		return New("Literal", Arg{"this", v}, Arg{"is_string", true})
	}
	for _, c := range []struct {
		name string
		a, b *Expression
		want int
		ok   bool
	}{
		{"numbers ascending", num("1"), num("2"), -1, true},
		{"numbers descending", num("2"), num("1"), 1, true},
		{"numbers equal", num("2"), num("2"), 0, true},
		{"a negative is a number", New("Neg", Arg{"this", num("1")}), num("0"), -1, true},
		{"strings ascending", str("a"), str("b"), -1, true},
		{"strings descending", str("b"), str("a"), 1, true},
		{"strings equal", str("a"), str("a"), 0, true},
		{"a number against a string has no order", num("1"), str("a"), 0, false},
		{"nor does a date", New("Cast"), New("Cast"), 0, false},
		{"nor an unparseable number", num("not a number"), num("1"), 0, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, ok := compareConstants(c.a, c.b)
			if ok != c.ok || (ok && got != c.want) {
				t.Errorf("compareConstants() = (%d, %v), want (%d, %v)", got, ok, c.want, c.ok)
			}
		})
	}
}

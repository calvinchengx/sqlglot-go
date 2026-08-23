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

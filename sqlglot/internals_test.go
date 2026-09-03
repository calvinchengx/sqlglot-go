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
		{"⑹", true}, // a parenthesised digit, which an inclusion list missed
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
		{"", false},
		// A name whose class one argument's WORD chooses is read, not
		// refused: HASHBYTES('SHA1', x) is an SHA. It used to be the one
		// name this guard caught.
		{"HASHBYTES", false},
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
	// No name in any of the five dialects reaches the guard's own answer any
	// more -- every one the reference names has a signature the port can
	// build. The guard stays because the property it protects does: a
	// spelling the parser cannot read back is a statement nobody has checked.
	// So it is put to a name that has none, rather than deleted for want of
	// one in the tables as they stand today.
	doctored := *cfg.Tables
	doctored.NamedFunctions = map[string]struct{}{"ZZ_NO_SIGNATURE": {}}
	doctored.Functions = nil
	doctored.FunctionsByArity = nil
	doctored.ValueDispatchFunctions = nil
	doctored.SyntaxFunctions = nil
	blind := &generator{cfg: cfg, tables: &doctored, dialect: "tsql"}
	if !blind.parserWouldRefuse("ZZ_NO_SIGNATURE") {
		t.Error("a name with no signature at all did not reach the guard")
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

// The case a name is compared in is a fact about the dialect, and a dialect
// that upper-cases is one the reference already has -- Snowflake. None of the
// five configured here does, so the fold is exercised directly rather than
// shipped unrun.
func TestIdentifierFolding(t *testing.T) {
	for _, tc := range []struct{ unquoted, quoted, name, want string }{
		{"lower", "", "AbC", "abc"},
		{"upper", "", "AbC", "ABC"},
		{"", "", "AbC", "AbC"},
	} {
		g := &generator{tables: &ParserTables{
			NormalizeUnquoted: tc.unquoted, NormalizeQuoted: tc.quoted,
		}}
		if got := g.normalized(New("Identifier", Arg{"this", tc.name})); got != tc.want {
			t.Errorf("%q folded to %q, want %q", tc.unquoted, got, tc.want)
		}
	}
	// A quoted name folds by its own rule: PostgreSQL keeps it as written
	// where it lower-cases a bare one.
	g := &generator{tables: &ParserTables{NormalizeUnquoted: "lower", NormalizeQuoted: ""}}
	quoted := New("Identifier", Arg{"this", "AbC"}, Arg{"quoted", true})
	if got := g.normalized(quoted); got != "AbC" {
		t.Errorf("a quoted name folded to %q, want AbC", got)
	}
}

// Three small answers the writers and the tokenizer lean on, put to the cases
// the corpus does not happen to contain.
func TestSmallHelpers(t *testing.T) {
	// Python calls four control characters space and Go does not, which is
	// where a value ends for the reference.
	for _, r := range []rune{0x1C, 0x1D, 0x1E, 0x1F, ' ', '\t', '\n'} {
		if !isPythonSpace(r) {
			t.Errorf("isPythonSpace(%#x) = false", r)
		}
	}
	for _, r := range []rune{'a', '0', 0x1B} {
		if isPythonSpace(r) {
			t.Errorf("isPythonSpace(%#x) = true", r)
		}
	}

	// How many members a set of keys holds, counting a list as its length and
	// an absent key as nothing.
	node := New("Select",
		Arg{"expressions", []*Expression{New("Star"), New("Star")}},
		Arg{"from_", New("From")})
	if got := listLength(node, []string{"expressions", "from_", "where"}); got != 3 {
		t.Errorf("listLength = %d, want 3", got)
	}

	cfg, ok := ConfigFor("postgres")
	if !ok {
		t.Fatal("no postgres config")
	}
	g := &generator{cfg: cfg, tables: cfg.Tables, dialect: "postgres"}
	// A name reads back as one token, or it does not.
	for _, c := range []struct {
		name string
		want bool
	}{
		{"plain", true},
		{"two words", false},
		{"", false},
	} {
		if got := g.lexesBackAsOneName(c.name); got != c.want {
			t.Errorf("lexesBackAsOneName(%q) = %v, want %v", c.name, got, c.want)
		}
	}
	// A dollar already written turns the same name into two.
	g.wroteDollar = true
	if g.lexesBackAsOneName("a$b") {
		t.Error("a name holding a dollar was called one token after a dollar")
	}

	// A cast the reference writes as its own operand, and one it does not.
	div := New("Cast",
		Arg{"this", New("IntDiv", Arg{"this", New("Literal", Arg{"this", "4"})},
			Arg{"expression", New("Literal", Arg{"this", "2"})})},
		Arg{"to", New("DataType", Arg{"this", DataTypeKind("DECIMAL")})})
	if _, gone := castElidedOver(cfg.Tables, div); !gone {
		t.Error("the cast PostgreSQL never writes was kept")
	}
	if _, gone := castElidedOver(cfg.Tables, New("Cast")); gone {
		t.Error("a cast over nothing was dropped")
	}
}

// A setting's value is whatever it was written as: a literal stays one, and a
// bare word is a word rather than a column, because nothing is being selected.
func TestSettingValues(t *testing.T) {
	for _, c := range []struct{ sql, class, text string }{
		{"CREATE INDEX i ON t(a) WITH (k=on)", "Var", "on"},
		{"CREATE INDEX i ON t(a) WITH (k=OFF)", "Var", "OFF"},
		{"CREATE INDEX i ON t(a) WITH (k=b)", "Var", "b"},
		{"CREATE INDEX i ON t(a) WITH (k=1)", "Literal", "1"},
		{"CREATE INDEX i ON t(a) WITH (k='x')", "Literal", "x"},
		{"CREATE INDEX i ON t(a) WITH (k=TRUE)", "Boolean", ""},
	} {
		tree, err := ParseOne(c.sql, "postgres")
		if err != nil {
			t.Errorf("%s: %v", c.sql, err)
			continue
		}
		index, _ := tree.Args["this"].(*Expression)
		params, _ := index.Args["params"].(*Expression)
		storage, _ := params.Args["with_storage"].([]*Expression)
		if len(storage) != 1 {
			t.Errorf("%s: %d settings, want 1", c.sql, len(storage))
			continue
		}
		value, _ := storage[0].Args["value"].(*Expression)
		if value == nil || value.Class != c.class {
			t.Errorf("%s: value is %v, want %s", c.sql, value, c.class)
			continue
		}
		if c.text != "" {
			if got, _ := value.Args["this"].(string); got != c.text {
				t.Errorf("%s: value is %q, want %q", c.sql, got, c.text)
			}
		}
	}
	// A name with a qualifier or quotes is not a bare word.
	for _, e := range []*Expression{
		New("Column", Arg{"this", New("Identifier", Arg{"this", "a"}, Arg{"quoted", true})}),
		New("Column",
			Arg{"this", New("Identifier", Arg{"this", "a"}, Arg{"quoted", false})},
			Arg{"table", New("Identifier", Arg{"this", "t"}, Arg{"quoted", false})}),
		New("Column"),
	} {
		if _, ok := bareColumnName(e); ok {
			t.Errorf("%v was called a bare word", e.Args)
		}
	}
	// An EXCLUDE over nothing has nothing to write.
	cfg, _ := ConfigFor("postgres")
	g := &generator{cfg: cfg, tables: cfg.Tables, dialect: "postgres"}
	if out := g.writeExcludeConstraint(New("ExcludeColumnConstraint")); g.err == nil {
		t.Errorf("an EXCLUDE over nothing wrote %q", out)
	}
}

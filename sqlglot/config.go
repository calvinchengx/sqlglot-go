package sqlglot

// FormatString is a prefixed string literal -- x'…', b'…', $$…$$ -- carrying the
// delimiter that closes it and the token type it produces.
type FormatString struct {
	End  string
	Type TokenType
}

type set = map[string]struct{}
type ttset = map[TokenType]struct{}

// Config is one dialect's resolved tokenizer tables. The fields mirror the
// reference's TokenizerCore constructor one-for-one; dialects_gen.go fills them
// from the pinned reference so a dialect override cannot be missed by hand.
type Config struct {
	Name          string
	SingleTokens  map[string]TokenType
	Keywords      map[string]TokenType
	Quotes        map[string]string
	FormatStrings map[string]FormatString
	Identifiers   map[string]string
	// Comments maps a start delimiter to its end. An empty end means the
	// comment runs to end of line -- the reference stores None there.
	Comments                         map[string]string
	StringEscapes                    set
	ByteStringEscapes                set
	IdentifierEscapes                set
	EscapeFollowChars                set
	Commands                         ttset
	CommandPrefixTokens              ttset
	NestedComments                   bool
	HintStart                        string
	TokensPrecedingHint              ttset
	HasBitStrings                    bool
	HasHexStrings                    bool
	NumericLiterals                  map[string]string
	VarSingleTokens                  set
	StringEscapesAllowedInRawStrings bool
	HeredocTagIsIdentifier           bool
	HeredocStringAlternative         TokenType
	NumbersCanBeUnderscoreSeparated  bool
	NumbersCanHaveDecimals           bool
	IdentifiersCanStartWithDigit     bool
	UnescapedSequences               map[string]string

	trie *trieNode
}

// ConfigFor returns the tokenizer configuration for a dialect. The empty name
// is the dialect-neutral default, matching the reference's `read=None`.
func ConfigFor(dialect string) (*Config, bool) {
	c, ok := dialectConfigs[dialect]
	if !ok {
		return nil, false
	}
	if c.trie == nil {
		c.trie = c.buildTrie()
	}
	return c, true
}

// Dialects lists the dialects the port configures, neutral first.
func Dialects() []string {
	out := make([]string, 0, len(dialectConfigs))
	for _, d := range []string{"", "tsql", "postgres", "duckdb", "databricks"} {
		if _, ok := dialectConfigs[d]; ok {
			out = append(out, d)
		}
	}
	return out
}

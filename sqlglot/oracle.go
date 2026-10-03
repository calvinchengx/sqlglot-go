package sqlglot

import "strings"

// Oracle is registered beside the generated dialect map. Growing that
// map for one dialect is a verbosity regression on the whole of it.
// The vocabulary starts as the neutral one; each Oracle spelling is a
// later mechanism.
func init() {
	if src := parserTables[""]; src != nil {
		tables := *src
		// Oracle's (+) is a join mark on a column. The reference stamps
		// every column, false when the mark is absent.
		tables.SupportsColumnJoinMarks = true
		parserTables["oracle"] = &tables
	}
	base := dialectConfigs[""]
	if base == nil {
		return
	}
	cfg := *base
	cfg.Name = "oracle"
	cfg.trie = nil
	cfg.Tables = nil
	dialectConfigs["oracle"] = &cfg
}

// oracleSelectUnique reads SELECT UNIQUE as SELECT DISTINCT. UNIQUE
// stays the constraint keyword everywhere else, which is why this is
// not a tokenizer change.
func (p *parser) oracleSelectUnique(tt TokenType) bool {
	if p.dialect != "oracle" || tt != TokDISTINCT || p.index == 0 {
		return false
	}
	c := p.curr()
	if c == nil || c.Type != TokUNIQUE {
		return false
	}
	return strings.EqualFold(p.tokens[p.index-1].Text, "SELECT")
}

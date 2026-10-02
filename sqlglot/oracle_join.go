package sqlglot

import "maps"

// Oracle's (+) is one token, a join mark on the column it follows.
// The parser already stamps join_mark once that token exists.
func init() {
	cfg := dialectConfigs["oracle"]
	if cfg == nil {
		return
	}
	keys := maps.Clone(cfg.Keywords)
	keys["(+)"] = TokJOIN_MARKER
	cfg.Keywords = keys
	cfg.trie = nil
}

// tableAliasSep is the words between a table and its alias. Oracle writes
// `e e1`; every other dialect writes `e AS e1`.
func (g *generator) tableAliasSep() string {
	if g.dialect == "oracle" {
		return " "
	}
	return " AS "
}

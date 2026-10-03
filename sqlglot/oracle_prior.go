package sqlglot

// Oracle's PRIOR and CONNECT_BY_ROOT are prefixes on a column, not names.
// They bind tighter than a comparison, so PRIOR id = parent_id keeps the
// equality outside the prefix.
func (p *parser) oracleHierarchyPrefix() (*Expression, bool, error) {
	if p.dialect != "oracle" {
		return nil, false, nil
	}
	class := ""
	switch {
	case p.atWords("PRIOR"):
		class = "Prior"
	case p.atWords("CONNECT_BY_ROOT"):
		class = "ConnectByRoot"
	default:
		return nil, false, nil
	}
	p.advance()
	inner, err := p.parseJSONArrow()
	if err != nil || inner == nil {
		return nil, true, err
	}
	node := New(class, Arg{"this", inner})
	if p.tables.SupportsColumnJoinMarks {
		node.Set("join_mark", p.match(TokJOIN_MARKER))
	}
	return node, true, nil
}

func init() {
	spellOracleHierarchy()
}

// START WITH is a clause, and the reference's token for START is BEGIN.
// PRIOR and CONNECT_BY_ROOT are written as a word in front of the column.
func spellOracleHierarchy() {
	tables := parserTables["oracle"]
	cfg := dialectConfigs["oracle"]
	if tables == nil || cfg == nil || tables.SyntaxSQL == nil || cfg.Keywords == nil {
		return
	}
	for _, word := range []struct{ class, spell string }{
		{class: "Prior", spell: "PRIOR {this}"},
		{class: "ConnectByRoot", spell: "CONNECT_BY_ROOT {this}"},
	} {
		tables.SyntaxSQL[word.class] = []SyntaxTemplate{{
			Keys: []string{"this"}, Marked: []string{"this"}, Template: word.spell,
		}}
	}
	cfg.Keywords["START"] = TokBEGIN
	cfg.trie = nil
}

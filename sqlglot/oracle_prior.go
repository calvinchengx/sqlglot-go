package sqlglot

// Oracle's PRIOR and CONNECT_BY_ROOT are prefixes on a column, not names.
// They bind tighter than a comparison, so PRIOR id = parent_id keeps the
// equality outside the prefix.
func (p *parser) oracleHierarchyPrefix() (*Expression, bool, error) {
	class := ""
	switch {
	case p.atWords("PRIOR"):
		// PRIOR is a prefix only inside a CONNECT BY's own condition, or in
		// Oracle, where it is one wherever it stands
		// (Parser.NO_PAREN_FUNCTION_PARSERS, _parse_connect_with_prior).
		if p.dialect != "oracle" && !p.inConnectBy {
			return nil, false, nil
		}
		class = "Prior"
	case p.atWords("CONNECT_BY_ROOT"):
		// CONNECT_BY_ROOT is a no-paren function of the BASE parser, so it
		// stands in every dialect, not only Oracle
		// (Parser.NO_PAREN_FUNCTION_PARSERS).
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
	// PRIOR and CONNECT_BY_ROOT are written by writers of their own now, in
	// every dialect -- the reference spells them with base methods, not with a
	// per-dialect table.
	cfg.Keywords["START"] = TokBEGIN
	cfg.trie = nil
}

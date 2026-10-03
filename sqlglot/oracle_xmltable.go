package sqlglot

// PATH names where an XMLTABLE column is read from in the document.
// The same words are written for every dialect that has the constraint.
func init() {
	generators["PathColumnConstraint"] = writePathColumnConstraint
	// COLUMNS is a clause word inside XMLTABLE. The reference's token for
	// it is COLUMN, the same type as the singular word.
	if cfg := dialectConfigs["oracle"]; cfg != nil && cfg.Keywords != nil {
		cfg.Keywords["COLUMNS"] = TokCOLUMN
		cfg.trie = nil
	}
}

func writePathColumnConstraint(g *generator, e *Expression) string {
	return "PATH " + g.child(e, "this")
}

// oracleXMLPassing reads one value XMLTABLE passes in. Oracle stamps a
// join mark on that value, false when the (+) mark is not there.
func (p *parser) oracleXMLPassing() (*Expression, error) {
	col, err := p.parseColumn()
	if err != nil || col == nil || !p.tables.SupportsColumnJoinMarks {
		return col, err
	}
	if _, set := col.Args["join_mark"]; !set {
		col.Set("join_mark", false)
	}
	return col, nil
}

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
	// The mark is stamped only where it was WRITTEN: a passing value with
	// no `(+)` carries no join_mark at all.
	return p.parseColumn()
}

// oracleXMLTableExpr reads a bare XMLTABLE(...) as the table function it
// is. Anywhere else the expression is an ordinary term.
func (p *parser) oracleXMLTableExpr() (*Expression, error) {
	if p.dialect == "oracle" && p.atWords("XMLTABLE") {
		if n := p.next(); n != nil && n.Type == TokL_PAREN {
			// No join mark on the table itself: the reference stamps one on
			// each value PASSED IN, and none here (Parser._parse_xml_table).
			return p.parseXMLTable()
		}
	}
	return p.parseTerm()
}

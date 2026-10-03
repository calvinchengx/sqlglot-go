package sqlglot

// Oracle writes an XML element as XMLELEMENT(EVALNAME expr) when the name
// is computed, and as XMLELEMENT(NAME tag, ...) when the tag is written out.
func init() {
	spellOracleXMLElement(parserTables["oracle"])
}

func spellOracleXMLElement(tables *ParserTables) {
	if tables == nil || tables.SyntaxSQL == nil {
		return
	}
	computed := SyntaxTemplate{
		Keys:     []string{"evalname", "this"},
		Marked:   []string{"this"},
		Required: []FuncConst{{Key: "evalname", Value: true}, {Key: "expressions", Value: false}},
		Template: "XMLELEMENT(EVALNAME {this})",
	}
	tagged := SyntaxTemplate{
		Keys:     []string{"expressions", "this"},
		Marked:   []string{"this", "expressions"},
		Template: "XMLELEMENT(NAME {this}, {expressions})",
	}
	empty := SyntaxTemplate{
		Keys:     []string{"this"},
		Marked:   []string{"this"},
		Required: []FuncConst{{Key: "expressions", Value: false}},
		Template: "XMLELEMENT(NAME {this})",
	}
	tables.SyntaxSQL["XMLElement"] = []SyntaxTemplate{computed, tagged, empty}
}

package sqlglot

func init() {
	generators["JSONTable"] = (*generator).writeJSONTable
	generators["JSONColumnDef"] = (*generator).writeJSONColumnDef
	parserTables["oracle"].SyntaxSQL["JSONSchema"] = []SyntaxTemplate{{
		Keys:     []string{"expressions"},
		Marked:   []string{"expressions"},
		Template: "COLUMNS({expressions})",
	}}
	parserTables["oracle"].TypeSQL["TEXT"] = "CLOB"
}

func (p *parser) oracleJSONOn(on string) string {
	if p.atWords("ERROR", "ON", on) {
		p.advance()
		p.advance()
		p.advance()
		return "ERROR ON " + on
	}
	if p.atWords("NULL", "ON", on) {
		p.advance()
		p.advance()
		p.advance()
		return "NULL ON " + on
	}
	return ""
}

func (g *generator) writeJSONTable(e *Expression) string {
	if out, ok := g.syntaxTemplate(e); ok {
		return out
	}
	this := g.child(e, "this")
	path := g.child(e, "path")
	if path != "" {
		path = ", " + path
	}
	handling := ""
	if words, _ := e.Args["error_handling"].(string); words != "" {
		handling += " " + words
	}
	if words, _ := e.Args["empty_handling"].(string); words != "" {
		handling += " " + words
	}
	return "JSON_TABLE(" + this + path + handling + " " + g.child(e, "schema") + ")"
}

func (g *generator) writeJSONColumnDef(e *Expression) string {
	if out, ok := g.syntaxTemplate(e); ok {
		return out
	}
	if schema := g.child(e, "nested_schema"); schema != "" {
		path := g.child(e, "path")
		if path != "" {
			path = " PATH " + path
		}
		return "NESTED" + path + " " + schema
	}
	kind := g.child(e, "kind")
	if kind != "" {
		kind = " " + kind
	}
	format := ""
	if marked, _ := e.Args["format_json"].(bool); marked {
		format = " FORMAT JSON"
	}
	path := g.child(e, "path")
	if path != "" {
		path = " PATH " + path
	}
	return g.child(e, "this") + kind + format + path
}

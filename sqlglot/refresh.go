package sqlglot

// parseRefresh reads REFRESH [EXTERNAL TABLE | TABLE | MATERIALIZED VIEW] target.
// A REFRESH with no kind and a target that is not a string is the whole
// statement kept as a command: building a Refresh there would name a table
// the statement never named.
func (p *parser) parseRefresh() (*Expression, error) {
	start := *p.curr()
	p.advance()
	kind := ""
	switch {
	case p.takeWords("EXTERNAL", "TABLE"):
		kind = "EXTERNAL TABLE"
	case p.match(TokTABLE):
		kind = "TABLE"
	case p.takeWords("MATERIALIZED", "VIEW"):
		kind = "MATERIALIZED VIEW"
	}
	var this *Expression
	if p.at(TokSTRING) {
		tok := p.curr()
		p.advance()
		this = New("Literal", Arg{"this", tok.Text}, Arg{"is_string", true})
	} else {
		table, err := p.parseTable()
		if err != nil {
			return nil, err
		}
		this = table
	}
	if kind == "" && this.Class != "Literal" {
		return p.parseAsCommand(start), nil
	}
	return New("Refresh", Arg{"this", this}, Arg{"kind", kind}), nil
}

func (g *generator) writeRefresh(e *Expression) string {
	this := g.child(e, "this")
	target, _ := e.Args["this"].(*Expression)
	if target != nil && target.Class == "Literal" {
		return "REFRESH " + this
	}
	kind, _ := e.Args["kind"].(string)
	if kind == "" {
		return "REFRESH " + this
	}
	return "REFRESH " + kind + " " + this
}

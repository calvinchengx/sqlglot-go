package sqlglot

// writeNationalAsString writes a DAX national string as N followed by
// the dialect's own string spelling. Other dialects keep the shared writer.
func (g *generator) writeNationalAsString(e *Expression) string {
	if g.dialect != "dax" {
		return g.writeNational(e)
	}
	quoted := g.writeStringLiteral(New("Literal",
		Arg{"this", e.Args["this"]},
		Arg{"is_string", true},
	))
	return "N" + quoted
}

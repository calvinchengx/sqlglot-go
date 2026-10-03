package sqlglot

// Oracle writes an aggregate as MIN(x) KEEP (DENSE_RANK FIRST ORDER BY y).
// KEEP is a window whose name is the word KEEP, and FIRST is a flag that
// LAST sets false. A following OVER wraps that window.
func (p *parser) oracleKeepOperand() (*Expression, error) {
	return p.oracleKeep(p.parseFactorOperand())
}

func (p *parser) oracleKeep(this *Expression, err error) (*Expression, error) {
	if err != nil || this == nil || p.dialect != "oracle" || !p.atWords("KEEP") {
		return this, err
	}
	if n := p.next(); n == nil || n.Type != TokL_PAREN {
		return this, nil
	}
	p.advance()
	p.advance()
	alias, aerr := p.parseIdentifier()
	if aerr != nil {
		return nil, aerr
	}
	var first any
	switch {
	case p.atWords("FIRST"):
		first = true
		p.advance()
	case p.atWords("LAST"):
		first = false
		p.advance()
	default:
		return nil, p.unsupported("KEEP without FIRST or LAST")
	}
	if !p.match(TokORDER_BY) {
		return nil, p.unsupported("KEEP without ORDER BY")
	}
	order, oerr := p.parseOrder()
	if oerr != nil {
		return nil, oerr
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed KEEP clause")
	}
	// The join mark belongs on the finished column expression. It was
	// stamped on the call before KEEP was visible.
	_, marked := this.Args["join_mark"]
	if marked {
		this.Set("join_mark", nil)
	}
	out := New("Window",
		Arg{"this", this},
		Arg{"order", order},
		Arg{"alias", alias},
		Arg{"over", "KEEP"},
		Arg{"first", first})
	if p.at(TokOVER) {
		outer, werr := p.parseWindow(out)
		if werr != nil {
			return nil, werr
		}
		out = outer
	}
	if marked {
		out.Set("join_mark", false)
	}
	return out, nil
}

func init() {
	prev := generators["Window"]
	generators["Window"] = func(g *generator, e *Expression) string {
		return writeOracleKeep(g, e, prev)
	}
}

// writeOracleKeep writes KEEP (DENSE_RANK FIRST ORDER BY ...) in place of
// the OVER spelling. Any other window is the one already recorded.
func writeOracleKeep(g *generator, e *Expression, prev func(*generator, *Expression) string) string {
	if g.dialect != "oracle" {
		return prev(g, e)
	}
	over, _ := e.Args["over"].(string)
	if over != "KEEP" {
		return prev(g, e)
	}
	first, ok := e.Args["first"].(bool)
	if !ok {
		return prev(g, e)
	}
	word := "LAST"
	if first {
		word = "FIRST"
	}
	return g.child(e, "this") + " KEEP (" + g.child(e, "alias") + " " + word + " " + g.child(e, "order") + ")"
}

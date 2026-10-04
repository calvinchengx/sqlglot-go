package sqlglot

import "strings"

func init() {
	generators["Into"] = (*generator).writeOracleSelectInto
}

func (g *generator) writeOracleSelectInto(e *Expression) string {
	if g.dialect == "oracle" {
		if cols, _ := e.Args["expressions"].([]*Expression); len(cols) > 0 {
			word := "INTO "
			if bulk, _ := e.Args["bulk_collect"].(bool); bulk {
				word = "BULK COLLECT INTO "
			}
			return word + g.list(e)
		}
	}
	return g.writeOracleInto(e)
}

func (p *parser) readSelectInto(sel *Expression) error {
	if !p.at(TokINTO) {
		return nil
	}
	if p.oracleCommaInto() {
		return p.readOracleCommaInto(sel)
	}
	p.advance()
	temporary := p.match(TokTEMPORARY)
	unlogged := false
	if c := p.curr(); c != nil && c.Type == TokVAR && strings.EqualFold(c.Text, "UNLOGGED") {
		p.advance()
		unlogged = true
	}
	target, err := p.parseTable()
	if err != nil {
		return err
	}
	sel.Set("into", New("Into", Arg{"this", target},
		Arg{"temporary", temporary || namesATemporaryTable(target)},
		Arg{"unlogged", unlogged}))
	return nil
}

func (p *parser) oracleCommaInto() bool {
	if p.dialect != "oracle" || p.index+2 >= len(p.tokens) {
		return false
	}
	name := p.tokens[p.index+1]
	comma := p.tokens[p.index+2]
	if name.Type != TokVAR && name.Type != TokIDENTIFIER {
		return false
	}
	return comma.Type == TokCOMMA
}

func (p *parser) readOracleCommaInto(sel *Expression) error {
	p.advance()
	cols, err := p.oracleIntoColumns()
	if err != nil {
		return err
	}
	into := New("Into")
	into.Set("bulk_collect", false)
	into.Set("expressions", cols)
	sel.Set("into", into)
	return nil
}

func (p *parser) oracleIntoColumns() ([]*Expression, error) {
	col, err := p.parseColumn()
	if err != nil {
		return nil, err
	}
	if _, set := col.Args["join_mark"]; !set {
		col.Set("join_mark", false)
	}
	if !p.match(TokCOMMA) {
		return []*Expression{col}, nil
	}
	rest, err := p.oracleIntoColumns()
	if err != nil {
		return nil, err
	}
	return append([]*Expression{col}, rest...), nil
}

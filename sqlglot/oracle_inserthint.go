package sqlglot

import "strings"

func init() {
	generators["Insert"] = (*generator).writeOracleInsert
}

func (g *generator) writeOracleInsert(e *Expression) string {
	sql := g.writeInsert(e)
	if g.dialect != "oracle" {
		return sql
	}
	hint := g.child(e, "hint")
	if hint == "" || !strings.HasPrefix(sql, "INSERT ") {
		return sql
	}
	return "INSERT " + hint + " " + strings.TrimPrefix(sql, "INSERT ")
}

func (p *parser) oracleInsertHint() (*Expression, error) {
	if p.dialect != "oracle" || !p.at(TokHINT) {
		return nil, nil
	}
	text := p.curr().Text
	p.advance()
	return p.parseHint(text)
}

func (p *parser) oracleInsertAlias(table *Expression) error {
	if !p.oracleBareInsertAlias() {
		return nil
	}
	id, err := p.parseIdentifier()
	if err != nil {
		return err
	}
	table.Set("alias", New("TableAlias", Arg{"this", id}, Arg{"columns", nil}))
	return nil
}

func (p *parser) oracleBareInsertAlias() bool {
	if p.dialect != "oracle" || !p.atAliasName() {
		return false
	}
	if p.atWords("VALUES") || p.atWords("REPLACE") || p.atWords("DEFAULT") || p.at(TokSELECT) || p.at(TokWITH) {
		return false
	}
	nxt := p.next()
	if nxt == nil {
		return false
	}
	switch nxt.Type {
	case TokL_PAREN, TokSELECT, TokWITH:
		return true
	}
	return strings.EqualFold(nxt.Text, "VALUES")
}

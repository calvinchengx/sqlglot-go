package sqlglot

import "strings"

// objectAggInstalled records that the Oracle JSON_OBJECTAGG writer is in
// place. The install happens on the first expression, not from an init.
var objectAggInstalled bool

// disjunctionOrObjectAgg is parseAssignment's first step. Oracle's
// JSON_OBJECTAGG(KEY … VALUE …) is not a call the generic reader accepts.
func (p *parser) disjunctionOrObjectAgg() (*Expression, error) {
	if !objectAggInstalled {
		objectAggInstalled = true
		generators["JSONObjectAgg"] = (*generator).writeObjectAgg
	}
	if p.oracleObjectAggAhead() {
		return p.readOracleObjectAgg()
	}
	return p.parseDisjunction()
}

func (p *parser) oracleObjectAggAhead() bool {
	if p.dialect != "oracle" || !p.atWords("JSON_OBJECTAGG") || p.index+2 >= len(p.tokens) {
		return false
	}
	open := p.tokens[p.index+1].Type == TokL_PAREN
	key := strings.EqualFold(p.tokens[p.index+2].Text, "KEY")
	return open && key
}

func (p *parser) readOracleObjectAgg() (*Expression, error) {
	p.advance()
	opened := p.match(TokL_PAREN)
	pair, err := p.parseJSONKeyValue()
	closed := opened && err == nil && p.match(TokR_PAREN)
	node := New("JSONObjectAgg")
	if pair != nil {
		node.Set("expressions", []*Expression{pair})
	}
	node.Set("return_type", false)
	node.Set("encoding", false)
	if p.tables.SupportsColumnJoinMarks {
		node.Set("join_mark", false)
	}
	if err != nil {
		return nil, err
	}
	if !closed {
		return nil, p.unsupported("unclosed JSON_OBJECTAGG")
	}
	return node, nil
}

func (g *generator) writeObjectAgg(e *Expression) string {
	if g.dialect == "oracle" {
		return g.appendParen("JSON_OBJECTAGG", g.list(e))
	}
	return g.spell(e)
}

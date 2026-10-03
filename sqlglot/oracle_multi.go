package sqlglot

import "strings"

func init() {
	spellOracleMultiInsert()
}

// INSERT ALL and INSERT FIRST write several tables from one query.
func spellOracleMultiInsert() {
	generators["MultitableInserts"] = (*generator).writeMultitableInserts
}

// oracleMultiInsert reads INSERT ALL and INSERT FIRST. A plain INSERT
// is left for the ordinary statement reader.
func (p *parser) oracleMultiInsert() (*Expression, bool, error) {
	if p.dialect != "oracle" || !p.at(TokINSERT) {
		return nil, false, nil
	}
	nxt := p.next()
	if nxt == nil {
		return nil, false, nil
	}
	kind := strings.ToUpper(nxt.Text)
	if kind != "ALL" && kind != "FIRST" {
		return nil, false, nil
	}
	p.advance()
	leading := p.takeComments()
	p.advance()

	var branches []*Expression
	for p.curr() != nil && !p.at(TokSELECT) && !p.at(TokWITH) {
		var cond *Expression
		isElse := false
		switch {
		case p.atWords("WHEN"):
			p.advance()
			var err error
			cond, err = p.parseExpression()
			if err != nil {
				return nil, true, err
			}
			if !p.match(TokTHEN) {
				return nil, true, p.unsupported("WHEN without THEN")
			}
		case p.atWords("ELSE"):
			p.advance()
			isElse = true
		case p.at(TokINTO):
		default:
			return nil, true, p.unsupported("INSERT ALL without INTO")
		}
		if !p.at(TokINTO) {
			return nil, true, p.unsupported("INSERT ALL without INTO")
		}
		for p.at(TokINTO) {
			ins, err := p.parseIntoInsert()
			if err != nil {
				return nil, true, err
			}
			branch := New("ConditionalInsert", Arg{"this", ins})
			if cond != nil {
				branch.Set("expression", cond)
				cond = nil
			}
			branch.Set("else_", isElse)
			isElse = false
			branches = append(branches, branch)
		}
	}
	source, err := p.parseQuery()
	if err != nil {
		return nil, true, err
	}
	return putComments(New("MultitableInserts",
		Arg{"kind", kind},
		Arg{"expressions", branches},
		Arg{"source", source},
	), leading), true, nil
}

// parseIntoInsert reads one INTO target of a multi-table insert. The
// source query that follows every branch is not part of this target.
func (p *parser) parseIntoInsert() (*Expression, error) {
	if !p.match(TokINTO) {
		return nil, p.unsupported("INSERT without INTO")
	}
	table, err := p.parseTableName()
	if err != nil {
		return nil, err
	}
	this := table
	if p.at(TokL_PAREN) && !p.opensAParenthesisedQuery() {
		columns, err := p.parseInsertColumns()
		if err != nil {
			return nil, err
		}
		this = New("Schema", Arg{"this", table}, Arg{"expressions", columns})
	}
	ins := New("Insert", Arg{"this", this})
	if p.atWords("VALUES") {
		expression, err := p.parseValues()
		if err != nil {
			return nil, err
		}
		ins.Set("expression", expression)
	}
	return ins, nil
}

func (g *generator) writeMultitableInserts(e *Expression) string {
	kind, _ := e.Args["kind"].(string)
	out := "INSERT " + kind
	branches, _ := e.Args["expressions"].([]*Expression)
	for _, branch := range branches {
		head := g.writeNestedInsert(branch)
		switch {
		case branch.Args["else_"] == true:
			out += " ELSE " + head
		case g.child(branch, "expression") != "":
			out += " WHEN " + g.child(branch, "expression") + " THEN " + head
		default:
			out += " " + head
		}
	}
	if src := g.child(e, "source"); src != "" {
		out += " " + src
	}
	return out
}

// writeNestedInsert writes one INTO target. The word INSERT belongs to
// the statement, once, not to each target.
func (g *generator) writeNestedInsert(e *Expression) string {
	ins, _ := e.Args["this"].(*Expression)
	if ins == nil {
		return ""
	}
	was := g.inColumnList
	g.inColumnList = true
	target := g.child(ins, "this")
	g.inColumnList = was
	body := g.child(ins, "expression")
	if body == "" {
		return "INTO " + target
	}
	return "INTO " + target + " " + body
}

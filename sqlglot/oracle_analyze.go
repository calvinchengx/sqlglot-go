package sqlglot

import "strings"

// Oracle's ANALYZE names an index or a cluster, and it validates, deletes
// statistics, or lists chained rows. Those clauses are the reference's own
// expression parsers, so the writers are the reference's too.

func init() {
	generators["AnalyzeValidate"] = (*generator).writeAnalyzeValidate
	generators["AnalyzeDelete"] = (*generator).writeAnalyzeDelete
	generators["AnalyzeListChainedRows"] = (*generator).writeAnalyzeListChainedRows
}

func (p *parser) parseAnalyzeNamedSubject() (string, []*Expression, error) {
	word := strings.ToUpper(p.curr().Text)
	switch {
	case word == "DATABASE":
		p.advance()
		db, err := p.parseIdentifier()
		if err != nil {
			return "", nil, err
		}
		return "DATABASE", []*Expression{New("Table", Arg{"db", db})}, nil
	case p.dialect == "oracle" && (word == "INDEX" || word == "CLUSTER"):
		p.advance()
		table, err := p.parseTableName()
		switch {
		case err != nil:
			return "", nil, err
		default:
			return word, []*Expression{table}, nil
		}
	default:
		return "", nil, p.unsupported("ANALYZE of something other than tables")
	}
}

func (p *parser) readAnalyzePartition(node *Expression) error {
	sub := p.atUnquotedWord("SUBPARTITION")
	open := (sub || p.atWords("PARTITION")) && p.next() != nil && p.next().Type == TokL_PAREN
	if !open {
		return nil
	}
	p.advance()
	members, err := p.parseParenthesisedList()
	switch {
	case err != nil:
		return err
	default:
		node.Set("partition", New("Partition",
			Arg{"subpartition", sub}, Arg{"expressions", members}))
		return nil
	}
}

func (p *parser) readAnalyzeExpression(node *Expression) error {
	var expr *Expression
	var err error
	switch {
	case p.dialect == "oracle" && p.atWords("VALIDATE"):
		expr, err = p.parseAnalyzeValidate()
	case p.dialect == "oracle" && p.atWords("DELETE"):
		expr, err = p.parseAnalyzeDelete()
	case p.dialect == "oracle" && p.atWords("LIST"):
		expr, err = p.parseAnalyzeList()
	default:
		return nil
	}
	switch {
	case err != nil:
		return err
	default:
		node.Set("expression", expr)
		return nil
	}
}

func (p *parser) parseAnalyzeValidate() (*Expression, error) {
	p.advance()
	kind, this, into, err := p.analyzeValidateBody()
	if err != nil {
		return nil, err
	}
	args := []Arg{{"kind", kind}}
	if this != "" {
		args = append(args, Arg{"this", this})
	}
	if into != nil {
		args = append(args, Arg{"expression", into})
	}
	return New("AnalyzeValidate", args...), nil
}

func (p *parser) analyzeValidateBody() (string, string, *Expression, error) {
	switch {
	case p.atWords("REF", "UPDATE"):
		p.advance()
		p.advance()
		this := "UPDATE"
		if p.atWords("SET", "DANGLING", "TO", "NULL") {
			p.advance()
			p.advance()
			p.advance()
			p.advance()
			this = "UPDATE SET DANGLING TO NULL"
		}
		return "REF", this, nil, nil
	case p.atWords("STRUCTURE"):
		p.advance()
		return p.analyzeStructure()
	default:
		return "", "", nil, p.unsupported("ANALYZE VALIDATE")
	}
}

func (p *parser) analyzeStructure() (string, string, *Expression, error) {
	switch {
	case p.atWords("CASCADE", "FAST"):
		p.advance()
		p.advance()
		return "STRUCTURE", "CASCADE FAST", nil, nil
	case p.atWords("CASCADE", "COMPLETE", "ONLINE"), p.atWords("CASCADE", "COMPLETE", "OFFLINE"):
		mode := strings.ToUpper(p.tokens[p.index+2].Text)
		p.advance()
		p.advance()
		p.advance()
		into, err := p.parseAnalyzeInto()
		return "STRUCTURE", "CASCADE COMPLETE " + mode, into, err
	default:
		return "STRUCTURE", "", nil, nil
	}
}

func (p *parser) parseAnalyzeDelete() (*Expression, error) {
	p.advance()
	kind := ""
	if p.atWords("SYSTEM") {
		p.advance()
		kind = "SYSTEM"
	}
	if !p.atWords("STATISTICS") {
		return nil, p.unsupported("ANALYZE DELETE")
	}
	p.advance()
	if kind == "" {
		return New("AnalyzeDelete"), nil
	}
	return New("AnalyzeDelete", Arg{"kind", kind}), nil
}

func (p *parser) parseAnalyzeList() (*Expression, error) {
	p.advance()
	if !p.atWords("CHAINED", "ROWS") {
		return nil, p.unsupported("ANALYZE LIST")
	}
	p.advance()
	p.advance()
	into, err := p.parseAnalyzeInto()
	switch {
	case err != nil:
		return nil, err
	case into != nil:
		return New("AnalyzeListChainedRows", Arg{"expression", into}), nil
	default:
		return New("AnalyzeListChainedRows"), nil
	}
}

func (p *parser) parseAnalyzeInto() (*Expression, error) {
	if !p.match(TokINTO) {
		return nil, nil
	}
	target, err := p.parseTableName()
	switch {
	case err != nil:
		return nil, err
	default:
		return New("Into", Arg{"this", target}, Arg{"bulk_collect", false}), nil
	}
}

func (g *generator) writeAnalyzeValidate(e *Expression) string {
	kind, _ := e.Args["kind"].(string)
	if kind == "" {
		return g.fail("ANALYZE VALIDATE without a kind")
	}
	out := "VALIDATE " + kind
	if this, _ := e.Args["this"].(string); this != "" {
		out += " " + this
	}
	if into := g.child(e, "expression"); into != "" {
		out += " " + into
	}
	return out
}

func (g *generator) writeAnalyzeDelete(e *Expression) string {
	return strings.Join(analyzeDeleteWords(e), " ")
}

func analyzeDeleteWords(e *Expression) []string {
	words := []string{"DELETE", "STATISTICS"}
	system, _ := e.Args["kind"].(string)
	if system == "" {
		return words
	}
	return []string{"DELETE", system, "STATISTICS"}
}

func (g *generator) writeAnalyzeListChainedRows(e *Expression) string {
	words := []string{"LIST", "CHAINED", "ROWS"}
	if into, ok := e.Args["expression"].(*Expression); ok {
		words = append(words, g.node(into))
	}
	return strings.Join(words, " ")
}

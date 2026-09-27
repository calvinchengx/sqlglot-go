package sqlglot

import "strings"

// attachMatchRecognize reads one MATCH_RECOGNIZE when that is the next
// clause. It lives outside the modifier switch: that function is already
// past the complexity bar, and another case there pushes it further.
func (p *parser) attachMatchRecognize(sel *Expression) error {
	if !p.at(TokMATCH_RECOGNIZE) {
		return nil
	}
	match, err := p.parseMatchRecognize()
	if err != nil {
		return err
	}
	return p.setOnce(sel, "match", match)
}

// parseMatchRecognize reads MATCH_RECOGNIZE (...), a clause of the query
// rather than of the table. Absent sections are absent on the node: an
// empty list would be a different tree from one that never named the section.
func (p *parser) parseMatchRecognize() (*Expression, error) {
	p.advance()
	if !p.match(TokL_PAREN) {
		return nil, p.unsupported("MATCH_RECOGNIZE without parentheses")
	}
	partition, err := p.matchRecognizePartition()
	if err != nil {
		return nil, err
	}
	var order *Expression
	if p.match(TokORDER_BY) {
		order, err = p.parseOrder()
		if err != nil {
			return nil, err
		}
	}
	measures, err := p.matchRecognizeMeasures()
	if err != nil {
		return nil, err
	}
	rows, err := p.matchRecognizeRows()
	if err != nil {
		return nil, err
	}
	after, err := p.matchRecognizeAfter()
	if err != nil {
		return nil, err
	}
	pattern, err := p.matchRecognizePattern()
	if err != nil {
		return nil, err
	}
	define, err := p.matchRecognizeDefine()
	if err != nil {
		return nil, err
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed MATCH_RECOGNIZE")
	}
	alias, err := p.parseTableAlias()
	if err != nil {
		return nil, err
	}
	args := make([]Arg, 0, 8)
	if len(partition) > 0 {
		args = append(args, Arg{"partition_by", partition})
	}
	if order != nil {
		args = append(args, Arg{"order", order})
	}
	if len(measures) > 0 {
		args = append(args, Arg{"measures", measures})
	}
	if rows != nil {
		args = append(args, Arg{"rows", rows})
	}
	if after != nil {
		args = append(args, Arg{"after", after})
	}
	if pattern != nil {
		args = append(args, Arg{"pattern", pattern})
	}
	if len(define) > 0 {
		args = append(args, Arg{"define", define})
	}
	if alias != nil {
		args = append(args, Arg{"alias", alias})
	}
	return New("MatchRecognize", args...), nil
}

func (p *parser) matchRecognizePartition() ([]*Expression, error) {
	if !p.match(TokPARTITION_BY) {
		return nil, nil
	}
	var cols []*Expression
	for {
		col, err := p.parseDisjunction()
		if err != nil {
			return nil, err
		}
		cols = append(cols, col)
		if !p.match(TokCOMMA) {
			return cols, nil
		}
	}
}

func (p *parser) matchRecognizeMeasures() ([]*Expression, error) {
	if !p.takeWords("MEASURES") {
		return nil, nil
	}
	var measures []*Expression
	for {
		measure, err := p.matchRecognizeMeasure()
		if err != nil {
			return nil, err
		}
		measures = append(measures, measure)
		if !p.match(TokCOMMA) {
			return measures, nil
		}
	}
}

// A measure may say FINAL or RUNNING. Neither word is stored as its own
// node: it is the string, or false when the measure named neither.
func (p *parser) matchRecognizeMeasure() (*Expression, error) {
	frame := any(false)
	if p.atWords("FINAL") || p.atWords("RUNNING") {
		frame = strings.ToUpper(p.curr().Text)
		p.advance()
	}
	this, err := p.parseExpression()
	if err != nil {
		return nil, err
	}
	this, err = p.parseAlias(this)
	if err != nil {
		return nil, err
	}
	return New("MatchRecognizeMeasure", Arg{"window_frame", frame}, Arg{"this", this}), nil
}

func (p *parser) matchRecognizeRows() (*Expression, error) {
	text := ""
	switch {
	case p.takeWords("ONE", "ROW", "PER", "MATCH"):
		text = "ONE ROW PER MATCH"
	case p.takeWords("ALL", "ROWS", "PER", "MATCH"):
		text = "ALL ROWS PER MATCH"
		switch {
		case p.takeWords("SHOW", "EMPTY", "MATCHES"):
			text += " SHOW EMPTY MATCHES"
		case p.takeWords("OMIT", "EMPTY", "MATCHES"):
			text += " OMIT EMPTY MATCHES"
		case p.takeWords("WITH", "UNMATCHED", "ROWS"):
			text += " WITH UNMATCHED ROWS"
		}
	default:
		return nil, nil
	}
	return New("Var", Arg{"this", text}), nil
}

func (p *parser) matchRecognizeAfter() (*Expression, error) {
	if !p.takeWords("AFTER", "MATCH", "SKIP") {
		return nil, nil
	}
	text := "AFTER MATCH SKIP"
	switch {
	case p.takeWords("PAST", "LAST", "ROW"):
		text += " PAST LAST ROW"
	case p.takeWords("TO", "NEXT", "ROW"):
		text += " TO NEXT ROW"
	case p.atWords("TO", "FIRST") || p.atWords("TO", "LAST"):
		p.advance()
		direction := strings.ToUpper(p.curr().Text)
		p.advance()
		name := p.curr()
		if name == nil {
			return nil, p.unsupported("AFTER MATCH SKIP without a pattern variable")
		}
		p.advance()
		text += " TO " + direction + " " + name.Text
	}
	return New("Var", Arg{"this", text}), nil
}

// The pattern is the source text between its parentheses, operators included.
// Joining the token texts would put spaces around `+` and `*` that were not written.
func (p *parser) matchRecognizePattern() (*Expression, error) {
	if !p.takeWords("PATTERN") {
		return nil, nil
	}
	if !p.match(TokL_PAREN) {
		return nil, p.unsupported("PATTERN without parentheses")
	}
	if p.at(TokR_PAREN) {
		return nil, p.unsupported("PATTERN without a variable")
	}
	start := p.curr()
	depth := 1
	var end *Token
	for p.curr() != nil && depth > 0 {
		tok := p.curr()
		if tok.Type == TokL_PAREN {
			depth++
		}
		if tok.Type == TokR_PAREN {
			depth--
		}
		if depth == 0 {
			break
		}
		end = tok
		p.advance()
	}
	if depth > 0 || end == nil {
		return nil, p.unsupported("unclosed PATTERN")
	}
	p.advance()
	return New("Var", Arg{"this", p.sql[start.Start : end.End+1]}), nil
}

func (p *parser) matchRecognizeDefine() ([]*Expression, error) {
	if !p.takeWords("DEFINE") {
		return nil, nil
	}
	var defs []*Expression
	for {
		name, err := p.parseIdentifier()
		if err != nil {
			return nil, err
		}
		def := name
		if p.match(TokALIAS) {
			expr, err := p.parseDisjunction()
			if err != nil {
				return nil, err
			}
			// The name stands on the left of AS. A normal alias is the other way around.
			def = New("Alias", Arg{"alias", name}, Arg{"this", expr})
		}
		defs = append(defs, def)
		if !p.match(TokCOMMA) {
			return defs, nil
		}
	}
}

func (p *parser) takeWords(words ...string) bool {
	if !p.atWords(words...) {
		return false
	}
	for range words {
		p.advance()
	}
	return true
}

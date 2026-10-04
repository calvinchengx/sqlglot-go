package sqlglot

import "strings"

func (p *parser) parseJSONObjectAt() (*Expression, error) {
	switch {
	case p.oracleJSONObjectSpecial():
		return p.parseOracleJSONObject()
	default:
		return p.parseJSONObject()
	}
}

func (p *parser) oracleJSONObjectSpecial() bool {
	if p.dialect != "oracle" {
		return false
	}
	returning := false
	for i := p.index; i < len(p.tokens); i++ {
		tok := p.tokens[i]
		switch {
		case tok.Type == TokR_PAREN:
			return false
		case tok.Type == TokIS:
			return true
		case strings.EqualFold(tok.Text, "RETURNING"):
			returning = true
		case !returning && i+1 < len(p.tokens) && strings.EqualFold(tok.Text, "FORMAT") && strings.EqualFold(p.tokens[i+1].Text, "JSON"):
			return true
		}
	}
	return false
}

func (p *parser) parseOracleJSONObject() (*Expression, error) {
	p.advance()
	p.advance()
	first, err := p.oracleJSONPair()
	if err != nil {
		return nil, err
	}
	second, err := p.oracleSecondJSONPair()
	if err != nil {
		return nil, err
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed JSON_OBJECT")
	}
	pairs := []*Expression{first}
	if second != nil {
		pairs = append(pairs, second)
	}
	return New("JSONObject",
		Arg{"expressions", pairs},
		Arg{"return_type", false},
		Arg{"encoding", false},
	), nil
}

func (p *parser) oracleSecondJSONPair() (*Expression, error) {
	if !p.match(TokCOMMA) {
		return nil, nil
	}
	return p.oracleJSONPair()
}

func (p *parser) oracleJSONPair() (*Expression, error) {
	var pair *Expression
	var err error
	if p.oracleKeyIsAhead() {
		pair, err = p.oracleKeyIsPair()
	} else {
		pair, err = p.parseJSONKeyValue()
	}
	if err != nil {
		return nil, err
	}
	if !p.atWords("FORMAT", "JSON") {
		return pair, nil
	}
	p.advance()
	p.advance()
	return New("FormatJson", Arg{"this", pair}), nil
}

func (p *parser) oracleKeyIsAhead() bool {
	if !p.atWords("KEY") || p.index+2 >= len(p.tokens) {
		return false
	}
	return p.tokens[p.index+2].Type == TokIS
}

func (p *parser) oracleKeyIsPair() (*Expression, error) {
	p.advance()
	key, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}
	if key != nil && key.Class == "Literal" {
		if _, set := key.Args["join_mark"]; !set {
			key.Set("join_mark", false)
		}
	}
	if !p.match(TokIS) {
		return nil, p.unsupported("a JSON key without IS")
	}
	value, err := p.parseExpression()
	if err != nil {
		return nil, err
	}
	return New("JSONKeyValue", Arg{"this", key}, Arg{"expression", value}), nil
}

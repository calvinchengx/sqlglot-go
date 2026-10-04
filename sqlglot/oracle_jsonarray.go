package sqlglot

import "strings"

// A rich Oracle JSON_ARRAY names FORMAT JSON, NULL ON NULL or RETURNING.
// Plain JSON_ARRAY(1, 2) never enters here, so its call stays anonymous.
var richJSONArrayInstalled bool

func (p *parser) expressionOrJSONArray() (*Expression, error) {
	if !richJSONArrayInstalled {
		richJSONArrayInstalled = true
		generators["JSONArray"] = (*generator).writeRichJSONArray
	}
	if !p.mentionsRichJSONArray() {
		return p.parseAssignment()
	}
	values, err := p.richJSONArrayValues()
	if err != nil {
		return nil, err
	}
	array := New("JSONArray")
	if word := p.atNullHandling(); word != "" {
		p.advance()
		p.advance()
		p.advance()
		array.Set("null_handling", word)
	}
	if p.atWords("RETURNING") || p.atWords("STRICT") {
		if err = p.oracleJSONArrayTail(array); err != nil {
			return nil, err
		}
	}
	array.Set("expressions", values)
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed JSON_ARRAY")
	}
	if p.tables.SupportsColumnJoinMarks {
		array.Set("join_mark", false)
	}
	return array, nil
}

func (p *parser) richJSONArrayValues() ([]*Expression, error) {
	p.advance()
	if !p.match(TokL_PAREN) {
		return nil, p.unsupported("JSON_ARRAY without parentheses")
	}
	var values []*Expression
	err := p.appendJSONArrayValue(&values)
	return values, err
}

func (p *parser) appendJSONArrayValue(values *[]*Expression) error {
	value, err := p.parseBitwise()
	if err != nil {
		return err
	}
	if p.atWords("FORMAT", "JSON") {
		p.advance()
		p.advance()
		value = New("FormatJson", Arg{"this", value})
	}
	*values = append(*values, value)
	if !p.match(TokCOMMA) || p.atNullHandling() != "" || p.atWords("RETURNING") || p.at(TokR_PAREN) {
		return nil
	}
	return p.appendJSONArrayValue(values)
}

func (p *parser) mentionsRichJSONArray() bool {
	if p.dialect != "oracle" || !p.atWords("JSON_ARRAY") {
		return false
	}
	var window strings.Builder
	limit := p.index + 48
	if limit > len(p.tokens) {
		limit = len(p.tokens)
	}
	for i := p.index; i < limit; i++ {
		window.WriteByte(' ')
		window.WriteString(strings.ToUpper(p.tokens[i].Text))
	}
	text := window.String()
	switch {
	case strings.Contains(text, " FORMAT JSON"):
		return true
	case strings.Contains(text, " NULL ON NULL"):
		return true
	default:
		return strings.Contains(text, " RETURNING ")
	}
}

func (g *generator) writeRichJSONArray(e *Expression) string {
	nulls, _ := e.Args["null_handling"].(string)
	ret := g.child(e, "return_type")
	strict, _ := e.Args["strict"].(bool)
	if g.dialect != "oracle" || nulls == "" || ret == "" || !strict {
		return g.spell(e)
	}
	return "JSON_ARRAY(" + g.list(e) + " " + nulls + " RETURNING " + ret + " STRICT)"
}

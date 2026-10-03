package sqlglot

import "strings"

// oracleTemporaryScope reads GLOBAL TEMPORARY and PRIVATE TEMPORARY as
// one property. The scope is the property's name, and TEMPORARY is how
// it is written, not a second property.
func (p *parser) oracleTemporaryScope() *Expression {
	if p.dialect != "oracle" {
		return nil
	}
	word := p.curr()
	if word == nil {
		return nil
	}
	scope := strings.ToUpper(word.Text)
	switch scope {
	case "GLOBAL", "PRIVATE":
	default:
		return nil
	}
	nxt := p.next()
	if nxt == nil || !strings.EqualFold(nxt.Text, "TEMPORARY") {
		return nil
	}
	p.advance()
	p.advance()
	return New("TemporaryProperty", Arg{"this", scope})
}

// temporaryKind is TEMPORARY, or GLOBAL TEMPORARY / PRIVATE TEMPORARY
// when the property names its scope.
func (g *generator) temporaryKind(e *Expression) string {
	properties, _ := e.Args["properties"].(*Expression)
	if properties == nil {
		return "TEMPORARY "
	}
	items, _ := properties.Args["expressions"].([]*Expression)
	for _, item := range items {
		if item == nil || item.Class != "TemporaryProperty" {
			continue
		}
		if scope, _ := item.Args["this"].(string); scope != "" {
			return scope + " TEMPORARY "
		}
	}
	return "TEMPORARY "
}

package sqlglot

func init() {
	spellOracleOrderSiblings()
}

// The reference's token for this clause is one word, ORDER_SIBLINGS_BY.
func spellOracleOrderSiblings() {
	cfg := dialectConfigs["oracle"]
	if cfg == nil || cfg.Keywords == nil {
		return
	}
	for _, word := range []struct {
		text string
		kind TokenType
	}{
		{text: "ORDER SIBLINGS BY", kind: TokORDER_SIBLINGS_BY},
	} {
		cfg.Keywords[word.text] = word.kind
	}
	cfg.trie = nil
}

// ORDER SIBLINGS BY orders the rows of a CONNECT BY walk. It is an ORDER
// whose siblings flag is set, not a different clause.
func (p *parser) oracleOrderSiblings(sel *Expression) error {
	if p.dialect != "oracle" || sel == nil || !p.at(TokORDER_SIBLINGS_BY) {
		return nil
	}
	p.advance()
	order, err := p.parseOrder()
	if err != nil {
		return err
	}
	order.Set("siblings", true)
	return p.setOnce(sel, "order", order)
}

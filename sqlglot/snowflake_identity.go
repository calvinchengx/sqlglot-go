package sqlglot

// Snowflake numbers a column with AUTOINCREMENT. A bare word is the
// auto-increment constraint. A start, an increment, or an order makes it
// the identity constraint, still written as AUTOINCREMENT.
func init() {
	spellSnowflakeIdentity()
}

func spellSnowflakeIdentity() {
	tables := parserTables["snowflake"]
	if tables == nil {
		return
	}
	tables.AutoIncrementSQL = "AUTOINCREMENT"
	generators["GeneratedAsIdentityColumnConstraint"] = (*generator).writeSnowflakeIdentity
}

// columnAutoIncrement reads the word the caller matched. Snowflake may
// follow it with a parenthesised pair or with START, INCREMENT, ORDER,
// and NOORDER in any mix. Any of those is the identity constraint. None
// of them is the bare auto-increment constraint every dialect already had.
func (p *parser) columnAutoIncrement() (*Expression, error) {
	p.advance()
	if p.dialect != "snowflake" {
		return New("AutoIncrementColumnConstraint"), nil
	}
	start, increment, err := p.snowflakeIdentityPair()
	if err != nil {
		return nil, err
	}
	var orderSet bool
	var ordered bool
	for {
		switch {
		case p.matchWords("START"):
			start, err = p.parseBitwise()
		case p.matchWords("INCREMENT"):
			increment, err = p.parseBitwise()
		case p.matchWords("NOORDER"):
			orderSet, ordered = true, false
		case p.matchWords("ORDER"):
			orderSet, ordered = true, true
		default:
			return snowflakeIdentityNode(start, increment, orderSet, ordered), nil
		}
		if err != nil {
			return nil, err
		}
	}
}

func (p *parser) snowflakeIdentityPair() (*Expression, *Expression, error) {
	if !p.match(TokL_PAREN) {
		return nil, nil, nil
	}
	start, err := p.parseBitwise()
	if err != nil {
		return nil, nil, err
	}
	var increment *Expression
	if p.match(TokCOMMA) {
		increment, err = p.parseBitwise()
		if err != nil {
			return nil, nil, err
		}
	}
	if !p.match(TokR_PAREN) {
		return nil, nil, p.unsupported("unclosed AUTOINCREMENT")
	}
	return start, increment, nil
}

func snowflakeIdentityNode(start, increment *Expression, orderSet, ordered bool) *Expression {
	if start == nil && increment == nil && !orderSet {
		return New("AutoIncrementColumnConstraint")
	}
	node := New("GeneratedAsIdentityColumnConstraint")
	if start != nil {
		node.Set("start", start)
	}
	if increment != nil {
		node.Set("increment", increment)
	}
	node.Set("this", false)
	if orderSet {
		node.Set("order", ordered)
	}
	return node
}

func (g *generator) writeSnowflakeIdentity(e *Expression) string {
	if g.dialect != "snowflake" {
		return g.writeGeneratedAsIdentity(e)
	}
	out := "AUTOINCREMENT"
	if start := g.child(e, "start"); start != "" {
		out += " START " + start
	}
	if increment := g.child(e, "increment"); increment != "" {
		out += " INCREMENT " + increment
	}
	if ordered, ok := e.Args["order"].(bool); ok {
		if ordered {
			out += " ORDER"
		} else {
			out += " NOORDER"
		}
	}
	return out
}

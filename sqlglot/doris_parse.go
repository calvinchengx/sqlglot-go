package sqlglot

import "strings"

// parseDorisProperty reads the property words Doris adds to MySQL's set:
// UNIQUE/KEY and DUPLICATE composite keys, DISTRIBUTED BY, BUILD, REFRESH and
// PROPERTIES. own is false when the cursor is not on one of them, so the
// caller falls through to the generated property table.
//
// These are the reference's own PROPERTY_PARSERS overrides (parsers/doris.py),
// which the probe cannot record because they are lambdas rather than a word and
// a shape.
func (p *parser) parseDorisProperty() (prop *Expression, own bool, err error) {
	switch {
	case p.atWords("UNIQUE"):
		p.advance() // UNIQUE
		e, err := p.parseCompositeKeyProperty("UniqueKeyProperty")
		return e, true, err
	case p.atWords("KEY"):
		p.advance() // KEY
		e, err := p.parseCompositeKeyProperty("UniqueKeyProperty")
		return e, true, err
	case p.atWords("DUPLICATE"):
		p.advance() // DUPLICATE
		e, err := p.parseCompositeKeyProperty("DuplicateKeyProperty")
		return e, true, err
	case p.atWords("DISTRIBUTED"):
		e, err := p.parseDistributedProperty()
		return e, true, err
	case p.atWords("BUILD"):
		p.advance() // BUILD
		word := p.curr()
		if word == nil {
			return nil, true, p.unsupported("BUILD naming nothing")
		}
		p.advance()
		return New("BuildProperty",
			Arg{"this", New("Var", Arg{"this", strings.ToUpper(word.Text)})}), true, nil
	case p.atWords("REFRESH"):
		e, err := p.parseRefreshTriggerProperty()
		return e, true, err
	}
	return nil, false, nil
}

// parseCompositeKeyProperty reads `[UNIQUE] KEY (<columns>)` -- the reference's
// _parse_composite_key_property. DUPLICATE KEY carries its own class; KEY and
// UNIQUE KEY both build a UniqueKeyProperty (the writer picks the word).
func (p *parser) parseCompositeKeyProperty(class string) (*Expression, error) {
	p.matchRoutineText("KEY")
	columns, err := p.parseWrappedCSV(p.parseIdentifier)
	if err != nil {
		return nil, err
	}
	return New(class, Arg{"expressions", columns}), nil
}

// parseDistributedProperty reads `DISTRIBUTED BY HASH (<cols>) [BUCKETS n]` or
// `DISTRIBUTED BY RANDOM [BUCKETS n]` -- the reference's
// _parse_distributed_property.
func (p *parser) parseDistributedProperty() (*Expression, error) {
	p.advance() // DISTRIBUTED
	kind := "HASH"
	var columns []*Expression
	switch {
	case p.matchWords("BY", "HASH"):
		columns, _ = p.parseWrappedCSV(p.parseIdentifier)
	case p.matchWords("BY", "RANDOM"):
		kind = "RANDOM"
	}
	var buckets *Expression
	if p.matchWords("BUCKETS") && !p.matchWords("AUTO") {
		buckets = p.tryParseNumberLiteral()
		if buckets == nil {
			return nil, p.unsupported("BUCKETS without a number")
		}
	}
	var order *Expression
	if p.matchWords("ORDER", "BY") {
		o, err := p.parseOrder()
		if err != nil {
			return nil, err
		}
		order = o
	}
	return New("DistributedByProperty",
		Arg{"expressions", columns}, Arg{"kind", kind},
		Arg{"buckets", buckets}, Arg{"order", order}), nil
}

// parseRefreshTriggerProperty reads a materialized view's REFRESH clause --
// the reference's _parse_refresh_property: `REFRESH <method> ON
// MANUAL|COMMIT|SCHEDULE [EVERY n unit] [STARTS 'when']`. Every argument is
// present even when the statement did not name it: the reference passes
// `every=False`, `unit=None` and `starts=False`, and the dumped trees agree
// only if those slots are there.
func (p *parser) parseRefreshTriggerProperty() (*Expression, error) {
	p.advance() // REFRESH
	method, err := p.parseDorisUpperVar()
	if err != nil {
		return nil, err
	}
	p.match(TokON)
	var kind any = false
	if p.atWords("MANUAL") || p.atWords("COMMIT") || p.atWords("SCHEDULE") {
		kind = strings.ToUpper(p.curr().Text)
		p.advance()
	}
	var every any = false
	var unit *Expression
	var starts any = false
	if p.matchWords("EVERY") {
		num := p.tryParseNumberLiteral()
		if num == nil {
			return nil, p.unsupported("EVERY without a number")
		}
		every = num
		unit, err = p.parseDorisAnyVar()
		if err != nil {
			return nil, err
		}
	}
	if p.matchWords("STARTS") {
		when := p.tryParseStringLiteral()
		if when == nil {
			return nil, p.unsupported("STARTS without a timestamp")
		}
		starts = when
	}
	return New("RefreshTriggerProperty",
		Arg{"method", method}, Arg{"kind", kind},
		Arg{"every", every}, Arg{"unit", unit}, Arg{"starts", starts}), nil
}

// parseDorisUpperVar reads one word into an upper-cased Var.
func (p *parser) parseDorisUpperVar() (*Expression, error) {
	c := p.curr()
	if c == nil {
		return nil, p.unsupported("a word expected")
	}
	p.advance()
	return New("Var", Arg{"this", strings.ToUpper(c.Text)}), nil
}

// parseDorisAnyVar reads one word into a Var, keeping its case.
func (p *parser) parseDorisAnyVar() (*Expression, error) {
	c := p.curr()
	if c == nil {
		return nil, p.unsupported("a word expected")
	}
	p.advance()
	return New("Var", Arg{"this", c.Text}), nil
}

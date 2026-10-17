package sqlglot

import "strings"

// ClickHouse gives a CREATE-table's own clauses a grammar the probe could not
// record as a single property shape: ENGINE takes a field, ORDER BY is an
// ordering, PARTITION BY a field, PRIMARY KEY a list with the parentheses
// optional, and SAMPLE BY a field
// (ClickHouseParser.PROPERTY_PARSERS, Parser.PROPERTY_PARSERS).
//
// own reports whether the words at the cursor were one of these; the property
// has then been read.
func (p *parser) parseClickhouseCreateProperty() (*Expression, bool, error) {
	if p.dialect != "clickhouse" {
		return nil, false, nil
	}
	switch {
	case p.atWords("ENGINE"):
		p.advance()
		p.match(TokEQ)
		field, err := p.parseClickhouseEngineField()
		if err != nil {
			return nil, false, err
		}
		return New("EngineProperty", Arg{"this", field}), true, nil
	case p.at(TokORDER_BY):
		p.advance()
		order, err := p.parseOrder()
		if err != nil {
			return nil, false, err
		}
		return order, true, nil
	case p.atWords("PARTITION BY"), p.atWords("PARTITIONED BY"):
		p.advance() // PARTITION BY, one token in the reference tokenizer
		p.match(TokEQ)
		// ClickHouse reads a whole ASSIGNMENT here, so a bare name is the
		// Column it is (ClickHouseParser._parse_partitioned_by).
		field, err := p.parseAssignment()
		if err != nil {
			return nil, false, err
		}
		return New("PartitionedByProperty", Arg{"this", field}), true, nil
	case p.atWords("PRIMARY KEY"):
		p.advance() // PRIMARY KEY, one token in the reference tokenizer
		key, err := p.parseClickhousePrimaryKey()
		if err != nil {
			return nil, false, err
		}
		return key, true, nil
	case p.atWords("ON", "CLUSTER"):
		// `CREATE TABLE test ON CLUSTER default (...)` names the cluster
		// between the name and the columns (ClickHouseParser._parse_on_property,
		// Parser.PROPERTY_PARSERS).
		p.advance()
		cluster, err := p.parseClickhouseOnCluster()
		if err != nil {
			return nil, false, err
		}
		return cluster, true, nil
	case p.atWords("SOURCE"), p.atWords("LAYOUT"):
		// A DICTIONARY's SOURCE and LAYOUT: a kind and a flat list of
		// key/value settings (Parser._parse_dict_property).
		word := strings.ToUpper(p.curr().Text)
		p.advance()
		prop, err := p.parseClickhouseDictProperty(word)
		if err != nil {
			return nil, false, err
		}
		return prop, true, nil
	case p.atWords("LIFETIME"), p.atWords("RANGE"):
		// A DICTIONARY's LIFETIME or RANGE: a minimum and a maximum
		// (Parser._parse_dict_range).
		word := strings.ToUpper(p.curr().Text)
		p.advance()
		span, err := p.parseClickhouseDictRange(word)
		if err != nil {
			return nil, false, err
		}
		return span, true, nil
	case p.atWords("SETTINGS"):
		// `SETTINGS k = v, ...` on a CREATE is a property of its own
		// (Parser.PROPERTY_PARSERS, Parser._parse_settings_property).
		p.advance()
		var settings []*Expression
		for {
			a, err := p.parseAssignment()
			if err != nil {
				return nil, false, err
			}
			settings = append(settings, a)
			if !p.match(TokCOMMA) {
				break
			}
		}
		return New("SettingsProperty", Arg{"expressions", settings}), true, nil
	case p.atWords("TTL"):
		// `TTL <expr> [DELETE|RECOMPRESS ..|TO DISK/|VOLUME ..], ...` with an
		// optional WHERE, GROUP BY and SET clause
		// (Parser.PROPERTY_PARSERS, Parser._parse_ttl).
		p.advance()
		ttl, err := p.parseClickhouseTTL()
		if err != nil {
			return nil, false, err
		}
		return ttl, true, nil
	case p.at(TokTABLE_SAMPLE) && p.next() != nil && strings.EqualFold(p.next().Text, "BY"):
		p.advance() // SAMPLE
		p.advance() // BY
		field, err := p.parseClickhouseField()
		if err != nil {
			return nil, false, err
		}
		return New("SampleProperty", Arg{"this", field}), true, nil
	case p.atWords("UUID"):
		p.advance()
		c := p.curr()
		if c == nil || c.Type != TokSTRING {
			return nil, false, p.unsupported("UUID without a string")
		}
		p.advance()
		return New("UuidProperty", Arg{"this",
			New("Literal", Arg{"this", c.Text}, Arg{"is_string", true})}), true, nil
	}
	return nil, false, nil
}

// parseClickhouseEngineField reads what an ENGINE names: a bare word is an
// Identifier, a call an Anonymous function
// (ClickHouseParser._parse_engine_property, Parser._parse_field).
func (p *parser) parseClickhouseEngineField() (*Expression, error) {
	if p.atIdentifier() && p.next() != nil && p.next().Type == TokL_PAREN {
		return p.parseFunction()
	}
	if p.atIdentifier() {
		c := p.curr()
		p.advance()
		return New("Identifier", Arg{"this", c.Text}, Arg{"quoted", c.Type == TokIDENTIFIER}), nil
	}
	return nil, p.unsupported("ENGINE without a name")
}

// parseClickhouseField reads one FIELD: a call (parsed as the reference reads
// it, an Anonymous function where it has no class of its own), a literal or a
// bracketed primary, or a bare name as an Identifier
// (Parser._parse_field).
func (p *parser) parseClickhouseField() (*Expression, error) {
	if p.atIdentifier() && p.next() != nil && p.next().Type == TokL_PAREN {
		return p.parseFunction()
	}
	if c := p.curr(); c != nil {
		switch c.Type {
		case TokL_PAREN, TokL_BRACKET, TokNUMBER, TokSTRING, TokTRUE, TokFALSE, TokNULL:
			return p.parsePrimary()
		}
	}
	if p.atIdentifier() {
		c := p.curr()
		p.advance()
		return New("Identifier", Arg{"this", c.Text}, Arg{"quoted", c.Type == TokIDENTIFIER}), nil
	}
	return nil, p.unsupported("a field")
}

// parseClickhousePrimaryKey reads `PRIMARY KEY a, b` or `PRIMARY KEY (a, b)`.
// ClickHouse makes the parentheses optional, where the base parser requires
// them (ClickHouseParser._parse_primary_key, Parser._parse_primary_key).
func (p *parser) parseClickhousePrimaryKey() (*Expression, error) {
	wrapped := p.match(TokL_PAREN)
	var exprs []*Expression
	for {
		part, err := p.parseClickhouseField()
		if err != nil {
			return nil, err
		}
		exprs = append(exprs, part)
		if !p.match(TokCOMMA) {
			break
		}
	}
	if wrapped && !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed PRIMARY KEY")
	}
	return New("PrimaryKey", Arg{"this", nil}, Arg{"expressions", exprs},
		Arg{"include", New("IndexParameters", Arg{"with_storage", false})},
		Arg{"options", nil}), nil
}

// parseClickhouseTTL reads a MergeTree TTL: one or more actions, then an
// optional WHERE, GROUP BY and SET (Parser._parse_ttl).
func (p *parser) parseClickhouseTTL() (*Expression, error) {
	var actions []*Expression
	for {
		this, err := p.parseBitwise()
		if err != nil {
			return nil, err
		}
		switch {
		case p.atWords("DELETE"):
			p.advance()
			this = New("MergeTreeTTLAction", Arg{"this", this}, Arg{"delete", true})
		case p.atWords("RECOMPRESS"):
			p.advance()
			value, err := p.parseBitwise()
			if err != nil {
				return nil, err
			}
			this = New("MergeTreeTTLAction", Arg{"this", this}, Arg{"recompress", value})
		case p.atWords("TO", "DISK"):
			p.advance()
			p.advance()
			value := p.tryParseStringLiteral()
			if value == nil {
				return nil, p.unsupported("TTL TO DISK without a string")
			}
			this = New("MergeTreeTTLAction", Arg{"this", this}, Arg{"to_disk", value})
		case p.atWords("TO", "VOLUME"):
			p.advance()
			p.advance()
			value := p.tryParseStringLiteral()
			if value == nil {
				return nil, p.unsupported("TTL TO VOLUME without a string")
			}
			this = New("MergeTreeTTLAction", Arg{"this", this}, Arg{"to_volume", value})
		}
		actions = append(actions, this)
		if !p.match(TokCOMMA) {
			break
		}
	}
	var where, group *Expression
	if p.at(TokWHERE) {
		p.advance()
		e, err := p.parseDisjunction()
		if err != nil {
			return nil, err
		}
		where = New("Where", Arg{"this", e})
	}
	if p.at(TokGROUP_BY) {
		p.advance()
		g, err := p.parseGroup()
		if err != nil {
			return nil, err
		}
		group = g
	}
	var aggregates []*Expression
	if group != nil && p.at(TokSET) {
		p.advance()
		for {
			item, err := p.parseAssignment()
			if err != nil {
				return nil, err
			}
			aggregates = append(aggregates, New("SetItem", Arg{"this", item}))
			if !p.match(TokCOMMA) {
				break
			}
		}
	}
	return New("MergeTreeTTL", Arg{"expressions", actions}, Arg{"where", where},
		Arg{"group", group}, Arg{"aggregates", aggregates}), nil
}

// parseClickhouseOnCluster reads `CLUSTER <name>` with the ON already consumed,
// where the name may be an identifier or a string
// (ClickHouseParser._parse_on_property, Parser.ON_CLUSTER).
func (p *parser) parseClickhouseOnCluster() (*Expression, error) {
	if !p.atWords("CLUSTER") {
		return nil, p.unsupported("ON without CLUSTER")
	}
	p.advance()
	if c := p.curr(); c != nil && c.Type == TokSTRING {
		p.advance()
		return New("OnCluster", Arg{"this",
			New("Literal", Arg{"this", c.Text}, Arg{"is_string", true})}), nil
	}
	name, err := p.parseIdentifier()
	if err != nil {
		return nil, err
	}
	return New("OnCluster", Arg{"this", name}), nil
}

// parseClickhouseDictProperty reads `KIND(key value ...)` with the word (SOURCE
// or LAYOUT) already consumed (Parser._parse_dict_property).
func (p *parser) parseClickhouseDictProperty(word string) (*Expression, error) {
	if !p.match(TokL_PAREN) {
		return nil, p.unsupported(word + " without a parenthesis")
	}
	kind := p.parseIDVarAny()
	kindName := ""
	if kind != nil {
		kindName = kind.Name()
	}
	var settings []*Expression
	if p.match(TokL_PAREN) {
		for !p.at(TokR_PAREN) {
			key := p.parseIDVarAny()
			value, err := p.parseDictValue()
			if err != nil {
				return nil, err
			}
			if key == nil && value == nil {
				break
			}
			settings = append(settings, New("DictSubProperty",
				Arg{"this", key}, Arg{"value", value}))
		}
		if !p.match(TokR_PAREN) {
			return nil, p.unsupported("unclosed " + word)
		}
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed " + word)
	}
	return New("DictProperty", Arg{"this", word}, Arg{"kind", kindName},
		Arg{"settings", settings}), nil
}

// parseDictValue reads one setting's value: a call, or a primary or a var. A
// bare word that is not a call is a Var, not the Column the expression reader
// would make (Parser._parse_primary_or_var).
func (p *parser) parseDictValue() (*Expression, error) {
	if p.atIdentifier() && p.next() != nil && p.next().Type == TokL_PAREN {
		return p.parseFunction()
	}
	if p.atIdentifier() {
		c := p.curr()
		p.advance()
		return New("Var", Arg{"this", c.Text}), nil
	}
	return p.parsePrimaryOrVar()
}

// parseClickhouseDictRange reads `(MIN a MAX b)` with LIFETIME already consumed
// (Parser._parse_dict_range).
func (p *parser) parseClickhouseDictRange(word string) (*Expression, error) {
	if !p.match(TokL_PAREN) {
		return nil, p.unsupported(word + " without a parenthesis")
	}
	var minE, maxE *Expression
	var err error
	if p.matchUnquotedWord("MIN") {
		if minE, err = p.parseDictRangeValue(); err != nil {
			return nil, err
		}
		p.matchUnquotedWord("MAX")
		if maxE, err = p.parseDictRangeValue(); err != nil {
			return nil, err
		}
	} else {
		if maxE, err = p.parseDictRangeValue(); err != nil {
			return nil, err
		}
		minE = New("Literal", Arg{"this", "0"}, Arg{"is_string", false})
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed " + word)
	}
	return New("DictRange", Arg{"this", word}, Arg{"min", minE}, Arg{"max", maxE}), nil
}

// parseDictRangeValue reads one bound of a DICTIONARY's RANGE or LIFETIME: a
// bare name is a Var, anything else a primary
// (Parser._parse_dict_range, `_parse_var() or _parse_primary()`).
func (p *parser) parseDictRangeValue() (*Expression, error) {
	if p.atIdentifier() {
		c := p.curr()
		p.advance()
		return New("Var", Arg{"this", c.Text}), nil
	}
	return p.parsePrimary()
}

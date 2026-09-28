package sqlglot

// RisingWave's CREATE SOURCE schema mixes a star, generated columns, and a
// watermark, then names included fields as properties of the source.

func (p *parser) atRisingWaveSchemaItem() bool {
	return p.dialect == "risingwave" && (p.at(TokSTAR) || p.atWords("WATERMARK"))
}

func (p *parser) atRisingWaveComputed() bool {
	return p.dialect == "risingwave" && p.at(TokALIAS)
}

func (p *parser) parseRisingWaveSchemaItem() (*Expression, bool, error) {
	if p.dialect != "risingwave" {
		return nil, false, nil
	}
	if p.at(TokSTAR) {
		p.advance()
		return New("Star"), true, nil
	}
	if !p.atWords("WATERMARK") {
		return nil, false, nil
	}
	p.advance()
	if !p.match(TokFOR) {
		return nil, true, p.unsupported("WATERMARK without FOR")
	}
	column, err := p.parseColumn()
	if err != nil {
		return nil, true, err
	}
	if !p.match(TokALIAS) {
		return nil, true, p.unsupported("WATERMARK without AS")
	}
	expr, err := p.parseExpression()
	if err != nil {
		return nil, true, err
	}
	return New("WatermarkColumnConstraint",
		Arg{"this", column}, Arg{"expression", expr}), true, nil
}

func (p *parser) parseIncludeProperty() (*Expression, error) {
	p.advance() // INCLUDE
	this, err := p.parseVarOrString()
	if err != nil {
		return nil, err
	}
	var column *Expression
	if !p.at(TokALIAS) {
		name, err := p.parseIdentifier()
		if err != nil {
			return nil, err
		}
		kind, err := p.parseColumnType()
		if err != nil {
			return nil, err
		}
		column = New("ColumnDef", Arg{"this", name}, Arg{"kind", kind})
	}
	if !p.match(TokALIAS) {
		return nil, p.unsupported("INCLUDE without AS")
	}
	alias, err := p.parseIdentifier()
	if err != nil {
		return nil, err
	}
	node := New("IncludeProperty", Arg{"this", this}, Arg{"alias", alias})
	if column != nil {
		node.Set("column_def", column)
	}
	return node, nil
}

func (p *parser) parseRisingWaveComputed() (*Expression, error) {
	p.advance() // AS
	expr, err := p.parseExpression()
	if err != nil {
		return nil, err
	}
	return New("ComputedColumnConstraint",
		Arg{"this", expr}, Arg{"persisted", false}), nil
}

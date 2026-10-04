package sqlglot

// Oracle reads CAST(... DEFAULT x ON CONVERSION ERROR) as a cast that
// keeps the fallback, and a DATE cast with a format as TO_DATE. The
// plain CAST path is unchanged.
func init() {
	generators["Cast"] = (*generator).writeCastDefault
}

func (p *parser) castOrConversion(try bool) (*Expression, error) {
	if try || p.dialect != "oracle" {
		return p.parseCast(try)
	}
	// A plain CAST has no DEFAULT. Reading it as a conversion fails, and
	// the index (and which comment was already given away) go back so the
	// ordinary cast reader sees the call exactly as it stood.
	saved, given := p.index, p.commentsFrom
	node, err := p.readConversionCast()
	if err == nil {
		return node, nil
	}
	p.index = saved
	p.commentsFrom = given
	return p.parseCast(false)
}

func (p *parser) readConversionCast() (*Expression, error) {
	p.advance()
	if !p.match(TokL_PAREN) {
		return nil, p.unsupported("CAST without parentheses")
	}
	this, err := p.parseExpression()
	if err != nil {
		return nil, err
	}
	if !p.match(TokALIAS) {
		return nil, p.unsupported("CAST without AS")
	}
	to, err := p.parseCollatedDataType()
	if err != nil {
		return nil, err
	}
	fallback, formatted, err := p.conversionFallback()
	if err != nil {
		return nil, err
	}
	if formatted {
		return p.conversionDate(this, to)
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed CAST")
	}
	cast := New("Cast", Arg{"this", this}, Arg{"to", to}, Arg{"default", fallback})
	cast.Type = to
	return p.keep(cast), nil
}

func (p *parser) conversionFallback() (fallback *Expression, formatted bool, err error) {
	if !p.match(TokDEFAULT) {
		return nil, false, p.unsupported("CAST without DEFAULT")
	}
	fallback, err = p.parseBitwise()
	if err != nil {
		return nil, false, err
	}
	if !p.atWords("ON", "CONVERSION", "ERROR") {
		return nil, false, p.unsupported("CAST DEFAULT without ON CONVERSION ERROR")
	}
	p.advance()
	p.advance()
	p.advance()
	return fallback, p.match(TokCOMMA), nil
}

func (p *parser) conversionDate(this, to *Expression) (*Expression, error) {
	kind, _ := to.Args["this"].(DataTypeKind)
	if kind != "DATE" {
		return nil, p.unsupported("CAST format for a non-date type")
	}
	raw := p.tryParseStringLiteral()
	if raw == nil {
		return nil, p.unsupported("CAST without a format")
	}
	text, _ := raw.Args["this"].(string)
	format := New("Literal",
		Arg{"this", formatTime(text, p.tables.TimeMapping)},
		Arg{"is_string", true})
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed CAST")
	}
	return p.keep(New("StrToDate", Arg{"this", this}, Arg{"format", format})), nil
}

func (g *generator) writeCastDefault(e *Expression) string {
	if e.Args["default"] == nil {
		return g.writeCast(e)
	}
	fallback := g.child(e, "default")
	return "CAST(" + g.child(e, "this") + " AS " + g.child(e, "to") + " DEFAULT " + fallback + " ON CONVERSION ERROR)"
}

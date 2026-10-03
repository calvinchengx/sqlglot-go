package sqlglot

// Oracle's CONVERT names a character set, not a cast. The value comes
// first, then the destination set, and a source set when one was given.
func init() {
	installOracleCharsetConvert(parserTables["oracle"])
}

func installOracleCharsetConvert(tables *ParserTables) {
	if tables == nil {
		return
	}
	syntax := make(map[string][]SyntaxTemplate, len(tables.SyntaxSQL)+1)
	for class, spells := range tables.SyntaxSQL {
		copied := make([]SyntaxTemplate, len(spells))
		copy(copied, spells)
		syntax[class] = copied
	}
	syntax["ConvertToCharset"] = []SyntaxTemplate{
		{
			Keys:     []string{"dest", "source", "this"},
			Marked:   []string{"dest", "source", "this"},
			Template: "CONVERT({this}, {dest}, {source})",
		},
		{
			Keys:     []string{"dest", "this"},
			Marked:   []string{"dest", "this"},
			Template: "CONVERT({this}, {dest})",
		},
	}
	tables.SyntaxSQL = syntax
}

func (p *parser) parseOracleCharsetConvert() (*Expression, error) {
	p.advance()
	p.advance()
	this, err := p.parseExpression()
	if err != nil {
		return nil, err
	}
	if !p.match(TokCOMMA) {
		return nil, p.unsupported("CONVERT without a character set")
	}
	dest, err := p.parseExpression()
	if err != nil {
		return nil, err
	}
	args := []Arg{{"this", this}, {"dest", dest}}
	if p.match(TokCOMMA) {
		source, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		args = append(args, Arg{"source", source})
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed CONVERT")
	}
	return New("ConvertToCharset", args...), nil
}

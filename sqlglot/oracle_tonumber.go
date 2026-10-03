package sqlglot

// Oracle's TO_NUMBER takes a value, an optional conversion default, and
// then a format and an NLS parameter. The default is written inside the
// call, before the format.
func init() {
	noteOracleToNumber(parserTables["oracle"])
}

// noteOracleToNumber sends TO_NUMBER through its own argument grammar and
// its own writer. The syntax table is already this dialect's copy.
func noteOracleToNumber(tables *ParserTables) {
	generators["ToNumber"] = (*generator).writeToNumber
	if tables == nil || tables.SyntaxFunctions == nil {
		return
	}
	tables.SyntaxFunctions["TO_NUMBER"] = struct{}{}
}

func (p *parser) parseOracleToNumber() (*Expression, error) {
	p.advance()
	if !p.match(TokL_PAREN) {
		return nil, p.unsupported("TO_NUMBER without parentheses")
	}
	value, err := p.parseBitwise()
	if err != nil {
		return nil, err
	}
	if value == nil {
		return nil, p.unsupported("TO_NUMBER without a value")
	}
	var fallback, format, nls *Expression
	if p.match(TokDEFAULT) {
		fallback, err = p.parseBitwise()
		if err != nil {
			return nil, err
		}
		if fallback == nil || !p.atWords("ON", "CONVERSION", "ERROR") {
			return nil, p.unsupported("TO_NUMBER DEFAULT without ON CONVERSION ERROR")
		}
		p.advance()
		p.advance()
		p.advance()
	}
	if p.match(TokCOMMA) {
		format, err = p.parseBitwise()
		if err != nil {
			return nil, err
		}
		if format == nil {
			return nil, p.unsupported("TO_NUMBER without a format")
		}
		if p.match(TokCOMMA) {
			nls, err = p.parseBitwise()
			if err != nil {
				return nil, err
			}
			if nls == nil {
				return nil, p.unsupported("TO_NUMBER without an NLS parameter")
			}
		}
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed TO_NUMBER")
	}
	call := New("ToNumber", Arg{"this", value})
	if format != nil {
		call.Set("format", format)
	}
	if nls != nil {
		call.Set("nlsparam", nls)
	}
	if fallback != nil {
		call.Set("default", fallback)
	}
	return call, nil
}

func (g *generator) writeToNumber(e *Expression) string {
	if g.dialect != "oracle" {
		return g.spell(e)
	}
	value := g.child(e, "this")
	if value == "" {
		return g.fail("ToNumber without a value")
	}
	format := g.child(e, "format")
	nls := g.child(e, "nlsparam")
	if nls != "" && format == "" {
		return g.fail("ToNumber with an NLS parameter and no format")
	}
	head := value
	if fallback := g.child(e, "default"); fallback != "" {
		head += " DEFAULT " + fallback + " ON CONVERSION ERROR"
	}
	args := head
	if format != "" {
		args += ", " + format
	}
	if nls != "" {
		args += ", " + nls
	}
	return "TO_NUMBER(" + args + ")"
}

package sqlglot

import "strings"

// Functions whose ARGUMENTS have their own grammar rather than being a
// comma-separated list. The reference keeps these in FUNCTION_PARSERS, and a
// name there that is not implemented here is missing grammar -- which is a
// different thing from a builder the probe cannot describe, and is labelled
// differently so the refusal counts say which.
//
// Entered with the name current and the opening parenthesis after it.
func (p *parser) parseSyntaxFunction(upper string) (*Expression, error) {
	switch upper {
	case "EXTRACT":
		return p.parseExtract()
	case "TRIM":
		return p.parseTrim()
	case "SUBSTRING", "SUBSTR":
		return p.parseSubstring()
	case "POSITION":
		return p.parsePosition()
	case "CONVERT":
		return p.parseConvert()
	case "STRING_AGG":
		return p.parseStringAgg()
	}
	return nil, p.unsupported("function " + upper + " with a syntax of its own")
}

// EXTRACT(MONTH FROM d): the unit is a bare word, recorded as a Var.
func (p *parser) parseExtract() (*Expression, error) {
	p.advance()
	p.advance()
	unit := p.curr()
	if unit == nil {
		return nil, p.unsupported("EXTRACT without a unit")
	}
	p.advance()
	if !p.match(TokFROM) {
		return nil, p.unsupported("EXTRACT without FROM")
	}
	this, err := p.parseExpression()
	if err != nil {
		return nil, err
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed EXTRACT")
	}
	return New("Extract",
		Arg{"this", New("Var", Arg{"this", strings.ToUpper(unit.Text)})},
		Arg{"expression", this}), nil
}

// TRIM([LEADING|TRAILING|BOTH] [chars] FROM target) -- and plain TRIM(x).
// Note the reference stores the TARGET as `this` and the characters as
// `expression`, which is the reverse of the order they are written in.
func (p *parser) parseTrim() (*Expression, error) {
	p.advance()
	p.advance()

	position := ""
	if c := p.curr(); c != nil && c.Type == TokVAR {
		switch strings.ToUpper(c.Text) {
		case "LEADING", "TRAILING", "BOTH":
			position = strings.ToUpper(c.Text)
			p.advance()
		}
	}

	first, err := p.parseExpression()
	if err != nil {
		return nil, err
	}

	var chars, target *Expression
	switch {
	case p.match(TokFROM):
		chars = first
		t, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		target = t
	case position != "":
		return nil, p.unsupported("TRIM with a position but no FROM")
	default:
		target = first
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed TRIM")
	}
	// Order matters: the reference records position BEFORE expression, and the
	// tree is compared in arg order.
	args := []Arg{{"this", target}}
	if position != "" {
		args = append(args, Arg{"position", position})
	}
	args = append(args, Arg{"expression", chars})
	return New("Trim", args...), nil
}

// SUBSTRING(x FROM a FOR b) and SUBSTRING(x, a, b) build the same node.
func (p *parser) parseSubstring() (*Expression, error) {
	p.advance()
	p.advance()
	this, err := p.parseExpression()
	if err != nil {
		return nil, err
	}
	var start, length *Expression
	if p.match(TokFROM) || p.match(TokCOMMA) {
		// parseBitwise, not parseExpression: `1 FOR 2` reads FOR as a range
		// operator at the full expression level and the FOR never arrives here.
		e, err := p.parseBitwise()
		if err != nil {
			return nil, err
		}
		start = e
		if p.match(TokFOR) || p.match(TokCOMMA) {
			e, err := p.parseExpression()
			if err != nil {
				return nil, err
			}
			length = e
		}
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed SUBSTRING")
	}
	return New("Substring", Arg{"this", this}, Arg{"start", start}, Arg{"length", length}), nil
}

// POSITION(needle IN haystack) -- the haystack is `this`, the needle `substr`,
// which is again the reverse of the written order.
func (p *parser) parsePosition() (*Expression, error) {
	p.advance()
	p.advance()
	// parseBitwise, not parseExpression: at the full level `a IN b` is an IN
	// predicate and the IN is gone before this function sees it.
	first, err := p.parseBitwise()
	if err != nil {
		return nil, err
	}
	if !p.match(TokIN) {
		return nil, p.unsupported("POSITION without IN")
	}
	haystack, err := p.parseExpression()
	if err != nil {
		return nil, err
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed POSITION")
	}
	return New("StrPosition", Arg{"this", haystack}, Arg{"substr", first}), nil
}

// CONVERT(type, x[, style]) -- the FIRST argument is a data type, which is why
// the ordinary argument parser cannot read this one: it would read VARCHAR(10)
// as a call to a function named VARCHAR.
func (p *parser) parseConvert() (*Expression, error) {
	p.advance()
	p.advance()
	to, err := p.parseDataType()
	if err != nil {
		return nil, err
	}
	if !p.match(TokCOMMA) {
		return nil, p.unsupported("CONVERT without a value")
	}
	value, err := p.parseExpression()
	if err != nil {
		return nil, err
	}
	args := []Arg{{"this", to}, {"expression", value}}
	if p.match(TokCOMMA) {
		style, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		args = append(args, Arg{"style", style})
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed CONVERT")
	}
	return New("Convert", args...), nil
}

// STRING_AGG(x, sep) is a GroupConcat. Its first argument may carry an
// ORDER BY, which the reference records by wrapping it in an Order -- not
// ported, so that form is refused rather than silently dropped.
func (p *parser) parseStringAgg() (*Expression, error) {
	p.advance()
	p.advance()
	this, err := p.parseExpression()
	if err != nil {
		return nil, err
	}
	if p.at(TokORDER_BY) {
		return nil, p.unsupported("STRING_AGG with an ORDER BY")
	}
	var separator *Expression
	if p.match(TokCOMMA) {
		sep, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		separator = sep
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed STRING_AGG")
	}
	return New("GroupConcat", Arg{"this", this}, Arg{"separator", separator}), nil
}

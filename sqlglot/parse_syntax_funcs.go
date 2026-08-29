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
	case "JSON_OBJECT":
		return p.parseJSONObject()
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

// STRING_AGG(x, sep) is a GroupConcat.
//
// Two things hang off its FIRST argument however they are written. DISTINCT
// wraps it, and an ORDER BY wraps whatever that produced -- and the ORDER BY
// is written after the SEPARATOR, so `STRING_AGG(DISTINCT x, ',' ORDER BY y)`
// is GroupConcat(Order(Distinct(x), y), ','). The reference reads the
// arguments first and then reaches back for the one it belongs to; so does
// this.
func (p *parser) parseStringAgg() (*Expression, error) {
	p.advance()
	p.advance()

	var args []*Expression
	distinct := p.match(TokDISTINCT)
	first, err := p.parseExpression()
	if err != nil {
		return nil, err
	}
	if distinct {
		// One expression, not the whole list: `STRING_AGG(DISTINCT x, ',')`
		// distinguishes x and takes ',' as the separator.
		first = New("Distinct", Arg{"expressions", []*Expression{first}})
	}
	args = append(args, first)
	for p.match(TokCOMMA) {
		next, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		args = append(args, next)
	}

	if p.at(TokORDER_BY) {
		p.advance()
		order, err := p.parseOrder()
		if err != nil {
			return nil, err
		}
		order.Set("this", args[0])
		args[0] = order
	}
	// A third argument is the overflow behaviour, which the reference reads
	// and then writes away -- `STRING_AGG(DISTINCT x, y, z)` comes back
	// without the z.
	if len(args) > 2 {
		return nil, p.unsupported("STRING_AGG with an overflow behaviour")
	}
	this := args[0]
	var separator *Expression
	if len(args) > 1 {
		separator = args[1]
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed STRING_AGG")
	}
	// The builder SWALLOWS a following `WITHIN GROUP (ORDER BY ...)` rather
	// than being wrapped by one -- but only for the names whose builder does,
	// which is a fact about the NAME and not the class it builds: Databricks
	// reads LISTAGG into this very class and does not fold it.
	//
	// The ordering is over the argument that was already there, so it takes
	// that argument as its own `this`: `STRING_AGG(x, ',') WITHIN GROUP
	// (ORDER BY y)` becomes GroupConcat(Order(x, y), ',').
	if _, folds := p.tables.WithinGroupFolds["STRING_AGG"]; folds && p.atWords("WITHIN", "GROUP") {
		p.advance()
		p.advance()
		if !p.match(TokL_PAREN) {
			return nil, p.unsupported("WITHIN GROUP without a parenthesised ORDER BY")
		}
		if !p.match(TokORDER_BY) {
			return nil, p.unsupported("WITHIN GROUP without ORDER BY")
		}
		order, err := p.parseOrder()
		if err != nil {
			return nil, err
		}
		if !p.match(TokR_PAREN) {
			return nil, p.unsupported("unclosed WITHIN GROUP")
		}
		order.Set("this", this)
		this = order
	}
	return New("GroupConcat", Arg{"this", this}, Arg{"separator", separator}), nil
}

// JSON_OBJECT builds its arguments in PAIRS, and the two spellings of a pair
// are per dialect: DuckDB writes `JSON_OBJECT('k', 1)` and the others
// `JSON_OBJECT('k': 1)`. Both are read here, because the corpus contains both
// and neither says which dialect it came from.
//
//	JSON_OBJECT()                      no pairs at all
//	JSON_OBJECT(*)                     every column, as a Star
//	JSON_OBJECT('k': 1, 'j': TRUE)     colon pairs
//	JSON_OBJECT('k', 1, 'j', TRUE)     comma pairs
//
// followed by any of `NULL ON NULL`, `ABSENT ON NULL` and `WITH UNIQUE KEYS`.
// RETURNING is refused: it builds a return_type out of an Anonymous call or a
// FormatJson, shapes this port does not model, and guessing at them would put
// a wrong tree behind a statement that reads fine.
func (p *parser) parseJSONObject() (*Expression, error) {
	p.advance() // the name
	p.advance() // the opening parenthesis

	args := []Arg{}
	pairs := []*Expression{}
	switch {
	case p.at(TokR_PAREN):
		// Nothing between the parentheses. The reference still records an
		// empty list rather than no list at all.
	case p.at(TokSTAR):
		p.advance()
		pairs = append(pairs, New("Star"))
	default:
		for {
			key, err := p.parseExpression()
			if err != nil {
				return nil, err
			}
			// The separator between a key and ITS value: a colon everywhere
			// but DuckDB, which uses the same comma that separates the pairs.
			if !p.match(TokCOLON) && !p.match(TokCOMMA) {
				return nil, p.unsupported("a JSON_OBJECT key without a value")
			}
			value, err := p.parseExpression()
			if err != nil {
				return nil, err
			}
			pairs = append(pairs, New("JSONKeyValue",
				Arg{"this", key}, Arg{"expression", value}))
			if !p.match(TokCOMMA) {
				break
			}
		}
	}
	args = append(args, Arg{"expressions", pairs})

	// The modifiers, in the order the reference writes them.
	if word := p.atNullHandling(); word != "" {
		p.advance()
		p.advance()
		p.advance()
		args = append(args, Arg{"null_handling", word})
	}
	if p.atWords("WITH", "UNIQUE", "KEYS") {
		p.advance()
		p.advance()
		p.advance()
		args = append(args, Arg{"unique_keys", true})
	}
	if p.atWords("RETURNING") {
		return nil, p.unsupported("JSON_OBJECT with a RETURNING type")
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed JSON_OBJECT")
	}
	// Both are always ON the node, and always false unless RETURNING set
	// them -- which is refused above.
	args = append(args, Arg{"return_type", false}, Arg{"encoding", false})
	return New("JSONObject", args...), nil
}

// atNullHandling reports the `NULL ON NULL` / `ABSENT ON NULL` clause, which
// the reference keeps as the whole phrase rather than as a flag.
func (p *parser) atNullHandling() string {
	for _, word := range []string{"NULL", "ABSENT"} {
		if p.atWords(word, "ON") {
			if third := p.peekAt(2); third != nil && strings.EqualFold(third.Text, "NULL") {
				return word + " ON NULL"
			}
		}
	}
	return ""
}

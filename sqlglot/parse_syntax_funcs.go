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
		return p.parseConvert(false)
	case "TRY_CONVERT":
		return p.parseConvert(true)
	case "STRING_AGG":
		return p.parseStringAgg()
	case "JSON_OBJECT":
		return p.parseJSONObject()
	case "CEIL":
		return p.parseCeilFloor("Ceil")
	case "FLOOR":
		return p.parseCeilFloor("Floor")
	case "OVERLAY":
		return p.parseOverlay()
	case "ARG_MAX", "ARGMAX", "MAX_BY":
		return p.parseDistinctArgFunction("ArgMax", 0)
	case "ARG_MIN", "ARGMIN", "MIN_BY":
		return p.parseDistinctArgFunction("ArgMin", 0)
	case "APPROX_QUANTILE", "APPROX_PERCENTILE", "PERCENTILE_APPROX":
		return p.parseDistinctArgFunction("ApproxQuantile", 0)
	case "QUANTILE", "PERCENTILE":
		return p.parseDistinctArgFunction("Quantile", 0)
	case "QUANTILE_CONT":
		return p.parseDistinctArgFunction("PercentileCont", 0)
	case "QUANTILE_DISC":
		return p.parseDistinctArgFunction("PercentileDisc", 0)
	// The regressions take their DISTINCT on the argument the reference names,
	// which for three of the five is the SECOND one.
	case "REGR_AVGY":
		return p.parseDistinctArgFunction("RegrAvgy", 0)
	case "REGR_SXY":
		return p.parseDistinctArgFunction("RegrSxy", 0)
	case "REGR_AVGX":
		return p.parseDistinctArgFunction("RegrAvgx", 1)
	case "REGR_SXX":
		return p.parseDistinctArgFunction("RegrSxx", 1)
	case "REGR_SYY":
		return p.parseDistinctArgFunction("RegrSyy", 1)
	case "XMLELEMENT":
		return p.parseXMLElement()
	// One parser under four names: DuckDB spells the same aggregate
	// GROUP_CONCAT, LISTAGG and STRINGAGG as well as STRING_AGG.
	case "GROUP_CONCAT", "LISTAGG", "STRINGAGG":
		return p.parseStringAgg()
	case "CHAR", "CHR":
		return p.parseChr()
	case "JSONB_EXISTS":
		return p.parseJSONBExists()
	case "JSON_AGG":
		return p.parseJSONAgg()
	case "JSON_ARRAYAGG":
		return p.parseJSONArrayAgg()
	case "DATEPART":
		return p.parseDatePart(true)
	case "DATE_PART":
		return p.parseDatePart(false)
	case "OPENJSON":
		return p.parseOpenJSON()
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
		if _, says := p.tables.TrimTypes[strings.ToUpper(c.Text)]; says {
			position = strings.ToUpper(c.Text)
			p.advance()
		}
	}

	// The characters to trim may be left out entirely: `TRIM(LEADING FROM x)`
	// says where to trim and not what, and the reference reads the missing
	// operand as nothing rather than failing on it.
	var first *Expression
	if !p.at(TokFROM) {
		var err error
		first, err = p.parseExpression()
		if err != nil {
			return nil, err
		}
	}

	var chars, target *Expression
	switch {
	// `TRIM(x FROM y)` and `TRIM(x, y)` are the same call. Which operand is
	// the STRING and which the characters depends on the separator and on the
	// dialect: after FROM the characters come first everywhere, and after a
	// comma only where the dialect says so.
	case p.at(TokFROM), p.at(TokCOMMA):
		charactersFirst := p.at(TokFROM) || p.tables.TrimPatternFirst
		p.advance()
		second, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		if charactersFirst {
			chars, target = first, second
		} else {
			target, chars = first, second
		}
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
	var args []*Expression
	for {
		arg, err := p.parseBitwise()
		if err != nil {
			return nil, err
		}
		args = append(args, arg)
		if !p.match(TokCOMMA) {
			break
		}
	}

	// `POSITION(a IN b)` looks for a in b, and so does `POSITION(a, b)` --
	// the same two arguments the other way round. Only the comma form takes
	// a third, which is where in b to start.
	var node *Expression
	if p.match(TokIN) {
		haystack, err := p.parseBitwise()
		if err != nil {
			return nil, err
		}
		node = New("StrPosition", Arg{"this", haystack}, Arg{"substr", argAt(args, 0)})
	} else {
		node = New("StrPosition",
			Arg{"this", argAt(args, 1)},
			Arg{"substr", argAt(args, 0)})
		if start := argAt(args, 2); start != nil {
			node.Set("position", start)
		}
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed POSITION")
	}
	return node, nil
}

// CONVERT(type, x[, style]) -- the FIRST argument is a data type, which is why
// the ordinary argument parser cannot read this one: it would read VARCHAR(10)
// as a call to a function named VARCHAR.
func (p *parser) parseConvert(safe bool) (*Expression, error) {
	// Only T-SQL keeps this call as a Convert. Everywhere else the reference
	// reads the same word as a CAST written another way, and it does so with
	// a grammar the port does not have -- `CONVERT(INT, x)` comes out as
	// `CAST(INT AS x)`, with the type read off the SECOND argument. Building
	// a Convert there was a divergence the corpus never happened to contain.
	if !p.tables.ConvertBuildsConvert {
		return nil, p.unsupported("CONVERT where it is a CAST written another way")
	}
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
	// TRY_CONVERT is the same node flagged safe. The flag is SET rather than
	// supplied, so an ordinary CONVERT does not carry the key at all.
	if safe {
		args = append(args, Arg{"safe", true})
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

// CEIL and FLOOR are ordinary-looking calls with one extra spelling: a unit to
// round TO. The reference gives them a grammar of their own for that alone --
// `FLOOR(x TO DAY)` is a Floor with a unit, not a Floor of two things.
//
// Both build the same shape, so the class is the only difference between them.
func (p *parser) parseCeilFloor(class string) (*Expression, error) {
	p.advance()
	p.advance()

	var args []*Expression
	if !p.at(TokR_PAREN) {
		for {
			var arg *Expression
			var err error
			if p.atLambda() {
				arg, err = p.parseLambda()
			} else {
				arg, err = p.parseExpression()
			}
			if err != nil {
				return nil, err
			}
			args = append(args, arg)
			if !p.match(TokCOMMA) {
				break
			}
		}
	}

	var unit *Expression
	if p.atUnquotedWord("TO") {
		p.advance()
		// The reference asks for a VAR and takes a placeholder otherwise; a
		// unit written any other way is not a unit it would read either.
		c := p.curr()
		if c == nil || c.Type != TokVAR {
			return nil, p.unsupported("TO without a unit")
		}
		p.advance()
		unit = New("Var", Arg{"this", c.Text})
	}

	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed " + strings.ToUpper(class))
	}
	// An absent argument is absent rather than present-and-empty: the
	// reference's constructor drops the Nones, and the dump compares keys.
	node := New(class)
	if this := argAt(args, 0); this != nil {
		node.Set("this", this)
	}
	if decimals := argAt(args, 1); decimals != nil {
		node.Set("decimals", decimals)
	}
	if unit != nil {
		node.Set("to", unit)
	}
	if len(args) > 2 {
		return nil, p.unsupported("more arguments to " + strings.ToUpper(class) + " than it reads")
	}
	return node, nil
}

// OVERLAY(x PLACING y FROM a FOR b) replaces part of a string, and the same
// call can be written with commas instead of the words. Each of the three
// pieces after the first is read the same way -- a comma or the word that
// introduces it, then an expression -- so a call may stop after any of them.
func (p *parser) parseOverlay() (*Expression, error) {
	p.advance()
	p.advance()

	this, err := p.parseBitwise()
	if err != nil {
		return nil, err
	}
	node := New("Overlay", Arg{"this", this})
	for _, part := range []struct{ word, key string }{
		{"PLACING", "expression"},
		{"FROM", "from_"},
		{"FOR", "for_"},
	} {
		if !p.match(TokCOMMA) && !p.matchUnquotedWord(part.word) {
			break
		}
		arg, err := p.parseBitwise()
		if err != nil {
			return nil, err
		}
		node.Set(part.key, arg)
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed OVERLAY")
	}
	return node, nil
}

// matchUnquotedWord consumes the current token when it spells this word.
func (p *parser) matchUnquotedWord(word string) bool {
	if !p.atUnquotedWord(word) {
		return false
	}
	p.advance()
	return true
}

// ARG_MAX and ARG_MIN take a DISTINCT that belongs to the FIRST argument
// rather than to the call: `ARG_MAX(DISTINCT a, b)` is a maximum over distinct
// values of a, not a distinct list of two things. That is the whole reason
// they are given a grammar -- everything after the first argument is read the
// way any call's arguments are.
func (p *parser) parseDistinctArgFunction(class string, distinctIndex int) (*Expression, error) {
	p.advance()
	p.advance()

	distinct := p.match(TokDISTINCT)
	if !distinct {
		p.match(TokALL)
	}

	var args []*Expression
	first, err := p.parseCallArgument()
	if err != nil {
		return nil, err
	}
	args = append(args, first)
	for p.match(TokCOMMA) {
		arg, err := p.parseCallArgument()
		if err != nil {
			return nil, err
		}
		args = append(args, arg)
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed " + strings.ToUpper(class))
	}
	if distinct && distinctIndex < len(args) && args[distinctIndex] != nil {
		args[distinctIndex] = New("Distinct",
			Arg{"expressions", []*Expression{args[distinctIndex]}}, Arg{"on", nil})
	}
	return fromArgList(class, args), nil
}

// parseCallArgument reads ONE argument of a call.
//
// `x -> x > 1` is a lambda ONLY here, in argument position. The same `->`
// between two ordinary expressions is JSON extraction, and reading
// `data -> '$.value'` as a lambda made a Lambda out of a JSON path.
func (p *parser) parseCallArgument() (*Expression, error) {
	return p.parseCallArgumentAliased(false)
}

// parseCallArgumentAliased is the same, for a call whose arguments may NAME
// themselves. The reference allows that only where the name is one it has no
// node for: `XMLATTRIBUTES('xyz' AS bar)` is an Anonymous call over an Alias,
// and the same words inside a call the reference knows would be an argument
// followed by a stray word.
func (p *parser) parseCallArgumentAliased(alias bool) (*Expression, error) {
	switch {
	case p.atLambda():
		return p.parseLambda()
	case p.atKwarg():
		return p.parseKwarg()
	case p.at(TokSELECT), p.at(TokWITH):
		// `EXISTS(SELECT 1)`: the call's own parentheses are the subquery's,
		// so the argument is the Select ITSELF -- there is no Subquery
		// wrapper the way there is after `IN`.
		return p.parseQuery()
	default:
		e, err := p.parseExpression()
		if err != nil || !alias {
			return e, err
		}
		// Only the WRITTEN alias counts: the reference asks for an explicit
		// one here, so `F(x a)` is a stray word rather than a naming.
		if !p.match(TokALIAS) {
			return e, nil
		}
		name, err := p.parseIdentifier()
		if err != nil {
			return nil, err
		}
		return New("Alias", Arg{"this", e}, Arg{"alias", name}), nil
	}
}

// XMLELEMENT(NAME tag, ...) builds one XML element. The tag is a NAME rather
// than an expression, unless EVALNAME says to compute it -- and the reference
// records which of the two was written.
//
// A call with no content records `expressions` as FALSE rather than leaving it
// out: the reference writes `self._match(COMMA) and self._parse_csv(...)`, and
// a comma that is not there yields the false rather than nothing at all.
func (p *parser) parseXMLElement() (*Expression, error) {
	p.advance()
	p.advance()

	var this *Expression
	var err error
	evalname := p.matchUnquotedWord("EVALNAME")
	if evalname {
		this, err = p.parseBitwise()
	} else {
		p.matchUnquotedWord("NAME")
		this, err = p.parseIdentifier()
	}
	if err != nil {
		return nil, err
	}

	node := New("XMLElement", Arg{"this", this})
	if p.match(TokCOMMA) {
		var contents []*Expression
		for {
			content, err := p.parseBitwise()
			if err != nil {
				return nil, err
			}
			contents = append(contents, content)
			if !p.match(TokCOMMA) {
				break
			}
		}
		node.Set("expressions", contents)
	} else {
		node.Set("expressions", false)
	}
	if evalname {
		node.Set("evalname", true)
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed XMLELEMENT")
	}
	return node, nil
}

// CHR(97) and T-SQL's CHAR(10) are the same node, and both take a list --
// `CHR(97, 98)` is one call, not two. The character set is recorded even when
// it is not written, as false rather than as nothing.
func (p *parser) parseChr() (*Expression, error) {
	p.advance()
	p.advance()

	var args []*Expression
	if !p.at(TokR_PAREN) && !p.at(TokUSING) {
		for {
			arg, err := p.parseExpression()
			if err != nil {
				return nil, err
			}
			args = append(args, arg)
			if !p.match(TokCOMMA) {
				break
			}
		}
	}
	node := New("Chr", Arg{"expressions", args})
	if p.match(TokUSING) {
		// The reference asks for a bare word here and takes nothing else.
		c := p.curr()
		if c == nil || (c.Type != TokVAR && c.Type != TokIDENTIFIER && c.Type != TokBINARY) {
			return nil, p.unsupported("USING without a character set")
		}
		p.advance()
		node.Set("charset", New("Var", Arg{"this", c.Text}))
	} else {
		node.Set("charset", false)
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed CHR")
	}
	return node, nil
}

// JSONB_EXISTS(doc, path) folds its second argument into a JSON path, and
// records FALSE where there is no second argument at all.
func (p *parser) parseJSONBExists() (*Expression, error) {
	p.advance()
	p.advance()

	this, err := p.parseBitwise()
	if err != nil {
		return nil, err
	}
	node := New("JSONBExists", Arg{"this", this})
	if p.match(TokCOMMA) {
		path, err := p.parseBitwise()
		if err != nil {
			return nil, err
		}
		if isStringLiteral(path) {
			text, _ := path.Args["this"].(string)
			folded, err := parseJSONPath(text)
			if err != nil {
				return nil, p.unsupported("JSONB_EXISTS over a path it cannot fold")
			}
			path = folded
		}
		node.Set("path", path)
	} else {
		node.Set("path", false)
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed JSONB_EXISTS")
	}
	return node, nil
}

// PostgreSQL's JSON_AGG reads its ONE argument the way a call's argument is
// read -- so a DISTINCT wraps it, and an ORDER BY written after it wraps that.
// The clause is consumed there rather than by the `order` slot, which is why
// the slot stays empty on a call that plainly has an ordering.
func (p *parser) parseJSONAgg() (*Expression, error) {
	p.advance()
	p.advance()

	this, err := p.parseAggregateArgument()
	if err != nil {
		return nil, err
	}
	if p.at(TokORDER_BY) {
		p.advance()
		order, err := p.parseOrder()
		if err != nil {
			return nil, err
		}
		order.Set("this", this)
		this = order
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed JSON_AGG")
	}
	return New("JSONArrayAgg", Arg{"this", this}), nil
}

// T-SQL's JSON_ARRAYAGG reads a plain expression instead, so its ORDER BY does
// land in the `order` slot -- the same clause in the same place, recorded two
// different ways by two dialects.
func (p *parser) parseJSONArrayAgg() (*Expression, error) {
	p.advance()
	p.advance()

	this, err := p.parseBitwise()
	if err != nil {
		return nil, err
	}
	node := New("JSONArrayAgg", Arg{"this", this})
	if p.at(TokORDER_BY) {
		p.advance()
		order, err := p.parseOrder()
		if err != nil {
			return nil, err
		}
		node.Set("order", order)
	}
	if word := p.atNullHandling(); word != "" {
		p.advance()
		p.advance()
		p.advance()
		node.Set("null_handling", word)
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed JSON_ARRAYAGG")
	}
	return node, nil
}

// parseAggregateArgument reads the single argument of an aggregate whose
// grammar the reference writes by hand: DISTINCT collects everything after it
// into one node, as it does in an ordinary call.
func (p *parser) parseAggregateArgument() (*Expression, error) {
	if !p.match(TokDISTINCT) {
		p.match(TokALL)
		return p.parseCallArgument()
	}
	var args []*Expression
	for {
		arg, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		args = append(args, arg)
		if !p.match(TokCOMMA) {
			break
		}
	}
	return New("Distinct", Arg{"expressions", args}), nil
}

// DATEPART and DATE_PART are both an Extract with the unit in front, and they
// differ in how the unit is read. T-SQL takes a bare word and NORMALISES it --
// `DATEPART(mm, x)` records MONTH -- while PostgreSQL takes a string and keeps
// whatever was inside it, so `DATE_PART('mm', x)` records mm.
//
// A unit that is neither a word nor a string is left exactly as it parsed:
// PostgreSQL's `DATE_PART('isodow'::varchar(6), x)` keeps the cast.
func (p *parser) parseDatePart(normalise bool) (*Expression, error) {
	p.advance()
	p.advance()

	var unit *Expression
	if normalise {
		c := p.curr()
		if c == nil || (c.Type != TokVAR && c.Type != TokIDENTIFIER) {
			return nil, p.unsupported("DATEPART without a unit")
		}
		p.advance()
		word := c.Text
		if full, ok := p.tables.DatePartMapping[strings.ToUpper(word)]; ok {
			word = full
		}
		unit = New("Var", Arg{"this", word})
	} else {
		part, err := p.parsePostfix()
		if err != nil {
			return nil, err
		}
		// A name is taken OFF a column or a string and made into a bare unit;
		// anything else is kept as the node it is.
		if part != nil && (part.Class == "Column" || part.Class == "Literal") {
			part = New("Var", Arg{"this", part.Name()})
		}
		unit = part
	}

	// T-SQL reads the value only if a comma introduces it, and records FALSE
	// where there is none. PostgreSQL reads one either way, so a call with
	// nothing left in it is refused there rather than left empty.
	node := New("Extract", Arg{"this", unit})
	if normalise && !p.match(TokCOMMA) {
		node.Set("expression", false)
	} else {
		if !normalise {
			p.match(TokCOMMA)
		}
		value, err := p.parseBitwise()
		if err != nil {
			return nil, err
		}
		node.Set("expression", value)
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed DATEPART")
	}
	return node, nil
}

// OPENJSON(doc, path) WITH (col TYPE 'path', ...) reads a JSON document as a
// table. The WITH list is written OUTSIDE the call's own parentheses, so the
// closing one belongs to the call and the list brings a second.
func (p *parser) parseOpenJSON() (*Expression, error) {
	p.advance()
	p.advance()

	this, err := p.parseBitwise()
	if err != nil {
		return nil, err
	}
	node := New("OpenJSON", Arg{"this", this})
	if p.match(TokCOMMA) {
		c := p.curr()
		if c == nil || c.Type != TokSTRING {
			return nil, p.unsupported("OPENJSON without a path")
		}
		p.advance()
		node.Set("path", New("Literal", Arg{"this", c.Text}, Arg{"is_string", true}))
	} else {
		node.Set("path", false)
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed OPENJSON")
	}
	if !p.match(TokWITH) {
		return node, nil
	}
	if !p.match(TokL_PAREN) {
		return nil, p.unsupported("OPENJSON WITH without a column list")
	}
	var columns []*Expression
	for {
		column, err := p.parseOpenJSONColumn()
		if err != nil {
			return nil, err
		}
		columns = append(columns, column)
		if !p.match(TokCOMMA) {
			break
		}
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed OPENJSON column list")
	}
	node.Set("expressions", columns)
	return node, nil
}

// One column of an OPENJSON WITH list: a name, an optional type, an optional
// path, and AS JSON. Everything after the name may be missing, and each piece
// that is missing is simply absent -- except AS JSON, which is a flag and is
// always recorded.
func (p *parser) parseOpenJSONColumn() (*Expression, error) {
	name, err := p.parseIdentifier()
	if err != nil {
		return nil, err
	}
	node := New("OpenJSONColumnDef", Arg{"this", name})
	if c := p.curr(); c != nil {
		if _, isType := p.tables.TypeTokens[c.Type]; isType {
			kind, err := p.parseDataType()
			if err != nil {
				return nil, err
			}
			node.Set("kind", kind)
		}
	}
	if c := p.curr(); c != nil && c.Type == TokSTRING {
		p.advance()
		node.Set("path", New("Literal", Arg{"this", c.Text}, Arg{"is_string", true}))
	}
	asJSON := false
	if p.at(TokALIAS) {
		if n := p.next(); n != nil && n.Type == TokJSON {
			p.advance()
			p.advance()
			asJSON = true
		}
	}
	node.Set("as_json", asJSON)
	return node, nil
}

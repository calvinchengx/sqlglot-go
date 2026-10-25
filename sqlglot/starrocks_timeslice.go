package sqlglot

import "strings"

// parseStarrocksTimeSlice reads `TIME_SLICE(dt, INTERVAL n unit [, FLOOR |
// CEIL])` and its unquoted twin `TIME_SLICE(dt, n, 'unit', 'floor')`
// (StarRocksParser._build_time_slice).
//
// Entered with the name current and the opening parenthesis after it.
func (p *parser) parseStarrocksTimeSlice() (*Expression, error) {
	p.advance() // the name
	p.advance() // (
	var args []*Expression
	for {
		arg, err := p.parseAssignment()
		if err != nil {
			return nil, err
		}
		args = append(args, arg)
		if !p.match(TokCOMMA) {
			break
		}
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed TIME_SLICE")
	}
	if len(args) < 2 {
		return nil, p.unsupported("TIME_SLICE with fewer than two arguments")
	}
	node := New("TimeSlice", Arg{"this", args[0]})
	var boundary *Expression
	if interval := args[1]; interval != nil && interval.Class == "Interval" {
		node.Set("expression", interval.Args["this"])
		node.Set("unit", interval.Args["unit"])
		if len(args) > 2 {
			boundary = args[2]
		}
	} else {
		amount := args[1]
		// A quoted integer names a NUMBER, not a string.
		if text, ok := literalText(amount); ok && isAllDigits(text) && isStringLiteral(amount) {
			amount = New("Literal", Arg{"this", text}, Arg{"is_string", false})
		}
		node.Set("expression", amount)
		var unit *Expression
		if text, ok := literalText(args[2]); ok && isStringLiteral(args[2]) {
			unit = New("Var", Arg{"this", strings.ToUpper(text)})
		} else {
			unit = New("Var", Arg{"this", strings.ToUpper(args[2].Name())})
		}
		node.Set("unit", unit)
		if len(args) > 3 {
			boundary = args[3]
		}
	}
	if boundary != nil {
		word := strings.ToUpper(boundary.Name())
		kind := "START"
		if word == "CEIL" {
			kind = "END"
		}
		node.Set("kind", New("Literal", Arg{"this", kind}, Arg{"is_string", true}))
	}
	return node, nil
}

// literalText is the text of a string literal, and whether it is one.
func literalText(e *Expression) (string, bool) {
	if e == nil || e.Class != "Literal" {
		return "", false
	}
	text, _ := e.Args["this"].(string)
	return text, true
}

// isAllDigits reports whether every character is a decimal digit.
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// writeStarrocksTimeSlice writes StarRocks' TIME_SLICE back with the interval
// and the boundary word its builder took apart
// (StarRocksGenerator.timeslice_sql).
func (g *generator) writeStarrocksTimeSlice(e *Expression) string {
	if g.dialect != "starrocks" {
		return g.writeSnowflakeTimeSlice(e)
	}
	// An interval written as a bare number is a NUMBER, so the interval is
	// written back without quotes: `INTERVAL 5 MINUTE`.
	amount, _ := e.Args["expression"].(*Expression)
	if text, ok := literalText(amount); ok && isAllDigits(text) && isStringLiteral(amount) {
		amount = New("Literal", Arg{"this", text}, Arg{"is_string", false})
	}
	interval := New("Interval", Arg{"this", amount}, Arg{"unit", e.Args["unit"]})
	parts := []string{g.child(e, "this"), g.node(interval)}
	if kind, _ := e.Args["kind"].(*Expression); kind != nil {
		word := "FLOOR"
		if strings.ToUpper(kind.Name()) == "END" {
			word = "CEIL"
		}
		parts = append(parts, word)
	}
	return "TIME_SLICE(" + strings.Join(parts, ", ") + ")"
}

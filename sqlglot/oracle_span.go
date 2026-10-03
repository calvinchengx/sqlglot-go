package sqlglot

import "strings"

// Oracle writes (expr) DAY TO SECOND with no INTERVAL keyword. The span
// is a unit on the parenthesised expression, and either unit may carry
// a precision: DAY(9) TO SECOND(3).
func (p *parser) oracleDayToSecond(this *Expression, err error) (*Expression, error) {
	if err != nil || this == nil || p.dialect != "oracle" || this.Class != "Paren" {
		return this, err
	}
	start := p.index
	unit, uerr := p.oracleSpanUnit()
	if uerr != nil || unit == nil || !p.atWords("TO") {
		p.index = start
		return this, nil
	}
	p.advance()
	end, eerr := p.oracleSpanUnit()
	if eerr != nil || end == nil {
		p.index = start
		return this, nil
	}
	// The reference canonicalises the quantity before the span is
	// attached: a TIMESTAMP literal is a formatted TO_TIMESTAMP, and
	// bare SYSTIMESTAMP is that function rather than a column.
	if inner, ok := this.Args["this"].(*Expression); ok {
		this.Set("this", p.oracleSpanQuantity(inner))
	}
	_, marked := this.Args["join_mark"]
	if marked {
		this.Set("join_mark", nil)
	}
	out := New("Interval",
		Arg{"this", this},
		Arg{"unit", New("IntervalSpan",
			Arg{"this", unit},
			Arg{"expression", end})})
	if marked {
		out.Set("join_mark", false)
	}
	return out, nil
}

// oracleSpanQuantity is the expression inside the parentheses. A
// subtraction's two sides are the same kind of quantity.
func (p *parser) oracleSpanQuantity(e *Expression) *Expression {
	if e == nil {
		return nil
	}
	switch e.Class {
	case "Sub", "Paren":
		if c, ok := e.Args["this"].(*Expression); ok {
			e.Set("this", p.oracleSpanQuantity(c))
		}
		if c, ok := e.Args["expression"].(*Expression); ok {
			e.Set("expression", p.oracleSpanQuantity(c))
		}
		return e
	case "Column":
		if s := oracleBareSystimestamp(e); s != nil {
			return s
		}
	case "Cast":
		if s := oracleTimestampLiteral(e); s != nil {
			return s
		}
	}
	return e
}

// oracleBareSystimestamp is SYSTIMESTAMP with no qualifier and no
// quotes. The reference builds a Systimestamp, and the join mark stays.
func oracleBareSystimestamp(e *Expression) *Expression {
	if table, _ := e.Args["table"].(*Expression); table != nil {
		return nil
	}
	name, _ := e.Args["this"].(*Expression)
	if name == nil || name.Class != "Identifier" || name.Args["quoted"] == true {
		return nil
	}
	if text, _ := name.Args["this"].(string); !strings.EqualFold(text, "SYSTIMESTAMP") {
		return nil
	}
	out := New("Systimestamp")
	if _, marked := e.Args["join_mark"]; marked {
		out.Set("join_mark", false)
	}
	return out
}

// oracleTimestampLiteral is TIMESTAMP '...'. The reference stores the
// default format in its own spelling and writes the Oracle letters back.
func oracleTimestampLiteral(e *Expression) *Expression {
	to, _ := e.Args["to"].(*Expression)
	if to == nil || to.Args["this"] != DataTypeKind("TIMESTAMP") {
		return nil
	}
	lit, _ := e.Args["this"].(*Expression)
	if lit == nil || lit.Class != "Literal" || lit.Args["is_string"] != true {
		return nil
	}
	return New("StrToTime",
		Arg{"this", lit},
		Arg{"format", New("Literal",
			Arg{"this", "%Y-%m-%d %H:%M:%S.%f"},
			Arg{"is_string", true})})
}

func (p *parser) oracleSpanUnit() (*Expression, error) {
	c := p.curr()
	if c == nil {
		return nil, nil
	}
	if _, known := p.tables.ValidIntervalUnits[strings.ToUpper(c.Text)]; !known {
		return nil, nil
	}
	if n := p.next(); n != nil && n.Type == TokL_PAREN {
		return p.parseFunction()
	}
	word := c.Text
	p.advance()
	return New("Var", Arg{"this", p.normalisedIntervalUnit(word)}), nil
}

func init() {
	prev := generators["Interval"]
	generators["Interval"] = func(g *generator, e *Expression) string {
		return writeOracleBareInterval(g, e, prev)
	}

	tables := parserTables["oracle"]
	if tables == nil {
		return
	}
	spells := map[string][]FuncSQL{}
	for class, list := range tables.FunctionSQL {
		spells[class] = list
	}
	// A bare SYSTIMESTAMP takes no parentheses. The recorded spelling
	// writes an empty pair.
	spells["Systimestamp"] = append([]FuncSQL{{
		Name:     "SYSTIMESTAMP",
		NoParens: true,
		Consts:   []FuncConst{{Key: "this", Value: nil}},
	}}, spells["Systimestamp"]...)
	// TIMESTAMP '...' is stored as StrToTime and written TO_TIMESTAMP.
	spells["StrToTime"] = append([]FuncSQL{{
		Name: "TO_TIMESTAMP",
		Keys: []string{"this", "format"},
	}}, spells["StrToTime"]...)
	tables.FunctionSQL = spells

	spelling := map[string]string{}
	for class, how := range tables.FormatSpellings {
		spelling[class] = how
	}
	spelling["StrToTime"] = "inverse"
	tables.FormatSpellings = spelling

	// The reference tokenizes SYSTIMESTAMP as its own word. It is still
	// a name, so a column of that spelling can be read and then folded
	// into the function.
	if cfg := dialectConfigs["oracle"]; cfg != nil {
		keys := make(map[string]TokenType, len(cfg.Keywords)+1)
		for k, v := range cfg.Keywords {
			keys[k] = v
		}
		keys["SYSTIMESTAMP"] = TokSYSTIMESTAMP
		cfg.Keywords = keys
		cfg.trie = nil
	}
	ids := make(map[TokenType]struct{}, len(tables.IDVarTokens)+1)
	for k, v := range tables.IDVarTokens {
		ids[k] = v
	}
	ids[TokSYSTIMESTAMP] = struct{}{}
	tables.IDVarTokens = ids
}

// writeOracleBareInterval omits the INTERVAL keyword when the quantity
// is not a literal. A parenthesised difference is that quantity.
func writeOracleBareInterval(g *generator, e *Expression, prev func(*generator, *Expression) string) string {
	if g.dialect != "oracle" {
		return prev(g, e)
	}
	this, _ := e.Args["this"].(*Expression)
	if this == nil || this.Class == "Literal" {
		return prev(g, e)
	}
	return g.node(this) + " " + g.child(e, "unit")
}

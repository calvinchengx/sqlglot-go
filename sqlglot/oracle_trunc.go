package sqlglot

// Oracle reads a bare TRUNC of SYSDATE as a date truncation to the day.
// A numeric TRUNC, and a TRUNC that already names its unit, stay as they
// were parsed.
func (p *parser) oracleTruncTerm() (*Expression, error) {
	return p.oracleDateTrunc(p.parseFactor())
}

func (p *parser) oracleDateTrunc(this *Expression, err error) (*Expression, error) {
	if err != nil || this == nil || p.dialect != "oracle" || this.Class != "Trunc" {
		return this, err
	}
	for _, key := range this.Keys {
		if key == "this" || key == "join_mark" || this.Args[key] == nil {
			continue
		}
		return this, nil
	}
	inner, _ := this.Args["this"].(*Expression)
	if inner == nil || inner.Class != "CurrentTimestamp" || inner.Args["sysdate"] != true {
		return this, nil
	}
	_, marked := this.Args["join_mark"]
	// The reference types the timestamp it truncates. The bare call does
	// not carry that annotation on its own.
	inner.Type = New("DataType", Arg{"this", DataTypeKind("TIMESTAMP")})
	out := New("DateTrunc",
		Arg{"this", inner},
		Arg{"unit", New("Literal",
			Arg{"this", "DD"},
			Arg{"is_string", true})})
	if marked {
		out.Set("join_mark", false)
	}
	return out, nil
}

func init() {
	tables := parserTables["oracle"]
	if tables == nil {
		return
	}
	sqls := map[string][]FuncSQL{}
	for class, list := range tables.FunctionSQL {
		sqls[class] = list
	}
	// The stored unit is already the Oracle spelling, and the call puts
	// the value before that unit.
	sqls["DateTrunc"] = append([]FuncSQL{{
		Name: "TRUNC",
		Keys: []string{"this", "unit"},
	}}, sqls["DateTrunc"]...)
	tables.FunctionSQL = sqls
}

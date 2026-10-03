package sqlglot

// Oracle writes DATE '2022-01-01' as TO_DATE with a fixed format. The
// node is a date-from-string, which is what the reference records.
func init() {
	noteOracleDateLiteral(parserTables["oracle"])
}

func noteOracleDateLiteral(tables *ParserTables) {
	if tables == nil || tables.FunctionSQL == nil || tables.SyntaxSQL == nil {
		return
	}
	sqls := map[string][]FuncSQL{}
	for class, list := range tables.FunctionSQL {
		if class == "DateStrToDate" {
			continue
		}
		copied := make([]FuncSQL, len(list))
		copy(copied, list)
		sqls[class] = copied
	}
	tables.FunctionSQL = sqls
	tables.SyntaxSQL["DateStrToDate"] = []SyntaxTemplate{{
		Keys:     []string{"this"},
		Marked:   []string{"this"},
		Template: "TO_DATE({this}, 'YYYY-MM-DD')",
	}}
}

func (p *parser) oracleDateLiteral(e *Expression) *Expression {
	if p.dialect != "oracle" || e == nil || e.Class != "Cast" || !p.noJoinMark[e] {
		return e
	}
	to, _ := e.Args["to"].(*Expression)
	if to == nil || to.Args["this"] != DataTypeKind("DATE") {
		return e
	}
	lit, _ := e.Args["this"].(*Expression)
	if lit == nil || lit.Class != "Literal" {
		return e
	}
	if text, _ := lit.Args["is_string"].(bool); !text {
		return e
	}
	out := New("DateStrToDate", Arg{"this", lit})
	p.noJoinMark[out] = true
	return out
}

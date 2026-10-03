package sqlglot

// Oracle writes a bare CURRENT_TIMESTAMP with no parentheses. A
// timestamp that remembers it was spelled SYSDATE stays that word.
func init() {
	tables := parserTables["oracle"]
	spells := map[string][]FuncSQL{}
	for class, list := range tables.FunctionSQL {
		spells[class] = list
	}
	bare := FuncSQL{
		Name:     "CURRENT_TIMESTAMP",
		NoParens: true,
		Consts: []FuncConst{
			{Key: "this", Value: nil},
			{Key: "sysdate", Value: nil},
		},
	}
	spells["CurrentTimestamp"] = append([]FuncSQL{bare}, spells["CurrentTimestamp"]...)
	tables.FunctionSQL = spells
}

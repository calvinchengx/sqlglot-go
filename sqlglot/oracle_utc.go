package sqlglot

// Oracle's UTC_TIME and UTC_TIMESTAMP are the same calls MySQL writes.
// A precision is the one argument; with none, the name keeps its parentheses.
func init() {
	noteOracleUTC("UTC_TIME", "UtcTime")
	noteOracleUTC("UTC_TIMESTAMP", "UtcTimestamp")
}

func noteOracleUTC(word, class string) {
	tables := parserTables["oracle"]
	funcs := map[string]FuncSpec{}
	for name, spec := range tables.Functions {
		funcs[name] = spec
	}
	funcs[word] = FuncSpec{Class: class, Args: []FuncArg{{Key: "this", Index: 0}}}
	tables.Functions = funcs

	sqls := map[string][]FuncSQL{}
	for name, list := range tables.FunctionSQL {
		sqls[name] = list
	}
	precise := FuncSQL{Name: word, Keys: []string{"this"}}
	bare := FuncSQL{Name: word, Consts: []FuncConst{{Key: "this", Value: nil}}}
	sqls[class] = append([]FuncSQL{precise, bare}, sqls[class]...)
	tables.FunctionSQL = sqls
}

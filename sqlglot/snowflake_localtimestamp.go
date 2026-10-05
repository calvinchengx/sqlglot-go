package sqlglot

import "maps"

// Snowflake's bare LOCALTIMESTAMP is a Localtimestamp written CURRENT_TIMESTAMP.
// LOCALTIMESTAMP(...) is CurrentTimestamp, written the same way.
func init() {
	spellSnowflakeLocalTimestamp(parserTables["snowflake"])
}

func spellSnowflakeLocalTimestamp(tables *ParserTables) {
	if tables == nil {
		return
	}
	readSnowflakeLocalTimestamp(tables)
	writeSnowflakeLocalTimestamp(tables)
}

func readSnowflakeLocalTimestamp(tables *ParserTables) {
	functions := maps.Clone(tables.Functions)
	functions["LOCALTIMESTAMP"] = FuncSpec{
		Class: "CurrentTimestamp",
		Args:  []FuncArg{{Key: "this", Index: 0}},
	}
	tables.Functions = functions
}

func writeSnowflakeLocalTimestamp(tables *ParserTables) {
	owned := maps.Clone(tables.FunctionSQL)
	owned["Localtimestamp"] = []FuncSQL{
		{
			Name:     "CURRENT_TIMESTAMP",
			Consts:   []FuncConst{{Key: "this", Value: nil}},
			NoParens: true,
		},
		{
			Name: "CURRENT_TIMESTAMP",
			Keys: []string{"this"},
		},
	}
	tables.FunctionSQL = owned
}

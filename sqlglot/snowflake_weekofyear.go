package sqlglot

// Snowflake's WEEKOFYEAR is WEEK. The node is Week.
func init() {
	recordSnowflakeWeekOfYear(parserTables["snowflake"])
}

func recordSnowflakeWeekOfYear(tables *ParserTables) {
	if tables == nil {
		return
	}
	functions := tables.Functions
	if functions == nil {
		return
	}
	functions["WEEKOFYEAR"] = FuncSpec{
		Class: "Week",
		Args:  []FuncArg{{Key: "this", Index: 0}},
	}
}

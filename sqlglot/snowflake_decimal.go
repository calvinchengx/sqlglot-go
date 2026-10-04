package sqlglot

// A Snowflake number written without a precision is DECIMAL(38, 0).
// One that already names its precision is left as it was written.
func init() {
	widenSnowflakeDecimal(parserTables["snowflake"], "38", "0")
}

func widenSnowflakeDecimal(tables *ParserTables, precision, scale string) {
	if tables == nil || precision == "" {
		return
	}
	tables.DefaultTypeParams = map[string][]string{
		"DECIMAL": {precision, scale},
	}
}

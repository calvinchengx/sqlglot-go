package sqlglot

// Snowflake writes a star's dropped columns as EXCLUDE. The list stays
// on except_.
func init() {
	spellSnowflakeExclude(parserTables["snowflake"])
}

func spellSnowflakeExclude(tables *ParserTables) {
	if tables == nil {
		return
	}
	tables.StarExceptWord = "EXCLUDE"
}

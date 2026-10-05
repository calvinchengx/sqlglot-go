package sqlglot

import "maps"

// Snowflake stores TEXT and writes it VARCHAR. VARCHAR itself stays VARCHAR.
func init() {
	spellSnowflakeTextType(parserTables["snowflake"])
}

func spellSnowflakeTextType(tables *ParserTables) {
	if tables == nil || tables.TypeSQL == nil {
		return
	}
	types := maps.Clone(tables.TypeSQL)
	if _, ok := types["TEXT"]; !ok {
		return
	}
	types["TEXT"] = "VARCHAR"
	tables.TypeSQL = types
}

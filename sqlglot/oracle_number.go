package sqlglot

// Oracle writes a decimal type as NUMBER. The kind stays decimal.
func init() {
	spellOracleNumber(parserTables["oracle"])
}

// spellOracleNumber records that spelling on this dialect's own type
// table. The table is already a copy by the time this runs.
func spellOracleNumber(tables *ParserTables) {
	if tables == nil || tables.TypeSQL == nil {
		return
	}
	tables.TypeSQL["DECIMAL"] = "NUMBER"
}

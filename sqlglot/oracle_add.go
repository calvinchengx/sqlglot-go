package sqlglot

// Oracle writes ADD and the column. The word COLUMN is not part of it.
func init() {
	noteOracleAdd(parserTables["oracle"])
}

func noteOracleAdd(tables *ParserTables) {
	if tables == nil {
		return
	}
	tables.AlterAddColumnWord = false
}

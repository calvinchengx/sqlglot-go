package sqlglot

// Oracle reads a row lock. The writer already spells FOR UPDATE, OF,
// WAIT, NOWAIT and SKIP LOCKED; it declines until the dialect records
// that it has the clause.
func init() {
	noteOracleLock(parserTables["oracle"])
}

func noteOracleLock(tables *ParserTables) {
	if tables == nil {
		return
	}
	// The slice is a capability flag. writeLock spells every form itself
	// and refuses the dialect when this entry is absent.
	tables.SyntaxSQL["Lock"] = []SyntaxTemplate{{Template: "FOR UPDATE"}}
}

package sqlglot

// Oracle writes ROWS after every OFFSET count. The word is not stored
// on the node: the count is the whole clause, and the dialect puts it back.
func init() {
	if tables := parserTables["oracle"]; tables != nil {
		tables.OffsetRowsWord = "ROWS"
	}
}

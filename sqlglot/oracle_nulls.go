package sqlglot

// Oracle sorts nulls as large: they follow an ascending key and lead a
// descending one. An explicit NULLS clause is written only when it says
// the other thing.
func init() {
	tables := parserTables["oracle"]
	tables.NullOrdering = "nulls_are_large"
	tables.DefaultNullsFirstAsc = false
	tables.DefaultNullsFirstDesc = true
}

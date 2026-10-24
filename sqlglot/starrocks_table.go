package sqlglot

import "maps"

// StarRocks registers TABLE(<tvf>) as a function of its own: the relation is
// the rows of the call inside it, not a table named TABLE
// (StarRocks Parser.FUNCTIONS). The probe sees the resulting TableFromRows
// and lists the name as one that is not a Table in a FROM clause, so the
// registration takes it back out of that set.
func init() {
	tables := parserTables["starrocks"]
	if tables == nil {
		return
	}
	functions := maps.Clone(tables.Functions)
	functions["TABLE"] = FuncSpec{
		Class: "TableFromRows",
		Args:  []FuncArg{{Key: "this", Index: 0}},
	}
	tables.Functions = functions
	notTables := maps.Clone(tables.TableFunctions)
	delete(notTables, "TABLE")
	tables.TableFunctions = notTables
}

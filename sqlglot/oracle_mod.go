package sqlglot

// Oracle writes MOD(a, b). The same node is an infix percent sign in
// the neutral dialect, which says something else.
func init() {
	tables := parserTables["oracle"]
	if tables == nil {
		return
	}
	// The percent sign is a binary spelling, and that spelling is chosen
	// before any call template. Oracle has no percent sign for this node.
	ops := map[string]string{}
	for class, op := range tables.BinarySQL {
		if class == "Mod" {
			continue
		}
		ops[class] = op
	}
	tables.BinarySQL = ops
	spellOracleCall(tables, "Mod", "{this} % {expression}", "MOD({this}, {expression})")
}

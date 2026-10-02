package sqlglot

// Oracle does not nest block comments. 1 /* /* */ closes at the first
// end mark; a dialect that nests would keep scanning and run off the end.
func init() {
	cfg := dialectConfigs["oracle"]
	if cfg == nil {
		return
	}
	cfg.NestedComments = false
}

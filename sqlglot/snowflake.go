package sqlglot

// Snowflake is registered beside the generated dialect map. That map is
// one composite literal, and growing it for a single dialect is a
// verbosity regression on the whole of it. The vocabulary starts as the
// neutral one. Each Snowflake spelling is a later mechanism, and a
// construct this port does not read is refused rather than given a tree
// that means less than the input.
func init() {
	registerSnowflake(parserTables[""], dialectConfigs[""])
}

// registerSnowflake installs the dialect. Block comments do not nest:
// the first end mark closes them, which is the reference tokenizer's rule.
func registerSnowflake(src *ParserTables, base *Config) {
	if src == nil || base == nil {
		return
	}
	tables := *src
	parserTables["snowflake"] = &tables
	cfg := *base
	cfg.Name = "snowflake"
	cfg.trie = nil
	cfg.Tables = nil
	cfg.NestedComments = false
	dialectConfigs["snowflake"] = &cfg
}

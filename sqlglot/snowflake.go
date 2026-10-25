package sqlglot

// Snowflake is registered beside the generated dialect map. That map is
// one composite literal, and growing it for a single dialect is a
// verbosity regression on the whole of it. The vocabulary starts as the
// neutral one. Each Snowflake spelling is a later mechanism, and a
// construct this port does not read is refused rather than given a tree
// that means less than the input.
func init() {
	// The tokenizer configuration is the reference's own, generated with the
	// rest of them; only the parser's vocabulary starts from the neutral one.
	registerSnowflake(parserTables[""], dialectConfigs["snowflake"])
}

// registerSnowflake installs the dialect. Block comments do not nest in
// Snowflake, which the generated configuration already records.
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
	dialectConfigs["snowflake"] = &cfg
}

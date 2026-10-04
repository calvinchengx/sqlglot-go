package sqlglot

import "maps"

// A Snowflake hex literal is x'…' or X'…'. It is a hex string, and it is
// written back in lowercase either way. 0x is not one of these.
func init() {
	cfg := dialectConfigs["snowflake"]
	tables := parserTables["snowflake"]
	spellSnowflakeHex(cfg, tables)
}

func spellSnowflakeHex(cfg *Config, tables *ParserTables) {
	if cfg == nil || tables == nil {
		return
	}
	formats := maps.Clone(cfg.FormatStrings)
	formats["x'"] = FormatString{End: "'", Type: TokHEX_STRING}
	formats["X'"] = FormatString{End: "'", Type: TokHEX_STRING}
	cfg.FormatStrings = formats
	cfg.trie = nil
	spellings := maps.Clone(tables.StringClassSQL)
	spellings["HexString"] = "x'{body}'\tfalse"
	tables.StringClassSQL = spellings
}

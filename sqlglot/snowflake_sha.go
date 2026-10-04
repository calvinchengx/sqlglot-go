package sqlglot

import "maps"

// Snowflake writes the SHA digest as SHA1. The node stays an SHA.
func init() {
	spellSnowflakeDigest(parserTables["snowflake"])
}

func spellSnowflakeDigest(tables *ParserTables) {
	if tables == nil || len(tables.FunctionSQL["SHA"]) == 0 {
		return
	}
	owned := maps.Clone(tables.FunctionSQL)
	prior := owned["SHA"]
	digest := make([]FuncSQL, len(prior))
	copy(digest, prior)
	for n := range digest {
		digest[n].Name = "SHA1"
	}
	owned["SHA"] = digest
	tables.FunctionSQL = owned
}

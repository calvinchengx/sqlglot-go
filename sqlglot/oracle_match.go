package sqlglot

// Oracle reads MATCH_RECOGNIZE as its own token. The clause parser is
// already the reference's; it never runs while the word is still a name.
func init() {
	noteOracleMatch(dialectConfigs["oracle"])
}

func noteOracleMatch(cfg *Config) {
	words := cfg.Keywords
	words["MATCH_RECOGNIZE"] = TokMATCH_RECOGNIZE
	cfg.trie = nil
}

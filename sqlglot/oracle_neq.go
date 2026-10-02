package sqlglot

// Oracle spells not-equal as ^=. Both characters are already single
// tokens, so the scanner sees the pair only when the keyword trie holds it.
func init() {
	cfg := dialectConfigs["oracle"]
	if cfg == nil {
		return
	}
	keys := make(map[string]TokenType, len(cfg.Keywords)+1)
	for k, v := range cfg.Keywords {
		keys[k] = v
	}
	keys["^="] = TokNEQ
	cfg.Keywords = keys
	cfg.trie = nil
}

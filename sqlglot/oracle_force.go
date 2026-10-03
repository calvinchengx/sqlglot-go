package sqlglot

// Oracle writes FORCE between OR REPLACE and the kind. The word is one
// property, and every dialect that places it spells that property FORCE.
func init() {
	if tables := parserTables["oracle"]; tables != nil {
		tables.CreateProperties = withForceProperty(tables.CreateProperties)
	}
	generators["ForceProperty"] = (*generator).writeForceProperty
}

// withForceProperty returns a copy that also reads FORCE. The shared
// table stays as it is, so another dialect does not gain the word.
func withForceProperty(src map[string]string) map[string]string {
	out := make(map[string]string, len(src)+1)
	for word, class := range src {
		out[word] = class
	}
	out["FORCE"] = "ForceProperty"
	return out
}

func (g *generator) writeForceProperty(e *Expression) string {
	return "FORCE"
}

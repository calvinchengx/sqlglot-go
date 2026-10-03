package sqlglot

// Oracle writes TRIM(chars FROM text) and TRIM(BOTH chars FROM text).
// The neutral spelling is a two-argument call, which drops BOTH and FROM.
func init() {
	tables := parserTables["oracle"]
	if tables == nil {
		return
	}
	tables.SyntaxSQL["Trim"] = []SyntaxTemplate{
		{
			Keys:     []string{"this", "expression", "position"},
			Marked:   []string{"this", "expression", "position"},
			Template: "TRIM({position} {expression} FROM {this})",
		},
		{
			Keys:     []string{"this", "expression"},
			Marked:   []string{"this", "expression"},
			Template: "TRIM({expression} FROM {this})",
		},
		{
			Keys:     []string{"this"},
			Marked:   []string{"this"},
			Template: "TRIM({this})",
		},
	}
}

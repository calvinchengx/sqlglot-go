package sqlglot

// Oracle's bare SYSDATE is a timestamp. The parser already builds that
// node when the name takes no parentheses. The neutral spelling of the
// same node is CURRENT_TIMESTAMP, which is a different word.
func init() {
	tables := parserTables["oracle"]
	if tables == nil {
		return
	}
	names := map[string]struct{}{}
	for name := range tables.NoParenFunctionNames {
		names[name] = struct{}{}
	}
	names["SYSDATE"] = struct{}{}
	tables.NoParenFunctionNames = names

	syntax := map[string][]SyntaxTemplate{}
	for class, spells := range tables.SyntaxSQL {
		syntax[class] = spells
	}
	bare := SyntaxTemplate{
		Keys:     []string{"sysdate"},
		Required: []FuncConst{{Key: "sysdate", Value: true}},
		Template: "SYSDATE",
	}
	syntax["CurrentTimestamp"] = append([]SyntaxTemplate{bare}, syntax["CurrentTimestamp"]...)
	tables.SyntaxSQL = syntax
}

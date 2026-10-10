package sqlglot

import "maps"

// Snowflake writes Stuff as INSERT. The node stays Stuff. Only this
// dialect's tables change; other dialects keep writing STUFF.
func init() {
	spellSnowflakeInsert(parserTables["snowflake"])
}

func spellSnowflakeInsert(tables *ParserTables) {
	if tables == nil {
		return
	}
	funcs := maps.Clone(tables.FunctionSQL)
	delete(funcs, "Stuff")
	tables.FunctionSQL = funcs
	forms := maps.Clone(tables.SyntaxSQL)
	forms["Stuff"] = []SyntaxTemplate{
		{
			Keys:     []string{"this", "start", "length", "expression"},
			Marked:   []string{"this", "start", "length", "expression"},
			Template: "INSERT({this}, {start}, {length}, {expression})",
		},
	}
	tables.SyntaxSQL = forms
}

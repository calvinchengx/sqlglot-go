package sqlglot

import "maps"

// Snowflake writes ArgMax as MAX_BY and ArgMin as MIN_BY. The nodes stay
// ArgMax and ArgMin. A count argument is part of the call when present.
func init() {
	spellSnowflakeMaxBy(parserTables["snowflake"])
}

func spellSnowflakeMaxBy(tables *ParserTables) {
	if tables == nil {
		return
	}
	forms := maps.Clone(tables.SyntaxSQL)
	forms["ArgMax"] = snowflakeByForms("MAX_BY")
	forms["ArgMin"] = snowflakeByForms("MIN_BY")
	tables.SyntaxSQL = forms
}

func snowflakeByForms(name string) []SyntaxTemplate {
	two := name + "({this}, {expression})"
	three := name + "({this}, {expression}, {count})"
	return []SyntaxTemplate{
		{
			Keys:     []string{"this", "expression"},
			Marked:   []string{"this", "expression"},
			Template: two,
		},
		{
			Keys:     []string{"this", "expression", "count"},
			Marked:   []string{"this", "expression", "count"},
			Template: three,
		},
	}
}

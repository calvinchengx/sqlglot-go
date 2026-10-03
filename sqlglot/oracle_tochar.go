package sqlglot

func init() {
	spellOracleToChar()
}

// Oracle writes TO_CHAR of one argument, and of a value with a format
// and an NLS parameter. The neutral templates only know the two-argument
// form.
func spellOracleToChar() {
	tables := parserTables["oracle"]
	if tables == nil || tables.SyntaxSQL == nil {
		return
	}
	for _, word := range []struct {
		keys  []string
		spell string
	}{
		{keys: []string{"this"}, spell: "TO_CHAR({this})"},
		{keys: []string{"this", "format", "nlsparam"}, spell: "TO_CHAR({this}, {format}, {nlsparam})"},
	} {
		tables.SyntaxSQL["ToChar"] = append(tables.SyntaxSQL["ToChar"], SyntaxTemplate{
			Keys:     word.keys,
			Marked:   word.keys,
			Template: word.spell,
		})
	}
}

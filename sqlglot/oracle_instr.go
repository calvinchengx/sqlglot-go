package sqlglot

import "strings"

// Oracle spells a string position as INSTR. The arguments stay in the
// order the neutral templates already use.
func init() {
	spellOracleCall(parserTables["oracle"], "StrPosition", "STR_POSITION", "INSTR")
}

// spellOracleCall copies the dialect's syntax table and renames one
// call. The shared table is left alone, so other dialects keep their word.
func spellOracleCall(tables *ParserTables, class, from, to string) {
	if tables == nil {
		return
	}
	syntax := map[string][]SyntaxTemplate{}
	for name, spells := range tables.SyntaxSQL {
		syntax[name] = spells
	}
	rewritten := make([]SyntaxTemplate, len(syntax[class]))
	for i, spell := range syntax[class] {
		spell.Template = strings.Replace(spell.Template, from, to, 1)
		rewritten[i] = spell
	}
	syntax[class] = rewritten
	tables.SyntaxSQL = syntax
}

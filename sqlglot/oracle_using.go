package sqlglot

// Oracle writes CHR(n USING charset). A call with no charset keeps the
// neutral spelling, which is the number alone.
func init() {
	tables := parserTables["oracle"]
	if tables == nil {
		return
	}
	using := SyntaxTemplate{
		Keys:     []string{"charset", "expressions"},
		Marked:   []string{"charset", "expressions"},
		Template: "CHR({expressions} USING {charset})",
	}
	tables.SyntaxSQL["Chr"] = append([]SyntaxTemplate{using}, tables.SyntaxSQL["Chr"]...)
}

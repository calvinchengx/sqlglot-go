package sqlglot

// parseInsertOrCall reads a statement that starts with INSERT. A parenthesis
// makes it the string function, and INSERT(str, pos, len, newstr) is Stuff.
// Anything else is the statement. The call has to be read here: the
// statement-token refusal below would otherwise reject the word outright.
func (p *parser) parseInsertOrCall() (*Expression, error) {
	spec, ok := p.tables.Functions["INSERT"]
	if ok && spec.Class == "Stuff" && p.namesAFunctionCall() {
		return p.parseExpression()
	}
	return p.parseInsert()
}

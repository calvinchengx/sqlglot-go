package sqlglot

import "strings"

// Registered after the writer map exists. DAX writes a string in double
// quotes; the shared writer quotes every string with a single quote, and
// that writer is left as it is.
func init() {
	if generators == nil {
		return
	}
	generators["Literal"] = (*generator).writeStringLiteral
}

// writeStringLiteral quotes a DAX string with " and doubles one inside it.
// Any other dialect uses the shared literal writer.
func (g *generator) writeStringLiteral(e *Expression) string {
	if g.dialect == "dax" && e.Args["is_string"] == true {
		text, _ := e.Args["this"].(string)
		return `"` + strings.ReplaceAll(text, `"`, `""`) + `"`
	}
	return g.writeLiteral(e)
}

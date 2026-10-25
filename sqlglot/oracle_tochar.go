package sqlglot

import "strings"

func init() {
	generators["ToChar"] = (*generator).writeToChar
}

// writeToChar writes Oracle's TO_CHAR with the arguments the node carries, in
// the reference's own order: the value, the format and the NLS parameter
// (OracleGenerator.TRANSFORMS, exp.ToChar: function_fallback_sql).
//
// The generic `tochar_sql` writes `CAST(x AS TEXT)` and refuses a format, which
// is right for a dialect with no TO_CHAR of its own; Oracle has one, so it
// keeps the call -- with one argument, two, or three. Another dialect spells
// the same node its own way, which the probed tables already know.
func (g *generator) writeToChar(e *Expression) string {
	if g.dialect != "oracle" {
		return g.spell(e)
	}
	args := []string{g.child(e, "this")}
	if format := g.child(e, "format"); format != "" {
		args = append(args, format)
	}
	if nls := g.child(e, "nlsparam"); nls != "" {
		args = append(args, nls)
	}
	return "TO_CHAR(" + strings.Join(args, ", ") + ")"
}

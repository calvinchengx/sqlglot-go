package sqlglot

import "strings"

// Oracle writes a grouped concatenation as LISTAGG, and the ordering
// comes back out as WITHIN GROUP rather than inside the call. An
// overflow clause sits inside the call, after the separator.
func init() {
	noteOracleListagg(parserTables["oracle"])
	generators["GroupConcat"] = (*generator).writeListedGroupConcat
}

func noteOracleListagg(tables *ParserTables) {
	if tables == nil || tables.FunctionSQL == nil {
		return
	}
	tables.GroupConcatOrder = "within_group"
	syntax := map[string]struct{}{}
	for name := range tables.SyntaxFunctions {
		syntax[name] = struct{}{}
	}
	syntax["LISTAGG"] = struct{}{}
	tables.SyntaxFunctions = syntax
	list := tables.FunctionSQL["GroupConcat"]
	named := make([]FuncSQL, len(list))
	for i, spell := range list {
		spell.Name = "LISTAGG"
		named[i] = spell
	}
	tables.FunctionSQL["GroupConcat"] = named
}

func (g *generator) writeListedGroupConcat(e *Expression) string {
	if g.dialect != "oracle" {
		return g.writeGroupConcat(e)
	}
	overflow := g.child(e, "on_overflow")
	if overflow == "" {
		return g.writeGroupConcat(e)
	}
	plain := e.shallowCopy()
	plain.Set("on_overflow", nil)
	out := g.writeGroupConcat(plain)
	at := strings.Index(out, ") WITHIN GROUP")
	if at < 0 {
		at = strings.LastIndex(out, ")")
	}
	if at < 0 {
		return g.fail(e.Class + " whose spelling is not a call")
	}
	return out[:at] + " ON OVERFLOW " + overflow + out[at:]
}

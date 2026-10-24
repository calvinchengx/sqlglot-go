package sqlglot

import "maps"

// Snowflake's TABLE(...) is not a table named TABLE. It is a table built
// from the rows of the call inside it, and FLATTEN is the explode that
// call usually holds.
func init() {
	tables := parserTables["snowflake"]
	spellSnowflakeFlatten(tables)
}

func spellSnowflakeFlatten(tables *ParserTables) {
	if tables == nil {
		return
	}
	functions := maps.Clone(tables.Functions)
	functions["FLATTEN"] = FuncSpec{
		Class: "Explode",
		Args: []FuncArg{
			{Key: "this", Index: 0},
			{Key: "expressions", Index: 1, VarLen: true},
		},
	}
	functions["TABLE"] = FuncSpec{
		Class: "TableFromRows",
		Args:  []FuncArg{{Key: "this", Index: 0}},
	}
	tables.Functions = functions
	forms := maps.Clone(tables.FunctionSQL)
	explode := append([]FuncSQL(nil), forms["Explode"]...)
	for i := range explode {
		explode[i].Name = "FLATTEN"
	}
	forms["Explode"] = explode
	tables.FunctionSQL = forms
	generators["TableFromRows"] = (*generator).writeSnowflakeRows
}

func (g *generator) writeSnowflakeRows(e *Expression) string {
	if g.dialect != "snowflake" && g.dialect != "starrocks" {
		return g.spell(e)
	}
	if _, ok := e.Args["joins"]; ok {
		return g.fail("TableFromRows joins")
	}
	if _, ok := e.Args["pivots"]; ok {
		return g.fail("TableFromRows pivots")
	}
	if _, ok := e.Args["sample"]; ok {
		return g.fail("TableFromRows sample")
	}
	out := "TABLE(" + g.child(e, "this") + ")"
	if alias := g.child(e, "alias"); alias != "" {
		out += " AS " + alias
	}
	return out
}

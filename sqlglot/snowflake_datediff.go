package sqlglot

import (
	"maps"
	"strings"
)

// Snowflake writes the unit first and keeps the later date in this.
// The boundary flag is set on every one of these.
func init() {
	spellSnowflakeDateDiff()
}

func spellSnowflakeDateDiff() {
	tables := parserTables["snowflake"]
	if tables == nil {
		return
	}
	functions := maps.Clone(tables.Functions)
	functions["DATEDIFF"] = FuncSpec{
		Class: "DateDiff",
		Args: []FuncArg{
			{Key: "this", Index: 2},
			{Key: "expression", Index: 1},
			{Key: "unit", Index: 0, Wrap: "Var"},
			{Key: "date_part_boundary", Index: -1, Const: true},
		},
	}
	tables.Functions = functions
	generators["DateDiff"] = (*generator).writeSnowflakeDateDiff
}

func (g *generator) writeSnowflakeDateDiff(e *Expression) string {
	if g.dialect != "snowflake" {
		return g.writeDateDiff(e)
	}
	return "DATEDIFF(" + strings.Join([]string{
		g.child(e, "unit"),
		g.child(e, "expression"),
		g.child(e, "this"),
	}, ", ") + ")"
}

package sqlglot

import "strings"

// Snowflake's BIT_NOT and BITNOT are the same node, written BITNOT.
// The operator ~ is that node too.
func init() {
	tables := parserTables["snowflake"]
	if tables != nil && tables.Functions != nil {
		call := bitNotCall()
		tables.Functions["BITNOT"] = call
		tables.Functions["BIT_NOT"] = call
	}
	generators["BitwiseNot"] = (*generator).writeSnowflakeBitNot
}

func bitNotCall() FuncSpec {
	return FuncSpec{
		Class: "BitwiseNot",
		Args:  []FuncArg{{Key: "this", Index: 0}},
	}
}

func (g *generator) writeSnowflakeBitNot(e *Expression) string {
	operand, _ := e.Args["this"].(*Expression)
	switch g.dialect {
	case "snowflake":
		if operand == nil {
			return g.fail("BITNOT without an operand")
		}
		return strings.Join([]string{"BITNOT(", g.node(operand), ")"}, "")
	default:
		return g.spell(e)
	}
}

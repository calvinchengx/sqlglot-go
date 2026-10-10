package sqlglot

import (
	"maps"
	"strings"
)

// Snowflake's bitwise AND, OR, and XOR are BITAND, BITOR, and BITXOR.
// A third argument is which side to pad. The operators `&`, `|`, and `^`
// are the same nodes, written the same way.
func init() {
	spellSnowflakeBitwise(parserTables["snowflake"])
}

func spellSnowflakeBitwise(tables *ParserTables) {
	if tables == nil {
		return
	}
	readSnowflakeBitwise(tables)
	for _, class := range [3]string{"BitwiseAnd", "BitwiseOr", "BitwiseXor"} {
		generators[class] = (*generator).writeSnowflakeBitwise
	}
}

func readSnowflakeBitwise(tables *ParserTables) {
	// The neutral names are the aggregates. Snowflake's two- and
	// three-argument forms are the bitwise calls, and any other count
	// is refused: one argument is missing its second operand, and four
	// is not the pad-side form.
	functions := maps.Clone(tables.Functions)
	delete(functions, "BIT_AND")
	delete(functions, "BIT_OR")
	delete(functions, "BIT_XOR")
	tables.Functions = functions

	byCount := maps.Clone(tables.FunctionsByArity)
	if byCount == nil {
		byCount = map[string]map[int]FuncSpec{}
	}
	placeBitwiseArity(byCount, "BITAND", "BitwiseAnd")
	placeBitwiseArity(byCount, "BIT_AND", "BitwiseAnd")
	placeBitwiseArity(byCount, "BITOR", "BitwiseOr")
	placeBitwiseArity(byCount, "BIT_OR", "BitwiseOr")
	placeBitwiseArity(byCount, "BITXOR", "BitwiseXor")
	placeBitwiseArity(byCount, "BIT_XOR", "BitwiseXor")
	tables.FunctionsByArity = byCount
}

func placeBitwiseArity(byCount map[string]map[int]FuncSpec, name, class string) {
	counts := maps.Clone(byCount[name])
	if counts == nil {
		counts = map[int]FuncSpec{}
	}
	call := bitwiseCall(class)
	counts[2] = call
	counts[3] = call
	byCount[name] = counts
}

func bitwiseCall(class string) FuncSpec {
	return FuncSpec{
		Class: class,
		Args: []FuncArg{
			{Key: "this", Index: 0},
			{Key: "expression", Index: 1},
			{Key: "padside", Index: 2},
		},
	}
}

func (g *generator) writeSnowflakeBitwise(e *Expression) string {
	if g.dialect != "snowflake" {
		return g.spell(e)
	}
	name := snowflakeBitwiseWord(e.Class)
	if name == "" {
		return g.fail(e.Class)
	}
	return name + "(" + snowflakeBitwiseArgs(g, e) + ")"
}

func snowflakeBitwiseWord(class string) string {
	switch class {
	case "BitwiseAnd":
		return "BITAND"
	case "BitwiseOr":
		return "BITOR"
	case "BitwiseXor":
		return "BITXOR"
	default:
		return ""
	}
}

func snowflakeBitwiseArgs(g *generator, e *Expression) string {
	slots := [3]string{"this", "expression", "padside"}
	parts := make([]string, 0, len(slots))
	for _, key := range slots {
		if part := g.child(e, key); part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, ", ")
}

package sqlglot

import "maps"

// Snowflake's RANDOM is a Rand. The bounds are the signed 64-bit range,
// and only the seed is written back.
func init() {
	spellSnowflakeRandom(parserTables["snowflake"])
}

func spellSnowflakeRandom(tables *ParserTables) {
	if tables == nil {
		return
	}
	functions := maps.Clone(tables.Functions)
	functions["RANDOM"] = FuncSpec{
		Class: "Rand",
		Args: []FuncArg{
			{Key: "this", Index: 0},
			{
				Key:    "lower",
				Nested: "Neg",
				NestedArgs: []FuncArg{{
					Key:   "this",
					Index: -1,
					Wrap:  "Literal",
					WrapArgs: []FuncConst{
						{Key: "this", Value: "9.223372036854776E+18"},
						{Key: "is_string", Value: false},
					},
				}},
			},
			{
				Key:   "upper",
				Index: -1,
				Wrap:  "Literal",
				WrapArgs: []FuncConst{
					{Key: "this", Value: "9.223372036854776e+18"},
					{Key: "is_string", Value: false},
				},
			},
		},
	}
	tables.Functions = functions
	generators["Rand"] = (*generator).writeSnowflakeRandom
}

func (g *generator) writeSnowflakeRandom(e *Expression) string {
	if g.dialect != "snowflake" {
		return g.spell(e)
	}
	if seed := g.child(e, "this"); seed != "" {
		return "RANDOM(" + seed + ")"
	}
	return "RANDOM()"
}

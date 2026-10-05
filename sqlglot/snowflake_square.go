package sqlglot

// Snowflake's SQUARE is POWER with the exponent fixed at 2.
// The function map is already Snowflake's own by the time this runs.
func init() {
	tables := parserTables["snowflake"]
	switch {
	case tables == nil:
	case tables.Functions == nil:
	default:
		tables.Functions["SQUARE"] = squaredPower()
	}
}

func squaredPower() FuncSpec {
	exponent := FuncArg{Key: "expression", Index: -1, Wrap: "Literal"}
	exponent.WrapArgs = []FuncConst{
		{Key: "this", Value: "2"},
		{Key: "is_string", Value: false},
	}
	base := FuncArg{Key: "this", Index: 0}
	return FuncSpec{Class: "Pow", Args: []FuncArg{base, exponent}}
}

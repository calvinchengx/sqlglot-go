package sqlglot

// Databricks inherits Hive's NAMED_STRUCT builder. The name is registered
// here, after the generated tables exist, because the probe never recorded
// a signature for a builder that pairs its arguments itself.
func init() {
	tables := parserTables["databricks"]
	if tables == nil || tables.Functions == nil {
		return
	}
	tables.Functions["NAMED_STRUCT"] = FuncSpec{
		Class: "Struct",
		Args:  []FuncArg{{Key: "expressions", VarLen: true}},
	}
}

// namedStruct pairs ('k', v, ...) into PropertyEQ fields. A trailing
// argument with no value is dropped, which is what Hive's builder does.
func namedStruct(args []*Expression) *Expression {
	fields := make([]*Expression, 0, len(args)/2)
	for i := 0; i+1 < len(args); i += 2 {
		value := args[i+1]
		if value == nil {
			continue
		}
		fields = append(fields, New("PropertyEQ",
			Arg{"this", New("Identifier", Arg{"this", args[i].Name()}, Arg{"quoted", false})},
			Arg{"expression", value}))
	}
	return New("Struct", Arg{"expressions", fields})
}

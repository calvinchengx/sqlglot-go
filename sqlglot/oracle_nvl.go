package sqlglot

import "maps"

// Oracle writes NVL, which is a COALESCE that remembers the name. The
// neutral builder records the flag as absent, so the call would come
// back as COALESCE.
func init() {
	tables := parserTables["oracle"]
	if tables == nil {
		return
	}
	funcs := maps.Clone(tables.Functions)
	if spec, ok := funcs["NVL"]; ok {
		args := make([]FuncArg, len(spec.Args))
		copy(args, spec.Args)
		for i := range args {
			if args[i].Key == "is_nvl" {
				args[i].Const = true
			}
		}
		spec.Args = args
		funcs["NVL"] = spec
	}
	tables.Functions = funcs

	sqls := maps.Clone(tables.FunctionSQL)
	nvl := FuncSQL{
		Name:   "NVL",
		Keys:   []string{"this", "expressions"},
		Consts: []FuncConst{{Key: "is_nvl", Value: true}},
	}
	sqls["Coalesce"] = append([]FuncSQL{nvl}, sqls["Coalesce"]...)
	tables.FunctionSQL = sqls
}

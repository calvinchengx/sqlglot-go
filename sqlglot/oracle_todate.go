package sqlglot

// Oracle stores a TO_DATE format in the reference's spelling and writes
// the Oracle letters back. HH12 and HH share a canonical hour, and the
// reference keeps HH12.
var oracleTimeLetters = map[string]string{
	"D":     "%u",
	"DAY":   "%A",
	"DD":    "%d",
	"DDD":   "%j",
	"DY":    "%a",
	"HH":    "%I",
	"HH12":  "%I",
	"HH24":  "%H",
	"IW":    "%V",
	"MI":    "%M",
	"MM":    "%m",
	"MON":   "%b",
	"MONTH": "%B",
	"SS":    "%S",
	"WW":    "%W",
	"YY":    "%y",
	"YYYY":  "%Y",
	"FF6":   "%f",
}

var oracleTimeCanonical = map[string]string{
	"%u": "D",
	"%A": "DAY",
	"%d": "DD",
	"%j": "DDD",
	"%a": "DY",
	"%I": "HH12",
	"%H": "HH24",
	"%V": "IW",
	"%M": "MI",
	"%m": "MM",
	"%b": "MON",
	"%B": "MONTH",
	"%S": "SS",
	"%W": "WW",
	"%y": "YY",
	"%Y": "YYYY",
	"%f": "FF6",
}

func init() {
	tables := parserTables["oracle"]
	tables.TimeMapping = oracleTimeLetters
	tables.InverseTimeMapping = oracleTimeCanonical

	formats := map[string][]int{}
	for name, indexes := range tables.TimeFormatArgs {
		formats[name] = indexes
	}
	formats["TO_DATE"] = []int{1}
	tables.TimeFormatArgs = formats

	spelling := map[string]string{}
	for class, how := range tables.FormatSpellings {
		spelling[class] = how
	}
	spelling["StrToDate"] = "inverse"
	tables.FormatSpellings = spelling

	funcs := map[string]FuncSpec{}
	for name, spec := range tables.Functions {
		funcs[name] = spec
	}
	funcs["TO_DATE"] = FuncSpec{
		Class: "StrToDate",
		Args: []FuncArg{
			{Key: "this", Index: 0},
			{Key: "format", Index: 1},
		},
	}
	tables.Functions = funcs

	sqls := map[string][]FuncSQL{}
	for class, list := range tables.FunctionSQL {
		sqls[class] = list
	}
	withFormat := FuncSQL{Name: "TO_DATE", Keys: []string{"this", "format"}}
	bare := FuncSQL{
		Name:   "TO_DATE",
		Keys:   []string{"this"},
		Consts: []FuncConst{{Key: "format", Value: nil}},
	}
	sqls["StrToDate"] = append([]FuncSQL{withFormat, bare}, sqls["StrToDate"]...)
	tables.FunctionSQL = sqls
}

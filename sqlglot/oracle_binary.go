package sqlglot

// binaryTypeWords are Oracle names for the two IEEE floating types.
// They are not user-defined type names.
var binaryTypeWords = map[string]TokenType{
	"BINARY_FLOAT":  TokFLOAT,
	"BINARY_DOUBLE": TokDOUBLE,
}

// oracleSpelledType reads those names as FLOAT and DOUBLE. Every other
// word keeps the token the keyword table already chose.
func oracleSpelledType(dialect, word string, tt TokenType) TokenType {
	if dialect != "oracle" {
		return tt
	}
	if spelled, ok := binaryTypeWords[word]; ok {
		return spelled
	}
	return tt
}

func init() {
	tables := parserTables["oracle"]
	spellings := map[string]string{}
	for kind, written := range tables.TypeSQL {
		spellings[kind] = written
	}
	spellings["DOUBLE"] = "DOUBLE PRECISION"
	tables.TypeSQL = spellings
}

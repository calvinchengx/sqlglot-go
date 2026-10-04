package sqlglot

// A Snowflake colon path is one extraction. `a:b:c` is the path b.c,
// and the node records that the value is already JSON. Databricks nests
// a second extraction instead, and does not set that flag.
func init() {
	spellSnowflakeColon(parserTables["snowflake"])
}

func spellSnowflakeColon(tables *ParserTables) {
	if tables == nil {
		return
	}
	tables.VariantExtractColon = true
	path := tables.JSONPath
	path.Open = ""
	path.Close = ""
	path.Key = "{key}"
	path.KeyAfter = ".{key}"
	path.Subscript = "[{index}]"
	path.QuotedKey = `["{key}"]`
	tables.JSONPath = path
	tables.JSONExtractSQL = map[string]string{
		"JSONExtract":       "GET_PATH({this}, '{path}')",
		"JSONExtractScalar": "JSON_EXTRACT_SCALAR({this}, {path})",
	}
	tables.JSONPathByClass = map[string]JSONPathSQL{
		"JSONExtract": {
			Key:       "{key}",
			KeyAfter:  ".{key}",
			Subscript: "[{index}]",
			QuotedKey: `["{key}"]`,
			Form:      "GET_PATH({this}, '{path}')",
			PlainForm: "GET_PATH({this}, '{path}')",
		},
	}
}

// snowflakeJSONRequired is the requires_json flag on a colon extraction.
// Snowflake sets it. A dialect that writes the colon back does not.
func snowflakeJSONRequired(dialect string) bool {
	return dialect == "snowflake"
}

// variantPathContinues reports another segment of the same path. A dot
// or a subscript does that everywhere. Snowflake also chains colons:
// `a:b:c` is one path, not an extraction of an extraction.
func (p *parser) variantPathContinues() bool {
	if p.match(TokDOT) || p.at(TokL_BRACKET) {
		return true
	}
	return p.dialect == "snowflake" && p.match(TokCOLON)
}

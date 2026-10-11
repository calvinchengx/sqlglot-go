package sqlglot

// buildSQLiteFunction builds the SQLite functions whose shape the generic
// signature-driven builder cannot express (SQLiteParser.FUNCTIONS).
func (p *parser) buildSQLiteFunction(upper string, args []*Expression) *Expression {
	switch upper {
	case "JSON_EXTRACT":
		// A single-path extract returns the JSON subtype, so it is a
		// JSONExtractScalar flagged `json_subtype`; the multi-path form is a
		// JSONExtract (SQLiteParser._build_json_extract).
		if len(args) == 2 {
			return New("JSONExtractScalar",
				Arg{"this", args[0]}, Arg{"expression", p.jsonPathFor(args[1])},
				Arg{"scalar_only", false}, Arg{"json_subtype", true})
		}
		if len(args) >= 2 {
			node := New("JSONExtract",
				Arg{"this", args[0]}, Arg{"expression", p.jsonPathFor(args[1])})
			if len(args) > 2 {
				// The extra paths are carried as plain literals.
				node.Set("expressions", append([]*Expression{}, args[2:]...))
			}
			return node
		}
	case "JSON_GROUP_OBJECT":
		return New("JSONObjectAgg", Arg{"expressions", args})
	}
	return nil
}

// sqliteOwnedFunction reports the names buildSQLiteFunction builds by hand.
func sqliteOwnedFunction(upper string) bool {
	switch upper {
	case "JSON_EXTRACT", "JSON_GROUP_OBJECT":
		return true
	}
	return false
}

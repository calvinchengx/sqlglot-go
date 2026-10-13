package sqlglot

import "strings"

// bigqueryOwnedFunction reports the names buildBigQueryFunction builds by hand,
// so the generic dispatch does not turn them away for having no recorded
// signature (BigQueryParser.FUNCTIONS is full of lambdas the probe cannot see).
func bigqueryOwnedFunction(upper string) bool {
	switch upper {
	case "WEEK", "DATE_ADD", "DATE_SUB", "DATETIME_ADD", "DATETIME_SUB",
		"TIME_ADD", "TIME_SUB", "TIMESTAMP_ADD", "TIMESTAMP_SUB",
		"SPLIT", "LENGTH", "TO_JSON", "SHA256", "SHA512",
		"NORMALIZE_AND_CASEFOLD":
		return true
	}
	return false
}

// buildBigQueryFunction builds the BigQuery functions whose shape the generic
// signature-driven builder cannot express (BigQueryParser.FUNCTIONS).
func (p *parser) buildBigQueryFunction(upper string, args []*Expression) *Expression {
	switch upper {
	case "WEEK":
		// `WEEK(MONDAY)` names the day a week starts on.
		if len(args) >= 1 {
			return New("WeekStart", Arg{"this", New("Var", Arg{"this", strings.ToUpper(args[0].Name())})})
		}
	case "DATE_ADD", "DATE_SUB", "DATETIME_ADD", "DATETIME_SUB",
		"TIME_ADD", "TIME_SUB", "TIMESTAMP_ADD", "TIMESTAMP_SUB":
		// `DATE_ADD(x, INTERVAL n unit)` -- the reference's
		// build_date_delta_with_interval, which lifts the interval apart.
		class := map[string]string{
			"DATE_ADD": "DateAdd", "DATE_SUB": "DateSub",
			"DATETIME_ADD": "DatetimeAdd", "DATETIME_SUB": "DatetimeSub",
			"TIME_ADD": "TimeAdd", "TIME_SUB": "TimeSub",
			"TIMESTAMP_ADD": "TimestampAdd", "TIMESTAMP_SUB": "TimestampSub",
		}[upper]
		if len(args) >= 2 {
			if iv := args[1]; iv != nil && iv.Class == "Interval" {
				return New(class, Arg{"this", args[0]},
					Arg{"expression", iv.Args["this"]}, Arg{"unit", iv.Args["unit"]})
			}
		}
	case "SPLIT":
		// A separator that was not written is a comma.
		sep := any(New("Literal", Arg{"this", ","}, Arg{"is_string", true}))
		if len(args) >= 2 {
			sep = args[1]
		}
		if len(args) >= 1 {
			return New("Split", Arg{"this", args[0]}, Arg{"expression", sep})
		}
	case "LENGTH":
		if len(args) >= 1 {
			return New("Length", Arg{"this", args[0]}, Arg{"binary", true})
		}
	case "TO_JSON":
		if len(args) >= 1 {
			node := New("JSONFormat", Arg{"this", args[0]}, Arg{"to_json", true})
			if len(args) >= 2 {
				node.Set("options", args[1])
			}
			return node
		}
	case "SHA256", "SHA512":
		if len(args) >= 1 {
			return New("SHA2Digest", Arg{"this", args[0]},
				Arg{"length", New("Literal", Arg{"this", upper[3:]}, Arg{"is_string", false})})
		}
	case "NORMALIZE_AND_CASEFOLD":
		if len(args) >= 1 {
			node := New("Normalize", Arg{"this", args[0]}, Arg{"is_casefold", true})
			if len(args) >= 2 {
				node.Set("form", args[1])
			}
			return node
		}
	}
	return nil
}

// bigqueryOwnedParser reports the names BigQuery reads with a grammar of their
// own, where the generic argument reader cannot help
// (BigQueryParser.FUNCTION_PARSERS). The opening parenthesis is already
// consumed when these are called.
func bigqueryOwnedParser(upper string) bool {
	switch upper {
	case "ARRAY", "JSON_ARRAY", "MAKE_INTERVAL", "PREDICT", "TRANSLATE",
		"FEATURES_AT_TIME", "GENERATE_EMBEDDING", "GENERATE_TEXT_EMBEDDING",
		"GENERATE_TEXT", "GENERATE_TABLE", "GENERATE_BOOL", "GENERATE_INT",
		"GENERATE_DOUBLE", "VECTOR_SEARCH", "FORECAST":
		return true
	}
	return false
}

// parseBigQueryFunctionParser reads one of those grammars. The name and the
// opening parenthesis are already consumed.
func (p *parser) parseBigQueryFunctionParser(upper string) (*Expression, error) {
	switch upper {
	case "ARRAY":
		stmt, err := p.parseStatement()
		if err != nil {
			return nil, err
		}
		return New("Array", Arg{"expressions", []*Expression{stmt}},
			Arg{"struct_name_inheritance", true}), nil
	case "JSON_ARRAY":
		items, err := p.parseBigQueryCSV(p.parseBitwise)
		if err != nil {
			return nil, err
		}
		if !p.match(TokR_PAREN) {
			return nil, p.unsupported("unclosed JSON_ARRAY")
		}
		return New("JSONArray", Arg{"expressions", items}), nil
	case "MAKE_INTERVAL":
		return p.parseBigQueryMakeInterval()
	case "PREDICT":
		return p.parseBigQueryML("Predict")
	case "GENERATE_EMBEDDING", "GENERATE_TEXT_EMBEDDING":
		node, err := p.parseBigQueryML("GenerateEmbedding")
		if err != nil {
			return nil, err
		}
		if upper == "GENERATE_TEXT_EMBEDDING" {
			node.Set("is_text", true)
		}
		return node, nil
	case "GENERATE_TEXT", "GENERATE_TABLE", "GENERATE_BOOL", "GENERATE_INT", "GENERATE_DOUBLE":
		class := map[string]string{
			"GENERATE_TEXT": "GenerateText", "GENERATE_TABLE": "GenerateTable",
			"GENERATE_BOOL": "GenerateBool", "GENERATE_INT": "GenerateInt",
			"GENERATE_DOUBLE": "GenerateDouble",
		}[upper]
		return p.parseBigQueryGenerate(class)
	case "FEATURES_AT_TIME":
		return p.parseBigQueryFeaturesAtTime()
	case "VECTOR_SEARCH":
		return p.parseBigQueryVectorSearch()
	case "FORECAST":
		return p.parseBigQueryForecast()
	case "TRANSLATE":
		// ML.TRANSLATE is the ML shape; a bare TRANSLATE is the ordinary one.
		if p.index >= 4 {
			if t := p.tokens[p.index-4]; strings.EqualFold(t.Text, "ML") {
				return p.parseBigQueryML("MLTranslate")
			}
		}
		args, err := p.parseBigQueryCSV(p.parseLambda)
		if err != nil {
			return nil, err
		}
		if !p.match(TokR_PAREN) {
			return nil, p.unsupported("unclosed TRANSLATE")
		}
		return New("Translate", Arg{"expressions", args}), nil
	}
	return nil, p.unsupported(upper)
}

// parseBigQueryML reads `[MODEL] table [, TABLE] [table] [, params]`.
func (p *parser) parseBigQueryML(class string) (*Expression, error) {
	p.matchUnquotedWord("MODEL")
	this, err := p.parseTable()
	if err != nil {
		return nil, err
	}
	p.match(TokCOMMA)
	p.matchUnquotedWord("TABLE")
	var expression *Expression
	if !p.at(TokSTRUCT) {
		expression, err = p.parseTable()
		if err != nil {
			return nil, err
		}
	}
	p.match(TokCOMMA)
	params, err := p.parseBitwise()
	if err != nil {
		return nil, err
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed " + class)
	}
	return New(class, Arg{"this", this}, Arg{"expression", expression},
		Arg{"params_struct", params}), nil
}

// parseBigQueryGenerate reads `[MODEL] table, <table|expr> [, params]`.
func (p *parser) parseBigQueryGenerate(class string) (*Expression, error) {
	p.matchUnquotedWord("MODEL")
	this, err := p.parseTable()
	if err != nil {
		return nil, err
	}
	p.match(TokCOMMA)
	var expression *Expression
	if p.matchUnquotedWord("TABLE") {
		expression, err = p.parseTable()
	} else if p.at(TokL_PAREN) {
		expression, err = p.parseTable()
	} else {
		expression, err = p.parseBitwise()
	}
	if err != nil {
		return nil, err
	}
	var params any
	if p.match(TokCOMMA) {
		params, err = p.parseBitwise()
		if err != nil {
			return nil, err
		}
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed " + class)
	}
	return New(class, Arg{"this", this}, Arg{"expression", expression},
		Arg{"params_struct", params}), nil
}

// parseBigQueryMakeInterval fills the six interval parts, positional first.
func (p *parser) parseBigQueryMakeInterval() (*Expression, error) {
	keys := []string{"year", "month", "day", "hour", "minute", "second"}
	node := New("MakeInterval")
	for _, key := range keys {
		if p.at(TokR_PAREN) {
			break
		}
		value, err := p.parseLambda()
		if err != nil {
			return nil, err
		}
		if value == nil {
			break
		}
		if value.Class == "Kwarg" {
			if lhs, _ := value.Args["this"].(*Expression); lhs != nil {
				key = lhs.Name()
			}
		}
		node.Set(key, value)
		p.match(TokCOMMA)
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed MAKE_INTERVAL")
	}
	return node, nil
}

// parseBigQueryFeaturesAtTime reads `[TABLE] table [, key => value ...]`.
func (p *parser) parseBigQueryFeaturesAtTime() (*Expression, error) {
	p.matchUnquotedWord("TABLE")
	this, err := p.parseTable()
	if err != nil {
		return nil, err
	}
	node := New("FeaturesAtTime", Arg{"this", this})
	for p.match(TokCOMMA) {
		arg, err := p.parseLambda()
		if err != nil {
			return nil, err
		}
		if arg != nil {
			if lhs, _ := arg.Args["this"].(*Expression); lhs != nil {
				node.Set(lhs.Name(), arg)
			}
		}
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed FEATURES_AT_TIME")
	}
	return node, nil
}

// parseBigQueryVectorSearch reads `[TABLE] t, column, [TABLE] t [, ...]`.
func (p *parser) parseBigQueryVectorSearch() (*Expression, error) {
	p.matchUnquotedWord("TABLE")
	base, err := p.parseTable()
	if err != nil {
		return nil, err
	}
	p.match(TokCOMMA)
	column, err := p.parseBitwise()
	if err != nil {
		return nil, err
	}
	p.match(TokCOMMA)
	p.matchUnquotedWord("TABLE")
	query, err := p.parseTable()
	if err != nil {
		return nil, err
	}
	node := New("VectorSearch", Arg{"this", base},
		Arg{"column_to_search", column}, Arg{"query_table", query})
	for p.match(TokCOMMA) {
		if p.at(TokSTRING) {
			s, err := p.parsePrimary()
			if err != nil {
				return nil, err
			}
			node.Set("query_column_to_search", s)
			continue
		}
		arg, err := p.parseLambda()
		if err != nil {
			return nil, err
		}
		if arg != nil {
			if lhs, _ := arg.Args["this"].(*Expression); lhs != nil {
				node.Set(lhs.Name(), arg)
			}
		}
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed VECTOR_SEARCH")
	}
	return node, nil
}

// parseBigQueryForecast reads AI.FORECAST's `[TABLE] t [, key => value ...]`.
func (p *parser) parseBigQueryForecast() (*Expression, error) {
	if p.index >= 4 {
		if t := p.tokens[p.index-4]; strings.EqualFold(t.Text, "ML") {
			return p.parseBigQueryML("MLForecast")
		}
	}
	p.matchUnquotedWord("TABLE")
	this, err := p.parseTable()
	if err != nil {
		return nil, err
	}
	node := New("AIForecast", Arg{"this", this})
	for p.match(TokCOMMA) {
		arg, err := p.parseLambda()
		if err != nil {
			return nil, err
		}
		if arg != nil {
			if lhs, _ := arg.Args["this"].(*Expression); lhs != nil {
				node.Set(lhs.Name(), arg)
			}
		}
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed FORECAST")
	}
	return node, nil
}

// parseBigQueryCSV reads a comma-separated list up to (but not including) the
// closing parenthesis, which the caller matches.
func (p *parser) parseBigQueryCSV(member func() (*Expression, error)) ([]*Expression, error) {
	var out []*Expression
	for !p.at(TokR_PAREN) {
		e, err := member()
		if err != nil {
			return nil, err
		}
		out = append(out, e)
		if !p.match(TokCOMMA) {
			break
		}
	}
	return out, nil
}

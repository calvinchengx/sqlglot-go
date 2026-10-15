package sqlglot

import (
	"regexp"
	"strings"
)

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
		// `TO_JSON(x, stringify_wide_numbers => FALSE)` keeps the options
		// under their own key, written BEFORE the flag that makes this the
		// JSON reading (BigQueryParser.FUNCTIONS).
		if len(args) >= 1 {
			node := New("JSONFormat", Arg{"this", args[0]})
			if len(args) >= 2 {
				node.Set("options", args[1])
			}
			node.Set("to_json", true)
			return node
		}
	case "JSON_STRIP_NULLS":
		// The named arguments are keys of their own, named after the
		// argument rather than read positionally
		// (BigQueryParser._build_json_strip_nulls).
		if len(args) >= 1 {
			node := New("JSONStripNulls", Arg{"this", args[0]})
			for _, a := range args[1:] {
				if a == nil {
					continue
				}
				if a.Class == "Kwarg" {
					if k := childOf(a, "this"); k != nil {
						node.Set(strings.ToLower(k.Name()), a)
					}
					continue
				}
				node.Set("expression", a)
			}
			return node
		}
	case "SHA256", "SHA512":
		if len(args) >= 1 {
			return New("SHA2Digest", Arg{"this", args[0]},
				Arg{"length", New("Literal", Arg{"this", upper[3:]}, Arg{"is_string", false})})
		}
	case "NORMALIZE_AND_CASEFOLD":
		// The form is written BEFORE the flag, in the node's own order
		// (BigQueryParser.FUNCTIONS).
		if len(args) >= 1 {
			node := New("Normalize", Arg{"this", args[0]})
			if len(args) >= 2 {
				node.Set("form", args[1])
			}
			node.Set("is_casefold", true)
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
		// `ARRAY(SELECT ...)` reads the query itself, and the closing
		// parenthesis is consumed here rather than by the argument list
		// (BigQueryParser.FUNCTION_PARSERS).
		stmt, err := p.parseStatement()
		if err != nil {
			return nil, err
		}
		if !p.match(TokR_PAREN) {
			return nil, p.unsupported("unclosed ARRAY")
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
	// A generate call that names no parameters records FALSE rather than
	// nothing: the reference's `self._match(COMMA) and self._parse_bitwise()`
	// short-circuits (BigQueryParser._parse_generate).
	params := any(false)
	if p.match(TokCOMMA) {
		value, err := p.parseBitwise()
		if err != nil {
			return nil, err
		}
		params = value
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
		value, err := p.parseBigQueryLambda()
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
		arg, err := p.parseBigQueryLambda()
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
		arg, err := p.parseBigQueryLambda()
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
		arg, err := p.parseBigQueryLambda()
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

// parseBigQueryLambda reads what the ML family's arguments are: a named
// argument (`key => value`) where one stands, and a lambda or expression
// otherwise (Parser._parse_lambda).
func (p *parser) parseBigQueryLambda() (*Expression, error) {
	if p.atKwarg() {
		return p.parseKwarg()
	}
	// An arrow is what MAKES it a lambda; without one the argument is the
	// expression itself, which is how MAKE_INTERVAL(100, 11, ...) reads
	// (Parser._parse_lambda).
	if p.atLambda() {
		if value, err := p.parseLambda(); err != nil || value != nil {
			return value, err
		}
	}
	return p.parseExpression()
}

// normalizeBareWeek rewrites a bare WEEK date part into WEEK(SUNDAY): BigQuery
// reads the two as the same, and the tree records the day explicitly
// (BigQueryParser._normalize_bare_week).
func normalizeBareWeek(upper string, expr *Expression) *Expression {
	if expr == nil {
		return expr
	}
	switch upper {
	case "DATE_DIFF", "DATETIME_DIFF", "DATE_TRUNC", "DATETIME_TRUNC",
		"TIMESTAMP_TRUNC", "LAST_DAY":
	default:
		return expr
	}
	unit, _ := expr.Args["unit"].(*Expression)
	if unit == nil || (unit.Class != "Literal" && unit.Class != "Var") {
		return expr
	}
	if strings.ToUpper(unit.Name()) != "WEEK" {
		return expr
	}
	expr.Set("unit", New("WeekStart", Arg{"this", New("Var", Arg{"this", "SUNDAY"})}))
	return expr
}

// normalizeRegexpGroup records the capture group BigQuery's REGEXP_EXTRACT
// returns: when the pattern holds exactly ONE group, that group is the one
// read, and the tree records it as `group` = 1. REGEXP_EXTRACT_ALL reads
// group 0 unless a pattern names one, so its default is written instead
// (BigQueryParser._build_regexp_extract).
func normalizeRegexpGroup(upper string, expr *Expression) *Expression {
	if expr == nil {
		return expr
	}
	var fallback string
	switch upper {
	case "REGEXP_EXTRACT", "REGEXP_SUBSTR":
		if expr.Class != "RegexpExtract" {
			return expr
		}
	case "REGEXP_EXTRACT_ALL":
		if expr.Class != "RegexpExtractAll" {
			return expr
		}
		fallback = "0"
	default:
		return expr
	}
	// The reference compiles the SECOND argument's NAME, whatever node that
	// argument is -- a raw string reads the same way a plain one does.
	pat, _ := expr.Args["expression"].(*Expression)
	if pat == nil {
		return expr
	}
	text := pat.Name()
	group := fallback
	if captureGroupCount(text) == 1 {
		group = "1"
	}
	if group == "" {
		// The reference passes no group at all rather than a null one.
		expr.Args["group"] = nil
		return expr
	}
	expr.Set("group", New("Literal", Arg{"this", group}, Arg{"is_string", false}))
	return expr
}

// captureGroupCount counts the capturing groups in a regular expression the
// way the reference's `re.compile(pattern).groups` does: a pattern that does
// not compile has none.
func captureGroupCount(pattern string) int {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return 0
	}
	return re.NumSubexp()
}

// bigqueryColumnParts splits a BigQuery column name that was written inside
// one pair of backticks, the same way a table name is split, so that
// “ `db.t`.`c` “ names three parts rather than two
// (BigQueryParser._parse_column).
func bigqueryColumnParts(col *Expression) *Expression {
	var names []string
	hasDot := false
	for _, key := range []string{"catalog", "db", "table", "this"} {
		e, _ := col.Args[key].(*Expression)
		if e == nil {
			continue
		}
		names = append(names, e.Name())
		if strings.Contains(e.Name(), ".") {
			hasDot = true
		}
	}
	if !hasDot {
		return col
	}
	words := splitQualifiedName(strings.Join(names, "."), 4)
	ident := func(w string) *Expression {
		if w == "" {
			return nil
		}
		return New("Identifier", Arg{"this", w}, Arg{"quoted", true})
	}
	newThis := ident(words[3])
	for _, w := range words[4:] {
		newThis = New("Dot", Arg{"this", newThis}, Arg{"expression", ident(w)})
	}
	out := New("Column",
		Arg{"this", newThis},
		Arg{"table", ident(words[2])},
		Arg{"db", ident(words[1])},
		Arg{"catalog", ident(words[0])})
	// The qualifier was written inside one pair of backticks, and that is how
	// it goes back out (BigQueryGenerator.column_parts).
	out.quotedColumn = true
	return out
}

// bigqueryBracketOffset names the numbering a subscript's own function asks
// for (BigQueryParser.BRACKET_OFFSETS).
func bigqueryBracketOffset(name string) (offset int, safe bool, ok bool) {
	switch name {
	case "OFFSET":
		return 0, false, true
	case "ORDINAL":
		return 1, false, true
	case "SAFE_OFFSET":
		return 0, true, true
	case "SAFE_ORDINAL":
		return 1, true, true
	}
	return 0, false, false
}

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

package sqlglot

// singlestoreOwnedFunction reports the names buildSingleStoreFunction builds
// by hand, so the generic dispatch does not turn them away for having no
// recorded signature.
func singlestoreOwnedFunction(upper string) bool {
	switch upper {
	case "STR_TO_DATE", "TO_DATE", "TO_TIMESTAMP", "TO_CHAR",
		"DATE_FORMAT", "FROM_UNIXTIME", "TIME_FORMAT",
		"HOUR", "MICROSECOND", "SECOND", "MINUTE",
		"MONTHNAME", "DAYNAME", "WEEKDAY", "TIME_BUCKET", "TIMESTAMPDIFF",
		"DATE_ADD", "DATE_SUB":
		return true
	}
	return false
}

// buildSingleStoreFunction builds the SingleStore functions whose shape the
// generic signature-driven builder cannot express (SingleStoreParser.FUNCTIONS).
// It returns nil for anything it does not own, so the caller falls through to
// the generic path.
func (p *parser) buildSingleStoreFunction(upper string, args []*Expression) *Expression {
	switch upper {
	case "STR_TO_DATE":
		// build_formatted_time(exp.StrToDate, "mysql").
		return p.singlestoreFormattedTime("StrToDate", args, true)

	case "TO_DATE", "TO_TIMESTAMP", "TO_CHAR":
		class := map[string]string{
			"TO_DATE": "TsOrDsToDate", "TO_TIMESTAMP": "StrToTime", "TO_CHAR": "ToChar",
		}[upper]
		return p.singlestoreFormattedTime(class, args, false)

	case "DATE_FORMAT", "FROM_UNIXTIME":
		class := map[string]string{"DATE_FORMAT": "TimeToStr", "FROM_UNIXTIME": "UnixToTime"}[upper]
		return p.singlestoreFormattedTime(class, args, true)

	case "TIME_FORMAT":
		if len(args) == 2 {
			return New("TimeToStr",
				Arg{"this", singlestoreCastToTime6(args[0])},
				Arg{"format", p.singlestoreFormat(args[1], true)})
		}

	case "HOUR", "MICROSECOND", "SECOND", "MINUTE":
		// Each reads a time-of-day field as a number: the value rendered
		// through one format specifier and cast to INT.
		spec := map[string]string{
			"HOUR": "%k", "MICROSECOND": "%f", "SECOND": "%s", "MINUTE": "%i",
		}[upper]
		if len(args) == 1 {
			inner := New("TimeToStr",
				Arg{"this", singlestoreCastToTime6(args[0])},
				Arg{"format", p.singlestoreFormat(New("Literal", Arg{"this", spec}, Arg{"is_string", true}), true)})
			to := New("DataType", Arg{"this", DataTypeKind("INT")})
			cast := New("Cast", Arg{"this", inner}, Arg{"to", to})
			cast.Type = to
			return cast
		}

	case "MONTHNAME", "DAYNAME":
		spec := map[string]string{"MONTHNAME": "%M", "DAYNAME": "%W"}[upper]
		if len(args) == 1 {
			return New("TimeToStr",
				Arg{"this", args[0]},
				Arg{"format", p.singlestoreFormat(New("Literal", Arg{"this", spec}, Arg{"is_string", true}), true)})
		}

	case "WEEKDAY":
		// `(DAYOFWEEK(x) + 5) % 7` -- Monday-based, zero-indexed.
		if len(args) == 1 {
			plusFive := New("Add",
				Arg{"this", New("DayOfWeek", Arg{"this", args[0]})},
				Arg{"expression", New("Literal", Arg{"this", "5"}, Arg{"is_string", false})})
			return New("Mod",
				Arg{"this", New("Paren", Arg{"this", plusFive})},
				Arg{"expression", New("Literal", Arg{"this", "7"}, Arg{"is_string", false})})
		}

	case "DATE_ADD", "DATE_SUB":
		// SingleStore inherits MySQL's interval delta: the second argument is
		// read as an INTERVAL and unwrapped onto the call.
		if len(args) == 2 {
			if built := buildMySQLDateDeltaWithInterval(upper, args); built != nil {
				return built
			}
		}

	case "TIME_BUCKET":
		return New("DateBin",
			Arg{"this", argAt(args, 0)},
			Arg{"expression", argAt(args, 1)},
			Arg{"origin", argAt(args, 2)})

	case "TIMESTAMPDIFF":
		if len(args) == 3 {
			// The unit is a bare word, which the reference holds as a Var.
			unit := args[0]
			if unit != nil && unit.Class == "Column" {
				unit = New("Var", Arg{"this", unit.Name()})
			}
			return New("TimestampDiff",
				Arg{"this", args[2]}, Arg{"expression", args[1]}, Arg{"unit", unit})
		}
	}
	return nil
}

// singlestoreFormattedTime is the reference's build_formatted_time for the
// functions that carry a format. `mysql` says the format is spelled in MySQL's
// own time syntax (build_formatted_time's "mysql" override, and the
// MySQL.format_time the field functions call directly); otherwise it is
// SingleStore's own Oracle-style syntax.
func (p *parser) singlestoreFormattedTime(class string, args []*Expression, mysql bool) *Expression {
	return New(class, Arg{"this", argAt(args, 0)}, Arg{"format", p.singlestoreFormat(argAt(args, 1), mysql)})
}

// singlestoreFormat translates a format string the way the reference's
// Dialect.format_time does -- through the named dialect's TIME_MAPPING --
// leaving a non-literal alone.
func (p *parser) singlestoreFormat(format *Expression, mysql bool) *Expression {
	if !isStringLiteral(format) {
		return format
	}
	text, _ := format.Args["this"].(string)
	mapping := p.tables.TimeMapping
	if mysql {
		if mt := parserTables["mysql"]; mt != nil {
			mapping = mt.TimeMapping
		}
	}
	return New("Literal", Arg{"this", formatTime(text, mapping)}, Arg{"is_string", true})
}

// singlestoreCastToTime6 is the reference's cast_to_time6: a CAST to TIME(6),
// which several of SingleStore's time functions wrap their argument in so a
// bare `12:05:47` reads.
func singlestoreCastToTime6(e *Expression) *Expression {
	to := New("DataType",
		Arg{"this", DataTypeKind("TIME")},
		Arg{"expressions", []*Expression{New("DataTypeParam",
			Arg{"this", New("Literal", Arg{"this", "6"}, Arg{"is_string", false})})}})
	cast := New("Cast", Arg{"this", e}, Arg{"to", to})
	cast.Type = to
	return cast
}

package sqlglot

import "strings"

// Builders that READ the arguments they are handed.
//
// Almost every function node the port builds comes from a signature the probe
// recorded by driving the reference's own builder with placeholder columns.
// That works because almost every builder only moves its arguments into
// slots. A few decide what to BUILD from what is in them -- and a placeholder
// column tells the probe nothing about that, so the recorded signature
// describes a shape the reference never produces for real SQL. Those builders
// are written out here, from the reference, and pinned by tests.

// buildFormat is T-SQL's FORMAT.
//
// The reference reads the format it is given and builds one of two nodes from
// it: a spelling with no date field anywhere in it is a NUMBER format and
// stays as written, and anything else is a time format, rewritten into the
// reference's own spelling on the way in -- through a table of its own when
// the whole format is a single character, and through the dialect's ordinary
// time mapping otherwise.
//
// Enabled by the dialect having a FORMAT_TIME_MAPPING at all, which is the
// same condition that installs this builder in the reference.
func (p *parser) buildFormat(args []*Expression) (*Expression, error) {
	this := argAt(args, 0)
	format := argAt(args, 1)
	culture := argAt(args, 2)

	// Both nodes require a format, and the reference rejects the call outright
	// when it is missing rather than building one without.
	if format == nil {
		return nil, p.unsupported("FORMAT without a format")
	}
	name := format.Name()
	if name == "N" || name == "C" || !hasDateField(name) {
		return New("NumberToStr",
			Arg{"this", this}, Arg{"format", format}, Arg{"culture", culture}), nil
	}
	mapping := p.tables.TimeMapping
	if len([]rune(name)) == 1 {
		mapping = p.tables.FormatTimeMapping
	}
	// A fresh string literal whatever the argument was: the reference takes
	// the NAME off it and builds a literal from that, so a column in this slot
	// comes back as a quoted string.
	return New("TimeToStr",
		Arg{"this", this},
		Arg{"format", New("Literal",
			Arg{"this", formatTime(name, mapping)}, Arg{"is_string", true})},
		Arg{"culture", culture}), nil
}

// hasDateField reports whether a format spelling names any date or time field.
//
// The reference asks a regexp -- `([dD]{1,2})|([mM]{1,2})|([yY]{1,4})|
// ([hH]{1,2})|([sS]{1,2})` -- but it asks it with `search`, so the repetition
// counts never decide anything: one of these characters anywhere is a match.
func hasDateField(s string) bool {
	return strings.ContainsAny(s, "dDmMyYhHsS")
}

// buildDateDiff is T-SQL's DATEDIFF and DATEDIFF_BIG:
// DATEDIFF(unit, start, end) reads as DateDiff(this=end, expression=start,
// unit=Var(unit)) -- the two dates SWAP position, and each is wrapped in
// TimeStrToTime -- except where start is a non-integer NUMBER, which the
// reference keeps bare and does not swap either, the one shape a probe run
// with placeholder columns could never see.
func (p *parser) buildDateDiff(upper string, args []*Expression, bigInt bool) (*Expression, error) {
	if len(args) != 3 {
		return nil, p.unsupported("function " + upper + " with this many arguments")
	}
	unit := args[0]
	start, end := args[1], args[2]
	word := strings.ToUpper(unit.Name())
	if aliases, ok := p.tables.UnitAliases[upper]; ok {
		if full, ok := aliases[word]; ok {
			word = full
		}
	}
	unitVar := New("Var", Arg{"this", word})
	if start != nil && start.Class == "Literal" {
		if isString, _ := start.Args["is_string"].(bool); !isString {
			if isIntegerLiteral(start) {
				return nil, p.unsupported("function " + upper + " over an integer date")
			}
			return New("DateDiff",
				Arg{"this", end}, Arg{"expression", start},
				Arg{"unit", unitVar}, Arg{"big_int", bigInt}), nil
		}
	}
	return New("DateDiff",
		Arg{"this", New("TimeStrToTime", Arg{"this", end})},
		Arg{"expression", New("TimeStrToTime", Arg{"this", start})},
		Arg{"unit", unitVar}, Arg{"big_int", bigInt}), nil
}

// buildDateName is T-SQL's DATENAME(part, date): the part is read through
// TimeMapping merged with FullFormatTimeMapping, so `mm` gives the full month
// name rather than FORMAT's two-digit one, and the date is cast to DATETIME2
// rather than left as written.
func (p *parser) buildDateName(args []*Expression) (*Expression, error) {
	if len(args) != 2 {
		return nil, p.unsupported("function DATENAME with this many arguments")
	}
	part, this := args[0], args[1]
	spelled := formatTime(strings.ToLower(part.Name()), p.tables.FullFormatTimeMapping)
	to := New("DataType", Arg{"this", DataTypeKind("DATETIME2")})
	cast := New("Cast", Arg{"this", this}, Arg{"to", to})
	// A cast the port BUILDS carries its type already, as every cast the
	// reference builds does; see wrapStringArgument.
	cast.Type = to
	return New("TimeToStr",
		Arg{"this", cast},
		Arg{"format", New("Literal", Arg{"this", spelled}, Arg{"is_string", true})}), nil
}

func argAt(args []*Expression, i int) *Expression {
	if i < len(args) {
		return args[i]
	}
	return nil
}

// fromArgList places a call's arguments onto a class's own argument keys, in
// order, the way the reference's Func.from_arg_list does: the zip stops at
// whichever runs out first, and an argument that is not there at all leaves
// its key absent rather than present and empty.
//
// The reference's variadic tail is not handled -- no hand-written parser here
// places arguments onto a class that has one, and the probed signatures cover
// every call that does.
func fromArgList(class string, args []*Expression) *Expression {
	node := New(class)
	for i, key := range classArgKeys[class] {
		if i >= len(args) {
			break
		}
		node.Set(key, args[i])
	}
	return node
}

// wrapStringArgument rewrites a string argument the way the builder that
// receives it would. Only two shapes are recorded, and both are read off the
// reference rather than named here: a CAST to a type it chose, and the
// reference's own `to_interval` -- which is `INTERVAL <text>` parsed in the
// NEUTRAL dialect, whatever dialect the call was written in.
func (p *parser) wrapStringArgument(arg *Expression, how string) (*Expression, error) {
	text, _ := arg.Args["this"].(string)
	if kind, ok := strings.CutPrefix(how, "cast:"); ok {
		// No `nested` arg: a type the reference BUILDS carries only its
		// name, where one that was WRITTEN records nested as well.
		to := New("DataType", Arg{"this", DataTypeKind(kind)})
		cast := New("Cast", Arg{"this", arg}, Arg{"to", to})
		// A cast the port BUILDS carries its type already, as every cast the
		// reference builds does. Leaving it to the annotator is a different
		// tree: the type is recorded on the node, and the differential
		// compares it.
		cast.Type = to
		return cast, nil
	}
	if how == "interval" {
		interval, err := ParseOne("INTERVAL "+text, "")
		if err != nil || interval == nil || interval.Class != "Interval" {
			return nil, p.unsupported("a step this port cannot read as an interval")
		}
		return interval, nil
	}
	return nil, p.unsupported("a string argument this port cannot rewrite")
}

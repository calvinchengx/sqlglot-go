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

func argAt(args []*Expression, i int) *Expression {
	if i < len(args) {
		return args[i]
	}
	return nil
}

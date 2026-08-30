package sqlglot

import (
	"regexp"
	"strings"
)

// intervalStringRE is sqlglot's INTERVAL_STRING_RE, character for character.
// It is what decides whether `INTERVAL '1 day'` is one quantity with a unit
// inside the string or a literal to be left alone.
var intervalStringRE = regexp.MustCompile(`\s*(-?[0-9]+(?:\.[0-9]+)?)\s*([a-zA-Z]+)\s*`)

// parseInterval reads INTERVAL and what follows it. Entered with INTERVAL
// current.
//
// The unit can arrive three ways and the reference normalises all of them to
// the same tree: `INTERVAL '1' DAY`, `INTERVAL 1 DAY` (the number becomes a
// STRING literal) and `INTERVAL '1 day'` (the unit is inside the string and is
// split back out, upper-cased). A string holding MORE than one quantity --
// `'1 year 2 months'` -- is left whole with no unit, which is why the count of
// matches decides rather than the first match.
func (p *parser) parseInterval() (*Expression, error) {
	start := p.index
	p.advance() // INTERVAL

	// INTERVAL is a NAME wherever what follows it is not a quantity: `SELECT
	// interval` selects a column, and `WHERE interval IS NULL` tests one. The
	// reference reads the operand and puts everything back where it did not
	// find one -- or found a bare column with no unit after it, which is a
	// name too. Returning nothing here lets the caller read the word.
	// A STRING is read on its own; anything else is read as a TERM, so that
	// `INTERVAL 1 + 2` is a quantity and `interval + 1` is one too.
	var this *Expression
	var err error
	if c := p.curr(); c != nil && c.Type == TokSTRING {
		this, err = p.parsePrimary()
	} else {
		this, err = p.parseTerm()
	}
	if err != nil || this == nil || p.namesAColumnRatherThanAQuantity(this) {
		p.index = start
		return nil, nil
	}

	unitIndex := p.index
	var unit *Expression
	if c := p.curr(); c != nil && c.Type == TokVAR && !strings.EqualFold(c.Text, "TO") {
		p.advance()
		unit = New("Var", Arg{"this", p.normalisedIntervalUnit(c.Text)})
	}

	switch {
	case this != nil && this.Class == "Literal" && this.Args["is_string"] == false:
		// `INTERVAL 1 DAY`: the reference stores the quantity as a STRING.
		this.Set("is_string", true)
	case this != nil && this.Class == "Literal" && this.Args["is_string"] == true:
		text, _ := this.Args["this"].(string)
		parts := intervalStringRE.FindAllStringSubmatch(text, -1)
		if len(parts) > 0 && unit != nil {
			// The real unit was inside the string, so give the token back.
			unit = nil
			p.index = unitIndex
		}
		if len(parts) == 1 {
			this = New("Literal", Arg{"this", parts[0][1]}, Arg{"is_string", true})
			unit = New("Var", Arg{"this", p.normalisedIntervalUnit(parts[0][2])})
		}
	}

	// `HOUR TO SECOND` is one unit spanning two, not two units.
	if c := p.curr(); c != nil && c.Type == TokVAR && strings.EqualFold(c.Text, "TO") {
		p.advance()
		end := p.curr()
		if end == nil {
			return nil, p.unsupported("INTERVAL span without an end unit")
		}
		p.advance()
		unit = New("IntervalSpan",
			Arg{"this", unit},
			Arg{"expression", New("Var", Arg{"this", p.normalisedIntervalUnit(end.Text)})})
	}

	return New("Interval", Arg{"this", this}, Arg{"unit", unit}), nil
}

// normalisedIntervalUnit is the unit the reference records for a spelling. Most words
// are simply upper-cased, and the short forms are NAMES for the long ones:
// `INTERVAL '500 us'` records MICROSECOND. Which spellings normalise is
// probed rather than transcribed -- `usec` and `hrs` are in the reference's
// own table and come back unchanged.
func (p *parser) normalisedIntervalUnit(text string) string {
	upper := strings.ToUpper(text)
	if full, ok := p.tables.IntervalUnitAliases[upper]; ok {
		return full
	}
	return upper
}

// namesAColumnRatherThanAQuantity reports whether what followed INTERVAL was a
// bare column with no unit after it -- in which case INTERVAL was a name and
// the column is the next thing in the statement, not its quantity.
func (p *parser) namesAColumnRatherThanAQuantity(this *Expression) bool {
	if this.Class != "Column" {
		return false
	}
	if table, _ := this.Args["table"].(*Expression); table != nil {
		return false
	}
	if name, _ := this.Args["this"].(*Expression); name == nil || name.Args["quoted"] == true {
		return false
	}
	c := p.curr()
	if c == nil {
		return true
	}
	_, isUnit := p.tables.ValidIntervalUnits[strings.ToUpper(c.Text)]
	return !isUnit
}

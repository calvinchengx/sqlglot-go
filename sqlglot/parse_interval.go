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
	p.advance() // INTERVAL

	this, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}

	unitIndex := p.index
	var unit *Expression
	if c := p.curr(); c != nil && c.Type == TokVAR && !strings.EqualFold(c.Text, "TO") {
		p.advance()
		unit = New("Var", Arg{"this", strings.ToUpper(c.Text)})
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
			unit = New("Var", Arg{"this", strings.ToUpper(parts[0][2])})
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
			Arg{"expression", New("Var", Arg{"this", strings.ToUpper(end.Text)})})
	}

	return New("Interval", Arg{"this", this}, Arg{"unit", unit}), nil
}

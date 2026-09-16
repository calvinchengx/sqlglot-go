package sqlglot

import (
	"strconv"
	"strings"
	"time"
)

// dateTimeLayout and dateLayout are the only two spellings a DATE/DATETIME
// literal's own text takes in this port's dialects: no offset, no fractional
// seconds -- the fixture never needs either, and guessing at one that is not
// there is worse than declining.
const (
	dateLayout     = "2006-01-02"
	dateTimeLayout = "2006-01-02 15:04:05"
)

// extractDateValue reads a date the way the reference's extract_date does,
// but scoped to the two shapes this port's fixture actually needs: a CAST of
// a string literal to DATE/DATETIME/TIMESTAMP, and a TS_OR_DS_TO_DATE over a
// string literal with no format. Both come back as a date-only value: a
// TS_OR_DS_TO_DATE truncates whatever precision its text carried, and a
// plain CAST(... AS DATE) never had a time component to keep. isDatetime
// says whether the CALLER should format the result back with one, which is
// a property of the CAST's own target type, not of the value.
func extractDateValue(e *Expression) (t time.Time, isDatetime bool, ok bool) {
	switch e.Class {
	case "Cast":
		typeName := typeKind(childOf(e, "to"))
		switch typeName {
		case "DATE":
			isDatetime = false
		case "DATETIME", "TIMESTAMP":
			isDatetime = true
		default:
			return time.Time{}, false, false
		}
		inner, _ := e.Args["this"].(*Expression)
		if !isStringLiteral(inner) {
			return time.Time{}, false, false
		}
		t, ok = parseDateText(inner.Name())
		return t, isDatetime, ok
	case "TsOrDsToDate":
		if format, _ := e.Args["format"].(*Expression); format != nil {
			return time.Time{}, false, false
		}
		inner, _ := e.Args["this"].(*Expression)
		if !isStringLiteral(inner) {
			return time.Time{}, false, false
		}
		t, ok = parseDateText(inner.Name())
		return t, false, ok
	}
	return time.Time{}, false, false
}

// parseDateText reads either layout this port's date/datetime literals use,
// always as UTC: there is no zone in the text, so there is nothing else to
// read it as.
func parseDateText(text string) (time.Time, bool) {
	if t, err := time.Parse(dateTimeLayout, text); err == nil {
		return t, true
	}
	if t, err := time.Parse(dateLayout, text); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// dateValueLiteral writes a date value back the way date_literal does: a
// CAST of a string onto DATE or DATETIME, the string formatted with or
// without a time-of-day component to match.
func dateValueLiteral(t time.Time, isDatetime bool) *Expression {
	layout, typeName := dateLayout, "DATE"
	if isDatetime {
		layout, typeName = dateTimeLayout, "DATETIME"
	}
	to := New("DataType", Arg{"this", DataTypeKind(typeName)}, Arg{"nested", false})
	cast := New("Cast",
		Arg{"this", New("Literal", Arg{"this", t.Format(layout)}, Arg{"is_string", true})},
		Arg{"to", to})
	// The parser sets a Cast's own Type to the type it casts TO, as a
	// shortcut -- a hand-built Cast that skips it is a tree the parser
	// would never actually produce, which is exactly what the "rewrite
	// must survive being written down" check exists to catch.
	cast.Type = to
	return cast
}

// intervalUnits maps an INTERVAL/DATE_ADD unit spelling to the number of
// calendar months it adds (for the three that are not a fixed duration) or
// signals that it is a fixed duration instead, ported from the reference's
// own `interval()` table. A unit outside this set -- `INTERVAL '90' FOO` --
// is left alone entirely, the same as the reference leaving an
// UnsupportedUnit unfolded.
var monthUnits = map[string]int{"YEAR": 12, "QUARTER": 3, "MONTH": 1}

// durationUnits maps a fixed-length unit to its time.Duration multiplier.
var durationUnits = map[string]time.Duration{
	"WEEK": 7 * 24 * time.Hour, "DAY": 24 * time.Hour,
	"HOUR": time.Hour, "MINUTE": time.Minute, "SECOND": time.Second,
}

// addCalendarInterval adds n of unit to t the way the reference's
// dateutil.relativedelta does: YEAR, QUARTER and MONTH walk calendar months
// and CLAMP the day to the last one the resulting month actually has --
// January 31st plus one month is February 28th, not March 3rd, which is
// what a fixed-duration add over the same span would give. Every other unit
// here is an exact duration with no such ambiguity.
func addCalendarInterval(t time.Time, unit string, n int) (time.Time, bool) {
	if perMonth, ok := monthUnits[unit]; ok {
		return addMonthsClamped(t, n*perMonth), true
	}
	if d, ok := durationUnits[unit]; ok {
		return t.Add(time.Duration(n) * d), true
	}
	return time.Time{}, false
}

// addMonthsClamped is time.Time.AddDate's month arithmetic without its own
// rollover: Go normalises Jan 31 + 1 month into Mar 3, treating the 31st as
// a day count rather than a date, where dateutil.relativedelta -- and the
// reference behind it -- clamps to Feb 28. Rebuilding the date from
// year/month/day after clamping is what makes it match.
func addMonthsClamped(t time.Time, months int) time.Time {
	year, month, day := t.Date()
	totalMonths := int(month) - 1 + months
	newYear := year + totalMonths/12
	newMonth := totalMonths % 12
	if newMonth < 0 {
		newMonth += 12
		newYear--
	}
	if last := daysInMonth(newYear, time.Month(newMonth+1)); day > last {
		day = last
	}
	return time.Date(newYear, time.Month(newMonth+1), day,
		t.Hour(), t.Minute(), t.Second(), 0, time.UTC)
}

// daysInMonth is the day-zero-of-next-month trick: the last day of `month`
// is one day before the first day of the month after it.
func daysInMonth(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// intervalAmount reads an INTERVAL's own count, or a DATE_ADD-family
// function's amount argument -- both are a plain integer, possibly Neg'd,
// by the time this runs: whatever expression it was written as has already
// folded on the way here, bottom-up, the same as every other operand this
// port folds.
func intervalAmount(e *Expression) (int, bool) {
	neg := false
	if e.Class == "Neg" {
		neg = true
		e, _ = e.Args["this"].(*Expression)
	}
	if e == nil || !isIntegerLiteral(e) {
		return 0, false
	}
	text, _ := e.Args["this"].(string)
	n, err := strconv.Atoi(text)
	if err != nil {
		return 0, false
	}
	if neg {
		n = -n
	}
	return n, true
}

// intervalOf reads an exp.Interval node's own unit and amount.
func intervalOf(e *Expression) (unit string, amount int, ok bool) {
	if e.Class != "Interval" {
		return "", 0, false
	}
	this, _ := e.Args["this"].(*Expression)
	unitArg, _ := e.Args["unit"].(*Expression)
	if this == nil || unitArg == nil {
		return "", 0, false
	}
	amount, ok = intervalAmount(this)
	if !ok {
		return "", 0, false
	}
	return strings.ToUpper(unitArg.Name()), amount, true
}

// dateAddFamilyOf reads a DATE_ADD/DATE_SUB/DATETIME_ADD/DATETIME_SUB call's
// own date, unit and amount -- the same three things an Interval carries,
// laid out as a function's arguments instead. sub says whether the class
// itself subtracts, the way DateSub and DatetimeSub do.
func dateAddFamilyOf(e *Expression) (date *Expression, unit string, amount int, sub bool, ok bool) {
	switch e.Class {
	case "DateAdd", "DatetimeAdd":
	case "DateSub", "DatetimeSub":
		sub = true
	default:
		return nil, "", 0, false, false
	}
	date, _ = e.Args["this"].(*Expression)
	expr, _ := e.Args["expression"].(*Expression)
	unitArg, _ := e.Args["unit"].(*Expression)
	if date == nil || expr == nil || unitArg == nil {
		return nil, "", 0, false, false
	}
	amount, ok = intervalAmount(expr)
	if !ok {
		return nil, "", 0, false, false
	}
	return date, strings.ToUpper(unitArg.Name()), amount, sub, true
}

// foldDateArithmetic is where a date/datetime literal actually moves: called
// for Add, Sub, DateAdd, DateSub, DatetimeAdd and DatetimeSub once the
// generic operand folds above it have already run. It returns nil, not an
// error -- same as every other fold in this file -- whenever the shape or
// the unit is not one it can move, which leaves the node exactly as written
// rather than guess.
func foldDateArithmetic(e *Expression) *Expression {
	switch e.Class {
	case "Add", "Sub":
		return foldDateIntervalBinary(e)
	case "DateAdd", "DateSub", "DatetimeAdd", "DatetimeSub":
		return foldDateAddFamily(e)
	}
	return nil
}

// foldDateIntervalBinary handles a date literal on one side of Add/Sub and
// an INTERVAL on the other -- `date '...' - interval '90' day`. Both
// orderings are the reference's: an interval MAY lead an Add (commutative),
// but never a Sub (there is no such thing as subtracting a date from a
// span).
func foldDateIntervalBinary(e *Expression) *Expression {
	left, _ := e.Args["this"].(*Expression)
	right, _ := e.Args["expression"].(*Expression)
	if left == nil || right == nil {
		return nil
	}
	if date, isDatetime, ok := extractDateValue(left); ok {
		if unit, amount, ok := intervalOf(right); ok {
			if e.Class == "Sub" {
				amount = -amount
			}
			return foldDateResult(date, isDatetime, unit, amount)
		}
		return nil
	}
	if e.Class == "Add" {
		if date, isDatetime, ok := extractDateValue(right); ok {
			if unit, amount, ok := intervalOf(left); ok {
				return foldDateResult(date, isDatetime, unit, amount)
			}
		}
	}
	return nil
}

// foldDateAddFamily handles the four DATE_ADD-shaped function calls, each
// carrying its own date, unit and amount as arguments rather than an
// Interval operand.
func foldDateAddFamily(e *Expression) *Expression {
	date, unit, amount, sub, ok := dateAddFamilyOf(e)
	if !ok {
		return nil
	}
	dateVal, isDatetime, ok := extractDateValue(date)
	if !ok {
		return nil
	}
	if sub {
		amount = -amount
	}
	return foldDateResult(dateVal, isDatetime, unit, amount)
}

// foldDateResult applies one calendar step and writes the result back, or
// declines -- a nil here is what an unsupported unit like FOO looks like
// all the way out to the statement that never folds it.
func foldDateResult(date time.Time, isDatetime bool, unit string, amount int) *Expression {
	result, ok := addCalendarInterval(date, unit, amount)
	if !ok {
		return nil
	}
	return dateValueLiteral(result, isDatetime)
}

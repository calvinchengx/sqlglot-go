package sqlglot

import (
	"strings"
	"time"
)

// truncUnits are the granularities this port's DATE_TRUNC folding reads --
// date-level only, which is everything the fixture asks for. An unrecognised
// unit, or one this does not yet floor (an hour, say), is left alone rather
// than guessed at.
var truncUnits = map[string]bool{
	"YEAR": true, "QUARTER": true, "MONTH": true, "WEEK": true, "DAY": true,
}

// truncClasses are the two node classes a TRUNC call over a date can be:
// DATE_TRUNC (this, unit) and TIMESTAMP_TRUNC/T-SQL's DATETRUNC (this, unit)
// -- the argument order differs by name but the port's own parser already
// normalises both into "this"/"unit" regardless of which one was written.
var truncClasses = map[string]bool{"DateTrunc": true, "TimestampTrunc": true}

// dateTruncOf reads a DATE_TRUNC/TIMESTAMP_TRUNC call's own date expression
// and unit, declining for any unit this port does not floor.
func dateTruncOf(e *Expression) (date *Expression, unit string, ok bool) {
	if !truncClasses[e.Class] {
		return nil, "", false
	}
	date, _ = e.Args["this"].(*Expression)
	unitArg, _ := e.Args["unit"].(*Expression)
	if date == nil || unitArg == nil {
		return nil, "", false
	}
	unit = strings.ToUpper(unitArg.Name())
	if !truncUnits[unit] {
		return nil, "", false
	}
	return date, unit, true
}

// weekStartsMonday is the reference's own WEEK_OFFSET, read for the one
// dialect among this port's five that differs: T-SQL (and BigQuery, which
// this port does not speak) starts its week on Sunday; every other dialect
// here defaults to Monday.
func weekStartsMonday(dialect string) bool {
	return dialect != "tsql"
}

// dateTruncFloor rounds t DOWN to the start of the unit it belongs to --
// the reference's datetime_floor, scoped to the date-level units this port
// reads. WEEK is the one that needs the dialect: which day a week starts on
// decides which Monday-or-Sunday it floors to.
func dateTruncFloor(t time.Time, unit string, dialect string) (time.Time, bool) {
	switch unit {
	case "YEAR":
		return time.Date(t.Year(), 1, 1, 0, 0, 0, 0, time.UTC), true
	case "QUARTER":
		month := time.Month(((int(t.Month())-1)/3)*3 + 1)
		return time.Date(t.Year(), month, 1, 0, 0, 0, 0, time.UTC), true
	case "MONTH":
		return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC), true
	case "WEEK":
		// Go's Weekday is Sunday=0..Saturday=6; Python's (and the
		// reference's) is Monday=0..Sunday=6, which every day-count below
		// is written against.
		mondayZero := (int(t.Weekday()) + 6) % 7
		offset := 0
		if !weekStartsMonday(dialect) {
			offset = -1
		}
		back := ((mondayZero-offset)%7 + 7) % 7
		return dayOnly(t).AddDate(0, 0, -back), true
	case "DAY":
		return dayOnly(t), true
	}
	return time.Time{}, false
}

// dayOnly zeroes a time.Time's own time-of-day, the way every date-level
// floor needs to whether or not its own arithmetic touches the clock.
func dayOnly(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// dateTruncCeil is the reference's date_ceil: t itself when it already sits
// exactly on a floor, else the NEXT one.
func dateTruncCeil(t time.Time, unit string, dialect string) (time.Time, bool) {
	floor, ok := dateTruncFloor(t, unit, dialect)
	if !ok {
		return time.Time{}, false
	}
	if floor.Equal(t) {
		return t, true
	}
	return addCalendarInterval(floor, unit, 1)
}

// dateTruncNextFloor is floor(t)+1 unit UNCONDITIONALLY -- the boundary GT
// and LTE both compare against, whether or not t itself sits on one. It
// differs from dateTruncCeil only when t is already a floor: ceil returns t
// itself there, this always steps one unit past it.
func dateTruncNextFloor(t time.Time, unit string, dialect string) (time.Time, bool) {
	floor, ok := dateTruncFloor(t, unit, dialect)
	if !ok {
		return time.Time{}, false
	}
	return addCalendarInterval(floor, unit, 1)
}

// dateRange is [low, high) -- the half-open span every value DATE_TRUNC'd to
// `date` falls into.
type dateRange struct{ low, high time.Time }

// dateTruncRange is the reference's _datetrunc_range: the span DATE_TRUNC(x)
// covers when it equals date, or ok=false when date is not itself a floor --
// nothing can ever equal a truncation that lands elsewhere, but the
// reference declines to say so rather than fold to FALSE, and this port
// matches that rather than going one step further on its own.
func dateTruncRange(date time.Time, unit string, dialect string) (dateRange, bool) {
	floor, ok := dateTruncFloor(date, unit, dialect)
	if !ok || !floor.Equal(date) {
		return dateRange{}, false
	}
	high, ok := addCalendarInterval(floor, unit, 1)
	if !ok {
		return dateRange{}, false
	}
	return dateRange{low: floor, high: high}, true
}

// foldDateTruncLiteral is the direct fold -- DATE_TRUNC(unit, <a date
// literal>) becomes the truncated date, the same value the call would
// return, spelled as a literal of its own operand's type. A DATE_TRUNC over
// a COLUMN has no literal to read and is left for foldDateTruncComparison
// to see once its own parent asks a comparison of it.
func foldDateTruncLiteral(e *Expression, dialect string) *Expression {
	date, unit, ok := dateTruncOf(e)
	if !ok {
		return nil
	}
	dateVal, typeName, ok := extractDateValue(date)
	if !ok {
		return nil
	}
	floor, ok := dateTruncFloor(dateVal, unit, dialect)
	if !ok {
		return nil
	}
	return dateValueLiteral(floor, typeName)
}

// datetruncComparisons are the operators the reference rewrites a DATE_TRUNC
// comparison into a range check for -- LT/GT/LTE/GTE/EQ/NEQ, ported from its
// own DATETRUNC_BINARY_COMPARISONS table.
var datetruncComparisons = map[string]bool{
	"LT": true, "GT": true, "LTE": true, "GTE": true, "EQ": true, "NEQ": true,
}

// foldDateTruncComparison rewrites `DATE_TRUNC(unit, l) OP <date literal>`
// into a range condition over l directly -- the whole point being that l
// itself never has to be truncated to answer the comparison, which is what
// lets an index on l stay useful. Only the left operand is ever read as the
// DATE_TRUNC: the reference's own sort_comparison (this port's
// sortComparison) is what puts it there when a statement writes the literal
// first, over however many passes that takes.
func foldDateTruncComparison(e, parent *Expression, dialect string) *Expression {
	if !datetruncComparisons[e.Class] {
		return nil
	}
	left, _ := e.Args["this"].(*Expression)
	right, _ := e.Args["expression"].(*Expression)
	if left == nil || right == nil {
		return nil
	}
	truncArg, unit, ok := dateTruncOf(left)
	if !ok {
		return nil
	}
	date, typeName, ok := extractDateValue(right)
	if !ok {
		return nil
	}
	switch e.Class {
	case "LT":
		bound, ok := dateTruncCeil(date, unit, dialect)
		if !ok {
			return nil
		}
		return New("LT", Arg{"this", truncArg}, Arg{"expression", dateValueLiteral(bound, typeName)})
	case "GT":
		bound, ok := dateTruncNextFloor(date, unit, dialect)
		if !ok {
			return nil
		}
		return New("GTE", Arg{"this", truncArg}, Arg{"expression", dateValueLiteral(bound, typeName)})
	case "LTE":
		bound, ok := dateTruncNextFloor(date, unit, dialect)
		if !ok {
			return nil
		}
		return New("LT", Arg{"this", truncArg}, Arg{"expression", dateValueLiteral(bound, typeName)})
	case "GTE":
		bound, ok := dateTruncCeil(date, unit, dialect)
		if !ok {
			return nil
		}
		return New("GTE", Arg{"this", truncArg}, Arg{"expression", dateValueLiteral(bound, typeName)})
	case "EQ":
		drange, ok := dateTruncRange(date, unit, dialect)
		if !ok {
			return nil
		}
		return parenthesizeNestedConnector(dateTruncRangeAnd(truncArg, drange, typeName), parent)
	case "NEQ":
		drange, ok := dateTruncRange(date, unit, dialect)
		if !ok {
			return nil
		}
		result := New("Or",
			Arg{"this", New("LT", Arg{"this", copyExpr(truncArg)}, Arg{"expression", dateValueLiteral(drange.low, typeName)})},
			Arg{"expression", New("GTE", Arg{"this", truncArg}, Arg{"expression", dateValueLiteral(drange.high, typeName)})})
		return parenthesizeNestedConnector(result, parent)
	}
	return nil
}

// dateTruncRangeAnd builds `l >= low AND l < high`, the reference's own
// _datetrunc_eq_expression, shared by EQ and by the IN-list fold below.
func dateTruncRangeAnd(l *Expression, r dateRange, typeName string) *Expression {
	return New("And",
		Arg{"this", New("GTE", Arg{"this", copyExpr(l)}, Arg{"expression", dateValueLiteral(r.low, typeName)})},
		Arg{"expression", New("LT", Arg{"this", l}, Arg{"expression", dateValueLiteral(r.high, typeName)})})
}

// copyExpr is a shallow structural copy: l is used TWICE in a range's own
// AND (once per bound), and the tree only shares scalars, never a node,
// the same rule every other builder in this port follows.
func copyExpr(e *Expression) *Expression {
	if e == nil {
		return nil
	}
	return e.Copy()
}

// foldDateTruncIn handles `DATE_TRUNC(unit, l) IN (<date literal>, ...)`: it
// declines the WHOLE fold only if any member is not itself a date literal at
// all -- the reference's own `_is_datetrunc_predicate` check across the
// whole list. A member that IS a date literal but off the unit's own floor
// contributes no range and is simply dropped, the same as a bare EQ against
// it would decline entirely on its own; it is not a reason to give up on
// whatever OTHER members are exactly on a floor. The surviving ranges are
// then merged the way two adjacent or overlapping years collapse into one
// wider AND, and ORed together.
func foldDateTruncIn(e, parent *Expression, dialect string) *Expression {
	if e.Class != "In" {
		return nil
	}
	left, _ := e.Args["this"].(*Expression)
	if left == nil {
		return nil
	}
	truncArg, unit, ok := dateTruncOf(left)
	if !ok {
		return nil
	}
	members, _ := e.Args["expressions"].([]*Expression)
	if len(members) == 0 {
		return nil
	}
	ranges := make([]dateRange, 0, len(members))
	typeName := ""
	for i, m := range members {
		date, dt, ok := extractDateValue(m)
		if !ok {
			// Every member has to be a genuine date literal for the fold to
			// mean anything at all -- this is the reference's own
			// `_is_datetrunc_predicate` check across the whole list.
			return nil
		}
		if i == 0 {
			typeName = dt
		}
		// A member that ISN'T exactly on a floor contributes no range of its
		// own and is silently dropped, the same as a bare EQ against it
		// would decline entirely -- it is not a reason to give up on the
		// OTHER members that are.
		if r, ok := dateTruncRange(date, unit, dialect); ok {
			ranges = append(ranges, r)
		}
	}
	if len(ranges) == 0 {
		return nil
	}
	merged := mergeDateRanges(ranges)
	if len(merged) == 1 {
		// One surviving range needs no OR at all, and so nothing of its own
		// AND's parentheses either -- the same shape a plain EQ fold gives.
		return parenthesizeNestedConnector(dateTruncRangeAnd(truncArg, merged[0], typeName), parent)
	}
	var result *Expression
	for _, r := range merged {
		// Each branch is its own AND, and AND already binds tighter than
		// the OR joining the branches -- but the reference wraps each one
		// in a Paren anyway, and this port matches that spelling exactly
		// rather than relying on precedence alone to say the same thing.
		cond := New("Paren", Arg{"this", dateTruncRangeAnd(truncArg, r, typeName)})
		if result == nil {
			result = cond
		} else {
			result = New("Or", Arg{"this", result}, Arg{"expression", cond})
		}
	}
	return parenthesizeNestedConnector(result, parent)
}

// mergeDateRanges is the reference's merge_ranges: sort by start, then fold
// any range into the one before it when the two overlap or touch exactly at
// the boundary -- [low, high) ranges are already half-open, so `high ==
// next.low` is adjacency, not a gap.
func mergeDateRanges(ranges []dateRange) []dateRange {
	if len(ranges) == 0 {
		return nil
	}
	sorted := append([]dateRange(nil), ranges...)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j].low.Before(sorted[j-1].low); j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	merged := []dateRange{sorted[0]}
	for _, r := range sorted[1:] {
		last := &merged[len(merged)-1]
		if !r.low.After(last.high) {
			if r.high.After(last.high) {
				last.high = r.high
			}
			continue
		}
		merged = append(merged, r)
	}
	return merged
}

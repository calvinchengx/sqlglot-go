package sqlglot

import (
	"testing"
	"time"
)

// TestDateArithmeticEndToEnd pins the SQL shapes the fixture actually needs,
// each verified against the pinned reference directly.
func TestDateArithmeticEndToEnd(t *testing.T) {
	for _, tc := range []struct{ name, sql, want string }{
		{"date literal minus a day interval",
			"SELECT date '1998-12-01' - interval '90' day", "SELECT CAST('1998-09-02' AS DATE)"},
		{"date literal plus a week interval",
			"SELECT date '1998-12-01' + interval '1' week", "SELECT CAST('1998-12-08' AS DATE)"},
		{"an interval leading an Add is commutative",
			"SELECT interval '1' year + date '1998-01-01'", "SELECT CAST('1999-01-01' AS DATE)"},
		{"a folded date stays a plain value the rest of the chain does not touch",
			"SELECT interval '1' year + date '1998-01-01' + 3 * 7 * 4",
			"SELECT CAST('1999-01-01' AS DATE) + 84"},
		{"a month interval over a DATETIME cast keeps its own time component",
			"SELECT CAST('2008-11-11' AS DATETIME) + INTERVAL '5' MONTH",
			"SELECT CAST('2009-04-11 00:00:00' AS DATETIME)"},
		{"datetime literal minus a day interval",
			"SELECT datetime '1998-12-01' - interval '90' day",
			"SELECT CAST('1998-09-02 00:00:00' AS DATETIME)"},
		{"TS_OR_DS_TO_DATE truncates its own time component",
			"SELECT TS_OR_DS_TO_DATE('1998-12-01 00:00:01') - interval '90' day",
			"SELECT CAST('1998-09-02' AS DATE)"},
		{"DATE_ADD with a negative month amount",
			"SELECT DATE_ADD(CAST('2023-01-02' AS DATE), -2, 'MONTH')",
			"SELECT CAST('2022-11-02' AS DATE)"},
		{"DATE_SUB with an amount that itself needed folding first",
			"SELECT DATE_SUB(CAST('2023-01-02' AS DATE), 1 + 1, 'DAY')",
			"SELECT CAST('2022-12-31' AS DATE)"},
		{"DATE_ADD by hours over a DATETIME cast",
			"SELECT DATE_ADD(CAST('2023-01-02' AS DATETIME), -2, 'HOUR')",
			"SELECT CAST('2023-01-01 22:00:00' AS DATETIME)"},
		{"DATETIME_ADD is DATE_ADD's own name for the same shape",
			"SELECT DATETIME_ADD(CAST('2023-01-02' AS DATETIME), -2, 'HOUR')",
			"SELECT CAST('2023-01-01 22:00:00' AS DATETIME)"},
		{"DATETIME_SUB with a folded amount",
			"SELECT DATETIME_SUB(CAST('2023-01-02' AS DATETIME), 1 + 1, 'HOUR')",
			"SELECT CAST('2023-01-01 22:00:00' AS DATETIME)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, "")
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			got, err := Generate(Simplify(e, ""), "")
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("Simplify(%q)\n  want %s\n  got  %s", tc.sql, tc.want, got)
			}
			// The rewrite must survive being written down -- read back into
			// a tree the SAME shape a fresh parse would give, which is what
			// the harness differential's sameMeaning() checks end to end
			// for the whole fixture; here it is enough that the fold's own
			// output reads back at all, with no format/safe/action/default
			// left off a Cast the way a hand-built one first was.
			if _, err := ParseOne(got, ""); err != nil {
				t.Fatalf("the fold's own output %q does not parse back: %v", got, err)
			}
		})
	}
}

// TestDateArithmeticDeclines covers every shape the fold must leave alone:
// an unknown unit, a non-literal interval amount, and a column standing
// where only a date literal folds.
func TestDateArithmeticDeclines(t *testing.T) {
	for _, sql := range []string{
		"SELECT date '1998-12-01' - interval x day",
		"SELECT date '1998-12-01' - interval '90' foo",
		"SELECT date '1998-12-01' + interval '90' foo",
		"SELECT CAST(x AS DATE) + interval '1' week",
		"SELECT CAST(x AS DATETIME) + interval '1' WEEK",
	} {
		t.Run(sql, func(t *testing.T) {
			e, err := ParseOne(sql, "")
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", sql, err)
			}
			got, err := Generate(Simplify(e, ""), "")
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			want, _ := Generate(e, "")
			if got != want {
				t.Errorf("Simplify(%q) folded to %q; it should have stayed %q", sql, got, want)
			}
		})
	}
}

// TestAddCalendarInterval pins the reference's own dateutil.relativedelta
// table -- YEAR, QUARTER and MONTH clamp the day to the last one the
// resulting month actually has, where WEEK/DAY/HOUR/MINUTE/SECOND are exact
// durations with nothing to clamp. Every case here was read out of
// dateutil.relativedelta directly, not hand-computed.
func TestAddCalendarInterval(t *testing.T) {
	date := func(y int, m time.Month, d int) time.Time {
		return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	}
	for _, tc := range []struct {
		name string
		t    time.Time
		unit string
		n    int
		want time.Time
	}{
		{"Jan 31 plus a month clamps to Feb 28", date(2023, 1, 31), "MONTH", 1, date(2023, 2, 28)},
		{"Jan 31 minus a month does not need to clamp", date(2023, 1, 31), "MONTH", -1, date(2022, 12, 31)},
		{"a leap day plus a year clamps to Feb 28", date(2020, 2, 29), "YEAR", 1, date(2021, 2, 28)},
		{"Feb 28 minus a year lands on a leap day's non-leap neighbour", date(2021, 2, 28), "YEAR", -1, date(2020, 2, 28)},
		{"Mar 31 plus a month clamps to Apr 30", date(2023, 3, 31), "MONTH", 1, date(2023, 4, 30)},
		{"Dec 31 plus a month rolls the year over", date(2023, 12, 31), "MONTH", 1, date(2024, 1, 31)},
		{"three months forward", date(2023, 1, 1), "MONTH", 3, date(2023, 4, 1)},
		{"a quarter is three months", date(2023, 1, 1), "QUARTER", 1, date(2023, 4, 1)},
		{"a plain year forward", date(2023, 1, 1), "YEAR", 1, date(2024, 1, 1)},
		{"two months back across a year boundary", date(2023, 1, 2), "MONTH", -2, date(2022, 11, 2)},
		{"a week is an exact seven days", date(2021, 1, 8), "WEEK", -1, date(2021, 1, 1)},
		{"a day is exact, no clamping question at all", date(2023, 1, 2), "DAY", 2, date(2023, 1, 4)},
		{"a negative day", date(2023, 1, 2), "DAY", -1, date(2023, 1, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := addCalendarInterval(tc.t, tc.unit, tc.n)
			if !ok {
				t.Fatalf("addCalendarInterval(%v, %s, %d) declined", tc.t, tc.unit, tc.n)
			}
			if !got.Equal(tc.want) {
				t.Errorf("addCalendarInterval(%v, %s, %d) = %v, want %v", tc.t, tc.unit, tc.n, got, tc.want)
			}
		})
	}
	// An hour, a minute and a second are exact durations too, and an
	// unknown unit is refused rather than guessed at.
	base := time.Date(2023, 1, 2, 10, 0, 0, 0, time.UTC)
	if got, ok := addCalendarInterval(base, "HOUR", -2); !ok || !got.Equal(time.Date(2023, 1, 2, 8, 0, 0, 0, time.UTC)) {
		t.Errorf("addCalendarInterval(HOUR, -2) = %v (ok=%v)", got, ok)
	}
	if got, ok := addCalendarInterval(base, "MINUTE", 90); !ok || !got.Equal(time.Date(2023, 1, 2, 11, 30, 0, 0, time.UTC)) {
		t.Errorf("addCalendarInterval(MINUTE, 90) = %v (ok=%v)", got, ok)
	}
	if got, ok := addCalendarInterval(base, "SECOND", 30); !ok || !got.Equal(time.Date(2023, 1, 2, 10, 0, 30, 0, time.UTC)) {
		t.Errorf("addCalendarInterval(SECOND, 30) = %v (ok=%v)", got, ok)
	}
	if _, ok := addCalendarInterval(base, "FOO", 1); ok {
		t.Error("addCalendarInterval with an unknown unit should be refused")
	}
}

// TestExtractDateValue covers every shape it reads and every one it
// declines: a Cast to an unsupported type, a Cast over something other than
// a string literal, a TS_OR_DS_TO_DATE with a format (not foldable here),
// and a class it has no business reading at all.
func TestExtractDateValue(t *testing.T) {
	col := New("Column", Arg{"this", New("Identifier", Arg{"this", "x"}, Arg{"quoted", false})})

	if _, _, ok := extractDateValue(col); ok {
		t.Error("extractDateValue(a bare column) should be refused")
	}

	castOfColumn := New("Cast", Arg{"this", col},
		Arg{"to", New("DataType", Arg{"this", DataTypeKind("DATE")}, Arg{"nested", false})})
	if _, _, ok := extractDateValue(castOfColumn); ok {
		t.Error("extractDateValue(CAST(column AS DATE)) should be refused; nothing to read")
	}

	castToInt := New("Cast",
		Arg{"this", New("Literal", Arg{"this", "5"}, Arg{"is_string", true})},
		Arg{"to", New("DataType", Arg{"this", DataTypeKind("INT")}, Arg{"nested", false})})
	if _, _, ok := extractDateValue(castToInt); ok {
		t.Error("extractDateValue(CAST(x AS INT)) should be refused; not a temporal type")
	}

	badText := New("Cast",
		Arg{"this", New("Literal", Arg{"this", "not a date"}, Arg{"is_string", true})},
		Arg{"to", New("DataType", Arg{"this", DataTypeKind("DATE")}, Arg{"nested", false})})
	if _, _, ok := extractDateValue(badText); ok {
		t.Error("extractDateValue(CAST('not a date' AS DATE)) should be refused")
	}

	tsWithFormat := New("TsOrDsToDate",
		Arg{"this", New("Literal", Arg{"this", "1998-12-01"}, Arg{"is_string", true})},
		Arg{"format", New("Literal", Arg{"this", "%Y"}, Arg{"is_string", true})})
	if _, _, ok := extractDateValue(tsWithFormat); ok {
		t.Error("extractDateValue(TS_OR_DS_TO_DATE with a format) should be refused")
	}

	tsOfColumn := New("TsOrDsToDate", Arg{"this", col})
	if _, _, ok := extractDateValue(tsOfColumn); ok {
		t.Error("extractDateValue(TS_OR_DS_TO_DATE(column)) should be refused")
	}
}

// TestIntervalAmountAndIntervalOf covers the small readers foldDateArithmetic
// leans on: a Neg'd amount, a non-integer amount, and an Interval or
// DATE_ADD-family call missing a piece it needs.
func TestIntervalAmountAndIntervalOf(t *testing.T) {
	five := New("Literal", Arg{"this", "5"}, Arg{"is_string", false})
	if n, ok := intervalAmount(five); !ok || n != 5 {
		t.Errorf("intervalAmount(5) = %d (ok=%v), want 5", n, ok)
	}
	negFive := New("Neg", Arg{"this", five})
	if n, ok := intervalAmount(negFive); !ok || n != -5 {
		t.Errorf("intervalAmount(-5) = %d (ok=%v), want -5", n, ok)
	}
	notANumber := New("Literal", Arg{"this", "abc"}, Arg{"is_string", true})
	if _, ok := intervalAmount(notANumber); ok {
		t.Error("intervalAmount('abc') should be refused")
	}
	fractional := New("Literal", Arg{"this", "1.5"}, Arg{"is_string", false})
	if _, ok := intervalAmount(fractional); ok {
		t.Error("intervalAmount(1.5) should be refused; DATE_ADD's amount is an integer")
	}

	if _, _, ok := intervalOf(New("Column")); ok {
		t.Error("intervalOf(a non-Interval node) should be refused")
	}
	incomplete := New("Interval", Arg{"this", five})
	if _, _, ok := intervalOf(incomplete); ok {
		t.Error("intervalOf(Interval with no unit) should be refused")
	}

	if _, _, _, _, ok := dateAddFamilyOf(New("Column")); ok {
		t.Error("dateAddFamilyOf(a non-DateAdd-family node) should be refused")
	}
	incompleteAdd := New("DateAdd", Arg{"this", five})
	if _, _, _, _, ok := dateAddFamilyOf(incompleteAdd); ok {
		t.Error("dateAddFamilyOf(DateAdd with no expression/unit) should be refused")
	}
}

// TestFoldDateArithmeticIgnoresOtherClasses covers foldDateArithmetic's own
// dispatch: it is called for every Add/Sub in the tree, and has to return
// nil quietly for the ones that are not date arithmetic at all, rather than
// assume its callers pre-filter for it.
func TestFoldDateArithmeticIgnoresOtherClasses(t *testing.T) {
	if out := foldDateArithmetic(New("Mul")); out != nil {
		t.Errorf("foldDateArithmetic(Mul) = %v, want nil", out)
	}
	plainAdd := New("Add",
		Arg{"this", New("Literal", Arg{"this", "1"}, Arg{"is_string", false})},
		Arg{"expression", New("Literal", Arg{"this", "2"}, Arg{"is_string", false})})
	if out := foldDateArithmetic(plainAdd); out != nil {
		t.Errorf("foldDateArithmetic(1 + 2) = %v, want nil (not this fold's job)", out)
	}
	// Sub never reads an interval off its LEFT side the way Add can --
	// there is no such thing as an interval minus a date.
	intervalMinusDate := New("Sub",
		Arg{"this", New("Interval",
			Arg{"this", New("Literal", Arg{"this", "1"}, Arg{"is_string", false})},
			Arg{"unit", New("Var", Arg{"this", "DAY"})})},
		Arg{"expression", dateValueLiteral(time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC), "DATE")})
	if out := foldDateArithmetic(intervalMinusDate); out != nil {
		t.Errorf("foldDateArithmetic(interval - date) = %v, want nil", out)
	}
}

// TestDateAddFamilyDeclinesNonLiteralAmount covers dateAddFamilyOf's own
// amount-reading failure directly, at the tree rather than through
// Generate: DATE_ADD has no dedicated generator writer registered for this
// shape yet (a separate, pre-existing gap, unrelated to whether the fold
// itself is right), so asserting through round-tripped SQL would fail for
// the wrong reason.
func TestDateAddFamilyDeclinesNonLiteralAmount(t *testing.T) {
	sql := "SELECT DATE_ADD(CAST('2023-01-02' AS DATE), x, 'DAY')"
	e, err := ParseOne(sql, "")
	if err != nil {
		t.Fatalf("ParseOne(%q): %v", sql, err)
	}
	simplified := Simplify(e, "")
	dateAdds := simplified.FindAll("DateAdd")
	if len(dateAdds) != 1 {
		t.Fatalf("Simplify(%q) folded away the DateAdd it should have left alone: %d remain", sql, len(dateAdds))
	}
}

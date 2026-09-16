package sqlglot

import (
	"testing"
	"time"
)

// TestDateTruncEndToEnd pins the SQL shapes the fixture actually needs, each
// verified against the pinned reference directly.
func TestDateTruncEndToEnd(t *testing.T) {
	for _, tc := range []struct{ name, sql, dialect, want string }{
		{"a direct fold to the truncated date", "SELECT DATE_TRUNC('week', CAST('2023-12-15' AS DATE))", "",
			"SELECT CAST('2023-12-11' AS DATE)"},
		{"the same week, a different day in it", "SELECT DATE_TRUNC('week', CAST('2023-12-16' AS DATE))", "",
			"SELECT CAST('2023-12-11' AS DATE)"},
		{"a date already on the floor stays itself", "SELECT DATE_TRUNC('week', CAST('2023-12-11' AS DATE))", "",
			"SELECT CAST('2023-12-11' AS DATE)"},
		{"GT rewrites to the next floor, unconditionally",
			"SELECT x WHERE DATE_TRUNC('week', x) > CAST('2023-12-11' AS DATE)", "",
			"SELECT x WHERE x >= CAST('2023-12-18' AS DATE)"},
		{"EQ rewrites to a half-open range",
			"SELECT x WHERE DATE_TRUNC('week', x) = CAST('2023-12-11' AS DATE)", "",
			"SELECT x WHERE x < CAST('2023-12-18' AS DATE) AND x >= CAST('2023-12-11' AS DATE)"},
		{"T-SQL's DATETRUNC is TimestampTrunc, and starts its week on Sunday",
			"SELECT DATETRUNC(WEEK, CAST('2021-12-08' AS DATETIME2))", "tsql",
			"SELECT CAST('2021-12-05 00:00:00' AS DATETIME2)"},
		{"a Sunday is already T-SQL's own week floor",
			"SELECT DATETRUNC(WEEK, CAST('2023-12-10' AS DATE))", "tsql",
			"SELECT CAST('2023-12-10' AS DATE)"},
		{"T-SQL's week comparison uses its own floor",
			"SELECT * WHERE DATETRUNC(WEEK, x) = CAST('2023-12-10' AS DATE)", "tsql",
			"SELECT * WHERE x < CAST('2023-12-17' AS DATE) AND x >= CAST('2023-12-10' AS DATE)"},
		{"and the same for GT", "SELECT * WHERE DATETRUNC(WEEK, x) > CAST('2023-12-10' AS DATE)", "tsql",
			"SELECT * WHERE x >= CAST('2023-12-17' AS DATE)"},
		{"YEAR", "SELECT * WHERE DATE_TRUNC('year', x) = CAST('2021-01-01' AS DATE)", "",
			"SELECT * WHERE x < CAST('2022-01-01' AS DATE) AND x >= CAST('2021-01-01' AS DATE)"},
		{"QUARTER", "SELECT * WHERE DATE_TRUNC('quarter', x) = CAST('2021-01-01' AS DATE)", "",
			"SELECT * WHERE x < CAST('2021-04-01' AS DATE) AND x >= CAST('2021-01-01' AS DATE)"},
		{"MONTH", "SELECT * WHERE DATE_TRUNC('month', x) = CAST('2021-01-01' AS DATE)", "",
			"SELECT * WHERE x < CAST('2021-02-01' AS DATE) AND x >= CAST('2021-01-01' AS DATE)"},
		{"WEEK on a Monday-start dialect", "SELECT * WHERE DATE_TRUNC('week', x) = CAST('2021-01-04' AS DATE)", "",
			"SELECT * WHERE x < CAST('2021-01-11' AS DATE) AND x >= CAST('2021-01-04' AS DATE)"},
		{"DAY", "SELECT * WHERE DATE_TRUNC('day', x) = CAST('2021-01-01' AS DATE)", "",
			"SELECT * WHERE x < CAST('2021-01-02' AS DATE) AND x >= CAST('2021-01-01' AS DATE)"},
		{"the literal written first still folds, once sortComparison reorders it",
			"SELECT * WHERE CAST('2021-01-01' AS DATE) = DATE_TRUNC('year', x)", "",
			"SELECT * WHERE x < CAST('2022-01-01' AS DATE) AND x >= CAST('2021-01-01' AS DATE)"},
		{"NEQ rewrites to an OR of two exclusions",
			"SELECT * WHERE DATE_TRUNC('year', x) <> CAST('2021-01-01' AS DATE)", "",
			"SELECT * WHERE x < CAST('2021-01-01' AS DATE) OR x >= CAST('2022-01-01' AS DATE)"},
		{"NEQ's own OR gets parenthesised once it joins an AND",
			"SELECT * WHERE DATE_TRUNC('year', x) <> CAST('2021-01-01' AS DATE) AND y = 1", "",
			"SELECT * WHERE (x < CAST('2021-01-01' AS DATE) OR x >= CAST('2022-01-01' AS DATE)) AND y = 1"},
		{"EQ's own AND does NOT need parentheses under an OR of the same-or-looser precedence",
			"SELECT * WHERE DATE_TRUNC('year', x) = CAST('2021-01-01' AS DATE) OR y = 1", "",
			"SELECT * WHERE (x < CAST('2022-01-01' AS DATE) AND x >= CAST('2021-01-01' AS DATE)) OR y = 1"},
		{"EQ's own AND, under NOT, is distributed by De Morgan into NEQ's own shape",
			"SELECT * WHERE NOT DATE_TRUNC('year', x) = CAST('2021-01-01' AS DATE)", "",
			"SELECT * WHERE x < CAST('2021-01-01' AS DATE) OR x >= CAST('2022-01-01' AS DATE)"},
		{"and NEQ's own OR the same way, into EQ's",
			"SELECT * WHERE NOT DATE_TRUNC('year', x) <> CAST('2021-01-01' AS DATE)", "",
			"SELECT * WHERE x < CAST('2022-01-01' AS DATE) AND x >= CAST('2021-01-01' AS DATE)"},
		{"LTE", "SELECT * WHERE DATE_TRUNC('year', x) <= CAST('2021-01-01' AS DATE)", "",
			"SELECT * WHERE x < CAST('2022-01-01' AS DATE)"},
		{"LTE off the floor reads the same next boundary",
			"SELECT * WHERE DATE_TRUNC('year', x) <= CAST('2021-01-02' AS DATE)", "",
			"SELECT * WHERE x < CAST('2022-01-01' AS DATE)"},
		{"GTE reordered from a literal written first",
			"SELECT * WHERE CAST('2021-01-01' AS DATE) >= DATE_TRUNC('year', x)", "",
			"SELECT * WHERE x < CAST('2022-01-01' AS DATE)"},
		{"LT on the floor itself", "SELECT * WHERE DATE_TRUNC('year', x) < CAST('2021-01-01' AS DATE)", "",
			"SELECT * WHERE x < CAST('2021-01-01' AS DATE)"},
		{"LT off the floor reads the CEILING, not the next floor",
			"SELECT * WHERE DATE_TRUNC('year', x) < CAST('2021-01-02' AS DATE)", "",
			"SELECT * WHERE x < CAST('2022-01-01' AS DATE)"},
		{"GTE on the floor itself", "SELECT * WHERE DATE_TRUNC('year', x) >= CAST('2021-01-01' AS DATE)", "",
			"SELECT * WHERE x >= CAST('2021-01-01' AS DATE)"},
		{"GTE off the floor reads the ceiling",
			"SELECT * WHERE DATE_TRUNC('year', x) >= CAST('2021-01-02' AS DATE)", "",
			"SELECT * WHERE x >= CAST('2022-01-01' AS DATE)"},
		{"GT on the floor itself", "SELECT * WHERE DATE_TRUNC('year', x) > CAST('2021-01-01' AS DATE)", "",
			"SELECT * WHERE x >= CAST('2022-01-01' AS DATE)"},
		{"GT off the floor reads the same next floor",
			"SELECT * WHERE DATE_TRUNC('year', x) > CAST('2021-01-02' AS DATE)", "",
			"SELECT * WHERE x >= CAST('2022-01-01' AS DATE)"},
		{"IN over two separate years ORs their ranges",
			"SELECT * WHERE DATE_TRUNC('year', x) IN (CAST('2021-01-01' AS DATE), CAST('2023-01-01' AS DATE))", "",
			"SELECT * WHERE (x < CAST('2022-01-01' AS DATE) AND x >= CAST('2021-01-01' AS DATE)) OR (x < CAST('2024-01-01' AS DATE) AND x >= CAST('2023-01-01' AS DATE))"},
		{"IN over two ADJACENT years merges into one range",
			"SELECT * WHERE DATE_TRUNC('year', x) IN (CAST('2021-01-01' AS DATE), CAST('2022-01-01' AS DATE))", "",
			"SELECT * WHERE x < CAST('2023-01-01' AS DATE) AND x >= CAST('2021-01-01' AS DATE)"},
		{"IN with a member off the floor drops just that member, not the whole fold",
			"SELECT * WHERE DATE_TRUNC('year', x) IN (CAST('2021-01-01' AS DATE), CAST('2022-01-02' AS DATE))", "",
			"SELECT * WHERE x < CAST('2022-01-01' AS DATE) AND x >= CAST('2021-01-01' AS DATE)"},
		{"IN's own OR is parenthesised joining an AND",
			"SELECT * WHERE DATE_TRUNC('year', x) IN (CAST('2021-01-01' AS DATE), CAST('2023-01-01' AS DATE)) AND y = 1", "",
			"SELECT * WHERE ((x < CAST('2022-01-01' AS DATE) AND x >= CAST('2021-01-01' AS DATE)) OR (x < CAST('2024-01-01' AS DATE) AND x >= CAST('2023-01-01' AS DATE))) AND y = 1"},
		// De Morgan pushes the NOT through the OR, then through each AND in
		// turn, landing on the same shape _datetrunc_neq builds directly --
		// each merged range's own OR-of-comparisons, ANDed together.
		{"a NOT wrapping an IN fold is distributed through it by De Morgan",
			"SELECT * WHERE NOT DATE_TRUNC('year', x) IN (CAST('2021-01-01' AS DATE), CAST('2023-01-01' AS DATE))", "",
			"SELECT * WHERE (x < CAST('2021-01-01' AS DATE) OR x >= CAST('2022-01-01' AS DATE)) AND (x < CAST('2023-01-01' AS DATE) OR x >= CAST('2024-01-01' AS DATE))"},
		{"TIMESTAMP_TRUNC keeps its DATETIME type in the rewritten bounds",
			"SELECT * WHERE TIMESTAMP_TRUNC(x, YEAR) = CAST('2021-01-01' AS DATETIME)", "",
			"SELECT * WHERE x < CAST('2022-01-01 00:00:00' AS DATETIME) AND x >= CAST('2021-01-01 00:00:00' AS DATETIME)"},
		{"a nested CAST on the literal side still reads through to its value",
			"SELECT * WHERE DATE_TRUNC('day', x) = CAST(CAST('2021-01-01 01:02:03' AS DATETIME) AS DATE)", "",
			"SELECT * WHERE x < CAST('2021-01-02' AS DATE) AND x >= CAST('2021-01-01' AS DATE)"},
		{"a nested CAST keeps the OUTER cast's own type, TIMESTAMP included",
			"SELECT * WHERE DATE_TRUNC('day', CAST(x AS DATE)) <= CAST('2021-01-01 01:02:03' AS TIMESTAMP)", "",
			"SELECT * WHERE CAST(x AS DATE) < CAST('2021-01-02 00:00:00' AS TIMESTAMP)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ParseOne(tc.sql, tc.dialect)
			if err != nil {
				t.Fatalf("ParseOne(%q): %v", tc.sql, err)
			}
			got, err := Generate(Simplify(e, tc.dialect), tc.dialect)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.want {
				t.Errorf("Simplify(%q)\n  want %s\n  got  %s", tc.sql, tc.want, got)
			}
			if _, err := ParseOne(got, tc.dialect); err != nil {
				t.Fatalf("the fold's own output %q does not parse back: %v", got, err)
			}
		})
	}
}

// TestDateTruncDeclines covers every shape the RANGE fold must leave alone:
// an unrecognised unit, or a comparison against a date that isn't exactly on
// a floor. Both of these compare DATE_TRUNC against a date LITERAL, which
// sort_comparison's own constant check leaves on the right where it was
// written -- unlike the non-literal cases in TestDateTruncEndToEnd, which
// still get reordered even though the range fold itself declines the same
// way.
func TestDateTruncDeclines(t *testing.T) {
	for _, sql := range []string{
		"SELECT * WHERE DATE_TRUNC('quarter', x) = CAST('2021-01-02' AS DATE)",
		"SELECT * WHERE DATE_TRUNC('year', x) <> CAST('2021-01-02' AS DATE)",
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

// TestDateTruncComparisonAgainstNonLiteralReorders covers the range fold
// declining against a non-literal right-hand side -- DATE_TRUNC over a
// column with no literal to read on the other side, and a nested-cast
// source that is not itself a date literal -- where sort_comparison's own
// `gen(l) > gen(r)` tiebreak still reorders the two sides even though
// neither is a column or a constant, matching the reference exactly.
func TestDateTruncComparisonAgainstNonLiteralReorders(t *testing.T) {
	for _, tc := range []struct{ sql, want string }{
		{"SELECT * WHERE DATE_TRUNC('day', x) = CAST(y AS DATE)",
			"SELECT * WHERE CAST(y AS DATE) = DATE_TRUNC('DAY', x)"},
		{"SELECT * WHERE TIMESTAMP_TRUNC(x, YEAR) = CAST(CAST(y AS DATE) AS DATETIME)",
			"SELECT * WHERE CAST(CAST(y AS DATE) AS DATETIME) = TIMESTAMP_TRUNC(x, YEAR)"},
	} {
		t.Run(tc.sql, func(t *testing.T) {
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
		})
	}
}

// TestDateTruncFloorAndCeil pins datetime_floor/date_ceil directly, read out
// of the reference for a spread of dates and units, across both week-start
// conventions this port's five dialects use.
func TestDateTruncFloorAndCeil(t *testing.T) {
	d := func(y int, m time.Month, day int) time.Time {
		return time.Date(y, m, day, 0, 0, 0, 0, time.UTC)
	}
	for _, tc := range []struct {
		dialect         string
		date            time.Time
		unit            string
		wantFloor, want time.Time
	}{
		{"", d(2023, 12, 15), "WEEK", d(2023, 12, 11), d(2023, 12, 18)},
		{"", d(2023, 12, 16), "WEEK", d(2023, 12, 11), d(2023, 12, 18)},
		{"", d(2023, 12, 11), "WEEK", d(2023, 12, 11), d(2023, 12, 11)}, // already on the floor
		{"tsql", d(2021, 12, 8), "WEEK", d(2021, 12, 5), d(2021, 12, 12)},
		{"tsql", d(2023, 12, 10), "WEEK", d(2023, 12, 10), d(2023, 12, 10)},
		{"", d(2024, 2, 29), "YEAR", d(2024, 1, 1), d(2025, 1, 1)},
		{"", d(2023, 7, 4), "QUARTER", d(2023, 7, 1), d(2023, 10, 1)},
		{"", d(2023, 3, 31), "MONTH", d(2023, 3, 1), d(2023, 4, 1)},
		{"", d(2023, 10, 15), "DAY", d(2023, 10, 15), d(2023, 10, 15)},
	} {
		t.Run(tc.date.Format("2006-01-02")+" "+tc.unit+" "+tc.dialect, func(t *testing.T) {
			floor, ok := dateTruncFloor(tc.date, tc.unit, tc.dialect)
			if !ok || !floor.Equal(tc.wantFloor) {
				t.Errorf("dateTruncFloor = %v (ok=%v), want %v", floor, ok, tc.wantFloor)
			}
			ceil, ok := dateTruncCeil(tc.date, tc.unit, tc.dialect)
			if !ok || !ceil.Equal(tc.want) {
				t.Errorf("dateTruncCeil = %v (ok=%v), want %v", ceil, ok, tc.want)
			}
		})
	}
	if _, ok := dateTruncFloor(time.Now(), "HOUR", ""); ok {
		t.Error("dateTruncFloor(HOUR) should be refused; this port floors date-level units only")
	}
	if _, ok := dateTruncCeil(time.Now(), "HOUR", ""); ok {
		t.Error("dateTruncCeil(HOUR) should be refused for the same reason")
	}
}

// TestDateTruncNextFloor covers the boundary GT and LTE both read: floor(t)
// plus one unit UNCONDITIONALLY, unlike ceil, which returns t itself when t
// already sits on a floor.
func TestDateTruncNextFloor(t *testing.T) {
	onFloor := time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)
	got, ok := dateTruncNextFloor(onFloor, "YEAR", "")
	want := time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC)
	if !ok || !got.Equal(want) {
		t.Errorf("dateTruncNextFloor(on a floor) = %v (ok=%v), want %v -- ceil would have returned t itself", got, ok, want)
	}
	if _, ok := dateTruncNextFloor(onFloor, "FOO", ""); ok {
		t.Error("dateTruncNextFloor with an unknown unit should be refused")
	}
}

// TestDateTruncOf covers dateTruncOf's own gates: a class it does not read
// at all, a missing unit, and a unit outside the date-level set.
func TestDateTruncOf(t *testing.T) {
	if _, _, ok := dateTruncOf(New("Column")); ok {
		t.Error("dateTruncOf(a non-trunc node) should be refused")
	}
	missingUnit := New("DateTrunc", Arg{"this", New("Column")})
	if _, _, ok := dateTruncOf(missingUnit); ok {
		t.Error("dateTruncOf(DateTrunc with no unit) should be refused")
	}
	unknownUnit := New("DateTrunc", Arg{"this", New("Column")}, Arg{"unit", New("Var", Arg{"this", "HOUR"})})
	if _, _, ok := dateTruncOf(unknownUnit); ok {
		t.Error("dateTruncOf(DateTrunc('hour', ...)) should be refused; not a date-level unit")
	}
}

// TestFoldDateTruncIgnoresOtherClasses covers the three fold entry points'
// own dispatch: each is called for every node of a wider class than it
// actually reads (every comparison, every In), and has to decline quietly
// for the ones that are not its own shape.
func TestFoldDateTruncIgnoresOtherClasses(t *testing.T) {
	if out := foldDateTruncLiteral(New("Column"), ""); out != nil {
		t.Errorf("foldDateTruncLiteral(a non-trunc node) = %v, want nil", out)
	}
	if out := foldDateTruncComparison(New("Add"), nil, ""); out != nil {
		t.Errorf("foldDateTruncComparison(Add) = %v, want nil", out)
	}
	plainEq := New("EQ",
		Arg{"this", New("Column")},
		Arg{"expression", New("Literal", Arg{"this", "1"}, Arg{"is_string", false})})
	if out := foldDateTruncComparison(plainEq, nil, ""); out != nil {
		t.Errorf("foldDateTruncComparison(x = 1) = %v, want nil (not this fold's job)", out)
	}
	if out := foldDateTruncIn(New("EQ"), nil, ""); out != nil {
		t.Errorf("foldDateTruncIn(EQ) = %v, want nil", out)
	}
	emptyIn := New("In", Arg{"this", New("DateTrunc",
		Arg{"this", New("Column")}, Arg{"unit", New("Var", Arg{"this", "YEAR"})})},
		Arg{"expressions", []*Expression{}})
	if out := foldDateTruncIn(emptyIn, nil, ""); out != nil {
		t.Errorf("foldDateTruncIn(IN with no members) = %v, want nil", out)
	}
}

// TestMergeDateRanges covers merge_ranges' own three shapes: ranges that do
// not touch, ranges that overlap, and ranges that are exactly adjacent at
// the boundary -- [low, high) makes `high == next.low` adjacency, not a gap,
// which is what lets DATE_TRUNC('year', x) IN (2021, 2022) merge into one
// two-year range rather than staying two.
func TestMergeDateRanges(t *testing.T) {
	d := func(y int) time.Time { return time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC) }
	got := mergeDateRanges([]dateRange{
		{low: d(2021), high: d(2022)},
		{low: d(2023), high: d(2024)},
	})
	if len(got) != 2 {
		t.Fatalf("non-adjacent ranges merged into %d, want 2", len(got))
	}
	got = mergeDateRanges([]dateRange{
		{low: d(2021), high: d(2022)},
		{low: d(2022), high: d(2023)},
	})
	if len(got) != 1 || !got[0].low.Equal(d(2021)) || !got[0].high.Equal(d(2023)) {
		t.Errorf("adjacent ranges merged into %v, want one [2021, 2023)", got)
	}
	// Out of input order, and overlapping rather than merely adjacent.
	got = mergeDateRanges([]dateRange{
		{low: d(2023), high: time.Date(2023, 6, 1, 0, 0, 0, 0, time.UTC)},
		{low: d(2021), high: d(2024)},
	})
	if len(got) != 1 || !got[0].low.Equal(d(2021)) || !got[0].high.Equal(d(2024)) {
		t.Errorf("overlapping out-of-order ranges merged into %v, want one [2021, 2024)", got)
	}
	if got := mergeDateRanges(nil); got != nil {
		t.Errorf("mergeDateRanges(nil) = %v, want nil", got)
	}
}

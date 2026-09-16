package sqlglot

import "testing"

// TestSimplifyEqualityMovesADateAcrossACompare extends simplify_equality's
// existing numeric case to a date literal opposite an INTERVAL: `x -
// INTERVAL 1 DAY = CAST('2021-01-01' AS DATE)` moves the INTERVAL across the
// comparison the same way a bare number would, landing on an Add for a
// later pass to fold the rest of the way. Every value here is read
// directly from the reference.
func TestSimplifyEqualityMovesADateAcrossACompare(t *testing.T) {
	for _, tc := range []struct{ name, sql, want string }{
		{"a Sub of an INTERVAL moves across an EQ, inverted to Add",
			"SELECT * WHERE x - INTERVAL 1 DAY = CAST('2021-01-01' AS DATE)",
			"SELECT * WHERE x = CAST('2021-01-02' AS DATE)"},
		{"the date literal may be written on either side of the EQ",
			"SELECT * WHERE CAST('2021-01-01' AS DATE) = x - INTERVAL 1 DAY",
			"SELECT * WHERE x = CAST('2021-01-02' AS DATE)"},
		{"a TS_OR_DS_TO_DATE call is a date literal too",
			"SELECT * WHERE x - INTERVAL 1 DAY = TS_OR_DS_TO_DATE('2021-01-01 00:00:01')",
			"SELECT * WHERE x = CAST('2021-01-02' AS DATE)"},
		{"an Add of an INTERVAL moves across too, inverted to Sub",
			"SELECT * WHERE x + INTERVAL 1 DAY = CAST('2021-01-01' AS DATE)",
			"SELECT * WHERE x = CAST('2020-12-31' AS DATE)"},
		{"Add is commutative: the INTERVAL may lead",
			"SELECT * WHERE INTERVAL 1 DAY + x = CAST('2021-01-01' AS DATE)",
			"SELECT * WHERE x = CAST('2020-12-31' AS DATE)"},
		{"a comparison other than EQ moves the same way, keeping its own direction",
			"SELECT * WHERE x - INTERVAL 1 HOUR > CAST('2021-01-01' AS DATETIME)",
			"SELECT * WHERE x > CAST('2021-01-01 01:00:00' AS DATETIME)"},
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
			if _, err := ParseOne(got, ""); err != nil {
				t.Fatalf("the fold's own output %q does not parse back: %v", got, err)
			}
		})
	}
}

// TestSimplifyEqualityDeclinesNonDateNonNumberShapes covers what the date
// branch must leave alone: neither operand of the Sub is a date or an
// interval (both sides are plain columns), and a Sub whose comparison
// partner is a plain column rather than a date literal -- there is no
// constant on either side of the EQ to move anything across.
func TestSimplifyEqualityDeclinesNonDateNonNumberShapes(t *testing.T) {
	for _, tc := range []struct{ sql, want string }{
		{"SELECT * WHERE x - y = CAST('2021-01-01' AS DATE)",
			"SELECT * WHERE x - y = CAST('2021-01-01' AS DATE)"},
		{"SELECT * WHERE x - INTERVAL 1 DAY = y",
			"SELECT * WHERE y = x - INTERVAL '1' DAY"},
		{"SELECT * WHERE x - y = z",
			"SELECT * WHERE z = x - y"},
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

func TestIsDateLiteral(t *testing.T) {
	dateCast := New("Cast",
		Arg{"this", New("Literal", Arg{"this", "2021-01-01"}, Arg{"is_string", true})},
		Arg{"to", New("DataType", Arg{"this", DataTypeKind("DATE")})})
	if !isDateLiteral(dateCast) {
		t.Errorf("isDateLiteral(CAST(... AS DATE)) = false, want true")
	}
	col := New("Column", Arg{"this", New("Identifier", Arg{"this", "x"})})
	if isDateLiteral(col) {
		t.Errorf("isDateLiteral(Column) = true, want false")
	}
}

func TestIsIntervalLiteral(t *testing.T) {
	iv := New("Interval",
		Arg{"this", New("Literal", Arg{"this", "1"}, Arg{"is_string", false})},
		Arg{"unit", New("Var", Arg{"this", "DAY"})})
	if !isIntervalLiteral(iv) {
		t.Errorf("isIntervalLiteral(INTERVAL 1 DAY) = false, want true")
	}
	if isIntervalLiteral(New("Column", Arg{"this", New("Identifier", Arg{"this", "x"})})) {
		t.Errorf("isIntervalLiteral(Column) = true, want false")
	}
	if isIntervalLiteral(nil) {
		t.Errorf("isIntervalLiteral(nil) = true, want false")
	}
}

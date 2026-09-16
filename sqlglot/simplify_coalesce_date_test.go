package sqlglot

import "testing"

// TestCoalesceDateLiteralEndToEnd extends isNonnullConstant -- already
// covering a plain Literal or Boolean -- to a date/datetime literal: a
// CAST that extractDateValue can read is exactly as safe a COALESCE
// shortcut as a bare literal, since it can never be NULL either. Every
// value here is read directly from the reference.
func TestCoalesceDateLiteralEndToEnd(t *testing.T) {
	for _, tc := range []struct{ name, sql, want string }{
		{"a single date cast is a nonnull constant, dropping the rest of COALESCE",
			"SELECT COALESCE(CAST('2023-01-01' AS TIMESTAMP), x)",
			"SELECT CAST('2023-01-01' AS TIMESTAMP)"},
		{"a nested CAST reading a date through another CAST counts too",
			"SELECT COALESCE(CAST(CAST('2023-01-01' AS TIMESTAMP) AS DATE), x)",
			"SELECT CAST(CAST('2023-01-01' AS TIMESTAMP) AS DATE)"},
		{"a CAST of a COLUMN to DATE is not a constant, so COALESCE stays",
			"SELECT COALESCE(CAST(x AS DATE), y)",
			"SELECT COALESCE(CAST(x AS DATE), y)"},
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

func TestIsNonnullConstant(t *testing.T) {
	dateCast := New("Cast",
		Arg{"this", New("Literal", Arg{"this", "2023-01-01"}, Arg{"is_string", true})},
		Arg{"to", New("DataType", Arg{"this", DataTypeKind("DATE")})})
	if !isNonnullConstant(dateCast) {
		t.Errorf("isNonnullConstant(CAST(... AS DATE)) = false, want true")
	}
	col := New("Column", Arg{"this", New("Identifier", Arg{"this", "x"})})
	if isNonnullConstant(col) {
		t.Errorf("isNonnullConstant(Column) = true, want false")
	}
	if isNonnullConstant(nil) {
		t.Errorf("isNonnullConstant(nil) = true, want false")
	}
}

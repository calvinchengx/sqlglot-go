package sqlglot

import "testing"

// The writers for the two list functions DuckDB names with strings: a
// boundary word that is not CEIL is the START of the slice, and an order that
// is not a literal cannot be written at all.
func TestSortAndSliceWriters(t *testing.T) {
	for _, c := range []struct{ sql, dialect, want string }{
		{"SELECT TIME_SLICE(dt, INTERVAL 5 MINUTE, 'CEIL')", "starrocks",
			"SELECT TIME_SLICE(dt, INTERVAL 5 MINUTE, CEIL)"},
		{"SELECT TIME_SLICE(dt, 5, 'minute', 'CEIL')", "starrocks",
			"SELECT TIME_SLICE(dt, INTERVAL 5 MINUTE, CEIL)"},
		{"SELECT TIME_SLICE(dt, 5, 'minute', 'floor')", "starrocks",
			"SELECT TIME_SLICE(dt, INTERVAL 5 MINUTE, FLOOR)"},
		{"SELECT LIST_SORT(x, 'DESC', 'NULLS FIRST')", "duckdb",
			"SELECT LIST_SORT(x, 'DESC', 'NULLS FIRST')"},
	} {
		tree, err := ParseOne(c.sql, c.dialect)
		if err != nil {
			t.Errorf("[%s] ParseOne(%q): %v", c.dialect, c.sql, err)
			continue
		}
		got, err := Generate(tree, c.dialect)
		if err != nil {
			t.Errorf("[%s] Generate(%q): %v", c.dialect, c.sql, err)
			continue
		}
		if got != c.want {
			t.Errorf("[%s] %s\n  want %s\n  got  %s", c.dialect, c.sql, c.want, got)
		}
	}

	// An ArraySort whose order is not a literal Boolean is refused rather
	// than written with a guess at what it means.
	sort := New("ArraySort", Arg{"this", New("Column", Arg{"this", New("Identifier", Arg{"this", "x"})})},
		Arg{"asc", New("Column", Arg{"this", New("Identifier", Arg{"this", "a"})})})
	if _, err := Generate(sort, "duckdb"); err == nil {
		t.Error("an ArraySort over a non-literal order was written; it should be refused")
	}
	// The same node outside DuckDB is ARRAY_SORT over its two arguments.
	plain := New("ArraySort", Arg{"this", New("Column", Arg{"this", New("Identifier", Arg{"this", "x"})})})
	if got, err := Generate(plain, "postgres"); err != nil || got != "ARRAY_SORT(x, )" {
		t.Errorf("ARRAY_SORT wrote %q (%v)", got, err)
	}
}

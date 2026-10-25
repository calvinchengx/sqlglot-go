package sqlglot

import "testing"

// GROUP BY ALL and DISTINCT are a prefix, not the whole clause, and CUBE,
// ROLLUP and GROUPING SETS are ordinary members of the comma-separated list.
// A parse or generate error fails the row.
func TestGroupByGrouping(t *testing.T) {
	cases := [][3]string{
		{"presto", "SELECT origin_state, destination_state, origin_zip, SUM(package_weight) FROM shipping GROUP BY ALL CUBE (origin_state, destination_state), ROLLUP (origin_state, origin_zip)",
			"SELECT origin_state, destination_state, origin_zip, SUM(package_weight) FROM shipping GROUP BY ALL CUBE (origin_state, destination_state), ROLLUP (origin_state, origin_zip)"},
		{"presto", "SELECT origin_state, destination_state, origin_zip, SUM(package_weight) FROM shipping GROUP BY DISTINCT CUBE (origin_state, destination_state), ROLLUP (origin_state, origin_zip)",
			"SELECT origin_state, destination_state, origin_zip, SUM(package_weight) FROM shipping GROUP BY DISTINCT CUBE (origin_state, destination_state), ROLLUP (origin_state, origin_zip)"},
		{"presto", "SELECT JSON_EXTRACT_SCALAR(CAST(extra AS JSON), '$.value_b'), COUNT(*) FROM table_a GROUP BY DISTINCT (JSON_EXTRACT_SCALAR(CAST(extra AS JSON), '$.value_b'))",
			"SELECT JSON_EXTRACT_SCALAR(CAST(extra AS JSON), '$.value_b'), COUNT(*) FROM table_a GROUP BY DISTINCT (JSON_EXTRACT_SCALAR(CAST(extra AS JSON), '$.value_b'))"},
		{"", "SELECT a FROM test GROUP BY ALL",
			"SELECT a FROM test GROUP BY ALL"},
		{"", "SELECT a FROM test GROUP BY CUBE (x), ROLLUP (x, y, z)",
			"SELECT a FROM test GROUP BY CUBE (x), ROLLUP (x, y, z)"},
	}
	written := 0
	for _, c := range cases {
		tree, err := ParseOne(c[1], c[0])
		if err != nil {
			t.Errorf("[%s] ParseOne(%q): %v", c[0], c[1], err)
			continue
		}
		got, err := Generate(tree, c[0])
		if err != nil {
			t.Errorf("[%s] Generate(%q): %v", c[0], c[1], err)
			continue
		}
		written++
		if got != c[2] {
			t.Errorf("[%s] %s\n  want %s\n  got  %s", c[0], c[1], c[2], got)
		}
	}
	if written != len(cases) {
		t.Errorf("wrote %d statements, want %d", written, len(cases))
	}
}

// A construct with no comma in front of it is no longer a suffix the
// reference accepts: `GROUP BY CUBE(a) ROLLUP(a)` leaves ROLLUP unparsed.
func TestGroupByConstructWithoutCommaIsRefused(t *testing.T) {
	if _, err := ParseOne(
		"SELECT a, b, c, d, sum(y) FROM z GROUP BY CUBE(a) ROLLUP(a), GROUPING SETS((b, c)), d",
		"presto"); err == nil {
		t.Error("read a second grouping construct with no comma in front of it")
	}
}

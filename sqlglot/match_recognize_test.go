package sqlglot

import "testing"

func TestMatchRecognize(t *testing.T) {
	presto := "SELECT\n  *\nFROM orders\nMATCH_RECOGNIZE (\n  PARTITION BY custkey\n  ORDER BY\n    orderdate\n  MEASURES\n    A.totalprice AS starting_price,\n    LAST(B.totalprice) AS bottom_price,\n    LAST(C.totalprice) AS top_price\n  ONE ROW PER MATCH\n  AFTER MATCH SKIP PAST LAST ROW\n  PATTERN (A B+ C+ D+)\n  DEFINE\n    B AS totalprice < PREV(totalprice),\n    C AS totalprice > PREV(totalprice) AND totalprice <= A.totalprice,\n    D AS totalprice > PREV(totalprice),\n    E AS MAX(foo) >= NEXT(bar)\n)"
	prestoWant := "SELECT * FROM orders MATCH_RECOGNIZE (PARTITION BY custkey ORDER BY orderdate MEASURES A.totalprice AS starting_price, LAST(B.totalprice) AS bottom_price, LAST(C.totalprice) AS top_price ONE ROW PER MATCH AFTER MATCH SKIP PAST LAST ROW PATTERN (A B+ C+ D+) DEFINE B AS totalprice < PREV(totalprice), C AS totalprice > PREV(totalprice) AND totalprice <= A.totalprice, D AS totalprice > PREV(totalprice), E AS MAX(foo) >= NEXT(bar))"
	trino := "SELECT * FROM tbl MATCH_RECOGNIZE (PARTITION BY id ORDER BY col MEASURES FIRST(col, 2) AS col1, LAST(col, 2) AS col2 PATTERN (B* A) DEFINE A AS col = 1)"
	for _, tc := range []struct{ dialect, sql, want string }{
		{"presto", presto, prestoWant},
		{"trino", trino, trino},
	} {
		tree, err := ParseOne(tc.sql, tc.dialect)
		if err != nil {
			t.Fatalf("%s ParseOne: %v", tc.dialect, err)
		}
		if _, ok := tree.Args["match"].(*Expression); !ok || tree.Args["match"].(*Expression).Class != "MatchRecognize" {
			t.Fatalf("%s match = %#v", tc.dialect, tree.Args["match"])
		}
		got, err := Generate(tree, tc.dialect)
		if err != nil || got != tc.want {
			t.Fatalf("%s Generate = %q, %v", tc.dialect, got, err)
		}
	}
}

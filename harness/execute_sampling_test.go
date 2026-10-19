package harness

import (
	"testing"

	"github.com/calvinchengx/sqlglot-go/sqlglot"
)

// A statement whose tree holds a TableSample is set aside by the execution
// oracle rather than compared, because a sample is a random draw. The rule has
// to come off the TREE: looking at the rows cannot tell, since a small sample
// repeats by luck -- which is exactly how `USING SAMPLE 10%` was once reported
// as the port's rewrite being wrong.
func TestASamplingStatementIsSetAside(t *testing.T) {
	for _, tc := range []struct {
		sql     string
		dialect string
		want    bool
	}{
		{"SELECT * FROM tbl USING SAMPLE 10%", "duckdb", true},
		{"SELECT * FROM tbl USING SAMPLE 10 ROWS", "duckdb", true},
		{"SELECT * FROM tbl TABLESAMPLE BERNOULLI (10)", "duckdb", true},
		{"SELECT * FROM tbl", "duckdb", false},
		{"SELECT RANDOM() FROM tbl", "duckdb", false},
	} {
		tree, err := sqlglot.ParseOne(tc.sql, tc.dialect)
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		if got := samplesIn(tree); got != tc.want {
			t.Errorf("samplesIn(%q) = %v, want %v", tc.sql, got, tc.want)
		}
	}
}

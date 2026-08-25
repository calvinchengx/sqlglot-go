package harness

import (
	"os"
	"strings"
	"testing"

	"github.com/calvinchengx/sqlglot-go/sqlglot"
)

func TestSampleLabel(t *testing.T) {
	want := os.Getenv("LABEL")
	_, cases, _ := Load("../testdata/expected")
	n := 0
	for _, c := range cases {
		if _, perr := sqlglot.ParseOne(c.SQL, c.Dialect); perr != nil &&
			strings.Contains(perr.Error(), want) {
			n++
			if n <= 12 {
				t.Log(c.Dialect + "  " + truncate(c.SQL, 88))
			}
		}
	}
	t.Logf("total %d", n)
}

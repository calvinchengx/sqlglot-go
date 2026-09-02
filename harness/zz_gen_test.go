package harness

import (
	"strings"
	"testing"

	"github.com/calvinchengx/sqlglot-go/sqlglot"
)

func TestZZGenRefused(t *testing.T) {
	_, cases, err := Load("../testdata/expected")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		e, perr := sqlglot.ParseOne(c.SQL, c.Dialect)
		if perr != nil {
			continue
		}
		if _, gerr := sqlglot.Generate(e, c.Dialect); gerr != nil {
			t.Logf("[%s] %s ||| %v ||| %s", c.Dialect,
				strings.ReplaceAll(c.SQL, "\n", " "), gerr,
				strings.ReplaceAll(c.Rendered, "\n", " "))
		}
	}
}

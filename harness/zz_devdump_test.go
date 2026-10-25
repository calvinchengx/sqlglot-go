package harness

import (
	"fmt"
	"testing"

	"github.com/calvinchengx/sqlglot-go/sqlglot"
)

func TestDevDump(t *testing.T) {
	for _, q := range []string{"OCTET_LENGTH('foo')", "SELECT struct(1 AS a)", "SELECT STRUCT(1 AS a)"} {
		tr, err := sqlglot.ParseOne(q, "bigquery")
		if err != nil {
			fmt.Println("ERR", q, err)
			continue
		}
		out, gerr := sqlglot.Generate(tr, "bigquery")
		fmt.Printf("%-30s -> %s (class %s) %v\n", q, out, tr.Class, gerr)
	}
}

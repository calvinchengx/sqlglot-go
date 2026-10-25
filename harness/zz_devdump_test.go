package harness

import (
	"fmt"
	"testing"

	"github.com/calvinchengx/sqlglot-go/sqlglot"
)

func TestDevDump(t *testing.T) {
	q := "CAST(CAST(x AS DATETIMEOFFSET(6)) AT TIME ZONE 'Pacific Standard Time' AS DATETIME2(6))"
	tr, err := sqlglot.ParseOne(q, "fabric")
	if err != nil {
		fmt.Println("ERR", err)
		return
	}
	var walk func(e *sqlglot.Expression, d int)
	walk = func(e *sqlglot.Expression, d int) {
		fmt.Printf("%*s%s\n", d*2, "", e.Class)
		for _, k := range e.Keys {
			switch v := e.Args[k].(type) {
			case *sqlglot.Expression:
				walk(v, d+1)
			}
		}
	}
	walk(tr, 0)
}

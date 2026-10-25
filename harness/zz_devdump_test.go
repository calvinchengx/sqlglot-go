package harness

import (
	"fmt"
	"testing"

	"github.com/calvinchengx/sqlglot-go/sqlglot"
)

func TestDevDump(t *testing.T) {
	tr, _ := sqlglot.ParseOne("SELECT FLOOR(5)", "presto")
	e := tr.Args["expressions"].([]*sqlglot.Expression)[0]
	fmt.Println(e.Class, e.Type)
}

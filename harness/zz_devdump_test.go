package harness

import (
	"fmt"
	"testing"

	"github.com/calvinchengx/sqlglot-go/sqlglot"
)

func TestDevDump(t *testing.T) {
	tr, err := sqlglot.ParseOne("CREATE TABLE t (a INT) ENGINE=InnoDB, /* c */ ROW_FORMAT=DYNAMIC", "mysql")
	if err != nil {
		fmt.Println("ERR", err)
		return
	}
	for _, n := range tr.FindAll("Properties") {
		items := n.Args["expressions"].([]*sqlglot.Expression)
		for _, it := range items {
			fmt.Printf("%s comments=%q\n", it.Class, it.Comments)
		}
	}
	fmt.Println("create comments:", tr.Comments)
}

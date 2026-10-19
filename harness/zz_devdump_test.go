package harness

import (
	"fmt"
	"os"
	"testing"

	"github.com/calvinchengx/sqlglot-go/sqlglot"
)

func TestDevDump(t *testing.T) {
	sql, d := os.Getenv("SQL"), os.Getenv("DIALECT")
	tree, err := sqlglot.ParseOne(sql, d)
	if err != nil {
		fmt.Println("PARSE ERROR:", err)
		return
	}
	g, err := sqlglot.Generate(tree, d)
	fmt.Printf("PORT: %s (err=%v)\n", g, err)
}

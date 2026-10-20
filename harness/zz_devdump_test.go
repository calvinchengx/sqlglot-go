package harness

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"testing"

	"github.com/calvinchengx/sqlglot-go/sqlglot"
)

func TestDevDump(t *testing.T) {
	if os.Getenv("SQL") != "" {
		sql, d := os.Getenv("SQL"), os.Getenv("DIALECT")
		tree, err := sqlglot.ParseOne(sql, d)
		if err != nil {
			fmt.Println("PARSE ERROR:", err)
			return
		}
		g, err := sqlglot.Generate(tree, d)
		fmt.Printf("PORT: %s (err=%v)\n", g, err)
		return
	}
	raw, _ := os.ReadFile("../testdata/expected/index.json")
	var idx struct {
		Statements []struct{ Dialect, SQL, Key string } `json:"statements"`
	}
	if err := json.Unmarshal(raw, &idx); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, s := range idx.Statements {
		if s.Dialect != os.Getenv("DIALECT") {
			continue
		}
		if _, err := sqlglot.ParseOne(s.SQL, s.Dialect); err != nil {
			out = append(out, fmt.Sprintf("%-40s | %s", s.SQL, err))
		}
	}
	sort.Strings(out)
	fmt.Printf("=== %d refused ===\n", len(out))
	for _, l := range out {
		fmt.Println(l)
	}
}

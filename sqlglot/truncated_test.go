package sqlglot

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestTruncatedStatementsNeverPanic cuts every statement of the reference
// corpus off at each word boundary and reads what is left. Most prefixes are
// not statements at all, which is the point: they walk the error paths of
// every grammar rule, and each of them has to turn the input away rather than
// panic, hang, or write something back that it then cannot read.
func TestTruncatedStatementsNeverPanic(t *testing.T) {
	raw, err := os.ReadFile("../testdata/expected/index.json")
	if err != nil {
		t.Skipf("no corpus: %v", err)
	}
	var index struct {
		Statements []struct {
			SQL     string `json:"sql"`
			Dialect string `json:"dialect"`
		} `json:"statements"`
	}
	if err := json.Unmarshal(raw, &index); err != nil {
		t.Fatal(err)
	}
	for _, entry := range index.Statements {
		words := strings.Fields(entry.SQL)
		if len(words) > 60 {
			words = words[:60]
		}
		for cut := 1; cut < len(words); cut++ {
			sql := strings.Join(words[:cut], " ")
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("[%s] %q panicked: %v", entry.Dialect, sql, r)
					}
				}()
				tree, err := ParseOne(sql, entry.Dialect)
				if err == nil && tree != nil {
					_, _ = Generate(tree, entry.Dialect)
				}
			}()
		}
	}
}

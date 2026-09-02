package harness

import (
	"os"
	"testing"

	"github.com/calvinchengx/sqlglot-go/sqlglot"
)

func TestZZRT(t *testing.T) {
	sql := os.Getenv("SQL")
	if sql == "" {
		t.Skip("no sql")
	}
	e, err := sqlglot.ParseOne(sql, os.Getenv("DIA"))
	if err != nil {
		t.Fatalf("parse %v", err)
	}
	out, gerr := sqlglot.Generate(e, os.Getenv("DIA"))
	t.Logf("OUT %q (%v)", out, gerr)
	if gerr == nil {
		_, e2 := sqlglot.ParseOne(out, os.Getenv("DIA"))
		t.Logf("REPARSE %v", e2)
	}
}

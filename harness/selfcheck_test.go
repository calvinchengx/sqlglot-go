package harness

import (
	"testing"

	"github.com/calvinchengx/sqlglot-go/sqlglot"
)

// The harness must be able to FAIL. A differential test that only ever
// reports gaps would pass a parser that produced nonsense, so this proves
// three things against a reference expectation for `SELECT 1`:
//
//  1. a tree built to match the reference exactly is reported as a match;
//  2. a tree with one wrong leaf is reported as a divergence, at the record;
//  3. a tree with one missing node is reported as a divergence.
func TestTheHarnessCanTellRightFromWrong(t *testing.T) {
	_, cases, err := Load("../testdata/expected")
	if err != nil {
		t.Fatal(err)
	}
	var want []map[string]any
	for _, c := range cases {
		if c.SQL == "SELECT 1" && c.Dialect == "" {
			want = Normalise(c.Tree)
			break
		}
	}
	if want == nil {
		t.Fatal("the corpus has no neutral `SELECT 1`; the self-check needs one")
	}

	// 1. Built by hand to the reference's shape: Select{expressions=[Literal{this="1", is_string=false}]}
	lit := sqlglot.New("Literal")
	lit.Set("this", "1")
	lit.Set("is_string", false)
	sel := sqlglot.New("Select")
	sel.Set("expressions", []*sqlglot.Expression{lit})
	if d := Diff(want, Normalise(sel.Dump())); d != "" {
		t.Fatalf("a correct tree was reported as divergent:\n%s", d)
	}

	// 2. One wrong leaf.
	bad := sqlglot.New("Literal")
	bad.Set("this", "2")
	bad.Set("is_string", false)
	sel2 := sqlglot.New("Select")
	sel2.Set("expressions", []*sqlglot.Expression{bad})
	if d := Diff(want, Normalise(sel2.Dump())); d == "" {
		t.Fatal("a wrong literal was reported as a match")
	}

	// 3. A missing node.
	sel3 := sqlglot.New("Select")
	if d := Diff(want, Normalise(sel3.Dump())); d == "" {
		t.Fatal("a tree missing a node was reported as a match")
	}
}

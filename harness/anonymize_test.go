package harness

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/calvinchengx/sqlglot-go/sqlglot"
)

// TestAnonymizeAgainstReference holds the port's Anonymize to the
// reference's own anonymize+render, run over the SAME corpus
// TestAgainstReference measures: every statement the reference's own
// identity.sql and per-dialect fixtures harvest, whether or not the port
// can PARSE it -- anonymize works on tokens, not a tree, so it owes nothing
// to what the parser has closed.
//
// Deterministic and total: every statement gets an answer, so there is no
// "no answer" bucket here the way there is for annotate or simplify. Only
// exact match or wrong.
func TestAnonymizeAgainstReference(t *testing.T) {
	raw, err := os.ReadFile("../testdata/anonymize.json")
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		Reference string `json:"reference"`
		Cases     []struct {
			Dialect  string `json:"dialect"`
			SQL      string `json:"sql"`
			Rendered string `json:"rendered"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}

	var agreed, wrong int
	var problems []string
	for _, c := range data.Cases {
		got := sqlglot.Anonymize(c.SQL, c.Dialect)
		if got == c.Rendered {
			agreed++
			continue
		}
		wrong++
		if len(problems) < 12 {
			problems = append(problems, "WRONG: ["+c.Dialect+"] "+truncate(c.SQL, 70)+
				"\n    want "+truncate(c.Rendered, 90)+
				"\n    got  "+truncate(got, 90))
		}
	}

	t.Logf("%d of %d agreed, %d wrong", agreed, len(data.Cases), wrong)
	for _, p := range problems {
		t.Error(p)
	}
	if wrong > 0 {
		t.Fatalf("%d statement(s) anonymized differently than the reference", wrong)
	}
	assertAnonymizeFloor(t, agreed)
}

func assertAnonymizeFloor(t *testing.T, agreed int) {
	t.Helper()
	const floor = 4508 // raised by hand as anonymize grows; never lowered here
	if agreed < floor {
		t.Errorf("Anonymize REGRESSED: %d agreed, floor %d", agreed, floor)
	}
}

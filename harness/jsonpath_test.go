package harness

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/calvinchengx/sqlglot-go/sqlglot"
)

// TestJSONPathAgainstReference holds the port's JSONPath parser to the
// reference's own conformance suite: the JSONPath Compliance Test Suite
// (tests/fixtures/jsonpath/cts.json), 526 selectors, read by the reference's
// own `sqlglot.jsonpath.parse` rather than by the abstract standard the CTS
// is drawn from -- the reference is knowingly more lenient than the standard
// in places, and matching IT is the actual contract.
//
// Same asymmetry as everywhere else in this repo: a selector the port
// declines is a gap, counted and free; one it reads into a DIFFERENT tree
// than the reference, or accepts when the reference refuses it, is wrong.
func TestJSONPathAgainstReference(t *testing.T) {
	raw, err := os.ReadFile("../testdata/jsonpath.json")
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		Reference string `json:"reference"`
		Cases     []struct {
			Name     string           `json:"name"`
			Selector string           `json:"selector"`
			Valid    bool             `json:"valid"`
			Tree     []map[string]any `json:"tree"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}

	var agreed, noAnswer, wrong int
	var problems []string
	for _, c := range data.Cases {
		got, perr := sqlglot.ParseJSONPath(c.Selector)
		switch {
		case !c.Valid:
			if perr == nil {
				wrong++
				if len(problems) < 12 {
					problems = append(problems, "WRONGLY ACCEPTED: "+c.Selector+" ("+c.Name+")")
				}
			} else {
				agreed++
			}
		case perr != nil:
			noAnswer++
		default:
			if d := Diff(Normalise(c.Tree), Normalise(got.Dump())); d != "" {
				wrong++
				if len(problems) < 12 {
					problems = append(problems, "WRONG: "+c.Selector+" ("+c.Name+")\n"+d)
				}
			} else {
				agreed++
			}
		}
	}

	t.Logf("%d of %d agreed, %d no answer, %d wrong",
		agreed, len(data.Cases), noAnswer, wrong)
	for _, p := range problems {
		t.Error(p)
	}
	if wrong > 0 {
		t.Fatalf("%d selector(s) parsed into a different tree than the reference, or wrongly accepted", wrong)
	}
	assertJSONPathFloor(t, agreed)
}

func assertJSONPathFloor(t *testing.T, agreed int) {
	t.Helper()
	const floor = 519 // raised by hand as the JSONPath parser grows; never lowered here
	if agreed < floor {
		t.Errorf("JSONPath parser REGRESSED: %d agreed, floor %d", agreed, floor)
	}
}

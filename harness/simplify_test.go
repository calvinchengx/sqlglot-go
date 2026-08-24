package harness

import (
	"encoding/json"
	"os"
	"sort"
	"testing"

	"github.com/calvinchengx/sqlglot-go/sqlglot"
)

type simplifyPairs struct {
	Reference string `json:"reference"`
	Pairs     []struct {
		Dialect  string `json:"dialect"`
		SQL      string `json:"sql"`
		Expected string `json:"expected"`
	} `json:"pairs"`
}

// TestSimplifyAgainstReference measures the port's optimizer against
// sqlglot's own contract: tests/fixtures/optimizer/simplify.sql, which pins
// what each statement becomes.
//
// It counts EXACT matches and holds a floor. It does NOT try to decide whether
// a non-exact result is wrong, and that restraint is deliberate. A first cut
// guessed -- "if the output differs from the input, the port must have folded
// it wrongly" -- and reported 35 failures of which nearly all were partial
// folds: correct as far as they went, simply not finished. The one genuine bug
// in that batch (`NOT (2 <> ALL S)` folded to `2 = ALL S`, which is a
// different question) was buried among them.
//
// Whether a rewrite is WRONG is a semantic question, and this repository has
// exactly one harness that can answer it: the execution oracle runs the
// original and the rewrite on a real engine and compares the rows. Simplify
// output is fed to it, so a rewrite that changes an answer fails there --
// where the failure means something -- rather than being guessed at here.
func TestSimplifyAgainstReference(t *testing.T) {
	raw, err := os.ReadFile("../testdata/simplify.json")
	if err != nil {
		t.Fatal(err)
	}
	var data simplifyPairs
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}

	var exact, notYet, unreadable int
	reasons := map[string]int{}
	for _, p := range data.Pairs {
		tree, perr := sqlglot.ParseOne(p.SQL, p.Dialect)
		if perr != nil {
			unreadable++
			continue
		}
		got, gerr := sqlglot.Generate(sqlglot.Simplify(tree, p.Dialect), p.Dialect)
		if gerr != nil {
			unreadable++
			continue
		}
		if got == p.Expected {
			exact++
			continue
		}
		notYet++
		reasons[p.Expected]++
	}

	t.Logf("reference %s: %d of %d simplified exactly, %d not folded that far",
		data.Reference[:12], exact, len(data.Pairs), notYet)
	t.Logf("  %d could not be read or written back at all", unreadable)
	assertSimplifyFloor(t, exact)

	// The most common thing the port declines to fold, so the next rule is
	// chosen by what actually blocks the contract.
	if testing.Verbose() {
		keys := make([]string, 0, len(reasons))
		for k := range reasons {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			if reasons[keys[i]] != reasons[keys[j]] {
				return reasons[keys[i]] > reasons[keys[j]]
			}
			return keys[i] < keys[j]
		})
		for i, k := range keys {
			if i >= 15 {
				break
			}
			t.Logf("  not folded (%d): -> %s", reasons[k], truncate(k, 60))
		}
	}
}

func assertSimplifyFloor(t *testing.T, simplified int) {
	t.Helper()
	const floor = 120 // raised by hand as the optimizer grows; never lowered here
	if simplified < floor {
		t.Errorf("simplify REGRESSED: %d exact, floor %d", simplified, floor)
	}
}

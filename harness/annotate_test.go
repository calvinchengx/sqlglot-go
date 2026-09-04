package harness

import (
	"encoding/json"
	"os"
	"sort"
	"testing"

	"github.com/calvinchengx/sqlglot-go/sqlglot"
)

// TestAnnotateAgainstReference holds the port's type annotator to the
// reference's own contract: the scope-free half of
// tests/fixtures/optimizer/annotate_types.sql and annotate_functions.sql.
//
// Three outcomes, and only one fails the build:
//
//	agreed     the port answered exactly what the reference answers.
//	no answer  the port declined. A gap, counted, and free -- callers treat
//	           no answer as UNKNOWN and keep refusing, as they do today.
//	WRONG      the port answered something ELSE. A caller acting on that
//	           would fold a tree on a type the reference never inferred.
//
// The asymmetry is the point: not knowing a type costs nothing here, and
// knowing it wrongly is how an optimizer produces SQL that runs and lies.
func TestAnnotateAgainstReference(t *testing.T) {
	raw, err := os.ReadFile("../testdata/annotate.json")
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		Cases []struct{ Dialect, SQL, Type string } `json:"cases"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}

	var agreed, noAnswer, wrong, unreadable int
	missing := map[string]int{}
	var problems []string
	for _, c := range data.Cases {
		tree, perr := sqlglot.ParseOne(c.SQL, c.Dialect)
		if perr != nil {
			unreadable++
			continue
		}
		got := sqlglot.Annotate(tree, c.Dialect)
		if got == nil {
			noAnswer++
			missing[c.Type]++
			continue
		}
		// The reference compares RENDERED types, so the port is held to the
		// same: `bool` and `BOOLEAN` are one answer.
		rendered, gerr := sqlglot.Generate(got, c.Dialect)
		switch {
		case gerr != nil:
			unreadable++
		case rendered == c.Type:
			agreed++
		default:
			wrong++
			if len(problems) < 12 {
				problems = append(problems, "WRONG: ["+c.Dialect+"] "+c.SQL+
					"\n    want "+c.Type+"\n    got  "+rendered)
			}
		}
	}

	t.Logf("%d of %d annotated exactly, %d no answer, %d wrong",
		agreed, len(data.Cases), noAnswer, wrong)
	t.Logf("  %d could not be read or written back at all", unreadable)
	for _, p := range problems {
		t.Error(p)
	}
	if wrong > 0 {
		t.Fatalf("%d expression(s) given a type the reference does not infer", wrong)
	}
	assertAnnotateFloor(t, agreed)

	if testing.Verbose() {
		keys := make([]string, 0, len(missing))
		for k := range missing {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			if missing[keys[i]] != missing[keys[j]] {
				return missing[keys[i]] > missing[keys[j]]
			}
			return keys[i] < keys[j]
		})
		for i, k := range keys {
			if i >= 12 {
				break
			}
			t.Logf("  no answer (%d): the reference says %s", missing[k], k)
		}
	}
}

func assertAnnotateFloor(t *testing.T, agreed int) {
	t.Helper()
	const floor = 73 // raised by hand as the annotator grows; never lowered here
	if agreed < floor {
		t.Errorf("annotator REGRESSED: %d agreed, floor %d", agreed, floor)
	}
}

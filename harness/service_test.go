package harness

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/calvinchengx/sqlglot-go/sqlglot"
)

// tally counts one slice of the service corpus.
type tally struct{ parsed, unparsed, mismatched int }

// ServiceCase is one statement data agent service is actually held to, and
// what has to happen to it.
type ServiceCase struct {
	SQL      string           `json:"sql"`
	Dialect  string           `json:"dialect"`
	Category string           `json:"category"`
	From     string           `json:"from"`
	RefOK    bool             `json:"reference_parsed"`
	Rendered string           `json:"rendered"`
	Tree     []map[string]any `json:"tree"`
}

type ServiceCorpus struct {
	Service   string        `json:"service"`
	Reference string        `json:"reference"`
	Cases     []ServiceCase `json:"cases"`
}

// TestAgainstTheService is the measurement that decides the switchover.
//
// The sqlglot corpus measures the port against sqlglot, and most of what it
// still cannot read is dialect exotica no data agent will ever emit. This one
// measures the port against the SQL the service is really tested on: the gold
// answers in its evaluation suites, the statements its guard permits, and the
// ones its adversarial corpus requires it to refuse for a named reason.
//
// The bar for rewriting the Go guard over a parse tree is this number, not
// the other one.
func TestAgainstTheService(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "service", "corpus.json"))
	if err != nil {
		t.Skipf("no service corpus yet (run harness/gen_service_corpus.py): %v", err)
	}
	var corpus ServiceCorpus
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatalf("service corpus: %v", err)
	}

	byCategory := map[string]*tally{}
	byDialect := map[string]*tally{}
	var divergences, gaps, unwritable []string

	get := func(m map[string]*tally, k string) *tally {
		if m[k] == nil {
			m[k] = &tally{}
		}
		return m[k]
	}

	for _, c := range corpus.Cases {
		if !c.RefOK {
			// The reference cannot read it either, so there is nothing to
			// compare against. That is the correct outcome for the one
			// deliberately malformed statement in the corpus.
			if _, err := sqlglot.ParseOne(c.SQL, c.Dialect); err == nil {
				divergences = append(divergences,
					fmt.Sprintf("[%s] %s\n  the port parsed a statement the REFERENCE cannot (%s)",
						c.Dialect, c.SQL, c.From))
			}
			continue
		}

		cat, dia := get(byCategory, c.Category), get(byDialect, c.Dialect)
		tree, perr := sqlglot.ParseOne(c.SQL, c.Dialect)

		// A statement refused for WHAT IT IS needs naming, not parsing: the
		// port has to say "this is a write" or "this is two statements", and
		// a tree for either would have no consumer. Parsing it correctly also
		// satisfies the guard -- `SELECT * INTO t2 FROM t1` is a query that
		// writes, and only the tree says so -- so either outcome counts.
		if c.Category == "must_name_the_statement" &&
			(errors.Is(perr, sqlglot.ErrNotAQuery) || errors.Is(perr, sqlglot.ErrMultipleStatements)) {
			cat.parsed++
			dia.parsed++
			continue
		}

		// Where any refusal is the right answer, a refusal is the right answer.
		if c.Category == "may_refuse_unparsed" && perr != nil {
			cat.parsed++
			dia.parsed++
			continue
		}

		switch {
		case perr != nil:
			cat.unparsed++
			dia.unparsed++
			gaps = append(gaps, fmt.Sprintf("  [%s] %-8s %s\n      %v", c.Category, c.Dialect, c.SQL, perr))
		default:
			if d := Diff(Normalise(c.Tree), Normalise(tree.Dump())); d != "" {
				cat.mismatched++
				dia.mismatched++
				divergences = append(divergences, fmt.Sprintf("[%s] %s\n%s", c.Dialect, c.SQL, d))
			} else {
				cat.parsed++
				dia.parsed++
				// Reading it is half the job: the guard rewrites the tree and
				// hands SQL back, so it has to be writable too.
				if got, gerr := sqlglot.Generate(tree, c.Dialect); gerr != nil {
					unwritable = append(unwritable, fmt.Sprintf("  [%s] %s\n      %v", c.Dialect, c.SQL, gerr))
				} else if got != c.Rendered {
					divergences = append(divergences, fmt.Sprintf(
						"[%s] %s\n  written differently from the reference\n    want %s\n    got  %s",
						c.Dialect, c.SQL, c.Rendered, got))
				}
			}
		}
	}

	t.Logf("data agent service %s, reference %s", short(corpus.Service), short(corpus.Reference))
	for _, k := range sortedKeys(byCategory) {
		v := byCategory[k]
		t.Logf("  %-22s %3d/%-3d parsed  (%d unparsed, %d mismatched)",
			k, v.parsed, v.parsed+v.unparsed+v.mismatched, v.unparsed, v.mismatched)
	}
	for _, k := range sortedKeys(byDialect) {
		v := byDialect[k]
		t.Logf("    %-10s %3d/%-3d", k, v.parsed, v.parsed+v.unparsed+v.mismatched)
	}

	if len(gaps) > 0 {
		t.Logf("%d statement(s) the service needs and the port cannot read yet:", len(gaps))
		for i, g := range gaps {
			if i == 12 {
				t.Logf("  … and %d more", len(gaps)-12)
				break
			}
			t.Log(g)
		}
	}

	if len(unwritable) > 0 {
		t.Errorf("%d statement(s) the port reads but cannot write back:", len(unwritable))
		for _, u := range unwritable {
			t.Error(u)
		}
	}

	writeServiceCoverage(t, corpus, byCategory, byDialect)

	for _, d := range divergences {
		t.Error(d)
	}
	if len(divergences) > 0 {
		t.Fatalf("%d divergence(s) on the service's own SQL", len(divergences))
	}
	assertServiceFloor(t, byCategory)
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func writeServiceCoverage(t *testing.T, corpus ServiceCorpus, byCategory, byDialect map[string]*tally) {
	t.Helper()
	out := map[string]any{"service": corpus.Service, "reference": corpus.Reference}
	for name, m := range map[string]map[string]*tally{
		"by_category": byCategory, "by_dialect": byDialect,
	} {
		section := map[string]any{}
		for k, v := range m {
			section[k] = map[string]int{
				"parsed": v.parsed, "unparsed": v.unparsed, "mismatched": v.mismatched,
				"total": v.parsed + v.unparsed + v.mismatched,
			}
		}
		out[name] = section
	}
	b, err := json.MarshalIndent(out, "", " ")
	if err != nil {
		t.Fatalf("marshal service coverage: %v", err)
	}
	if err := os.WriteFile(filepath.Join("..", "testdata", "service", "coverage.json"), append(b, '\n'), 0o600); err != nil {
		t.Fatalf("write service coverage: %v", err)
	}
}

// assertServiceFloor refuses a regression against the service's own SQL. This
// floor is the one that matters most: it only ever goes up, and when the
// must_parse categories reach their totals the guard can be rewritten.
//
// `may_refuse_unparsed` is deliberately ABSENT from floor.json. That category
// means "any refusal is right", so a floor on it ratchets a guarantee nobody
// wants: when the corpus grew, the statement in that bucket became
// `SELECT * FRO dbo.x`, which the REFERENCE rejects too -- and the ratchet
// called matching the reference a regression. A floor belongs only on
// categories where parsing is required.
func assertServiceFloor(t *testing.T, byCategory map[string]*tally) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "service", "floor.json"))
	if err != nil {
		t.Logf("no service floor yet (testdata/service/floor.json); not enforced")
		return
	}
	var floor map[string]int
	if err := json.Unmarshal(raw, &floor); err != nil {
		t.Fatalf("service floor.json: %v", err)
	}
	for category, want := range floor {
		got := 0
		if v := byCategory[category]; v != nil {
			got = v.parsed
		}
		if got < want {
			t.Errorf("REGRESSED on the service's SQL: %s parses %d, floor is %d", category, got, want)
		}
	}
}

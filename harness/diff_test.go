package harness

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/calvinchengx/sqlglot-go/sqlglot"
)

// TestAgainstReference is the port's single most important test: every
// statement the Python reference parsed, parsed here and compared tree for
// tree.
//
// It does not fail on a coverage GAP -- an ErrUnsupported is expected while
// the port is being built, and is tallied. It fails on a DIVERGENCE: a
// statement the port parsed into a different tree than the reference. The
// distinction is the whole point. A gap is honest; a divergence is a bug in
// the one control the executor's safety rests on.
//
// It also fails if coverage REGRESSES below the committed floor, so the
// number only moves up.
func TestAgainstReference(t *testing.T) {
	// An alternate expectation directory, so the same comparison can be run
	// over statements a fuzz session found rather than only the committed
	// corpus. The committed corpus is the default and what CI measures; a
	// fuzz run is exploratory and writes its expectations to a temp dir.
	dir := os.Getenv("SQLGLOT_GO_EXPECTED")
	if dir == "" {
		dir = "../testdata/expected"
	}
	idx, cases, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	cov := &Coverage{Reference: idx.Reference, ByDialect: map[string]DialectCoverage{}}
	var divergences []string

	for _, c := range cases {
		name := c.Dialect
		if name == "" {
			name = "neutral"
		}
		dc := cov.ByDialect[name]
		dc.Total++
		cov.Total++

		tree, perr := sqlglot.ParseOne(c.SQL, c.Dialect)
		if perr != nil {
			dc.Unparsed++
			cov.ByDialect[name] = dc
			continue
		}
		if d := Diff(Normalise(c.Tree), Normalise(tree.Dump())); d != "" {
			dc.Mismatched++
			divergences = append(divergences,
				fmt.Sprintf("[%s] %s\n%s", name, truncate(c.SQL, 90), d))
		} else {
			dc.Matched++
			cov.Matched++
		}
		cov.ByDialect[name] = dc
	}

	// Only a run over the COMMITTED corpus records coverage or is held to the
	// floor. An exploratory run over a fuzz session's statements measures a
	// different population entirely: it would overwrite the recorded numbers
	// with figures nobody can reproduce, and trip a floor for dialects its
	// candidates happen not to include. Divergences still fail, which is the
	// whole reason to run it.
	exploratory := os.Getenv("SQLGLOT_GO_EXPECTED") != ""
	if !exploratory {
		if err := cov.Write("../testdata/coverage.json"); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("reference %s", idx.Reference[:12])
	for _, d := range cov.Dialects() {
		dc := cov.ByDialect[d]
		t.Logf("  %-10s %5.1f%%  matched %d / %d  (unparsed %d, mismatched %d)",
			d, dc.Percent(), dc.Matched, dc.Total, dc.Unparsed, dc.Mismatched)
	}

	for _, d := range divergences {
		t.Error(d)
	}
	if len(divergences) > 0 {
		t.Fatalf("%d divergence(s): the port parsed these into a DIFFERENT tree than the reference", len(divergences))
	}
	// The floor describes the committed corpus, not a fuzz session's.
	if !exploratory {
		assertNoRegression(t, cov)
	}
}

// assertNoRegression refuses a drop below the floor committed in
// testdata/floor.json. The floor is raised by hand as coverage grows; it is
// never lowered by a test.
func assertNoRegression(t *testing.T, cov *Coverage) {
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "floor.json"))
	if err != nil {
		t.Logf("no coverage floor yet (testdata/floor.json); not enforced")
		return
	}
	var floor map[string]float64
	if err := parseFloor(raw, &floor); err != nil {
		t.Fatalf("floor.json: %v", err)
	}
	for d, want := range floor {
		got := cov.ByDialect[d].Percent()
		if got+0.05 < want {
			t.Errorf("coverage REGRESSED for %s: %.1f%% < floor %.1f%%", d, got, want)
		}
	}
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

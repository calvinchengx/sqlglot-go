package sqlglot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

// Two properties that hold regardless of what the reference does, which is
// what makes them worth fuzzing here.
//
// Everything else about this port is verified by DIFFERENTIAL: the reference
// is the oracle, and matching it is the whole contract. A fuzzer cannot use
// that oracle -- it would need a Python round trip per input -- so it is
// pointed at the two things the reference cannot arbitrate.
//
// The tempting third property, "a tree written out reads back as the same
// tree", was tried and REMOVED. sqlglot does not have it: `SELECTa0000(0)A00`
// round-trips through the reference into a different tree, because the
// generator uppercases an anonymous function name that the parser stored as
// written. Asserting it here would report the reference's behaviour as the
// port's bug, and diverging to satisfy it would break the parity the port
// exists for. See the note on `joins` in docs, and the guard's
// refuseJoinWithoutFrom, for the case where that matters.

// A panic is not a refusal. This parser sits under a guard: whatever it cannot
// read must come back as an error, because a crash in the thing deciding
// whether a statement is safe is a denial of service in front of a database.
func FuzzParseOneNeverPanics(f *testing.F) {
	for _, s := range seeds {
		f.Add(s)
	}
	for _, s := range serviceSeeds() {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, sql string) {
		for _, dialect := range []string{"tsql", "postgres", "duckdb", "databricks"} {
			_, _ = ParseOne(sql, dialect)
		}
	})
}

// Whatever the generator writes, the parser must at least be able to READ.
//
// A DISCOVERY INSTRUMENT, not a regression gate, and deliberately not in CI.
// It found four real bugs -- Databricks quote escaping, TOP without its
// parentheses, parseTop refusing the TOP it writes, and `~ *` fusing into the
// `~*` operator -- and it will keep finding them. But it cannot be made green,
// because the property is STRONGER THAN THE REFERENCE'S OWN. Three times now a
// failure turned out to be sqlglot doing the same thing:
//
//	`SELECT 1 JOIN a` -> `SELECT 1, a`   the joined table moves into the
//	                                     select list; sqlglot writes it too
//	`SELECTa0000(0)A00`                  the name is uppercased on the way
//	                                     out, so the tree differs; likewise
//	`+Do` -> `Do`                        the unary plus is dropped by both,
//	                                     and a bare `Do` is a Command, not a
//	                                     column, in the reference as well
//
// Which is the argument for Tier 1.5's batched differential: only the oracle
// can say whether a divergence is this port's or sqlglot's, and a fuzzer
// cannot call it per input. Run this by hand, adjudicate each finding against
// the reference, and promote the real ones into the fixture corpus.
// Weaker than tree equality on purpose -- see above -- but not weaker than it
// looks: the guard emits this text to an engine, and output the port itself
// cannot re-read is output nobody has checked the meaning of.
func FuzzGeneratedSQLCanBeReadBack(f *testing.F) {
	for _, s := range seeds {
		f.Add(s)
	}
	for _, s := range serviceSeeds() {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, sql string) {
		for _, dialect := range []string{"tsql", "postgres", "duckdb", "databricks"} {
			tree, err := ParseOne(sql, dialect)
			if err != nil {
				continue
			}
			out, err := Generate(tree, dialect)
			if err != nil {
				continue // declining to write is documented behaviour
			}
			if _, err := ParseOne(out, dialect); err != nil {
				if collect(dialect, err, sql) {
					continue // collecting, not asserting; see collect
				}
				t.Fatalf("%s: wrote %q from %q and cannot read it back: %v",
					dialect, out, sql, err)
			}
		}
	})
}

var seeds = []string{
	"SELECT 1", "SELECT a FROM t WHERE b > 1", "WITH x AS (SELECT 1) SELECT * FROM x",
	"SELECT * FROM a JOIN b ON a.i = b.i", "SELECT CAST(a AS INT) FROM t",
	"SELECT a, COUNT(*) FROM t GROUP BY a HAVING COUNT(*) > 1",
	"SELECT TOP 5 a FROM t ORDER BY a DESC", "SELECT a FROM t UNION SELECT b FROM u",
	"SELECT a FROM t WHERE a IN (SELECT b FROM u)", "SELECT a FROM t WHERE a IS NOT NULL",
	"SELECT CASE WHEN a > 1 THEN 1 ELSE 2 END FROM t", "SELECT a FROM t1, t2",
	"SELECT 1 JOIN a", "SELECT a FROM t LIMIT 5", "SELECT DISTINCT a FROM t",
	"SET x = 1", "DROP TABLE t", "SELECT a[1:2]", "SELECT N'abc'", "((((",
}

// collect appends a failing input to the file named by DAS_FUZZ_COLLECT and
// reports that it did, so the run keeps going instead of stopping at the first
// finding.
//
// This is the cheap half of the batched differential. The expensive half --
// deciding whether a finding is this port's bug or the reference's behaviour --
// needs Python, and cannot run per input at a hundred thousand executions a
// second. So the fuzzer collects and `harness/adjudicate.py` judges, in bulk,
// afterwards. Without it a session stops on its first finding and every one
// after it stays hidden.
//
//	DAS_FUZZ_COLLECT=/tmp/candidates.txt \
//	  go test ./sqlglot/ -run=XXX -fuzz=FuzzGeneratedSQLCanBeReadBack -fuzztime=60s
//	python3 harness/adjudicate.py --sqlglot ~/opensource/sqlglot \
//	  --candidates /tmp/candidates.txt
func collect(dialect string, cause error, sql string) bool {
	path := os.Getenv("DAS_FUZZ_COLLECT")
	if path == "" {
		return false
	}
	collectMu.Lock()
	defer collectMu.Unlock()
	//nolint:gosec // G304: a path the developer running the fuzzer names in
	// DAS_FUZZ_COLLECT. Nothing in a candidate reaches it, and the collector
	// does nothing at all unless that variable is set.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return false // cannot collect, so fail loudly instead of silently
	}
	defer func() { _ = f.Close() }()
	// One line per candidate, and a statement with a newline in it would
	// forge a second: those are dropped rather than written wrong.
	if strings.ContainsAny(sql, "\n\r\t") {
		return true
	}
	// The adjudicator hands this to sqlglot, whose parser takes a str. An
	// input that is not valid UTF-8 cannot be given to the oracle faithfully,
	// so it cannot be judged -- and a candidate nobody can judge is noise in
	// the file. The fuzzer generates a great many of them.
	if !utf8.ValidString(sql) {
		return true
	}
	// The port's own error travels with the candidate. Without it the
	// adjudicator can say which findings are the port's but not how many
	// DISTINCT bugs they are, and a fuzz session produces hundreds of
	// variations of a handful of causes.
	why := strings.NewReplacer("\t", " ", "\n", " ", "\r", " ").Replace(cause.Error())
	_, _ = fmt.Fprintf(f, "%s\t%s\t%s\n", dialect, why, sql)
	return true
}

var collectMu sync.Mutex

// serviceSeeds are the statements the first consumer is actually held to.
//
// A fuzzer mutates what it is given, so what it is given decides where it
// looks. The hand-written seeds below are shapes; these are real queries --
// the evaluation suites' gold answers, the guard's permitted set, the
// adversarial corpus -- and a mutation of a real query lands somewhere a
// mutation of `SELECT 1` does not.
//
// Absent or unreadable, they are simply skipped: this is a seed list, and a
// fuzz target that refused to start because a corpus file moved would be
// worse than one exploring a little less.
func serviceSeeds() []string {
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "service", "corpus.json"))
	if err != nil {
		return nil
	}
	var corpus struct {
		Cases []struct {
			SQL string `json:"sql"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &corpus); err != nil {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(corpus.Cases))
	for _, c := range corpus.Cases {
		if c.SQL != "" && !seen[c.SQL] {
			seen[c.SQL] = true
			out = append(out, c.SQL)
		}
	}
	return out
}

// FuzzParsedStatementsForTheDifferential collects statements the port PARSES,
// so the reference can be asked whether it parsed them the SAME WAY.
//
// This is the port's central claim -- `mismatched: 0`, that it never reads a
// statement into a different tree than sqlglot does -- and until now it was
// verified only against a fixed corpus of 2,171. A construct no fixture
// contains has never been compared at all, which is precisely where the
// `IS NOT NULL` divergence was hiding.
//
// It asserts nothing on its own; it cannot, because the oracle is a Python
// process. It writes candidates, `harness/oracle.py --candidates` builds
// expectations for them, and the existing differential does the comparing:
//
//	DAS_FUZZ_COLLECT=/tmp/parsed.txt \
//	  go test ./sqlglot/ -run=XXX -fuzz=FuzzParsedStatementsForTheDifferential -fuzztime=30s
//	python3 harness/oracle.py --sqlglot ~/opensource/sqlglot \
//	  --candidates /tmp/parsed.txt --out /tmp/exp
//	SQLGLOT_GO_EXPECTED=/tmp/exp go test ./harness/ -run TestAgainstReference
//
// Capped, because a fuzzer parses millions of statements a minute and the
// oracle is the slow half: a hundred thousand candidates would take longer to
// judge than to find. `make fuzz-differential` runs the three steps.
func FuzzParsedStatementsForTheDifferential(f *testing.F) {
	for _, s := range seeds {
		f.Add(s)
	}
	for _, s := range serviceSeeds() {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, sql string) {
		for _, dialect := range []string{"tsql", "postgres", "duckdb", "databricks"} {
			if _, err := ParseOne(sql, dialect); err != nil {
				continue // a refusal is a gap, and gaps are already counted
			}
			collectParsed(dialect, sql)
		}
	})
}

const parsedCandidateCap = 4000

var parsedCollected int

// collectParsed writes a parsed statement out for the oracle to judge, up to a
// cap. Silently doing nothing without DAS_FUZZ_COLLECT is deliberate: this
// target is a collector, and a plain `go test` run should neither write files
// nor fail.
func collectParsed(dialect, sql string) {
	path := os.Getenv("DAS_FUZZ_COLLECT")
	if path == "" {
		return
	}
	if strings.ContainsAny(sql, "\n\r\t") || !utf8.ValidString(sql) {
		return // see collect: unwritable as a line, or unreadable by the oracle
	}
	collectMu.Lock()
	defer collectMu.Unlock()
	if parsedCollected >= parsedCandidateCap {
		return
	}
	//nolint:gosec // G304: as above -- DAS_FUZZ_COLLECT is the developer's own.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	if _, err := fmt.Fprintf(f, "%s\t%s\n", dialect, sql); err == nil {
		parsedCollected++
	}
}

package harness

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/calvinchengx/sqlglot-go/sqlglot"
)

// TestEmitExecutionPairs writes what the EXECUTION oracle needs: for every
// statement the port can read, the SQL that was written and the SQL the port
// writes back.
//
// It asserts nothing. Go cannot run DuckDB without cgo, and this repository's
// central promise is that a Go toolchain ALONE reproduces the differential on
// five platforms -- so the engine lives on the Python side, the same way the
// reference does, and this is the handover. See harness/execute.py.
//
// Run it through `make oracle-exec` rather than directly; on its own it is a
// no-op, because emitting is gated on the destination being named.
func TestEmitExecutionPairs(t *testing.T) {
	dest := os.Getenv("DAS_EXEC_EMIT")
	if dest == "" {
		t.Skip("set DAS_EXEC_EMIT to a path to emit; see make oracle-exec")
	}
	_, cases, err := Load("../testdata/expected")
	if err != nil {
		t.Fatal(err)
	}

	f, err := os.Create(dest) //nolint:gosec // G304: a path the operator names
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			t.Error(cerr)
		}
	}()

	// The dialects an engine exists for. DuckDB embeds; PostgreSQL needs a
	// server, which CI supplies and a developer may not -- the oracle skips
	// what it cannot reach rather than failing. Databricks has no local
	// engine at all, and the neutral dialect rewrites 8 statements out of
	// 991, so neither would earn its keep.
	engines := map[string]bool{"duckdb": true, "postgres": true}

	enc := json.NewEncoder(f)
	emitted := 0
	for _, c := range cases {
		if !engines[c.Dialect] {
			continue
		}
		rec := map[string]string{"dialect": c.Dialect, "sql": c.SQL, "reference": c.Rendered}
		tree, perr := sqlglot.ParseOne(c.SQL, c.Dialect)
		if perr == nil && samplesIn(tree) {
			rec["samples"] = "true"
		}
		switch {
		case perr != nil:
			rec["port_error"] = perr.Error()
		default:
			got, gerr := sqlglot.Generate(tree, c.Dialect)
			if gerr != nil {
				rec["port_error"] = gerr.Error()
			} else {
				rec["port"] = got
			}
			// The port's SIMPLIFIED form too. This is the first thing here
			// that rewrites a tree rather than reproducing one, so it is the
			// first that can be wrong in a way no string comparison sees: the
			// SQL still parses, still runs, and returns different rows.
			if s, serr := sqlglot.Generate(sqlglot.Simplify(tree, c.Dialect), c.Dialect); serr == nil {
				rec["simplified"] = s
			}
		}
		if err := enc.Encode(rec); err != nil {
			t.Fatal(err)
		}
		emitted++
	}
	t.Logf("emitted %d statements to %s", emitted, dest)
}

// samplesIn reports whether a statement samples, which leaves it unable to
// arbitrate anything.
//
// A sample is a RANDOM DRAW, so two runs of the SAME statement need not agree
// -- and a small one is stable often enough that the execution oracle's
// repeated looks cannot tell. `SELECT * FROM tbl USING SAMPLE 10%` returned
// the same rows three times and nothing on the fourth, so an unstable INPUT
// was reported as the port's rewrite being wrong when the rewrite was right.
// The tree says a statement samples, so it is recorded here rather than
// guessed at there.
func samplesIn(tree *sqlglot.Expression) bool {
	return len(tree.FindAll("TableSample")) > 0
}

package sqlglot

import "testing"

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
	f.Fuzz(func(t *testing.T, sql string) {
		for _, dialect := range []string{"tsql", "postgres", "duckdb", "databricks"} {
			_, _ = ParseOne(sql, dialect)
		}
	})
}

// KNOWN OPEN, found by this target and not yet fixed: `~ *` in T-SQL is
// written `~*`, which lexes back as the single `~*` operator. `unary` already
// spaces `- -5`, but only when the operand starts with the operator's own last
// character; the general rule is "space it if joining would form a longer
// token", which needs the keyword trie rather than a character comparison.
// This target is therefore NOT in CI yet -- a fuzz session will find it in
// seconds. Fix that and it can be.
//
// Whatever the generator writes, the parser must at least be able to READ.
// Weaker than tree equality on purpose -- see above -- but not weaker than it
// looks: the guard emits this text to an engine, and output the port itself
// cannot re-read is output nobody has checked the meaning of.
func FuzzGeneratedSQLCanBeReadBack(f *testing.F) {
	for _, s := range seeds {
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

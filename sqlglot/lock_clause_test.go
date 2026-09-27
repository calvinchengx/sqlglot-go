package sqlglot

import "testing"

// Row locks name the tables they cover and say whether the read waits.
// A parse or generate error fails the row. A lock with no wait word must
// come back without SKIP LOCKED, and a dialect that does not support the
// clause must refuse it rather than write the query without the lock.
func TestRowLocks(t *testing.T) {
	cases := [][3]string{
		{"mysql", "SELECT * FROM t1, t2 FOR SHARE OF t1, t2 SKIP LOCKED",
			"SELECT * FROM t1, t2 FOR SHARE OF t1, t2 SKIP LOCKED"},
		{"mysql", "SELECT * FROM t1, t2, t3 FOR SHARE OF t1 NOWAIT FOR UPDATE OF t2, t3 SKIP LOCKED",
			"SELECT * FROM t1, t2, t3 FOR SHARE OF t1 NOWAIT FOR UPDATE OF t2, t3 SKIP LOCKED"},
		{"mysql", "SELECT * FROM t FOR SHARE OF t",
			"SELECT * FROM t FOR SHARE OF t"},
		{"mysql", "SELECT * FROM t FOR UPDATE OF t NOWAIT",
			"SELECT * FROM t FOR UPDATE OF t NOWAIT"},
		{"mysql", "SELECT * FROM t FOR UPDATE WAIT 5",
			"SELECT * FROM t FOR UPDATE WAIT 5"},
		{"mysql", "SELECT * FROM t LOCK IN SHARE MODE",
			"SELECT * FROM t FOR SHARE"},
		{"postgres", "SELECT * FROM t1, t2 FOR SHARE OF t1, t2 SKIP LOCKED",
			"SELECT * FROM t1, t2 FOR SHARE OF t1, t2 SKIP LOCKED"},
		{"postgres", "SELECT * FROM t FOR SHARE OF t",
			"SELECT * FROM t FOR SHARE OF t"},
		{"postgres", "SELECT * FROM t FOR UPDATE WAIT 5",
			"SELECT * FROM t FOR UPDATE WAIT 5"},
	}
	written := 0
	for _, c := range cases {
		tree, err := ParseOne(c[1], c[0])
		if err != nil {
			t.Errorf("[%s] ParseOne(%q): %v", c[0], c[1], err)
			continue
		}
		got, err := Generate(tree, c[0])
		if err != nil {
			t.Errorf("[%s] Generate(%q): %v", c[0], c[1], err)
			continue
		}
		written++
		if got != c[2] {
			t.Errorf("[%s] %s\n  want %s\n  got  %s", c[0], c[1], c[2], got)
		}
	}
	if written != len(cases) {
		t.Errorf("wrote %d statements, want %d", written, len(cases))
	}

	tree, err := ParseOne("SELECT * FROM t1, t2 FOR SHARE OF t1, t2 SKIP LOCKED", "duckdb")
	if err != nil {
		t.Fatalf("duckdb ParseOne: %v", err)
	}
	if got, err := Generate(tree, "duckdb"); err == nil {
		t.Errorf("duckdb wrote %q for a lock it does not support", got)
	}
}

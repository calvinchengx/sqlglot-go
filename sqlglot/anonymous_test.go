package sqlglot

import "testing"

// A call's name is a plain string when it was written bare and an Identifier
// node when it was written quoted, because that is what the reference builds
// and the two are different trees. Writing the string for both lost the
// quoting: `"myfunc"()` came back as `MYFUNC()` where the reference writes
// `[MYFUNC]()`, and an empty quoted name vanished entirely.
func TestAQuotedCallNameKeepsItsQuoting(t *testing.T) {
	for _, tc := range []struct{ sql, want string }{
		{`myfunc()`, "MYFUNC()"},
		{`"myfunc"()`, "[MYFUNC]()"},
		{`[myfunc]()`, "[MYFUNC]()"},
		{`"myfunc"(1)`, "[MYFUNC](1)"},
		{`""()`, "[]()"},
	} {
		tree, err := ParseOne(tc.sql, "tsql")
		if err != nil {
			t.Errorf("%q: %v", tc.sql, err)
			continue
		}
		got, err := Generate(tree, "tsql")
		if err != nil {
			t.Errorf("%q: %v", tc.sql, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%q -> %q, want %q", tc.sql, got, tc.want)
		}
	}
}

// The reason the change above touched three readers at once.
//
// FunctionName feeds the guard's denied-call check. Recording the quoting as
// an Identifier node without teaching FunctionName to unwrap it would have
// stopped the guard seeing `[xp_cmdshell]('x')` -- a name it is specifically
// there to refuse, hidden by nothing more than a pair of brackets. The tree
// changed shape; what the guard asks of it must not.
func TestTheGuardStillSeesAQuotedCallByName(t *testing.T) {
	for _, sql := range []string{
		`xp_cmdshell('dir')`,
		`[xp_cmdshell]('dir')`,
		`"xp_cmdshell"('dir')`,
	} {
		tree, err := ParseOne(sql, "tsql")
		if err != nil {
			t.Fatalf("%q: %v", sql, err)
		}
		name, isFunc := FunctionName(tree, "tsql")
		if !isFunc || name != "xp_cmdshell" {
			t.Errorf("%q: FunctionName = %q, %v; the guard cannot refuse what it cannot name",
				sql, name, isFunc)
		}
	}
}

// `x IS NOT NULL` has two shapes and the dialect chooses, which the port did
// not know: it used PostgreSQL's everywhere, so the Go guard and the Python
// guard saw different trees for a predicate that appears in almost every real
// query. Semantically identical, and precisely the divergence this port exists
// to prevent.
//
// Found by promoting a fuzz candidate into the expectation corpus — the
// statement was not in 2,171 reference fixtures.
func TestIsNotNullShapeIsPerDialect(t *testing.T) {
	for _, tc := range []struct{ dialect, want, writes string }{
		{"", "Not", "SELECT * FROM t WHERE NOT a IS NULL"},
		{"tsql", "Not", "SELECT * FROM t WHERE NOT a IS NULL"},
		{"duckdb", "Not", "SELECT * FROM t WHERE NOT a IS NULL"},
		{"databricks", "Not", "SELECT * FROM t WHERE NOT a IS NULL"},
		{"postgres", "Is", "SELECT * FROM t WHERE a IS NOT NULL"},
	} {
		tree, err := ParseOne("SELECT * FROM t WHERE a IS NOT NULL", tc.dialect)
		if err != nil {
			t.Errorf("%s: %v", tc.dialect, err)
			continue
		}
		where, _ := tree.Args["where"].(*Expression)
		if where == nil {
			t.Errorf("%s: no WHERE clause", tc.dialect)
			continue
		}
		if got, _ := where.Args["this"].(*Expression); got == nil || got.Class != tc.want {
			t.Errorf("%s: predicate is %v, want %s", tc.dialect, got, tc.want)
		}
		out, err := Generate(tree, tc.dialect)
		if err != nil || out != tc.writes {
			t.Errorf("%s: wrote %q (%v), want %q", tc.dialect, out, err, tc.writes)
		}
	}
}

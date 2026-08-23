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

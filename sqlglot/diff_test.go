package sqlglot

import "testing"

// editString renders a DiffEdit the way the reference's own test file
// compares them, only by generated SQL rather than by full dump equality --
// terser to read and to write test tables against.
func editString(t *testing.T, e DiffEdit) string {
	t.Helper()
	g := func(x *Expression) string {
		if x == nil {
			return "-"
		}
		s, err := Generate(x, "")
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		return s
	}
	switch e.Kind {
	case EditInsert, EditRemove:
		return string(e.Kind) + "(" + g(e.Expression) + ")"
	default:
		return string(e.Kind) + "(" + g(e.Source) + " -> " + g(e.Target) + ")"
	}
}

// TestDiffEdgeCases is lifted from the reference's own tests/test_diff.py --
// the cases testdata/simplify.json's pairs happen not to exercise, either
// because nothing in that corpus reorders three siblings at once or because
// nothing in it calls an ANONYMOUS function (one this port has no dedicated
// node for, `"my.udf"(...)`), which is exactly the case that caught a real
// bug: isSameType compared an Anonymous call's own name with reflect.DeepEqual,
// which walks an *Expression's Parent pointer back into its tree and so
// compares unrelated ancestor context instead of the name itself -- every
// call ended up looking like a different function, wrongly replacing the
// whole call rather than the one argument that changed.
func TestDiffEdgeCases(t *testing.T) {
	for _, tc := range []struct {
		src, tgt string
		want     []string
	}{
		{"SELECT a, b, c", "SELECT c, a, b", []string{"Move(c -> c)"}},
		{"SELECT a + b", "SELECT b + a", []string{"Move(a -> a)"}},
		{"SELECT aaaa AND bbbb", "SELECT bbbb AND aaaa", []string{"Move(aaaa -> aaaa)"}},
		// The reference's own edit script duplicates these two -- once from
		// the identical-leaf-moved check, once from the containing OR's own
		// move-edit generation -- and its test suite compares as a set,
		// silently losing the duplicate. This port matches the reference
		// EXACTLY, duplicates included, so the list is written that way
		// rather than deduplicated for tidiness.
		{"SELECT aaaa OR bbbb OR cccc", "SELECT cccc OR bbbb OR aaaa",
			[]string{"Move(cccc -> cccc)", "Move(aaaa -> aaaa)", "Move(cccc -> cccc)", "Move(aaaa -> aaaa)"}},
		{`SELECT a, b, "my.udf1"()`, `SELECT a, b, "my.udf2"()`,
			[]string{`Remove("MY.UDF1"())`, `Insert("MY.UDF2"())`}},
		{`SELECT a, b, "my.udf"(x, y, z)`, `SELECT a, b, "my.udf"(x, y, w)`,
			[]string{"Remove(z)", "Insert(w)"}},
	} {
		source, err := ParseOne(tc.src, "")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.src, err)
		}
		target, err := ParseOne(tc.tgt, "")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.tgt, err)
		}
		edits := Diff(source, target, "", true)
		got := make([]string, len(edits))
		for i, e := range edits {
			got[i] = editString(t, e)
		}
		if !equalStrSlices(got, tc.want) {
			t.Errorf("Diff(%q, %q):\n got  %v\n want %v", tc.src, tc.tgt, got, tc.want)
		}
	}
}

func equalStrSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

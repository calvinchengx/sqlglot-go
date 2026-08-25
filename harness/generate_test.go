package harness

import (
	"testing"

	"github.com/calvinchengx/sqlglot-go/sqlglot"
)

// TestGenerateAgainstReference checks the other direction: a tree written back
// out as SQL, string for string against what the reference writes.
//
// The guard does not hand the engine the text it was given. It rewrites the
// tree -- injecting a row ceiling the caller did not ask for -- and emits the
// result, so an executor that emitted something subtly different from its
// counterpart would have the two engines running different queries while both
// believed they agreed. "Parses back" is not a strong enough test for that;
// the reference's own output is.
//
// Only statements the port already parses correctly are in scope. A statement
// it cannot read is a parser gap, counted there, not here.
func TestGenerateAgainstReference(t *testing.T) {
	idx, cases, err := Load("../testdata/expected")
	if err != nil {
		t.Fatal(err)
	}

	var written, refused, wrong, commented int
	var problems []string
	for _, c := range cases {
		// A comment is metadata the tree comparison already ignores, and the
		// port does not carry it. The reference writes comments back out, so
		// a statement that has one can never match here -- counted, not
		// silently skipped, because it is a real gap in the generator even
		// though nothing the guard emits depends on it.
		if carriesComments(c.Tree) {
			commented++
			continue
		}
		tree, perr := sqlglot.ParseOne(c.SQL, c.Dialect)
		if perr != nil {
			continue
		}
		if d := Diff(Normalise(c.Tree), Normalise(tree.Dump())); d != "" {
			continue // a parser divergence; TestAgainstReference owns it
		}

		got, gerr := sqlglot.Generate(tree, c.Dialect)
		switch {
		case gerr != nil:
			refused++
			if len(problems) < 10 {
				problems = append(problems,
					"  refused: ["+c.Dialect+"] "+c.SQL+"\n      "+gerr.Error())
			}
		case got != c.Rendered:
			wrong++
			problems = append(problems,
				"WRONG: ["+c.Dialect+"] "+c.SQL+"\n    want "+c.Rendered+"\n    got  "+got)
		default:
			written++
		}
	}

	t.Logf("reference %s: %d parsed statements written back identically, %d refused, %d wrong",
		idx.Reference[:12], written, refused, wrong)
	t.Logf("  %d more carry comments, which the port does not reproduce", commented)
	for _, p := range problems {
		if wrong > 0 {
			t.Error(p)
		} else {
			t.Log(p)
		}
	}
	if wrong > 0 {
		t.Fatalf("%d statement(s) written differently from the reference", wrong)
	}
	assertGeneratorFloor(t, written)
}

// carriesComments reports whether the reference attached a comment anywhere
// in the tree; the dump records them under "o".
func carriesComments(tree []map[string]any) bool {
	for _, rec := range tree {
		if _, ok := rec["o"]; ok {
			return true
		}
	}
	return false
}

func assertGeneratorFloor(t *testing.T, written int) {
	t.Helper()
	const floor = 2905 // raised by hand as the generator grows; never lowered here
	if written < floor {
		t.Errorf("generator REGRESSED: %d statements written, floor %d", written, floor)
	}
}

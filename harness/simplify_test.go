package harness

import (
	"encoding/json"
	"os"
	"sort"
	"testing"

	"github.com/calvinchengx/sqlglot-go/sqlglot"
)

type simplifyPairs struct {
	Reference string `json:"reference"`
	Pairs     []struct {
		Dialect  string `json:"dialect"`
		SQL      string `json:"sql"`
		Expected string `json:"expected"`
	} `json:"pairs"`
}

// TestSimplifyAgainstReference measures the port's optimizer against
// sqlglot's own contract: tests/fixtures/optimizer/simplify.sql, which pins
// what each statement becomes.
//
// It counts EXACT matches and holds a floor. It does NOT try to decide whether
// a non-exact result is wrong, and that restraint is deliberate. A first cut
// guessed -- "if the output differs from the input, the port must have folded
// it wrongly" -- and reported 35 failures of which nearly all were partial
// folds: correct as far as they went, simply not finished. The one genuine bug
// in that batch (`NOT (2 <> ALL S)` folded to `2 = ALL S`, which is a
// different question) was buried among them.
//
// Whether a rewrite is WRONG is a semantic question, and this repository has
// exactly one harness that can answer it: the execution oracle runs the
// original and the rewrite on a real engine and compares the rows. Simplify
// output is fed to it, so a rewrite that changes an answer fails there --
// where the failure means something -- rather than being guessed at here.
func TestSimplifyAgainstReference(t *testing.T) {
	raw, err := os.ReadFile("../testdata/simplify.json")
	if err != nil {
		t.Fatal(err)
	}
	var data simplifyPairs
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}

	var exact, notYet, unreadable, reparsed int
	reasons := map[string]int{}
	var problems []string
	for _, p := range data.Pairs {
		tree, perr := sqlglot.ParseOne(p.SQL, p.Dialect)
		if perr != nil {
			unreadable++
			continue
		}
		simplified := sqlglot.Simplify(tree, p.Dialect)
		got, gerr := sqlglot.Generate(simplified, p.Dialect)
		if gerr != nil {
			unreadable++
			continue
		}
		// A rewrite must SURVIVE being written down. If simplify drops a pair
		// of parentheses the statement needed, the SQL still parses -- into a
		// different tree, asking a different question. Reading it back and
		// comparing catches that with no engine at all, which matters because
		// most of this contract is bare predicates over undefined columns:
		// they can never reach the execution oracle.
		//
		// It caught `A AND (A OR B)` being flattened to `A AND A OR B`, which
		// re-associates to `(A AND A) OR B`.
		if back, berr := sqlglot.ParseOne(got, p.Dialect); berr == nil {
			if !sameMeaning(back, simplified) {
				reparsed++
				if len(problems) < 8 {
					problems = append(problems, "REWRITE DOES NOT SURVIVE BEING WRITTEN: ["+
						p.Dialect+"] "+p.SQL+"\n    wrote "+got+
						"\n    which reads back as a different tree")
				}
			}
		}

		if got == p.Expected {
			exact++
			continue
		}
		notYet++
		reasons[p.Expected]++
	}

	t.Logf("reference %s: %d of %d simplified exactly, %d not folded that far",
		data.Reference[:12], exact, len(data.Pairs), notYet)
	t.Logf("  %d could not be read or written back at all", unreadable)
	for _, pr := range problems {
		t.Error(pr)
	}
	if reparsed > 0 {
		t.Fatalf("%d rewrite(s) do not survive being written down: the SQL parses, "+
			"into a DIFFERENT tree", reparsed)
	}
	assertSimplifyFloor(t, exact)

	// The most common thing the port declines to fold, so the next rule is
	// chosen by what actually blocks the contract.
	if testing.Verbose() {
		keys := make([]string, 0, len(reasons))
		for k := range reasons {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			if reasons[keys[i]] != reasons[keys[j]] {
				return reasons[keys[i]] > reasons[keys[j]]
			}
			return keys[i] < keys[j]
		})
		for i, k := range keys {
			if i >= 15 {
				break
			}
			t.Logf("  not folded (%d): -> %s", reasons[k], truncate(k, 60))
		}
	}
}

func assertSimplifyFloor(t *testing.T, simplified int) {
	t.Helper()
	const floor = 286 // raised by hand as the optimizer grows; never lowered here
	if simplified < floor {
		t.Errorf("simplify REGRESSED: %d exact, floor %d", simplified, floor)
	}
}

// sameMeaning compares two trees up to the ASSOCIATIVITY of AND and OR.
//
// A strict tree comparison is too strict here: `A AND (B AND C)` written down
// and read back becomes `(A AND B) AND C`, which nests differently and asks
// the same question. Flattening each connector chain to its operand sequence
// keeps that reshape equal -- while the bug this check exists for still
// shows, because re-associating ACROSS two different operators changes the
// sequence and usually the root: `A AND (A OR B)` flattened is
// And[A, Or[A, B]], and the mis-written `A AND A OR B` reads back as
// Or[And[A, A], B].
func sameMeaning(a, b *sqlglot.Expression) bool {
	return flattenConnectors(a).Equal(flattenConnectors(b))
}

// flattenConnectors rewrites every AND/OR chain into one right-nested chain of
// its operands, so two spellings of the same associative chain agree.
func flattenConnectors(e *sqlglot.Expression) *sqlglot.Expression {
	return normalise(e, true)
}

// normalise rebuilds a tree in a canonical form. `asIf` says whether a bare
// If may be rewritten into the Case it is written as -- false for the If
// nodes that ARE a Case's branches, which are already where they belong.
func normalise(e *sqlglot.Expression, asIf bool) *sqlglot.Expression {
	if e == nil {
		return nil
	}
	// Rebuilt without the nil-valued args, so two spellings of the same node
	// compare equal whatever optional slots each happens to carry.
	out := sqlglot.New(e.Class)
	out.Type = e.Type
	for _, key := range e.Keys {
		switch v := e.Args[key].(type) {
		case *sqlglot.Expression:
			if v != nil {
				out.Set(key, normalise(v, true))
			}
		case []*sqlglot.Expression:
			if len(v) > 0 {
				kids := make([]*sqlglot.Expression, len(v))
				for i, k := range v {
					kids[i] = normalise(k, key != "ifs")
				}
				out.Set(key, kids)
			}
		case nil:
		default:
			out.Set(key, v)
		}
	}
	// A Subquery wrapping a query is parenthesisation, not meaning. The
	// reference itself does not round-trip through it: simplify produces
	// Any(Select), writes ANY(SELECT ...), and reads that back as
	// Any(Subquery(Select)). The port reproduces the reference exactly there,
	// so the comparison has to see past the wrapper rather than report the
	// reference's own asymmetry as the port's bug.
	if out.Class == "Subquery" {
		if inner, ok := out.Args["this"].(*sqlglot.Expression); ok && inner != nil {
			if _, hasAlias := out.Args["alias"].(*sqlglot.Expression); !hasAlias {
				switch inner.Class {
				case "Select", "Union", "Intersect", "Except":
					return inner
				}
			}
		}
	}
	// A TS_OR_DS_TO_DATE and the CAST it is written as are the same thing
	// spelled two ways: the node says "read a date out of this", and outside
	// the dialects that have a call for it that is exactly what the cast
	// says. The reference does not round-trip it either -- it writes
	// `CAST(x AS DATE)` and reads back a Cast -- so the comparison sees past
	// it rather than reporting the reference's own asymmetry as the port's.
	if inner := readsADate(out); inner != nil {
		// A date read out of something that is a date already is that thing:
		// the writer collapses the pair, so the comparison collapses it too.
		for {
			deeper := readsADate(inner)
			if deeper == nil {
				break
			}
			inner = deeper
		}
		return sqlglot.New("TsOrDsToDate", sqlglot.Arg{Key: "this", Value: inner})
	}
	// An IF and the CASE it is written as are the same thing spelled two
	// ways: most dialects have no IF, so `IF(FALSE, x)` is written
	// `CASE WHEN FALSE THEN x END` and reads back as a Case. The reference
	// does not round-trip that either, so the comparison sees past it.
	if out.Class == "If" && asIf {
		branch := sqlglot.New("If",
			sqlglot.Arg{Key: "this", Value: out.Args["this"]},
			sqlglot.Arg{Key: "true", Value: out.Args["true"]})
		args := []sqlglot.Arg{{Key: "ifs", Value: []*sqlglot.Expression{branch}}}
		if fallback, ok := out.Args["false"].(*sqlglot.Expression); ok && fallback != nil {
			args = append(args, sqlglot.Arg{Key: "default", Value: fallback})
		}
		return sqlglot.New("Case", args...)
	}
	if out.Class != "And" && out.Class != "Or" {
		return out
	}
	operands := connectorOperands(out, out.Class)
	rebuilt := operands[len(operands)-1]
	for i := len(operands) - 2; i >= 0; i-- {
		rebuilt = sqlglot.New(out.Class,
			sqlglot.Arg{Key: "this", Value: operands[i]},
			sqlglot.Arg{Key: "expression", Value: rebuilt})
	}
	return rebuilt
}

// readsADate returns what a node reads a date out of, where the node says
// only that -- a formatless TS_OR_DS_TO_DATE, or the CAST it is written as.
// Both become one canonical shape so the two compare equal.
func readsADate(e *sqlglot.Expression) *sqlglot.Expression {
	inner, _ := e.Args["this"].(*sqlglot.Expression)
	if inner == nil {
		return nil
	}
	switch e.Class {
	case "TsOrDsToDate":
		if _, hasFormat := e.Args["format"].(*sqlglot.Expression); hasFormat {
			return nil
		}
		return inner
	case "Cast":
		to, _ := e.Args["to"].(*sqlglot.Expression)
		if to == nil || to.Class != "DataType" {
			return nil
		}
		if kind, _ := to.Args["this"].(sqlglot.DataTypeKind); string(kind) == "DATE" {
			return inner
		}
	}
	return nil
}

func connectorOperands(e *sqlglot.Expression, class string) []*sqlglot.Expression {
	if e == nil || e.Class != class {
		return []*sqlglot.Expression{e}
	}
	this, _ := e.Args["this"].(*sqlglot.Expression)
	expr, _ := e.Args["expression"].(*sqlglot.Expression)
	return append(connectorOperands(this, class), connectorOperands(expr, class)...)
}

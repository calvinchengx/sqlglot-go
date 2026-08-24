package sqlglot

import "strconv"

// Simplify rewrites a tree the way the reference's optimizer does.
//
// This is the first thing in the port that CHANGES a tree rather than
// reproducing one. That distinction is the reason the execution oracle exists:
// every other harness here compares the port against sqlglot, which cannot
// tell a wrong-but-plausible rewrite from a right one -- the SQL still parses,
// still round-trips, and still runs. It just returns different rows.
//
// The rule is conservative throughout: where a rule's precondition cannot be
// established, the node is LEFT ALONE. A rewrite the port declines to make
// shows up as a statement it did not simplify far enough, which the gate
// counts and which costs nothing. A rewrite it makes wrongly is a different
// question asked of the database.
//
// The reference runs `annotate_types` before simplifying and several of its
// rules decide on the types that pass leaves behind. Those rules are not here
// yet, and the nodes they would touch are left alone rather than guessed at.
func Simplify(e *Expression, dialect string) *Expression {
	if e == nil {
		return nil
	}
	// The reference applies its rules until nothing changes rather than in one
	// pass: folding `1 + 1` to `2` is what lets the comparison above it fold in
	// turn. The bound is a safety net, not an expectation -- a rule that keeps
	// finding work is a rule that is oscillating, and looping forever inside a
	// database guard is worse than stopping early.
	for i := 0; i < 32; i++ {
		out := simplifyNode(e, nil, dialect)
		if out.Equal(e) {
			return out
		}
		e = out
	}
	return e
}

// simplifyNode rewrites children first, then the node itself: a rule that
// folds `x AND TRUE` can only see the TRUE once whatever produced it has run.
func simplifyNode(e, parent *Expression, dialect string) *Expression {
	if e == nil {
		return nil
	}
	out := e.Copy()
	for key, arg := range out.Args {
		switch v := arg.(type) {
		case *Expression:
			out.Set(key, simplifyNode(v, out, dialect))
		case []*Expression:
			kids := make([]*Expression, len(v))
			for i, k := range v {
				kids[i] = simplifyNode(k, out, dialect)
			}
			out.Set(key, kids)
		}
	}
	out = simplifyNot(out, dialect)
	out = simplifyConnectors(out)
	out = simplifyParens(out, parent)
	out = sortComparison(out)
	return out
}

func isA(base string, e *Expression) bool {
	return e != nil && classIsA[base][e.Class]
}

// simplifyParens drops parentheses the reference would not write.
//
// The reference's own conditions are wider than this, and porting them
// literally was unsound HERE. Its generator re-inserts whatever parentheses
// precedence requires; this port's does not, so dropping a pair the reference
// would drop produced SQL that no longer parses -- `INTERVAL (13 - x) DAY`
// became `INTERVAL 13 - x DAY`, and `~(-1)` became `~-1`, which is not an
// operator. Every one of those was caught by the execution oracle, and none
// of them could have been caught by comparing strings against sqlglot,
// because sqlglot is not asked to simplify those statements at all.
//
// So the rule is narrowed to the case the port can be sure of: a condition
// inside a connector, where the parentheses carry no precedence the writer
// will not restore. Everything else keeps them. An extra pair is noise; a
// missing one is a different statement.
func simplifyParens(e, parent *Expression) *Expression {
	if e.Class != "Paren" {
		return e
	}
	this, _ := e.Args["this"].(*Expression)
	if this == nil {
		return e
	}
	if parent != nil && !isA("Connector", parent) && parent.Class != "Not" {
		return e
	}
	switch {
	case isA("Connector", this), isA("Predicate", this):
	case this.Class == "Boolean", this.Class == "Null", this.Class == "Paren":
	default:
		return e
	}
	return this
}

// inverseComparison is what swapping a comparison's operands does to it.
var inverseComparison = map[string]string{
	"LT": "GT", "GT": "LT", "LTE": "GTE", "GTE": "LTE", "EQ": "EQ", "NEQ": "NEQ",
}

// sortComparison puts the column on the left and the constant on the right:
// the reference writes `x <= 1`, never `1 >= x`. Without this the complement
// rule above produces a comparison that is correct and differently spelled,
// which the contract counts as wrong.
func sortComparison(e *Expression) *Expression {
	to, ok := inverseComparison[e.Class]
	if !ok {
		return e
	}
	left, _ := e.Args["this"].(*Expression)
	right, _ := e.Args["expression"].(*Expression)
	if left == nil || right == nil || len(e.Keys) != 2 {
		return e
	}
	lCol, rCol := left.Class == "Column", right.Class == "Column"
	lConst, rConst := isConstant(left), isConstant(right)
	// A subquery predicate stays on the right. Dropping this guard turned
	// `NOT (2 <> ALL (SELECT …))` into `ALL (SELECT …) = 2`, which is not the
	// same question -- the quantifier has to govern the comparison.
	if (lCol && !rCol) || (rConst && !lConst) || isA("SubqueryPredicate", right) {
		return e
	}
	if (rCol && !lCol) || (lConst && !rConst) {
		return New(to, Arg{"this", right}, Arg{"expression", left})
	}
	return e
}

// isConstant is the reference's `_is_constant`: a literal, a boolean or NULL.
func isConstant(e *Expression) bool {
	if e == nil {
		return false
	}
	switch e.Class {
	case "Literal", "Boolean", "Null":
		return true
	}
	return false
}

// The reference's own predicates, by the names it gives them. Kept separate
// and tiny because nearly every rule below is built out of them, and getting
// one subtly wrong would be a rewrite that changes an answer.
func isNull(e *Expression) bool  { return e != nil && e.Class == "Null" }
func isFalse(e *Expression) bool { return isBooleanLiteral(e, false) }

func isBooleanLiteral(e *Expression, want bool) bool {
	if e == nil || e.Class != "Boolean" {
		return false
	}
	v, _ := e.Args["this"].(bool)
	return v == want
}

// alwaysTrue is not "is TRUE": a non-zero NUMBER is true too, which is why
// `1 AND TRUE` folds to TRUE.
func alwaysTrue(e *Expression) bool {
	return isBooleanLiteral(e, true) || (isNumberLiteral(e) && !isZero(e))
}

func alwaysFalse(e *Expression) bool { return isFalse(e) || isNull(e) || isZero(e) }

func isNumberLiteral(e *Expression) bool {
	if e == nil || e.Class != "Literal" {
		return false
	}
	str, _ := e.Args["is_string"].(bool)
	return !str
}

func isZero(e *Expression) bool {
	if !isNumberLiteral(e) {
		return false
	}
	text, _ := e.Args["this"].(string)
	n, err := strconv.ParseFloat(text, 64)
	return err == nil && n == 0
}

func boolLit(v bool) *Expression { return New("Boolean", Arg{"this", v}) }

// complementComparison is what NOT does to a comparison: the reference turns
// `NOT a = b` into `a <> b` rather than keeping the negation.
// complementQuantifier flips the quantifier a negated comparison governs.
var complementQuantifier = map[string]string{"All": "Any", "Any": "All"}

var complementComparison = map[string]string{
	"LT": "GTE", "GT": "LTE", "LTE": "GT", "GTE": "LT", "EQ": "NEQ", "NEQ": "EQ",
}

// booleanClasses are the nodes the reference's annotator types as BOOLEAN
// without needing a schema. It matters because eliminating a connector down to
// a single operand is only safe when that operand is a condition: reducing
// `x AND x` to a bare column would change a predicate into a value, so the
// reference appends `AND TRUE` to keep it one.
//
// This is the type annotator's job, and the annotator is not ported. What is
// here is the part that needs no schema -- a comparison is boolean whatever
// its operands are -- and it is deliberately CONSERVATIVE: a node not listed
// is treated as not-known-boolean, which is what the reference does with a
// node it could not annotate either.
var booleanClasses = map[string]bool{
	"And": true, "Or": true, "Not": true, "Boolean": true,
	"EQ": true, "NEQ": true, "GT": true, "GTE": true, "LT": true, "LTE": true,
	"Is": true, "In": true, "Like": true, "ILike": true, "Between": true,
	"Exists": true, "RegexpLike": true, "NullSafeEQ": true, "NullSafeNEQ": true,
}

func isKnownBoolean(e *Expression) bool { return e != nil && booleanClasses[e.Class] }

// safeToEliminateDoubleNegation is the dialect's own answer. It is true in
// every dialect this port configures, which is exactly why it is asked rather
// than assumed: a rule that happens to hold everywhere today is not the same
// as a rule with no condition on it.
func safeToEliminateDoubleNegation(dialect string) bool {
	t := parserTables[dialect]
	return t != nil && t.SafeToEliminateDoubleNegation
}

// simplifyNot folds a negation whose operand is already decided.
//
// De Morgan over a parenthesised connector is the reference's headline case
// here and is NOT ported yet: it rewrites structure rather than folding a
// constant, and the nodes it would touch are left alone.
func simplifyNot(e *Expression, dialect string) *Expression {
	if e.Class != "Not" {
		return e
	}
	this, _ := e.Args["this"].(*Expression)
	if this == nil {
		return e
	}
	if to, ok := complementComparison[this.Class]; ok {
		left, _ := this.Args["this"].(*Expression)
		right, _ := this.Args["expression"].(*Expression)
		if left == nil || right == nil || len(this.Keys) != 2 {
			return e
		}
		// A quantifier on the right has to flip with the comparison:
		// `NOT (2 <> ALL S)` is `2 = ANY S`, not `2 = ALL S`. Flipping only
		// the comparison asked the database a different question -- caught by
		// the contract, which is what it is for.
		if flipped, isQuantifier := complementQuantifier[right.Class]; isQuantifier {
			inner, _ := right.Args["this"].(*Expression)
			if inner == nil {
				return e
			}
			right = New(flipped, Arg{"this", inner})
		}
		return New(to, Arg{"this", left}, Arg{"expression", right})
	}
	if alwaysTrue(this) {
		return boolLit(false)
	}
	if isFalse(this) {
		return boolLit(true)
	}
	// NOT NOT x collapses only where the dialect says it is safe AND x is
	// known to be a boolean -- over a nullable non-boolean the two negations
	// are not the identity.
	if this.Class == "Not" && safeToEliminateDoubleNegation(dialect) {
		inner, _ := this.Args["this"].(*Expression)
		if isKnownBoolean(inner) {
			return inner
		}
	}
	return e
}

// simplifyConnectors folds an AND or an OR whose operands are decided.
func simplifyConnectors(e *Expression) *Expression {
	if e.Class != "And" && e.Class != "Or" {
		return e
	}
	left, _ := e.Args["this"].(*Expression)
	right, _ := e.Args["expression"].(*Expression)
	if left == nil || right == nil {
		return e
	}

	var folded *Expression
	if e.Class == "And" {
		switch {
		case isFalse(left) || isFalse(right), isZero(left) || isZero(right):
			folded = boolLit(false)
		case isNull(left) && isNull(right),
			isNull(left) && alwaysTrue(right),
			alwaysTrue(left) && isNull(right):
			folded = New("Null")
		case alwaysTrue(left) && alwaysTrue(right):
			folded = boolLit(true)
		case alwaysTrue(left):
			folded = right
		case alwaysTrue(right):
			folded = left
		}
	} else {
		switch {
		case alwaysTrue(left) || alwaysTrue(right):
			folded = boolLit(true)
		case isNull(left) && isNull(right),
			isNull(left) && alwaysFalse(right),
			alwaysFalse(left) && isNull(right):
			folded = New("Null")
		case isFalse(left):
			folded = right
		case isFalse(right):
			folded = left
		}
	}
	if folded == nil {
		return e
	}
	// Reducing a connector to a single operand can turn a CONDITION into a
	// value -- `x AND TRUE` down to a bare column `x`. The reference keeps it
	// a condition by putting the TRUE back, which is why the contract says
	// `x AND x` becomes `x AND TRUE` rather than `x`.
	if folded.Class != "And" && folded.Class != "Or" && folded.Class != "Boolean" &&
		!isKnownBoolean(folded) {
		return New("And", Arg{"this", folded}, Arg{"expression", boolLit(true)})
	}
	return folded
}

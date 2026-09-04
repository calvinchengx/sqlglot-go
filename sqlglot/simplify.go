package sqlglot

import (
	"math"
	"strconv"
	"strings"
)

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
//
// The node is copied ONE LEVEL deep, not wholly. Every child is replaced by
// its own simplified copy on the next line, so a deep copy here duplicates
// the entire subtree and then throws it away -- once per node, which over a
// chain of n operators is n^2 nodes copied per pass and up to 32 passes of
// it. `A[0*0*0*...]` with two thousand terms took three seconds to parse,
// nearly all of it garbage collection. The fuzzer found it as a worker that
// stopped responding rather than as a wrong answer.
//
// Nothing is shared with the input: a leaf is copied too, and the only values
// carried over are scalars.
func simplifyNode(e, parent *Expression, dialect string) *Expression {
	if e == nil {
		return nil
	}
	out := e.shallowCopy()
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
	out = simplifyLiterals(out, parent)
	out = simplifyCoalesce(out, parent)
	out = simplifyNot(out, parent, dialect)
	out = absorb(out, parent)
	out = simplifyConnectors(out, parent)
	out = simplifyParens(out, parent)
	out = sortComparison(out)
	out = simplifyEquality(out)
	out = simplifyStartsWith(out)
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
	case parent == nil:
		// Parentheses around the whole statement carry no precedence.
	case isA("Connector", this):
		// `A AND (A OR B)` is NOT `A AND A OR B`: AND binds tighter, so
		// dropping the pair re-associates the statement into `(A AND A) OR B`
		// and asks a different question. Only a connector nested inside the
		// SAME connector is associative and safe to flatten.
		if this.Class != parent.Class {
			return e
		}
	case isA("Predicate", this):
		// A comparison binds tighter than any connector and than NOT, so its
		// parentheses are decoration.
	case this.Class == "Boolean", this.Class == "Null", this.Class == "Paren":
	default:
		return e
	}
	return this
}

// nullOK are the comparisons that TOLERATE a null operand, so folding one
// away would change what they answer.
var nullOK = map[string]bool{"NullSafeEQ": true, "NullSafeNEQ": true, "PropertyEQ": true}

// simplifyLiterals folds an operator whose operands are both constants:
// `1 + 1` to `2`, `2 > 2.5` to FALSE, `1 IS NULL` to FALSE.
//
// Dates and intervals are the reference's other half of this rule and are not
// here: folding them means doing calendar arithmetic, and a port that got that
// subtly wrong would return the wrong rows rather than the wrong spelling.
func simplifyLiterals(e, parent *Expression) *Expression {
	// Double negation of a value, not of a boolean: `--500` is `500`.
	// The boolean case (`NOT NOT x`) is simplifyNot, and needs a type.
	if e.Class == "Neg" {
		this := childOf(e, "this")
		if this != nil && this.Class == "Neg" {
			return childOf(this, "this")
		}
		return e
	}
	if !isA("Binary", e) || isA("Connector", e) || nullOK[e.Class] {
		return e
	}
	a, _ := e.Args["this"].(*Expression)
	b, _ := e.Args["expression"].(*Expression)
	if a == nil || b == nil || len(e.Keys) < 2 {
		return e
	}

	if e.Class == "Is" {
		return simplifyIs(e, a, b)
	}
	// The two ASSOCIATIVE operators fold across a whole chain rather than one
	// pair at a time. `a[CAST(x AS INT)]` is read as `CAST(x AS INT) + -1`
	// and written as `(CAST(x AS INT) + -1) + 1`, and only a rule that can
	// see past the inner Add turns that back into `+ 0` -- without it the
	// generator declined the shift altogether and wrote a subscript one
	// element lower than the one it read.
	//
	// Only Add and Mul. The reference folds a Sub or a Div solely where both
	// operands hang off the SAME node, because neither is associative:
	// `y - 2 + 3` is not `y + 1`, and a chain that lost track of which
	// operator separated which pair would say it was.
	if e.Class == "Add" || e.Class == "Mul" {
		if parent != nil && parent.Class == e.Class {
			return e
		}
		return flatFold(e)
	}
	// A comparison sees THROUGH a widening cast of a small integer:
	// `CAST(1 AS UINT) >= 0` is `1 >= 0`, which then folds to TRUE. Only
	// byte-sized values, and only widening -- a narrowing cast can overflow,
	// and what an engine does then is its own business.
	if comparisons[e.Class] {
		a = withoutWideningCast(a)
		b = withoutWideningCast(b)
	}
	if isNumberLiteral(a) && isNumberLiteral(b) {
		if out := foldNumbers(e, a, b); out != nil {
			return out
		}
		return e
	}
	if isStringLiteral(a) && isStringLiteral(b) {
		x, _ := a.Args["this"].(string)
		y, _ := b.Args["this"].(string)
		if out := evalBooleanString(e.Class, x, y); out != nil {
			return out
		}
	}
	return e
}

// simplifyIs folds `<constant> IS [NOT] NULL`. Nothing else: whether a COLUMN
// is null is not knowable here.
func simplifyIs(e, a, b *Expression) *Expression {
	not := false
	c := b
	if b.Class == "Not" {
		c, _ = b.Args["this"].(*Expression)
		not = true
	}
	if negate, _ := e.Args["negate"].(bool); negate {
		not = !not
	}
	if c == nil || !isNull(c) {
		return e
	}
	switch {
	case a.Class == "Literal", a.Class == "Boolean":
		return boolLit(not)
	case isNull(a):
		return boolLit(!not)
	}
	return e
}

func foldNumbers(e, a, b *Expression) *Expression {
	x, okA := numberOf(a)
	y, okB := numberOf(b)
	if !okA || !okB {
		return nil
	}
	bothInt := isIntegerLiteral(a) && isIntegerLiteral(b)
	// A result too big to be a float is not a number this can write down:
	// `1E70 * 1E300` overflows, and the literal that came out of it --
	// `+Inf.0` -- was SQL nothing could read. The reference declines to fold
	// these too. The generator fuzzer found it.
	folded := func(v float64) *Expression {
		if math.IsInf(v, 0) || math.IsNaN(v) {
			return nil
		}
		return numberLit(v, bothInt)
	}
	switch e.Class {
	case "Add":
		return folded(x + y)
	case "Mul":
		return folded(x * y)
	case "Sub":
		return folded(x - y)
	case "Div":
		// Integer division differs between engines, so the reference declines
		// to fold it rather than pick one engine's answer.
		if bothInt || y == 0 {
			return nil
		}
		if q := x / y; !math.IsInf(q, 0) && !math.IsNaN(q) {
			return numberLit(q, false)
		}
		return nil
	}
	return evalBooleanNumber(e.Class, x, y)
}

func evalBooleanNumber(class string, a, b float64) *Expression {
	switch class {
	case "EQ":
		return boolLit(a == b)
	case "NEQ":
		return boolLit(a != b)
	case "GT":
		return boolLit(a > b)
	case "GTE":
		return boolLit(a >= b)
	case "LT":
		return boolLit(a < b)
	case "LTE":
		return boolLit(a <= b)
	}
	return nil
}

func evalBooleanString(class, a, b string) *Expression {
	switch class {
	case "EQ":
		return boolLit(a == b)
	case "NEQ":
		return boolLit(a != b)
	case "GT":
		return boolLit(a > b)
	case "GTE":
		return boolLit(a >= b)
	case "LT":
		return boolLit(a < b)
	case "LTE":
		return boolLit(a <= b)
	}
	return nil
}

func numberOf(e *Expression) (float64, bool) {
	if e.Class == "Neg" {
		n, ok := numberOf(childOf(e, "this"))
		return -n, ok
	}
	text, _ := e.Args["this"].(string)
	n, err := strconv.ParseFloat(text, 64)
	return n, err == nil
}

func isIntegerLiteral(e *Expression) bool {
	if e.Class == "Neg" {
		return isIntegerLiteral(childOf(e, "this"))
	}
	text, _ := e.Args["this"].(string)
	_, err := strconv.ParseInt(text, 10, 64)
	return err == nil
}

// numberLit writes a folded number the way the reference does. Python's str()
// keeps a float's point -- 2.0 stays "2.0" -- and Go's shortest formatting
// drops it, which would write a different literal than the reference for every
// float that happens to be whole.
func numberLit(v float64, integral bool) *Expression {
	// A negative result is Neg(Literal) in the reference, not a Literal whose
	// text begins with a minus -- that is what parsing `-1` produces, and the
	// optimizer's output has to be a tree the parser could have built. Writing
	// Literal("-1") produced SQL that read back as a different tree.
	if v < 0 {
		return New("Neg", Arg{"this", numberLit(-v, integral)})
	}
	var text string
	if integral {
		text = strconv.FormatInt(int64(v), 10)
	} else {
		text = strconv.FormatFloat(v, 'g', -1, 64)
		if !strings.ContainsAny(text, ".eE") {
			text += ".0"
		}
	}
	return New("Literal", Arg{"this", text}, Arg{"is_string", false})
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

// inverseArithmetic is what moving a constant across a comparison does:
// `x + 1 = 3` becomes `x = 3 - 1`. Only Add and Sub -- Mul and Div are
// not in the reference's INVERSE_OPS, and date/interval arithmetic is
// left alone until a fold can be checked on a calendar, not a string.
var inverseArithmetic = map[string]string{"Add": "Sub", "Sub": "Add"}

// simplifyEquality is the reference's `simplify_equality`: move a constant
// across + or - so the column stands alone. Subtraction is not commutative,
// so `5 - x = 2` inverts the comparison (`x < 3` when it was `>`).
func simplifyEquality(e *Expression) *Expression {
	if !comparisons[e.Class] || e.Class == "Is" {
		return e
	}
	left, right := childOf(e, "this"), childOf(e, "expression")
	if left == nil || right == nil || inverseArithmetic[left.Class] == "" {
		return e
	}
	if !isNumberLiteral(right) {
		return e
	}
	a, b := childOf(left, "this"), childOf(left, "expression")
	if a == nil || b == nil {
		return e
	}
	switch {
	case !isNumberLiteral(a) && isNumberLiteral(b):
		// x + 1 = 3  →  x = 3 - 1
	case !isNumberLiteral(b) && isNumberLiteral(a):
		if left.Class == "Sub" {
			// 5 - x = 2  →  x < 3  (comparison inverted, 5 - 2)
			to, ok := inverseComparison[e.Class]
			if !ok {
				return e
			}
			return New(to, Arg{"this", b.Copy()}, Arg{
				"expression", New("Sub", Arg{"this", a.Copy()}, Arg{"expression", right.Copy()}),
			})
		}
		a, b = b, a
	default:
		return e
	}
	return New(e.Class, Arg{"this", a.Copy()}, Arg{
		"expression", New(inverseArithmetic[left.Class],
			Arg{"this", right.Copy()}, Arg{"expression", b.Copy()}),
	})
}

// isConstant is the reference's `_is_constant`: a literal, a boolean, NULL,
// or a Neg of one. `-500` is Neg(Literal), not a Literal whose text begins
// with a minus, and without counting it a comparison against it would not
// flip to put the column on the left.
func isConstant(e *Expression) bool {
	if e == nil {
		return false
	}
	if e.Class == "Neg" {
		return isConstant(childOf(e, "this"))
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

// isNumberLiteral counts `-1` as a number as well as `1`. The reference does
// -- its `is_number` is true for a Neg over one -- and without that the shift
// could not fold itself back: reading `a[0]` gives Neg(1), and writing it
// again asks for Neg(1) + 1, which came out as the text `-1 + 1`.
func isNumberLiteral(e *Expression) bool {
	if e == nil {
		return false
	}
	if e.Class == "Neg" {
		return isNumberLiteral(childOf(e, "this"))
	}
	if e.Class != "Literal" {
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
func simplifyNot(e, parent *Expression, dialect string) *Expression {
	if e.Class != "Not" {
		return e
	}
	this, _ := e.Args["this"].(*Expression)
	if this == nil {
		return e
	}
	// `NOT NULL` is not NULL: the reference keeps it a CONDITION by writing
	// `NULL AND TRUE`, the same guard it applies whenever a fold would leave a
	// value where a predicate belongs.
	if isNull(this) {
		return parenthesizeNestedConnector(
			New("And", Arg{"this", New("Null")}, Arg{"expression", boolLit(true)}), parent)
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
func simplifyConnectors(e, parent *Expression) *Expression {
	if e.Class != "And" && e.Class != "Or" {
		return e
	}
	left, _ := e.Args["this"].(*Expression)
	right, _ := e.Args["expression"].(*Expression)
	if left == nil || right == nil {
		return e
	}

	// `x AND x` is `x`. This is the safe corner of the reference's uniq_sort,
	// which also SORTS a flattened chain of operands -- `A OR (NOT A AND B)`
	// comes back as `A OR (B AND NOT A)`. The ordering is not ported, so only
	// the two-operand case is folded, where there is nothing to order.
	if left.Equal(right) {
		return keepCondition(left, parent)
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
		// Two comparisons of the SAME column can decide each other:
		// `x > 1 AND x < 1` is FALSE whatever x is.
		folded = simplifyComparison(e, left, right, e.Class == "Or")
	}
	if folded == nil {
		return e
	}
	return keepCondition(folded, parent)
}

// unnest strips the parentheses around an operand so it can be compared with
// one that was written without them.
func unnest(e *Expression) *Expression {
	for e != nil && e.Class == "Paren" {
		e, _ = e.Args["this"].(*Expression)
	}
	return e
}

// chainOperands flattens a run of the same connector into its operands:
// `A AND B AND C` gives three, whatever way it happens to be nested.
func chainOperands(e *Expression, class string) []*Expression {
	if e == nil || e.Class != class {
		return []*Expression{e}
	}
	this, _ := e.Args["this"].(*Expression)
	expr, _ := e.Args["expression"].(*Expression)
	return append(chainOperands(this, class), chainOperands(expr, class)...)
}

// absorb is the absorption half of the reference's absorb_and_eliminate:
//
//	A AND (A OR B) -> A
//	A OR (A AND B) -> A
//	A OR (NOT A AND B) -> A OR B   (only where A cannot be NULL)
//	A AND (NOT A OR B) -> A AND B  (only where A cannot be NULL)
//
// The first two hold even when A is NULL. The complement forms do not:
// `NULL OR (NOT NULL AND B)` is NULL, while `NULL OR B` follows B. They
// are applied only where A is known never-null without a schema -- an IS
// predicate is BOOLEAN and never SQL NULL, which is the case COALESCE
// comparison rewrites into (`x IS NULL`).
//
// The ELIMINATION half -- `(A AND B) OR (A AND NOT B)` down to A -- is not
// here. It holds only where B is known non-null, and a column is not.
func absorb(e, parent *Expression) *Expression {
	if e.Class != "And" && e.Class != "Or" {
		return e
	}
	opposite := "Or"
	if e.Class == "Or" {
		opposite = "And"
	}
	ops := chainOperands(e, e.Class)
	if len(ops) < 2 {
		return e
	}
	if out := removeComplements(e.Class, ops); out != nil {
		return out
	}

	changed := false
	kept := make([]*Expression, 0, len(ops))
	for i, op := range ops {
		inner := unnest(op)
		if inner == nil || inner.Class != opposite {
			kept = append(kept, op)
			continue
		}
		subs := chainOperands(inner, opposite)
		// `A AND (A OR B)`: the parenthesised operand is absorbed when one of
		// ITS operands is already being required alongside it.
		absorbed := false
		var leftover []*Expression
		for _, sub := range subs {
			u := unnest(sub)
			drop := false
			for j, other := range ops {
				if i == j {
					continue
				}
				o := unnest(other)
				if o.Equal(u) {
					absorbed = true
					drop = true
					break
				}
				// A OR (NOT A AND B): drop NOT A, keep B, only when A cannot
				// be NULL. Without that guard the rewrite follows B when A
				// is NULL and the original does not.
				if u != nil && u.Class == "Not" {
					target := unnest(childOf(u, "this"))
					if isKnownNonnull(target) && o.Equal(target) {
						changed = true
						drop = true
						break
					}
				}
			}
			if !drop {
				leftover = append(leftover, sub)
			}
		}
		switch {
		case absorbed:
			changed = true
		case len(leftover) == len(subs):
			kept = append(kept, op)
		case len(leftover) == 0:
			changed = true
		case len(leftover) == 1:
			changed = true
			kept = append(kept, leftover[0])
		default:
			changed = true
			rebuilt := leftover[0]
			for _, s := range leftover[1:] {
				rebuilt = New(opposite, Arg{"this", rebuilt}, Arg{"expression", s})
			}
			kept = append(kept, rebuilt)
		}
	}
	if !changed || len(kept) == 0 {
		return e
	}
	return rebuildConnector(e.Class, kept, parent)
}

// removeComplements folds A AND NOT A to FALSE and A OR NOT A to TRUE,
// only where A cannot be NULL. `x IS NULL AND NOT x IS NULL` is FALSE;
// `x AND NOT x` is left alone, because a NULL column makes both NULL.
func removeComplements(class string, ops []*Expression) *Expression {
	for i, op := range ops {
		inner := unnest(op)
		if inner == nil || inner.Class != "Not" {
			continue
		}
		target := unnest(childOf(inner, "this"))
		if !isKnownNonnull(target) {
			continue
		}
		for j, other := range ops {
			if i != j && unnest(other).Equal(target) {
				if class == "And" {
					return boolLit(false)
				}
				return boolLit(true)
			}
		}
	}
	return nil
}

func rebuildConnector(class string, ops []*Expression, parent *Expression) *Expression {
	rebuilt := ops[len(ops)-1]
	for i := len(ops) - 2; i >= 0; i-- {
		rebuilt = New(class, Arg{"this", ops[i]}, Arg{"expression", rebuilt})
	}
	if rebuilt.Class == "And" || rebuilt.Class == "Or" {
		return rebuilt
	}
	return keepCondition(unnest(rebuilt), parent)
}

// isKnownNonnull is the part of the annotator's `nonnull` flag that needs
// no schema. An IS predicate is never SQL NULL; neither is a literal or a
// boolean. A column is not, even when it is compared -- `x = 1` is NULL
// when x is.
func isKnownNonnull(e *Expression) bool {
	e = unnest(e)
	if e == nil {
		return false
	}
	switch e.Class {
	case "Is", "Literal", "Boolean":
		return true
	case "Not":
		return isKnownNonnull(childOf(e, "this"))
	}
	return false
}

// simplifyCoalesce is the reference's `simplify_coalesce`.
//
// COALESCE(x) is x, and COALESCE(1, …) is 1. A comparison of COALESCE against
// a constant becomes a disjunction: either the first argument is present and
// the comparison uses it, or it is NULL and the comparison uses the first
// constant fallback. Existing connector and literal folds then produce
// `NOT x IS NULL AND x = 2` or `x = 1 OR x IS NULL`.
//
// Redshift is the only dialect that refuses this rewrite, and it is not
// configured here. A comparison whose other side is not a constant is left
// alone -- the rewrite is valid but does no work.
func simplifyCoalesce(e, parent *Expression) *Expression {
	if e.Class == "Coalesce" {
		if parent != nil && parent.Class == "Hint" {
			return e
		}
		this := childOf(e, "this")
		if this == nil {
			return e
		}
		// COALESCE(1, 2) is 1. COALESCE(x) is x when x is a column.
		// A Star or COLUMNS expansion inside COALESCE is not: DuckDB
		// accepts COALESCE(*COLUMNS(*)) and rejects a bare *COLUMNS(*)
		// at the root, so unwrapping it would emit SQL that does not run.
		if isNonnullConstant(this) {
			return this
		}
		if len(coalesceArgs(e)) == 0 && this.Class == "Column" {
			return this
		}
		return e
	}
	if !comparisons[e.Class] {
		return e
	}
	left, right := childOf(e, "this"), childOf(e, "expression")
	var coalesce, other *Expression
	switch {
	case left != nil && left.Class == "Coalesce":
		coalesce, other = left, right
	case right != nil && right.Class == "Coalesce":
		coalesce, other = right, left
	default:
		return e
	}
	if !isConstant(other) {
		return e
	}
	args := coalesceArgs(coalesce)
	argIndex := -1
	var fallback *Expression
	for i, arg := range args {
		if isConstant(arg) {
			argIndex, fallback = i, arg
			break
		}
	}
	if argIndex < 0 {
		return e
	}
	remaining := args[:argIndex]
	var this *Expression
	if len(remaining) == 0 {
		this = childOf(coalesce, "this")
	} else {
		this = New("Coalesce",
			Arg{"this", childOf(coalesce, "this").Copy()},
			Arg{"expressions", copyExpressions(remaining)})
	}
	if this == nil {
		return e
	}
	replaced := e.shallowCopy()
	constCmp := e.shallowCopy()
	if left != nil && left.Class == "Coalesce" {
		replaced.Set("this", this.Copy())
		constCmp.Set("this", fallback.Copy())
	} else {
		replaced.Set("expression", this.Copy())
		constCmp.Set("expression", fallback.Copy())
	}
	isNull := New("Is", Arg{"this", this.Copy()}, Arg{"expression", New("Null")})
	notNull := New("Not", Arg{"this", isNull})
	present := New("And", Arg{"this", notNull}, Arg{"expression", replaced})
	absent := New("And", Arg{"this", isNull.Copy()}, Arg{"expression", constCmp})
	return parenthesizeNestedConnector(
		New("Or", Arg{"this", present}, Arg{"expression", absent}), parent)
}

func coalesceArgs(e *Expression) []*Expression {
	args, _ := e.Args["expressions"].([]*Expression)
	return args
}

func copyExpressions(in []*Expression) []*Expression {
	out := make([]*Expression, len(in))
	for i, e := range in {
		out[i] = e.Copy()
	}
	return out
}

// simplifyStartsWith folds a prefix check whose both sides are string
// literals: STARTS_WITH('foo', 'f') is TRUE. A column on either side is
// left alone -- whether it starts with a prefix is not knowable here.
func simplifyStartsWith(e *Expression) *Expression {
	if e.Class != "StartsWith" {
		return e
	}
	this, prefix := childOf(e, "this"), childOf(e, "expression")
	if !isStringLiteral(this) || !isStringLiteral(prefix) {
		return e
	}
	s, _ := this.Args["this"].(string)
	p, _ := prefix.Args["this"].(string)
	return boolLit(strings.HasPrefix(s, p))
}

func isNonnullConstant(e *Expression) bool {
	if e == nil {
		return false
	}
	switch e.Class {
	case "Literal", "Boolean":
		return true
	}
	return false
}

// parenthesizeNestedConnector is the reference's rule, and its comment says
// exactly why: "The generator flattens nested connectors and relies on Paren
// nodes for grouping." A connector under a NOT, or under a connector of a
// different kind, needs its parentheses back -- otherwise `NOT NULL AND TRUE`
// reads as `(NOT NULL) AND TRUE`, which is a different statement.
//
// Skipping this produced rewrites that parsed cleanly and meant something
// else. Nothing that runs them could have caught it either: these are bare
// predicates over undefined columns, so the execution oracle never sees them.
func parenthesizeNestedConnector(e, parent *Expression) *Expression {
	if !isA("Connector", e) {
		return e
	}
	if parent != nil && (parent.Class == "Not" ||
		(isA("Connector", parent) && parent.Class != e.Class)) {
		return New("Paren", Arg{"this", e})
	}
	return e
}

// keepCondition puts a TRUE back when a fold would leave a VALUE where a
// predicate belongs. Reducing `x AND TRUE` to a bare column changes a
// condition into a column reference, so the reference writes `x AND TRUE` --
// which is why the contract says `x AND x` becomes `x AND TRUE` and not `x`.
func keepCondition(folded, parent *Expression) *Expression {
	if folded.Class == "And" || folded.Class == "Or" || folded.Class == "Boolean" ||
		isKnownBoolean(folded) {
		return folded
	}
	return parenthesizeNestedConnector(
		New("And", Arg{"this", folded}, Arg{"expression", boolLit(true)}), parent)
}

// comparisons are the operators simplifyComparison reasons about.
var comparisons = map[string]bool{
	"EQ": true, "NEQ": true, "GT": true, "GTE": true, "LT": true, "LTE": true, "Is": true,
}

var ltLTE = map[string]bool{"LT": true, "LTE": true}
var gtGTE = map[string]bool{"GT": true, "GTE": true}

// nondeterministic calls cannot stand in for each other even when they are
// written identically: two RANDs are two different numbers.
var nondeterministic = map[string]bool{"Rand": true, "Randn": true}

// simplifyComparison folds two comparisons that share an operand.
//
// `x > 1 AND x < 1` is FALSE, `x = 1 AND x >= 2` is FALSE, `x < 1 AND x < 2`
// is `x < 1`. The shared operand does not have to be a column -- it has to be
// something that is the SAME on both sides and not a constant, so that the
// two comparisons are talking about one value.
//
// Never TRUE, only FALSE or one of the two: the shared operand may be NULL,
// and `x > 1 OR x <= 1` is NULL rather than TRUE when it is.
//
// Dates are not handled. Comparing them means parsing calendar literals, and
// a port that got that subtly wrong would fold a range into the wrong answer.
func simplifyComparison(e, left, right *Expression, or bool) *Expression {
	if !comparisons[left.Class] || !comparisons[right.Class] {
		return nil
	}
	// A negated IS does not behave like the others under this reasoning.
	for _, side := range []*Expression{left, right} {
		if negate, _ := side.Args["negate"].(bool); side.Class == "Is" && negate {
			return nil
		}
	}

	lArgs := []*Expression{childOf(left, "this"), childOf(left, "expression")}
	rArgs := []*Expression{childOf(right, "this"), childOf(right, "expression")}
	for _, a := range append(append([]*Expression{}, lArgs...), rArgs...) {
		if a == nil {
			return nil
		}
	}

	// The operand both sides share, and which is not itself a constant.
	var shared *Expression
	for _, a := range lArgs {
		for _, b := range rArgs {
			if a.Equal(b) && !isConstant(a) && !hasNondeterministic(a) {
				shared = a
			}
		}
	}
	if shared == nil {
		return nil
	}

	lOther := otherThan(lArgs, shared)
	rOther := otherThan(rArgs, shared)
	if lOther == nil || rOther == nil {
		return e
	}

	cmp, ok := compareConstants(lOther, rOther)
	if !ok {
		return nil
	}

	// Both orderings, as the reference tries both permutations.
	for _, pair := range [][2]*Expression{{left, right}, {right, left}} {
		a, b := pair[0], pair[1]
		av := cmp
		if a == right {
			av = -cmp
		}
		if out := foldComparisonPair(a, b, av, or, left, right); out != nil {
			return out
		}
	}
	return nil
}

// foldComparisonPair applies the reference's table for one ordering. `av` is
// the sign of a's constant against b's: negative when a's is smaller.
func foldComparisonPair(a, b *Expression, av int, or bool, left, right *Expression) *Expression {
	switch {
	case ltLTE[a.Class] && ltLTE[b.Class]:
		// The tighter bound wins under AND, the looser under OR.
		if or {
			return pick(av > 0, left, right)
		}
		return pick(av <= 0, left, right)
	case gtGTE[a.Class] && gtGTE[b.Class]:
		if or {
			return pick(av < 0, left, right)
		}
		return pick(av >= 0, left, right)
	}
	if or {
		// Never TRUE: the shared operand could be NULL.
		return nil
	}
	switch {
	case a.Class == "LT" && gtGTE[b.Class] && av <= 0:
		return boolLit(false)
	case a.Class == "GT" && ltLTE[b.Class] && av >= 0:
		return boolLit(false)
	case a.Class == "EQ":
		switch b.Class {
		case "LT":
			return pick(av >= 0, boolLit(false), a)
		case "LTE":
			return pick(av > 0, boolLit(false), a)
		case "GT":
			return pick(av <= 0, boolLit(false), a)
		case "GTE":
			return pick(av < 0, boolLit(false), a)
		case "NEQ":
			return pick(av == 0, boolLit(false), a)
		}
	}
	return nil
}

func pick(cond bool, whenTrue, whenFalse *Expression) *Expression {
	if cond {
		return whenTrue
	}
	return whenFalse
}

func otherThan(args []*Expression, shared *Expression) *Expression {
	for _, a := range args {
		if !a.Equal(shared) {
			return a
		}
	}
	return nil
}

func hasNondeterministic(e *Expression) bool {
	found := false
	e.Walk(func(n *Expression) bool {
		if nondeterministic[n.Class] {
			found = true
		}
		return true
	})
	return found
}

// compareConstants orders two constants, reporting false where they are not
// both numbers or both strings. Dates fall here on purpose.
func compareConstants(a, b *Expression) (int, bool) {
	if isNumberLiteral(a) && isNumberLiteral(b) {
		x, okA := numberOf(a)
		y, okB := numberOf(b)
		if !okA || !okB {
			return 0, false
		}
		return sign(x, y), true
	}
	if isStringLiteral(a) && isStringLiteral(b) {
		x, _ := a.Args["this"].(string)
		y, _ := b.Args["this"].(string)
		switch {
		case x < y:
			return -1, true
		case x > y:
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

func sign(x, y float64) int {
	switch {
	case x < y:
		return -1
	case x > y:
		return 1
	}
	return 0
}

// Byte-sized bounds. A literal inside them cannot overflow ANY integer type,
// which is what makes dropping the cast side-effect free.
const (
	tinyintMin  = -128
	tinyintMax  = 127
	utinyintMin = 0
	utinyintMax = 255
)

var signedIntegerTypes = map[string]bool{
	"BIGINT": true, "INT": true, "INT128": true, "INT256": true,
	"MEDIUMINT": true, "SMALLINT": true, "TINYINT": true,
}

var unsignedIntegerTypes = map[string]bool{
	"UBIGINT": true, "UINT": true, "UINT128": true, "UINT256": true,
	"UMEDIUMINT": true, "USMALLINT": true, "UTINYINT": true,
}

// withoutWideningCast strips a cast that cannot change the value.
//
// Nested casts are unwrapped from the inside out, so
// `CAST(CAST(CAST(-1 AS INT) AS INT) AS INT)` comes back as `-1`.
func withoutWideningCast(e *Expression) *Expression {
	if e == nil || e.Class != "Cast" {
		return e
	}
	inner := childOf(e, "this")
	if inner != nil && inner.Class == "Cast" {
		inner = withoutWideningCast(inner)
	}
	if !isIntegerLiteral(inner) {
		return e
	}
	n, ok := numberOf(inner)
	if !ok {
		return e
	}
	to := typeKind(childOf(e, "to"))
	switch {
	case n >= tinyintMin && n <= tinyintMax && signedIntegerTypes[to]:
		return inner
	case n >= utinyintMin && n <= utinyintMax && unsignedIntegerTypes[to]:
		return inner
	}
	return e
}

// flatFold folds the constants out of a chain of one associative operator,
// wherever in the chain they sit.
//
// This is the reference's `_flat_simplify`, and the shape of the scan is its
// own: take the first operand, look for any LATER one it combines with, and
// put the combined value back at the FRONT so it can combine again. What is
// left over keeps the order it was written in, and the chain is rebuilt
// leaning left, which is how the parser built it.
//
// A chain nothing folds in is returned unchanged rather than rebuilt, so a
// sum of columns is not re-associated for nothing.
func flatFold(e *Expression) *Expression {
	queue := chainOperands(e, e.Class)
	size := len(queue)
	var kept []*Expression
	for len(queue) > 0 {
		a := queue[0]
		queue = queue[1:]
		folded := false
		for i, b := range queue {
			out := foldNumbers(e, a, b)
			if out == nil {
				continue
			}
			rest := append([]*Expression{out}, queue[:i]...)
			queue = append(rest, queue[i+1:]...)
			folded = true
			break
		}
		if !folded {
			kept = append(kept, a)
		}
	}
	if len(kept) == size {
		return e
	}
	out := kept[0]
	for _, operand := range kept[1:] {
		out = New(e.Class, Arg{"this", out}, Arg{"expression", operand})
	}
	return out
}

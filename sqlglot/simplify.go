package sqlglot

import (
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"
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
		// An INTERVAL's own amount is never visited: the reference's own
		// traversal only ever descends into a node the annotator can type
		// (Binary, Func, Lambda, Predicate, Unary), and exp.Interval is none
		// of those, so `extract_interval`'s own `.to_py()` call always sees
		// exactly the literal that was written, never a folded one -- an
		// interval written as `INTERVAL (5 - 2) DAY` stays that way even
		// though `5 - 2` would fold anywhere else. Folding it here anyway
		// produced a signed bare number DuckDB's own grammar cannot read
		// back (`INTERVAL -15 MONTH`), found by the execution oracle.
		if out.Class == "Interval" && key == "this" {
			continue
		}
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
	// `WHERE TRUE` filters out nothing, and the reference drops the clause
	// entirely rather than leave a condition that always passes. Returning
	// nil here removes it: the caller's own Set("where", nil) is how a Select
	// drops an argument everywhere else in this tree.
	//
	// FILTER's own WHERE is a different position -- `AVG(x) FILTER (WHERE
	// TRUE)` -- and the keyword there is not optional the way a query's own
	// WHERE is: an empty FILTER() is not SQL a parser will read back. The
	// reference drops it there too and writes exactly that, which the
	// execution oracle caught as one more of the reference's own bugs this
	// port agrees with byte for byte but declines to reproduce, the same way
	// it already declines to reproduce a give-up Command from a point of its
	// own the reference never reaches.
	if out.Class == "Where" && (parent == nil || parent.Class != "Filter") {
		cond, _ := out.Args["this"].(*Expression)
		if alwaysTrue(cond) {
			return nil
		}
	}
	if out.Class == "Join" {
		out = simplifyAlwaysTrueJoin(out)
	}
	switch out.Class {
	case "Add", "Sub", "DateAdd", "DateSub", "DatetimeAdd", "DatetimeSub":
		if folded := foldDateArithmetic(out); folded != nil {
			return folded
		}
	case "DateTrunc", "TimestampTrunc":
		if folded := foldDateTruncLiteral(out, dialect); folded != nil {
			return folded
		}
	case "LT", "GT", "LTE", "GTE", "EQ", "NEQ":
		if folded := foldDateTruncComparison(out, parent, dialect); folded != nil {
			return folded
		}
	case "In":
		if folded := foldDateTruncIn(out, parent, dialect); folded != nil {
			return folded
		}
	case "Between":
		return rewriteBetween(out, parent)
	}
	out = simplifyConditionals(out, parent)
	out = propagateConstants(out, parent)
	out = simplifyLiterals(out, parent)
	out = simplifyCoalesce(out, parent)
	out = simplifyConcat(out)
	out = simplifyNot(out, parent, dialect)
	out = uniqSort(out, parent, dialect)
	out = absorb(out, parent)
	out = simplifyConnectors(out, parent)
	out = simplifyParens(out, parent)
	out = sortComparison(out, dialect)
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
	parentClass := ""
	if parent != nil {
		parentClass = parent.Class
	}
	arithmeticParent := parentClass == "Add" || parentClass == "Sub" ||
		parentClass == "Mul" || parentClass == "Div"

	// Arithmetic is handled in a switch of its OWN, entirely separate from
	// the boolean-only one below: the two guards were written for different
	// operators, and widening the shared one to admit an arithmetic parent
	// would also have widened the Predicate/Connector cases below to fire
	// under it -- `a - (b < c)` dropping to `a - b < c` reads back as
	// `(a - b) < c`, a different statement. Only the cases actually verified
	// against the reference are ported: an atomic operand (nothing left of
	// it to bind wrong), Add-in-Add and Mul-in-Mul by associativity, and
	// Mul-in-Add/Sub by precedence. Sub-in-Sub, Sub-in-Add and anything
	// under Div beyond a bare literal are NOT associative the same way and
	// stay untouched.
	if (this.Class == "Literal" || this.Class == "Column") && arithmeticParent {
		return this
	}
	if this.Class == "Add" && parentClass == "Add" {
		// `x + (y + z)` is `x + y + z`: Add is associative, so which side of
		// it the grouping was written on carries no meaning. Once the pair
		// is gone the child is a direct Add operand of its parent, which is
		// what lets flatFold see across it on a later pass.
		return this
	}
	if this.Class == "Mul" && parentClass == "Mul" {
		return this // Same, for Mul.
	}
	if this.Class == "Mul" && (parentClass == "Add" || parentClass == "Sub") {
		// Mul binds tighter than Add or Sub, so its parentheses are always
		// redundant there -- the same reason a Predicate's are under a
		// Connector, just one precedence tier down.
		return this
	}
	if arithmeticParent {
		return e
	}
	// A Paren directly wrapping another Paren is redundant regardless of
	// what encloses either of them: two layers of grouping never mean
	// anything a plain layer would not.
	if this.Class == "Paren" {
		return this
	}

	// A parent that is not itself a Condition or a Binary operator -- WHERE,
	// HAVING, the top of the statement -- needs no grouping around a
	// boolean-shaped or Unary `this` at all, the same as parent being nil.
	// This is scoped to those two cases specifically -- a Connector, a
	// Predicate, or a Unary (Neg, BitwiseNot; Not and Paren itself are
	// already handled their own way) -- rather than every non-Condition/
	// Binary parent, because a NUMERIC Binary child (an INTERVAL's own
	// amount, say) is neither Condition nor Binary either, and DuckDB's own
	// grammar needs the parens THERE kept around a non-literal amount; the
	// fuzzer found exactly that the one time this was tried unconditionally.
	// A Unary `this`, unlike an arbitrary Binary one, is always safe to drop
	// here: nothing above a clause wrapper can misread `-x` or `~x` written
	// without its own parens, the way it could misread a `+`/`-` operand.
	clauseWrapper := parent != nil && !isA("Condition", parent) && !isA("Binary", parent) &&
		(isA("Connector", this) || isA("Predicate", this) || isA("Unary", this))
	// An atomic operand -- a bare Column, Literal, Boolean or Null -- binds
	// tighter than a COMPARISON above it, the same way one already does
	// under a Connector or a NOT: `(x.a) IS NULL` needs its parens no more
	// than `x.a IS NULL` alone would, whatever the qualified column's own
	// dotted path looks like. Scoped to `comparisons`, not the wider
	// Predicate class: ANY/ALL/SOME are Predicates too, and THEIR parens are
	// part of the call syntax itself -- `ANY(t.value)` is not `ANY t.value`.
	atomicThis := this.Class == "Boolean" || this.Class == "Null" || this.Class == "Literal" || this.Class == "Column"
	underPredicate := atomicThis && parent != nil && comparisons[parent.Class]
	if parent != nil && !isA("Connector", parent) && parentClass != "Not" && !clauseWrapper && !underPredicate {
		return e
	}
	switch {
	case parent == nil, clauseWrapper, underPredicate:
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
	case this.Class == "Not":
		// NOT binds tighter than any connector or another NOT above it, so
		// `NOT (NOT a)` needs its parens no more than `NOT a` alone would --
		// `NOT NOT a` reads back the same tree either way.
	case this.Class == "Boolean", this.Class == "Null", this.Class == "Literal", this.Class == "Column":
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
	// Double negation of a value, not of a boolean: `--500` is `500`, and so
	// is `-(-500)` -- a Paren around a Unary operand never changes its own
	// value, so unnesting through one here before checking is exact, not a
	// guess: simplify_parens has no path of its own to reach in and drop
	// THIS particular Paren (Neg is a Condition, which blocks every general
	// reason simplify_parens has for dropping one), so nothing else ever
	// will. The boolean case (`NOT NOT x`) is simplifyNot, and needs a type.
	if e.Class == "Neg" {
		this := unnest(childOf(e, "this"))
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
	// A comparison with a NULL operand keeps its own NULL spelling everywhere
	// EXCEPT directly inside an IF's own condition (which includes a CASE
	// WHEN's condition -- one of those is an If node too): there, and only
	// there, a NULL condition already means "skip this branch" the same way
	// FALSE does, so folding it to a bare NULL is a spelling change, not a
	// semantic one. `SELECT x = NULL` must not become `SELECT NULL`.
	if (isNull(a) || isNull(b)) && parent != nil && parent.Class == "If" {
		return New("Null")
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
	// Two date/datetime literals compare directly, the same as two numbers
	// or two strings do -- `CAST('2023-01-01' AS DATE) = CAST(... AS
	// DATETIME)` decides itself the moment both sides read as a calendar
	// value, whatever the two CASTs happened to name their own type as.
	if isA("Predicate", e) {
		if ta, _, ok := extractDateValue(a); ok {
			if tb, _, ok := extractDateValue(b); ok {
				if out := evalBooleanTime(e.Class, ta, tb); out != nil {
					return out
				}
			}
		}
	}
	return e
}

// evalBooleanTime is evalBooleanString's own counterpart for two date or
// datetime values already read out of their CASTs.
func evalBooleanTime(class string, a, b time.Time) *Expression {
	switch class {
	case "EQ":
		return boolLit(a.Equal(b))
	case "NEQ":
		return boolLit(!a.Equal(b))
	case "GT":
		return boolLit(a.After(b))
	case "GTE":
		return boolLit(a.After(b) || a.Equal(b))
	case "LT":
		return boolLit(a.Before(b))
	case "LTE":
		return boolLit(a.Before(b) || a.Equal(b))
	}
	return nil
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
	switch e.Class {
	case "Add", "Sub", "Mul", "Div":
		if isIntegerLiteral(a) && isIntegerLiteral(b) {
			return foldIntegerArithmetic(e.Class, a, b)
		}
		return foldDecimalArithmetic(e.Class, a, b)
	}
	x, okA := numberOf(a)
	y, okB := numberOf(b)
	if !okA || !okB {
		return nil
	}
	return evalBooleanNumber(e.Class, x, y)
}

// foldIntegerArithmetic is the float64 path, kept for operands that are both
// plain integers: no fractional literal can appear on either side, so
// float64 has nothing to lose that decimal arithmetic would have kept. Both
// operands fit int64 -- that is what makes them integer literals at all --
// so Add, Sub and Mul of them never overflow float64 the way a huge
// fractional literal could; that guard now lives with the decimal path,
// which is where a literal large enough to need it actually goes.
func foldIntegerArithmetic(class string, a, b *Expression) *Expression {
	x, okA := numberOf(a)
	y, okB := numberOf(b)
	if !okA || !okB {
		return nil
	}
	switch class {
	case "Add":
		return numberLit(x+y, true)
	case "Mul":
		return numberLit(x*y, true)
	case "Sub":
		return numberLit(x-y, true)
	}
	// Div: integer division differs between engines, so the reference
	// declines to fold it rather than pick one engine's answer.
	return nil
}

// foldDecimalArithmetic computes Add, Sub, Mul and Div the way the
// reference does whenever either operand carries a fractional literal:
// exactly, in decimal, because a SQL numeric literal is `Decimal(text)`
// there, not a binary float. `0.06` has no exact float64 value at all, so
// folding `0.06 + 0.01` through one answers a question about a different
// number than the one written -- `0.06999999999999999`, not `0.07`.
func foldDecimalArithmetic(class string, a, b *Expression) *Expression {
	da, okA := decimalOf(a)
	db, okB := decimalOf(b)
	if !okA || !okB {
		return nil
	}
	var result *bigDecimal
	switch class {
	case "Add":
		result = da.Add(db)
	case "Sub":
		result = da.Sub(db)
	case "Mul":
		result = da.Mul(db)
	case "Div":
		r, ok := da.Div(db, decimalPrecision)
		if !ok {
			return nil // division by zero
		}
		result = r
	default:
		return nil
	}
	// Larger than anything the reference's own contract needs, and past
	// here the reference may switch to scientific notation, which this
	// port does not write. Declining costs nothing the contract asks for.
	if result.digitCount() > maxDecimalDigits {
		return nil
	}
	return decimalLit(result)
}

// decimalLit writes a bigDecimal the way numberLit writes a float64: a
// negative value is Neg(Literal) rather than a literal whose text begins
// with a minus, and a whole result still shows a point, because it came
// from a fractional literal and the reference marks it as one for that
// reason alone.
func decimalLit(d *bigDecimal) *Expression {
	if d.unscaled.Sign() < 0 {
		return New("Neg", Arg{"this", decimalLit(&bigDecimal{
			unscaled: new(big.Int).Abs(d.unscaled), scale: d.scale,
		})})
	}
	text := d.String()
	if d.scale == 0 {
		text += ".0"
	}
	return New("Literal", Arg{"this", text}, Arg{"is_string", false})
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
func sortComparison(e *Expression, dialect string) *Expression {
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
	// The final tiebreak, when neither side is a column or a constant --
	// e.g. two arithmetic expressions freshly built by simplifyConditionals
	// out of the same CASE subject -- is a plain string-sort of each side's
	// own SQL, the reference's own `gen(l) > gen(r)`. Reusing this port's
	// real generator instead of a bespoke bare one changes what a handful of
	// dialect-specific fringe cases would sort as, but not this rule's own
	// contract: it only ever breaks a tie neither of the rules above settled.
	if (rCol && !lCol) || (lConst && !rConst) || genGreater(left, right, dialect) {
		return New(to, Arg{"this", right}, Arg{"expression", left})
	}
	return e
}

// genGreater reports whether left's own SQL sorts after right's -- the
// reference's `gen(l) > gen(r)` tiebreak. Either side failing to generate
// leaves the comparison exactly as written rather than guessing.
func genGreater(left, right *Expression, dialect string) bool {
	l, lerr := Generate(left, dialect)
	r, rerr := Generate(right, dialect)
	return lerr == nil && rerr == nil && l > r
}

// inverseArithmetic is what moving a constant across a comparison does:
// `x + 1 = 3` becomes `x = 3 - 1`. Only Add and Sub -- Mul and Div are not
// in the reference's INVERSE_OPS. DATE_ADD/DATE_SUB and their DATETIME
// counterparts are in the reference's own INVERSE_DATE_OPS too, moving an
// interval argument the same way this file's own DATE_ADD family folds --
// but nothing in the fixture needs that shape, only a plain `x - INTERVAL
// n unit` (already covered here as Sub), so it is not ported speculatively.
var inverseArithmetic = map[string]string{"Add": "Sub", "Sub": "Add"}

// simplifyEquality is the reference's `simplify_equality`: move a constant
// across + or - so the column stands alone. Subtraction is not commutative,
// so `5 - x = 2` inverts the comparison (`x < 3` when it was `>`).
//
// The constant moved is either a NUMBER on both sides, or -- on the
// comparison's own side -- a date literal opposite an INTERVAL on the
// Add/Sub's own side: `x - INTERVAL 1 DAY = CAST('2021-01-01' AS DATE)`
// becomes `x = CAST('2021-01-01' AS DATE) + INTERVAL 1 DAY`, which a LATER
// pass folds the rest of the way once the new Add is its own node to visit.
func simplifyEquality(e *Expression) *Expression {
	if !comparisons[e.Class] || e.Class == "Is" {
		return e
	}
	left, right := childOf(e, "this"), childOf(e, "expression")
	if left == nil || right == nil || inverseArithmetic[left.Class] == "" {
		return e
	}
	a, b := childOf(left, "this"), childOf(left, "expression")
	if a == nil || b == nil {
		return e
	}
	var aPredicate, bPredicate func(*Expression) bool
	switch {
	case isNumberLiteral(right):
		aPredicate, bPredicate = isNumberLiteral, isNumberLiteral
	case isDateLiteral(right):
		aPredicate, bPredicate = isDateLiteral, isIntervalLiteral
	default:
		return e
	}
	switch {
	case !aPredicate(a) && bPredicate(b):
		// x + 1 = 3  →  x = 3 - 1
	case !aPredicate(b) && bPredicate(a):
		if left.Class == "Sub" {
			// 5 - x = 2  →  x < 3  (comparison inverted, 5 - 2)
			// `e.Class` is never "Is" here -- excluded above -- so it is
			// always one of the six comparisons inverseComparison covers.
			to := inverseComparison[e.Class]
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

// isDateLiteral is the reference's `_is_date_literal`: whatever
// extractDateValue can read a calendar value out of.
func isDateLiteral(e *Expression) bool {
	_, _, ok := extractDateValue(e)
	return ok
}

// isIntervalLiteral is the reference's `_is_interval`: an INTERVAL whose
// own amount and unit intervalOf can read.
func isIntervalLiteral(e *Expression) bool {
	if e == nil || e.Class != "Interval" {
		return false
	}
	_, _, ok := intervalOf(e)
	return ok
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
	// A date/datetime literal -- `CAST('2021-01-01' AS DATE)` -- is a value
	// this port can fold arithmetic over the same way a bare Literal is, so
	// the reference counts it as constant too: without this, `CAST(date) =
	// DATE_TRUNC(x)` never reorders into the shape simplify_datetrunc reads,
	// because that check always requires the DATE_TRUNC on the left.
	if _, _, ok := extractDateValue(e); ok {
		return true
	}
	return false
}

// The reference's own predicates, by the names it gives them. Kept separate
// and tiny because nearly every rule below is built out of them, and getting
// one subtly wrong would be a rewrite that changes an answer.
func isNull(e *Expression) bool { return e != nil && e.Class == "Null" }

// isNullShaped is isNull widened to also recognise NULL's own wrapped form,
// `NULL AND TRUE` -- the shape keepCondition leaves behind when a fold to
// NULL stands somewhere a bare value cannot. A second rule reading `this` as
// a value has to see through that wrapper the same way it would see a bare
// NULL, or the wrapping becomes permanent the moment one rule applies it.
func isNullShaped(e *Expression) bool {
	// keepCondition's own wrap can itself come back wrapped in a Paren --
	// parenthesizeNestedConnector adds one when the parent is a NOT, which is
	// exactly the position `NOT NOT NULL`'s outer NOT reads this shape from.
	e = unnest(e)
	if isNull(e) {
		return true
	}
	if e == nil || e.Class != "And" {
		return false
	}
	left, _ := e.Args["this"].(*Expression)
	right, _ := e.Args["expression"].(*Expression)
	return isNull(left) && alwaysTrue(right)
}
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

// negate builds NOT of e the way this file needs it built where a bare
// `New("Not", ...)` is not safe: as one operand of a De Morgan rewrite,
// which is a BRAND NEW node this same pass never recurses back into, so a
// double negation or a plain comparison left unresolved here would stay
// that way, unprotected, until some LATER pass finally saw it -- and if it
// is itself a Connector by then, nothing before it in the tree still knows
// this one needed a NOT in front of it. Resolving the two cheap, purely
// syntactic shortcuts immediately -- complementing a comparison, cancelling
// a double negation -- is what keeps every De Morgan step this file takes
// self-contained and safe to write down on its own.
func negate(e *Expression, dialect string) *Expression {
	if to, ok := complementComparison[e.Class]; ok {
		left, right := childOf(e, "this"), childOf(e, "expression")
		if left != nil && right != nil && len(e.Keys) == 2 {
			if flipped, isQuantifier := complementQuantifier[right.Class]; isQuantifier {
				if inner := childOf(right, "this"); inner != nil {
					right = New(flipped, Arg{"this", inner})
				}
			}
			return New(to, Arg{"this", left}, Arg{"expression", right})
		}
	}
	if e.Class == "Not" && safeToEliminateDoubleNegation(dialect) {
		if inner := childOf(e, "this"); isKnownBoolean(inner) {
			return inner
		}
	}
	// e is itself a Connector: a bare `Not(e)` here would be exactly the
	// unprotected shape this function exists to avoid ever constructing, so
	// De Morgan distributes into it immediately too, recursively, rather
	// than leaving a second application for some later pass to find.
	if e.Class == "And" || e.Class == "Or" {
		left, right := childOf(e, "this"), childOf(e, "expression")
		if left != nil && right != nil {
			distributed := "Or"
			if e.Class == "Or" {
				distributed = "And"
			}
			return New("Paren", Arg{"this", New(distributed,
				Arg{"this", negate(left, dialect)},
				Arg{"expression", negate(right, dialect)})})
		}
	}
	return New("Not", Arg{"this", e})
}

// simplifyNot folds a negation whose operand is already decided.
func simplifyNot(e, parent *Expression, dialect string) *Expression {
	if e.Class != "Not" {
		return e
	}
	this, _ := e.Args["this"].(*Expression)
	if this == nil {
		return e
	}
	// `NOT NULL` is not NULL: the reference keeps it a CONDITION by writing
	// `NULL AND TRUE`, the same guard keepCondition applies whenever a fold
	// would leave a value where a predicate belongs -- unless the parent is
	// itself a connector, in which case the value is already there.
	//
	// isNullShaped, not isNull: children fold bottom-up, so `NOT NOT NULL`
	// reaches here with `this` already turned into the wrapped `NULL AND
	// TRUE` by the INNER NOT's own turn through this same rule. Without
	// recognising that shape too, the double negation would never re-collapse
	// -- `this.Class` is "And" by the time the outer NOT sees it, not "Not".
	if isNullShaped(this) {
		return keepCondition(New("Null"), parent)
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
	// De Morgan's law, but only through an explicit Paren: valid SQL always
	// writes one around an AND/OR directly under NOT, so this is the shape
	// `NOT (a AND b)` and `NOT BETWEEN`'s own negated rewrite actually take,
	// not a general distribution rule reached for any bare Connector. A
	// Paren wrapping NULL (`NOT (NULL)`) needs no case of its own here --
	// isNullShaped above already unnests a Paren on its way to checking for
	// NULL, so it has already caught that shape by the time this runs.
	if this.Class == "Paren" {
		condition := unnest(this)
		switch {
		case condition != nil && condition.Class == "And":
			left, right := childOf(condition, "this"), childOf(condition, "expression")
			return New("Paren", Arg{"this", New("Or",
				Arg{"this", negate(left, dialect)},
				Arg{"expression", negate(right, dialect)})})
		case condition != nil && condition.Class == "Or":
			left, right := childOf(condition, "this"), childOf(condition, "expression")
			return New("Paren", Arg{"this", New("And",
				Arg{"this", negate(left, dialect)},
				Arg{"expression", negate(right, dialect)})})
		}
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
		folded = simplifyComparison(left, right, e.Class == "Or")
	}
	if folded == nil {
		// The two DIRECT children decide nothing on their own, but a THIRD
		// operand further down the same chain might: `x > 1 AND x < 2 AND
		// x > 3` is FALSE, which the pairwise check above can never see
		// because neither of e's own two children is itself a comparison --
		// one of them is `x > 1 AND x < 2`. Scanning every pair in the whole
		// flattened chain is what the reference's own `_flat_simplify` does.
		if out := flatFoldComparisons(e); out != nil {
			folded = out
		}
	}
	if folded == nil {
		return e
	}
	return keepCondition(folded, parent)
}

// flatMerge repeatedly asks merge to combine any two operands, replacing
// them with the result and trying again, until no pair combines further --
// the reference's own `_flat_simplify`. It returns nil, not the unchanged
// input, when nothing combined, so a caller can tell "no change" apart from
// "collapsed to a single operand" without comparing trees.
func flatMerge(operands []*Expression, class string, merge func(a, b *Expression) *Expression) *Expression {
	queue := operands
	size := len(queue)
	var kept []*Expression
	for len(queue) > 0 {
		a := queue[0]
		queue = queue[1:]
		folded := false
		for i, b := range queue {
			out := merge(a, b)
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
		return nil
	}
	out := kept[0]
	for _, operand := range kept[1:] {
		out = New(class, Arg{"this", out}, Arg{"expression", operand})
	}
	return out
}

// flatFoldComparisons scans every pair in e's own fully flattened AND/OR
// chain for a shared-column comparison one of them decides, not just e's
// own two direct children -- see the comment where this is called.
func flatFoldComparisons(e *Expression) *Expression {
	queue := chainOperands(e, e.Class)
	if len(queue) < 3 {
		// Two operands is exactly what the caller's own pairwise check just
		// tried; a flat scan can only find something new past that.
		return nil
	}
	or := e.Class == "Or"
	return flatMerge(queue, e.Class, func(a, b *Expression) *Expression {
		return simplifyComparison(a, b, or)
	})
}

// unnest strips the parentheses around an operand so it can be compared with
// one that was written without them.
func unnest(e *Expression) *Expression {
	for e != nil && e.Class == "Paren" {
		e, _ = e.Args["this"].(*Expression)
	}
	return e
}

// rewriteBetween is the reference's `rewrite_between`: `x BETWEEN y AND z`
// becomes `x >= y AND x <= z`, because every other comparison-range rule in
// this file only ever looks for LT/LTE/GT/GTE -- BETWEEN itself is invisible
// to `x > 3 AND x BETWEEN 0 AND 10` unless it is spoken in those terms first.
// A BETWEEN directly under NOT keeps its own parentheses on the way out --
// `NOT x BETWEEN 0 AND 1` needs `NOT (x >= 0 AND x <= 1)`, not
// `NOT x >= 0 AND x <= 1`, which is a different, unparenthesized question --
// so simplify_not's own De Morgan step can read it back out again.
func rewriteBetween(e, parent *Expression) *Expression {
	this := childOf(e, "this")
	low := childOf(e, "low")
	high := childOf(e, "high")
	if this == nil || low == nil || high == nil {
		return e
	}
	result := New("And",
		Arg{"this", New("GTE", Arg{"this", this.Copy()}, Arg{"expression", low})},
		Arg{"expression", New("LTE", Arg{"this", this.Copy()}, Arg{"expression", high})})
	if parent != nil && parent.Class == "Not" {
		return New("Paren", Arg{"this", result})
	}
	return result
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

// joinKindsThatBecomeCross are the (side, kind) pairs the reference allows to
// turn into a CROSS JOIN once their ON is always true: a plain, an explicit
// INNER, a RIGHT, and a RIGHT OUTER. Every one of those already returns every
// row of its left-hand side matched against every row of its right, which is
// what CROSS means -- LEFT and FULL do not, because they also keep the rows
// that fail to match, and there is nothing left to fail once ON is TRUE for
// a LEFT/FULL only in the degenerate case the reference does not special-case.
var joinKindsThatBecomeCross = map[[2]string]bool{
	{"", ""}: true, {"", "INNER"}: true, {"RIGHT", ""}: true, {"RIGHT", "OUTER"}: true,
}

// simplifyAlwaysTrueJoin turns a JOIN whose ON is always true into a CROSS
// JOIN: `y JOIN z ON TRUE` becomes `y CROSS JOIN z`, saying the same thing
// without a condition that can never do anything.
func simplifyAlwaysTrueJoin(e *Expression) *Expression {
	on, _ := e.Args["on"].(*Expression)
	if !alwaysTrue(on) {
		return e
	}
	if e.Args["using"] != nil || e.Args["method"] != nil {
		return e
	}
	side, _ := e.Args["side"].(string)
	kind, _ := e.Args["kind"].(string)
	if !joinKindsThatBecomeCross[[2]string{side, kind}] {
		return e
	}
	e.Set("on", nil)
	e.Set("side", nil)
	e.Set("kind", "CROSS")
	return e
}

// uniqSort ports the reference's uniq_sort: a flattened AND/OR chain is
// deduplicated by its generated SQL, and put into that same order when it was
// not written in it already -- `C AND A AND B AND B` becomes `A AND B AND C`.
//
// Both halves are purely syntactic: reordering or dropping a REPEATED operand
// of a connector never changes what the chain means, so -- unlike absorb --
// this rule needs no nullability guard. A chain reduced to a single operand
// is not returned bare: the reference rebuilds it as `operand AND TRUE`, and
// that is matched exactly rather than folded further, because it is the
// fixture's own committed answer.
func uniqSort(e, parent *Expression, dialect string) *Expression {
	if e.Class != "And" && e.Class != "Or" {
		return e
	}
	ops := chainOperands(e, e.Class)
	if len(ops) < 2 {
		return e
	}
	// The KEY is generated from the operand with its own parentheses
	// stripped -- `(b OR c)` sorts as `b OR c` would, matching the
	// reference's flatten(), which unnests before calling gen(). The
	// wrapper itself is kept on the operand used to rebuild the tree: this
	// port's generator does not re-insert parentheses precedence requires,
	// so a compound operand still needs its own on the way back out.
	keys := make([]string, len(ops))
	for i, op := range ops {
		s, err := Generate(unnest(op), dialect)
		if err != nil {
			// Cannot key this operand safely; leave the chain as it is.
			return e
		}
		keys[i] = s
	}
	// Dedupe first, keeping the FIRST occurrence of each key -- matching
	// which of two syntactically identical operands it is does not matter.
	seen := make(map[string]bool, len(ops))
	deduped := make([]*Expression, 0, len(ops))
	dedupedKeys := make([]string, 0, len(ops))
	for i, k := range keys {
		if seen[k] {
			continue
		}
		seen[k] = true
		deduped = append(deduped, ops[i])
		dedupedKeys = append(dedupedKeys, k)
	}
	sorted := true
	for i := 1; i < len(dedupedKeys); i++ {
		if dedupedKeys[i] < dedupedKeys[i-1] {
			sorted = false
			break
		}
	}
	if sorted && len(deduped) == len(ops) {
		return e
	}
	if !sorted {
		idx := make([]int, len(deduped))
		for i := range idx {
			idx[i] = i
		}
		sort.SliceStable(idx, func(a, b int) bool { return dedupedKeys[idx[a]] < dedupedKeys[idx[b]] })
		reordered := make([]*Expression, len(deduped))
		for i, j := range idx {
			reordered[i] = deduped[j]
		}
		deduped = reordered
	}
	if len(deduped) == 1 {
		return keepCondition(deduped[0], parent)
	}
	return rebuildConnector(e.Class, deduped, parent)
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
	if out := absorbSupersets(e.Class, opposite, ops, parent); out != nil {
		return out
	}
	if out := eliminateComplementPairs(e.Class, opposite, ops, parent); out != nil {
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
			// `rebuilt` is the OPPOSITE connector to the chain it is about
			// to join -- an Or spliced bare into an And read back with AND
			// binding tighter, which is a different statement than the one
			// meant. The parentheses are not optional here, the way they
			// are for parenthesizeNestedConnector's other callers: this one
			// is ALWAYS a different class from what it joins.
			kept = append(kept, New("Paren", Arg{"this", rebuilt}))
		}
	}
	if !changed || len(kept) == 0 {
		return e
	}
	return rebuildConnector(e.Class, kept, parent)
}

// absorbSupersets is the reference's own subset half of absorb_and_eliminate:
// `(A OR C) AND (A OR C OR B)` is `A OR C`, because whichever of the two
// disjunctions decides the AND, the SMALLER one already does -- a value that
// makes `A OR C` true always makes `A OR C OR B` true too, so the larger one
// contributes nothing the smaller doesn't already say. It generalises the
// plain `A AND (A OR B)` absorption absorb's own main loop already handles
// (a bare operand is the size-1 case of the same rule), but ONLY past two
// operands of the OPPOSITE class, each fully flattened, does the general
// form find anything that loop does not: comparing LEAVES of one opposite-
// class operand against another operand's OWN leaves, not against the other
// operand as a single unit.
func absorbSupersets(class, opposite string, ops []*Expression, parent *Expression) *Expression {
	sets := make([][]*Expression, len(ops))
	for i, op := range ops {
		inner := unnest(op)
		if inner != nil && inner.Class == opposite {
			sets[i] = chainOperands(inner, opposite)
		} else {
			sets[i] = []*Expression{inner}
		}
	}
	changed := false
	kept := make([]*Expression, len(ops))
	copy(kept, ops)
	for i, op := range ops {
		inner := unnest(op)
		if inner == nil || inner.Class != opposite {
			continue
		}
		for j := range ops {
			if i != j && isProperSubset(sets[j], sets[i]) {
				changed = true
				kept[i] = boolLit(class == "And")
				break
			}
		}
	}
	if !changed {
		return nil
	}
	return rebuildConnector(class, kept, parent)
}

// isProperSubset reports whether every member of a appears in b (by
// structural equality) and a is strictly smaller.
func isProperSubset(a, b []*Expression) bool {
	if len(a) >= len(b) {
		return false
	}
	for _, x := range a {
		found := false
		for _, y := range b {
			if x.Equal(y) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// eliminateComplementPairs is absorb_and_eliminate's own elimination half:
// `(A AND B) OR (A AND NOT B)` is `A`, when B cannot be NULL -- whichever
// way B goes, the OR is decided by A alone. The OR-of-ANDs case flips to
// AND-of-ORs the same way absorbSupersets does. Unlike the reference's own
// version, this does not need a type-annotator's "nonnull" meta flag: the
// same isKnownNonnull check absorb's main loop already uses for the
// single-operand absorption case is exactly what this needs too.
func eliminateComplementPairs(class, opposite string, ops []*Expression, parent *Expression) *Expression {
	kept := make([]*Expression, len(ops))
	copy(kept, ops)
	changed := false
	for i, opI := range ops {
		innerI := unnest(opI)
		if innerI == nil || innerI.Class != opposite {
			continue
		}
		a1, b1 := childOf(innerI, "this"), childOf(innerI, "expression")
		if a1 == nil || b1 == nil {
			continue
		}
		for j, opJ := range ops {
			if i == j {
				continue
			}
			innerJ := unnest(opJ)
			if innerJ == nil || innerJ.Class != opposite {
				continue
			}
			a2, b2 := childOf(innerJ, "this"), childOf(innerJ, "expression")
			if a2 == nil || b2 == nil {
				continue
			}
			if common, ok := complementaryPair(a1, b1, a2, b2); ok {
				kept[i] = common
				kept[j] = common
				changed = true
			}
		}
	}
	if !changed {
		return nil
	}
	return rebuildConnector(class, kept, parent)
}

// complementaryPair checks whether {a1, b1} and {a2, b2} are ("common", x)
// and ("common", NOT x) in some order, with x known non-null, and returns
// "common" if so.
func complementaryPair(a1, b1, a2, b2 *Expression) (*Expression, bool) {
	for _, pair := range [2][2]*Expression{{a1, b1}, {b1, a1}} {
		common, other := pair[0], pair[1]
		var rest *Expression
		switch {
		case a2.Equal(common):
			rest = b2
		case b2.Equal(common):
			rest = a2
		default:
			continue
		}
		if rest.Class != "Not" {
			continue
		}
		inner := childOf(rest, "this")
		if isKnownNonnull(inner) && inner.Equal(other) {
			return common, true
		}
	}
	return nil, false
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
	// A Binary operator over two operands that can never themselves be NULL
	// -- `'abc' ~ 'a'` -- cannot be NULL either: there is no column or
	// expression left in it for a NULL to come from. `x ~ 'a'` stays
	// unknown, since x itself might be.
	if isA("Binary", e) {
		this, expr := childOf(e, "this"), childOf(e, "expression")
		if this != nil && expr != nil && isNonnullConstant(this) && isNonnullConstant(expr) {
			return true
		}
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

// simplifyConcat ports the reference's simplify_concat: a run of ADJACENT
// string literals inside a CONCAT, CONCAT_WS, or || chain is joined into one
// literal, leaving every other operand exactly where it was.
//
// CONCAT_WS needs its separator known at fold time to join anything at all,
// and it is the first argument -- a CONCAT_WS whose separator is not itself
// a literal is left alone entirely, matching the reference, rather than
// guessed at.
func simplifyConcat(e *Expression) *Expression {
	switch e.Class {
	case "Concat", "ConcatWs":
		return simplifyConcatArgs(e)
	case "DPipe":
		return simplifyDPipeChain(e)
	}
	return e
}

// simplifyConcatArgs handles CONCAT and CONCAT_WS, which carry their operands
// as one "expressions" list rather than a binary chain.
func simplifyConcatArgs(e *Expression) *Expression {
	exprs, _ := e.Args["expressions"].([]*Expression)
	if len(exprs) == 0 {
		return e
	}
	items := exprs
	var sepArg *Expression
	sep := ""
	if e.Class == "ConcatWs" {
		sepArg = exprs[0]
		if !isStringLiteral(sepArg) {
			return e
		}
		sep = sepArg.Name()
		items = exprs[1:]
	}
	folded, changed := foldConcatGroups(items, sep)
	// A single string survives whether or not anything needed MERGING to
	// reach it -- `CONCAT_WS('-', 'a')` has nothing to join and is still
	// bare `'a'`, the separator dropped along with the call.
	if len(folded) == 1 && isStringLiteral(folded[0]) {
		return folded[0]
	}
	if !changed {
		return e
	}
	if sepArg != nil {
		folded = append([]*Expression{sepArg}, folded...)
	}
	out := e.shallowCopy()
	out.Set("expressions", folded)
	return out
}

// simplifyDPipeChain handles ||, which the parser builds as a binary chain
// rather than a list: `'a' || 'b' || x` is DPipe(DPipe('a', 'b'), x).
func simplifyDPipeChain(e *Expression) *Expression {
	ops := chainOperands(e, "DPipe")
	if len(ops) < 2 {
		return e
	}
	folded, changed := foldConcatGroups(ops, "")
	if len(folded) == 1 && isStringLiteral(folded[0]) {
		return folded[0]
	}
	if !changed {
		return e
	}
	safe, _ := e.Args["safe"].(bool)
	rebuilt := folded[0]
	for _, op := range folded[1:] {
		rebuilt = New("DPipe", Arg{"this", rebuilt}, Arg{"expression", op}, Arg{"safe", safe})
	}
	return rebuilt
}

// foldConcatGroups joins each run of two or more ADJACENT string literals in
// items into one literal, separated by sep, and leaves every other operand --
// including a run of exactly one literal -- where it was. changed reports
// whether any run was long enough to actually join something, which is what
// tells simplifyConcatArgs/simplifyDPipeChain whether rebuilding is worth it.
func foldConcatGroups(items []*Expression, sep string) (folded []*Expression, changed bool) {
	for i := 0; i < len(items); {
		if !isStringLiteral(items[i]) {
			folded = append(folded, items[i])
			i++
			continue
		}
		j := i
		var parts []string
		for j < len(items) && isStringLiteral(items[j]) {
			parts = append(parts, items[j].Name())
			j++
		}
		if j-i > 1 {
			changed = true
		}
		folded = append(folded, New("Literal", Arg{"this", strings.Join(parts, sep)}, Arg{"is_string", true}))
		i = j
	}
	return folded, changed
}

// simplifyConditionals is the reference's `simplify_conditionals`: fold a
// CASE or a standalone IF whose condition is already known, and turn a
// simple CASE (`CASE x WHEN y THEN ...`) into a searched one (`CASE WHEN x =
// y THEN ...`) so the rest of the optimizer -- which only ever looks for a
// bare EQ -- can reach into it.
//
// The EQ each WHEN gets is built fresh here and is not itself folded on this
// visit: bottom-up, this node's children were already simplified before
// control reached it, so a `CASE 4 WHEN 1 THEN x ...` needs the outer
// Simplify loop to come back around once more before `4 = 1` becomes FALSE
// and this rule can drop the branch. That mirrors the reference's own
// while_changing loop, which does the same thing over more, smaller passes.
func simplifyConditionals(e, parent *Expression) *Expression {
	switch e.Class {
	case "Case":
		return simplifyCase(e)
	case "If":
		// An If that IS a CASE's own WHEN clause is simplifyCase's job: its
		// "true" arm is read directly as case.args["true"], with no "false"
		// arm of its own to fall back to the way a standalone IF has.
		if parent != nil && parent.Class == "Case" {
			return e
		}
		this := childOf(e, "this")
		if alwaysTrue(this) {
			return childOf(e, "true")
		}
		if alwaysFalse(this) {
			if f := childOf(e, "false"); f != nil {
				return f
			}
			return New("Null")
		}
	}
	return e
}

// simplifyCase folds CASE's own WHEN branches: a subject (`CASE x WHEN ...`)
// rewrites each WHEN's condition into an EQ against a copy of x, consuming x
// itself once every branch has one; and any WHEN whose (possibly just
// rewritten) condition is now known drops out -- fully, to the branch's own
// result, if it is TRUE, replacing the whole CASE; or entirely, if it is
// FALSE, leaving the rest of the branches to decide it instead.
func simplifyCase(e *Expression) *Expression {
	this := childOf(e, "this")
	ifs, _ := e.Args["ifs"].([]*Expression)
	kept := make([]*Expression, 0, len(ifs))
	changed := false
	for _, ifNode := range ifs {
		cond := childOf(ifNode, "this")
		if this != nil {
			// The reference's own `.eq()` builder parenthesizes a Binary
			// operand on EITHER side before writing the EQ down --
			// `CASE x1 + x2 WHEN x3 ...` becomes `x3 = (x1 + x2)`, with the
			// parens present even though EQ's own precedence would not
			// need them. It is a property of how the node was BUILT, not
			// of how it prints, so it has to be added here rather than
			// left to the generator.
			cond = New("EQ", Arg{"this", wrapBinary(this.Copy())}, Arg{"expression", wrapBinary(cond)})
			ifNode.Set("this", cond)
			changed = true
		}
		if alwaysTrue(cond) {
			return childOf(ifNode, "true")
		}
		if alwaysFalse(cond) {
			changed = true
			continue
		}
		kept = append(kept, ifNode)
	}
	if !changed {
		return e
	}
	if this != nil {
		e.Set("this", nil)
	}
	if len(kept) == 0 {
		if def := childOf(e, "default"); def != nil {
			return def
		}
		return New("Null")
	}
	e.Set("ifs", kept)
	return e
}

// wrapBinary is the reference's own `_wrap(e, Binary)`: a Binary operand
// (`x1 + x2`) gets an explicit Paren around it, anything else is untouched.
func wrapBinary(e *Expression) *Expression {
	if isA("Binary", e) {
		return New("Paren", Arg{"this", e})
	}
	return e
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
	return isDateLiteral(e)
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
//
// Only where the VALUE is about to stand alone, though: once it becomes an
// operand of a connector ONE LEVEL UP, that connector is the predicate, and
// wrapping the operand too is a pair of parentheses the reference never
// writes -- `y = 1 OR (x AND x)` is `x OR y = 1`, not `(x AND TRUE) OR y = 1`.
// A connector parent means the value is already exactly where a predicate
// belongs, so it goes back bare and lets that parent's own fold (or the next
// pass, if it does not fold there) decide what becomes of it.
func keepCondition(folded, parent *Expression) *Expression {
	// An And/Or result already LOOKS like a condition, so it never needs the
	// "AND TRUE" wrapping below -- but it can still need PROTECTIVE parens
	// around it, the same as any other connector standing where a NOT or a
	// different-class connector is its parent. Skipping that check here is
	// what let a coalesce fold's own OR -- reduced, on a later pass, down to
	// a bare AND once its other branch turned out to be FALSE -- come back
	// as `NOT x AND y`, a different, unparenthesized statement.
	if folded.Class == "And" || folded.Class == "Or" {
		return parenthesizeNestedConnector(folded, parent)
	}
	// A Paren already wrapping a Connector -- De Morgan's own construction,
	// which builds its protective parens itself rather than leaving that to
	// this function -- is exactly as condition-shaped as a bare one.
	if folded.Class == "Paren" && isA("Connector", childOf(folded, "this")) {
		return folded
	}
	if folded.Class == "Boolean" || isKnownBoolean(folded) || isA("Connector", parent) {
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
func simplifyComparison(left, right *Expression, or bool) *Expression {
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
		// A degenerate comparison like `x = x` has no OTHER operand once the
		// shared one is accounted for. Nothing to compare against.
		return nil
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
	if ta, _, ok := extractDateValue(a); ok {
		if tb, _, ok := extractDateValue(b); ok {
			switch {
			case ta.Before(tb):
				return -1, true
			case ta.After(tb):
				return 1, true
			}
			return 0, true
		}
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
	if out := flatMerge(queue, e.Class, func(a, b *Expression) *Expression { return foldNumbers(e, a, b) }); out != nil {
		return out
	}
	return e
}

package sqlglot

import (
	"fmt"
	"strings"
)

// Generation: a tree back into SQL, in a dialect.
//
// The guard rewrites the tree -- it injects a row ceiling the caller did not
// ask for -- and then has to hand a statement back to the engine. Nothing
// downstream re-reads the original text, so this is not a formatter: it is the
// statement that will actually run, and it has to say the same thing in T-SQL's
// spelling as in DuckDB's.
//
// It is held to the reference's own output, string for string, by the same
// differential as everything else. Emitting something that merely parses would
// let the two executors disagree about a query while both believing they had
// agreed.

// generateError reports a node the generator does not know how to write.
//
// A guard that rewrote a statement and then emitted something subtly different
// would be worse than one that refused, so an unknown node stops the rewrite
// rather than producing an approximation.
type generateError struct{ what string }

func (e *generateError) Error() string { return "sqlglot-go: cannot generate SQL for " + e.what }

// Generate renders a tree as SQL in a dialect.
func Generate(e *Expression, dialect string) (string, error) {
	cfg, ok := ConfigFor(dialect)
	if !ok {
		return "", fmt.Errorf("sqlglot: no generator for dialect %q (have %v)", dialect, Dialects())
	}
	g := &generator{cfg: cfg, tables: cfg.Tables, dialect: dialect}
	out := g.node(e)
	if g.err != nil {
		return "", g.err
	}
	return out, nil
}

// generator holds the first failure rather than threading an error through
// every writer. Sixty writers each propagating an error they cannot act on is
// noise that buries the one line in each that says anything.
type generator struct {
	cfg     *Config
	tables  *ParserTables
	dialect string
	err     error
}

// fail records the first failure. Later writers keep running and return empty
// strings; Generate reports the failure rather than the half-written text.
func (g *generator) fail(what string) string {
	if g.err == nil {
		g.err = &generateError{what: what}
	}
	return ""
}

func (g *generator) node(e *Expression) string {
	if e == nil {
		return ""
	}
	if fn, ok := generators[e.Class]; ok {
		return fn(g, e)
	}
	if op, ok := g.tables.BinarySQL[e.Class]; ok {
		if e.Class == "And" || e.Class == "Or" {
			g.requireCondition(e, "this", "expression")
		}
		return g.binary(e, op)
	}
	if word, ok := g.tables.UnarySQL[e.Class]; ok {
		if e.Class == "Not" {
			g.requireCondition(e, "this")
		}
		return g.unary(e, word)
	}
	if spec, ok := g.functionSpelling(e); ok {
		return g.namedFunction(e, spec)
	}
	return g.fail(e.Class)
}

// child renders one arg, or "" when it is absent.
func (g *generator) child(e *Expression, key string) string {
	c, _ := e.Args[key].(*Expression)
	return g.node(c)
}

// list renders the `expressions` arg, the only repeated arg rendered this way
// -- CASE renders its own branches, because exp.If is written one way as a
// branch and another as a standalone call.
func (g *generator) list(e *Expression) string {
	items, _ := e.Args["expressions"].([]*Expression)
	parts := make([]string, 0, len(items))
	for _, item := range items {
		parts = append(parts, g.node(item))
	}
	return strings.Join(parts, ", ")
}

func (g *generator) binary(e *Expression, op string) string {
	// Both operands, or nothing. A class can be a binary operator in one
	// dialect and a function in another -- DuckDB writes GLOB(x), T-SQL has
	// only the operator spelling -- so a node BUILT as a call can reach here
	// with no left-hand side. Writing it anyway produced
	// `SELECT * FROM main. GLOB '/**'`, which is not SQL at all, from a
	// statement the reference simply refuses.
	this, _ := e.Args["this"].(*Expression)
	other, _ := e.Args["expression"].(*Expression)
	if this == nil || other == nil {
		return g.fail(e.Class + " written as an operator without two operands")
	}
	return g.node(this) + " " + op + " " + g.node(other)
}

// unary writes a prefix operator. The spelling carries its own spacing --
// "NOT " has a trailing space where "-" does not -- because that is how the
// reference's generator writes them, and the probe read it back verbatim.
func (g *generator) unary(e *Expression, word string) string {
	this := g.child(e, "this")
	if g.wouldFuse(word, this) {
		return word + " " + this
	}
	return word + this
}

// wouldFuse reports whether writing an operator straight against its operand
// would lex as something other than that operator.
//
// Asked of the TOKENIZER rather than of the characters. The character version
// -- space it when the operand starts with the operator's own last character
// -- covers `- -5`, where `--5` opens a comment and swallows the rest of the
// line, and misses `~ *`, where `~*` is a single operator in its own right.
// Those are the same bug, and only the trie knows the whole list.
//
// Found by fuzzing the generator's output back through the parser: `~ *` came
// out as `~*` and read back as one token, which is a different statement.
func (g *generator) wouldFuse(word, operand string) bool {
	if strings.HasSuffix(word, " ") || operand == "" {
		return false
	}
	first := []rune(operand)[0]
	tk, err := NewTokenizer(g.dialect)
	if err != nil {
		// No tokenizer to ask; keep the old, narrower rule rather than guess.
		return strings.HasPrefix(operand, word[len(word)-1:])
	}
	toks, err := tk.Tokenize(word + string(first))
	if err != nil || len(toks) == 0 {
		// Unlexable joined -- `--` opens a comment and eats the operand. A
		// space is the fix in every such case.
		return true
	}
	return toks[0].Text != word
}

// functionSpelling picks how this node is written. A class can be written more
// than one way depending on its arguments -- COUNT_BIG rather than COUNT, MAX
// of one argument rather than GREATEST of two -- and the candidates are
// ordered most-constrained first, so the first whose constraints all hold is
// the right one.
func (g *generator) functionSpelling(e *Expression) (FuncSQL, bool) {
	for _, candidate := range g.tables.FunctionSQL[e.Class] {
		matches := true
		for _, c := range candidate.Consts {
			if !sameConst(e.Args[c.Key], c.Value) {
				matches = false
				break
			}
		}
		if matches {
			return candidate, true
		}
	}
	return FuncSQL{}, false
}

// sameConst compares an argument against a constant the spelling requires,
// treating an absent argument and a nil requirement as the same thing.
func sameConst(got, want any) bool {
	if got == nil || want == nil {
		return got == nil && want == nil
	}
	return got == want
}

// namedFunction writes a node the parser built from a function name, using the
// keyword and the argument keys it was built from -- so the round trip lands
// back on the same node.
func (g *generator) namedFunction(e *Expression, spec FuncSQL) string {
	if spec.NoParens {
		return spec.Name
	}
	parts := []string{}
	for _, key := range spec.Keys {
		switch v := e.Args[key].(type) {
		case *Expression:
			parts = append(parts, g.node(v))
		case []*Expression:
			for _, item := range v {
				parts = append(parts, g.node(item))
			}
		}
	}
	// Some names are written differently when an argument's type is VISIBLE:
	// DuckDB's BIT_OR over an explicitly non-integer argument becomes
	// `BIT_OR(CAST(ROUND(CAST(x AS REAL)) AS INT))`. The coercion is what makes
	// the statement mean the same thing on the engine, and the port does not
	// have it, so a call that would need it is refused rather than written
	// without it. A bare column is left alone -- the reference cannot type
	// that one either.
	if indexes, ok := g.tables.CastSensitiveArgs[strings.ToUpper(spec.Name)]; ok {
		for _, i := range indexes {
			if i < len(argNodes(e, spec)) && isCastToNonInteger(argNodes(e, spec)[i]) {
				return g.fail("cast argument to " + spec.Name)
			}
		}
	}
	return spec.Name + "(" + strings.Join(parts, ", ") + ")"
}

// argNodes flattens a call's arguments in the order they are written, so an
// index from CastSensitiveArgs lines up with the argument the caller passed.
func argNodes(e *Expression, spec FuncSQL) []*Expression {
	out := []*Expression{}
	for _, key := range spec.Keys {
		switch v := e.Args[key].(type) {
		case *Expression:
			out = append(out, v)
		case []*Expression:
			out = append(out, v...)
		}
	}
	return out
}

// isCastToNonInteger reports whether an argument asserts a type that is not an
// integer -- the trigger the reference itself uses.
func isCastToNonInteger(e *Expression) bool {
	if e == nil || (e.Class != "Cast" && e.Class != "TryCast") {
		return false
	}
	to, _ := e.Args["to"].(*Expression)
	if to == nil {
		return false
	}
	kind, _ := to.Args["this"].(DataTypeKind)
	switch kind {
	case "INT", "BIGINT", "SMALLINT", "TINYINT", "UINT", "UBIGINT", "USMALLINT", "UTINYINT", "INT128", "INT256":
		return false
	}
	return true
}

// requireCondition stops the generator where the dialect would rewrite a value
// used as a condition into `x <> 0`.
//
// The port does not perform that rewrite, and emitting the uncoerced form
// would not be a near miss -- T-SQL rejects a bare column as a condition. So
// this refuses, and the guard refuses the statement, which is what a guard is
// for.
func (g *generator) requireCondition(e *Expression, keys ...string) {
	if !g.tables.CoercesBooleans {
		return
	}
	for _, key := range keys {
		child, _ := e.Args[key].(*Expression)
		if child != nil && !predicates[child.Class] {
			g.fail(child.Class + " used as a condition in a dialect that coerces it")
		}
	}
}

// predicates are the nodes that are already a condition. Everything else needs
// coercing before a dialect without a boolean type will accept it there.
var predicates = map[string]bool{
	"EQ": true, "NEQ": true, "GT": true, "GTE": true, "LT": true, "LTE": true,
	"NullSafeEQ": true, "NullSafeNEQ": true, "And": true, "Or": true, "Not": true,
	"Is": true, "In": true, "Between": true, "Like": true, "ILike": true,
	"RegexpLike": true, "Glob": true, "SimilarTo": true, "Exists": true, "Paren": true,
}

// FunctionName is the name a function node is written with in a dialect, and
// whether the node is a function at all.
//
// It exists for the layer above: a guard denying OPENROWSET or xp_cmdshell has
// to ask what a node is called, and the answer is not on the node -- a Count
// does not remember that it was written COUNT, and in T-SQL with big_int set
// it is written COUNT_BIG.
func FunctionName(e *Expression, dialect string) (string, bool) {
	if e == nil {
		return "", false
	}
	if e.Class == "Anonymous" {
		name, _ := anonymousName(e)
		return name, name != ""
	}
	cfg, ok := ConfigFor(dialect)
	if !ok {
		return "", false
	}
	g := &generator{cfg: cfg, tables: cfg.Tables, dialect: dialect}
	spec, ok := g.functionSpelling(e)
	if !ok {
		return "", false
	}
	return spec.Name, true
}

// anonymousName is the name of a call, and whether it was written quoted.
//
// The reference stores an UNQUOTED name as a plain string and a quoted one as
// an Identifier node, and the two are different trees. Every reader of that
// arg goes through here, because there are three of them and the one that
// matters is the guard's: FunctionName feeds the denied-call check, so a
// reader that understood only the string form would stop seeing
// `[xp_cmdshell]()` the moment the parser started recording the quoting.
func anonymousName(e *Expression) (name string, quoted bool) {
	switch v := e.Args["this"].(type) {
	case string:
		return v, false
	case *Expression:
		if v != nil && v.Class == "Identifier" {
			s, _ := v.Args["this"].(string)
			q, _ := v.Args["quoted"].(bool)
			return s, q
		}
	}
	return "", false
}

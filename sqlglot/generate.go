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

// list renders a repeated arg.
func (g *generator) list(e *Expression, key, sep string) string {
	items, _ := e.Args[key].([]*Expression)
	parts := make([]string, 0, len(items))
	for _, item := range items {
		parts = append(parts, g.node(item))
	}
	return strings.Join(parts, sep)
}

func (g *generator) binary(e *Expression, op string) string {
	return g.child(e, "this") + " " + op + " " + g.child(e, "expression")
}

// unary writes a prefix operator. The spelling carries its own spacing --
// "NOT " has a trailing space where "-" does not -- because that is how the
// reference's generator writes them, and the probe read it back verbatim.
func (g *generator) unary(e *Expression, word string) string {
	this := g.child(e, "this")
	// `- -5` needs the space: without it the two operators would lex as one.
	if !strings.HasSuffix(word, " ") && this != "" && strings.HasPrefix(this, word[len(word)-1:]) {
		return word + " " + this
	}
	return word + this
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
	return spec.Name + "(" + strings.Join(parts, ", ") + ")"
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

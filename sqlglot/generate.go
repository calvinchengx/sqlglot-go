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
	// conditions are the Boolean nodes that sit where a CONDITION is wanted.
	// T-SQL writes those `(1 = 1)` and a plain value `1`, so which one to
	// write is a fact about the node's position rather than about the node.
	conditions map[*Expression]bool
	// inCallArgs marks the arguments of a CALL being written, where a named
	// argument is spelled `k := v` rather than as a struct field.
	inCallArgs bool
	// inColumnList marks a table's COLUMN list, where a definition is spelled
	// `a STRING` rather than as the struct field `a: STRING`.
	inColumnList bool
	// pathOwner is the extraction class whose path is being written, because
	// a path's separators depend on the call it appears in.
	pathOwner string
	// mergeTarget are the columns whose qualifier names the MERGE's target,
	// which some dialects leave off: the assigned side of a branch can name
	// nothing else, so the qualifier is noise there and written nowhere.
	mergeTarget map[*Expression]bool
	// wroteDollar records that a dollar has gone into the output already, so
	// a later bare name holding one would pair with it; see
	// lexesBackAsOneName.
	wroteDollar bool
	// coerced holds the nodes standing where a CONDITION is wanted in a
	// dialect that has no boolean type, so each is compared into one.
	coerced map[*Expression]bool
	// spellKeepsZeros holds while a PART of a call is being written: the
	// arguments after it are still to come, so an argument the dialect would
	// drop from a SHORTER call has to stay. DuckDB writes
	// `REGEXP_EXTRACT(a, p)` for a zero group and
	// `REGEXP_EXTRACT(a, p, 0, 'i')` for the same group beside a flag.
	spellKeepsZeros bool
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
	// A value standing where a CONDITION is wanted, in a dialect with no
	// boolean type: the comparison goes around whatever it renders as.
	if g.coerced[e] {
		delete(g.coerced, e)
		return strings.ReplaceAll(g.tables.ConditionCoercion, "{value}", g.node(e))
	}
	// A FORMAT is spelled three ways: through the dialect's own mapping (the
	// tree stores `%Y-%m-%d` and PostgreSQL writes `YYYY-MM-DD`), as stored,
	// or through a table of the dialect's own that this port does not have.
	// Which of the three is the CLASS's, and is probed -- PostgreSQL's
	// TO_CHAR translates and its STR_TO_UNIX does not.
	//
	// The probe that records how each class is WRITTEN fills the slot with a
	// placeholder, where none of this happens, so the template it produced
	// writes the stored spelling verbatim -- a different format string, not
	// another way of writing one.
	if spelling, known := g.tables.FormatSpellings[e.Class]; known {
		if format, _ := e.Args["format"].(*Expression); isStringLiteral(format) {
			switch spelling {
			case "inverse":
				text, _ := format.Args["this"].(string)
				if spelled := formatTime(text, g.tables.InverseTimeMapping); spelled != text {
					e = e.shallowCopy()
					e.Set("format", New("Literal",
						Arg{"this", spelled}, Arg{"is_string", true}))
				}
			case "default-dropped":
				// A format that is the dialect's OWN default is written as
				// nothing: Databricks spells `FROM_UNIXTIME(x)` for the
				// format it would otherwise put down in full.
				text, _ := format.Args["this"].(string)
				if text == g.tables.DefaultTimeFormat {
					e = e.shallowCopy()
					e.Set("format", nil)
					break
				}
				// Any other format has to be spelled the dialect's way, and
				// the spelling is only safe where it means the same thing
				// read back: this dialect keeps more than one table of them,
				// and a format written from the wrong one says something
				// else. Mapping out and back in again is the test.
				spelled := formatTime(text, g.tables.InverseTimeMapping)
				if spelled == text || formatTime(spelled, g.tables.TimeMapping) != text {
					return g.fail(e.Class + " whose format this dialect spells its own way")
				}
				e = e.shallowCopy()
				e.Set("format", New("Literal",
					Arg{"this", spelled}, Arg{"is_string", true}))
			case "other":
				// A table of the dialect's own: Databricks spells a TO_DATE
				// format `yyyy-M-d` and a DATE_FORMAT one `yyyy-MM-dd`, both
				// from the stored `%Y-%m-%d`. Writing either spelling for the
				// other says something else -- so the port writes only what
				// it can VERIFY, by mapping out and back in again and keeping
				// the result where it lands on what was stored.
				text, _ := format.Args["this"].(string)
				spelled := formatTime(text, g.tables.InverseTimeMapping)
				if spelled == text || formatTime(spelled, g.tables.TimeMapping) != text {
					return g.fail(e.Class + " whose format this dialect spells its own way")
				}
				e = e.shallowCopy()
				e.Set("format", New("Literal",
					Arg{"this", spelled}, Arg{"is_string", true}))
			}
		}
	}
	if fn, ok := generators[e.Class]; ok {
		return fn(g, e)
	}
	return g.spell(e)
}

// spell writes a node by the generic routes -- operators, function spellings,
// templates -- skipping the per-class writers. A writer that has decided what
// SHAPE to write calls this to have that shape spelled.
func (g *generator) spell(e *Expression) string {
	if op, ok := g.tables.BinarySQL[e.Class]; ok {
		if e.Class == "And" || e.Class == "Or" {
			g.requireCondition(e, "this", "expression")
		}
		return g.binary(e, op)
	}
	// A range operator is a binary node with its own spelling: PostgreSQL's
	// `@>`, `&&`, `-|-` and the rest, probed alongside the parser's table for
	// them so reading and writing cannot drift apart.
	if op, ok := g.tables.BinaryRangeSQL[e.Class]; ok {
		return g.binary(e, op)
	}
	// And so is a JSON operator, in the dialect that READS it as one. The
	// same node is a function call elsewhere, which the function route below
	// spells -- so this table is empty for every dialect but PostgreSQL.
	if op, ok := g.tables.JSONOperatorSQL[e.Class]; ok {
		return g.jsonOperator(e, op)
	}
	if word, ok := g.tables.UnarySQL[e.Class]; ok {
		if e.Class == "Not" {
			g.requireCondition(e, "this")
			return g.unaryOperand(e, word)
		}
		return g.unary(e, word)
	}
	if spec, ok := g.functionSpelling(e); ok {
		return g.namedFunction(e, spec)
	}
	// Last: a template the reference itself produced for this class, this
	// dialect and this set of arguments. Everything without one is refused.
	if out, ok := g.syntaxTemplate(e); ok {
		return out
	}
	return g.fail(e.Class)
}

// child renders one arg, or "" when it is absent.
func (g *generator) child(e *Expression, key string) string {
	c, _ := e.Args[key].(*Expression)
	return g.node(c)
}

// childOperand renders the `this` arg as the OPERAND of an operator,
// parenthesised where this dialect's spelling would otherwise re-associate.
func (g *generator) childOperand(e *Expression) string {
	c, _ := e.Args["this"].(*Expression)
	return g.operand(c)
}

// list renders the `expressions` arg, the only repeated arg rendered this way
// -- CASE renders its own branches, because exp.If is written one way as a
// branch and another as a standalone call.
// callList renders a call's arguments, where a named argument takes its own
// spelling.
func (g *generator) callList(e *Expression) string {
	was := g.inCallArgs
	g.inCallArgs = true
	out := g.list(e)
	g.inCallArgs = was
	return out
}

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
	return g.operand(this) + " " + op + " " + g.operand(other)
}

// operand writes one side of a binary operator, parenthesising it if this
// dialect spells it as an operator that would otherwise re-associate. Only the
// JSON extractions need it so far, and only where they are written with an
// arrow: DuckDB writes `(a -> b) & c` and Databricks `a:b & c` for the same
// tree, so which one it is was probed rather than inferred from the template.
func (g *generator) operand(e *Expression) string {
	if e != nil && g.tables.JSONExtractNeedsParens[e.Class] {
		return "(" + g.node(e) + ")"
	}
	return g.node(e)
}

// unaryOperand writes a prefix operator over an OPERAND -- the same rule the
// binary writer uses, and the reference applies it to the same four parents:
// a binary operator, a subscript, an IN and a NOT.
func (g *generator) unaryOperand(e *Expression, word string) string {
	this := g.childOperand(e)
	if g.wouldFuse(word, this) {
		return word + " " + this
	}
	return word + this
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
// parserWouldRefuse reports whether the port's parser declines this function
// name because the reference builds it with something the probe could not
// read -- a builder that inspects the arguments it is given.
func (g *generator) parserWouldRefuse(name string) bool {
	if name == "" {
		return false
	}
	// The parser's own order: a name with a syntax of its own is handled
	// before this question is even asked. Leaving SyntaxFunctions out made
	// the guard refuse CONVERT, TRIM, EXTRACT and SUBSTRING -- 136
	// statements, none of which the parser has any trouble with.
	if _, ok := g.tables.SyntaxFunctions[name]; ok {
		return false
	}
	if _, ok := g.tables.Functions[name]; ok {
		return false
	}
	if _, ok := g.tables.FunctionsByArity[name]; ok {
		return false
	}
	_, custom := g.tables.NamedFunctions[name]
	return custom
}

// dropsAnArgument reports whether this spelling leaves out an argument the
// node actually carries: a key it names as a CONSTANT rather than writing.
func (f FuncSQL) dropsAnArgument(e *Expression) bool {
	written := map[string]bool{}
	for _, key := range f.Keys {
		written[key] = true
	}
	for _, c := range f.Consts {
		if written[c.Key] {
			continue
		}
		if child, _ := e.Args[c.Key].(*Expression); child != nil {
			return true
		}
	}
	return false
}

// listLength is how many members the keys hold between them, counting a
// single node as one.
func listLength(e *Expression, keys []string) int {
	n := 0
	for _, key := range keys {
		switch v := e.Args[key].(type) {
		case *Expression:
			if v != nil {
				n++
			}
		case []*Expression:
			n += len(v)
		}
	}
	return n
}

func (g *generator) functionSpelling(e *Expression) (FuncSQL, bool) {
	for _, candidate := range g.tables.FunctionSQL[e.Class] {
		// Never write a call the PARSER would refuse to read. T-SQL spells a
		// Sha as `HASHBYTES('SHA1', x)`, and HASHBYTES is a builder that
		// inspects its first argument to decide the class -- which the port
		// refuses rather than half-implement. Writing it anyway produced SQL
		// the port could not read back, which the generator fuzzer found and
		// the adjudicator called the port's own.
		//
		// The condition here is the parser's, mirrored: a name that has a
		// custom builder and no plain signature.
		if g.parserWouldRefuse(candidate.Name) {
			continue
		}
		matches := true
		for _, c := range candidate.Consts {
			if !g.sameConst(e.Args[c.Key], c.Value) {
				matches = false
				break
			}
		}
		// Every key the candidate names must be PRESENT. The spelling was
		// recorded for exactly this arity: Databricks writes a bare
		// GroupConcat as `LISTAGG(x, ',')`, supplying a separator the tree
		// does not carry, so no spelling was recorded for the one-argument
		// form. Applying the two-argument spelling anyway silently wrote
		// `LISTAGG(x)` -- a shorter call than the reference emits.
		// While a PART of a call is being written, a spelling that leaves an
		// argument OUT is the wrong one: the arguments after it are still to
		// come, and something has to hold its place. DuckDB records
		// `REGEXP_EXTRACT(a, p)` for a zero group, which is right on its own
		// and wrong in front of a flag.
		if g.spellKeepsZeros && candidate.dropsAnArgument(e) {
			continue
		}
		// A spelling recorded for calls of at least N arguments does not
		// apply to a narrower one: T-SQL writes `CONCAT(a, b)` for two and
		// just `a` for one, and writing the call form for one would put a
		// call where the reference puts none.
		if candidate.MinArgs > 0 && listLength(e, candidate.Keys) < candidate.MinArgs {
			continue
		}
		for _, key := range candidate.Keys {
			switch v := e.Args[key].(type) {
			case nil:
				matches = false
			case *Expression:
				if v == nil {
					matches = false
				}
			case []*Expression:
				if len(v) == 0 {
					matches = false
				}
			}
			if !matches {
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
func (g *generator) sameConst(got, want any) bool {
	// A constant the builder supplies as a NODE, compared by what it WRITES:
	// SHA2 carries length as Literal("384"), and the spelling that selects
	// SHA384 over SHA256 is conditioned on that text.
	if child, ok := got.(*Expression); ok && child != nil {
		text, isText := want.(string)
		return isText && g.node(child) == text
	}
	if flag, ok := want.(bool); ok {
		// A flag condition compares to the flag, and an ABSENT arg is false:
		// the reference sets these lazily and a template conditioned on false
		// must still match a node that never had the key at all.
		got, _ := got.(bool)
		return got == flag
	}
	if got == nil || want == nil {
		return got == nil && want == nil
	}
	return got == want
}

// namedFunction writes a node the parser built from a function name, using the
// keyword and the argument keys it was built from -- so the round trip lands
// back on the same node.
// refuseSensitive reports whether this node must be refused because an
// argument would change how the reference writes the call. Keyed by CLASS and
// arg KEY, so it applies whether the node is written by its function spelling
// or by a template -- the template writer used to skip these entirely, which
// is how a DATE_DIFF over date STRINGS came out without the casts the
// reference adds.
func (g *generator) refuseSensitive(e *Expression) bool {
	arity := argCount(e)
	for _, key := range g.tables.CastSensitiveArgs[e.Class][arity] {
		child, _ := e.Args[key].(*Expression)
		if !isCastToNonInteger(child) {
			continue
		}
		// A slot is not sensitive to casting as such, but to being cast to
		// particular TYPES: DuckDB wraps a non-text argument to UPPER in a
		// cast to TEXT and leaves one that is already TEXT alone. Where the
		// types are known, only those are refused.
		if types := g.tables.CastSensitiveTypes[e.Class][arity][key]; len(types) > 0 {
			if !castsToOneOf(child, types) {
				continue
			}
			// The coercion the dialect applies here is IDEMPOTENT where the
			// argument already carries it: DuckDB writes
			// `BOOL_OR(CAST(x AS BOOLEAN))` whatever it is given, so an
			// argument that IS that cast leaves it nothing to add. The
			// spelling recorded for exactly that shape writes it as it
			// stands, so this is not a refusal.
			if types := g.tables.CastIdempotentTypes[e.Class][key]; castsToOneOf(child, types) {
				continue
			}
			// And where the WRAPPER the dialect puts round an argument of
			// this type was measured, the port writes that instead of
			// refusing: DuckDB rounds a float into a BIT_OR and casts a
			// decimal without rounding, and both are recorded.
			if _, known := g.tables.CastCoercions[e.Class][arity][key][castTargetOf(child)]; known {
				continue
			}
			// A coercion the dialect applies to EVERY one of these slots is
			// absorbed when every one of them already carries it: Databricks
			// casts both ends of a SEQUENCE to DATE, and a call whose ends
			// are both DATE casts leaves it nothing to add.
			if g.everySensitiveSlotIsCast(e, arity) {
				continue
			}
		}
		g.fail("cast argument to " + e.Class)
		return true
	}
	// A zero this dialect DROPS is not a refusal: it is written by leaving
	// the argument out, which namedFunction does below. Checked before the
	// refusals so a slot that merely drops a zero does not turn away every
	// other literal in it.
	for _, key := range g.tables.DropsZeroArgs[e.Class][arity] {
		if child, _ := e.Args[key].(*Expression); isZero(child) {
			return false
		}
	}
	for _, key := range g.tables.ZeroSensitiveArgs[e.Class][arity] {
		if child, _ := e.Args[key].(*Expression); !isLiteral(child) {
			continue
		}
		// A literal in one of these slots may still be one the dialect has a
		// SPELLING for: DuckDB's UnixToTime writes EPOCH_MS at scale 3 and
		// MAKE_TIMESTAMP at 6, and only the scales it has no name for become
		// a division the port cannot write. A spelling whose constants match
		// this node was measured against a real rendering of exactly this
		// shape, so it is trusted; where none does, the refusal stands.
		if _, ok := g.functionSpelling(e); ok {
			continue
		}
		g.fail("literal argument to " + e.Class)
		return true
	}
	return false
}

// argCount is how many arguments a call actually carries, which is what the
// sensitivity tables are keyed by: DuckDB wraps ROUND's second argument in a
// cast at four arguments and not at two.
func argCount(e *Expression) int {
	n := 0
	for _, value := range e.Args {
		switch v := value.(type) {
		case *Expression:
			if v != nil {
				n++
			}
		case []*Expression:
			n += len(v)
		}
	}
	return n
}

// dropsThisZero reports whether this argument is a literal zero the dialect
// leaves out entirely: DuckDB writes `REGEXP_EXTRACT(x, p)` for a zero group.
func (g *generator) dropsThisZero(e *Expression, arity int, key string) bool {
	if g.spellKeepsZeros {
		return false
	}
	for _, dropped := range g.tables.DropsZeroArgs[e.Class][arity] {
		if dropped == key {
			child, _ := e.Args[key].(*Expression)
			return isZero(child)
		}
	}
	return false
}

func (g *generator) namedFunction(e *Expression, spec FuncSQL) string {
	if spec.NoParens {
		return spec.Name
	}
	arity := argCount(e)
	parts := []string{}
	// These are a CALL's arguments, so a named one takes its own spelling.
	wasInCallArgs := g.inCallArgs
	g.inCallArgs = true
	defer func() { g.inCallArgs = wasInCallArgs }()
	for _, key := range spec.Keys {
		// A literal zero this dialect leaves out entirely: DuckDB writes
		// `REGEXP_EXTRACT(x, p)` for a zero group. Probed, not assumed.
		if g.dropsThisZero(e, arity, key) {
			continue
		}
		switch v := e.Args[key].(type) {
		case *Expression:
			// An argument the dialect WRAPS by its type -- DuckDB rounds a
			// float into a BIT_OR and casts a decimal without rounding --
			// takes that wrapper here, where the call's arguments are laid
			// out.
			parts = append(parts, g.coerceArgument(e, key, g.node(v)))
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
	if out := g.refuseSensitive(e); out {
		return ""
	}
	// Keyed by ARITY as well as index: DuckDB wraps ROUND's second argument in
	// a cast when the call has four arguments and not when it has two, and
	// applying that at every arity refused ordinary two-argument calls.
	return spec.Name + "(" + strings.Join(parts, ", ") + ")"
}

// isLiteral reports whether an argument is a literal of either kind. A literal
// can change the call as much as a cast can: DuckDB drops a zero group from
// REGEXP_EXTRACT entirely, and wraps a date STRING in a cast the tree does not
// carry -- both make the written call differ from the one the reference emits.
func isLiteral(e *Expression) bool {
	return e != nil && e.Class == "Literal"
}

// isCastToNonInteger reports whether an argument asserts a type that is not an
// integer -- the trigger the reference itself uses.
// everySensitiveSlotIsCast reports whether every slot this dialect coerces on
// this node already carries a cast to a type it coerces to.
//
// The dialect applies the coercion across the slots together -- Databricks
// casts both ends of a SEQUENCE when either is a date -- so it is absorbed
// only when they all carry it, and the plain spelling then says everything.
func (g *generator) everySensitiveSlotIsCast(e *Expression, arity int) bool {
	for _, key := range g.tables.CastSensitiveArgs[e.Class][arity] {
		// A slot the node leaves empty asks nothing; one it fills has to
		// carry the cast, or the dialect has something left to add.
		child, _ := e.Args[key].(*Expression)
		if child != nil &&
			!castsToOneOf(child, g.tables.CastSensitiveTypes[e.Class][arity][key]) {
			return false
		}
	}
	return true
}

// castTargetOf is the type a cast names, or "" where the node is not one.
func castTargetOf(e *Expression) string {
	if e == nil || (e.Class != "Cast" && e.Class != "TryCast") {
		return ""
	}
	if to, _ := e.Args["to"].(*Expression); to != nil {
		kind, _ := to.Args["this"].(DataTypeKind)
		return string(kind)
	}
	return ""
}

// castsToOneOf reports whether a cast's target is one of the named types.
func castsToOneOf(e *Expression, types []string) bool {
	to, _ := e.Args["to"].(*Expression)
	if to == nil {
		return false
	}
	kind, _ := to.Args["this"].(DataTypeKind)
	for _, want := range types {
		if string(kind) == want {
			return true
		}
	}
	return false
}

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

// requireCondition marks the values standing where a CONDITION is wanted, in a
// dialect that has no boolean type.
//
// T-SQL rejects a bare column there, so the reference compares it into one:
// `NOT c` is written `NOT c <> 0`. A boolean has a spelling of its own --
// `(1 = 1)` -- and everything else takes the comparison around whatever it
// renders as.
func (g *generator) requireCondition(e *Expression, keys ...string) {
	if !g.tables.CoercesBooleans {
		return
	}
	for _, key := range keys {
		child, _ := e.Args[key].(*Expression)
		if child == nil {
			continue
		}
		// A boolean HERE has a spelling -- `(1 = 1)` -- so it is marked rather
		// than refused. Everything else still is refused: the port does not
		// perform the coercion the dialect would.
		if child.Class == "Boolean" {
			g.markCondition(child)
			continue
		}
		// Everything else is COMPARED into one. The mark is on the node, and
		// the comparison goes around whatever it renders as.
		if !predicates[child.Class] {
			if g.coerced == nil {
				g.coerced = map[*Expression]bool{}
			}
			g.coerced[child] = true
		}
	}
}

// markCondition records that a node is being written where a condition is
// wanted. Only booleans care, and only in a dialect that has no boolean.
func (g *generator) markCondition(e *Expression) {
	if e == nil || e.Class != "Boolean" {
		return
	}
	if g.conditions == nil {
		g.conditions = map[*Expression]bool{}
	}
	g.conditions[e] = true
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

// jsonOperator writes one of PostgreSQL's JSON operators.
//
// Its right-hand side is PARENTHESISED where it is an operator of its own --
// a binary, a predicate or a NOT. The parentheses are not in the tree: the
// same node written as a function call carries none, and `JSONB_EXTRACT(a, n
// IN (1, 2))` has to come back as `a #> (n IN (1, 2))` or it reads as
// `(a #> n) IN (1, 2)`, which asks a different question.
func (g *generator) jsonOperator(e *Expression, op string) string {
	this, _ := e.Args["this"].(*Expression)
	other, _ := e.Args["expression"].(*Expression)
	if this == nil || other == nil {
		return g.fail(e.Class + " written as an operator without two operands")
	}
	// Left first, the order the text is READ in. Some of what a writer records
	// while rendering is positional -- a dollar already written pairs with the
	// next one and opens a quote -- so rendering the right operand first let a
	// name that was safe on its own be written where it no longer was.
	left := g.operand(this)
	right := g.node(other)
	if isA("Binary", other) || isA("Predicate", other) || other.Class == "Not" {
		right = "(" + right + ")"
	}
	return left + " " + op + " " + right
}

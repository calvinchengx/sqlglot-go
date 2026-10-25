package sqlglot

import (
	"regexp"
	"strconv"
	"strings"
)

// timeZoneRE is the reference's own TIME_ZONE_RE: a colon somewhere in the
// literal, followed eventually by a letter or a sign -- `11:22:29 Europe/
// Prague` and `11:22:29+05:00` both match; a plain `11:22:29` does not.
var timeZoneRE = regexp.MustCompile(`:.*?[a-zA-Z+\-]`)

func timeZoneInLiteral(s string) bool {
	return timeZoneRE.MatchString(s)
}

// The expression grammar: precedence climbing, in the reference's shapes.
//
// Everything this file does not recognise is refused, not guessed. That is the
// rule the whole port runs on -- a construct parsed into a plausible-looking
// wrong tree is worse than one refused, because the guard above would then
// reason about something the engine will not execute.

// The reference's precedence chain, level for level, reading the operator
// tables off the dialect. The levels are not interchangeable: EQUALITY sits
// above COMPARISON, so `a = b > c` is `a = (b > c)`; MOD sits with the additive
// operators rather than the multiplicative ones, so `a % b * c` is
// `a % (b * c)`. Nor are the tables: DuckDB reads `^` as Pow where the default
// reads it as BitwiseXor. A port that collapsed any of these would parse most
// statements correctly and a few silently wrong.

// specialConstruction are operator classes the reference builds with more than
// a left and a right operand. Div and DPipe are handled; the rest are refused
// rather than built without the arguments that give them meaning.
var specialConstruction = map[string]bool{
	"Div":   true,
	"DPipe": true,
	// Databricks' `a:b` reads the right-hand side as a JSON PATH, not as the
	// column the generic operator rule produces. `->` and `->>` are handled
	// in parseColumnOps or parseBitwise according to the probe, and never
	// reach here; the colon form has its own grammar which is not ported, so
	// it stays refused.
	"JSONExtract":       true,
	"JSONExtractScalar": true,
	// Databricks' `a:b` reads the right-hand side as a JSON PATH, not as the
	// column the generic rule produces. The port modelled neither the path
	// node nor its grammar, and built JSONExtract(a, Column(b)) anyway -- a
	// tree the reference never makes, and one the generator could not write
	// back at all. A construct this port cannot read is a refusal; building
	// something plausible instead is the one thing it must not do.
	//
	// Found by the fuzzed differential: the largest divergence cluster it
	// reported. Porting JSONPath properly is Tier 2 work.
}

func (p *parser) parseExpression() (*Expression, error) { return p.parseExpressionAfterDAX() }

func (p *parser) parseAssignment() (*Expression, error) {
	this, err := p.disjunctionOrObjectAgg()
	if err != nil {
		return nil, err
	}
	if p.at(TokCOLON_EQ) {
		// `f(name := value)` is a NAMED ARGUMENT, and the reference records
		// the name as a bare identifier -- the same PropertyEQ a struct field
		// uses.
		if p.inCallArgs {
			name := namedArgument(this)
			if name == nil {
				return nil, p.unsupported("a named argument whose name is not a name")
			}
			p.advance()
			value, err := p.parseDisjunction()
			if err != nil {
				return nil, err
			}
			return New("PropertyEQ", Arg{"this", name}, Arg{"expression", value}), nil
		}
		// Everywhere else, `:=` is the reference's own generic ASSIGNMENT
		// operator -- `SELECT x := 1` and `SELECT @v := 1` both read as a
		// PropertyEQ at plain expression level, confirmed universal across
		// every dialect already landed, not something MySQL alone does; the
		// restriction to call-argument position above was never something
		// the reference draws. A single-part, unqualified Column unwraps to
		// its own Identifier first -- `x := 1` names `x`, not a column
		// reference to it -- and anything else (a Parameter, a qualified
		// Column, another PropertyEQ from a longer chain) is kept exactly as
		// parsed, right-associatively.
		if this != nil && this.Class == "Column" {
			unqualified := true
			for _, key := range []string{"table", "db", "catalog"} {
				if part, _ := this.Args[key].(*Expression); part != nil {
					unqualified = false
					break
				}
			}
			if unqualified {
				if inner, _ := this.Args["this"].(*Expression); inner != nil {
					this = inner
				}
			}
		}
		p.advance()
		value, err := p.parseAssignment()
		if err != nil {
			return nil, err
		}
		return New("PropertyEQ", Arg{"this", this}, Arg{"expression", value}), nil
	}
	// ClickHouse's ternary: `cond ? a : b` is an If, read one level up from
	// the terms it joins (ClickHouseParser._parse_assignment).
	if p.dialect == "clickhouse" && p.at(TokPLACEHOLDER) {
		p.advance()
		trueE, err := p.parseAssignment()
		if err != nil {
			return nil, err
		}
		var falseE any = false
		if p.match(TokCOLON) {
			f, err := p.parseAssignment()
			if err != nil {
				return nil, err
			}
			falseE = f
		}
		return New("If", Arg{"this", this}, Arg{"true", trueE}, Arg{"false", falseE}), nil
	}
	return this, nil
}

func (p *parser) parseDisjunction() (*Expression, error) {
	return p.parseBinary(p.tables.Disjunction, p.parseConjunction)
}

func (p *parser) parseConjunction() (*Expression, error) {
	this, err := p.parseBinary(p.tables.Conjunction, p.parseEquality)
	if err != nil {
		return nil, err
	}
	// MySQL puts XOR at the conjunction tier, and its right-hand side is a
	// CONJUNCTION rather than an equality (MySQLParser._parse_conjunction).
	if isMySQLFamily(p.dialect) {
		for p.at(TokXOR) {
			p.advance()
			comments := p.takeComments()
			right, rerr := p.parseBinary(p.tables.Conjunction, p.parseEquality)
			if rerr != nil {
				return nil, rerr
			}
			this = putComments(New("Xor", Arg{"this", this}, Arg{"expression", right}), comments)
		}
	}
	return this, nil
}

func (p *parser) parseEquality() (*Expression, error) {
	return p.parseEqualityWith(true)
}

// parseEqualityWith is the reference's _parse_equality(parse_is=...): where IS
// binds at or below this tier, a postfix predicate is read HERE rather than
// inside the range tier, and can itself be an operand of a comparison.
func (p *parser) parseEqualityWith(parseIs bool) (*Expression, error) {
	this, err := p.parseComparison()
	if err != nil {
		return nil, err
	}
	for {
		if c := p.curr(); c != nil {
			if class, ok := p.tables.Equality[c.Type]; ok {
				p.advance()
				comments := p.takeComments()
				right, rerr := p.parseComparison()
				if rerr != nil {
					return nil, rerr
				}
				this = p.buildPredicate(class, this, right, comments, false)
				continue
			}
		}
		if parseIs && p.isPredicateAhead() {
			index := p.index
			ranged, rerr := p.parseRangeFrom(this, true)
			if rerr != nil {
				return nil, rerr
			}
			this, rerr = p.parseComparisonFrom(ranged)
			if rerr != nil {
				return nil, rerr
			}
			if p.index == index {
				break
			}
			continue
		}
		break
	}
	return this, nil
}

func (p *parser) parseComparison() (*Expression, error) {
	return p.parseBinary(p.tables.Comparison, p.parseRange)
}

// parseComparisonFrom continues a comparison chain from a node already read.
func (p *parser) parseComparisonFrom(this *Expression) (*Expression, error) {
	return p.parseBinaryFrom(this, p.tables.Comparison, p.parseRange)
}

// isPredicateAhead reports whether a postfix IS/ISNULL/NOTNULL (or `NOT NULL`)
// stands here and belongs to the EQUALITY tier rather than this one.
func (p *parser) isPredicateAhead() bool {
	if p.tables.ISPredicatePrecedence == "" || p.tables.ISPredicatePrecedence == "HIGH" {
		return false
	}
	c := p.curr()
	if c == nil {
		return false
	}
	switch c.Type {
	case TokIS, TokISNULL, TokNOTNULL:
		return true
	case TokNOT:
		n := p.next()
		return n != nil && n.Type == TokNULL
	}
	return false
}

// classInOps reports whether a node class is one of an operator table's.
func (p *parser) classInOps(ops map[TokenType]string, class string) bool {
	for _, c := range ops {
		if c == class {
			return true
		}
	}
	return false
}

// buildPredicate is the reference's _build_predicate: it preserves the
// grouping an IS (and the null-safe comparisons) needs, on either side, and
// braces a predicate that stands as an OPERAND of a comparison.
func (p *parser) buildPredicate(class string, this, expression *Expression, comments []string, negate bool) *Expression {
	isPredicate := class == "Is" || class == "NullSafeEQ" || class == "NullSafeNEQ"
	predicate := func(e *Expression) bool {
		return e != nil && (e.Class == "Is" || e.Class == "NullSafeEQ" || e.Class == "NullSafeNEQ")
	}
	if isPredicate {
		if this != nil && (isA("Connector", this) || this.Class == "Not" || this.Class == "NullSafeNEQ" ||
			p.classInOps(p.tables.Equality, this.Class) ||
			p.classInOps(p.tables.Comparison, this.Class) ||
			(class != "Is" && this.Class == "Is")) {
			this = New("Paren", Arg{"this", this})
		}
		if expression != nil && (isA("Predicate", expression) || isA("Connector", expression) ||
			expression.Class == "Not" || expression.Class == "Escape") {
			expression = New("Paren", Arg{"this", expression})
		}
	} else {
		inner := this
		if this != nil && this.Class == "Not" {
			inner = childOf(this, "this")
		}
		if predicate(inner) {
			this = New("Paren", Arg{"this", this})
		}
		innerE := expression
		if expression != nil && expression.Class == "Not" {
			innerE = childOf(expression, "this")
		}
		if predicate(innerE) {
			expression = New("Paren", Arg{"this", expression})
		}
	}
	node := New(class, Arg{"this", this}, Arg{"expression", expression})
	if negate {
		node.Set("negate", true)
	}
	return putComments(node, comments)
}

// parseRange handles IS, IN, BETWEEN and the LIKE family, including their
// negated forms.
//
// NOT is read here rather than at the unary level because `a NOT LIKE b` sets
// a flag on the Like node while `a NOT IN (…)` wraps the In in a Not -- the
// reference treats the two differently and so must this. A NOT that turns out
// not to introduce a range is put back.
func (p *parser) parseRange() (*Expression, error) {
	return p.parseRangeFrom(nil, false)
}

// parseRangeFrom reads the range tier, optionally CONTINUING from a node
// already read and optionally consuming a postfix IS that belongs here.
func (p *parser) parseRangeFrom(this *Expression, parseIs bool) (*Expression, error) {
	if this == nil {
		if prefixed, handled, err := p.oracleHierarchyPrefix(); handled {
			return prefixed, err
		}
		var err error
		this, err = p.parseJSONArrow()
		if err != nil {
			return nil, err
		}
	}

	var err error
	for {
		// Where IS binds below this tier it is not read here at all: the
		// EQUALITY tier takes it, and may then hand the result back.
		if !parseIs && p.isPredicateAhead() {
			return this, nil
		}
		negate := p.match(TokNOT)
		c := p.curr()
		if c == nil {
			if negate {
				p.index--
			}
			return this, nil
		}

		switch {
		// `a NOT NULL` is the negated IS NULL: the reference reads the NULL
		// after a NOT at this level and wraps the Is in a Not.
		case negate && c.Type == TokNULL:
			// `a NOT NULL` is a negated IS NULL; the negation wraps below.
			p.advance()
			this = p.buildPredicate("Is", this, New("Null"), nil, false)
		// PostgreSQL writes `x ISNULL` and `x NOTNULL` for the two IS NULL
		// tests. The first is exactly `IS NULL`; the second is a NEGATED Is
		// in the dialect that keeps one and a Not around an Is in the
		// dialects that normalise it -- one word, two trees.
		case p.dialect == "mysql" && p.atWords("SOUNDS", "LIKE"):
			// `a SOUNDS LIKE b` is SOUNDEX(a) = SOUNDEX(b); MySQL evaluates = and
			// IS left to right, so a following IS keeps the comparison whole.
			p.advance()
			p.advance()
			var right *Expression
			right, err = p.parseBitwise()
			if err == nil {
				this = New("EQ",
					Arg{"this", New("Soundex", Arg{"this", this})},
					Arg{"expression", New("Soundex", Arg{"this", right})})
				if p.at(TokIS) {
					this = New("Paren", Arg{"this", this})
				}
			}
		case c.Type == TokISNULL:
			p.advance()
			this = p.buildPredicate("Is", this, New("Null"), nil, false)
		case c.Type == TokNOTNULL:
			p.advance()
			if p.tables.NormalizeNotNull {
				this = New("Not", Arg{"this",
					p.buildPredicate("Is", this, New("Null"), nil, false)})
			} else {
				this = p.buildPredicate("Is", this, New("Null"), nil, true)
			}
		case c.Type == TokIS:
			p.advance()
			this, err = p.parseIs(this)
		case c.Type == TokGLOBAL:
			// ClickHouse's GLOBAL IN: the same In, flagged, with an optional
			// NOT in front (ClickHouseParser._parse_global_in). A GLOBAL that
			// does not introduce an IN is left where it is: the join syntax
			// reads it next (`GLOBAL ANY LEFT JOIN`), and consuming it here
			// would lose it.
			mark := p.index
			p.advance()
			negated := p.match(TokNOT)
			if !p.match(TokIN) {
				p.index = mark
				return this, nil
			}
			in, gerr := p.parseIn(this)
			if gerr != nil {
				return nil, gerr
			}
			in.Set("is_global", true)
			if negated {
				this = New("Not", Arg{"this", in})
			} else {
				this = in
			}
		case c.Type == TokIN:
			p.advance()
			this, err = p.parseIn(this)
		case c.Type == TokBETWEEN:
			p.advance()
			this, err = p.parseBetween(this)
		case p.tables.BinaryRangeOps[c.Type] != "":
			// Every range operator that is just a binary node, from the
			// probed table rather than a hand-written five. PostgreSQL alone
			// has a dozen -- `@>`, `&&`, `-|-`, `?&` -- and refusing the ones
			// nobody had listed cost 37 statements. IS, IN and BETWEEN have
			// shapes of their own and are matched above, before this.
			class := p.tables.BinaryRangeOps[c.Type]
			p.advance()
			_, swapped := p.tables.SwappedRangeOps[c.Type]
			_, listed := p.tables.ListedRangeOps[c.Type]
			var right *Expression
			right, err = p.parseJSONArrow()
			if err == nil {
				left := this
				if swapped {
					// The operands go the other way round: `x @@ y` is a
					// match of y over x.
					left, right = right, left
				}
				if listed {
					// One operand is held as a LIST of one, which is the
					// reference's shape rather than a second operand.
					this = New(class,
						Arg{"this", left}, Arg{"expressions", []*Expression{right}})
				} else {
					this = New(class, Arg{"this", left}, Arg{"expression", right})
				}
				this = wholeRegexpLike(p.dialect, this)
			}
		case c.Type == TokMEMBER_OF:
			// `x MEMBER OF(y)`: the right side is PARENTHESISED and
			// mandatorily so, unlike the generic binary range operators
			// this port reads through the probed table -- read by hand
			// rather than folded into that table for exactly that reason.
			p.advance()
			if !p.match(TokL_PAREN) {
				return nil, p.unsupported("MEMBER OF without its parenthesised argument")
			}
			var right *Expression
			right, err = p.parseExpression()
			if err == nil {
				if !p.match(TokR_PAREN) {
					return nil, p.unsupported("unclosed MEMBER OF")
				}
				this = New("JSONArrayContains", Arg{"this", this}, Arg{"expression", right})
			}
		case c.Type == TokOPERATOR:
			// PostgreSQL's `OPERATOR(schema.op)` names a custom operator by
			// SCHEMA and symbol rather than by a word, and the reference
			// reads the parenthesised name as raw TEXT -- every token
			// between the parens, concatenated with none of its own
			// whitespace -- rather than as an expression.
			p.advance()
			if !p.match(TokL_PAREN) {
				return nil, p.unsupported("OPERATOR without its name")
			}
			var op strings.Builder
			for p.curr() != nil && !p.at(TokR_PAREN) {
				op.WriteString(p.curr().Text)
				p.advance()
			}
			if !p.match(TokR_PAREN) {
				return nil, p.unsupported("unclosed OPERATOR name")
			}
			var right *Expression
			right, err = p.parseBitwise()
			if err == nil {
				this = New("Operator",
					Arg{"this", this}, Arg{"operator", op.String()}, Arg{"expression", right})
			}
		case c.Type == TokFOR:
			// DuckDB's own list comprehension: `[x FOR x IN l]`, or
			// `[x FOR x, i IN l IF i = 2]` naming the index too. FOR is a
			// range operator generally -- `SELECT x FOR UPDATE` reaches
			// here the same way -- so what follows decides: a name (or a
			// name, a name) and then IN commits to a comprehension, and
			// anything else retreats to before FOR, leaving it for
			// whatever reads it elsewhere.
			mark := p.index
			p.advance() // FOR
			comprehension, matched, cerr := p.parseComprehension(this)
			if cerr != nil {
				return nil, cerr
			}
			if !matched {
				p.index = mark
				if negate {
					p.index--
				}
				return this, nil
			}
			this = comprehension
		default:
			if _, isRange := p.tables.RangeTokens[c.Type]; isRange {
				// Exasol's REGEXP_LIKE is the one range operator whose node
				// carries a CONSTANT beside its two operands -- it is a FULL
				// match, so the reference records `full_match: true` on it
				// (ExasolParser.RANGE_PARSERS). The probe records an operator
				// only where the result is a plain two-argument binary, so
				// this one is skipped there and read here.
				if p.dialect == "exasol" && c.Type == TokRLIKE {
					p.advance()
					var right *Expression
					right, err = p.parseJSONArrow()
					if err != nil {
						return nil, err
					}
					this = New("RegexpLike", Arg{"this", this},
						Arg{"expression", right}, Arg{"full_match", true})
					break
				}
				return nil, p.unsupported("range operator " + c.Text)
			}
			if negate {
				p.index--
			}
			return this, nil
		}
		if err != nil {
			return nil, err
		}

		if negate {
			this = negateRange(this)
		}
		// A negation -- written, or a Not around an Is -- followed by another
		// range operator is parenthesised, so it cannot re-associate
		// (Parser._parse_range).
		if negate || (this.Class == "Not" && childOf(this, "this") != nil &&
			childOf(this, "this").Class == "Is") {
			if n := p.curr(); n != nil {
				_, isRange := p.tables.RangeTokens[n.Type]
				if n.Type == TokNOT || isRange {
					this = New("Paren", Arg{"this", this})
				}
			}
		}

		// `x LIKE 'y' ESCAPE '!'` WRAPS the comparison rather than adding an
		// argument to it, so the escape character is a node of its own -- and
		// it wraps the NEGATION too: `x NOT ILIKE 'y' ESCAPE '#'` is an Escape
		// over a Not, not a Not over an Escape.
		if p.match(TokESCAPE) {
			char, cerr := p.parseBitwise()
			if cerr != nil {
				return nil, cerr
			}
			this = New("Escape", Arg{"this", this}, Arg{"expression", char})
		}
	}
}

// parseComprehension reads what follows a FOR already consumed: a name (or a
// name, a name naming the index too), then IN and what is iterated, then an
// optional IF condition. Returning matched=false leaves the cursor where the
// caller put it (right after FOR) for it to retreat from -- IN never
// standing where expected means this was not a comprehension at all, the
// same as the reference's own retreat.
func (p *parser) parseComprehension(this *Expression) (*Expression, bool, error) {
	expression, err := p.parseColumn()
	if err != nil {
		return nil, false, nil
	}
	var position any
	if p.match(TokCOMMA) {
		pos, perr := p.parseColumn()
		if perr != nil {
			return nil, false, nil
		}
		position = pos
	} else {
		position = false
	}
	if !p.match(TokIN) {
		return nil, false, nil
	}
	iterator, err := p.parseColumn()
	if err != nil {
		// The reference's own column reader also accepts a bracket where a
		// name would be, and an array there is an array: `IN ['1', '2', 3]`.
		// A name that failed for any other reason is still a refusal. The
		// bracket is unconsumed, because a failed name reads nothing.
		if !p.at(TokL_BRACKET) {
			return nil, false, err
		}
		iterator, err = p.parsePrimary()
		if err != nil {
			return nil, false, err
		}
	}
	var condition *Expression
	if p.atWords("IF") {
		p.advance()
		condition, err = p.parseDisjunction()
		if err != nil {
			return nil, false, err
		}
	}
	return New("Comprehension",
		Arg{"this", this}, Arg{"expression", expression}, Arg{"position", position},
		Arg{"iterator", iterator}, Arg{"condition", condition}), true, nil
}

// negateRange flags a negated LIKE rather than wrapping it, which is what the
// reference does and what keeps `NOT LIKE` a single node.
func negateRange(this *Expression) *Expression {
	if this.Class == "Like" || this.Class == "ILike" {
		this.Set("negate", true)
		return this
	}
	return New("Not", Arg{"this", this})
}

func (p *parser) parseIs(this *Expression) (*Expression, error) {
	negate := p.match(TokNOT)

	// `a IS DISTINCT FROM b` and its negation are null-safe comparisons, not
	// an Is over a DISTINCT: the reference builds NullSafeNEQ and NullSafeEQ.
	// Databricks spells the negated form `<=>`, which the generator already
	// wrote as `IS NOT DISTINCT FROM` -- and then could not read back, in any
	// dialect. Found by the batched differential, which reduced 96,096 fuzz
	// findings to this one cause.
	if p.at(TokDISTINCT) {
		p.advance()
		if !p.match(TokFROM) {
			return nil, p.unsupported("IS DISTINCT without FROM")
		}
		other, err := p.parseIsRhs(true)
		if err != nil {
			return nil, err
		}
		class := "NullSafeNEQ"
		if negate {
			class = "NullSafeEQ"
		}
		return p.buildPredicate(class, this, other, nil, false), nil
	}

	var expression *Expression
	// `x IS UNKNOWN` is `x IS NULL`: the reference gives UNKNOWN no node of its
	// own after IS, it simply builds a Null. The negated form then picks up the
	// dialect's NOT shape below, exactly as `IS NOT NULL` does.
	if p.match(TokNULL) || p.match(TokUNKNOWN) {
		expression = New("Null")
	} else if p.match(TokJSON) {
		// `x IS JSON [VALUE|SCALAR|ARRAY|OBJECT] [WITH|WITHOUT] [UNIQUE
		// [KEYS]]`. The kind reads as `false`, not absent, where the word was
		// not written -- the reference's own `_match_texts(...) and ...`
		// short-circuits to the boolean itself, which is what the dump then
		// carries at that key, not nothing.
		var kind any = false
		if p.atWords("VALUE") || p.atWords("SCALAR") || p.atWords("ARRAY") || p.atWords("OBJECT") {
			kind = strings.ToUpper(p.curr().Text)
			p.advance()
		}
		var with_ any
		switch {
		case p.atWords("WITH"):
			p.advance()
			with_ = true
		case p.atWords("WITHOUT"):
			p.advance()
			with_ = false
		}
		unique := p.match(TokUNIQUE)
		if p.atWords("KEYS") {
			p.advance()
		}
		expression = New("JSON", Arg{"this", kind}, Arg{"with_", with_}, Arg{"unique", unique})
	} else {
		var err error
		expression, err = p.parseBitwise()
		if err != nil {
			return nil, err
		}
	}
	// `x IS NOT NULL` has two shapes and the dialect picks. PostgreSQL records
	// the negation on the Is node; everywhere else the reference wraps the Is
	// in a Not and writes it back as `NOT x IS NULL`. The port used the
	// PostgreSQL shape everywhere, so the Go guard saw a different tree from
	// the Python one for one of the commonest predicates in SQL -- semantically
	// the same, and exactly the divergence this port exists to prevent. The
	// flag is probed from the reference; see harness/gen_parser.py.
	if negate && expression.Class == "Null" && !p.tables.IsNotNullWrapsInNot {
		return p.parseColumnOps(p.buildPredicate("Is", this, expression, nil, true))
	}
	is := p.buildPredicate("Is", this, expression, nil, false)
	if negate {
		return p.parseColumnOps(New("Not", Arg{"this", is}))
	}
	return p.parseColumnOps(is)
}

// parseIsRhs is the reference's _parse_is_rhs: how much of the right-hand
// side belongs to the predicate depends on where IS binds in this dialect.
func (p *parser) parseIsRhs(distinct bool) (*Expression, error) {
	if distinct {
		switch p.tables.ISPredicatePrecedence {
		case "LOW":
			return p.parseEqualityWith(false)
		case "EQUALITY":
			return p.parseComparison()
		case "COMPARISON":
			return p.parseRange()
		}
	}
	return p.parseBitwise()
}

func (p *parser) parseIn(this *Expression) (*Expression, error) {
	// `x IN UNNEST(a)` keeps the unnested expression under `unnest` rather
	// than as a field or an expression list (Parser._parse_in).
	if p.at(TokUNNEST) {
		if n := p.next(); n != nil && n.Type == TokL_PAREN {
			unnest, err := p.parseUnnestWithAlias(false)
			if err != nil {
				return nil, err
			}
			return New("In", Arg{"this", this}, Arg{"unnest", unnest}), nil
		}
	}
	if !p.match(TokL_PAREN) {
		// `x IN [a, b]` is the same list written with brackets
		// (ClickHouseParser._parse_in, through _parse_bracket).
		if p.dialect == "clickhouse" && p.at(TokL_BRACKET) {
			arr, aerr := p.parsePrimary()
			if aerr != nil {
				return nil, aerr
			}
			items, _ := arr.Args["expressions"].([]*Expression)
			return New("In", Arg{"this", this}, Arg{"expressions", items}), nil
		}
		// `'red' IN flags` asks whether the value is in the LIST that column
		// holds, rather than in a list written here. The reference keeps
		// what follows under a key of its own.
		if p.startsATable(p.curr()) {
			field, err := p.parseBitwise()
			if err != nil {
				return nil, err
			}
			return New("In", Arg{"this", this}, Arg{"field", field}), nil
		}
		return nil, p.unsupported("IN without a parenthesised list")
	}
	// `a IN (SELECT 1)`: the reference records the query under `query` rather
	// than as a one-item expression list, and it DOES wrap it in a Subquery.
	// A parenthesised query that OPENS with another parenthesis is a query
	// too: `IN ((SELECT 1) EXCEPT (SELECT 2))` names a set operation.
	p.index--
	opensAQuery := p.opensAParenthesisedQuery()
	p.index++
	if p.at(TokSELECT) || p.at(TokWITH) || opensAQuery {
		inner, err := p.parseQuery()
		if err != nil {
			return nil, err
		}
		if !p.match(TokR_PAREN) {
			return nil, p.unsupported("unclosed IN subquery")
		}
		sub := New("Subquery", Arg{"this", inner}, Arg{"pivots", nil},
			Arg{"alias", nil}, Arg{"sample", nil})
		return New("In", Arg{"this", this}, Arg{"query", sub}), nil
	}
	var items []*Expression
	if !p.at(TokR_PAREN) {
		var err error
		items, err = p.parseExpressionList()
		if err != nil {
			return nil, err
		}
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed IN list")
	}
	// `a IN ((SELECT 1))` writes the parentheses twice, and the reference
	// records BOTH: the inner pair is a Subquery of its own and `query` wraps
	// it in a second one. Reusing the inner node lost a level.
	if len(items) == 1 && items[0].Class == "Subquery" {
		sub := New("Subquery", Arg{"this", items[0]}, Arg{"pivots", nil},
			Arg{"alias", nil}, Arg{"sample", nil})
		return New("In", Arg{"this", this}, Arg{"query", sub}), nil
	}
	return New("In", Arg{"this", this}, Arg{"expressions", items}), nil
}

func (p *parser) parseBetween(this *Expression) (*Expression, error) {
	// The grouping a BETWEEN's operand already carries is preserved when the
	// predicate is rewritten into comparisons (Parser._parse_between).
	if this != nil && (this.Class == "Is" || this.Class == "NullSafeEQ" ||
		this.Class == "NullSafeNEQ") {
		this = New("Paren", Arg{"this", this})
	}
	// PostgreSQL's SYMMETRIC swaps the bounds when they arrive the wrong way
	// round, so `BETWEEN SYMMETRIC 10 AND 2` matches where the plain form
	// matches nothing. Neither word is a keyword token -- the reference
	// matches them by text, in every dialect, regardless of whether that
	// dialect can WRITE the word back -- and reading either as the lower
	// bound turned the whole predicate into an And over two comparisons.
	//
	// TokVAR only: `SELECT a BETWEEN "symmetric" AND b` is a column with that
	// name, and matching on text alone read the caller's own identifier as a
	// keyword.
	var symmetric any
	if c := p.curr(); c != nil && c.Type == TokVAR {
		switch {
		case strings.EqualFold(c.Text, "SYMMETRIC"):
			symmetric = true
			p.advance()
		case strings.EqualFold(c.Text, "ASYMMETRIC"):
			symmetric = false
			p.advance()
		}
	}
	low, err := p.parseBitwise()
	if err != nil {
		return nil, err
	}
	p.match(TokAND)
	high, err := p.parseBitwise()
	if err != nil {
		return nil, err
	}
	return New("Between", Arg{"this", this}, Arg{"low", low}, Arg{"high", high},
		Arg{"symmetric", symmetric}), nil
}

// parseJSONArrow is the range-level entry for `j -> '$.a'` and `j ->> '$.a'`.
//
// Which tier they sit at is a dialect fact. PostgreSQL and DuckDB read them
// level with `||`, so `1 + x -> 'y'` is `(1 + x) -> 'y'` there. Everywhere
// else they are accessors, tighter than arithmetic: `1 + (x -> 'y')`. The
// probe JSONOperatorsAtBitwise is that asymmetry; parseBitwise and
// parseColumnOps each take the reading that matches.
//
// The right-hand side is a path STRING parsed into a JSONPath, not an
// expression, which is why this is not simply another binary level.
func (p *parser) parseJSONArrow() (*Expression, error) {
	return p.parseBitwise()
}

// consumeJSONArrow reads one `->` or `->>` whose left operand is already in
// hand. The right-hand side is supplied by the caller: a TERM at the bitwise
// tier (`a -> b + c` keeps `b + c`) and a field-or-bitwise at the accessor
// tier (`x -> 'y' + 1` keeps the addition outside).
func (p *parser) consumeJSONArrow(this *Expression, rhs func() (*Expression, error)) (*Expression, error) {
	c := p.curr()
	class := "JSONExtract"
	if c.Type == TokDARROW {
		class = "JSONExtractScalar"
	}
	p.advance()
	operand, err := rhs()
	if err != nil {
		return nil, err
	}
	path := p.jsonPathFor(operand)
	args := []Arg{{"this", this}, {"expression", path}}
	// The flag is stamped by the BUILDER, and PostgreSQL's returns
	// before it gets there when the operand is not a path it can read.
	if path == nil || path.Class == "JSONPath" || p.tables.JSONArrowTypesWithoutPath {
		args = append(args, Arg{"only_json_types", p.tables.JSONArrowOnlyJSONTypes})
	}
	if class == "JSONExtractScalar" && p.tables.JSONArrowSetsScalarOnly {
		// PostgreSQL leaves this arg OFF the node; the others set it
		// false. An arg present-but-false is a different tree from an
		// arg absent, so whether to set it is probed, not the value.
		args = append(args, Arg{"scalar_only", false})
	}
	return New(class, args...), nil
}

// parseJSONArrowAccessorRHS is the reference's `_parse_column_reference() or
// _parse_bitwise()`. A field or literal binds tighter than arithmetic, so
// `x -> 'y' + 1` keeps the addition outside. A unary or a parenthesised
// expression is still a valid path; those go through bitwise.
func (p *parser) parseJSONArrowAccessorRHS() (*Expression, error) {
	saved := p.index
	this, err := p.parsePrimary()
	if err == nil {
		return this, nil
	}
	p.index = saved
	return p.parseBitwise()
}

func (p *parser) parseBitwise() (*Expression, error) {
	this, err := p.oracleXMLTableExpr()
	if err != nil {
		return nil, err
	}
	for {
		c := p.curr()
		if c == nil {
			return this, nil
		}
		// PostgreSQL's other JSON operators sit at THIS tier too, and only
		// there: `1 + x #> 'y'` is `(1 + x) #> 'y'` in PostgreSQL and
		// `1 + (x #> 'y')` everywhere else, which is the asymmetry the
		// generator probes for. Elsewhere they are refused rather than read
		// one tier out. The arrows join them in PostgreSQL and DuckDB; in
		// every other dialect parseColumnOps takes them at the accessor tier.
		if class, ok := p.tables.JSONOperatorsAtBitwise[c.Type]; ok {
			// ClickHouse's `?` is its TERNARY, and the reference reads it one
			// level up, at the assignment -- so its operand is the whole
			// condition, not the deepest term. Left here for parseAssignment
			// (ClickHouseParser._parse_assignment).
			if class == "If" {
				return this, nil
			}
			if c.Type == TokARROW || c.Type == TokDARROW {
				this, err = p.consumeJSONArrow(this, p.parseTerm)
				if err != nil {
					return nil, err
				}
				continue
			}
			p.advance()
			right, err := p.parseTerm()
			if err != nil {
				return nil, err
			}
			this = New(class, Arg{"this", this}, Arg{"expression", right})
			continue
		}
		switch {
		case p.tables.Bitwise[c.Type] != "":
			class := p.tables.Bitwise[c.Type]
			p.advance()
			right, err := p.parseTerm()
			if err != nil {
				return nil, err
			}
			this = New(class, Arg{"this", this}, Arg{"expression", right})
		case c.Type == TokDPIPE:
			if !p.tables.DPipeIsStringConcat {
				return this, nil
			}
			p.advance()
			right, err := p.parseTerm()
			if err != nil {
				return nil, err
			}
			this = New("DPipe", Arg{"this", this}, Arg{"expression", right},
				Arg{"safe", !p.tables.StrictStringConcat})
		case p.atPair(TokLT, TokLT), p.atPair(TokGT, TokGT):
			// The tokenizer has no << or >>; the reference matches the pair.
			class := "BitwiseLeftShift"
			if c.Type == TokGT {
				class = "BitwiseRightShift"
			}
			p.advance()
			p.advance()
			right, err := p.parseTerm()
			if err != nil {
				return nil, err
			}
			this = New(class, Arg{"this", this}, Arg{"expression", right})
		default:
			return this, nil
		}
	}
}

func (p *parser) parseTerm() (*Expression, error) {
	return p.parseBinary(p.tables.Term, p.oracleTruncTerm)
}

func (p *parser) parseFactor() (*Expression, error) {
	return p.parseBinary(p.tables.Factor, p.oracleKeepOperand)
}

// parseFactorOperand inserts the exponent level only where the dialect has
// one, exactly as the reference does.
func (p *parser) parseFactorOperand() (*Expression, error) {
	// inArith is read BEFORE the operand, off the operator that precedes it.
	inArith := false
	if p.dialect == "sqlite" && p.index > 0 {
		inArith = p.sqliteArithToken(p.tokens[p.index-1].Type)
	}
	// SQLite reads `||`, `->` and `->>` at THIS tier -- tighter than
	// multiplication, looser than COLLATE (SQLiteParser._parse_factor_operand).
	if p.dialect == "sqlite" {
		this, err := p.sqliteConcatOperand()
		if err != nil {
			return nil, err
		}
		return p.sqliteConcatTier(this, inArith)
	}
	if len(p.tables.Exponent) == 0 {
		return p.oracleDayToSecond(p.parseUnary())
	}
	return p.oracleDayToSecond(p.parseBinary(p.tables.Exponent, p.parseUnary))
}

// sqliteArithToken reports whether a token is one of SQLite's arithmetic
// operators -- BITWISE, TERM and FACTOR minus COLLATE, which binds elsewhere.
func (p *parser) sqliteArithToken(tt TokenType) bool {
	if tt == TokCOLLATE {
		return false
	}
	if _, ok := p.tables.Bitwise[tt]; ok {
		return true
	}
	if _, ok := p.tables.Term[tt]; ok {
		return true
	}
	_, ok := p.tables.Factor[tt]
	return ok
}

// sqliteConcatTier continues the `||`/`->`/`->>` tier from an operand already
// read, parenthesising it where the grouping would otherwise change.
func (p *parser) sqliteConcatTier(this *Expression, inArith bool) (*Expression, error) {
	parsedOp := false
	for {
		c := p.curr()
		if c == nil {
			break
		}
		if c.Type != TokDPIPE && c.Type != TokARROW && c.Type != TokDARROW {
			break
		}
		// A DPipe already read binds differently from the arrows, so it is
		// parenthesised before an arrow takes it as its operand.
		if c.Type != TokDPIPE && this.Class == "DPipe" {
			this = New("Paren", Arg{"this", this})
		}
		var err error
		if c.Type == TokDPIPE {
			p.advance()
			rhs, err := p.sqliteConcatOperand()
			if err != nil {
				return nil, err
			}
			this = New("DPipe", Arg{"this", this}, Arg{"expression", rhs}, Arg{"safe", true})
		} else {
			this, err = p.consumeJSONArrow(this, p.sqliteConcatOperand)
			if err != nil {
				return nil, err
			}
		}
		parsedOp = true
	}
	if parsedOp && (inArith || (p.curr() != nil && p.sqliteArithToken(p.curr().Type))) {
		this = New("Paren", Arg{"this", this})
	}
	return this, nil
}

// sqliteConcatOperand reads one operand of the concat tier: a unary
// expression, then any COLLATE, which binds tighter than any binary operator
// (SQLiteParser._parse_concat_operand).
func (p *parser) sqliteConcatOperand() (*Expression, error) {
	this, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for p.match(TokCOLLATE) {
		rhs, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		collate := New("Collate", Arg{"this", this}, Arg{"expression", rhs})
		// The collation is a NAME, not a column (SQLiteParser._normalize_collate).
		if rhs != nil && rhs.Class == "Column" {
			if name, _ := rhs.Args["this"].(*Expression); name != nil {
				collate.Set("expression", New("Var", Arg{"this", name.Args["this"]}))
			}
		}
		this = collate
	}
	return this, nil
}

// parseBinary runs one left-associative precedence level.
func (p *parser) parseBinary(ops map[TokenType]string, next func() (*Expression, error)) (*Expression, error) {
	this, err := next()
	if err != nil {
		return nil, err
	}
	return p.parseBinaryFrom(this, ops, next)
}

// parseBinaryFrom runs one precedence level from a node already read.
func (p *parser) parseBinaryFrom(this *Expression, ops map[TokenType]string, next func() (*Expression, error)) (*Expression, error) {
	for {
		c := p.curr()
		if c == nil {
			return this, nil
		}
		class, ok := ops[c.Type]
		if !ok {
			return this, nil
		}
		p.advance()
		opComments := p.takeComments()
		// COLLATE reads a TERM, the same as `+` and `-` do here, which is
		// why a SCHEMA-qualified name reads fine: `pg_catalog.default` is a
		// Column, same as it would be anywhere else. Only an UNQUALIFIED
		// name is not one -- a bare word there is a Var and a quoted one an
		// Identifier, where the generic column rule would make one of a
		// single Identifier either way.
		if class == "Collate" {
			name, cerr := next()
			if cerr != nil {
				return nil, cerr
			}
			this = putComments(New(class, Arg{"this", this}, Arg{"expression", collationName(name)}), opComments)
			continue
		}
		right, err := next()
		if err != nil {
			return nil, err
		}
		if specialConstruction[class] && class != "Div" {
			return nil, p.unsupported(class)
		}
		if class == "Div" {
			// Div records how the dialect divides; the reference reads both
			// flags off the dialect rather than defaulting them.
			this = putComments(New(class, Arg{"this", this}, Arg{"expression", right},
				Arg{"typed", p.tables.TypedDivision}, Arg{"safe", p.tables.SafeDivision}), opComments)
			continue
		}
		this = putComments(New(class, Arg{"this", this}, Arg{"expression", right}), opComments)
	}
}

// parseMySQLBinaryPrefix reads `BINARY a` as CAST(a AS BINARY). The word is
// current, and a parenthesis has already been ruled out.
func (p *parser) parseMySQLBinaryPrefix() (*Expression, error) {
	p.advance()
	col, err := p.parseColumn()
	if err != nil {
		return nil, err
	}
	to := New("DataType", Arg{"this", DataTypeKind("BINARY")}, Arg{"nested", false})
	cast := New("Cast",
		Arg{"this", col}, Arg{"to", to}, Arg{"format", nil},
		Arg{"safe", nil}, Arg{"action", nil}, Arg{"default", nil})
	cast.Type = to
	return cast, nil
}

// parseUnary mirrors the reference's UNARY_PARSERS, including that NOT takes
// an equality as its operand rather than a unary -- so `NOT a = b` negates the
// comparison, not the column.
func (p *parser) parseUnary() (*Expression, error) {
	c := p.curr()
	if c == nil {
		return nil, p.unsupported("expression")
	}
	// MySQL's bare BINARY is a cast of the column that follows, including in
	// ORDER BY. BINARY(...) is a call, and a parenthesis keeps it one.
	if p.dialect == "mysql" && p.at(TokBINARY) {
		if n := p.next(); n == nil || n.Type != TokL_PAREN {
			return p.parseMySQLBinaryPrefix()
		}
	}
	// MySQL and the dialects that inherit it read NOT their own way: the
	// EXCLAMATION MARK binds tighter than the word, so its negation is
	// parenthesised to keep that grouping (MySQLParser._parse_not).
	if isMySQLFamily(p.dialect) && c.Type == TokNOT {
		p.advance()
		if c.Text == "!" {
			inner, err := p.parseUnary()
			if err != nil {
				return nil, err
			}
			return New("Paren", Arg{"this", New("Not", Arg{"this", inner})}), nil
		}
		inner, err := p.parseEquality()
		if err != nil {
			return nil, err
		}
		return New("Not", Arg{"this", inner}), nil
	}
	class, isUnary := p.tables.UnaryOps[c.Type]
	if !isUnary {
		return p.oracleNthValueFrom(p.parsePostfix())
	}
	p.advance()
	// Unary plus is a no-op in the reference too: it yields its operand.
	if class == "" {
		return p.parseUnary()
	}
	// NOT takes an EQUALITY as its operand rather than a unary, so `NOT a = b`
	// negates the comparison and not the column. Every other prefix operator
	// binds as tightly as it can.
	operand := p.parseUnary
	if class == "Not" {
		operand = p.parseEquality
	}
	this, err := operand()
	if err != nil {
		return nil, err
	}
	return New(class, Arg{"this", this}), nil
}

// parsePostfix reads what binds tighter than any operator: the :: cast, which
// the reference handles among the column operators.
//
// This is the reference's own `_parse_column`: a primary, then whatever
// postfix operators apply to it, and -- for Redshift, where the tokenizer
// reads Oracle's own `(+)` outer-join mark as a token of its own -- a stamp
// of whether one followed. Not only when one did: the reference marks EVERY
// result of this rule, a bare `x` included, and stamps it on WHATEVER it
// parsed here, a Cast or a call as much as a bare Column.
func (p *parser) parsePostfix() (*Expression, error) {
	if zoned, handled, err := p.oracleZonedTimestamp(); handled {
		return zoned, err
	}
	this, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}
	out, err := p.parseColumnOps(this)
	if err != nil {
		return nil, err
	}
	// A bare LITERAL never reaches the reference's own `_parse_column` at
	// all -- its fast path is for identifier-shaped things, and the reference
	// records no join_mark on a plain `1` inside `ROWS BETWEEN 1 PRECEDING`.
	// A Column, a Cast, a call: everything else this function can return
	// does go through it, and does carry the mark.
	// The mark is recorded only where it was actually written: the reference
	// no longer stamps a `join_mark: false` on every column.
	if p.tables.SupportsColumnJoinMarks && out != nil && !p.noJoinMark[out] &&
		out.Class != "Literal" && out.Class != "Boolean" && out.Class != "Null" &&
		out.Class != "Interval" && out.Class != "Table" && out.Class != "Star" &&
		p.match(TokJOIN_MARKER) {
		out.Set("join_mark", true)
	}
	// AT TIME ZONE is not one of the reference's own COLUMN_OPERATORS --
	// `_parse_column_ops` is only brackets, dots and `::` casts -- so it is
	// parsed OUTSIDE `_parse_column`, after whatever join_mark that rule
	// already stamped on `out`. An AtTimeZone wrapper never carries a mark
	// of its own; only the thing it wraps might.
	for p.atAtTimeZone() {
		p.advance()
		p.advance()
		p.advance()
		// parsePrimary, not parseBitwise: the zone parse recurses back into
		// this loop, so a chained `AT TIME ZONE 'a' AT TIME ZONE 'b'` had
		// the second one swallowed into the first one's ZONE -- right
		// associative, where the reference nests left.
		zone, zerr := p.parsePrimary()
		if zerr != nil {
			return nil, zerr
		}
		out = New("AtTimeZone", Arg{"this", out}, Arg{"zone", zone})
	}
	return out, nil
}

// parseColumnOps reads the operators that apply to something already parsed:
// the `::` cast, a subscript, a dot. The reference runs them after an IS as
// well as after a column, which is why `col IS NULL::BOOLEAN` casts the whole
// test rather than the NULL.
func (p *parser) parseColumnOps(this *Expression) (*Expression, error) {
	this = p.oracleDateLiteral(this)
	// A call whose ARGUMENT carried IGNORE NULLS keeps the words outside the
	// call: `SUM(x IGNORE NULLS)` is IgnoreNulls(Sum(x)), because the
	// reference lifts the treatment off the argument and around the whole
	// call once the function is built (Parser._parse_function_call ->
	// _parse_window).
	if lifted := liftNullTreatment(this); lifted != nil {
		this = lifted
	}
	for {
		// Teradata's `(FORMAT '...')` after a column overrides how its value
		// is shown. It is a PHRASE rather than a call, which is why the
		// reference reads it here and makes the function reader refuse the
		// parenthesis (TeradataParser._parse_column_ops, _parse_function).
		if p.at(TokL_PAREN) && p.next() != nil && p.next().Type == TokFORMAT {
			p.advance() // (
			p.advance() // FORMAT
			format := p.tryParseStringLiteral()
			if format == nil {
				return nil, p.unsupported("FORMAT without a string")
			}
			p.match(TokR_PAREN)
			this = New("FormatPhrase", Arg{"this", this}, Arg{"format", format})
			continue
		}
		// SingleStore's column operators are its own (SingleStoreParser
		// .COLUMN_OPERATORS): `:>` and `!:` CAST, where every other dialect's
		// `::` does, and `::`/`::$`/`::%`/`::?` EXTRACT JSON, where every
		// other dialect's `::` casts. They are checked here, before the
		// generic `::` branch below.
		if p.dialect == "singlestore" {
			if p.match(TokCOLON_GT) {
				to, err := p.parseDataType()
				if err != nil {
					return nil, err
				}
				cast := New("Cast", Arg{"this", this}, Arg{"to", to})
				cast.Type = to
				this = cast
				continue
			}
			if p.match(TokNCOLON_GT) {
				to, err := p.parseDataType()
				if err != nil {
					return nil, err
				}
				cast := New("TryCast", Arg{"this", this}, Arg{"to", to})
				cast.Type = to
				this = cast
				continue
			}
			if c := p.curr(); c != nil && (c.Type == TokDCOLON || c.Type == TokDCOLONDOLLAR ||
				c.Type == TokDCOLONPERCENT || c.Type == TokDCOLONQMARK) {
				op := c.Type
				p.advance()
				field, err := p.parseJSONArrowAccessorRHS()
				if err != nil {
					return nil, err
				}
				name := ""
				if field != nil {
					name = field.Name()
				}
				switch op {
				case TokDCOLON:
					this = New("JSONExtract", Arg{"this", this},
						Arg{"expression", p.singlestoreJSONPath(name)},
						Arg{"only_json_types", false})
				case TokDCOLONDOLLAR:
					this = New("JSONExtractScalar", Arg{"this", this},
						Arg{"expression", p.singlestoreJSONPath(name)},
						Arg{"only_json_types", false}, Arg{"json_type", "STRING"})
				case TokDCOLONPERCENT:
					this = New("JSONExtractScalar", Arg{"this", this},
						Arg{"expression", p.singlestoreJSONPath(name)},
						Arg{"only_json_types", false}, Arg{"json_type", "DOUBLE"})
				case TokDCOLONQMARK:
					this = New("JSONExists", Arg{"this", this},
						Arg{"path", name}, Arg{"from_dcolonqmark", true})
				}
				continue
			}
		}
		// `x .: Type` is ClickHouse's JSON cast: the same JSONCast `JSON_CAST`
		// builds (Parser.COLUMN_OPERATORS). The type may be written INSIDE a
		// quoted name -- `.:"Array(JSON)"` -- which the reference tokenizes
		// and reads as a type (Parser._parse_types, DataType.from_str).
		if p.match(TokDOTCOLON) {
			var to *Expression
			var err error
			if c := p.curr(); c != nil && c.Type == TokIDENTIFIER {
				text := c.Text
				p.advance()
				to, err = p.parseDataTypeText(text)
			} else {
				to, err = p.parseDataType()
			}
			if err != nil {
				return nil, err
			}
			cast := New("JSONCast", Arg{"this", this}, Arg{"to", to})
			cast.Type = to
			this = cast
			continue
		}
		if p.match(TokDCOLON) {
			typeToken := p.curr()
			to, err := p.parseDataType()
			if err != nil {
				return nil, err
			}
			// A dialect with STRICT_CAST off (Hive's own) builds a TryCast
			// here too -- this is the shape `123L` takes, the tokenizer
			// spelling the suffix as `123 :: BIGINT`.
			class := "Cast"
			if !p.tables.StrictCast {
				class = "TryCast"
			}
			// `1.0BD` names a DECIMAL whose precision and scale come from
			// the literal itself, and casts the TEXT so nothing is rounded
			// on the way (HiveParser.build_cast).
			if typeToken != nil && hiveDecimalSuffix(p.dialect) &&
				strings.EqualFold(typeToken.Text, "BD") {
				if literal, dt := hiveBDDecimal(this); literal != nil {
					cast := New(class, Arg{"this", literal}, Arg{"to", dt})
					cast.Type = dt
					this = cast
					continue
				}
			}
			to = p.hiveCastType(to)
			cast := New(class, Arg{"this", this}, Arg{"to", to})
			cast.Type = to
			this = cast
			continue
		}
		// Databricks' own `?::` is `::` spelled for the TRY variant: `'20'?::INT`
		// is a TryCast the same shape a plain `::` casts, one token rather
		// than a placeholder followed by a cast.
		if p.dialect == "databricks" && p.match(TokQDCOLON) {
			to, err := p.parseDataType()
			if err != nil {
				return nil, err
			}
			cast := New("TryCast", Arg{"this", this}, Arg{"to", to})
			cast.Type = to
			this = cast
			continue
		}
		// `c1:item[1].price` is a JSON extraction, the form Databricks writes.
		// The port WROTE it while refusing to read a single one, so every
		// extraction it emitted for that dialect was SQL it could not read
		// back; the generator fuzzer found it on `0->''`.
		if p.tables.VariantExtractColon && p.at(TokCOLON) && p.variantKeyAhead() {
			path, err := p.parseVariantPath()
			if err != nil {
				return nil, err
			}
			this = New("JSONExtract",
				Arg{"this", this},
				Arg{"expression", path},
				Arg{"variant_extract", true},
				Arg{"requires_json", snowflakeJSONRequired(p.dialect)})
			continue
		}
		// `->` / `->>` at the accessor tier, binding tighter than arithmetic.
		// Dialects that read them level with `||` leave them for parseBitwise.
		// SQLite reads them at its own concat tier, inside the factor operand.
		if c := p.curr(); c != nil && (c.Type == TokARROW || c.Type == TokDARROW) && p.dialect != "sqlite" {
			if _, atBitwise := p.tables.JSONOperatorsAtBitwise[c.Type]; !atBitwise {
				var err error
				this, err = p.consumeJSONArrow(this, p.parseJSONArrowAccessorRHS)
				if err != nil {
					return nil, err
				}
				continue
			}
		}
		// `x[1]`, `x[1:2]` and `x[1][2]` are Brackets over what precedes them.
		// In T-SQL `[` opens a quoted identifier and the tokenizer has already
		// consumed it, so this branch is unreachable there -- which is why it
		// needs no dialect flag.
		// `f(x) IGNORE NULLS` and `f(x) WITHIN GROUP (ORDER BY y)` WRAP the
		// call. Every word involved is a plain VAR, so all of them are matched
		// by text.
		// Not inside a call's own argument list: `SUM(x IGNORE NULLS)` belongs
		// to the SUM, and letting the argument claim it built
		// Sum(IgnoreNulls(x)) where the reference has IgnoreNulls(Sum(x)).
		if word := p.atNullsModifier(); word != "" && !p.inCallArgs {
			p.advance()
			p.advance()
			this = New(word, Arg{"this", this})
			continue
		}
		if p.atWords("WITHIN", "GROUP") {
			p.advance()
			p.advance()
			if !p.match(TokL_PAREN) {
				return nil, p.unsupported("WITHIN GROUP without a parenthesised ORDER BY")
			}
			if !p.match(TokORDER_BY) {
				return nil, p.unsupported("WITHIN GROUP without ORDER BY")
			}
			order, err := p.parseOrder()
			if err != nil {
				return nil, err
			}
			if !p.match(TokR_PAREN) {
				return nil, p.unsupported("unclosed WITHIN GROUP")
			}
			this = New("WithinGroup", Arg{"this", this}, Arg{"expression", order})
			continue
		}
		// `SUM(x) FILTER(WHERE p)` wraps the aggregate in a Filter carrying a
		// Where -- not a call to a function named FILTER.
		if p.at(TokFILTER) && p.next() != nil && p.next().Type == TokL_PAREN {
			p.advance()
			p.advance()
			// The WHERE is not always written: `SUM(x) FILTER (x = 1)`
			// names the condition straight after the parenthesis, and the
			// reference still wraps it in a Where.
			p.match(TokWHERE)
			pred, err := p.parseExpression()
			if err != nil {
				return nil, err
			}
			if !p.match(TokR_PAREN) {
				return nil, p.unsupported("unclosed FILTER")
			}
			this = New("Filter", Arg{"this", this},
				Arg{"expression", New("Where", Arg{"this", pred})})
			continue
		}
		// `f(x) OVER (...)` wraps the call in a Window.
		if p.at(TokOVER) {
			w, err := p.parseWindow(this)
			if err != nil {
				return nil, err
			}
			this = w
			continue
		}
		if p.at(TokL_BRACKET) {
			// A subscript turns the array it indexes into a Bracket, and the
			// reference marks only the node its bracket reader RETURNS: an
			// array that ends up subscripted is not marked
			// (BigQueryParser._parse_bracket).
			if p.dialect == "bigquery" && this.Class == "Array" {
				this.Set("struct_name_inheritance", nil)
			}
			p.advance()
			// A SUBSCRIPT: a colon here separates a slice's bounds.
			// `x[]` subscripts nothing, and the reference refuses it -- a
			// Bracket must carry an index. An empty pair of brackets is
			// still an ARRAY where one may stand, which is why this is here
			// and not in the reader below.
			if p.at(TokR_BRACKET) {
				// ClickHouse's `x[]` casts x to Array(JSON), and each further
				// pair nests the array (ClickHouseParser._parse_bracket).
				if p.dialect == "clickhouse" {
					var elem any = New("DataType",
						Arg{"this", DataTypeKind("JSON")}, Arg{"dialect", p.dialect},
						Arg{"nullable", false})
					for {
						elem = New("DataType", Arg{"this", DataTypeKind("ARRAY")},
							Arg{"expressions", []*Expression{elem.(*Expression)}},
							Arg{"nested", true})
						p.advance() // ]
						if !p.atPair(TokL_BRACKET, TokR_BRACKET) {
							break
						}
						p.advance() // [
					}
					this = New("JSONCast", Arg{"this", this}, Arg{"to", elem})
					// The reference's dump carries the node's inferred TYPE as
					// `t`, and for a JSON cast that type is the `to` itself.
					this.Type = elem.(*Expression)
					continue
				}
				return nil, p.unsupported("subscript with no index")
			}
			items, err := p.parseBracketItems(true)
			if err != nil {
				return nil, err
			}
			items, err = p.applyIndexOffset(this, items)
			if err != nil {
				return nil, err
			}
			this = New("Bracket", Arg{"this", this}, Arg{"expressions", items},
				Arg{"offset", nil}, Arg{"safe", nil}, Arg{"returns_null_on_error", nil})
			// BigQuery names the numbering INSIDE the brackets -- OFFSET(1)
			// counts from 0 and ORDINAL(1) from 1 -- and the reference reads
			// that name as a flag on the Bracket, keeping the argument alone
			// in its place (BigQueryParser._parse_bracket).
			if p.dialect == "bigquery" {
				for i, expr := range items {
					if expr == nil || expr.Class != "Anonymous" {
						break
					}
					offset, safe, ok := bigqueryBracketOffset(strings.ToUpper(expr.Name()))
					callArgs, _ := expr.Args["expressions"].([]*Expression)
					if !ok || len(callArgs) == 0 {
						break
					}
					this.Set("offset", offset)
					this.Set("safe", safe)
					items[i] = New("Literal",
						Arg{"this", callArgs[0].Name()}, Arg{"is_string", false})
				}
			}
			continue
		}
		// A dot after something that is NOT a plain name: `a[0].b`, `f(x).g`,
		// `X(y).1`. parsePrimary reads `a.b.c` into one Column and stops where
		// it cannot go on; whatever dot is left over continues HERE, which is
		// where the reference continues it too -- its `_parse_column_ops`
		// loops over brackets and dots together, and only the run of plain
		// names collapses into a Column.
		// ClickHouse's `.^` reads one field of a nested JSON object:
		// `col.^nested` (ClickHouseParser.COLUMN_OPERATORS, DOTCARET).
		if p.dialect == "clickhouse" && p.at(TokDOTCARET) {
			p.advance()
			field, err := p.parseColumn()
			if err != nil {
				return nil, err
			}
			this = New("NestedJSONSelect", Arg{"this", this}, Arg{"expression", field})
			continue
		}
		if p.at(TokDOT) {
			field, isCall, err := p.parseDotField()
			if err != nil {
				return nil, err
			}
			if field == nil {
				return this, nil
			}
			// A dot chain ending in a CALL is a QUALIFIED call, and the names
			// leading to it are parts of the call's name rather than columns:
			// `a[b].C()` reads a and b as identifiers where `a[b].c` reads
			// both as columns. The reference rewrites every Column below the
			// chain when the field turns out to be a function.
			if isCall {
				this = columnsToDots(this)
			}
			this = New("Dot", Arg{"this", this}, Arg{"expression", field})
			continue
		}
		return this, nil
	}
}

// applyIndexOffset shifts a written subscript to sqlglot's 0-based Bracket.
//
// This is the reference's `apply_index_offset`, and almost all of it is
// conditions rather than arithmetic. It fires only for a SINGLE index, only
// where the base is UNKNOWN or an ARRAY, and only where the index is an
// INTEGER -- so `a[x]`, `a['k']` and `a[1:2]` keep the index they were
// written with and gain only the type annotations the reference stamps on
// them while deciding not to shift.
//
// The annotations are the reason this cannot be done with arithmetic alone:
// they are part of the tree the reference produces, and the differential
// compares them.
func (p *parser) applyIndexOffset(this *Expression, items []*Expression) ([]*Expression, error) {
	out, ok := ApplyIndexOffset(this, items, -p.tables.IndexOffset, p.dialect)
	if !ok {
		return nil, p.unsupported("subscript the port cannot type")
	}
	return out, nil
}

// atSliceStart reports whether a colon here opens a slice with no lower
// bound rather than a bound parameter.
//
// The two look identical and the reference tells them apart by what FOLLOWS:
// `[:1]` is a slice up to 1, `[:a]` is an array holding the parameter `a`.
// A blanket rule in either direction is wrong -- reading every colon as a
// slice built `[:A.a]` into a Slice over a column where the reference builds
// a Dot over a Placeholder, and reading every colon as a parameter refused
// `[:0.]`, which the reference reads as a slice.
func (p *parser) atSliceStart() bool {
	if !p.at(TokCOLON) {
		return false
	}
	// A plain word only. A KEYWORD after the colon is the start of the bound,
	// not the name of a parameter: `[:NOT x]` is a slice up to NOT x, and
	// reading NOT as a name left the rest of the expression trailing. The
	// `$WHERE` form still takes keywords -- that is a different token and a
	// different rule.
	n := p.next()
	return n == nil || (n.Type != TokVAR && !isQuotedName(n))
}

// isQuotedName reports whether a token is a name written in quotes.
func isQuotedName(t *Token) bool { return t != nil && t.Type == TokIDENTIFIER }

// parseBracketItems reads what sits between `[` and `]`: a comma-separated
// list where any item may be a slice. `x[:]` is a Slice with neither bound,
// which is why an empty side is a missing arg rather than an error.
func (p *parser) parseBracketItems(_ bool) ([]*Expression, error) {
	var items []*Expression
	for !p.at(TokR_BRACKET) {
		var low *Expression
		// Where a colon is the JSON operator (Databricks' `c:path`), a slice
		// with no lower bound is an extraction with nothing to extract from,
		// and the reference refuses it. Reading it as a slice wrote
		// `ARRAY(::a)`, which reads back as a cast.
		if p.tables.VariantExtractColon && p.atSliceStart() {
			return nil, p.unsupported("slice with no lower bound")
		}
		if !p.atSliceStart() {
			e, err := p.parseExpression()
			if err != nil {
				return nil, err
			}
			low = e
		}
		if p.match(TokCOLON) {
			item, err := p.parseSliceRest(low)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		} else {
			if low == nil {
				return nil, p.unsupported("empty subscript")
			}
			items = append(items, low)
		}
		if !p.match(TokCOMMA) {
			break
		}
	}
	if !p.match(TokR_BRACKET) {
		return nil, p.unsupported("unclosed subscript")
	}
	return items, nil
}

// parsePrimary reads the smallest thing an expression can be.
//
// Every path through parseUnary checks there is a token first, but the
// statement parsers that reach for an expression directly -- KILL, SET -- do
// not always have one, and a bare `KILL` panicked here. The check belongs in
// one place rather than at each caller.
func (p *parser) parsePrimary() (*Expression, error) {
	c := p.curr()
	if c == nil {
		return nil, p.unsupported("expression")
	}

	// ClickHouse reads AND, OR and XOR as FUNCTION names when a parenthesis
	// follows -- `or(1, 2)` is the connector over its arguments -- where the
	// same tokens are operators everywhere else
	// (ClickHouseParser.FUNCTION_PARSERS, Parser._parse_connector_function).
	if p.dialect == "clickhouse" && (c.Type == TokAND || c.Type == TokOR || c.Type == TokXOR) {
		if n := p.next(); n != nil && n.Type == TokL_PAREN {
			return p.parseFunction()
		}
	}

	// MySQL's `_utf8mb4 'text'`: a character set introducer in front of a literal.
	if c.Type == TokINTRODUCER {
		p.advance()
		literal, err := p.parsePrimary()
		if err != nil {
			return nil, err
		}
		return New("Introducer", Arg{"this", c.Text}, Arg{"expression", literal}), nil
	}

	if c.Type == TokINTERVAL {
		// Nothing back means INTERVAL was a NAME rather than a quantity, and
		// the index is where it was; the identifier rules below read it.
		interval, err := p.parseInterval()
		if err != nil {
			return nil, err
		}
		if interval != nil {
			return interval, nil
		}
	}

	// A bound parameter. `?` is one on its own; `:name` is the colon and the
	// name, and a COLON can only open a placeholder HERE -- everywhere else it
	// is infix, separating a slice's bounds or a struct's key from its value.
	//
	// The port wrote `ARRAY(:Wa)` and could not read it back, which is how the
	// generator fuzzer found this: the reference reads it, so the round trip
	// was the port's own gap rather than a property stronger than the oracle.
	// `N'abc'` is a National, not a plain string: the reference keeps the
	// prefix in the node so it can write it back.
	if c.Type == TokNATIONAL_STRING {
		p.advance()
		return p.dotted(New("National", Arg{"this", c.Text})), nil
	}
	if c.Type == TokPLACEHOLDER {
		p.advance()
		if p.tables.Placeholder.AnonymousJDBC {
			return p.dotted(New("Placeholder", Arg{"jdbc", true})), nil
		}
		return p.dotted(New("Placeholder")), nil
	}
	if c.Type == TokCOLON {
		// A NAME after the colon, never a number. `:a` is a bound parameter
		// and `[:1]` is a slice with no lower bound -- the reference tells
		// them apart the same way, and treating a number as a parameter name
		// here read `[:0.]` as a parameter called `0.`.
		//
		// A QUOTED name counts here, unlike after `@`: `[:"a"]` is the
		// parameter `a` and the quotes are not kept, where `@"x"` carries an
		// Identifier. The colon form was reading it as a slice instead, and
		// wrote `[:"a"]` back for a tree the reference writes as `[$a]`.
		if n := p.next(); (isParameterName(n) || isQuotedName(n)) && n.Type != TokNUMBER {
			p.advance()
			p.advance()
			return p.dotted(New("Placeholder", Arg{"this", n.Text})), nil
		}
	}
	// One token, several nodes, and which one is the DIALECT's business.
	// `$nm` is a Placeholder in DuckDB, a Parameter in PostgreSQL and
	// Databricks, and a plain column elsewhere. `@nm` is a Parameter
	// everywhere except DuckDB, where `@` is ABSOLUTE VALUE -- reading it as a
	// Parameter there built three trees the reference never makes. So the
	// class is looked up rather than decided here; see harness/gen_parser.py.
	// Databricks brackets the name: `${x}`. It is the same Parameter, with an
	// `expression` flag recording that the braces were there -- and the port
	// WRITES this form, so it has to read it back.
	if node := p.parseParameter(); node != nil {
		// A PLACEHOLDER may be reached through: `?.a` is an attribute of what
		// was bound. A Parameter is not, and never was.
		if node.Class == "Placeholder" {
			return p.dotted(node), nil
		}
		return node, nil
	}
	if p.at(TokSESSION_PARAMETER) {
		return p.parseSessionParameter()
	}
	// PostgreSQL spells it `%(name)s`, or `%s` unnamed -- and records the name
	// as an IDENTIFIER rather than a string, unlike every other dialect. A
	// modulo cannot open an expression, so a MOD here is unambiguous.
	if c.Type == TokMOD {
		if p.tables.Placeholder.PercentAnonymous == "Placeholder" {
			if n := p.next(); n != nil && strings.EqualFold(n.Text, "s") {
				p.advance()
				p.advance()
				return p.dotted(New("Placeholder")), nil
			}
		}
		if p.tables.Placeholder.PercentNamed == "Placeholder" {
			if ph, ok := p.parseNamedPercentPlaceholder(); ok {
				return p.dotted(ph), nil
			}
		}
	}

	// Databricks reads `{name}` as a notebook WIDGET, a Placeholder rather
	// than the Struct a brace opens everywhere else -- the one dialect where
	// this shape means something else entirely.
	if widget := p.parseWidgetPlaceholder(); widget != nil {
		return p.dotted(widget), nil
	}

	// `{'a': 1, 'b': x}` is a Struct whose items are PropertyEQ: the key is an
	// IDENTIFIER even though it is written as a string.
	if c.Type == TokL_BRACE {
		if p.daxBraceValues() {
			return p.parseDAXBraceValues()
		}
		// A ClickHouse query parameter is `{name: Type}` -- a name, a colon
		// and a TYPE -- rather than the map literal the same braces open
		// (ClickHouseParser.PLACEHOLDER_PARSERS, _parse_query_parameter).
		if p.dialect == "clickhouse" {
			if ph, handled := p.parseClickhouseQueryParameter(); handled {
				return ph, nil
			}
		}
		p.advance()
		// Exasol's ODBC datetime literal: `{d'2024-01-01'}` is a Date and
		// `{ts'2024-01-01 12:00:00'}` a Timestamp, which is the only ODBC
		// shape any dialect in the corpus has
		// (ExasolParser.ODBC_DATETIME_LITERALS,
		// Parser._parse_odbc_datetime_literal).
		if p.dialect == "exasol" {
			if c := p.curr(); c != nil && (c.Type == TokVAR || c.Type == TokIDENTIFIER) {
				class := ""
				switch strings.ToLower(c.Text) {
				case "d":
					class = "Date"
				case "ts":
					class = "Timestamp"
				}
				if class != "" && p.next() != nil && p.next().Type == TokSTRING {
					p.advance()
					lit := p.tryParseStringLiteral()
					if lit == nil {
						return nil, p.unsupported("ODBC literal without a string")
					}
					if !p.match(TokR_BRACE) {
						return nil, p.unsupported("ODBC literal without a closing brace")
					}
					return New(class, Arg{"this", lit}), nil
				}
			}
		}
		var items []*Expression
		for !p.at(TokR_BRACE) {
			key := p.curr()
			if key == nil {
				return nil, p.unsupported("struct key")
			}
			p.advance()
			if !p.match(TokCOLON) {
				return nil, p.unsupported("struct entry without a colon")
			}
			value, err := p.parseExpression()
			if err != nil {
				return nil, err
			}
			items = append(items, New("PropertyEQ",
				Arg{"this", New("Identifier", Arg{"this", key.Text},
					Arg{"quoted", !isBareIdentifier(key.Text)})},
				Arg{"expression", value}))
			if !p.match(TokCOMMA) {
				break
			}
		}
		if !p.match(TokR_BRACE) {
			return nil, p.unsupported("unclosed struct")
		}
		// ClickHouse reads `{'k': v}` as a MAP literal rather than a struct:
		// every key and value becomes an entry of two parallel arrays
		// (ClickHouseParser._parse_bracket).
		if p.dialect == "clickhouse" {
			return clickhouseVarMap(items), nil
		}
		return New("Struct", Arg{"expressions", items}), nil
	}

	// `x LIKE ALL (...)` and `x = ANY (...)` are QUANTIFIERS over what follows,
	// not calls to functions named ALL and ANY -- which is what the generic
	// call rule made of them, since both are followed by a parenthesis.
	// `0 < ALL()` is an ANONYMOUS call, not a quantifier over nothing -- there
	// is no operand to quantify. The reference builds Anonymous(ALL) and the
	// port refused, which the generator fuzzer found by writing `ALL()` and
	// failing to read it back.
	if (p.at(TokALL) || p.at(TokANY)) && p.next() != nil && p.next().Type == TokL_PAREN &&
		p.quantifierHere() {
		class := "All"
		if p.at(TokANY) {
			class = "Any"
		}
		p.advance()
		inner, err := p.parsePrimary()
		if err != nil {
			return nil, err
		}
		// Over a QUERY the two quantifiers differ from each other, and the
		// difference is the CLASS rather than the dialect or the operator
		// above: ANY keeps the Subquery wrapper the parentheses made, ALL
		// carries the Select straight. Probed; see harness/gen_parser.go.
		if inner != nil && inner.Class == "Subquery" && !p.tables.QuantifierWrapsSubquery[class] {
			inner, _ = inner.Args["this"].(*Expression)
			if inner == nil {
				return nil, p.unsupported("quantifier over an empty subquery")
			}
		}
		return New(class, Arg{"this", inner}), nil
	}

	// ANY is a quantifier WHEREVER it stands, not only after a comparison:
	// `ANY(x) OVER (...)` quantifies over x and `name LIKE ANY XXX('a')` over
	// the call. What follows may be parenthesised or not -- the reference
	// takes whatever expression is there and wraps nothing of its own.
	//
	// ALL is left alone: `SELECT ALL x` is the quantity modifier rather than
	// a quantifier, and `ALL()` is a Tuple.
	// ClickHouse's `any(...)` is the AGGREGATE, not a quantifier: `any` is a
	// name in its function table and its quantifier form is the comparison
	// above, which the earlier block has already taken. Reading it here built
	// an Any over a Paren, which writes back as `ANY(x)`.
	if p.at(TokANY) && p.next() != nil && !endsSelectExpression(p.next()) &&
		(p.dialect != "clickhouse" || p.next().Type != TokL_PAREN) {
		p.advance()
		inner, err := p.parsePrimary()
		if err != nil {
			return nil, err
		}
		if inner == nil {
			return nil, p.unsupported("ANY over nothing")
		}
		return New("Any", Arg{"this", inner}), nil
	}

	// `ARRAY[1, 2]` is an Array too -- the keyword is part of the literal, not
	// a column being subscripted, which is what the postfix rule would make of
	// it.
	if p.atPair(TokARRAY, TokL_BRACKET) {
		p.advance()
		p.advance()
		items, err := p.parseBracketItems(true)
		if err != nil {
			return nil, err
		}
		arr := p.arrayConstructor(items, TokL_BRACKET)
		return arr, nil
	}

	// `LIST[1, 2]` is a List. The empty `LIST[]` is a type instead. Both
	// live in parseListLiteral so this function does not grow another branch.
	if built, ok, err := p.parseListLiteral(); ok {
		return built, err
	}

	// `[1, 2, 3]` is an Array literal. Same token as the subscript above; the
	// difference is position, and only this one begins an expression.
	if c.Type == TokL_BRACKET {
		p.advance()
		items, err := p.parseBracketItems(true)
		if err != nil {
			return nil, err
		}
		arr := p.arrayConstructor(items, TokL_BRACKET)
		return arr, nil
	}

	// CURRENT_DATE and friends are calls with no argument list.
	if class, ok := p.tables.NoParenFunctionClasses[c.Type]; ok {
		// The parentheses are OPTIONAL, not forbidden: `CURRENT_TIMESTAMP(0)`
		// is the same node carrying a precision, and `CURRENT_TIMESTAMP()` is
		// the same node carrying nothing. Databricks writes both, so a port
		// that could not read them could not read back what it had just
		// written. The generator fuzzer found that.
		if n := p.next(); n != nil && n.Type == TokL_PAREN {
			return p.parseFunction()
		}
		p.advance()
		return New(class), nil
	}

	// `IF cond THEN a [ELSE b] END` is an EXPRESSION as well as a statement,
	// and the reference reads both from ONE reader (Parser._parse_if, the
	// FUNCTION_PARSERS entry for IF). The parenthesised form is the ordinary
	// function and is read further down; this is the bare one.
	if p.atWords("IF") {
		// A dot after the word makes it a NAME qualified by what follows --
		// `if.x` -- exactly as the CASE branch below says of `case.*`.
		if n := p.next(); n != nil && n.Type != TokL_PAREN && n.Type != TokDOT {
			// The word is no more than a NAME when nothing that could be a
			// condition follows -- `SELECT if, a` -- so a failed read puts
			// the parser back where it was rather than failing the
			// statement (Parser._parse_if, which retreats and returns None).
			node, ok, err := p.tryParseIfExpression()
			if err != nil {
				return nil, err
			}
			if ok {
				return node, nil
			}
		}
	}

	switch c.Type {
	case TokCASE:
		// `case.*` is a COLUMN qualified by a table called case. The
		// reference has the rule in as many words -- a CASE sitting on a
		// dot retreats and is not a CASE at all -- so the word falls
		// through to the identifier branch below.
		if n := p.next(); n == nil || n.Type != TokDOT {
			return p.parseCase()
		}
	case TokDOT:
		// A number written without its leading zero: the tokenizer gives a
		// dot and a number, and the reference joins them and puts the zero
		// back -- `.5` is `0.5`, in the tree and on the way out.
		n := p.next()
		if n == nil || n.Type != TokNUMBER {
			break
		}
		p.advance()
		p.advance()
		return p.keep(New("Literal",
			Arg{"this", "0." + n.Text}, Arg{"is_string", false})), nil
	case TokNUMBER:
		p.advance()
		return p.keep(New("Literal", Arg{"this", c.Text}, Arg{"is_string", false})), nil
	case TokSTRING:
		p.advance()
		first := p.keep(New("Literal", Arg{"this", c.Text}, Arg{"is_string", true}))
		// Strings written NEXT TO each other are one string: `'x' 'y' 'z'`
		// is a concatenation, which is what the reference builds.
		if n := p.curr(); n != nil && n.Type == TokSTRING {
			items := []*Expression{first}
			for {
				n := p.curr()
				if n == nil || n.Type != TokSTRING {
					break
				}
				p.advance()
				items = append(items,
					p.keep(New("Literal", Arg{"this", n.Text}, Arg{"is_string", true})))
			}
			return New("Concat",
				Arg{"expressions", items}, Arg{"coalesce", concatCoalesce(p.dialect)}), nil
		}
		return first, nil
	// The tokenizer already tells these apart -- a raw string, a byte string,
	// a unicode string, a hex or bit literal -- and each is a class of its
	// own rather than a Literal with a flag, because what a dialect WRITES
	// for one has nothing to do with what it writes for another: `0x1F` is
	// `x'1F'` in PostgreSQL and `UNHEX('1F')` in DuckDB.
	case TokHEREDOC_STRING, TokRAW_STRING:
		p.advance()
		return New("RawString", Arg{"this", c.Text}), nil
	case TokBYTE_STRING:
		p.advance()
		// BigQuery's BYTE_STRING_IS_BYTES_TYPE is on, which records the
		// literal as a BYTES value (Parser.NUMERIC_PARSERS).
		if p.dialect == "bigquery" {
			return New("ByteString", Arg{"this", c.Text}, Arg{"is_bytes", true}), nil
		}
		return New("ByteString", Arg{"this", c.Text}), nil
	case TokUNICODE_STRING:
		p.advance()
		// `UESCAPE '!'` names the character that introduces an escape,
		// instead of the backslash PostgreSQL otherwise reads: `!0061`
		// rather than `\0061`. The clause is a plain STRING, not a Var.
		var escape any = false
		if p.atWords("UESCAPE") {
			p.advance()
			s := p.curr()
			if s == nil || s.Type != TokSTRING {
				return nil, p.unsupported("UESCAPE without a string")
			}
			p.advance()
			escape = New("Literal", Arg{"this", s.Text}, Arg{"is_string", true})
		}
		return New("UnicodeString", Arg{"this", c.Text}, Arg{"escape", escape}), nil
	case TokHEX_STRING:
		p.advance()
		// BigQuery and ClickHouse read a hex literal as an INTEGER, which
		// records it as one (Parser.NUMERIC_PARSERS).
		if p.dialect == "bigquery" || p.dialect == "clickhouse" {
			return New("HexString", Arg{"this", c.Text}, Arg{"is_integer", true}), nil
		}
		return New("HexString", Arg{"this", c.Text}), nil
	case TokBIT_STRING:
		p.advance()
		return New("BitString", Arg{"this", c.Text}), nil
	case TokTRUE, TokFALSE:
		p.advance()
		return New("Boolean", Arg{"this", c.Type == TokTRUE}), nil
	case TokNULL:
		p.advance()
		return New("Null"), nil
	case TokSTAR:
		p.advance()
		// `*COLUMNS(...)` unpacks the columns COLUMNS returns into the call
		// it stands inside -- an argument list rather than a star with
		// modifiers of its own, which is the only other thing a `*` here
		// can mean.
		if p.atWords("COLUMNS") && p.next() != nil && p.next().Type == TokL_PAREN {
			call, err := p.parseFunction()
			if err != nil {
				return nil, err
			}
			if call.Class == "Columns" {
				call.Set("unpack", true)
			}
			return call, nil
		}
		return p.starModifiers(p.keep(newStar()))
	}

	// A TYPE followed by a string is a typed literal -- `TIMESTAMP '2020-01-01'`,
	// `INET '127.0.0.1/32'` -- and the reference records it as an ordinary
	// CAST of the string. The shape is what makes it one: the same word with
	// anything else after it is a name.
	//
	// The word binds LOOSER than the cast operator, so
	// `TIMESTAMP 'x'::DATE` is a TIMESTAMP of a DATE and not the other way
	// round; the operand is therefore read with its own postfix casts before
	// this one wraps it.
	if _, isType := p.tables.TypeTokens[c.Type]; isType {
		if n := p.next(); n != nil && n.Type == TokSTRING {
			kind, err := p.parseDataType()
			if err != nil {
				return nil, err
			}
			if text := p.curr(); text == nil || text.Type != TokSTRING {
				// The type took the string as a parameter of its own, so
				// there is nothing left for it to be a literal of.
				return nil, p.unsupported("a typed literal with no value")
			}
			inner, err := p.parsePostfix()
			if err != nil {
				return nil, err
			}
			// `JSON '...'` is not a cast at all, in any dialect: the
			// reference reads it straight into a ParseJSON, the same node
			// the JSON_PARSE/JSON_VALID family of functions build.
			if kind.Args["this"] == DataTypeKind("JSON") {
				// The literal form, which writes back as JSON '...' rather
				// than JSON_PARSE(...) (Parser.TYPE_LITERAL_PARSERS).
				return New("ParseJSON", Arg{"this", inner}, Arg{"is_literal", true}), nil
			}
			// Presto reads a TIMESTAMP/TIME literal carrying a zone offset
			// or name as TIMESTAMPTZ/TIMETZ instead -- the type the literal
			// ACTUALLY names, not the bare word that introduced it.
			if p.tables.ZoneAwareTimestampConstructor && inner.Class == "Literal" {
				if text, _ := inner.Args["this"].(string); timeZoneInLiteral(text) {
					switch kind.Args["this"] {
					case DataTypeKind("TIMESTAMP"):
						kind = New("DataType", Arg{"this", DataTypeKind("TIMESTAMPTZ")})
					case DataTypeKind("TIME"):
						kind = New("DataType", Arg{"this", DataTypeKind("TIMETZ")})
					}
				}
			}
			cast := New("Cast", Arg{"this", inner}, Arg{"to", kind})
			cast.Type = kind
			if p.tables.SupportsColumnJoinMarks {
				if p.noJoinMark == nil {
					p.noJoinMark = map[*Expression]bool{}
				}
				p.noJoinMark[cast] = true
			}
			return cast, nil
		}
	}
	// A nested type opened with `<` cannot also be read as a column or a
	// call -- neither takes one -- so it is read as the bare TYPE itself:
	// `STRUCT<a INT>` standing on its own is the same DataType a CAST's
	// right side would build, not a name that happens to spell one.
	if kind, isType := p.tables.TypeTokens[c.Type]; isType && p.tables.NestedTypeKinds[kind] {
		if n := p.next(); n != nil && n.Type == TokLT {
			return p.parseDataType()
		}
	}

	switch c.Type {
	case TokL_PAREN:
		// Past a RUN of parentheses, because a query may be wrapped more than
		// once and every pair is a Subquery of its own: `((SELECT 1))` is one
		// inside another, not a Paren around one. And a query used as a
		// plain VALUE may still chain into a set operation the same as one
		// standing anywhere else does: `X((SELECT 1) UNION (SELECT 2))`
		// unions the function's own argument.
		if p.opensAParenthesisedQuery() {
			sub, err := p.parseScalarSubquery()
			if err != nil {
				return nil, err
			}
			return p.parseSetOperations(sub)
		}
		p.advance()
		inner, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		// A parenthesised item may be NAMED: `(x AS y)` is a Paren over an
		// Alias, and `(x AS y, y AS z)` a Tuple of them.
		inner, err = p.parseAlias(inner)
		if err != nil {
			return nil, err
		}
		// A COMMA makes it a row rather than a grouping. `(a, b)` is a Tuple,
		// which is what the left of `WHERE (a, b) IN (...)` is, and what
		// OVERLAPS compares. One item is a Paren, whatever it contains.
		if p.at(TokCOMMA) {
			items := []*Expression{inner}
			for p.match(TokCOMMA) {
				item, err := p.parseExpression()
				if err != nil {
					return nil, err
				}
				item, err = p.parseAlias(item)
				if err != nil {
					return nil, err
				}
				items = append(items, item)
			}
			if !p.match(TokR_PAREN) {
				return nil, p.unsupported("unclosed tuple")
			}
			return New("Tuple", Arg{"expressions", items}), nil
		}
		if !p.match(TokR_PAREN) {
			return nil, p.unsupported("unclosed parenthesis")
		}
		return New("Paren", Arg{"this", inner}), nil
	}
	// PostgreSQL's VARIADIC spreads an array over a call's parameters:
	// `MLEAST(VARIADIC ARRAY[10, -1])`. It is the word and then an
	// expression -- a no-paren function in the reference's terms, not
	// anything the call's own signature knows about -- and only PostgreSQL
	// has it, which is what its own table of no-paren names says. The token
	// has a type of its own, so this stands in front of the name branch
	// rather than inside it.
	//
	// The operand is a BITWISE expression: `VARIADIC a || b` spreads the
	// concatenation, and a comma ends it.
	if c.Type == TokVARIADIC {
		if _, ok := p.tables.NoParenFunctionNames["VARIADIC"]; ok {
			p.advance()
			inner, err := p.parseBitwise()
			if err != nil {
				return nil, err
			}
			return New("Variadic", Arg{"this", inner}), nil
		}
	}
	// `NEXT VALUE FOR seq` takes the sequence's next number. It is a no-paren
	// function whose name is three words, and only where the dialect has one.
	if _, hasNext := p.tables.NoParenFunctionNames["NEXT"]; hasNext &&
		p.atWords("NEXT", "VALUE", "FOR") {
		return p.parseNextValueFor()
	}
	// Punctuation that NAMES a function: DuckDB's `@x` is ABS(x). It lives
	// among the no-paren function names rather than among the prefix
	// operators, and it takes a whole arithmetic expression -- `@col + 1` is
	// ABS(col + 1). Keyed by the characters, because the same token type is
	// the parameter marker here too.
	// A name written in QUOTES is a name, never punctuation: `"@"` is a
	// column called @, and reading it as the operator turned `"@":x` into
	// ABS of a parameter -- SQL the port then could not read back. The
	// generator fuzzer found it.
	if class, ok := p.tables.PrefixCalls[c.Text]; ok && !isQuotedName(c) {
		p.advance()
		inner, err := p.parseBitwise()
		if err != nil {
			return nil, err
		}
		return New(class, Arg{"this", inner}), nil
	}
	// T-SQL's mark for a temporary table stands in front of a NAME wherever a
	// name may stand -- including a projection, where `SELECT #x` is a column
	// carrying the mark rather than an operator applied to one. Nothing else
	// in T-SQL begins with a #, so the token settles it on its own.
	if p.dialect == "tsql" && p.at(TokHASH) {
		return p.parseColumn()
	}
	// `#2` is the SECOND output column, in the dialect that reads it as one.
	if p.tables.PositionalColumns && p.at(TokHASH) {
		n := p.next()
		if n == nil || n.Type != TokNUMBER {
			return nil, p.unsupported("a positional column with no position")
		}
		p.advance()
		p.advance()
		return New("PositionalColumn", Arg{"this",
			New("Literal", Arg{"this", n.Text}, Arg{"is_string", false})}), nil
	}
	if p.atIdentifier() {
		// These names have a PARSER of their own in the reference, not a
		// signature -- and the refusal used to fire on the name alone. But
		// `IF(x, y, z)` and `MAP([1], [2])` are ordinary calls with ordinary
		// specs, and a bare `if` is a COLUMN. Only the form that needs the
		// dedicated parser is still refused, and it is the one with neither
		// parentheses nor a plain name after it.
		upper := strings.ToUpper(c.Text)
		_, noParen := p.tables.NoParenFunctionNames[upper]
		_, hasSpec := p.tables.Functions[upper]
		if !hasSpec {
			_, hasSpec = p.tables.FunctionsByArity[upper]
		}
		// A call with a signature the port HAS is parsed with it: `IF(x, y, z)`
		// and `MAP([1], [2])` are ordinary calls. ANY has a parser in the
		// reference and no signature here, so it stays refused rather than
		// becoming an Anonymous call the reference never builds.
		// An EMPTY argument list makes it an ordinary anonymous call whatever
		// the name: `ALL()` is Anonymous(ALL).
		// `MAP {'x': 1}` is a map LITERAL, not a call: the reference builds a
		// ToMap over a Struct whose keys stay literals, where a bare `{...}`
		// makes them identifiers.
		if upper == "MAP" && p.tables.MapBraceLiteral {
			if n := p.next(); n != nil && n.Type == TokL_BRACE {
				return p.parseMapLiteral()
			}
		}
		// Materialize's own MAP grammar, entirely its own: `MAP(SELECT ...)`
		// wraps a whole query, and `MAP[k => v, ...]` a bracketed list of
		// key/value pairs -- neither is an ordinary call this port's
		// argument-list reader could make sense of.
		if upper == "MAP" && p.dialect == "materialize" {
			if n := p.next(); n != nil && (n.Type == TokL_PAREN || n.Type == TokL_BRACKET) {
				return p.parseMaterializeMap()
			}
		}
		// Databricks' CURDATE takes NO argument, ever -- with or without
		// parentheses, and it errors on one rather than keeping it. The
		// probe describes the parenthesised form as taking one, because
		// what it actually does is match an opening paren and then require
		// a closing one right after, which a placeholder argument fails.
		if upper == "CURDATE" && p.dialect == "databricks" {
			p.advance()
			if p.match(TokL_PAREN) && !p.match(TokR_PAREN) {
				return nil, p.unsupported("CURDATE with an argument")
			}
			return New("CurrentDate"), nil
		}
		// Redshift's APPROXIMATE is a no-paren parser that retreats. COUNT
		// (DISTINCT ...) and PERCENTILE_DISC (...) WITHIN GROUP are the two
		// shapes it keeps; anything else, including `APPROXIMATE AS y`, is a
		// column of that name. The bare-name retreat cannot see this: COUNT is
		// reserved, so it does not look like an operand, and the word was read
		// as a column with the call left over.
		if upper == "APPROXIMATE" && noParen {
			approx, ok, err := p.parseApproximate()
			if err != nil {
				return nil, err
			}
			if ok {
				return approx, nil
			}
			// The parser put the word back. It is a column, including where
			// what follows could itself begin an expression (`AS`, `+`): the
			// bare-name test treats that as "still a call" and refuses it.
			return p.parseColumn()
		}
		// Redshift's SYSDATE is a timestamp, not GETDATE() and not a column.
		// The generator already writes CurrentTimestamp with this flag as
		// SYSDATE. A parenthesis is a different call, and a quoted name is
		// not this word. A name after a dot never reaches here.
		if upper == "SYSDATE" && noParen && c.Type != TokIDENTIFIER {
			if n := p.next(); n == nil || n.Type != TokL_PAREN {
				p.advance()
				return New("CurrentTimestamp", Arg{"sysdate", true}), nil
			}
		}
		if built := p.parseBareCurrentDateUTC(upper, noParen, c); built != nil {
			return built, nil
		}
		empty := p.namesAFunctionCall() && p.atEmptyArgList()
		if noParen && c.Type != TokCASE && !empty && (!hasSpec || !p.namesAFunctionCall()) &&
			!p.namesItselfNotACall(c) {
			return nil, p.unsupported("no-paren function " + strings.ToUpper(c.Text))
		}
		if n := p.next(); n != nil && n.Type == TokL_PAREN && !p.formatPhraseFollows() {
			if _, canName := p.tables.FuncTokens[c.Type]; !canName {
				return nil, p.unsupported("call named by a token that cannot name one")
			}
			return p.parseFunction()
		}
		return p.parseColumn()
	}
	// A word that is an OPERATOR here may still name a call: `ILIKE(x, 'z')`
	// and `XOR(a, b)` are the same names read the other way, and the token
	// table already says which of them may name one.
	if n := p.next(); n != nil && n.Type == TokL_PAREN && !p.formatPhraseFollows() {
		if _, canName := p.tables.FuncTokens[c.Type]; canName {
			return p.parseFunction()
		}
	}
	return nil, p.unsupported("expression")
}

// parseBareCurrentDateUTC reads Dremio's CURRENT_DATE_UTC when no parenthesis
// follows. That is today's date in UTC, the same tree CURRENT_DATE_UTC()
// already builds. A parenthesis stays that call, and a quoted name is a
// column. A name after a dot never reaches here. Nil means this token is
// not that word.
func (p *parser) parseBareCurrentDateUTC(upper string, noParen bool, c *Token) *Expression {
	if upper != "CURRENT_DATE_UTC" || !noParen || c.Type == TokIDENTIFIER {
		return nil
	}
	if n := p.next(); n != nil && n.Type == TokL_PAREN {
		return nil
	}
	p.advance()
	return buildDremioCurrentDateUTC()
}

// newStar builds a bare `*`. The reference constructs it with four modifier
// args, all empty; they dump as nothing but are kept so the shape is the
// reference's rather than a lookalike.
func newStar() *Expression {
	return New("Star", Arg{"ilike", nil}, Arg{"except_", nil}, Arg{"replace", nil}, Arg{"rename", nil})
}

// starModifiers reads what may follow a `*`: `EXCEPT (a, b)` drops columns and
// `REPLACE (a AS b)` swaps them. Both are lists on the Star itself rather than
// anything wrapping it, so `SELECT * EXCEPT (a)` is one projection.
//
// EXCEPT is also a set operation, which is why the port used to read this as
// one and refuse the statement for having no SELECT after it.
func (p *parser) starModifiers(star *Expression) (*Expression, error) {
	for {
		// ClickHouse applies a function to every column a `*` names:
		// `* APPLY(sum)` (ClickHouseParser._parse_star_ops).
		if p.dialect == "clickhouse" && p.at(TokAPPLY) {
			p.advance()
			if !p.match(TokL_PAREN) {
				return nil, p.unsupported("APPLY without a parenthesis")
			}
			name := p.parseIDVarAny()
			if name == nil {
				return nil, p.unsupported("APPLY without a name")
			}
			if !p.match(TokR_PAREN) {
				return nil, p.unsupported("unclosed APPLY")
			}
			star = New("Apply", Arg{"this", star},
				Arg{"expression", New("Var", Arg{"this", name.Name()})})
			continue
		}
		var key string
		switch {
		// EXCEPT and EXCLUDE are one list under two words -- DuckDB writes
		// the second, and the reference matches either into the same slot.
		case p.at(TokEXCEPT), p.atWords("EXCLUDE"):
			key = "except_"
		case p.atWords("REPLACE"):
			key = "replace"
		case p.atWords("RENAME"):
			key = "rename"
		default:
			return star, nil
		}
		if next := p.next(); next == nil || next.Type != TokL_PAREN {
			// EXCEPT with no list after it is the set operation.
			return star, nil
		}
		p.advance()
		p.advance()
		var items []*Expression
		for {
			item, err := p.parseExpression()
			if err != nil {
				return nil, err
			}
			item, err = p.parseAlias(item)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
			if !p.match(TokCOMMA) {
				break
			}
		}
		if !p.match(TokR_PAREN) {
			return nil, p.unsupported("unclosed star modifier")
		}
		star.Set(key, items)
	}
}

// parseDataTypeText parses a type WRITTEN AS TEXT, which ClickHouse allows
// inside a quoted name: `.:"Array(JSON)"` (Parser._parse_types,
// exp.DataType.from_str).
func (p *parser) parseDataTypeText(text string) (*Expression, error) {
	tk, err := NewTokenizer(p.dialect)
	if err != nil {
		return nil, err
	}
	toks, err := tk.Tokenize(text)
	if err != nil {
		return nil, err
	}
	cfg := tk.Config()
	sub := &parser{tokens: toks, cfg: cfg, tables: cfg.Tables, dialect: p.dialect, sql: text}
	dt, err := sub.parseDataType()
	if err != nil {
		return nil, err
	}
	if sub.curr() != nil {
		return nil, p.unsupported("a quoted type with trailing tokens")
	}
	return dt, nil
}

// dotted reads `.name` after something that is not part of an identifier
// chain. A column's dots are its qualifiers and parseColumn owns those; a
// PLACEHOLDER's are a Dot node, which is the only place this port builds one.
//
// The generator fuzzer found it twice: the port wrote `ARRAY(:A.a)` and could
// not read it back, because it had no rule for a dot in that position.
func (p *parser) dotted(this *Expression) *Expression {
	for p.at(TokDOT) {
		n := p.next()
		var right *Expression
		switch {
		case n == nil:
			return this
		case n.Type == TokSTRING:
			// A STRING after the dot stays a string: `$0.'AS'` is a Dot over a
			// Literal, not over an identifier called AS.
			right = New("Literal", Arg{"this", n.Text}, Arg{"is_string", true})
		case n.Type == TokNATIONAL_STRING:
			right = New("National", Arg{"this", n.Text})
		case isParameterName(n) && n.Type != TokNUMBER:
			right = New("Identifier", Arg{"this", n.Text}, Arg{"quoted", false})
		default:
			return this
		}
		p.advance()
		p.advance()
		this = New("Dot", Arg{"this", this}, Arg{"expression", right})
	}
	return this
}

// dispatchByType picks the signature for the type an argument CARRIES.
//
// Carries, not infers. The reference's builder runs while parsing, before
// anything is annotated, so the only argument with a type at that moment is
// one written as an explicit CAST. Everything else -- a column, a sum, a
// date plus an interval -- has no type yet and takes the default.
//
// Annotating here instead looked more thorough and was wrong: the annotator
// types `CAST(x AS DATE) + INTERVAL '1' DAY` as a DATE, which it is, and the
// reference still builds the default because at parse time it was just an
// Add. Eleven statements said so.
func dispatchByType(d TypeDispatch, arg *Expression) FuncSpec {
	if arg != nil && arg.Type != nil {
		if spec, ok := d.ByType[typeKind(arg.Type)]; ok {
			return spec
		}
	}
	return d.Default
}

// atEmptyArgList reports whether the name at the cursor is followed by `()`.
func (p *parser) atEmptyArgList() bool {
	after := p.peekAt(2)
	return after != nil && after.Type == TokR_PAREN
}

// isParameterName reports whether a token can name a bound parameter or a
// `@` parameter: a word, which includes keywords -- `$WHERE` is a parameter
// called WHERE, not the start of a clause.
func isParameterName(t *Token) bool {
	if t == nil {
		return false
	}
	switch t.Type {
	case TokNUMBER, TokVAR:
		// Whatever the tokenizer called a word is a name here, including one
		// no human would write: the reference reads `:\x01` as a parameter
		// called \x01, and a stricter rule refused SQL the port itself had
		// just written.
		return true
	case TokIDENTIFIER:
		// A QUOTED name is a different node -- `@"x"` carries an Identifier,
		// not a Var -- and is left to the rules below.
		return false
	}
	// A KEYWORD used as a name: `$WHERE` is a parameter called WHERE.
	return isBareIdentifier(t.Text)
}

// parseNamedPercentPlaceholder reads PostgreSQL's `%(name)s`. It reports
// failure rather than erroring, so a `%` that opens something else is left for
// the rules below to refuse in their own words.
func (p *parser) parseNamedPercentPlaceholder() (*Expression, bool) {
	start := p.index
	p.advance() // %
	if !p.match(TokL_PAREN) {
		p.index = start
		return nil, false
	}
	// Any word, keywords included: `%(name)s` is a parameter called `name`,
	// and requiring a VAR here refused exactly that -- masked at first by
	// testing with `id_param`, which is not a keyword.
	name := p.curr()
	if !isParameterName(name) {
		p.index = start
		return nil, false
	}
	p.advance()
	if !p.match(TokR_PAREN) {
		p.index = start
		return nil, false
	}
	suffix := p.curr()
	if suffix == nil || !strings.EqualFold(suffix.Text, "s") {
		p.index = start
		return nil, false
	}
	p.advance()
	id := New("Identifier", Arg{"this", name.Text}, Arg{"quoted", false})
	return New("Placeholder", Arg{"this", id}), true
}

// parseCast reads CAST(x AS type) and TRY_CAST(x AS type).
//
// The node also carries a type annotation -- the reference reports a cast's
// type as the type it casts to -- which dumps as its own nested record list.
func (p *parser) parseCast(try bool) (*Expression, error) {
	p.advance() // the name
	p.advance() // the opening parenthesis

	this, err := p.parseExpression()
	if err != nil {
		return nil, err
	}
	if !p.match(TokALIAS) {
		// `CAST(x, 'type')` names the type as a STRING rather than reading
		// it as a type (Parser._parse_cast).
		if p.match(TokCOMMA) {
			to, err := p.parsePrimary()
			if err != nil {
				return nil, err
			}
			if !p.match(TokR_PAREN) {
				return nil, p.unsupported("unclosed CAST")
			}
			return New("CastToStrType", Arg{"this", this}, Arg{"to", to}), nil
		}
		return nil, p.unsupported("CAST without AS")
	}
	// Teradata names the FORMAT where the TYPE would go: `CAST(x AS FORMAT
	// 'YYYY-DD')` has no type at all, and the reference reads the format
	// before the type (Parser._parse_cast). The type is UNKNOWN, which the
	// generator drops in favour of the phrase.
	var format *Expression
	formatFirst := false
	if p.at(TokFORMAT) {
		p.advance()
		format = p.tryParseStringLiteral()
		if format == nil {
			return nil, p.unsupported("CAST AS FORMAT without a format")
		}
		formatFirst = true
	}
	// The collation belongs to the TYPE here, not to a column: inside a cast
	// there is no column for it to belong to.
	var to *Expression
	if formatFirst {
		to = New("DataType", Arg{"this", DataTypeKind("UNKNOWN")})
	} else if to, err = p.parseCollatedDataType(); err != nil {
		return nil, err
	}
	// `CAST(x AS CHAR CHARACTER SET latin1)` casts to a character set.
	if kind, _ := to.Args["this"].(DataTypeKind); kind == "CHAR" && p.dialect == "mysql" && (p.at(TokCHARACTER_SET) || p.atWords("CHARACTER", "SET")) {
		if p.at(TokCHARACTER_SET) {
			p.advance()
		} else {
			p.advance()
			p.advance()
		}
		c := p.curr()
		if c == nil {
			return nil, p.unsupported("CHARACTER SET without a name")
		}
		var name *Expression
		switch c.Type {
		case TokSTRING:
			name = New("Literal", Arg{"this", c.Text}, Arg{"is_string", true})
		case TokIDENTIFIER:
			return nil, p.unsupported("CHARACTER SET a quoted name")
		default:
			name = New("Var", Arg{"this", c.Text})
		}
		p.advance()
		to = New("DataType", Arg{"this", DataTypeKind("CHARACTER_SET")}, Arg{"kind", name})
	}
	// `CAST(x AS STRING FORMAT 'DAY')` names the format the value is read or
	// written with (Parser._parse_cast).
	if format == nil && p.atWords("FORMAT") {
		p.advance()
		// The format may be written inside parentheses
		// (Parser._parse_cast, _parse_wrapped).
		wrapped := p.match(TokL_PAREN)
		c := p.curr()
		if c == nil || c.Type != TokSTRING {
			return nil, p.unsupported("CAST FORMAT without a string")
		}
		format = New("Literal", Arg{"this", c.Text}, Arg{"is_string", true})
		p.advance()
		if wrapped && !p.match(TokR_PAREN) {
			return nil, p.unsupported("unclosed CAST FORMAT")
		}
		// `... FORMAT ('f') AT TIME ZONE z` names the zone the text is read
		// in. It wraps the format, and a cast that becomes a StrToTime moves
		// the zone onto itself (Parser._parse_at_time_zone, _parse_cast).
		if p.atWords("AT", "TIME", "ZONE") {
			p.advance()
			p.advance()
			p.advance()
			zone, err := p.parseUnary()
			if err != nil {
				return nil, err
			}
			format = New("AtTimeZone", Arg{"this", format}, Arg{"zone", zone})
		}
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed CAST")
	}
	// A TEMPORAL cast carrying a format builds a StrToDate/StrToTime with the
	// format MAPPED through the dialect's own (Parser._parse_cast).
	if format != nil {
		if kind, _ := to.Args["this"].(DataTypeKind); kind == "DATE" || kind == "TIMESTAMP" ||
			kind == "TIMESTAMPTZ" || kind == "DATETIME" || kind == "TIME" {
			fmtLiteral := format
			var zone *Expression
			if format.Class == "AtTimeZone" {
				fmtLiteral, zone = childOf(format, "this"), childOf(format, "zone")
			}
			text, _ := fmtLiteral.Args["this"].(string)
			// FORMAT_MAPPING is read FIRST where a dialect has one --
			// BigQuery's alone -- and the dialect's TIME_MAPPING after it
			// (Parser._parse_cast).
			mapping := p.tables.FormatMapping
			if len(mapping) == 0 {
				mapping = p.tables.FormatTimeMapping
			}
			if len(mapping) == 0 {
				mapping = p.tables.TimeMapping
			}
			class := "StrToTime"
			if kind == "DATE" {
				class = "StrToDate"
			}
			node := New(class, Arg{"this", this},
				Arg{"format", New("Literal", Arg{"this", formatTime(text, mapping)}, Arg{"is_string", true})})
			if try {
				node.Set("safe", true)
			}
			// A zone the format named goes on the StrToTime, where the
			// reference keeps it (Parser._parse_cast).
			if zone != nil && class == "StrToTime" {
				node.Set("zone", zone)
			}
			return p.keep(node), nil
		}
	}
	to = p.hiveCastType(to)

	class := "Cast"
	args := []Arg{
		{"this", this}, {"to", to}, {"format", format},
		{"safe", nil}, {"action", nil}, {"default", nil},
	}
	if try {
		// TRY_CAST is a Cast that is flagged safe, not a silent variant.
		class = "TryCast"
		args[3] = Arg{"safe", true}
		args = append(args, Arg{"requires_string", nil})
	} else if !p.tables.StrictCast {
		// Hive's STRICT_CAST is off, so its CAST is a TryCast too -- one with
		// no `safe` flag, which is what tells the two spellings apart.
		class = "TryCast"
	}
	cast := New(class, args...)
	cast.Type = to
	return p.keep(cast), nil
}

// hiveCastType is Hive's own `_parse_types` rule: outside a schema definition a
// CAST to CHAR(length)/VARCHAR(length) is a cast to TEXT -- Spark treats
// char/varchar as string there and does not truncate, so the length is
// dropped rather than carried (HiveParser._parse_types).
func (p *parser) hiveCastType(to *Expression) *Expression {
	if !isHiveFamily(p.dialect) || to == nil || to.Class != "DataType" {
		return to
	}
	// The reference runs its rewrite over the WHOLE type, so a char/varchar
	// nested inside a STRUCT or ARRAY is rewritten too.
	var rewrite func(*Expression)
	rewrite = func(n *Expression) {
		if n == nil {
			return
		}
		if n.Class == "DataType" {
			kind, _ := n.Args["this"].(DataTypeKind)
			if k := strings.ToUpper(string(kind)); k == "CHAR" || k == "VARCHAR" {
				n.Set("this", DataTypeKind("TEXT"))
				n.Set("expressions", nil)
			}
		}
		for _, key := range n.Keys {
			switch v := n.Args[key].(type) {
			case *Expression:
				rewrite(v)
			case []*Expression:
				for _, x := range v {
					rewrite(x)
				}
			}
		}
	}
	rewrite(to)
	return to
}

// parseDataType reads a type name, its parameters, and any array suffix.
//
// The reference splits a type's parameters three ways by what the type is: a
// STRUCT-like type names its fields, another nested type lists bare types, and
// a plain type takes sizes. All three are read here; anything outside them is
// refused rather than built as one of the three.
func (p *parser) parseDataType() (*Expression, error) {
	dt, err := p.parseBaseDataType()
	if err != nil {
		return nil, err
	}
	// Materialize's `INT LIST LIST` -- a list of lists of integers -- is read
	// by the base parser, so in every dialect, right after the base type and
	// before any array suffix.
	for p.at(TokLIST) {
		p.advance()
		dt = New("DataType",
			Arg{"this", DataTypeKind("LIST")},
			Arg{"expressions", []*Expression{dt}},
			Arg{"nested", true})
	}
	dt, err = p.parseArraySuffix(dt)
	if err != nil {
		return nil, err
	}
	p.convertType(dt)
	// Spark reads a cast to CHAR(n) or VARCHAR(n) as a cast to STRING, and
	// the reference drops the length everywhere but a column's definition,
	// members of a nested type included.
	if p.dialect == "databricks" && !p.inColumnType {
		dt.Walk(func(n *Expression) bool {
			if n.Class != "DataType" {
				return true
			}
			if k, _ := n.Args["this"].(DataTypeKind); k == "CHAR" || k == "VARCHAR" {
				n.Set("this", DataTypeKind("TEXT"))
				n.Set("expressions", nil)
			}
			return true
		})
	}
	// ClickHouse marks every type it READS as not nullable -- that is its
	// default -- so that one written `Nullable(...)` can be told from one
	// that was not, which is what its writer consults
	// (ClickHouseParser._parse_types).
	if p.dialect == "clickhouse" && dt != nil && dt.Class == "DataType" &&
		dt.Args["nullable"] != true {
		dt.Set("nullable", false)
	}
	return dt, nil
}

// convertType is the reference's TYPE_CONVERTERS, applied as it applies
// them: to the OUTERMOST type read, after any LIST or array suffix, so the
// member of `VARCHAR(3)[]` keeps its length where a bare `VARCHAR(3)` does
// not.
func (p *parser) convertType(dt *Expression) {
	if dt == nil || dt.Class != "DataType" {
		return
	}
	kind, _ := dt.Args["this"].(DataTypeKind)
	if nested, _ := dt.Args["nested"].(bool); nested {
		return
	}
	params, _ := dt.Args["expressions"].([]*Expression)
	switch {
	// DuckDB reads every text type as TEXT and drops the length, so
	// `VARCHAR(5)` is a bare TEXT. Keeping the 5 sent the engine a
	// different CAST than the Python executor sent.
	case len(params) > 0 && p.tables.DropsTypeParams[string(kind)]:
		dt.Set("expressions", nil)
	// A bare type that this dialect reads as parameterised. DuckDB's
	// `numeric` is DECIMAL(18, 3), and leaving it bare sent the engine a
	// different CAST from the one the Python executor sends -- on a
	// division, a different number rather than a different spelling.
	case len(params) == 0 && len(p.tables.DefaultTypeParams[string(kind)]) > 0:
		defaults := p.tables.DefaultTypeParams[string(kind)]
		params := make([]*Expression, 0, len(defaults))
		for _, v := range defaults {
			params = append(params, New("DataTypeParam",
				Arg{"this", New("Literal", Arg{"this", v}, Arg{"is_string", false})}))
		}
		// Right after the kind, where the reference's freshly built node
		// has it, not after `nested`.
		dt.Set("expressions", params)
		keys := []string{"this", "expressions"}
		for _, k := range dt.Keys {
			if k != "this" && k != "expressions" {
				keys = append(keys, k)
			}
		}
		dt.Keys = keys
	}
}

// parseBracketedMapType reads Materialize's `MAP[TEXT => INT]`, which the
// base parser reads in every dialect whose `[` is a bracket. Anything else
// after the bracket is not this type (nil), and the tokens are left as they
// were.
func (p *parser) parseBracketedMapType() *Expression {
	start := p.index
	p.advance() // MAP
	p.advance() // [
	key, err := p.parseDataType()
	if err != nil || !p.match(TokFARROW) {
		p.index = start
		return nil
	}
	value, err := p.parseDataType()
	if err != nil || !p.match(TokR_BRACKET) {
		p.index = start
		return nil
	}
	return New("DataType",
		Arg{"this", DataTypeKind("MAP")},
		Arg{"expressions", []*Expression{key, value}},
		Arg{"nested", true})
}

// parseCollatedDataType reads a type that may name the COLLATION its values
// are compared under: `ARRAY<STRING COLLATE UTF8_BINARY>`.
//
// Only the members of a nested type may say it. A type standing on its own
// takes no collation here, because COLLATE after one is the COLUMN's rather
// than the type's, and reading it here would take it off the column.
func (p *parser) parseCollatedDataType() (*Expression, error) {
	dt, err := p.parseDataType()
	if err != nil {
		return nil, err
	}
	if dt == nil || !p.match(TokCOLLATE) {
		return dt, nil
	}
	// A QUOTED name is an identifier and an unquoted one a column: the
	// reference reads the two down different paths, and reading both as a
	// column built a tree that differed over nothing the SQL shows.
	var name *Expression
	if c := p.curr(); c != nil && c.Type == TokIDENTIFIER {
		name, err = p.parseIdentifier()
	} else {
		name, err = p.parseColumn()
	}
	if err != nil {
		return nil, err
	}
	dt.Set("collate", name)
	return dt, nil
}

func (p *parser) parseBaseDataType() (*Expression, error) {
	c := p.curr()
	if c == nil {
		return nil, p.unsupported("type")
	}
	// PostgreSQL's OID and its `reg*` family name a slot in the catalog
	// rather than a value's shape, so the reference reads each as its own
	// ObjectIdentifier node, keyed by nothing but the word itself; CSTRING
	// is the same idea one level down, a PseudoType rather than a real one.
	// Databricks' VOID is read as the ordinary NULL type instead -- there is
	// no word for it in the generated DataType.
	switch c.Type {
	case TokPSEUDO_TYPE:
		p.advance()
		return New("PseudoType", Arg{"this", strings.ToUpper(c.Text)}), nil
	case TokOBJECT_IDENTIFIER:
		p.advance()
		return New("ObjectIdentifier", Arg{"this", strings.ToUpper(c.Text)}), nil
	case TokVOID:
		p.advance()
		return New("DataType", Arg{"this", DataTypeKind("NULL")}), nil
	}
	kind, ok := p.tables.TypeTokens[c.Type]
	if !ok {
		// PostgreSQL keeps a quoted type of its own as USER-DEFINED:
		// `CAST(65 AS "char")` is a type named char, not the CHAR type
		// (Parser.QUOTED_TYPES_TO_PRESERVE).
		if c.Type == TokIDENTIFIER && p.quotedTypePreserved(c.Text) {
			p.advance()
			return New("DataType",
				Arg{"this", DataTypeKind("USER-DEFINED")},
				Arg{"kind", New("Identifier",
					Arg{"this", c.Text}, Arg{"quoted", true})}), nil
		}
		// A type may be written in QUOTES -- T-SQL brackets one, `[a] [int]`
		// -- which makes it an identifier to the tokenizer rather than a type
		// word. The reference lexes the identifier's TEXT again and takes the
		// type from that, so the same name works quoted or bare.
		if quoted, name := p.quotedTypeName(c); quoted {
			kind, ok = name, true
		} else if named := p.quotedNamedTypeWord(c); named != nil {
			// A quoted `"oid"` re-lexes to the very keyword an unquoted one
			// does -- an ObjectIdentifier or PseudoType, not a name -- in
			// whichever dialect has that keyword at all; DuckDB has none of
			// PostgreSQL's, so `x::"oid"` names a USER-DEFINED type there
			// instead, same as any other unrecognized word.
			p.advance()
			return named, nil
		}
	}
	if !ok {
		// A word the dialect has no type for is a USER-DEFINED one, named by
		// the word itself. The reference reads any name that way rather than
		// refusing, which is how a schema's own types reach a cast. The
		// words read specially instead of as a name -- PostgreSQL's OID
		// family, CSTRING, Databricks' VOID -- are caught above, by token
		// type, before this fallback is ever reached.
		// Only a plain NAME, never a keyword: `CREATE TABLE t (a DEFAULT 0)`
		// declares a typeless column with a default, and reading DEFAULT as
		// the name of a type made the constraint disappear into it.
		if c.Type == TokVAR || c.Type == TokIDENTIFIER {
			p.advance()
			// A QUOTED name is lexed again to see whether it names anything
			// at all: `"``"` is not a name in any dialect, and the reference
			// falls back to the UNKNOWN type rather than making one. Written
			// as a name it came back out unreadable -- the generator fuzzer
			// found it.
			if c.Type == TokIDENTIFIER && !p.namesAType(c.Text) {
				return New("DataType",
					Arg{"this", DataTypeKind("UNKNOWN")},
					Arg{"nested", false}), nil
			}
			var named any = c.Text
			if p.tables.UserDefinedTypeIsIdentifier {
				// A user-defined type may be SCHEMA-QUALIFIED here too, but
				// kept as a chain of DOTS over identifiers rather than
				// joined into one string -- `a.b.c` is Dot(Dot(a, b), c),
				// left-associative the way any other dotted name is.
				dotted := New("Identifier",
					Arg{"this", c.Text}, Arg{"quoted", c.Type == TokIDENTIFIER})
				for n := p.next(); p.at(TokDOT) && n != nil &&
					(n.Type != TokL_PAREN && n.Type != TokR_PAREN &&
						n.Type != TokCOMMA && n.Type != TokDOT); n = p.next() {
					p.advance()
					p.advance()
					dotted = New("Dot", Arg{"this", dotted}, Arg{"expression",
						New("Identifier", Arg{"this", n.Text}, Arg{"quoted", n.Type == TokIDENTIFIER})})
				}
				named = dotted
			} else {
				// A user-defined type may be SCHEMA-QUALIFIED -- `a.b.c` --
				// and the reference keeps the whole dotted name as one
				// string, joining each part's own text whether it was
				// quoted or not: `"a.b".c` and `a.b.c` both give `a.b.c`.
				// Read here, or the generator's own dotted names -- another
				// USER-DEFINED type's own qualifier -- came back unreadable.
				text, _ := named.(string)
				for n := p.next(); p.at(TokDOT) && n != nil &&
					(n.Type != TokL_PAREN && n.Type != TokR_PAREN &&
						n.Type != TokCOMMA && n.Type != TokDOT); n = p.next() {
					p.advance()
					p.advance()
					text += "." + n.Text
				}
				named = text
			}
			return New("DataType",
				Arg{"this", DataTypeKind("USER-DEFINED")},
				Arg{"kind", named}), nil
		}
		// ClickHouse's `Nullable(T)` is not a type of its own: it IS the
		// inner type with the nullability flag set, which is how the
		// reference keeps it (the base parser's NULLABLE branch).
		if p.dialect == "clickhouse" && c.Type == TokNULLABLE &&
			p.next() != nil && p.next().Type == TokL_PAREN {
			p.advance() // NULLABLE
			p.advance() // (
			inner, err := p.parseCollatedDataType()
			if err != nil {
				return nil, err
			}
			if !p.match(TokR_PAREN) {
				return nil, p.unsupported("unclosed Nullable")
			}
			if inner != nil && inner.Class == "DataType" {
				inner.Set("nullable", true)
			}
			return inner, nil
		}
		return nil, p.unsupported("type " + c.Text)
	}
	if c.Type == TokMAP && p.next() != nil && p.next().Type == TokL_BRACKET {
		if dt := p.parseBracketedMapType(); dt != nil {
			return dt, nil
		}
	}
	// ClickHouse's Dynamic type takes assignments rather than sizes:
	// `Dynamic(max_types = 10)` (Parser._parse_types, TYPE_TOKENS).
	if c.Type == TokDYNAMIC {
		p.advance()
		dt := New("DataType", Arg{"this", DataTypeKind("DYNAMIC")})
		if p.at(TokL_PAREN) {
			p.advance()
			var params []*Expression
			for !p.at(TokR_PAREN) {
				e, perr := p.parseAssignment()
				if perr != nil {
					return nil, perr
				}
				params = append(params, e)
				if !p.match(TokCOMMA) {
					break
				}
			}
			if !p.match(TokR_PAREN) {
				return nil, p.unsupported("unclosed Dynamic")
			}
			dt.Set("expressions", params)
		}
		dt.Set("nested", false)
		dt.Set("nullable", false)
		return dt, nil
	}
	// INTERVAL as a type carries a UNIT, and the DataType's `this` is an
	// Interval node rather than a type name -- `CAST(x AS INTERVAL DAY)` is
	// DataType(Interval(unit=Var(DAY))). A word that is not a unit means
	// there is no unit: `CAST(x AS INTERVAL)` is a bare interval type.
	if c.Type == TokINTERVAL {
		p.advance()
		return p.parseIntervalType(), nil
	}
	// An ENUM or a MySQL SET carries the VALUES it may take rather than a
	// size: they are strings, and what a sized type takes there is a number.
	if (c.Type == TokENUM || c.Type == TokENUM8 || c.Type == TokENUM16 ||
		c.Type == TokSET) && p.next() != nil && p.next().Type == TokL_PAREN {
		p.advance()
		p.advance() // the opening parenthesis
		// `ENUM ()` takes no members at all -- PostgreSQL writes exactly this
		// for one with none, since it is the one dialect that keeps the
		// parentheses even when they are empty -- and parseWrappedCSV always
		// reads at least one, since every OTHER caller's list requires one.
		var members []*Expression
		if !p.at(TokR_PAREN) {
			for {
				// An ENUM member is an EQUALITY, so `Enum('a' = 1)` keeps the
				// value it is given (Parser._parse_types).
				m, err := p.parseEquality()
				if err != nil {
					return nil, err
				}
				members = append(members, m)
				if !p.match(TokCOMMA) {
					break
				}
			}
		}
		if !p.match(TokR_PAREN) {
			return nil, p.unsupported("unclosed ENUM member list")
		}
		return New("DataType",
			Arg{"this", DataTypeKind(kind)},
			Arg{"expressions", members},
			Arg{"nested", false}), nil
	}
	// ClickHouse's AGGREGATEFUNCTION takes a function (or its name) and then
	// column types (Parser._parse_types, is_aggregate).
	if (kind == "AGGREGATEFUNCTION" || kind == "SIMPLEAGGREGATEFUNCTION") &&
		p.dialect == "clickhouse" && p.next() != nil && p.next().Type == TokL_PAREN {
		p.advance()
		p.advance() // the opening parenthesis
		var params []*Expression
		var funcOrIdent *Expression
		var err error
		if p.atIdentifier() && p.next() != nil && p.next().Type == TokL_PAREN {
			// The reference reads this name with anonymous=True, so a name
			// that is otherwise a class of its own -- `any(x)` is AnyValue --
			// stays the AnonymousAggFunc the aggregate resolver makes of it
			// (ClickHouseParser._parse_function).
			funcOrIdent, err = p.parseClickhouseAnonymousFunc()
			if err != nil {
				return nil, err
			}
		} else {
			funcOrIdent = p.parseIDVarAny()
		}
		if funcOrIdent == nil {
			return nil, p.unsupported("an aggregate type without a function")
		}
		params = append(params, funcOrIdent)
		if p.match(TokCOMMA) {
			for {
				arg, err := p.parseDataType()
				if err != nil {
					return nil, err
				}
				params = append(params, arg)
				if !p.match(TokCOMMA) {
					break
				}
			}
		}
		if !p.match(TokR_PAREN) {
			return nil, p.unsupported("unclosed aggregate type")
		}
		return New("DataType", Arg{"this", DataTypeKind(kind)},
			Arg{"expressions", params}, Arg{"nested", false}), nil
	}
	// ClickHouse's JSON type takes a list of column definitions and SKIP
	// clauses rather than sizes (Parser._parse_types, _parse_json_type_arg).
	if kind == "JSON" && p.dialect == "clickhouse" && p.next() != nil && p.next().Type == TokL_PAREN {
		p.advance()
		p.advance() // the opening parenthesis
		var params []*Expression
		for !p.at(TokR_PAREN) {
			param, err := p.parseClickhouseJSONTypeArg()
			if err != nil {
				return nil, err
			}
			params = append(params, param)
			if !p.match(TokCOMMA) {
				break
			}
		}
		if !p.match(TokR_PAREN) {
			return nil, p.unsupported("unclosed JSON type")
		}
		return New("DataType", Arg{"this", DataTypeKind("JSON")},
			Arg{"expressions", params}, Arg{"nested", false}), nil
	}
	p.advance()

	nested := p.tables.NestedTypeKinds[kind]
	isStruct := p.tables.StructTypeKinds[kind]
	dt := New("DataType", Arg{"this", DataTypeKind(kind)})

	// A nested type takes either delimiter -- DuckDB writes STRUCT(a INT) and
	// Databricks STRUCT<a INT>, and both read to the same tree. A plain type
	// takes only parentheses; `<` after one is something else entirely.
	open, close := TokL_PAREN, TokR_PAREN
	if nested && p.at(TokLT) {
		open, close = TokLT, TokGT
	} else if !nested && p.at(TokLT) {
		return nil, p.unsupported("parameterised composite type")
	}

	if p.match(open) {
		var params []*Expression
		for {
			var param *Expression
			var err error
			switch {
			case isStruct:
				param, err = p.parseStructField()
			case nested:
				param, err = p.parseCollatedDataType()
			default:
				param, err = p.parseTypeSize()
			}
			if err != nil {
				return nil, err
			}
			params = append(params, param)
			if !p.match(TokCOMMA) {
				break
			}
		}
		if !p.match(close) {
			return nil, p.unsupported("unclosed type parameters")
		}
		// SingleStore writes a VECTOR's element type SECOND but stores it
		// first, resolving the short alias (I32 -> INT) on the way
		// (SingleStoreParser._parse_vector_expressions).
		if kind == "VECTOR" && len(params) == 2 {
			// The element type is a type parameter (a DataTypeParam holding
			// the word), so its name is read from inside.
			typeName := ""
			if t := params[1]; t != nil {
				if inner, _ := t.Args["this"].(*Expression); inner != nil {
					typeName = strings.ToUpper(inner.Name())
				} else {
					typeName = strings.ToUpper(t.Name())
				}
			}
			if alias, ok := p.tables.VectorTypeAliases[typeName]; ok {
				typeName = alias
			}
			params = []*Expression{
				New("DataType", Arg{"this", DataTypeKind(typeName)}, Arg{"nested", false}),
				params[0],
			}
		}
		dt.Set("expressions", params)

		// `ARRAY<INT64>[1, 2]` writes the array's VALUES inside the type, and
		// the reference builds the cast that produces them: an Array of
		// those values cast to the type just read. Only the `<...>` form --
		// a `(...)` type leaves the bracket to the ordinary reader
		// (Parser._parse_types).
		if open == TokLT && (p.at(TokL_BRACKET) || p.at(TokL_PAREN)) {
			// The values are closed by the bracket that OPENED them, and by
			// that one alone: `UNNEST(ARRAY<INT64>[1, 2])` has a parenthesis
			// of its own still to come (Parser._parse_types).
			closer := TokR_BRACKET
			if p.at(TokL_PAREN) {
				closer = TokR_PAREN
			}
			p.advance()
			var values []*Expression
			for !p.at(TokR_BRACKET) && !p.at(TokR_PAREN) {
				v, err := p.parseDisjunction()
				if err != nil {
					return nil, err
				}
				if v == nil {
					break
				}
				values = append(values, v)
				if !p.match(TokCOMMA) {
					break
				}
			}
			if len(values) == 0 && isStruct {
				// An empty STRUCT's bracket is left for the ordinary reader.
				p.index--
			} else {
				p.match(closer)
				cls := "Array"
				if isStruct {
					cls = "Struct"
				}
				dt.Set("nested", nested)
				cast := New("Cast", Arg{"this", New(cls, Arg{"expressions", values})},
					Arg{"to", dt})
				cast.Type = dt
				return cast, nil
			}
		}
	}
	// `TIMESTAMP WITH TIME ZONE` is a type of its own rather than a timestamp
	// carrying a flag, and WITHOUT says only that the plain one was meant. The
	// zoned type is built fresh, which is why it carries no `nested` where the
	// plain one does.
	if _, timestamp := p.tables.TimestampTypeTokens[c.Type]; timestamp {
		_, isTime := p.tables.TimeTypeTokens[c.Type]
		switch {
		case p.atWords("WITH", "TIME", "ZONE"):
			p.advance()
			p.advance()
			p.advance()
			zoned := "TIMESTAMPTZ"
			if isTime {
				zoned = "TIMETZ"
			}
			out := New("DataType", Arg{"this", DataTypeKind(zoned)})
			if params, ok := dt.Args["expressions"].([]*Expression); ok && len(params) > 0 {
				out.Set("expressions", params)
			}
			return out, nil
		case p.atWords("WITH", "LOCAL", "TIME", "ZONE"):
			p.advance()
			p.advance()
			p.advance()
			p.advance()
			out := New("DataType", Arg{"this", DataTypeKind("TIMESTAMPLTZ")})
			if params, ok := dt.Args["expressions"].([]*Expression); ok && len(params) > 0 {
				out.Set("expressions", params)
			}
			return out, nil
		case p.atWords("WITHOUT", "TIME", "ZONE"):
			p.advance()
			p.advance()
			p.advance()
		}
	}
	dt.Set("nested", nested)
	return dt, nil
}

// parseIntervalType reads the unit after INTERVAL, if there is one.
func (p *parser) parseIntervalType() *Expression {
	unit := p.intervalUnit()
	if unit == nil {
		// No `nested` arg: the reference builds this one from a bare type name
		// and it carries none, where a type WRITTEN with parameters does.
		return New("DataType", Arg{"this", DataTypeKind("INTERVAL")})
	}
	// `DAY TO HOUR` is a span, which is one unit made of two -- where the
	// dialect reads spans at all (Parser.INTERVAL_SPANS).
	if p.tables.IntervalSpans && p.atWords("TO") {
		p.advance()
		if to := p.intervalUnit(); to != nil {
			unit = New("IntervalSpan", Arg{"this", unit}, Arg{"expression", to})
		}
	}
	return New("DataType", Arg{"this", New("Interval", Arg{"unit", unit})})
}

// intervalUnit reads one unit word, upper-cased as the reference records it,
// or nothing where the next word is not a unit this dialect knows.
func (p *parser) intervalUnit() *Expression {
	c := p.curr()
	if c == nil {
		return nil
	}
	word := strings.ToUpper(c.Text)
	if _, ok := p.tables.ValidIntervalUnits[word]; !ok {
		return nil
	}
	p.advance()
	return New("Var", Arg{"this", word})
}

// parseTypeSize reads one parameter of a plain type. A number is a Literal; a
// word -- VARCHAR(MAX) -- is a Var rather than the Column an expression would
// produce, which is why it is read here rather than by parseExpression.
func (p *parser) parseTypeSize() (*Expression, error) {
	c := p.curr()
	if c == nil {
		return nil, p.unsupported("type parameter")
	}
	switch {
	case c.Type == TokNUMBER:
		p.advance()
		lit := New("Literal", Arg{"this", c.Text}, Arg{"is_string", false})
		return p.typeSizeParam(lit), nil
	case c.Type == TokVAR, p.atIdentifier() && c.Type != TokIDENTIFIER:
		// A bare word, not a quoted one: `VARCHAR(MAX)` is a Var, and the
		// reference UPPER-CASES it, so `varchar(max)` and `VARCHAR(MAX)` are
		// the same node. A quoted `"max"` is not the same thing and is refused.
		p.advance()
		v := New("Var", Arg{"this", strings.ToUpper(c.Text)})
		return New("DataTypeParam", Arg{"this", v}), nil
	case c.Type == TokSTRING:
		// A string parameter: `DateTime64(6, 'UTC')` names a time zone where
		// every other size is a number, and the reference keeps it literal
		// (Parser._parse_type_size).
		p.advance()
		return New("DataTypeParam", Arg{"this",
			New("Literal", Arg{"this", c.Text}, Arg{"is_string", true})}), nil
	case c.Type == TokL_PAREN:
		// `VARCHAR((50))`: the size is itself parenthesised, and the
		// reference keeps the parentheses rather than folding them away.
		p.advance()
		inner, err := p.parseTypeSize()
		if err != nil {
			return nil, err
		}
		if !p.match(TokR_PAREN) {
			return nil, p.unsupported("unclosed type parameter")
		}
		return New("DataTypeParam", Arg{"this", New("Paren", Arg{"this", inner.Args["this"]})}), nil
	}
	return nil, p.unsupported("non-numeric type parameter")
}

// parseClickhouseAnonymousFunc reads a call as the ANONYMOUS function it is,
// and lets ClickHouse's aggregate resolver name it -- `any(x)` becomes an
// AnonymousAggFunc rather than the AnyValue it would otherwise be
// (ClickHouseParser._parse_function, anonymous=True).
func (p *parser) parseClickhouseAnonymousFunc() (*Expression, error) {
	name := p.curr().Text
	p.advance() // the name
	p.advance() // the opening parenthesis
	args, err := p.parseCallArgsCSV()
	if err != nil {
		return nil, err
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed " + name)
	}
	return p.clickhouseAggFunc(New("Anonymous", Arg{"this", name}, Arg{"expressions", args})), nil
}

// parseClickhouseJSONTypeArg reads one argument of ClickHouse's JSON type: a
// SKIP clause, a `name=value` parameter, or the name and type of a column
// (Parser._parse_json_type_arg).
func (p *parser) parseClickhouseJSONTypeArg() (*Expression, error) {
	if p.atWords("SKIP") {
		p.advance()
		regexp := p.match(TokRLIKE)
		col, err := p.parseColumn()
		if err != nil {
			return nil, err
		}
		if col != nil && col.Class == "Column" {
			col = col.This()
		}
		return New("SkipJSONColumn", Arg{"regexp", regexp}, Arg{"expression", col}), nil
	}
	col, err := p.parseColumn()
	if err != nil {
		return nil, err
	}
	name := col
	if col != nil && col.Class == "Column" {
		name = col.This()
	}
	if p.match(TokEQ) {
		value, err := p.parsePrimary()
		if err != nil {
			return nil, err
		}
		return New("EQ", Arg{"this", New("Var", Arg{"this", name.Name()})},
			Arg{"expression", value}), nil
	}
	kind, err := p.parseDataType()
	if err != nil {
		return nil, err
	}
	return New("ColumnDef", Arg{"this", name}, Arg{"kind", kind}), nil
}

// parseStructField reads one member of a STRUCT-like type. The colon is
// optional: Databricks writes `a: INT` and DuckDB `a INT`, and both arrive as
// the same ColumnDef -- but a member need not be NAMED at all: `STRUCT<INT,
// DOUBLE>` lists two bare types, the same shape a nested (non-struct) type's
// own member list takes. Tried first, at a mark: reading the member as a
// whole TYPE and finding nothing after it -- the member has ended -- means it
// was never a name to begin with, and the read stands; anything else and the
// attempt is undone, because a name is still what most members are.
func (p *parser) parseStructField() (*Expression, error) {
	mark := p.index
	if dt, err := p.parseDataType(); err == nil {
		if c := p.curr(); c == nil || c.Type == TokCOMMA || c.Type == TokGT || c.Type == TokR_PAREN {
			return dt, nil
		}
	}
	p.index = mark
	name, err := p.parseIdentifier()
	if err != nil {
		// A member may be NAMED by a keyword -- `Tuple(select String)` -- and
		// the reference reads the name with `_parse_id_var`
		// (Parser._parse_struct_types).
		// A member may be NAMED by a token that is a keyword but not RESERVED
		// -- `Tuple(select String)` in ClickHouse -- and the reference reads
		// the name through `_parse_type(fallback_to_identifier=True)`
		// (Parser._parse_struct_types).
		if id := p.advanceAnyIdentifier(); id != nil {
			name = id
		} else {
			return nil, err
		}
	}
	p.match(TokCOLON)
	kind, err := p.parseDataType()
	if err != nil {
		return nil, err
	}
	def := New("ColumnDef", Arg{"this", name}, Arg{"kind", kind})
	// A field of a struct is a COLUMN definition, so it may say the same
	// things about itself a column may -- `STRUCT<a: DOUBLE COMMENT 'aaa'>`.
	constraints, err := p.parseColumnConstraints()
	if err != nil {
		return nil, err
	}
	if len(constraints) > 0 {
		def.Set("constraints", constraints)
	}
	return def, nil
}

// parseArraySuffix wraps a type in as many ARRAY layers as it has bracket
// pairs, and in one more where the word ARRAY follows it.
//
// `INT[3]` is a fixed-size array only where the dialect has them -- unless the
// type is a COLUMN's, where every dialect takes the size. Where neither
// holds, the reference RETREATS and reads the brackets as a subscript of the
// cast, so the loop stops without consuming them rather than building an
// array the reference never builds.
func (p *parser) parseArraySuffix(dt *Expression) (*Expression, error) {
	for {
		// `integer ARRAY` is `integer[]` written the long way, and
		// `integer ARRAY[3]` is `integer[3]` -- one layer with a size, not
		// a layer for the word and another for the brackets.
		arrayWord := false
		if p.atWords("ARRAY") {
			p.advance()
			arrayWord = true
		}
		if !p.at(TokL_BRACKET) {
			if arrayWord {
				dt = arrayOf(dt, nil)
				continue
			}
			return dt, nil
		}

		var values []*Expression
		if p.atPair(TokL_BRACKET, TokR_BRACKET) {
			p.advance()
			p.advance()
		} else {
			if !p.tables.SupportsFixedSizeArrays && !p.inColumnType && !arrayWord {
				return dt, nil
			}
			size := p.next()
			if size == nil || size.Type != TokNUMBER {
				if arrayWord {
					return nil, p.unsupported("an array size that is not a number")
				}
				return dt, nil
			}
			p.advance()
			p.advance()
			if !p.match(TokR_BRACKET) {
				return nil, p.unsupported("unclosed array size")
			}
			values = []*Expression{
				New("Literal", Arg{"this", size.Text}, Arg{"is_string", false}),
			}
		}
		dt = arrayOf(dt, values)
	}
}

// arrayOf wraps a type in one ARRAY layer, with a fixed size when it has one.
func arrayOf(dt *Expression, values []*Expression) *Expression {
	args := []Arg{
		{"this", DataTypeKind("ARRAY")},
		{"expressions", []*Expression{dt}},
	}
	if values != nil {
		args = append(args, Arg{"values", values})
	}
	args = append(args, Arg{"nested", true})
	return New("DataType", args...)
}

// parseScalarSubquery reads a parenthesised query used where a value goes.
// It keeps a `pivots` slot the FROM-clause form does not, which is the
// reference's shape rather than a simplification of it.
func (p *parser) parseScalarSubquery() (*Expression, error) {
	p.advance() // the opening parenthesis
	if p.at(TokVALUES) {
		// A derived VALUES table: the reference wraps the Values ITSELF, with
		// no Select over it (Parser._parse_derived_table_values).
		values, err := p.parseValuesTable()
		if err != nil {
			return nil, err
		}
		if !p.match(TokR_PAREN) {
			return nil, p.unsupported("unclosed subquery")
		}
		return New("Subquery", Arg{"this", values}, Arg{"pivots", nil},
			Arg{"alias", nil}, Arg{"sample", nil}), nil
	}
	inner, err := p.parseQuery()
	if err != nil {
		return nil, err
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed subquery")
	}
	return New("Subquery", Arg{"this", inner}, Arg{"pivots", nil},
		Arg{"alias", nil}, Arg{"sample", nil}), nil
}

// parseCase reads a CASE expression, in both its forms: with a subject before
// the first WHEN, and without.
func (p *parser) parseCase() (*Expression, error) {
	p.advance() // CASE

	var subject *Expression
	if !p.at(TokWHEN) {
		var err error
		subject, err = p.parseExpression()
		if err != nil {
			return nil, err
		}
	}

	var ifs []*Expression
	for p.match(TokWHEN) {
		cond, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		if !p.match(TokTHEN) {
			return nil, p.unsupported("WHEN without THEN")
		}
		then, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		ifs = append(ifs, New("If", Arg{"this", cond}, Arg{"true", then}))
	}
	if len(ifs) == 0 {
		return nil, p.unsupported("CASE without WHEN")
	}

	var deflt *Expression
	if p.match(TokELSE) {
		var err error
		deflt, err = p.parseExpression()
		if err != nil {
			return nil, err
		}
	}
	if !p.match(TokEND) {
		return nil, p.unsupported("CASE without END")
	}
	return New("Case", Arg{"this", subject}, Arg{"ifs", ifs}, Arg{"default", deflt}), nil
}

// parseFunction reads a call with an argument list.
//
// Only the Anonymous form is built. The reference gives hundreds of names a
// node class of their own -- COUNT is a Count with a big_int flag, not a call
// named "COUNT" -- and each has its own argument shape. Producing Anonymous for
// one of those would be a divergence, so a name the reference knows is refused
// until its node is ported. The list is generated, not guessed.
// clickhouseFunctionParser reads the calls whose grammar ClickHouse gives its
// own parser (ClickHouseParser.FUNCTION_PARSERS). On entry the cursor is on the
// name; a handled call consumes the name, the parentheses and the arguments
// and returns its node.
func (p *parser) clickhouseFunctionParser(upper string) (bool, *Expression, error) {
	switch upper {
	case "XOR", "AND", "OR":
		p.advance()
		p.advance() // the opening parenthesis
		args, err := p.parseCallArgsCSV()
		if err != nil {
			return true, nil, err
		}
		if !p.match(TokR_PAREN) {
			return true, nil, p.unsupported(upper + " without a closing parenthesis")
		}
		if upper == "XOR" {
			return true, combineConnector("Xor", args), nil
		}
		if len(args) == 0 {
			return true, nil, p.unsupported(upper + " with no arguments")
		}
		class := "And"
		if upper == "OR" {
			class = "Or"
		}
		return true, New("Paren", Arg{"this", combineConnector(class, args)}), nil
	case "QUANTILE", "MEDIAN":
		// `quantile(0.5)(a)` and `median(a)`: the first list is the quantile,
		// the second the argument, and MEDIAN is QUANTILE of one half
		// (ClickHouseParser._parse_quantile).
		p.advance()
		p.advance() // the opening parenthesis
		this, err := p.parseCallArgumentAliased(false)
		if err != nil {
			return true, nil, err
		}
		params, hasParams, err := p.parseClickhouseFuncParams()
		if err != nil {
			return true, nil, err
		}
		if !p.match(TokR_PAREN) {
			return true, nil, p.unsupported("unclosed " + upper)
		}
		if hasParams {
			return true, New("Quantile", Arg{"this", params[0]}, Arg{"quantile", this}), nil
		}
		return true, New("Quantile", Arg{"this", this},
			Arg{"quantile", New("Literal", Arg{"this", "0.5"}, Arg{"is_string", false})}), nil
	case "SPLITBYSTRING", "SPLITBYREGEXP":
		// `splitByString(sep, s)` and `splitByRegexp(re, s)`
		// (ClickHouseParser.FUNCTIONS, _build_split).
		p.advance()
		p.advance() // the opening parenthesis
		args, err := p.parseCallArgsCSV()
		if err != nil {
			return true, nil, err
		}
		if !p.match(TokR_PAREN) {
			return true, nil, p.unsupported("unclosed " + upper)
		}
		class := "Split"
		if upper == "SPLITBYREGEXP" {
			class = "RegexpSplit"
		}
		node := New(class)
		if a := argAt(args, 1); a != nil {
			node.Set("this", a)
		}
		if a := argAt(args, 0); a != nil {
			node.Set("expression", a)
		}
		if a := argAt(args, 2); a != nil {
			node.Set("limit", a)
		}
		return true, node, nil
	case "SPLITBYCHAR":
		// A ONE-byte separator splits with Split; anything else stays the
		// anonymous call (ClickHouseParser._build_split_by_char).
		p.advance()
		p.advance() // the opening parenthesis
		args, err := p.parseCallArgsCSV()
		if err != nil {
			return true, nil, err
		}
		if !p.match(TokR_PAREN) {
			return true, nil, p.unsupported("unclosed splitByChar")
		}
		if sep := argAt(args, 0); sep != nil && sep.Class == "Literal" && sep.Args["is_string"] == true {
			if text, _ := sep.Args["this"].(string); len(text) == 1 {
				node := New("Split")
				if a := argAt(args, 1); a != nil {
					node.Set("this", a)
				}
				node.Set("expression", sep)
				if a := argAt(args, 2); a != nil {
					node.Set("limit", a)
				}
				return true, node, nil
			}
		}
		return true, New("Anonymous", Arg{"this", "splitByChar"},
			Arg{"expressions", args}), nil
	case "GROUPCONCAT":
		// `groupConcat(sep)(expr)` and `groupConcat(expr)`
		// (ClickHouseParser._parse_group_concat).
		p.advance()
		p.advance() // the opening parenthesis
		args, err := p.parseCallArgsCSV()
		if err != nil {
			return true, nil, err
		}
		params, hasParams, err := p.parseClickhouseFuncParams()
		if err != nil {
			return true, nil, err
		}
		if !p.match(TokR_PAREN) {
			return true, nil, p.unsupported("unclosed groupConcat")
		}
		node := New("GroupConcat")
		this := argAt(args, 0)
		if hasParams {
			this = argAt(params, 0)
			// `groupConcat(sep, limit)(expr)` keeps the limit by wrapping the
			// expression in it (ClickHouseParser._parse_group_concat).
			if limit := argAt(args, 1); limit != nil && this != nil {
				this = New("Limit", Arg{"this", this}, Arg{"expression", limit})
			}
		}
		if this != nil {
			node.Set("this", this)
		}
		if sep := argAt(args, 0); hasParams && sep != nil {
			node.Set("separator", sep)
		}
		return true, node, nil
	case "COLUMNS":
		// `COLUMNS('regex') [APPLY(f)]*` selects columns by pattern and
		// transforms them (ClickHouseParser._parse_columns).
		p.advance()
		p.advance() // the opening parenthesis
		inner, err := p.parseCallArgumentAliased(false)
		if err != nil {
			return true, nil, err
		}
		node := New("Columns", Arg{"this", inner})
		for p.at(TokR_PAREN) && p.next() != nil && strings.EqualFold(p.next().Text, "APPLY") &&
			p.peekAt(2) != nil && p.peekAt(2).Type == TokL_PAREN {
			p.advance() // the closing parenthesis
			p.advance() // APPLY
			p.advance() // (
			name := p.parseIDVarAny()
			if name == nil {
				return true, nil, p.unsupported("APPLY without a name")
			}
			node = New("Apply", Arg{"this", node},
				Arg{"expression", New("Var", Arg{"this", name.Name()})})
		}
		if !p.match(TokR_PAREN) {
			return true, nil, p.unsupported("unclosed COLUMNS")
		}
		return true, node, nil
	case "TUPLE":
		// `tuple(a, b)` is a Struct over its arguments, which may name
		// themselves (ClickHouseParser.FUNCTION_PARSERS, Struct.from_arg_list).
		p.advance()
		p.advance() // the opening parenthesis
		args, err := p.parseCallArgsCSVAliased(true)
		if err != nil {
			return true, nil, err
		}
		if !p.match(TokR_PAREN) {
			return true, nil, p.unsupported("unclosed tuple")
		}
		return true, New("Struct", Arg{"expressions", args}), nil
	case "ARRAYJOIN":
		p.advance()
		p.advance() // the opening parenthesis
		inner, err := p.parseCallArgumentAliased(true)
		if err != nil {
			return true, nil, err
		}
		if !p.match(TokR_PAREN) {
			return true, nil, p.unsupported("unclosed arrayJoin")
		}
		return true, New("Explode", Arg{"this", inner}), nil
	case "VIEW":
		// `view(SELECT ...)`: a table function over a query, and the name is
		// recorded in lowercase (ClickHouseParser.FUNCTION_PARSERS).
		p.advance()
		p.advance() // the opening parenthesis
		query, err := p.parseSelect()
		if err != nil {
			return true, nil, err
		}
		if !p.match(TokR_PAREN) {
			return true, nil, p.unsupported("unclosed view")
		}
		return true, New("Anonymous", Arg{"this", "view"},
			Arg{"expressions", []*Expression{query}}), nil
	}
	return false, nil, nil
}

// parseClickhouseFuncParams reads the SECOND parenthesised list a ClickHouse
// aggregate may carry: `quantile(0.5)(a)` (ClickHouseParser._parse_func_params).
// The closing parenthesis is left for the caller, as the reference leaves it
// for _parse_function_call.
func (p *parser) parseClickhouseFuncParams() ([]*Expression, bool, error) {
	if !p.at(TokR_PAREN) || p.next() == nil || p.next().Type != TokL_PAREN {
		return nil, false, nil
	}
	p.advance() // the first list's closing parenthesis
	p.advance() // the second list's opening parenthesis
	args, err := p.parseCallArgsCSV()
	if err != nil {
		return nil, false, err
	}
	return args, true, nil
}

// parseCallArgsCSV reads a call's arguments as a comma-separated list, closed
// by the caller.
func (p *parser) parseCallArgsCSV() ([]*Expression, error) {
	return p.parseCallArgsCSVAliased(false)
}

// parseCallArgsCSVAliased is the same reader for a call whose arguments may
// name themselves.
func (p *parser) parseCallArgsCSVAliased(alias bool) ([]*Expression, error) {
	var args []*Expression
	if p.at(TokR_PAREN) {
		return args, nil
	}
	wasInCallArgs := p.inCallArgs
	p.inCallArgs = true
	defer func() { p.inCallArgs = wasInCallArgs }()
	for {
		arg, err := p.parseCallArgumentAliased(alias)
		if err != nil {
			return nil, err
		}
		args = append(args, arg)
		if !p.match(TokCOMMA) {
			break
		}
	}
	return args, nil
}

// combineConnector folds a connector over its arguments the way the
// reference's `_combine` does: left-nested, with every operand that is itself
// a connector wrapped in parentheses so precedence survives, the first among
// them included once there is more than one.
func combineConnector(class string, args []*Expression) *Expression {
	conds := make([]*Expression, 0, len(args))
	for _, a := range args {
		if a != nil {
			conds = append(conds, a)
		}
	}
	if len(conds) == 0 {
		return nil
	}
	this := conds[0]
	if len(conds) > 1 {
		this = wrapConnector(this)
	}
	for _, e := range conds[1:] {
		this = New(class, Arg{"this", this}, Arg{"expression", wrapConnector(e)})
	}
	return this
}

// wrapConnector brackets a connector where the reference's `_wrap` does.
func wrapConnector(e *Expression) *Expression {
	if e != nil && classIsA["Connector"][e.Class] {
		return New("Paren", Arg{"this", e})
	}
	return e
}

func (p *parser) parseFunction() (builtFn *Expression, err error) {
	name := p.curr().Text
	// A comment standing in front of the name belongs to the call built from
	// it -- and to the node this function RETURNS, since the wrapper a
	// dialect puts around the call is built afterwards from the same token
	// (Parser.expression, Parser._add_comments).
	var leading []string
	if c := p.curr(); c != nil && len(c.Comments) > 0 {
		leading = append([]string(nil), c.Comments...)
		c.Comments = nil
	}
	// A dialect that PRESERVES_ORIGINAL_NAMES records the name a call was
	// WRITTEN with, and writes that name back where the class's own writer is
	// the fallback -- `LOG(x)` is an Ln, and comes out LOG
	// (Parser._parse_function, Generator.function_fallback_sql).
	defer func() {
		if err != nil || builtFn == nil {
			return
		}
		if len(leading) > 0 && builtFn.Class != "Anonymous" {
			putComments(builtFn, leading)
		}
		if builtFn.Class == "Anonymous" {
			return
		}
		if _, ok := p.tables.ConsultsOriginalName[builtFn.Class]; !ok {
			return
		}
		builtFn.originalName = name
	}()
	quotedName := p.curr().Type == TokIDENTIFIER
	upper := strings.ToUpper(name)
	if p.dialect == "bigquery" && bigqueryOwnedParser(upper) && p.next() != nil && p.next().Type == TokL_PAREN {
		p.advance() // the name
		p.advance() // the opening parenthesis
		return p.parseBigQueryFunctionParser(upper)
	}
	if isHiveFamily(p.dialect) && upper == "TRANSFORM" && p.next() != nil && p.next().Type == TokL_PAREN {
		mark := p.index
		p.advance() // the name
		built, err := p.buildHiveTransform()
		if err != nil {
			return nil, err
		}
		if built != nil {
			return built, nil
		}
		p.index = mark // not a `... USING script` TRANSFORM; read it as a function
	}
	if (p.dialect == "spark2" || p.dialect == "spark") && sparkJoinHint(upper) {
		p.advance() // the name, as the reference does before a FUNCTION_PARSERS entry
		return p.parseJoinHintFunction(upper)
	}
	if upper == "CAST" || upper == "TRY_CAST" ||
		(p.dialect == "bigquery" && upper == "SAFE_CAST") {
		// BigQuery's SAFE_CAST is the same reader, flagged safe.
		return p.castOrConversion(upper == "TRY_CAST" || upper == "SAFE_CAST")
	}
	// ClickHouse gives a handful of names a grammar of their own, and the
	// words are OPERATORS elsewhere -- OR, AND, XOR -- so the syntax table the
	// probe recorded does not describe them (ClickHouseParser.FUNCTION_PARSERS).
	if p.dialect == "clickhouse" {
		if handled, node, err := p.clickhouseFunctionParser(upper); handled {
			return node, err
		}
	}
	if _, syntax := p.tables.SyntaxFunctions[upper]; syntax {
		return p.parseSyntaxFunction(upper)
	}
	// A name that turns its arguments into a JSON PATH. The generic probe
	// describes these from placeholder COLUMNS, where their builders take a
	// fallback shape real SQL never produces, so every one of them was
	// rejected outright -- which is the refusal this skips past.
	jsonPath, isJSONPath := p.tables.JSONPathFunctions[upper]
	spec, named := p.tables.Functions[upper]
	// A name whose SHAPE depends on how many arguments it is given -- DATEDIFF
	// of two is not DATEDIFF of three -- has one spec per count instead, and
	// which one applies cannot be known until the arguments are read.
	variants, byArity := p.tables.FunctionsByArity[upper]
	// A name whose class one argument's WORD chooses has no single spec and
	// no spec per count, only one per word -- which is not a builder nobody
	// can describe, so the refusal below does not apply to it.
	_, byWord := p.tables.ValueDispatchFunctions[upper]
	// Databricks (standing in for the Hive/Spark family it shares this
	// builder with) builds a bare MAP(...) through its own reference builder
	// rather than a probeable signature -- see buildVarMap -- so it is not
	// turned away here despite having none.
	isVarMap := upper == "MAP" && (p.dialect == "databricks" || p.dialect == "starrocks" ||
		p.dialect == "clickhouse" || isHiveFamily(p.dialect))
	// Dremio's DATETYPE has no generic fallback shape at all -- unlike
	// DATE_ADD/DATE_SUB's cast-interval builder, which still probes fine
	// through its own fallback_builder -- so it is not turned away here
	// either, the same exemption isVarMap gets.
	isDremioDateType := upper == "DATETYPE" && p.dialect == "dremio"
	// MySQL's own DATE_ADD/DATE_SUB builder raises outright when its second
	// argument is not an INTERVAL -- a placeholder column is not one, so the
	// probe that fills Functions from a placeholder call never got an
	// answer to record here either.
	isMySQLDateDelta := (upper == "DATE_ADD" || upper == "DATE_SUB") && p.dialect == "mysql"
	isPrestoToChar := upper == "TO_CHAR" && (p.dialect == "presto" || p.dialect == "trino")
	// SingleStore's own date/time functions have builders of their own that
	// the probe cannot record -- they are lambdas -- so they are built by
	// hand and must not be turned away here.
	isSingleStoreFn := p.dialect == "singlestore" && singlestoreOwnedFunction(upper)
	// The Hive family's own builders are hand-written too (the probe cannot
	// record the lambdas), so they must not be turned away here either.
	isHiveFn := isHiveFamily(p.dialect) && hiveOwnedFunction(upper)
	isSQLiteFn := p.dialect == "sqlite" && sqliteOwnedFunction(upper)
	isBigQueryFn := p.dialect == "bigquery" && bigqueryOwnedFunction(upper)
	if !named && !byArity && !isJSONPath && !byWord && !isVarMap && !isDremioDateType &&
		!isMySQLDateDelta && !isPrestoToChar && !isSingleStoreFn && !isHiveFn && !isSQLiteFn &&
		!isBigQueryFn {
		if _, custom := p.tables.NamedFunctions[upper]; custom {
			return nil, p.unsupported("function " + upper + " with a builder of its own")
		}
	}
	// A name the reference has no node for takes arguments that may name
	// themselves; one it does know does not -- except for the handful that
	// are listed as taking them anyway, which is how `STRUCT(1 AS a)` names
	// its fields.
	_, known := p.tables.NamedFunctions[upper]
	_, namesItsArgs := p.tables.FunctionsWithAliasedArgs[upper]
	aliasedArgs := !known || namesItsArgs
	// Spark's IF is not a name the reference knows, so each of its arguments
	// may name itself: `IF(cond, foo AS bar, bla AS baz)`.
	if isHiveFamily(p.dialect) && upper == "IF" {
		aliasedArgs = true
	}

	p.advance()
	p.advance() // the opening parenthesis

	var args []*Expression
	// `COUNT(DISTINCT a)` is a Count over a Distinct, not a Count of two
	// things: the reference collects everything after DISTINCT into one
	// Distinct node and passes that as the call's single argument. Refusing
	// it turned away one of the commonest aggregates a data agent writes.
	distinct := p.match(TokDISTINCT)
	var order, limitValue *Expression
	wasInCallArgs := p.inCallArgs
	p.inCallArgs = true
	if !p.at(TokR_PAREN) {
		for {
			// An ORDER BY with nothing in front of it IS the argument:
			// `RANK( ORDER BY foo)` passes the ordering itself, where
			// `ARRAY_AGG(x ORDER BY y)` orders the argument it follows.
			if p.at(TokORDER_BY) && len(args) == 0 {
				p.advance()
				o, oerr := p.parseOrder()
				if oerr != nil {
					return nil, oerr
				}
				args = append(args, o)
				if !p.match(TokCOMMA) {
					break
				}
				continue
			}
			// DISTINCT and named arguments inside a call also change the node
			// the reference builds; neither is handled here. ALL is also a
			// keyword usable as a bare lambda parameter -- `A(All -> ll)` --
			// which atLambda already recognises, so it must be checked before
			// this refuses ALL as a modifier: the generator writes a
			// single-parameter lambda over ALL without the parentheses the
			// original may have had, and reading it back hit this refusal.
			if p.atAny(TokDISTINCT, TokORDER_BY, TokALL) && !p.atLambda() {
				return nil, p.unsupported("modifier inside a function call")
			}
			// `x -> x > 1` is a lambda ONLY here, in argument position. The
			// same `->` between two ordinary expressions is JSON extraction,
			// and reading `data -> '$.value'` as a lambda made a Lambda out of
			// a JSON path.
			arg, err := p.parseCallArgumentAliased(aliasedArgs)
			if err != nil {
				return nil, err
			}
			args = append(args, arg)
			// `ARRAY_AGG(x ORDER BY y)`: the ORDER BY belongs to the
			// argument it follows, and the reference wraps that argument in
			// an Order rather than hanging the clause off the call. A nulls
			// modifier may stand between the two, and is read after the loop.
			if p.at(TokORDER_BY) {
				p.advance()
				o, oerr := p.parseOrder()
				if oerr != nil {
					return nil, oerr
				}
				order = o
			}
			if p.at(TokLIMIT) {
				p.advance()
				c := p.curr()
				if c == nil {
					return nil, p.unsupported("LIMIT without a value")
				}
				p.advance()
				limitValue = New("Literal", Arg{"this", c.Text}, Arg{"is_string", false})
			}
			if !p.match(TokCOMMA) {
				break
			}
		}
	}
	p.inCallArgs = wasInCallArgs
	// ClickHouse's AND, OR and XOR are connectors written as calls: AND and
	// OR wrap in the parentheses they were written with, XOR is a Xor node
	// (ClickHouseParser.FUNCTION_PARSERS, Parser._parse_connector_function).
	if p.dialect == "clickhouse" {
		switch upper {
		case "AND", "OR":
			if len(args) == 0 {
				return nil, p.unsupported(upper + " with no arguments")
			}
			class := "And"
			if upper == "OR" {
				class = "Or"
			}
			return New("Paren", Arg{"this", combineConnector(class, args)}), nil
		case "XOR":
			return combineConnector("Xor", args), nil
		}
	}
	// `SUM(x IGNORE NULLS)` puts the modifier INSIDE the parentheses and the
	// reference still wraps the whole call in it.
	inner := p.atNullsModifier()
	if inner != "" {
		p.advance()
		p.advance()
	}
	// `ANY_VALUE(x HAVING MAX y)` keeps the row whose y is greatest, and the
	// reference records it as a node of its own around the VALUE. It comes
	// BEFORE the ordering, which is where the reference reads it
	// (Parser._parse_function_call, _parse_having_max).
	var havingMaxColumn *Expression
	havingMaxMax := true
	if p.at(TokHAVING) && (p.nextWords("MAX") || p.nextWords("MIN")) {
		p.advance() // HAVING
		havingMaxMax = !p.atWords("MIN")
		p.advance() // MAX or MIN
		column, err := p.parseColumn()
		if err != nil {
			return nil, err
		}
		havingMaxColumn = column
	}
	// `ARRAY_AGG(x ORDER BY y)`: the ORDER BY belongs to the argument it
	// follows, and the reference wraps that argument in an Order rather than
	// hanging the clause off the call. It stands AFTER the nulls modifier and
	// the HAVING, which is where the reference reads it
	// (Parser._parse_function_call).
	if order == nil && p.at(TokORDER_BY) {
		p.advance()
		o, oerr := p.parseOrder()
		if oerr != nil {
			return nil, oerr
		}
		order = o
	}
	// BigQuery writes a LIMIT inside the call, after the ORDER BY, and the
	// reference wraps the Order in it.
	if limitValue == nil && p.at(TokLIMIT) {
		p.advance()
		c := p.curr()
		if c == nil {
			return nil, p.unsupported("LIMIT without a value")
		}
		p.advance()
		limitValue = New("Literal", Arg{"this", c.Text}, Arg{"is_string", false})
	}
	// The words may also stand AFTER the ordering -- `LAST_VALUE(x ORDER BY x
	// IGNORE NULLS)` -- where the reference reaches them through the ordered
	// expression it reads there.
	if inner == "" {
		if word := p.atNullsModifier(); word != "" {
			p.advance()
			p.advance()
			inner = word
		}
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed function argument list")
	}
	parenComments := p.takeComments()
	defer func() { putComments(builtFn, parenComments) }()
	// A named argument of one of those calls is a FIELD, not an alias: the
	// reference turns each key-value argument into a PropertyEQ before the
	// builder sees it, so `STRUCT(1 AS a)` and `{'a': 1}` produce the same
	// node from different syntax.
	if known && namesItsArgs {
		args = propertyEQArgs(args)
	}
	if distinct {
		args = []*Expression{New("Distinct", Arg{"expressions", args}, Arg{"on", nil})}
	}
	// The HAVING wraps the value, and the ordering wraps the HAVING: the
	// reference reads them in that order
	// (Parser._parse_order(_parse_having_max(...))).
	if havingMaxColumn != nil {
		if len(args) == 0 {
			return nil, p.unsupported("HAVING MAX without a value to keep")
		}
		args[len(args)-1] = New("HavingMax",
			Arg{"this", args[len(args)-1]},
			Arg{"expression", havingMaxColumn}, Arg{"max", havingMaxMax})
	}
	if order != nil {
		if len(args) == 0 {
			return nil, p.unsupported("ORDER BY without an argument to order")
		}
		order.Set("this", args[len(args)-1])
		args[len(args)-1] = order
	}
	if limitValue != nil {
		if len(args) == 0 {
			return nil, p.unsupported("LIMIT without a value to limit")
		}
		// The reference assigns `this` before `expression`, which is the order
		// the dump compares.
		args[len(args)-1] = New("Limit",
			Arg{"this", args[len(args)-1]}, Arg{"expression", limitValue})
	}
	// A builder that reads its arguments rather than only placing them. The
	// probe drives builders with placeholder columns and cannot see such a
	// decision, so the recorded signature describes a node the reference does
	// not actually build; these are written out in parse_builders.go.
	// Neither takes IGNORE/RESPECT NULLS inside its own parentheses -- that
	// is an aggregate's shape, and a data agent has no reason to write it
	// here -- so `inner`, read above for calls that do, goes unused by both.
	if upper == "DATEDIFF" || upper == "DATEDIFF_BIG" {
		// T-SQL's DATEDIFF (unit, start, end) swaps the dates, wraps each in
		// TimeStrToTime, and picks its shape by the FIRST date's literal
		// kind -- a decision the generic dispatch table below cannot
		// express. DuckDB and Redshift also build a class named "DateDiff"
		// (or, for Redshift, "TsOrDsDiff") for this name, but as a plain
		// reordering the generic path already handles correctly, so T-SQL's
		// own hand-written shape is the only one this special case is for.
		if p.dialect == "tsql" {
			return p.buildDateDiff(upper, args, upper == "DATEDIFF_BIG")
		}
	}
	if upper == "DATENAME" && len(p.tables.FullFormatTimeMapping) > 0 {
		return p.buildDateName(args)
	}
	if upper == "MOD" && len(args) == 2 {
		return p.buildMod(args), nil
	}
	if isHiveFamily(p.dialect) {
		if built := p.buildHiveFunction(upper, args); built != nil {
			return built, nil
		}
	}
	if p.dialect == "sqlite" {
		if built := p.buildSQLiteFunction(upper, args); built != nil {
			return built, nil
		}
	}
	if p.dialect == "bigquery" {
		if built := p.buildBigQueryFunction(upper, args); built != nil {
			return built, nil
		}
	}
	if p.dialect == "hive" && (upper == "DATE_ADD" || upper == "DATE_SUB") && len(args) == 2 {
		// Hive's DATE_ADD/DATE_SUB build a TsOrDsAdd whose unit is always DAY,
		// and DATE_SUB negates the count (HiveParser.FUNCTIONS).
		expression := args[1]
		if upper == "DATE_SUB" {
			// `expression * -1`, which parenthesises a non-atomic operand the
			// way the reference's own multiplication does.
			inner := expression
			if expression != nil && expression.Class != "Literal" && expression.Class != "Column" {
				inner = New("Paren", Arg{"this", expression})
			}
			expression = New("Mul",
				Arg{"this", inner},
				Arg{"expression", New("Neg", Arg{"this", New("Literal", Arg{"this", "1"}, Arg{"is_string", false})})})
		}
		return New("TsOrDsAdd",
			Arg{"this", args[0]}, Arg{"expression", expression},
			Arg{"unit", New("Var", Arg{"this", "DAY"})}), nil
	}
	if upper == "STR_TO_DATE" && (p.dialect == "mysql" || p.dialect == "doris" || p.dialect == "starrocks") && len(args) == 2 {
		return p.buildMySQLStrToDate(args), nil
	}
	// SingleStore's date and time functions are its own builders -- several
	// wrap their first argument in a CAST to TIME(6) so a bare time-of-day
	// string reads, and the formats are the MySQL ones
	// (SingleStoreParser.FUNCTIONS).
	if p.dialect == "singlestore" {
		if built := p.buildSingleStoreFunction(upper, args); built != nil {
			return built, nil
		}
	}
	if (upper == "DATE_ADD" || upper == "DATE_SUB") && p.dialect == "mysql" && len(args) == 2 {
		if built := buildMySQLDateDeltaWithInterval(upper, args); built != nil {
			return built, nil
		}
		return nil, p.unsupported(upper + " with a second argument this port does not read as an INTERVAL")
	}
	// Doris subclasses MySQL but overrides the date-delta builders with a
	// default unit of DAY -- a bare day count is legal, unlike MySQL's -- and
	// its DATE_TRUNC takes the unit and the datetime in either order. Both are
	// decided by the reference's own builders, not by the generic dispatch.
	if p.dialect == "doris" {
		switch upper {
		case "DATE_ADD", "DATE_SUB", "ADDDATE", "SUBDATE":
			class := map[string]string{
				"DATE_ADD": "DateAdd", "DATE_SUB": "DateSub",
				"ADDDATE": "DateAdd", "SUBDATE": "DateSub",
			}[upper]
			if built := buildDorisDateDeltaWithInterval(class, args); built != nil {
				return built, nil
			}
		case "DATE_TRUNC":
			return buildDorisDateTrunc(args), nil
		}
	}
	// StarRocks shares MySQL's family but writes its date functions its own
	// way: the delta builders default to DAY, DATE_TRUNC takes the unit
	// FIRST, and DATEDIFF/DATE_DIFF put the unit in different slots.
	if p.dialect == "starrocks" {
		switch upper {
		case "DATE_ADD", "DATE_SUB", "ADDDATE", "SUBDATE":
			class := map[string]string{
				"DATE_ADD": "DateAdd", "DATE_SUB": "DateSub",
				"ADDDATE": "DateAdd", "SUBDATE": "DateSub",
			}[upper]
			if built := buildDorisDateDeltaWithInterval(class, args); built != nil {
				return built, nil
			}
		case "DATE_TRUNC":
			return buildStarRocksTimestampTrunc(args), nil
		case "DATEDIFF":
			return buildStarRocksDateDiff(argAt(args, 0), argAt(args, 1),
				New("Literal", Arg{"this", "DAY"}, Arg{"is_string", true})), nil
		case "DATE_DIFF":
			return buildStarRocksDateDiff(argAt(args, 1), argAt(args, 2), argAt(args, 0)), nil
		}
	}
	// DATE_ADD/DATE_SUB over an INTERVAL argument are rewritten by these
	// dialects' own builders, which are not ported (the neutral dialect's
	// empty name stands for itself), so they are declined rather than built
	// the generic way and written wrongly.
	if (upper == "DATE_ADD" || upper == "DATE_SUB") && len(args) == 2 &&
		(p.dialect == "" || strings.Contains(" redshift materialize risingwave fabric ", " "+p.dialect+" ")) &&
		args[1] != nil && args[1].Class == "Interval" {
		return nil, p.unsupported(upper + " over an INTERVAL in this dialect")
	}
	if isPrestoToChar {
		// A TimeToStr needs its format: the reference rejects a bare
		// TO_CHAR(ts) here rather than build one without.
		if len(args) < 2 {
			return nil, p.unsupported("TO_CHAR without a format")
		}
		return buildPrestoToChar(args), nil
	}
	if p.dialect == "dremio" {
		switch {
		case upper == "TO_CHAR":
			return p.buildToCharOrTimeToStr(args)
		case upper == "CURRENT_DATE_UTC" && len(args) == 0:
			return buildDremioCurrentDateUTC(), nil
		case upper == "DATE_ADD" || upper == "DATE_SUB":
			if built := buildDremioDateDeltaWithCastInterval(map[string]string{
				"DATE_ADD": "DateAdd", "DATE_SUB": "DateSub",
			}[upper], args); built != nil {
				return built, nil
			}
		case upper == "DATETYPE":
			if built := buildDremioDateType(args); built != nil {
				return built, nil
			}
			// Not three arguments. DATETYPE has no generic fallback, so this
			// refuses rather than building Anonymous.
			return nil, p.unsupported("function DATETYPE with a non-integer argument")
		}
	}
	if isVarMap {
		return p.buildVarMap(args)
	}
	if upper == "FORMAT" && len(p.tables.FormatTimeMapping) > 0 {
		built, err := p.buildFormat(args)
		if err != nil {
			return nil, err
		}
		if inner != "" {
			return New(inner, Arg{"this", built}), nil
		}
		return built, nil
	}
	if isJSONPath {
		if node := p.buildJSONPathFunction(jsonPath, args); node != nil {
			return node, nil
		}
		return nil, p.unsupported("function " + upper + " over these arguments")
	}
	// A name whose CLASS depends on the TYPE of one argument. DuckDB's
	// DATE_TRUNC builds a DateTrunc over a DATE and a TimestampTrunc over
	// anything else -- two different shapes, and the choice is a question
	// only a type annotator can answer, which is why this was refused for as
	// long as there was no annotator.
	if d, ok := p.tables.TypeDispatchFunctions[upper]; ok && d.Index < len(args) {
		spec, named, byArity = dispatchByType(d, args[d.Index]), true, false
	}
	// And a name whose class depends on the WORD in one argument. T-SQL's
	// HASHBYTES('SHA1', x) is an SHA and HASHBYTES('MD5', x) an MD5; a digest
	// it does not know stays the plain call it was written as.
	if d, ok := p.tables.ValueDispatchFunctions[upper]; ok && d.Index < len(args) {
		spec, named, byArity = dispatchByValue(d, args[d.Index]), true, false
	}
	if !named && byArity {
		byCount, ok := variants[len(args)]
		if !ok {
			return nil, p.unsupported("function " + upper + " with this many arguments")
		}
		// One arity may be two shapes, told apart by what KIND of thing an
		// argument is: PostgreSQL reads REGEXP_REPLACE's last argument as
		// flags when it is a string and as a position when it is a number.
		if alt, found := kindSpec(p.tables.ArityKindSpecs[upper][len(args)], args); found {
			byCount = alt
		}
		spec, named = byCount, true
	}
	if named {
		// More arguments than the recorded signature consumes means the
		// reference's builder is doing something the probe could not see --
		// Databricks' FIRST(c, TRUE) wraps the call in IgnoreNulls, and the
		// builder only reveals that when argument 1 is literally TRUE. The
		// probe runs builders with placeholders, so it recorded a signature
		// that quietly DROPS the flag. Dropping an argument changes what the
		// statement means, so this is a refusal -- except for the four names
		// whose builder this is read from directly, where it is not a drop
		// but the documented behaviour.
		if n, bounded := spec.consumes(); bounded && len(args) > n {
			if wrapped, ok := ignoreNullsOnTrue(p.dialect, upper, args); ok {
				return wrapped, nil
			}
			// DATE_TRUNC's builder reads only a unit and a value; a third
			// argument -- PostgreSQL's own DATE_TRUNC takes a time zone
			// there -- is never even looked at, dropped the same way in
			// every dialect that reads this name. Silently losing a real
			// argument is ordinarily the refusal above, but here it is what
			// the reference itself does.
			if _, drops := dateTruncDropsExtra[upper]; drops {
				args = args[:n]
			} else {
				return nil, p.unsupported("extra arguments to " + name)
			}
		}
		// A wrap takes the argument's NAME, so an argument with no name -- a
		// cast, a subquery -- is one the reference does not name either: it
		// keeps the node instead, which is a different tree. Refused here
		// rather than built from an empty name.
		if !namedWhereWrapped(spec.Args, args) {
			// A wrap takes the argument's NAME, so an argument with no name
			// -- a cast, a subquery -- is one the reference does not name
			// either: it keeps the node. Where that is what the reference
			// does, the node is kept here too; where it is not, the call is
			// refused rather than built from an empty name.
			if _, keeps := p.tables.KeepsUnnamedWrapped[upper]; !keeps {
				return nil, p.unsupported("unnamed argument where " + upper + " wants a word")
			}
		}
		// A string literal in one of these slots makes the reference build
		// something else -- an Interval step, a `modifiers` argument that
		// shifts the rest. The recorded signature was probed with columns and
		// does not describe that, so the call is refused rather than filled in
		// with the argument in the wrong slot.
		// A time FORMAT is rewritten into the reference's spelling rather than
		// refused: T-SQL writes `yyyy-MM-dd` where the tree stores `%Y-%m-%d`.
		// This runs before the string-sensitivity check below, which is what
		// used to turn every one of these calls away.
		formatArgs := p.tables.TimeFormatArgs[upper]
		for _, i := range formatArgs {
			if i < len(args) && isStringLiteral(args[i]) {
				text, _ := args[i].Args["this"].(string)
				args[i] = New("Literal",
					Arg{"this", formatTime(text, p.tables.TimeMapping)},
					Arg{"is_string", true})
			}
		}
		// A position where the builder REWRITES a string rather than carrying
		// it. Two in the catalogue: T-SQL's DATETRUNC casts one to DATETIME2
		// and PostgreSQL's GENERATE_SERIES turns a step of `'1 day'` into an
		// INTERVAL. The port does the rewrite here, before building, which is
		// what lets the recorded signature explain the call.
		wraps := p.tables.StringArgWraps[upper]
		for i, how := range wraps {
			if i >= len(args) || !isStringLiteral(args[i]) {
				continue
			}
			wrapped, err := p.wrapStringArgument(args[i], how)
			if err != nil {
				return nil, err
			}
			args[i] = wrapped
		}
		for _, i := range p.tables.StringSensitiveArgs[strings.ToUpper(name)] {
			// Exasol's TRUNC READS a string argument rather than being
			// surprised by one: the string is what chooses a DateTrunc over a
			// Trunc, and the port writes that builder itself
			// (exasol_funcs.go). Refusing here would refuse the very case the
			// builder exists for.
			if p.dialect == "exasol" && upper == "TRUNC" {
				continue
			}
			if isTimeFormatArg(formatArgs, i) {
				continue
			}
			if _, rewritten := wraps[i]; rewritten {
				continue
			}
			if i < len(args) && isStringLiteral(args[i]) {
				return nil, p.unsupported("string argument to " + name)
			}
		}
		// The argument's own CLASS can change what is built: LOWER(HEX(x))
		// is a LowerHex, and UPPER(HEX(x)) simplifies straight to a bare
		// Hex, since HEX already writes uppercase. The spec was probed with
		// a placeholder column and describes only the plain Lower/Upper, so
		// this is read from the reference's own builder rather than guessed.
		for _, t := range p.tables.ClassSensitiveArgs[strings.ToUpper(name)] {
			if t.Index >= len(args) || args[t.Index] == nil {
				continue
			}
			for _, c := range t.Classes {
				if args[t.Index].Class != c {
					continue
				}
				if upper == "LOWER" || upper == "UPPER" {
					inner := childOf(args[t.Index], "this")
					if inner == nil {
						continue
					}
					if upper == "LOWER" {
						return New("LowerHex", Arg{"this", inner}), nil
					}
					return New("Hex", Arg{"this", inner}), nil
				}
				return nil, p.unsupported(c + " argument to " + name)
			}
		}
		built := p.buildFunction(upper, spec, args)
		if p.dialect == "bigquery" {
			built = normalizeBareWeek(upper, built)
			built = normalizeRegexpGroup(upper, built)
		}
		if inner != "" {
			return New(inner, Arg{"this", built}), nil
		}
		return built, nil
	}
	// A quoted name is an Identifier node in the reference and a bare string
	// otherwise; the two are different trees, and writing the string for both
	// lost the quoting on the way out.
	var this any = name
	if quotedName {
		this = New("Identifier", Arg{"this", name}, Arg{"quoted", true})
	}
	anon := New("Anonymous", Arg{"this", this}, Arg{"expressions", args})
	// ClickHouse splits an aggregate into a parameter list and an argument
	// list -- `quantile(0.5)(a)` -- and gives the node a class of its own
	// according to the name's combinators (ClickHouseParser._parse_function).
	if p.dialect == "clickhouse" {
		return p.clickhouseAggFunc(anon), nil
	}
	return anon, nil
}

// clickhouseAggFunc gives an anonymous call the aggregate class ClickHouse's
// own resolver calls for. A name that is not an aggregate is left as it was.
func (p *parser) clickhouseAggFunc(anon *Expression) *Expression {
	_, suffixes, ok := p.resolveClickhouseAgg(anon.Name())
	if !ok {
		return anon
	}
	params := p.parseClickhouseAggParams()
	name := anon.Args["this"]
	args := anon.Args["expressions"]
	switch {
	case len(suffixes) > 0 && params != nil:
		node := New("CombinedParameterizedAgg", Arg{"this", name}, Arg{"expressions", args})
		node.Set("params", params)
		return node
	case len(suffixes) > 0:
		return New("CombinedAggFunc", Arg{"this", name}, Arg{"expressions", args})
	case params != nil:
		node := New("ParameterizedAgg", Arg{"this", name}, Arg{"expressions", args})
		node.Set("params", params)
		return node
	}
	return New("AnonymousAggFunc", Arg{"this", name}, Arg{"expressions", args})
}

// parseClickhouseAggParams reads the SECOND parenthesised list of a
// ClickHouse aggregate, where there is one.
func (p *parser) parseClickhouseAggParams() []*Expression {
	if !p.match(TokL_PAREN) {
		return nil
	}
	var params []*Expression
	for {
		item, err := p.parseExpression()
		if err != nil || item == nil {
			break
		}
		params = append(params, item)
		if !p.match(TokCOMMA) {
			break
		}
	}
	p.match(TokR_PAREN)
	return params
}

// resolveClickhouseAgg strips combinators off an aggregate's name until what
// is left is one the dialect knows, LONGEST suffix first -- so `quantilesIf`
// resolves to `quantiles` with `If` rather than to `quantile` with `sIf`
// (ClickHouseParser._resolve_clickhouse_agg).
func (p *parser) resolveClickhouseAgg(name string) (string, []string, bool) {
	if len(p.tables.AggFunctions) == 0 {
		return "", nil, false
	}
	known := func(n string) bool {
		for _, f := range p.tables.AggFunctions {
			if f == n {
				return true
			}
		}
		return false
	}
	var acc []string
	for cur := name; ; {
		if known(cur) {
			return cur, acc, true
		}
		matched := ""
		for _, suf := range p.tables.AggFuncSuffixes {
			if len(cur) > len(suf) && strings.HasSuffix(cur, suf) {
				matched = suf
				break
			}
		}
		if matched == "" {
			return "", nil, false
		}
		acc = append([]string{matched}, acc...)
		cur = cur[:len(cur)-len(matched)]
	}
}

// buildFunction fills a function node's arguments from the call's, following
// the spec the reference's own builder produced: a positional argument, a
// variadic tail, or a constant the builder always sets -- COUNT is a Count
// flagged big_int whatever it was called with.
//
// A call with fewer arguments than keys leaves the rest unset, as the
// reference's zip does.
// isStringLiteral reports whether an argument is a quoted string, which is
// what StringSensitiveArgs is about.
func isStringLiteral(e *Expression) bool {
	if e == nil || e.Class != "Literal" {
		return false
	}
	b, _ := e.Args["is_string"].(bool)
	return b
}

// consumes reports how many positional arguments the spec reads, and whether
// that count is a bound at all -- a variadic tail swallows everything after it.
func (spec FuncSpec) consumes() (int, bool) {
	return consumedBy(spec.Args)
}

func consumedBy(keys []FuncArg) (int, bool) {
	n := 0
	for _, a := range keys {
		if a.Nested != "" {
			inner, bounded := consumedBy(a.NestedArgs)
			if !bounded {
				return 0, false
			}
			if inner > n {
				n = inner
			}
			continue
		}
		if a.VarLen {
			return 0, false
		}
		if a.Index >= n {
			n = a.Index + 1
		}
	}
	return n, true
}

// namedWhereWrapped reports whether every argument a wrapper would take the
// NAME of actually has one. It reaches into a nested node, where a wrapper can
// sit just as well as at the top.
func namedWhereWrapped(keys []FuncArg, args []*Expression) bool {
	for _, a := range keys {
		if a.Nested != "" {
			if !namedWhereWrapped(a.NestedArgs, args) {
				return false
			}
			continue
		}
		// Index -1 marks a CONSTANT node rather than a wrapper: it takes no
		// argument, so there is no name for it to want.
		if a.Wrap == "" || a.Index < 0 || a.Index >= len(args) {
			continue
		}
		if args[a.Index].Name() == "" {
			return false
		}
	}
	return true
}

// heldAlone is the one argument position a nested wrapper holds, where it
// holds exactly one. A wrapper over several has no single argument whose kind
// could excuse it.
func heldAlone(keys []FuncArg) (int, bool) {
	held := -1
	for _, a := range keys {
		if a.Index < 0 || a.VarLen || a.Nested != "" || a.Wrap != "" {
			continue
		}
		if held >= 0 {
			return 0, false
		}
		held = a.Index
	}
	return held, held >= 0
}

// argumentKind names an argument the way the probe named it, so the two agree
// about which arguments escape a wrapper. The distinctions are only the ones
// the reference's builders actually branch on.
func argumentKind(e *Expression) string {
	switch {
	case e == nil:
		return ""
	case e.Class == "Literal":
		if !isStringLiteral(e) {
			return "number"
		}
		// A string that spells a NUMBER is its own kind, because at least
		// one builder tells the two apart: PostgreSQL reads a trailing
		// string as REGEXP_REPLACE's flags unless it spells an integer.
		if text, _ := e.Args["this"].(string); isDigits(text) {
			return "digits"
		}
		return "string"
	case e.Class == "Cast" || e.Class == "TryCast":
		return "cast"
	case e.Class == "Subquery":
		return "subquery"
	}
	return "call"
}

// escapesWrapper reports whether this argument is one the wrapper skips.
//
// Two shapes of exception: a KIND -- T-SQL's LEN leaves a string alone -- and
// a cast the argument ALREADY carries, written `cast:TYPE`, because the
// reference will not cast twice.
func escapesWrapper(a FuncArg, arg *Expression) bool {
	kind := argumentKind(arg)
	for _, k := range a.NestedExcept {
		if k == kind {
			return true
		}
		// Digits are a REFINEMENT of string, so a wrapper a string escapes
		// is escaped by a string that spells a number too. Only the one
		// builder that tells them apart ever sees the difference.
		if k == "string" && kind == "digits" {
			return true
		}
		if target, found := strings.CutPrefix(k, "cast:"); found &&
			castsTo(arg) == target {
			return true
		}
	}
	return false
}

// isDigits reports whether every character is a decimal digit.
func isDigits(text string) bool {
	if text == "" {
		return false
	}
	for _, r := range text {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// castsTo is the type a cast names, or the empty string for anything else.
func castsTo(e *Expression) string {
	if e == nil || (e.Class != "Cast" && e.Class != "TryCast") {
		return ""
	}
	to, _ := e.Args["to"].(*Expression)
	if to == nil {
		return ""
	}
	kind, _ := to.Args["this"].(DataTypeKind)
	return string(kind)
}

// kindSpec picks the shape whose argument kind this call matches, if any.
func kindSpec(forms []KindSpec, args []*Expression) (FuncSpec, bool) {
	for _, form := range forms {
		if form.Index < len(args) && argumentKind(args[form.Index]) == form.Kind {
			return form.Spec, true
		}
	}
	return FuncSpec{}, false
}

// dispatchByValue picks the spec the word in this argument selects, or the
// default where the argument is not a word this name knows.
func dispatchByValue(d ValueDispatch, arg *Expression) FuncSpec {
	if !isStringLiteral(arg) {
		return d.Default
	}
	text, _ := arg.Args["this"].(string)
	if spec, ok := d.ByValue[strings.ToUpper(text)]; ok {
		return spec
	}
	return d.Default
}

// ignoreNullsOnTrueClasses names the four Hive-family builders that read
// their own second argument rather than declaring it: `build_with_ignore_nulls`
// takes the first argument as `this` and, only where the second is literally
// TRUE, wraps the call in IgnoreNulls -- any other second argument is simply
// never stored anywhere. Hive, Spark and Databricks share the one builder;
// this port speaks only Databricks of the three.
// dateTruncDropsExtra names the DATE_TRUNC-family builders that only ever
// read a unit and a value: `date_trunc_to_time` takes `seq_get(args, 0)` and
// `seq_get(args, 1)` and nothing past them, in every dialect that names it
// this way -- so PostgreSQL's own three-argument DATE_TRUNC, with a time
// zone third, loses that argument the same way here as it does there.
var dateTruncDropsExtra = map[string]struct{}{
	"DATE_TRUNC": {},
	"DATETRUNC":  {},
}

var ignoreNullsOnTrueClasses = map[string]string{
	"FIRST":       "First",
	"LAST":        "Last",
	"FIRST_VALUE": "FirstValue",
	"LAST_VALUE":  "LastValue",
}

// ignoreNullsOnTrue reproduces build_with_ignore_nulls, gated on both the
// dialect and the name so the probe's own signature for these same names in
// every OTHER dialect -- where the second argument is an ordinary one, not
// this flag -- is left alone.
//
// A second argument that is NOT literally TRUE is still a refusal, not a
// silent drop: the reference's own arg-count check runs against whatever the
// builder returns, and only the WRAPPED shape is exempt from it -- IgnoreNulls
// is not itself a Func, so the check that catches every other extra argument
// never sees it. `FIRST_VALUE(c, FALSE)` hits that check nonetheless and
// raises there, same as it does without this builder at all.
func ignoreNullsOnTrue(dialect, upper string, args []*Expression) (*Expression, bool) {
	if dialect != "databricks" || len(args) != 2 || !isBooleanLiteral(args[1], true) {
		return nil, false
	}
	class, ok := ignoreNullsOnTrueClasses[upper]
	if !ok {
		return nil, false
	}
	call := New(class, Arg{"this", args[0]})
	return New("IgnoreNulls", Arg{"this", call}), true
}

func (p *parser) buildFunction(name string, spec FuncSpec, args []*Expression) *Expression {
	// NAMED_STRUCT is registered only so this call is reached. Its pairs are
	// a Struct, which the spec's own argument list cannot describe.
	if name == "NAMED_STRUCT" {
		return namedStruct(args)
	}
	// Builders the probe cannot describe, because they are LAMBDAS in the
	// reference rather than a mapping from arguments to a node
	// (ExasolParser.FUNCTIONS). EVERY is an ALL over whatever it is given,
	// and DATE_TRUNC takes its unit FIRST -- the opposite way round from the
	// ordinary two-argument call.
	if p.dialect == "exasol" {
		switch name {
		case "EVERY":
			return New("All", Arg{"this", argAt(args, 0)})
		case "TO_CHAR":
			return p.toCharOrTimeToStr(args)
		case "TRUNC":
			return p.truncOrDateTrunc(args)
		}
	}
	node := p.buildFromSpec(name, spec.Class, spec.Args, args)
	if spec.Annot != nil {
		// The builder's OWN node carries an annotation: PostgreSQL's DIV is
		// a Cast over an IntDiv, and a cast records its target twice.
		node.Type = annotation(spec.Annot)
	}
	return node
}

// annotation builds the type a spec says its node was annotated with.
func annotation(a *FuncArg) *Expression {
	args := make([]Arg, 0, len(a.WrapArgs))
	for _, extra := range a.WrapArgs {
		args = append(args, Arg(extra))
	}
	return New(a.Wrap, args...)
}

func (p *parser) buildFromSpec(name, class string, keys []FuncArg, args []*Expression) *Expression {
	node := New(class)
	for _, a := range keys {
		switch {
		case a.Nested != "":
			// A node built AROUND the arguments: DuckDB's ANY_VALUE is an
			// IgnoreNulls over an AnyValue, and the arguments belong to the
			// inner node, not this one.
			//
			// Some wrappers have an EXCEPTION -- T-SQL's LEN casts what it
			// counts to TEXT unless it is already a string -- and the
			// argument then takes the slot bare.
			if held, ok := heldAlone(a.NestedArgs); ok && held < len(args) &&
				escapesWrapper(a, args[held]) {
				node.Set(a.Key, args[held])
				continue
			}
			made := p.buildFromSpec(name, a.Nested, a.NestedArgs, args)
			if a.NestedAnnot != nil {
				// The builder ANNOTATES what it built as well as building
				// it, and the reference dumps the annotation.
				made.Type = annotation(a.NestedAnnot)
			}
			node.Set(a.Key, made)
		case a.Wrap != "" && a.Index < 0:
			// A constant node the builder always supplies, holding no
			// argument: DuckDB's two-argument REGEXP_EXTRACT_ALL fills
			// group with Literal('0'). It is not a scalar const -- the
			// value is a node -- and not a wrapper either, since there is
			// no argument inside it to wrap.
			wrapArgs := make([]Arg, 0, len(a.WrapArgs))
			for _, extra := range a.WrapArgs {
				wrapArgs = append(wrapArgs, Arg(extra))
			}
			node.Set(a.Key, New(a.Wrap, wrapArgs...))
		case a.Wrap != "":
			// Built FROM the argument, not holding it: DATEADD's unit is
			// Var(args[i].name upper-cased), and the argument node itself does
			// not appear in the result at all.
			if a.Index < len(args) {
				// The slot becomes a Var only where the argument IS one of
				// the things a unit can be written as -- a Var, a Literal, or
				// a one-part Column. A call or a subquery stays the node it is,
				// which is the reference's own rule (TimeUnit.__init__).
				if !wrappableUnitArg(args[a.Index]) {
					node.Set(a.Key, args[a.Index])
					continue
				}
				// An argument with no NAME to take is kept as the node it is,
				// where that is what the reference does: PostgreSQL's
				// DATE_BIN puts a subquery straight into its unit slot.
				if args[a.Index].Name() == "" {
					if _, keeps := p.tables.KeepsUnnamedWrapped[strings.ToUpper(name)]; keeps {
						node.Set(a.Key, args[a.Index])
						continue
					}
				}
				word := strings.ToUpper(args[a.Index].Name())
				// A unit spelling the name normalises: T-SQL records
				// DATEADD(qq, ...) as QUARTER, not QQ.
				if aliases, ok := p.tables.UnitAliases[name]; ok {
					if full, ok := aliases[word]; ok {
						word = full
					}
				}
				wrapArgs := []Arg{{"this", word}}
				for _, extra := range a.WrapArgs {
					wrapArgs = append(wrapArgs, Arg(extra))
				}
				node.Set(a.Key, New(a.Wrap, wrapArgs...))
			} else {
				node.Set(a.Key, nil)
			}
		case a.VarLen:
			rest := []*Expression{}
			if a.Index < len(args) {
				rest = args[a.Index:]
			}
			node.Set(a.Key, rest)
		case a.Index >= 0:
			if a.Index >= len(args) {
				node.Set(a.Key, nil)
				break
			}
			held := args[a.Index]
			if a.NestedAnnot != nil {
				// The builder ANNOTATES the argument it was handed --
				// PostgreSQL marks REGEXP_REPLACE's flags a VARCHAR. Copied
				// first: the argument is the caller's, not this node's.
				held = held.Copy()
				held.Type = annotation(a.NestedAnnot)
			}
			node.Set(a.Key, held)
		default:
			node.Set(a.Key, a.Const)
		}
	}
	return node
}

// wrappableUnitArg reports whether an argument put into a unit slot is one
// the reference turns into a Var. Its rule is exact: a Var, a Literal, or a
// Column of ONE part; anything else -- a call, a subquery, a qualified column
// -- is kept as it stands (TimeUnit.__init__, TimeUnit.VAR_LIKE).
func wrappableUnitArg(e *Expression) bool {
	switch e.Class {
	case "Var", "Literal":
		return true
	case "Column":
		return e.Args["table"] == nil && e.Args["db"] == nil && e.Args["catalog"] == nil
	}
	return false
}

// parseClickhouseQueryParameter reads `{name: Type}` -- a query parameter --
// where a map literal opens with the same brace. handled is false, and nothing
// is consumed, when the braces are not one
// (ClickHouseParser._parse_query_parameter).
func (p *parser) parseClickhouseQueryParameter() (*Expression, bool) {
	mark := p.index
	if !p.match(TokL_BRACE) {
		return nil, false
	}
	var this *Expression
	if p.atIdentifier() {
		this = p.parseIDVarAny()
	}
	if this == nil || !p.match(TokCOLON) {
		p.index = mark
		return nil, false
	}
	// `IDENTIFIER` is read before the type reader, which would otherwise take
	// it for a user-defined type (ClickHouseParser._parse_query_parameter).
	var kind *Expression
	kindStr := ""
	if p.atWords("IDENTIFIER") {
		p.advance()
		kindStr = "Identifier"
	} else if dt, err := p.parseDataType(); err == nil && dt != nil {
		kind = dt
	} else {
		p.index = mark
		return nil, false
	}
	if !p.match(TokR_BRACE) {
		p.index = mark
		return nil, false
	}
	// An unquoted name becomes a Var, as the reference unwraps it.
	if this.Class == "Identifier" && this.Args["quoted"] != true {
		this = New("Var", Arg{"this", this.Name()})
	}
	var kindArg any = kind
	if kindStr != "" {
		kindArg = kindStr
	}
	return New("Placeholder", Arg{"this", this}, Arg{"kind", kindArg}), true
}

// clickhouseVarMap turns the items of a `{'k': v}` brace into ClickHouse's
// VarMap: the keys become a string-literal array and the values a parallel
// one. A member that is not a key-value pair ends the run, as it does in the
// reference, and what was read so far is still returned.
func clickhouseVarMap(items []*Expression) *Expression {
	keys := New("Array")
	values := New("Array")
	for _, item := range items {
		if item.Class != "PropertyEQ" {
			break
		}
		key, _ := item.Args["this"].(*Expression)
		value, _ := item.Args["expression"].(*Expression)
		keys.Append("expressions",
			New("Literal", Arg{"this", key.Name()}, Arg{"is_string", true}))
		values.Append("expressions", value)
	}
	return New("VarMap", Arg{"keys", keys}, Arg{"values", values})
}

// parseColumn reads a possibly-qualified column reference: name, table.name,
// db.table.name, catalog.db.table.name, and the `t.*` form.

// propertyEQArgs turns each key-value argument of a call that names its
// arguments into the PropertyEQ the reference builds. Four shapes reach it --
// `a AS 1`, `a = 1`, `a: 1` and one already converted -- and they differ only
// in where the key sits; a plain argument is left alone.
func propertyEQArgs(args []*Expression) []*Expression {
	out := make([]*Expression, 0, len(args))
	for _, a := range args {
		var key, value *Expression
		switch a.Class {
		case "Alias":
			key, _ = a.Args["alias"].(*Expression)
			value, _ = a.Args["this"].(*Expression)
		case "PropertyEQ", "EQ", "Slice":
			this, _ := a.Args["this"].(*Expression)
			value, _ = a.Args["expression"].(*Expression)
			key = this
			if a.Class != "PropertyEQ" && this != nil {
				key = New("Identifier", Arg{"this", this.Name()}, Arg{"quoted", false})
			}
		default:
			out = append(out, a)
			continue
		}
		// A key written as a bare word arrives as a Column wrapping the
		// Identifier. The reference unwraps it in place.
		if key != nil && key.Class == "Column" {
			if inner, ok := key.Args["this"].(*Expression); ok {
				key = inner
			}
		}
		out = append(out, New("PropertyEQ", Arg{"this", key}, Arg{"expression", value}))
	}
	return out
}

func (p *parser) parseColumn() (*Expression, error) {
	first, err := p.parseIdentifier()
	if err != nil {
		return nil, err
	}

	parts := []*Expression{first}
	star := false
	for p.match(TokDOT) {
		if p.match(TokSTAR) {
			star = true
			break
		}
		// After a dot, a NUMBER names a FIELD rather than a value: BigQuery
		// reads `data.144A_FLAG` as a field called 144A_FLAG, quoted
		// (BigQueryParser.SUPPORTS_DIGIT_PREFIXED_FIELD_NAMES,
		// Parser._parse_field).
		if p.dialect == "bigquery" {
			if c := p.curr(); c != nil && c.Type == TokNUMBER {
				p.advance()
				name := c.Text
				// The rest of the name is a WORD written with no space after
				// the digits; a closing bracket is not part of it
				// (Parser._parse_field, _advance_any).
				if p.connectedTo(p.tokens[p.index-1]) {
					if n := p.curr(); n != nil && (n.Type == TokVAR || n.Type == TokIDENTIFIER) {
						name += n.Text
						p.advance()
					}
				}
				parts = append(parts, New("Identifier", Arg{"this", name}, Arg{"quoted", true}))
				continue
			}
		}
		// After a dot, a NUMBER names a FIELD rather than a value: `t.2` reads
		// a column whose name is the literal 2 (Parser._parse_field, which
		// tries _parse_number).
		if c := p.curr(); c != nil && c.Type == TokNUMBER {
			p.advance()
			parts = append(parts, New("Literal",
				Arg{"this", c.Text}, Arg{"is_string", false}))
			continue
		}
		// After a dot, `null` and `true` are names, not values -- and the
		// reference builds a bare Identifier for them, with no `quoted` arg
		// at all rather than one set false. A parenthesis after the word
		// makes it a CALL instead: `assert.true(1 = 1)` is a qualified call
		// to a function called `true` (Parser._parse_column_ops).
		if c := p.curr(); c != nil && (c.Type == TokNULL || c.Type == TokTRUE || c.Type == TokFALSE) {
			if n := p.next(); n != nil && n.Type == TokL_PAREN {
				fn, err := p.parseQualifiedName()
				if err != nil {
					return nil, err
				}
				chain := parts[0]
				for _, part := range parts[1:] {
					chain = New("Dot", Arg{"this", chain}, Arg{"expression", part})
				}
				return New("Dot", Arg{"this", chain}, Arg{"expression", fn}), nil
			}
			p.advance()
			parts = append(parts, p.keep(New("Identifier", Arg{"this", c.Text})))
			continue
		}
		// `a.b.C()` is a CALL under a chain of dots, not a column: the
		// reference builds Dot(Dot(a, b), C()). Detected before the name is
		// consumed, so the call parser sees it where it expects to.
		if p.namesAFunctionCall() {
			// BigQuery's NET.* and SAFE.* calls are nodes of their own, and the
			// reference RE-PARSES the call through the ordinary function
			// reader for them (and for AI.*/ML.*) so a known name gets its
			// builder rather than an Anonymous (BigQueryParser._parse_column_ops).
			head := parts[len(parts)-1]
			prefix := strings.ToUpper(head.Name())
			if p.dialect == "bigquery" &&
				(prefix == "NET" || prefix == "SAFE" || prefix == "AI" || prefix == "ML") {
				fn, err := p.parseFunction()
				if err != nil {
					return nil, err
				}
				switch prefix {
				case "NET":
					return New("NetFunc", Arg{"this", fn}), nil
				case "SAFE":
					return New("SafeFunc", Arg{"this", fn}), nil
				}
				return New("Dot", Arg{"this", head}, Arg{"expression", fn}), nil
			}
			fn, err := p.parseQualifiedName()
			if err != nil {
				return nil, err
			}
			chain := parts[0]
			for _, part := range parts[1:] {
				chain = New("Dot", Arg{"this", chain}, Arg{"expression", part})
			}
			return New("Dot", Arg{"this", chain}, Arg{"expression", fn}), nil
		}
		id, err := p.parseIdentifier()
		if err != nil {
			return nil, err
		}
		parts = append(parts, id)
	}

	if star {
		// `t.* EXCEPT (a)` carries the modifiers on the STAR inside the
		// column, the same ones a bare `*` takes.
		qualified, err := p.starModifiers(newStar())
		if err != nil {
			return nil, err
		}
		parts = append(parts, qualified)
	}

	// A column holds FOUR parts at most -- the name and its three qualifiers
	// -- and anything written past them hangs off it as a chain of Dots:
	// `a.b.c.d.e` is a Dot over a fully qualified column. The star counts as
	// one of the four, so `a.b.c.*` is a column and `a.b.c.d.*` is a Dot.
	held := len(parts)
	if held > 4 {
		held = 4
	}
	col := New("Column", Arg{"this", parts[held-1]})
	names := []string{"table", "db", "catalog"}
	for i := held - 2; i >= 0; i-- {
		col.Set(names[held-2-i], parts[i])
	}
	// A star column keeps all four slots whether or not they were written,
	// as the reference builds it. A name'd one keeps only what it was given.
	if star && held == len(parts) {
		for _, n := range names {
			if _, ok := col.Args[n]; !ok {
				col.Set(n, nil)
			}
		}
	}
	out := col
	for _, extra := range parts[held:] {
		out = New("Dot", Arg{"this", out}, Arg{"expression", extra})
	}
	liftAll(parts, out)
	// BigQuery splits a COLUMN whose part carries dots the way it splits a
	// table name: `` `db.t`.`c` `` names three parts, not two
	// (BigQueryParser._parse_column).
	if p.dialect == "bigquery" && out.Class == "Column" {
		out = bigqueryColumnParts(out)
	}
	// Exasol writes `LOCAL.cnt` where LOCAL names the PROJECTION rather than a
	// table, and its parser drops the qualifier as it reads the column -- the
	// generator puts it back (ExasolParser._parse_column,
	// ExasolGenerator.select_sql).
	if p.dialect == "exasol" && out.Class == "Column" {
		if table, _ := out.Args["table"].(*Expression); table != nil &&
			table.Class == "Identifier" &&
			!quotedName(table) && strings.EqualFold(table.Name(), "LOCAL") {
			out.Set("table", nil)
		}
	}
	return p.daxBracketColumn(out)
}

// quotedName reports whether an Identifier was written quoted.
func quotedName(e *Expression) bool {
	q, _ := e.Args["quoted"].(bool)
	return q
}

func (p *parser) parseIdentifier() (*Expression, error) { return p.parseIdentifierWhere(false) }

// parseIdentifierWhere reads a name, in a table's name or anywhere else.
func (p *parser) parseIdentifierWhere(namingATable bool) (*Expression, error) {
	// T-SQL writes a temporary table's name with a # in front of it, and a
	// GLOBAL one with two. The marks are not part of the name: the reference
	// takes them off and records a flag, and the writer puts them back.
	//
	// The tokenizer hands them over as HASH tokens of their own, so this is
	// where they are read -- and it is every identifier rather than only a
	// table's, because `SELECT #x` is a column with the same mark on it.
	temporary, global := false, false
	if p.dialect == "tsql" && p.at(TokHASH) {
		p.advance()
		temporary = true
		if p.at(TokHASH) {
			p.advance()
			global, temporary = true, false
		}
	}
	if !p.atIdentifierWhere(namingATable) {
		return nil, p.unsupported("identifier")
	}
	c := p.curr()
	p.advance()
	// The token's text, not its upper-cased keyword spelling: a keyword used
	// as a name keeps the case it was written in, and the tokenizer only
	// upper-cases the keywords it finds through the trie.
	name := New("Identifier", Arg{"this", c.Text}, Arg{"quoted", c.Type == TokIDENTIFIER})
	if temporary {
		name.Set("temporary", true)
	}
	if global {
		name.Set("global_", true)
	}
	return p.keep(name), nil
}

// namesItselfNotACall reports whether a word that USUALLY opens a no-paren
// function is, here, just a name.
//
// Two of the reference's own retreats, kept as two because they retreat for
// different reasons:
//
//   - A QUOTED name never opens anything. `SELECT "case"` is a column; the
//     reference keeps the token types that cannot name a call in a set of
//     their own, and it holds exactly the quoted identifier and the string.
//   - A name reached THROUGH a dot is the far half of a qualified name:
//     `t.next` is a column, not a call.
//
// A third is decided by what FOLLOWS the word, and is written out at the
// return below.
func (p *parser) namesItselfNotACall(c *Token) bool {
	if _, invalid := p.tables.InvalidFuncNameTokens[c.Type]; invalid {
		return true
	}
	if p.index > 0 && p.tokens[p.index-1].Type == TokDOT {
		return true
	}
	// A no-paren function takes its argument from what FOLLOWS it, and some
	// of these parsers put the word back when nothing there can be one: `IF`
	// wants a condition and `SELECT if` has none, so it is a column. Others
	// never retreat -- Databricks reads a bare CURDATE as CURRENT_DATE, with
	// no argument at all -- which is why the names are probed one at a time
	// rather than treated alike.
	if _, retreats := p.tables.BareNameIsColumn[strings.ToUpper(c.Text)]; !retreats {
		return false
	}
	return !p.beginsAnExpression(p.next())
}

// beginsAnExpression reports whether a token could START one. It is a coarse
// test on purpose -- its only caller uses it to decide that a word is a NAME
// because nothing follows it that could be an operand.
func (p *parser) beginsAnExpression(t *Token) bool {
	if t == nil {
		return false
	}
	if _, reserved := p.tables.ReservedTokens[t.Type]; reserved {
		return t.Type == TokL_PAREN || t.Type == TokNOT || t.Type == TokSTAR ||
			t.Type == TokDASH || t.Type == TokPLUS || t.Type == TokTILDE
	}
	return t.Type != TokCOMMA && t.Type != TokSEMICOLON
}

// atIdentifier reports whether the current token can stand in for a name --
// a plain word, a quoted identifier, or one of the many keywords the reference
// still allows as an identifier.
func (p *parser) atIdentifier() bool { return p.atIdentifierWhere(false) }

// atIdentifierWhere is atIdentifier with one exception made explicit: in a
// TABLE's name a word that would be a CALL anywhere else is just a name.
// `SELECT * FROM current_date` reads from a table called current_date -- the
// reference reads a table part as a call only where a parenthesis follows,
// and falls through to the ordinary name reader when none does.
func (p *parser) atIdentifierWhere(namingATable bool) bool {
	c := p.curr()
	if c == nil {
		return false
	}
	if c.Type == TokVAR || c.Type == TokIDENTIFIER {
		return true
	}
	// A no-paren function -- CURRENT_DATE and friends -- is a call, not a
	// name, even though it looks like a bare word.
	if _, isFunc := p.tables.NoParenFunctions[c.Type]; isFunc && !namingATable {
		return false
	}
	// A bare VALUES -- one with no argument list after it -- is a name where
	// the dialect always writes the clause WITH a list, and the start of a
	// VALUES clause where it does not. The reference reads the flag off the
	// dialect and so does this, because the same statement means different
	// things: `SELECT values` is a column in DuckDB and nothing in Spark.
	if c.Type == TokVALUES && p.tables.ValuesFollowedByParen {
		if n := p.next(); n == nil || n.Type != TokL_PAREN {
			return true
		}
	}
	_, ok := p.tables.IDVarTokens[c.Type]
	return ok
}

// afterComparison reports whether the token just consumed was a comparison or
// a LIKE. `ALL` and `ANY` are quantifiers only THERE: in a select list,
// `ALL (age >= 30) AS every` is an ordinary call to a function named ALL, and
// reading it as a quantifier built a node the reference never makes.
// quantifierHere reports whether ALL or ANY at the cursor, followed by
// parentheses, is a quantifier. After a comparison it is, except over an
// empty argument list. Standing alone it is one only over a query:
// ALL(SELECT 1) is a quantifier, while ALL() and ALL(1, 2) are calls.
func (p *parser) quantifierHere() bool {
	if p.atEmptyArgList() {
		return false
	}
	if p.afterComparison() {
		return true
	}
	inner := p.peekAt(2)
	return inner != nil && (inner.Type == TokSELECT || inner.Type == TokWITH || inner.Type == TokFROM)
}

func (p *parser) afterComparison() bool {
	if p.index == 0 {
		return false
	}
	prev := p.tokens[p.index-1].Type
	if _, ok := p.tables.Comparison[prev]; ok {
		return true
	}
	if _, ok := p.tables.Equality[prev]; ok {
		return true
	}
	return prev == TokLIKE || prev == TokILIKE
}

// singlestoreJSONPath turns the name after a SingleStore `::`/`::$`/`::%` into
// the JSONPath the reference's build_json_extract_path builds: an integral
// name is a SUBSCRIPT (SingleStore indexes from zero, so the number is kept as
// written), anything else a KEY.
func (p *parser) singlestoreJSONPath(name string) *Expression {
	if n, ok := pythonInt(name); ok {
		return New("JSONPath", Arg{"expressions", []*Expression{
			New("JSONPathRoot"),
			New("JSONPathSubscript", Arg{"this", n}),
		}})
	}
	return New("JSONPath", Arg{"expressions", []*Expression{
		New("JSONPathRoot"),
		New("JSONPathKey", Arg{"this", name}),
	}})
}

// jsonPathFor turns an argument into whatever the reference puts in a JSON
// extraction's `expression` slot. Probed across the dialects, not transcribed:
//
//	x -> 'a'    PATH[Root, Key(a)]        a path string is parsed
//	x -> '5'    Literal '5'               an INTEGRAL string is NOT a path
//	x -> 5      PATH[Root, Subscript(5)]  a number is a subscript
//	x -> 1.5    Literal 1.5               a number that is not an integer is not
//	x -> c      Column c                  a non-literal is carried through
//
// PostgreSQL parses no paths at all and keeps the whole string as one key --
// that is JSONPathIsParsed, and it was already probed.
//
// The integral-string rule was missing and nothing caught it: the port wrote
// `x -> '$."5"'` where the reference writes `x -> '5'`, and no corpus
// statement happened to put an integral string on an arrow.
func (p *parser) jsonPathFor(arg *Expression) *Expression {
	if arg == nil || arg.Class != "Literal" {
		// A column, a Neg, a concatenation: the reference cannot transpile a
		// path it cannot read, so it carries the expression through untouched.
		return arg
	}
	text, _ := arg.Args["this"].(string)
	isString, _ := arg.Args["is_string"].(bool)
	if n, ok := pythonInt(text); ok && !isString {
		return New("JSONPath", Arg{"expressions", []*Expression{
			New("JSONPathRoot"),
			New("JSONPathSubscript", Arg{"this", n}),
		}})
	}
	if !isString {
		// A number that is not a whole one is not a position either: the
		// reference makes it a KEY, spelled as it was written. Carrying it
		// through wrote `0 -> 0.5`, which reads back as a number rather than
		// as the key it names -- the generator fuzzer found it.
		return New("JSONPath", Arg{"expressions", []*Expression{
			New("JSONPathRoot"),
			New("JSONPathKey", Arg{"this", text}),
		}})
	}
	if !p.tables.JSONPathIsParsed {
		// PostgreSQL keeps the whole string as one key.
		return New("JSONPath", Arg{"expressions", []*Expression{
			New("JSONPathRoot"),
			New("JSONPathKey", Arg{"this", text}),
		}})
	}
	if _, ok := pythonInt(text); ok {
		return arg
	}
	if p.dialect == "duckdb" && duckdbKeepsJSONPointer(text) {
		// DuckDB also reads JSON Pointer syntax on the arrow, the same as on
		// its JSON_EXTRACT family: its own to_json_path skips parsing rather
		// than read `/duck/0` as the (different, and valid) JSONPath it
		// would otherwise be.
		return arg
	}
	path, err := parseJSONPath(text, p.jsonPathDashesInKeys())
	if err != nil {
		// ANY path the reference cannot read is handed straight back as the
		// string it was written as -- not only the ones that are obviously
		// not paths. `0 -> '[""@""]'` stays that literal. Falling back on
		// just the one error kind refused the rest, and the port then wrote
		// SQL it could not read: the generator fuzzer found both.
		//
		// Where the REFERENCE parses a path this port cannot, the two trees
		// differ and the differential says so; that is the check, and it is
		// silent today.
		return arg
	}
	return path
}

// isBareIdentifier reports whether a struct key could be written without
// quotes. The reference records `{'a b': 1}` with quoted=true and `{'a': 1}`
// with quoted=false, from the same written form -- the flag describes the NAME,
// not how it was typed.
func isBareIdentifier(text string) bool {
	if text == "" {
		return false
	}
	for i, r := range text {
		switch {
		case r == '_':
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// isTimeFormatArg reports whether index i is one the time mapping already
// handled, so the string-sensitivity check does not refuse it afterwards.
func isTimeFormatArg(indexes []int, i int) bool {
	for _, x := range indexes {
		if x == i {
			return true
		}
	}
	return false
}

// atAtTimeZone reports whether `AT TIME ZONE` starts here. AT and ZONE arrive
// as plain VARs and only TIME is a keyword, so all three are checked.
func (p *parser) atAtTimeZone() bool {
	if p.index+2 >= len(p.tokens) {
		return false
	}
	a, b, c := p.tokens[p.index], p.tokens[p.index+1], p.tokens[p.index+2]
	return a.Type == TokVAR && strings.EqualFold(a.Text, "AT") &&
		b.Type == TokTIME &&
		c.Type == TokVAR && strings.EqualFold(c.Text, "ZONE")
}

// atNullsModifier reports which class `IGNORE NULLS` or `RESPECT NULLS` builds
// here, or "" for neither. Both words are plain VARs.
func (p *parser) atNullsModifier() string {
	switch {
	case p.atWords("IGNORE", "NULLS"):
		return "IgnoreNulls"
	case p.atWords("RESPECT", "NULLS"):
		return "RespectNulls"
	}
	return ""
}

// atWords reports whether the next tokens are these words, whatever the
// tokenizer made of them.
// nextWords is atWords started one token further on, for a clause whose FIRST
// word is ambiguous and whose next ones settle it: `TIMESTAMP AS OF` is a
// temporal clause and a bare TIMESTAMP is a type.
func (p *parser) nextWords(words ...string) bool {
	was := p.index
	p.index++
	ok := p.index <= len(p.tokens) && p.atWords(words...)
	p.index = was
	return ok
}

func (p *parser) atWords(words ...string) bool {
	if p.index+len(words) > len(p.tokens) || p.hidesDAXWords(words) {
		return false
	}
	for i, w := range words {
		// A name written in QUOTES is a name, never the word it spells:
		// `[IF]` is a column called IF, and reading it as the keyword left
		// the port unable to read back what it had written. The generator
		// fuzzer found it.
		if isTextMatchExcluded(p.tokens[p.index+i].Type) {
			return false
		}
		if !strings.EqualFold(p.tokens[p.index+i].Text, w) {
			return false
		}
	}
	return true
}

// JSONPathFunc describes a name that turns its arguments into a JSON path.
//
// Nine names across these dialects do it, and the generic probe rejected every
// one: it feeds placeholder COLUMNS, and these builders look at what they were
// handed, so the shape they show it is not the shape real SQL takes.
type JSONPathFunc struct {
	Class string
	// Fold folds every argument after the first into ONE path, a segment
	// each -- JSON_EXTRACT_PATH(x, 'y', '0', 'z'). Otherwise a single
	// argument is read as a path STRING.
	Fold   bool
	Consts []FuncConst
	// KeepsTail says whether arguments past the path survive. Databricks
	// DROPS them, and dropping an argument changes what the call means, so
	// the port refuses rather than writing a call that says something else.
	KeepsTail bool
	// IntSubscripts: a folded key that reads as an integer is a SUBSCRIPT
	// rather than a key of that name, shifted by IndexShift.
	IntSubscripts bool
	IndexShift    int
	// RootDefault: with no path argument the whole document is meant, and
	// the builder supplies a bare root. T-SQL's JSON_QUERY(x) does this.
	RootDefault bool
}

// buildJSONPathFunction builds one of those calls, or returns nil to let the
// caller refuse it.
func (p *parser) buildJSONPathFunction(spec JSONPathFunc, args []*Expression) *Expression {
	if len(args) == 0 {
		return nil
	}
	var path *Expression
	var tail []*Expression
	switch {
	case spec.Fold:
		// Every argument has to be a LITERAL for the fold to happen. Handed
		// anything else the reference cannot transpile it and lays the
		// arguments out positionally instead -- `this`, `expression` and
		// whatever is left -- which is how `JSON_EXTRACT_PATH(a, VARIADIC
		// '{}')` keeps the VARIADIC where the path would be.
		for _, arg := range args[1:] {
			if arg.Class != "Literal" {
				return p.positionalJSONPathCall(spec, args)
			}
			// A literal that is not a STRING still folds where its text is
			// there to read: the reference turns a whole NUMBER into a
			// subscript (`BSON_EXTRACT_BSON(a, 'b', 2)`), and a
			// non-integral one into a key of that name. A literal with no
			// string text (a boolean) is not a segment this port folds.
			if !isStringLiteral(arg) {
				if _, ok := arg.Args["this"].(string); !ok {
					return nil
				}
			}
		}
		path = p.foldJSONPath(args[1:], spec)
	case len(args) == 1:
		// JSON_KEYS with no path argument passes the reference's own
		// `to_json_path` a missing second argument, which it hands straight
		// back as nil -- absent, not a ROOT path the way RootDefault's
		// builders default a missing one. `JSON_KEYS(x)` is JSONKeys(this=x)
		// alone.
		if spec.Class == "JSONKeys" {
			return New(spec.Class, Arg{"this", args[0]})
		}
		if !spec.RootDefault {
			return nil
		}
		path = New("JSONPath", Arg{"expressions", []*Expression{New("JSONPathRoot")}})
	default:
		// These builders PARSE the path, whatever the dialect's arrow does:
		// PostgreSQL's arrow keeps a string whole as one key, but its
		// JSON_EXTRACT_SCALAR(a, '$') gets a bare root.
		//
		// A string that will not parse is REFUSED rather than kept as a
		// literal the way the arrow keeps it. Databricks reads `$.x-y` as a
		// key the path grammar rejects, so falling back here would build a
		// Literal where the reference built a path -- a different tree, and
		// the differential said so.
		//
		// The exception is a SQL/JSON mode word: `lax $.b` and `strict $.b`
		// are not path syntax, and the reference keeps the whole string.
		if isStringLiteral(args[1]) {
			text, _ := args[1].Args["this"].(string)
			if p.dialect == "duckdb" && duckdbKeepsJSONPointer(text) {
				// DuckDB also reads JSON Pointer syntax, where every path
				// starts with a `/`, and its own `[#-i]` back-of-list
				// subscript -- neither is JSONPath, so the reference's own
				// to_json_path skips parsing rather than fail at it.
				path = args[1]
			} else if parsed, err := parseJSONPath(text, p.jsonPathDashesInKeys()); err != nil {
				if jsonPathKeptAsLiteral(text) {
					path = args[1]
				} else {
					return nil
				}
			} else {
				path = parsed
			}
		} else if lit := args[1]; lit.Class == "Literal" {
			// A number is a position: the reference reads `1` as `[1]`.
			text, _ := lit.Args["this"].(string)
			parsed, err := parseJSONPath("["+text+"]", p.jsonPathDashesInKeys())
			if err != nil {
				return nil
			}
			path = parsed
		} else {
			path = args[1]
		}
		if len(args) > 2 {
			if !spec.KeepsTail {
				return nil
			}
			tail = args[2:]
		}
	}
	out := []Arg{{"this", args[0]}, {"expression", path}}
	if len(tail) > 0 {
		out = append(out, Arg{"expressions", tail})
	}
	for _, c := range spec.Consts {
		out = append(out, Arg(c))
	}
	return New(spec.Class, out...)
}

// foldJSONPath turns a run of string literals into one path, one segment each.
func (p *parser) foldJSONPath(keys []*Expression, spec JSONPathFunc) *Expression {
	segments := []*Expression{New("JSONPathRoot")}
	for _, key := range keys {
		text, _ := key.Args["this"].(string)
		if n, ok := pythonInt(text); ok && spec.IntSubscripts {
			segments = append(segments,
				New("JSONPathSubscript", Arg{"this", n - spec.IndexShift}))
			continue
		}
		segments = append(segments, New("JSONPathKey", Arg{"this", text}))
	}
	return New("JSONPath", Arg{"expressions", segments})
}

// variantKeyAhead reports whether a NAME follows the colon. `c1:` alone is not
// an extraction -- the reference reads it as the column and leaves the colon --
// so the key has to be there before the colon is claimed.
func (p *parser) variantKeyAhead() bool {
	next := p.next()
	if next == nil {
		return false
	}
	return next.Type == TokVAR || next.Type == TokIDENTIFIER || next.Type == TokL_BRACKET
}

// parseVariantPath reads `:a`, `:a.b`, `:a[1].b` and `:['a']` into the JSONPath
// the reference builds. The key carries `quoted`, which the arrow form does not
// set -- a backquoted `c1:`a b“ is quoted and a bare one is not.
func (p *parser) parseVariantPath() (*Expression, error) {
	p.advance() // the colon
	segments := []*Expression{New("JSONPathRoot")}
	for {
		switch {
		case p.at(TokVAR) || p.at(TokIDENTIFIER):
			c := p.curr()
			p.advance()
			segments = append(segments, New("JSONPathKey",
				Arg{"this", c.Text}, Arg{"quoted", c.Type == TokIDENTIFIER}))
		case p.at(TokL_BRACKET):
			p.advance()
			c := p.curr()
			if c == nil {
				return nil, p.unsupported("a variant subscript with nothing in it")
			}
			switch c.Type {
			case TokSTAR:
				// `c1:item[*].price` takes every element. The subscript HOLDS
				// the wildcard rather than being one, which is the shape the
				// writer already knew how to spell.
				p.advance()
				segments = append(segments,
					New("JSONPathSubscript", Arg{"this", New("JSONPathWildcard")}))
			case TokNUMBER:
				n, err := strconv.Atoi(c.Text)
				if err != nil {
					return nil, p.unsupported("a variant subscript that is not an index")
				}
				p.advance()
				segments = append(segments, New("JSONPathSubscript", Arg{"this", n}))
			case TokSTRING:
				p.advance()
				segments = append(segments, New("JSONPathKey",
					Arg{"this", c.Text}, Arg{"quoted", true}))
			default:
				return nil, p.unsupported("a variant subscript that is not an index")
			}
			if !p.match(TokR_BRACKET) {
				return nil, p.unsupported("a variant subscript without its bracket")
			}
		default:
			return nil, p.unsupported("a variant path without a key")
		}
		if p.variantPathContinues() {
			continue
		}
		return New("JSONPath", Arg{"expressions", segments}), nil
	}
}

// MAP {k: v, ...} -- DuckDB's map literal. The keys are EXPRESSIONS and stay
// as they were written, where the keys of a bare `{...}` struct literal become
// identifiers.
func (p *parser) parseMapLiteral() (*Expression, error) {
	p.advance() // MAP
	p.advance() // the brace

	var items []*Expression
	for !p.at(TokR_BRACE) {
		key, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		if !p.match(TokCOLON) {
			return nil, p.unsupported("a map entry without a colon")
		}
		value, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		items = append(items, New("PropertyEQ",
			Arg{"this", key}, Arg{"expression", value}))
		if !p.match(TokCOMMA) {
			break
		}
	}
	if !p.match(TokR_BRACE) {
		return nil, p.unsupported("unclosed map literal")
	}
	return New("ToMap", Arg{"this", New("Struct", Arg{"expressions", items})}), nil
}

// parseMaterializeMap reads Materialize's own MAP grammar: `MAP(SELECT ...)`
// wraps a whole query into a ToMap, and `MAP[k => v, ...]` a bracketed list
// of key/value pairs into a ToMap over a Struct -- entered with MAP already
// current and the next token confirmed to be `(` or `[`.
func (p *parser) parseMaterializeMap() (*Expression, error) {
	p.advance() // MAP
	if p.match(TokL_PAREN) {
		if !p.at(TokSELECT) {
			return nil, p.unsupported("MAP(...) without a SELECT")
		}
		query, err := p.parseSelect()
		if err != nil {
			return nil, err
		}
		if !p.match(TokR_PAREN) {
			return nil, p.unsupported("unclosed MAP(...)")
		}
		return New("ToMap", Arg{"this", query}), nil
	}
	p.advance() // the bracket
	var items []*Expression
	for !p.at(TokR_BRACKET) {
		key, err := p.parsePrimary()
		if err != nil {
			return nil, err
		}
		if !p.match(TokFARROW) {
			return nil, p.unsupported("a MAP entry without =>")
		}
		value, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		items = append(items, New("PropertyEQ",
			Arg{"this", key}, Arg{"expression", value}))
		if !p.match(TokCOMMA) {
			break
		}
	}
	if !p.match(TokR_BRACKET) {
		return nil, p.unsupported("unclosed MAP[...]")
	}
	return New("ToMap", Arg{"this", New("Struct", Arg{"expressions", items})}), nil
}

// namedArgument unwraps what stands before a `:=` into the identifier the
// reference keeps there, or reports that it is not a name at all.
func namedArgument(e *Expression) *Expression {
	if e == nil {
		return nil
	}
	if e.Class == "Identifier" {
		return e
	}
	if e.Class == "Column" {
		inner, _ := e.Args["this"].(*Expression)
		if inner == nil || inner.Class != "Identifier" {
			return nil
		}
		// A QUALIFIED name is kept whole -- the reference writes
		// `F(a.b := 2)` -- where a bare one is unwrapped to the identifier
		// the reference keeps there. Unwrapping both dropped the qualifier
		// and named a different argument.
		for _, key := range []string{"table", "db", "catalog"} {
			if part, _ := e.Args[key].(*Expression); part != nil {
				return e
			}
		}
		return inner
	}
	return nil
}

// collationName coerces what COLLATE's TERM-level read produced: an
// UNQUALIFIED column -- one Identifier and nothing else -- names a word
// rather than selecting one, so it comes back as a Var (unquoted) or the
// Identifier itself (quoted), the same as any other bare word does where the
// reference wants a name, not an expression. A SCHEMA-qualified name is left
// as the Column it already is: `pg_catalog.default` names an object, and the
// dot is part of that name, not something to unwrap.
func collationName(e *Expression) *Expression {
	if e == nil || e.Class != "Column" {
		return e
	}
	for _, key := range []string{"table", "db", "catalog"} {
		if part, _ := e.Args[key].(*Expression); part != nil {
			return e
		}
	}
	id, _ := e.Args["this"].(*Expression)
	if id == nil || id.Class != "Identifier" {
		return e
	}
	if quoted, _ := id.Args["quoted"].(bool); quoted {
		return id
	}
	name, _ := id.Args["this"].(string)
	return New("Var", Arg{"this", name})
}

// parseQualifiedName reads the call at the end of a dotted chain. A name AFTER
// a dot is not the builtin it spells: `a.IF(1, 0)` is a call to a function
// called IF in some schema, and the reference builds an ANONYMOUS call for it
// rather than the If node a bare `IF(1, 0)` builds.
//
// Resolving the name here wrote `a.CASE WHEN 1 THEN 0 END`, which is not SQL
// at all -- the generator fuzzer found it and CI's gate stopped it.
func (p *parser) parseQualifiedName() (*Expression, error) {
	c := p.curr()
	if c == nil {
		return nil, p.unsupported("a qualified call with no name")
	}
	quoted := c.Type == TokIDENTIFIER
	name := c.Text
	p.advance()
	if !p.match(TokL_PAREN) {
		return nil, p.unsupported("a qualified call with no arguments")
	}
	var args []*Expression
	wasInCallArgs := p.inCallArgs
	p.inCallArgs = true
	if !p.at(TokR_PAREN) {
		for {
			// A qualified call's arguments are argument position too, so
			// `a.b(x -> y)` is a lambda here exactly as `b(x -> y)` is. Only
			// the unqualified call checked, so the port read this one as a
			// JSON extraction and Databricks wrote it back as `a.b(x:y)`,
			// which is not SQL. The generator fuzzer found it.
			var arg *Expression
			var err error
			switch {
			case p.atLambda():
				arg, err = p.parseLambda()
			case p.atKwarg():
				// `dfs.\`f.xlsx\`(type => 'excel')`: a named argument is a
				// Kwarg here exactly as it is in an unqualified call
				// (Parser._parse_function_call, _parse_kwarg).
				arg, err = p.parseKwarg()
			default:
				arg, err = p.parseExpression()
			}
			if err != nil {
				p.inCallArgs = wasInCallArgs
				return nil, err
			}
			args = append(args, arg)
			if !p.match(TokCOMMA) {
				break
			}
		}
	}
	p.inCallArgs = wasInCallArgs
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed qualified call")
	}
	var this any = name
	if quoted {
		this = New("Identifier", Arg{"this", name}, Arg{"quoted", true})
	}
	return New("Anonymous", Arg{"this", this}, Arg{"expressions", args}), nil
}

// parseDotField reads what follows a dot in a chain of accesses: a call, a
// name, a number or a string.
//
// It reports (nil, nil) where the token after the dot can be none of those,
// which leaves the dot unread and the statement refused for what follows it
// rather than for the dot itself.
func (p *parser) parseDotField() (*Expression, bool, error) {
	n := p.next()
	if n == nil {
		return nil, false, nil
	}
	switch n.Type {
	case TokSTAR:
		// `f(x).*` names every field of what the call returned
		// (Parser._parse_column_ops, _parse_star).
		p.advance()
		p.advance()
		return newStar(), false, nil
	case TokNUMBER:
		p.advance()
		p.advance()
		return New("Literal", Arg{"this", n.Text}, Arg{"is_string", false}), false, nil
	case TokSTRING:
		p.advance()
		p.advance()
		return New("Literal", Arg{"this", n.Text}, Arg{"is_string", true}), false, nil
	case TokNATIONAL_STRING:
		p.advance()
		p.advance()
		return New("National", Arg{"this", n.Text}), false, nil
	}
	// A name with an argument list after it is a CALL, and it is built
	// ANONYMOUS -- not dispatched to whatever class that name would build on
	// its own -- exactly as parseQualifiedName is for the same reason:
	// `f(x).IF(1, 0)` is a call to a function called IF in some schema, not
	// the reference's own IF, and the generator fuzzer found this port
	// writing `f(x).CASE WHEN 1 THEN 0 END`, which is not SQL at all.
	if after := p.peekAt(2); after != nil && after.Type == TokL_PAREN {
		p.advance()
		call, err := p.parseQualifiedName()
		return call, err == nil, err
	}
	mark := p.index
	p.advance()
	if !p.atIdentifier() {
		p.index = mark
		return nil, false, nil
	}
	name, err := p.parseIdentifier()
	return name, false, err
}

// columnsToDots rewrites every Column beneath a node into the dotted name it
// spells: `Column(this=b, table=a)` becomes `Dot(a, b)`, and a Column of one
// part becomes that part alone.
//
// This is the reference's `to_dot(include_dots=False)` applied through a
// transform. It runs when a chain of accesses turns out to end in a function
// call, because then every name in front of it is part of the call's name.
//
// In practice only single-part Columns reach it: a qualified name in front of
// a call -- `t.col[0].F()` -- is read as a dotted name by parsePrimary before
// the chain gets here, so it arrives already in this shape. The multi-part
// case below is the reference's rule rather than a path this port takes, and
// it is kept so that a Column arriving by some other route is rewritten the
// same way rather than left as a Column the reference would not have built.
func columnsToDots(e *Expression) *Expression {
	if e == nil {
		return nil
	}
	if e.Class == "Column" {
		var parts []*Expression
		for _, key := range []string{"catalog", "db", "table", "this"} {
			if part, _ := e.Args[key].(*Expression); part != nil {
				parts = append(parts, part)
			}
		}
		if len(parts) == 0 {
			return e
		}
		out := parts[0]
		for _, part := range parts[1:] {
			out = New("Dot", Arg{"this", out}, Arg{"expression", part})
		}
		return out
	}
	// A SHALLOW copy, rewritten child by child. A deep one here copies the
	// whole subtree at every level and then throws each copy away when the
	// level below copies it again -- quadratic in the depth of the tree, and
	// slow enough on a few thousand nested operators that the fuzzer's own
	// worker gave up on it.
	out := e.shallowCopy()
	for key, arg := range out.Args {
		switch v := arg.(type) {
		case *Expression:
			out.Set(key, columnsToDots(v))
		case []*Expression:
			kids := make([]*Expression, len(v))
			for i, k := range v {
				kids[i] = columnsToDots(k)
			}
			out.Set(key, kids)
		}
	}
	return out
}

// parseWidgetPlaceholder reads Databricks' `{name}`, a notebook widget
// reference -- the one dialect where a brace opens a placeholder rather
// than a struct literal -- and returns nil where the cursor opens neither.
func (p *parser) parseWidgetPlaceholder() *Expression {
	if !p.tables.Placeholder.WidgetPlaceholder || !p.at(TokL_BRACE) {
		return nil
	}
	n := p.next()
	if n == nil || (n.Type != TokVAR && n.Type != TokIDENTIFIER) {
		return nil
	}
	if close := p.peekAt(2); close == nil || close.Type != TokR_BRACE {
		return nil
	}
	p.advance()
	p.advance()
	p.advance()
	name := New("Identifier", Arg{"this", n.Text}, Arg{"quoted", n.Type == TokIDENTIFIER})
	return New("Placeholder", Arg{"this", name}, Arg{"widget", true})
}

// parseParameter reads a bound parameter or a placeholder written with the
// marker this dialect uses, and returns nil where the cursor opens neither.
//
// It is one function because the SPELLINGS are one question: `$nm` is a
// Placeholder in DuckDB, a Parameter in PostgreSQL and Databricks, and a
// plain column elsewhere; `@nm` is a Parameter everywhere except DuckDB,
// where `@` is absolute value. Every caller that accepts one has to accept
// all of them -- an `@`-only reader in one position was how a table variable
// came to parse in T-SQL while the `$` form this port WRITES could not be
// read back anywhere else.
// parseSessionParameter reads `@@name` and `@@scope.name`: the reference's
// own `_parse_session_parameter`. The first word is read as a bare
// identifier; a DOT after it means the word was actually the SCOPE
// (GLOBAL/SESSION), and what follows is the real name, read as a Var
// instead -- never quoted, unlike the first read.
func (p *parser) parseSessionParameter() (*Expression, error) {
	p.advance() // @@
	this, err := p.parseIdentifier()
	if err != nil {
		return nil, err
	}
	var kind any
	if p.match(TokDOT) {
		kind = this.Name()
		c := p.curr()
		if c == nil {
			return nil, p.unsupported("@@ scope without a name")
		}
		p.advance()
		this = New("Var", Arg{"this", c.Text})
	}
	return New("SessionParameter", Arg{"this", this}, Arg{"kind", kind}), nil
}

// parseBraceParameterName reads a name inside `${...}`: an Identifier where
// one is written, otherwise the token itself as a Var.
func (p *parser) parseBraceParameterName() *Expression {
	c := p.curr()
	if c == nil || c.Type == TokR_BRACE || c.Type == TokCOLON {
		return nil
	}
	p.advance()
	switch c.Type {
	case TokIDENTIFIER:
		return New("Identifier", Arg{"this", c.Text}, Arg{"quoted", true})
	case TokNUMBER:
		return New("Literal", Arg{"this", c.Text}, Arg{"is_string", false})
	case TokSTRING:
		return New("Literal", Arg{"this", c.Text}, Arg{"is_string", true})
	}
	return New("Var", Arg{"this", c.Text})
}

func (p *parser) parseParameter() *Expression {
	c := p.curr()
	if c == nil || c.Type != TokPARAMETER {
		return nil
	}
	// Databricks brackets the name: `${x}`. It is the same Parameter, with an
	// `expression` flag recording that the braces were there -- and the port
	// WRITES this form, so it has to read it back.
	// Hive brackets the name too, but with an optional COLON and a second
	// name after it: `${hiveconf:some_var}` (HiveParser._parse_parameter).
	if isHiveFamily(p.dialect) && c.Text == "$" && p.next() != nil && p.next().Type == TokL_BRACE {
		mark := p.index
		p.advance() // $
		p.advance() // {
		this := p.parseBraceParameterName()
		// The reference records the COLON's absence as FALSE rather than
		// leaving the argument out.
		var expression any = false
		if p.match(TokCOLON) {
			expression = p.parseBraceParameterName()
		}
		if this != nil && p.match(TokR_BRACE) {
			return New("Parameter", Arg{"this", this}, Arg{"expression", expression})
		}
		p.index = mark
	}
	if c.Text == "$" && p.next() != nil && p.next().Type == TokL_BRACE {
		// The braces delimit the name, so it can be ANY single token --
		// including a keyword, and including one that only became a keyword
		// by case-folding. `$WHERE` writes as `${WHERE}`, and `$aſ` writes as
		// `${aſ}` whose name upper-cases to AS; requiring a name-shaped token
		// here could read back neither. The reference takes whatever stands
		// between the braces too.
		if name := p.peekAt(2); name != nil && name.Type != TokR_BRACE &&
			p.peekAt(3) != nil && p.peekAt(3).Type == TokR_BRACE {
			p.advance()
			p.advance()
			p.advance()
			p.advance()
			return New("Parameter", Arg{"this", parameterName(name)}, Arg{"expression", false})
		}
		return nil
	}
	n := p.next()
	// A QUOTED name counts here too: `@"x"` is a Parameter named `x`, an
	// Identifier rather than the Var a bare word would give -- and `$"foo"`
	// the same Placeholder a bare `$foo` would, its quotes just dropped.
	if !isParameterName(n) && !isQuotedName(n) {
		return nil
	}
	class := ""
	switch {
	case c.Text == "$" && n.Type == TokNUMBER:
		class = p.tables.Placeholder.DollarNumber
	case c.Text == "$":
		class = p.tables.Placeholder.DollarName
	case c.Text == "@":
		class = p.tables.Placeholder.AtName
	}
	switch class {
	case "Placeholder":
		p.advance()
		p.advance()
		return New("Placeholder", Arg{"this", n.Text})
	case "Parameter":
		p.advance()
		p.advance()
		// The Hive family reads a parameter with its OWN reader, and that one
		// records whether a `:` and a second name followed as FALSE rather
		// than leaving the argument out (HiveParser._parse_parameter) --
		// Databricks extends Hive, so `FROM @t` carries it too.
		if hiveParameterReader(p.dialect) {
			return New("Parameter", Arg{"this", parameterName(n)}, Arg{"expression", false})
		}
		return New("Parameter", Arg{"this", parameterName(n)})
	}
	return nil
}

// parameterName is what a parameter's name is BUILT as: a numeric one is a
// Literal, a word is a Var, and the reference makes that split in both the
// braced form and the bare one.
func parameterName(n *Token) *Expression {
	if n.Type == TokNUMBER {
		return New("Literal", Arg{"this", n.Text}, Arg{"is_string", false})
	}
	// A name written in QUOTES stays quoted: the reference reads an
	// identifier here before it reads anything else, and only what is not one
	// becomes a Var. Reading `${`$$`}` as a Var dropped the quotes when it
	// was written back, leaving `${$$}` -- which the port could no longer
	// read, because a bare $$ is not a name. The generator fuzzer found it.
	if isQuotedName(n) {
		return New("Identifier", Arg{"this", n.Text}, Arg{"quoted", true})
	}
	// A name written as a STRING stays a string, for the same reason: the
	// braces take whatever stands between them, and `${'######'}` written
	// back as `${######}` was a name the port could no longer read. The
	// generator fuzzer found this one too.
	if n.Type == TokSTRING {
		return New("Literal", Arg{"this", n.Text}, Arg{"is_string", true})
	}
	return New("Var", Arg{"this", n.Text})
}

// positionalJSONPathCall is the reference's fallback for a path-folding
// function handed something it cannot fold: the arguments are laid out
// positionally rather than turned into a path.
//
// It is a DIFFERENT tree from the folded one, not a worse version of it --
// `JSON_EXTRACT_PATH(a, x)` cannot be a path because x is not known here, and
// the reference says so by keeping the argument where it was written.
//
// The GENERATOR does not write this shape yet. PostgreSQL spells a JSON
// extraction one argument per path part, and every template it has quotes the
// part -- so a tree whose parts are columns has no form to go into, and the
// writer refuses rather than quoting a column into a string. Reading these is
// worth having on its own: a guard asking which columns a statement touches
// gets an answer where it used to get a refusal.
func (p *parser) positionalJSONPathCall(spec JSONPathFunc, args []*Expression) *Expression {
	out := []Arg{{"this", args[0]}}
	if len(args) > 1 {
		out = append(out, Arg{"expression", args[1]})
	}
	if len(args) > 2 {
		out = append(out, Arg{"expressions", args[2:]})
	}
	// The constants the FOLDED form carries are not set here. The reference
	// adds them beside the path it built, and this shape has no path -- so
	// `only_json_types` is absent rather than present-and-false, which is a
	// different tree and the differential said so.
	return New(spec.Class, out...)
}

// parseNextValueFor reads `NEXT VALUE FOR seq [OVER (ORDER BY ...)]`.
//
// The ordering is recorded as FALSE where it is not written, which is what
// the reference's `matched and value` yields.
func (p *parser) parseNextValueFor() (*Expression, error) {
	p.advance() // NEXT
	p.advance() // VALUE
	p.advance() // FOR

	this, err := p.parseColumn()
	if err != nil {
		return nil, err
	}
	node := New("NextValueFor", Arg{"this", this})
	if !p.match(TokOVER) {
		node.Set("order", false)
		return node, nil
	}
	if !p.match(TokL_PAREN) {
		return nil, p.unsupported("OVER without a parenthesised ordering")
	}
	if !p.match(TokORDER_BY) {
		return nil, p.unsupported("NEXT VALUE FOR OVER without an ordering")
	}
	order, err := p.parseOrder()
	if err != nil {
		return nil, err
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed OVER clause")
	}
	node.Set("order", order)
	return node, nil
}

// quotedTypeName reports whether a QUOTED identifier names a type, and which.
//
// The quotes are the tokenizer's business, not the type's: T-SQL brackets a
// type name the same way it brackets a column name, so the word arrives here
// as an identifier. The reference lexes the identifier's text again and asks
// what the first token is; this does the same, so `[int]` and `int` name the
// same type.
// namesAType reports whether the text inside a QUOTED type name names
// anything at all.
//
// It does not when the text holds something the tokenizer cannot read at all:
// `"“"` is a pair of quotes round two characters DuckDB has no token for, and
// the reference falls back to the UNKNOWN type rather than making a name of
// them. Text that lexes to several WORDS is still a name -- `"a b"` names a
// type called `a b`.
func (p *parser) namesAType(text string) bool {
	tk, err := NewTokenizer(p.dialect)
	if err != nil {
		return true
	}
	toks, err := tk.Tokenize(text)
	if err != nil {
		return true
	}
	for _, t := range toks {
		if t.Type == TokUNKNOWN {
			return false
		}
	}
	return true
}

func (p *parser) quotedTypeName(c *Token) (bool, string) {
	if c.Type != TokIDENTIFIER {
		return false, ""
	}
	tk, err := NewTokenizer(p.dialect)
	if err != nil {
		return false, ""
	}
	toks, err := tk.Tokenize(c.Text)
	if err != nil || len(toks) == 0 {
		return false, ""
	}
	// The FIRST token settles it, and the rest are dropped: T-SQL's
	// `[INT 0]` is an INT to the reference, with the number written nowhere.
	// The generator fuzzer found the port writing the whole text back out.
	kind, ok := p.tables.TypeTokens[toks[0].Type]
	return ok, kind
}

// quotedNamedTypeWord is quotedTypeName's counterpart for the two keywords
// that name no DataType.Type member at all: a quoted `"oid"` re-lexes the
// same as a bare one, and reads as an ObjectIdentifier or PseudoType rather
// than a name -- but only where this dialect's tokenizer has that keyword;
// elsewhere the re-lex comes back a plain word and this returns nil.
func (p *parser) quotedNamedTypeWord(c *Token) *Expression {
	if c.Type != TokIDENTIFIER {
		return nil
	}
	tk, err := NewTokenizer(p.dialect)
	if err != nil {
		return nil
	}
	toks, err := tk.Tokenize(c.Text)
	if err != nil || len(toks) != 1 {
		return nil
	}
	switch toks[0].Type {
	case TokPSEUDO_TYPE:
		return New("PseudoType", Arg{"this", strings.ToUpper(c.Text)})
	case TokOBJECT_IDENTIFIER:
		return New("ObjectIdentifier", Arg{"this", strings.ToUpper(c.Text)})
	}
	return nil
}

// concatCoalesce reports the dialect's CONCAT_COALESCE: whether a Concat folds
// a NULL argument to NULL. Adjacent string literals become a Concat carrying
// this flag (Dialect.CONCAT_COALESCE, Parser._parse_primary).
func concatCoalesce(dialect string) bool {
	switch dialect {
	case "dremio", "drill", "duckdb", "exasol", "postgres", "tsql":
		return true
	}
	return false
}

// formatPhraseFollows reports Teradata's `col (FORMAT '...')`, where the
// parenthesis opens a column PHRASE rather than a call, so the name is read
// as a column and the phrase is attached to it
// (TeradataParser._parse_function, _parse_column_ops).
func (p *parser) formatPhraseFollows() bool {
	return p.dialect == "teradata" && p.index+2 < len(p.tokens) &&
		p.tokens[p.index+2].Type == TokFORMAT
}

// tryParseIfExpression reads `IF <cond> THEN <a> [ELSE <b>] END`. It reports
// FALSE with the parser put back where it was when the word turns out to be a
// name instead: `SELECT if, a` is a column, and the reference retreats rather
// than failing (Parser._parse_if).
func (p *parser) tryParseIfExpression() (*Expression, bool, error) {
	mark := p.index
	p.advance() // IF
	// A comma or a closing parenthesis after the word ends the matter: it is
	// a NAME there, and the reference's `_parse_disjunction` returns None for
	// both where this one would consume them.
	if c := p.curr(); c == nil || c.Type == TokCOMMA || c.Type == TokR_PAREN {
		p.index = mark
		return nil, false, nil
	}
	afterIf := p.index
	condition, err := p.parseDisjunction()
	if err != nil {
		p.index = mark
		return nil, false, err
	}
	// A condition that read NOTHING is not a condition: at a comma the
	// disjunction hands back a node without consuming a token, and taking
	// that as the condition turned `SELECT if, a` into a broken IF.
	if condition == nil || p.index == afterIf {
		p.index = mark
		return nil, false, nil
	}
	p.match(TokTHEN)
	trueArm, err := p.parseDisjunction()
	if err != nil {
		p.index = mark
		return nil, false, err
	}
	var falseArm *Expression
	if p.match(TokELSE) {
		falseArm, err = p.parseDisjunction()
		if err != nil {
			p.index = mark
			return nil, false, err
		}
	}
	p.match(TokEND)
	return New("If", Arg{"this", condition}, Arg{"true", trueArm},
		Arg{"false", falseArm}), true, nil
}

// isTextMatchExcluded reports whether a token type is one the reference keeps
// out of WORD matching: the token's TEXT is not what was written, so matching
// on it reads a quoted name -- or a STRING -- as the keyword it spells.
// `TBLPROPERTIES ('FORMAT'='parquet')` names a property CALLED FORMAT rather
// than the STORED AS property, and reading the quoted key as the word turned
// it into one (Parser.TEXT_MATCH_EXCLUDED_TOKENS).
func isTextMatchExcluded(t TokenType) bool {
	switch t {
	case TokBIT_STRING, TokBYTE_STRING, TokHEREDOC_STRING, TokHEX_STRING,
		TokIDENTIFIER, TokNATIONAL_STRING, TokRAW_STRING, TokSTRING:
		return true
	}
	return false
}

// quotedTypePreserved reports whether a quoted type name is kept as a type of
// its own rather than re-read as the keyword it spells. PostgreSQL names
// exactly one, and Redshift inherits it
// (PostgresParser.QUOTED_TYPES_TO_PRESERVE).
func (p *parser) quotedTypePreserved(name string) bool {
	if p.dialect != "postgres" && p.dialect != "redshift" {
		return false
	}
	return name == "char"
}

// arrayConstructor is the reference's build_array_constructor: an Array built
// from a bracket records whether its single member is a QUERY (a value
// constructor), whether the dialect distinguishes the bracket spelling, and
// -- in BigQuery -- that the bracket decides how a struct field inherits its
// name (Generator: Array.args, BigQueryParser._parse_bracket).
func (p *parser) arrayConstructor(items []*Expression, bracketKind TokenType) *Expression {
	arr := New("Array", Arg{"expressions", items})
	arr.Set("value_constructor", len(items) == 1 && isQuery(items[0]))
	if p.tables.HasDistinctArrayConstructors {
		arr.Set("bracket_notation", bracketKind == TokL_BRACKET)
	}
	if p.dialect == "bigquery" {
		arr.Set("struct_name_inheritance", true)
	}
	return arr
}

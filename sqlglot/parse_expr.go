package sqlglot

import "strings"

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
	"Div":     true,
	"DPipe":   true,
	"Collate": true,
	// Databricks' `a:b` reads the right-hand side as a JSON PATH, not as the
	// column the generic operator rule produces. `->` and `->>` are handled
	// directly in parsePostfix and never reach here; the colon form has its
	// own grammar which is not ported, so it stays refused.
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

func (p *parser) parseExpression() (*Expression, error) { return p.parseAssignment() }

func (p *parser) parseAssignment() (*Expression, error) {
	this, err := p.parseDisjunction()
	if err != nil {
		return nil, err
	}
	if p.at(TokCOLON_EQ) {
		return nil, p.unsupported("assignment")
	}
	return this, nil
}

func (p *parser) parseDisjunction() (*Expression, error) {
	return p.parseBinary(p.tables.Disjunction, p.parseConjunction)
}

func (p *parser) parseConjunction() (*Expression, error) {
	return p.parseBinary(p.tables.Conjunction, p.parseEquality)
}

func (p *parser) parseEquality() (*Expression, error) {
	return p.parseBinary(p.tables.Equality, p.parseComparison)
}

func (p *parser) parseComparison() (*Expression, error) {
	return p.parseBinary(p.tables.Comparison, p.parseRange)
}

// parseRange handles IS, IN, BETWEEN and the LIKE family, including their
// negated forms.
//
// NOT is read here rather than at the unary level because `a NOT LIKE b` sets
// a flag on the Like node while `a NOT IN (…)` wraps the In in a Not -- the
// reference treats the two differently and so must this. A NOT that turns out
// not to introduce a range is put back.
func (p *parser) parseRange() (*Expression, error) {
	this, err := p.parseJSONArrow()
	if err != nil {
		return nil, err
	}

	for {
		negate := p.match(TokNOT)
		c := p.curr()
		if c == nil {
			if negate {
				p.index--
			}
			return this, nil
		}

		switch {
		case c.Type == TokIS:
			p.advance()
			this, err = p.parseIs(this)
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
			var right *Expression
			right, err = p.parseJSONArrow()
			if err == nil {
				this = New(class, Arg{"this", this}, Arg{"expression", right})
			}
		default:
			if _, isRange := p.tables.RangeTokens[c.Type]; isRange {
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
			// A negation followed by another range operator is parenthesised,
			// so `NOT a LIKE b LIKE c` cannot re-associate.
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
		other, err := p.parseBitwise()
		if err != nil {
			return nil, err
		}
		class := "NullSafeNEQ"
		if negate {
			class = "NullSafeEQ"
		}
		return New(class, Arg{"this", this}, Arg{"expression", other}), nil
	}

	var expression *Expression
	// `x IS UNKNOWN` is `x IS NULL`: the reference gives UNKNOWN no node of its
	// own after IS, it simply builds a Null. The negated form then picks up the
	// dialect's NOT shape below, exactly as `IS NOT NULL` does.
	if p.match(TokNULL) || p.match(TokUNKNOWN) {
		expression = New("Null")
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
		return New("Is", Arg{"this", this}, Arg{"expression", expression}, Arg{"negate", true}), nil
	}
	is := New("Is", Arg{"this", this}, Arg{"expression", expression})
	if negate {
		return New("Not", Arg{"this", is}), nil
	}
	return is, nil
}

func (p *parser) parseIn(this *Expression) (*Expression, error) {
	if !p.match(TokL_PAREN) {
		return nil, p.unsupported("IN without a parenthesised list")
	}
	// `a IN (SELECT 1)`: the reference records the query under `query` rather
	// than as a one-item expression list, and it DOES wrap it in a Subquery.
	if p.at(TokSELECT) || p.at(TokWITH) {
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
	// PostgreSQL's SYMMETRIC swaps the bounds when they arrive the wrong way
	// round, so `BETWEEN SYMMETRIC 10 AND 2` matches where the plain form
	// matches nothing. Neither word is a keyword token -- the reference
	// matches them by text -- and reading them as the lower bound turned the
	// whole predicate into an And over two comparisons.
	// PostgreSQL's SYMMETRIC swaps the bounds when they arrive the wrong way
	// round, so `BETWEEN SYMMETRIC 10 AND 2` matches where the plain form
	// matches nothing. Reading the word as the lower bound turned the whole
	// predicate into an And over two comparisons -- a silently different
	// question. Only PostgreSQL WRITES the keyword back; every other dialect
	// expands it to an OR of two BETWEENs, a transform this port does not
	// have, so the construct is refused rather than half-supported.
	//
	// TokVAR only: `SELECT a BETWEEN "symmetric" AND b` is a column with that
	// name, and matching on text alone read the caller's own identifier as a
	// keyword.
	if c := p.curr(); c != nil && c.Type == TokVAR &&
		(strings.EqualFold(c.Text, "SYMMETRIC") || strings.EqualFold(c.Text, "ASYMMETRIC")) {
		return nil, p.unsupported("BETWEEN " + strings.ToUpper(c.Text))
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
		Arg{"symmetric", nil}), nil
}

// parseJSONArrow reads `j -> '$.a'` and `j ->> '$.a'`.
//
// These sit between the range operators and the bitwise ones: LOOSER than
// arithmetic, a cast or a unary minus, and TIGHTER than a comparison, IS,
// LIKE or AND. The port had them at the tightest level of all, with the
// postfix operators, which read `0 ^ 0 -> ”` as `0 ^ (0 -> ”)` where the
// reference reads `(0 ^ 0) -> ”`. Same tokens, different question, and no
// corpus statement happened to contain the combination -- the generator
// fuzzer found it.
//
// The right-hand side is a path STRING parsed into a JSONPath, not an
// expression, which is why this is not simply another binary level.
func (p *parser) parseJSONArrow() (*Expression, error) {
	this, err := p.parseBitwise()
	if err != nil {
		return nil, err
	}
	for p.at(TokARROW) || p.at(TokDARROW) {
		class := "JSONExtract"
		if p.at(TokDARROW) {
			class = "JSONExtractScalar"
		}
		p.advance()
		path, perr := p.parseJSONPathOperand()
		if perr != nil {
			return nil, perr
		}
		args := []Arg{{"this", this}, {"expression", path},
			{"only_json_types", p.tables.JSONArrowOnlyJSONTypes}}
		if class == "JSONExtractScalar" && p.tables.JSONArrowSetsScalarOnly {
			// PostgreSQL leaves this arg OFF the node; the others set it
			// false. An arg present-but-false is a different tree from an
			// arg absent, so whether to set it is probed, not the value.
			args = append(args, Arg{"scalar_only", false})
		}
		this = New(class, args...)
	}
	return this, nil
}

func (p *parser) parseBitwise() (*Expression, error) {
	this, err := p.parseTerm()
	if err != nil {
		return nil, err
	}
	for {
		c := p.curr()
		if c == nil {
			return this, nil
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
	return p.parseBinary(p.tables.Term, p.parseFactor)
}

func (p *parser) parseFactor() (*Expression, error) {
	return p.parseBinary(p.tables.Factor, p.parseFactorOperand)
}

// parseFactorOperand inserts the exponent level only where the dialect has
// one, exactly as the reference does.
func (p *parser) parseFactorOperand() (*Expression, error) {
	if len(p.tables.Exponent) == 0 {
		return p.parseUnary()
	}
	return p.parseBinary(p.tables.Exponent, p.parseUnary)
}

// parseBinary runs one left-associative precedence level.
func (p *parser) parseBinary(ops map[TokenType]string, next func() (*Expression, error)) (*Expression, error) {
	this, err := next()
	if err != nil {
		return nil, err
	}
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
			this = New(class, Arg{"this", this}, Arg{"expression", right},
				Arg{"typed", p.tables.TypedDivision}, Arg{"safe", p.tables.SafeDivision})
			continue
		}
		this = New(class, Arg{"this", this}, Arg{"expression", right})
	}
}

// parseUnary mirrors the reference's UNARY_PARSERS, including that NOT takes
// an equality as its operand rather than a unary -- so `NOT a = b` negates the
// comparison, not the column.
func (p *parser) parseUnary() (*Expression, error) {
	c := p.curr()
	if c == nil {
		return nil, p.unsupported("expression")
	}
	switch c.Type {
	case TokPLUS:
		p.advance()
		return p.parseUnary() // unary plus is a no-op in the reference too
	case TokDASH:
		p.advance()
		this, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return New("Neg", Arg{"this", this}), nil
	case TokTILDE:
		p.advance()
		this, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return New("BitwiseNot", Arg{"this", this}), nil
	case TokNOT:
		p.advance()
		this, err := p.parseEquality()
		if err != nil {
			return nil, err
		}
		return New("Not", Arg{"this", this}), nil
	}
	return p.parsePostfix()
}

// parsePostfix reads what binds tighter than any operator: the :: cast, which
// the reference handles among the column operators.
func (p *parser) parsePostfix() (*Expression, error) {
	this, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}
	for {
		if p.match(TokDCOLON) {
			to, err := p.parseDataType()
			if err != nil {
				return nil, err
			}
			cast := New("Cast", Arg{"this", this}, Arg{"to", to})
			cast.Type = to
			this = cast
			continue
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
			if this != nil && p.tables.WithinGroupAbsorbedBy[this.Class] {
				// This class folds the clause into itself rather than being
				// wrapped by it, and the fold is not ported.
				return nil, p.unsupported("WITHIN GROUP folded into " + this.Class)
			}
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
		// `x AT TIME ZONE 'UTC'`: three words, of which only TIME is a
		// keyword, so the phrase is matched by text.
		if p.atAtTimeZone() {
			p.advance()
			p.advance()
			p.advance()
			// parsePrimary, not parseBitwise: the zone parse recurses back into
			// this loop, so a chained `AT TIME ZONE 'a' AT TIME ZONE 'b'` had
			// the second one swallowed into the first one's ZONE -- right
			// associative, where the reference nests left.
			zone, err := p.parsePrimary()
			if err != nil {
				return nil, err
			}
			this = New("AtTimeZone", Arg{"this", this}, Arg{"zone", zone})
			continue
		}
		// `SUM(x) FILTER(WHERE p)` wraps the aggregate in a Filter carrying a
		// Where -- not a call to a function named FILTER.
		if p.at(TokFILTER) && p.next() != nil && p.next().Type == TokL_PAREN {
			p.advance()
			p.advance()
			if !p.match(TokWHERE) {
				return nil, p.unsupported("FILTER without WHERE")
			}
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
			p.advance()
			// A SUBSCRIPT: a colon here separates a slice's bounds.
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
	return n == nil || n.Type != TokVAR
}

// parseBracketItems reads what sits between `[` and `]`: a comma-separated
// list where any item may be a slice. `x[:]` is a Slice with neither bound,
// which is why an empty side is a missing arg rather than an error.
func (p *parser) parseBracketItems(_ bool) ([]*Expression, error) {
	var items []*Expression
	for !p.at(TokR_BRACKET) {
		var low *Expression
		if !p.atSliceStart() {
			e, err := p.parseExpression()
			if err != nil {
				return nil, err
			}
			low = e
		}
		if p.match(TokCOLON) {
			var high *Expression
			if !p.at(TokR_BRACKET) && !p.at(TokCOMMA) {
				e, err := p.parseExpression()
				if err != nil {
					return nil, err
				}
				high = e
			}
			items = append(items, New("Slice", Arg{"this", low}, Arg{"expression", high}))
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

// parsePrimary is entered with a token current; parseUnary checked.
func (p *parser) parsePrimary() (*Expression, error) {
	c := p.curr()

	if c.Type == TokINTERVAL {
		return p.parseInterval()
	}

	// A bound parameter. `?` is one on its own; `:name` is the colon and the
	// name, and a COLON can only open a placeholder HERE -- everywhere else it
	// is infix, separating a slice's bounds or a struct's key from its value.
	//
	// The port wrote `ARRAY(:Wa)` and could not read it back, which is how the
	// generator fuzzer found this: the reference reads it, so the round trip
	// was the port's own gap rather than a property stronger than the oracle.
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
		if n := p.next(); isParameterName(n) && n.Type != TokNUMBER {
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
	if c.Type == TokPARAMETER && c.Text == "$" && p.next() != nil && p.next().Type == TokL_BRACE {
		// The braces delimit the name, so it can be any single word --
		// including a KEYWORD. `$WHERE` writes as `${WHERE}` and requiring a
		// VAR there could not read it back.
		if name := p.peekAt(2); isParameterName(name) &&
			p.peekAt(3) != nil && p.peekAt(3).Type == TokR_BRACE {
			p.advance()
			p.advance()
			p.advance()
			p.advance()
			// A numeric name is a Literal, a word is a Var -- the same split
			// the unbraced form makes.
			inner := New("Var", Arg{"this", name.Text})
			if name.Type == TokNUMBER {
				inner = New("Literal", Arg{"this", name.Text}, Arg{"is_string", false})
			}
			return New("Parameter", Arg{"this", inner}, Arg{"expression", false}), nil
		}
	}
	if c.Type == TokPARAMETER {
		if n := p.next(); isParameterName(n) {
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
				return p.dotted(New("Placeholder", Arg{"this", n.Text})), nil
			case "Parameter":
				p.advance()
				p.advance()
				// `$1` names its parameter with a NUMBER, and the reference
				// keeps that as a Literal rather than a Var.
				if n.Type == TokNUMBER {
					lit := New("Literal", Arg{"this", n.Text}, Arg{"is_string", false})
					return New("Parameter", Arg{"this", lit}), nil
				}
				return New("Parameter", Arg{"this", New("Var", Arg{"this", n.Text})}), nil
			}
		}
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

	// `{'a': 1, 'b': x}` is a Struct whose items are PropertyEQ: the key is an
	// IDENTIFIER even though it is written as a string.
	if c.Type == TokL_BRACE {
		p.advance()
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
		p.afterComparison() && !p.atEmptyArgList() {
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
		return New("Array", Arg{"expressions", items}), nil
	}

	// `[1, 2, 3]` is an Array literal. Same token as the subscript above; the
	// difference is position, and only this one begins an expression.
	if c.Type == TokL_BRACKET {
		p.advance()
		items, err := p.parseBracketItems(true)
		if err != nil {
			return nil, err
		}
		return New("Array", Arg{"expressions", items}), nil
	}

	// CURRENT_DATE and friends are calls with no argument list.
	if class, ok := p.tables.NoParenFunctionClasses[c.Type]; ok {
		p.advance()
		return New(class), nil
	}

	switch c.Type {
	case TokCASE:
		return p.parseCase()
	case TokNUMBER:
		p.advance()
		return New("Literal", Arg{"this", c.Text}, Arg{"is_string", false}), nil
	case TokSTRING:
		p.advance()
		return New("Literal", Arg{"this", c.Text}, Arg{"is_string", true}), nil
	case TokTRUE, TokFALSE:
		p.advance()
		return New("Boolean", Arg{"this", c.Type == TokTRUE}), nil
	case TokNULL:
		p.advance()
		return New("Null"), nil
	case TokSTAR:
		p.advance()
		return newStar(), nil
	case TokINTERVAL:
		return nil, p.unsupported("INTERVAL")
	case TokL_PAREN:
		if n := p.next(); n != nil && (n.Type == TokSELECT || n.Type == TokWITH) {
			return p.parseScalarSubquery()
		}
		p.advance()
		inner, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		if !p.match(TokR_PAREN) {
			return nil, p.unsupported("unclosed parenthesis")
		}
		return New("Paren", Arg{"this", inner}), nil
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
		empty := p.namesAFunctionCall() && p.atEmptyArgList()
		if noParen && c.Type != TokCASE && !empty && (!hasSpec || !p.namesAFunctionCall()) {
			return nil, p.unsupported("no-paren function " + strings.ToUpper(c.Text))
		}
		if n := p.next(); n != nil && n.Type == TokL_PAREN {
			if _, canName := p.tables.FuncTokens[c.Type]; !canName {
				return nil, p.unsupported("call named by a token that cannot name one")
			}
			return p.parseFunction()
		}
		return p.parseColumn()
	}
	return nil, p.unsupported("expression")
}

// newStar builds a bare `*`. The reference constructs it with four modifier
// args, all empty; they dump as nothing but are kept so the shape is the
// reference's rather than a lookalike.
func newStar() *Expression {
	return New("Star", Arg{"ilike", nil}, Arg{"except_", nil}, Arg{"replace", nil}, Arg{"rename", nil})
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
		return nil, p.unsupported("CAST without AS")
	}
	to, err := p.parseDataType()
	if err != nil {
		return nil, err
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed CAST")
	}

	class := "Cast"
	args := []Arg{
		{"this", this}, {"to", to}, {"format", nil},
		{"safe", nil}, {"action", nil}, {"default", nil},
	}
	if try {
		// TRY_CAST is a Cast that is flagged safe, not a silent variant.
		class = "TryCast"
		args[3] = Arg{"safe", true}
		args = append(args, Arg{"requires_string", nil})
	}
	cast := New(class, args...)
	cast.Type = to
	return cast, nil
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
	return p.parseArraySuffix(dt)
}

func (p *parser) parseBaseDataType() (*Expression, error) {
	c := p.curr()
	if c == nil {
		return nil, p.unsupported("type")
	}
	kind, ok := p.tables.TypeTokens[c.Type]
	if !ok {
		return nil, p.unsupported("type " + c.Text)
	}
	// INTERVAL as a type carries a UNIT, and the DataType's `this` is an
	// Interval node rather than a type name -- `CAST(x AS INTERVAL DAY)` is
	// DataType(Interval(unit=Var(DAY))). A word that is not a unit means
	// there is no unit: `CAST(x AS INTERVAL)` is a bare interval type.
	if c.Type == TokINTERVAL {
		p.advance()
		return p.parseIntervalType(), nil
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
				param, err = p.parseDataType()
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
		// A dialect may discard them: DuckDB reads every text type as TEXT
		// and drops the length, so `VARCHAR(5)` is a bare TEXT. Keeping the 5
		// sent the engine a different CAST than the Python executor sent. Only
		// sizes are dropped -- a nested type's members are the type.
		if nested || !p.tables.DropsTypeParams[kind] {
			dt.Set("expressions", params)
		}
	} else if defaults := p.tables.DefaultTypeParams[kind]; len(defaults) > 0 {
		// A bare type that this dialect reads as parameterised. DuckDB's
		// `numeric` is DECIMAL(18, 3), and leaving it bare sent the engine a
		// different CAST from the one the Python executor sends -- on a
		// division, a different number rather than a different spelling.
		params := make([]*Expression, 0, len(defaults))
		for _, v := range defaults {
			params = append(params, New("DataTypeParam",
				Arg{"this", New("Literal", Arg{"this", v}, Arg{"is_string", false})}))
		}
		dt.Set("expressions", params)
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
	// `DAY TO HOUR` is a span, which is one unit made of two.
	if p.atWords("TO") {
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
		return New("DataTypeParam", Arg{"this", lit}), nil
	case c.Type == TokVAR, p.atIdentifier() && c.Type != TokIDENTIFIER:
		// A bare word, not a quoted one: `VARCHAR(MAX)` is a Var, and the
		// reference UPPER-CASES it, so `varchar(max)` and `VARCHAR(MAX)` are
		// the same node. A quoted `"max"` is not the same thing and is refused.
		p.advance()
		v := New("Var", Arg{"this", strings.ToUpper(c.Text)})
		return New("DataTypeParam", Arg{"this", v}), nil
	}
	return nil, p.unsupported("non-numeric type parameter")
}

// parseStructField reads one named field of a STRUCT-like type. The colon is
// optional: Databricks writes `a: INT` and DuckDB `a INT`, and both arrive as
// the same ColumnDef.
func (p *parser) parseStructField() (*Expression, error) {
	name, err := p.parseIdentifier()
	if err != nil {
		return nil, err
	}
	p.match(TokCOLON)
	kind, err := p.parseDataType()
	if err != nil {
		return nil, err
	}
	return New("ColumnDef", Arg{"this", name}, Arg{"kind", kind}), nil
}

// parseArraySuffix wraps a type in as many ARRAY layers as it has bracket
// pairs. `INT[3]` is a fixed-size array only where the dialect has them; where
// it does not, the reference RETREATS and reads the brackets as a subscript of
// the cast, so the loop stops without consuming them rather than building an
// array the reference never builds.
func (p *parser) parseArraySuffix(dt *Expression) (*Expression, error) {
	for p.at(TokL_BRACKET) {
		var values []*Expression
		if p.atPair(TokL_BRACKET, TokR_BRACKET) {
			p.advance()
			p.advance()
		} else {
			if !p.tables.SupportsFixedSizeArrays {
				return dt, nil
			}
			size := p.next()
			if size == nil || size.Type != TokNUMBER {
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
		args := []Arg{
			{"this", DataTypeKind("ARRAY")},
			{"expressions", []*Expression{dt}},
		}
		if values != nil {
			args = append(args, Arg{"values", values})
		}
		args = append(args, Arg{"nested", true})
		dt = New("DataType", args...)
	}
	return dt, nil
}

// parseScalarSubquery reads a parenthesised query used where a value goes.
// It keeps a `pivots` slot the FROM-clause form does not, which is the
// reference's shape rather than a simplification of it.
func (p *parser) parseScalarSubquery() (*Expression, error) {
	p.advance() // the opening parenthesis
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
func (p *parser) parseFunction() (*Expression, error) {
	name := p.curr().Text
	quotedName := p.curr().Type == TokIDENTIFIER
	upper := strings.ToUpper(name)
	if upper == "CAST" || upper == "TRY_CAST" {
		return p.parseCast(upper == "TRY_CAST")
	}
	if _, syntax := p.tables.SyntaxFunctions[upper]; syntax {
		return p.parseSyntaxFunction(upper)
	}
	spec, named := p.tables.Functions[upper]
	// A name whose SHAPE depends on how many arguments it is given -- DATEDIFF
	// of two is not DATEDIFF of three -- has one spec per count instead, and
	// which one applies cannot be known until the arguments are read.
	variants, byArity := p.tables.FunctionsByArity[upper]
	if !named && !byArity {
		if _, custom := p.tables.NamedFunctions[upper]; custom {
			return nil, p.unsupported("function " + upper + " with a builder of its own")
		}
	}
	p.advance()
	p.advance() // the opening parenthesis

	var args []*Expression
	// `COUNT(DISTINCT a)` is a Count over a Distinct, not a Count of two
	// things: the reference collects everything after DISTINCT into one
	// Distinct node and passes that as the call's single argument. Refusing
	// it turned away one of the commonest aggregates a data agent writes.
	distinct := p.match(TokDISTINCT)
	var order *Expression
	wasInCallArgs := p.inCallArgs
	p.inCallArgs = true
	if !p.at(TokR_PAREN) {
		for {
			// ORDER BY and named arguments inside a call also change the node
			// the reference builds; neither is handled here.
			if p.atAny(TokDISTINCT, TokORDER_BY, TokALL) {
				return nil, p.unsupported("modifier inside a function call")
			}
			// `x -> x > 1` is a lambda ONLY here, in argument position. The
			// same `->` between two ordinary expressions is JSON extraction,
			// and reading `data -> '$.value'` as a lambda made a Lambda out of
			// a JSON path.
			var arg *Expression
			var err error
			switch {
			case p.atLambda():
				arg, err = p.parseLambda()
			case p.at(TokSELECT), p.at(TokWITH):
				// `EXISTS(SELECT 1)`: the call's own parentheses are the
				// subquery's, so the argument is the Select ITSELF -- there
				// is no Subquery wrapper the way there is after `IN`.
				arg, err = p.parseQuery()
			default:
				arg, err = p.parseExpression()
			}
			if err != nil {
				return nil, err
			}
			args = append(args, arg)
			// `ARRAY_AGG(x ORDER BY y)`: the ORDER BY belongs to the argument
			// it follows, and the reference wraps that argument in an Order
			// rather than hanging the clause off the call. Where the whole
			// list was collected into a Distinct, the Order wraps THAT, which
			// is why it is applied below rather than here.
			if p.at(TokORDER_BY) {
				p.advance()
				o, oerr := p.parseOrder()
				if oerr != nil {
					return nil, oerr
				}
				order = o
			}
			if !p.match(TokCOMMA) {
				break
			}
		}
	}
	p.inCallArgs = wasInCallArgs
	// `SUM(x IGNORE NULLS)` puts the modifier INSIDE the parentheses and the
	// reference still wraps the whole call in it.
	inner := p.atNullsModifier()
	if inner != "" {
		p.advance()
		p.advance()
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed function argument list")
	}
	if distinct {
		args = []*Expression{New("Distinct", Arg{"expressions", args}, Arg{"on", nil})}
	}
	if order != nil {
		if len(args) == 0 {
			return nil, p.unsupported("ORDER BY without an argument to order")
		}
		order.Set("this", args[len(args)-1])
		args[len(args)-1] = order
	}
	if !named && byArity {
		byCount, ok := variants[len(args)]
		if !ok {
			return nil, p.unsupported("function " + upper + " with this many arguments")
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
		// statement means, so this is a refusal.
		if n, bounded := spec.consumes(); bounded && len(args) > n {
			return nil, p.unsupported("extra arguments to " + name)
		}
		// A wrap takes the argument's NAME, so an argument with no name -- a
		// cast, a subquery -- is one the reference does not name either: it
		// keeps the node instead, which is a different tree. Refused here
		// rather than built from an empty name.
		for _, a := range spec.Args {
			if a.Wrap == "" || a.Index >= len(args) {
				continue
			}
			if args[a.Index].Name() == "" {
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
		for _, i := range p.tables.StringSensitiveArgs[strings.ToUpper(name)] {
			if isTimeFormatArg(formatArgs, i) {
				continue
			}
			if i < len(args) && isStringLiteral(args[i]) {
				return nil, p.unsupported("string argument to " + name)
			}
		}
		// The argument's own CLASS can change what is built: LOWER(HEX(x)) is
		// a LowerHex, LOWER(x) is a Lower. The spec was probed with a
		// placeholder column and describes only the second, so a call that
		// nests one of the listed classes is refused rather than built as the
		// tree the reference never makes.
		for _, t := range p.tables.ClassSensitiveArgs[strings.ToUpper(name)] {
			if t.Index >= len(args) || args[t.Index] == nil {
				continue
			}
			for _, c := range t.Classes {
				if args[t.Index].Class == c {
					return nil, p.unsupported(c + " argument to " + name)
				}
			}
		}
		built := p.buildFunction(upper, spec, args)
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
	return New("Anonymous", Arg{"this", this}, Arg{"expressions", args}), nil
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
	n := 0
	for _, a := range spec.Args {
		if a.VarLen {
			return 0, false
		}
		if a.Index >= n {
			n = a.Index + 1
		}
	}
	return n, true
}

func (p *parser) buildFunction(name string, spec FuncSpec, args []*Expression) *Expression {
	node := New(spec.Class)
	for _, a := range spec.Args {
		switch {
		case a.Wrap != "":
			// Built FROM the argument, not holding it: DATEADD's unit is
			// Var(args[i].name upper-cased), and the argument node itself does
			// not appear in the result at all.
			if a.Index < len(args) {
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
			if a.Index < len(args) {
				node.Set(a.Key, args[a.Index])
			} else {
				node.Set(a.Key, nil)
			}
		default:
			node.Set(a.Key, a.Const)
		}
	}
	return node
}

// parseColumn reads a possibly-qualified column reference: name, table.name,
// db.table.name, catalog.db.table.name, and the `t.*` form.
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
		// After a dot, `null` and `true` are names, not values -- and the
		// reference builds a bare Identifier for them, with no `quoted` arg
		// at all rather than one set false.
		if c := p.curr(); c != nil && (c.Type == TokNULL || c.Type == TokTRUE || c.Type == TokFALSE) {
			p.advance()
			parts = append(parts, New("Identifier", Arg{"this", c.Text}))
			continue
		}
		id, err := p.parseIdentifier()
		if err != nil {
			return nil, err
		}
		if p.at(TokL_PAREN) {
			return nil, p.unsupported("qualified function call")
		}
		parts = append(parts, id)
	}

	if star {
		// t.* keeps all four qualifier slots, as the reference builds it.
		col := New("Column", Arg{"this", newStar()})
		names := []string{"table", "db", "catalog"}
		for i := len(parts) - 1; i >= 0; i-- {
			slot := len(parts) - 1 - i
			if slot >= len(names) {
				return nil, p.unsupported("over-qualified column")
			}
			col.Set(names[slot], parts[i])
		}
		for _, n := range names {
			if _, ok := col.Args[n]; !ok {
				col.Set(n, nil)
			}
		}
		return col, nil
	}

	// The last part is the column; the ones before it qualify it, nearest first.
	col := New("Column", Arg{"this", parts[len(parts)-1]})
	names := []string{"table", "db", "catalog"}
	for i := len(parts) - 2; i >= 0; i-- {
		slot := len(parts) - 2 - i
		if slot >= len(names) {
			return nil, p.unsupported("over-qualified column")
		}
		col.Set(names[slot], parts[i])
	}
	return col, nil
}

func (p *parser) parseIdentifier() (*Expression, error) {
	if !p.atIdentifier() {
		return nil, p.unsupported("identifier")
	}
	c := p.curr()
	// T-SQL strips a leading # from some identifiers and not others -- the
	// temp-table rule. Refuse rather than reproduce half of it.
	if p.dialect == "tsql" && strings.Contains(c.Text, "#") {
		return nil, p.unsupported("T-SQL temp-table identifier")
	}
	if c.Type == TokIDENTIFIER {
		p.advance()
		return New("Identifier", Arg{"this", c.Text}, Arg{"quoted", true}), nil
	}
	p.advance()
	// The token's text, not its upper-cased keyword spelling: a keyword used
	// as a name keeps the case it was written in, and the tokenizer only
	// upper-cases the keywords it finds through the trie.
	return New("Identifier", Arg{"this", c.Text}, Arg{"quoted", false}), nil
}

// atIdentifier reports whether the current token can stand in for a name --
// a plain word, a quoted identifier, or one of the many keywords the reference
// still allows as an identifier.
func (p *parser) atIdentifier() bool {
	c := p.curr()
	if c == nil {
		return false
	}
	if c.Type == TokVAR || c.Type == TokIDENTIFIER {
		return true
	}
	// A no-paren function -- CURRENT_DATE and friends -- is a call, not a
	// name, even though it looks like a bare word.
	if _, isFunc := p.tables.NoParenFunctions[c.Type]; isFunc {
		return false
	}
	_, ok := p.tables.IDVarTokens[c.Type]
	return ok
}

// afterComparison reports whether the token just consumed was a comparison or
// a LIKE. `ALL` and `ANY` are quantifiers only THERE: in a select list,
// `ALL (age >= 30) AS every` is an ordinary call to a function named ALL, and
// reading it as a quantifier built a node the reference never makes.
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

// parseJSONPathOperand reads the right-hand side of `->`. It must be a string
// literal: that is the only form the path grammar can read, and a column or
// expression there is a construct the port does not model.
func (p *parser) parseJSONPathOperand() (*Expression, error) {
	c := p.curr()
	if c == nil || c.Type != TokSTRING {
		return nil, p.unsupported("JSON path that is not a literal")
	}
	p.advance()
	if !p.tables.JSONPathIsParsed {
		// PostgreSQL keeps the whole string as one key.
		return New("JSONPath", Arg{"expressions", []*Expression{
			New("JSONPathRoot"),
			New("JSONPathKey", Arg{"this", c.Text}),
		}}), nil
	}
	path, err := parseJSONPath(c.Text)
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
		return New("Literal", Arg{"this", c.Text}, Arg{"is_string", true}), nil
	}
	return path, nil
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
func (p *parser) atWords(words ...string) bool {
	if p.index+len(words) > len(p.tokens) {
		return false
	}
	for i, w := range words {
		if !strings.EqualFold(p.tokens[p.index+i].Text, w) {
			return false
		}
	}
	return true
}

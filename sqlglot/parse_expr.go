package sqlglot

import (
	"strconv"
	"strings"
)

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
		// `f(name := value)` is a NAMED ARGUMENT, and the reference records
		// the name as a bare identifier -- the same PropertyEQ a struct field
		// uses. Only in argument position: `:=` anywhere else is an
		// assignment, which is a statement rather than an expression.
		if !p.inCallArgs {
			return nil, p.unsupported("assignment")
		}
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
	// The arrow lives INSIDE the bitwise level rather than above it -- see
	// parseBitwise, where it is one more case in the same loop.
	return p.parseBitwise()
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
		case c.Type == TokARROW || c.Type == TokDARROW:
			// The JSON arrow is one of these operators, not a level of its
			// own, and the asymmetry is what proves it: `a & b -> c` reads as
			// `(a & b) -> c` while `a -> b & c` reads as `(a -> b) & c`. Only
			// a left-associative loop shared with the bitwise operators does
			// both. Its right-hand side is a TERM -- arithmetic binds tighter
			// (`a -> b + c` keeps `b + c`) and `&` does not.
			class := "JSONExtract"
			if c.Type == TokDARROW {
				class = "JSONExtractScalar"
			}
			p.advance()
			operand, err := p.parseTerm()
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
			this = New(class, args...)
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
		// COLLATE names a collation, not an expression: a bare word there is
		// a Var and a quoted one an Identifier, where the generic operand
		// rule would make a column of either.
		if class == "Collate" {
			name, cerr := p.parseCollation()
			if cerr != nil {
				return nil, cerr
			}
			this = New(class, Arg{"this", this}, Arg{"expression", name})
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
				Arg{"requires_json", false})
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
		return p.starModifiers(newStar())
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

// starModifiers reads what may follow a `*`: `EXCEPT (a, b)` drops columns and
// `REPLACE (a AS b)` swaps them. Both are lists on the Star itself rather than
// anything wrapping it, so `SELECT * EXCEPT (a)` is one projection.
//
// EXCEPT is also a set operation, which is why the port used to read this as
// one and refuse the statement for having no SELECT after it.
func (p *parser) starModifiers(star *Expression) (*Expression, error) {
	for {
		var key string
		switch {
		case p.at(TokEXCEPT):
			key = "except_"
		case p.atWords("REPLACE"):
			key = "replace"
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
	if !named && !byArity && !isJSONPath {
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
			// Index -1 marks a CONSTANT node rather than a wrapper: it takes
			// no argument, so there is no name for it to want.
			if a.Wrap == "" || a.Index < 0 || a.Index >= len(args) {
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
		// `a.b.C()` is a CALL under a chain of dots, not a column: the
		// reference builds Dot(Dot(a, b), C()). Detected before the name is
		// consumed, so the call parser sees it where it expects to.
		if p.namesAFunctionCall() {
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
		// t.* keeps all four qualifier slots, as the reference builds it.
		// `t.* EXCEPT (a)` carries the modifiers on the STAR inside the
		// column, the same ones a bare `*` takes.
		qualified, err := p.starModifiers(newStar())
		if err != nil {
			return nil, err
		}
		col := New("Column", Arg{"this", qualified})
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
		return arg
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
	path, err := parseJSONPath(text)
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
		// Every argument has to be a string literal for the fold to happen.
		// Handed anything else the reference cannot transpile it and lays the
		// arguments out positionally instead -- a different tree, so the port
		// refuses rather than folding something it was not given.
		for _, arg := range args[1:] {
			if !isStringLiteral(arg) {
				return nil
			}
		}
		path = p.foldJSONPath(args[1:], spec)
	case len(args) == 1:
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
		if isStringLiteral(args[1]) {
			text, _ := args[1].Args["this"].(string)
			parsed, err := parseJSONPath(text)
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
		if p.match(TokDOT) {
			continue
		}
		if p.at(TokL_BRACKET) {
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

// parseCollation reads what follows COLLATE. A string literal stays a literal,
// a QUOTED name is an Identifier, and a bare word is a Var -- three shapes for
// one slot, and the generic operand rule made a column of the last two.
func (p *parser) parseCollation() (*Expression, error) {
	c := p.curr()
	if c == nil {
		return nil, p.unsupported("COLLATE without a collation")
	}
	switch {
	case c.Type == TokSTRING:
		p.advance()
		return New("Literal", Arg{"this", c.Text}, Arg{"is_string", true}), nil
	case c.Type == TokIDENTIFIER:
		p.advance()
		return New("Identifier", Arg{"this", c.Text}, Arg{"quoted", true}), nil
	case p.atIdentifier():
		p.advance()
		return New("Var", Arg{"this", c.Text}), nil
	}
	return nil, p.unsupported("COLLATE without a collation")
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
			if p.atLambda() {
				arg, err = p.parseLambda()
			} else {
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

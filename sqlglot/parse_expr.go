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

// binaryRangeOps are the range operators that take a single right operand.
var binaryRangeOps = map[TokenType]string{
	TokLIKE:       "Like",
	TokILIKE:      "ILike",
	TokRLIKE:      "RegexpLike",
	TokGLOB:       "Glob",
	TokSIMILAR_TO: "SimilarTo",
}

// parseRange handles IS, IN, BETWEEN and the LIKE family, including their
// negated forms.
//
// NOT is read here rather than at the unary level because `a NOT LIKE b` sets
// a flag on the Like node while `a NOT IN (…)` wraps the In in a Not -- the
// reference treats the two differently and so must this. A NOT that turns out
// not to introduce a range is put back.
func (p *parser) parseRange() (*Expression, error) {
	this, err := p.parseBitwise()
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
		case binaryRangeOps[c.Type] != "":
			class := binaryRangeOps[c.Type]
			p.advance()
			var right *Expression
			right, err = p.parseBitwise()
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

		if p.at(TokESCAPE) {
			return nil, p.unsupported("ESCAPE")
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
	var expression *Expression
	if p.match(TokNULL) {
		expression = New("Null")
	} else {
		var err error
		expression, err = p.parseBitwise()
		if err != nil {
			return nil, err
		}
	}
	// IS NOT NULL records the negation on the Is node; every other negated IS
	// wraps in a Not.
	if negate && expression.Class == "Null" {
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
	if p.at(TokSELECT) {
		return nil, p.unsupported("IN with a subquery")
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
	// A single query inside the parentheses is an IN over a subquery, which
	// the reference records under `query` rather than in the expression list.
	if len(items) == 1 && items[0].Class == "Subquery" {
		return nil, p.unsupported("IN with a subquery")
	}
	return New("In", Arg{"this", this}, Arg{"expressions", items}), nil
}

func (p *parser) parseBetween(this *Expression) (*Expression, error) {
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
	for p.match(TokDCOLON) {
		to, err := p.parseDataType()
		if err != nil {
			return nil, err
		}
		cast := New("Cast", Arg{"this", this}, Arg{"to", to})
		cast.Type = to
		this = cast
	}
	return this, nil
}

// parsePrimary is entered with a token current; parseUnary checked.
func (p *parser) parsePrimary() (*Expression, error) {
	c := p.curr()

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
		if _, noParen := p.tables.NoParenFunctionNames[strings.ToUpper(c.Text)]; noParen && c.Type != TokCASE {
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

// parseDataType reads a type name and its parameters. Only the flat forms are
// handled: a composite type -- ARRAY<INT>, STRUCT<…> -- nests, and the
// reference records the nesting in ways worth porting deliberately.
func (p *parser) parseDataType() (*Expression, error) {
	c := p.curr()
	if c == nil {
		return nil, p.unsupported("type")
	}
	kind, ok := p.tables.TypeTokens[c.Type]
	if !ok {
		return nil, p.unsupported("type " + c.Text)
	}
	// INTERVAL as a type carries a unit and is a different node entirely.
	if c.Type == TokINTERVAL {
		return nil, p.unsupported("INTERVAL type")
	}
	p.advance()
	if p.at(TokLT) {
		return nil, p.unsupported("parameterised composite type")
	}

	dt := New("DataType", Arg{"this", DataTypeKind(kind)})
	if p.match(TokL_PAREN) {
		var params []*Expression
		for {
			// A type parameter is a number here. A word -- VARCHAR(MAX) --
			// becomes a Var in the reference, not the Column an expression
			// would produce, so it is refused rather than mis-built.
			c := p.curr()
			if c == nil || c.Type != TokNUMBER {
				return nil, p.unsupported("non-numeric type parameter")
			}
			p.advance()
			lit := New("Literal", Arg{"this", c.Text}, Arg{"is_string", false})
			params = append(params, New("DataTypeParam", Arg{"this", lit}))
			if !p.match(TokCOMMA) {
				break
			}
		}
		if !p.match(TokR_PAREN) {
			return nil, p.unsupported("unclosed type parameters")
		}
		dt.Set("expressions", params)
	}
	dt.Set("nested", false)
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
	spec, named := p.tables.Functions[upper]
	if !named {
		if _, custom := p.tables.NamedFunctions[upper]; custom {
			return nil, p.unsupported("function " + upper + " with a builder of its own")
		}
	}
	p.advance()
	p.advance() // the opening parenthesis

	var args []*Expression
	if !p.at(TokR_PAREN) {
		for {
			// DISTINCT, ORDER BY and named arguments inside a call all change
			// the node the reference builds; none is handled here.
			if p.atAny(TokDISTINCT, TokORDER_BY, TokALL) {
				return nil, p.unsupported("modifier inside a function call")
			}
			arg, err := p.parseExpression()
			if err != nil {
				return nil, err
			}
			args = append(args, arg)
			if !p.match(TokCOMMA) {
				break
			}
		}
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed function argument list")
	}
	if named {
		return buildFunction(spec, args), nil
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
func buildFunction(spec FuncSpec, args []*Expression) *Expression {
	node := New(spec.Class)
	for _, a := range spec.Args {
		switch {
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

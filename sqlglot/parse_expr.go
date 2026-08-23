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

// parseRange is where IN, BETWEEN, LIKE and IS live in the reference. None is
// handled yet, so the statement simply ends with tokens left over and is
// refused -- which is the same outcome as refusing here, and one fewer place
// for the two to disagree.
func (p *parser) parseRange() (*Expression, error) { return p.parseBitwise() }

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
	return p.parsePrimary()
}

// parsePrimary is entered with a token current; parseUnary checked.
func (p *parser) parsePrimary() (*Expression, error) {
	c := p.curr()

	switch c.Type {
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
		if _, noParen := p.tables.NoParenFunctionNames[strings.ToUpper(c.Text)]; noParen {
			return nil, p.unsupported("no-paren function " + strings.ToUpper(c.Text))
		}
		if n := p.peek(1); n != nil && n.Type == TokL_PAREN {
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

// parseFunction reads a call with an argument list.
//
// Only the Anonymous form is built. The reference gives hundreds of names a
// node class of their own -- COUNT is a Count with a big_int flag, not a call
// named "COUNT" -- and each has its own argument shape. Producing Anonymous for
// one of those would be a divergence, so a name the reference knows is refused
// until its node is ported. The list is generated, not guessed.
func (p *parser) parseFunction() (*Expression, error) {
	name := p.curr().Text
	upper := strings.ToUpper(name)
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
	return New("Anonymous", Arg{"this", name}, Arg{"expressions", args}), nil
}

// buildFunction maps positional arguments onto a function node's declared
// argument keys, which is what the reference's Func.from_arg_list does: keys
// and arguments are zipped, so a call with fewer arguments than keys simply
// leaves the rest unset. With a variadic last key, everything past the fixed
// keys collects into it.
func buildFunction(spec FuncSpec, args []*Expression) *Expression {
	node := New(spec.Class)
	keys := spec.Keys
	if spec.VarLen {
		fixed := keys[:len(keys)-1]
		for i, k := range fixed {
			if i >= len(args) {
				break
			}
			node.Set(k, args[i])
		}
		rest := []*Expression{}
		if len(args) > len(fixed) {
			rest = args[len(fixed):]
		}
		node.Set(keys[len(keys)-1], rest)
		return node
	}
	for i, k := range keys {
		if i >= len(args) {
			break
		}
		node.Set(k, args[i])
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

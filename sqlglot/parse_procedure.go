package sqlglot

import "strings"

// parseProcedureRest reads everything after `CREATE PROCEDURE`.
//
// A procedure's name carries its PARAMETERS, in one of three shapes: a
// parenthesised list, which every dialect reads as a UserDefinedFunction; a
// bare list, which T-SQL alone allows; and no list at all. What a bare name is
// wrapped in is the dialect's own -- T-SQL puts a StoredProcedure round it and
// the rest leave the name alone -- and is probed rather than named here.
func (p *parser) parseProcedureRest(table *Expression, kind string, replace, exists bool) (*Expression, error) {
	this := table
	var params []*Expression
	wrapped := false
	if p.at(TokL_PAREN) {
		var err error
		params, err = p.parseFunctionParams()
		if err != nil {
			return nil, err
		}
		wrapped = true
		udf := New("UserDefinedFunction", Arg{"this", table})
		if len(params) > 0 {
			udf.Set("expressions", params)
		}
		udf.Set("wrapped", true)
		this = udf
	} else if p.tables.BareProcedureWrapper != "" && !p.at(TokALIAS) && !p.at(TokWITH) &&
		p.curr() != nil {
		// `CREATE PROCEDURE foo @a INT, @b INT AS ...` -- the parameters
		// stand after the name with nothing round them. Read only where the
		// dialect has a wrapper for a bare name, because it is the same
		// dialect that allows the form.
		// Read from a token list that STOPS at the AS opening the body. A
		// parenthesised list ends at its own bracket and this one has none,
		// so without the cut the constraint reader takes the AS for
		// something it cannot read.
		whole := p.tokens
		p.tokens = whole[:p.endOfBareParameters()]
		for {
			param, err := p.parseFunctionParam()
			if err != nil {
				p.tokens = whole
				return nil, err
			}
			params = append(params, param)
			if !p.match(TokCOMMA) {
				break
			}
		}
		p.tokens = whole
	}
	if !wrapped && p.tables.BareProcedureWrapper != "" {
		stored := New(p.tables.BareProcedureWrapper, Arg{"this", table})
		if len(params) > 0 {
			stored.Set("expressions", params)
		}
		this = stored
	}

	var properties []*Expression
	if p.at(TokWITH) {
		read, err := p.parseProcedureOptions()
		if err != nil {
			return nil, err
		}
		properties = read
	}

	// A procedure always carries a Block, even where nothing follows its
	// name: the reference reads the body unconditionally and gets a block
	// holding one empty statement, which it records as a null.
	expression := New("Block", Arg{"expressions", []*Expression{nil}})
	begin := false
	if p.match(TokALIAS) {
		begin = p.match(TokBEGIN)
		// A body written as a STRING stays a string: the reference reads
		// `AS 'DECLARE BEGIN; END'` as a literal and does not look inside it.
		if c := p.curr(); c != nil && c.Type == TokSTRING {
			p.advance()
			expression = New("Literal", Arg{"this", c.Text}, Arg{"is_string", true})
		} else {
			body, err := p.parseProcedureBody()
			if err != nil {
				return nil, err
			}
			expression = body
		}
	}
	if p.curr() != nil {
		return nil, p.unsupported("CREATE PROCEDURE with more than this port reads")
	}
	return New("Create",
		Arg{"this", this},
		Arg{"kind", kind},
		Arg{"replace", replace},
		Arg{"refresh", false},
		Arg{"unique", false},
		Arg{"expression", expression},
		Arg{"exists", exists},
		Arg{"properties", propertiesOf(properties)},
		Arg{"indexes", []*Expression{}},
		Arg{"no_schema_binding", nil},
		Arg{"begin", begin},
		Arg{"end_", nil},
		Arg{"clone", nil},
		Arg{"concurrently", false},
		Arg{"clustered", nil},
	), nil
}

// endOfBareParameters finds where an unparenthesised parameter list ends: the
// AS that opens the body, or the end of the statement.
func (p *parser) endOfBareParameters() int {
	depth := 0
	for i := p.index; i < len(p.tokens); i++ {
		switch p.tokens[i].Type {
		case TokL_PAREN:
			depth++
		case TokR_PAREN:
			depth--
		case TokALIAS:
			if depth == 0 {
				return i
			}
		}
	}
	return len(p.tokens)
}

// parseProcedureOptions reads the words after WITH. Two of them the reference
// reads as a view ATTRIBUTE, one property each; the rest go together into a
// single WithProcedureOptions. Which word is which is generated, because
// nothing in the words themselves says so -- and a LIST that begins with an
// attribute the reference does not read at all, so nor does this.
func (p *parser) parseProcedureOptions() ([]*Expression, error) {
	p.advance() // WITH
	c := p.curr()
	if c == nil {
		return nil, p.unsupported("WITH nothing after it")
	}
	word := strings.ToUpper(c.Text)
	if p.tables.ProcedureWithOptions[word] == "ViewAttributeProperty" {
		p.advance()
		if p.at(TokCOMMA) {
			return nil, p.unsupported("a list of procedure options beginning with " + word)
		}
		return []*Expression{New("ViewAttributeProperty", Arg{"this", word})}, nil
	}
	var options []*Expression
	for {
		option, err := p.parseProcedureOption()
		if err != nil {
			return nil, err
		}
		options = append(options, option)
		if !p.match(TokCOMMA) {
			break
		}
	}
	return []*Expression{New("WithProcedureOptions", Arg{"expressions", options})}, nil
}

// parseProcedureOption reads one word of a WITH list. `EXECUTE AS <who>` is a
// property of its own; every other word is kept as the bare Var it was
// written as.
func (p *parser) parseProcedureOption() (*Expression, error) {
	c := p.curr()
	if c == nil {
		return nil, p.unsupported("a procedure option that is not there")
	}
	word := strings.ToUpper(c.Text)
	if _, known := p.tables.ProcedureWithOptions[word]; !known && word != "EXECUTE" {
		return nil, p.unsupported("procedure option " + word)
	}
	p.advance()
	if word != "EXECUTE" {
		return New("Var", Arg{"this", word}), nil
	}
	if !p.match(TokALIAS) {
		return nil, p.unsupported("EXECUTE without AS")
	}
	who := p.curr()
	if who == nil {
		return nil, p.unsupported("EXECUTE AS nobody")
	}
	p.advance()
	if who.Type == TokSTRING {
		return New("ExecuteAsProperty",
			Arg{"this", New("Literal", Arg{"this", who.Text}, Arg{"is_string", true})}), nil
	}
	return New("ExecuteAsProperty",
		Arg{"this", New("Var", Arg{"this", strings.ToUpper(who.Text)})}), nil
}

// propertiesOf wraps a list of properties, or nothing where there are none.
func propertiesOf(items []*Expression) *Expression {
	if len(items) == 0 {
		return nil
	}
	return New("Properties", Arg{"expressions", items})
}

// parseIfStatement reads `IF <condition> <block> [ELSE <block>]`, which in one
// dialect is a STATEMENT rather than a call or a name.
//
// The condition is an ordinary expression, optionally parenthesised -- and
// ordinary includes an implicit ALIAS, which is how the reference reads
// `IF NOT EXISTS (...) EXEC('...')`: the EXEC becomes the condition's alias
// and what follows it becomes the block. That is the reference being odd
// rather than this port being careless, and the corpus records what it made.
func (p *parser) parseIfStatement() (*Expression, error) {
	p.advance() // IF
	condition, err := p.parseWrappedCondition()
	if err != nil {
		return nil, err
	}
	p.match(TokBEGIN)
	block, err := p.parseProcedureBody()
	if err != nil {
		return nil, err
	}
	var otherwise any = false
	if p.match(TokELSE) {
		p.match(TokBEGIN)
		other, err := p.parseProcedureBody()
		if err != nil {
			return nil, err
		}
		otherwise = other
	}
	return New("IfBlock",
		Arg{"this", condition}, Arg{"true", block}, Arg{"false", otherwise}), nil
}

// parseWrappedCondition reads an expression that may or may not be wrapped in
// parentheses. Where it is, the parentheses belong to the SYNTAX and leave no
// Paren in the tree -- so what is inside them has to be the whole condition.
func (p *parser) parseWrappedCondition() (*Expression, error) {
	if !p.at(TokL_PAREN) {
		return p.parseAliasedExpression()
	}
	p.advance()
	inner, err := p.parseAliasedExpression()
	if err != nil {
		return nil, err
	}
	// The parentheses are the SYNTAX's, and what is inside them is the whole
	// condition. `IF (a) = (b)` is refused rather than read as a comparison,
	// which is what the reference does with it too.
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("a condition whose parentheses do not close it")
	}
	return inner, nil
}

// parseAliasedExpression reads an expression and whatever names it.
//
// The name may be a KEYWORD here, which it may not be in a projection: the
// reference reads an implicit alias with its ordinary name reader, and that
// takes every word it allows as an identifier. Widening the projection's
// reader to match cost thirty statements their trees, so the wider rule is
// kept where the reference's own path actually reaches it.
func (p *parser) parseAliasedExpression() (*Expression, error) {
	this, err := p.parseExpression()
	if err != nil {
		return nil, err
	}
	if aliased, err := p.parseAlias(this); err != nil || aliased != this {
		return aliased, err
	}
	if !p.atIdentifier() {
		return this, nil
	}
	name, err := p.parseIdentifier()
	if err != nil {
		return nil, err
	}
	return New("Alias", Arg{"this", this}, Arg{"alias", name}), nil
}

// parseProcedureBody reads what a procedure DOES: one or more statements,
// optionally wrapped in BEGIN and END. The reference keeps them in a Block,
// and records the END that closed it as a statement of its own.
func (p *parser) parseProcedureBody() (*Expression, error) {
	var statements []*Expression
	for {
		// Each statement is read from a token list that STOPS where it does.
		// Every statement reader refuses what it has not consumed, and inside
		// a block what follows is another statement rather than a mistake --
		// so the list is cut at the separator and put back afterwards.
		end := p.endOfBlockStatement()
		whole := p.tokens
		p.tokens = whole[:end]
		statement, err := p.parseStatement()
		p.tokens = whole
		if err != nil {
			return nil, err
		}
		statements = append(statements, statement)
		if !p.match(TokSEMICOLON) {
			break
		}
		if p.curr() == nil || p.at(TokEND) {
			break
		}
	}
	if p.match(TokEND) {
		statements = append(statements, New("EndStatement"))
	}
	return New("Block", Arg{"expressions", statements}), nil
}

// endOfBlockStatement finds where the statement at the cursor ends: the next
// semicolon or END that is not inside parentheses, or the end of the input.
func (p *parser) endOfBlockStatement() int {
	depth := 0
	for i := p.index; i < len(p.tokens); i++ {
		switch p.tokens[i].Type {
		case TokL_PAREN:
			depth++
		case TokR_PAREN:
			depth--
		case TokSEMICOLON:
			if depth == 0 {
				return i
			}
		case TokEND:
			if depth == 0 {
				return i
			}
		}
	}
	return len(p.tokens)
}

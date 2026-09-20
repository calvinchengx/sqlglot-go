package sqlglot

import "strings"

// Trino's inline SQL routines (`WITH FUNCTION f(x INT) RETURNS INT RETURN x
// SELECT f(1)`), ported from the reference's TrinoParser. The function body is
// a routine statement -- RETURN, or a BEGIN ... END block of DECLARE / SET /
// IF / CASE / WHILE / LOOP / REPEAT / LEAVE / ITERATE -- and the query that
// follows the specification is left for the caller.

// parseFunctionSpecification reads one `FUNCTION <name>(<params>)
// <characteristics> <body>` entry of a WITH clause, positioned after FUNCTION.
func (p *parser) parseFunctionSpecification() (*Expression, error) {
	name, err := p.parseTableName()
	if err != nil {
		return nil, err
	}
	this := name
	if p.at(TokL_PAREN) {
		params, err := p.parseFunctionParams()
		if err != nil {
			return nil, err
		}
		udf := New("UserDefinedFunction", Arg{"this", name})
		if len(params) > 0 {
			udf.Set("expressions", params)
		}
		udf.Set("wrapped", true)
		this = udf
	}

	var characteristics, properties []*Expression
	for {
		if p.match(TokWITH) {
			if !p.match(TokL_PAREN) {
				return nil, p.unsupported("WITH in a function specification without a property list")
			}
			for {
				pair, err := p.parseKeyValueProperty()
				if err != nil {
					return nil, err
				}
				properties = append(properties, pair)
				if !p.match(TokCOMMA) {
					break
				}
			}
			if !p.match(TokR_PAREN) {
				return nil, p.unsupported("unclosed function property list")
			}
			continue
		}
		if p.atWords("NOT", "DETERMINISTIC") {
			p.advance()
			p.advance()
			characteristics = append(characteristics, New("StabilityProperty",
				Arg{"this", New("Literal", Arg{"this", "VOLATILE"}, Arg{"is_string", true})}))
			continue
		}
		if p.atWords("SECURITY") {
			p.advance()
			c := p.curr()
			if c == nil {
				return nil, p.unsupported("SECURITY without DEFINER, INVOKER or NONE")
			}
			word := strings.ToUpper(c.Text)
			if word != "DEFINER" && word != "INVOKER" && word != "NONE" {
				return nil, p.unsupported("SECURITY without DEFINER, INVOKER or NONE")
			}
			p.advance()
			characteristics = append(characteristics, New("SqlSecurityProperty", Arg{"this", word}))
			continue
		}
		if p.atWords("COMMENT") {
			p.advance()
			p.match(TokEQ)
			c := p.curr()
			if c == nil || c.Type != TokSTRING {
				return nil, p.unsupported("COMMENT without a string")
			}
			p.advance()
			characteristics = append(characteristics, New("SchemaCommentProperty",
				Arg{"this", New("Literal", Arg{"this", c.Text}, Arg{"is_string", true})}))
			continue
		}
		property, err := p.parseFunctionProperty()
		if err != nil {
			return nil, err
		}
		if property == nil {
			break
		}
		characteristics = append(characteristics, property)
	}

	body, err := p.parseRoutineStatement()
	if err != nil {
		return nil, err
	}
	spec := New("FunctionSpecification", Arg{"this", this})
	if len(characteristics) > 0 {
		spec.Set("characteristics", New("Properties", Arg{"expressions", characteristics}))
	}
	if len(properties) > 0 {
		spec.Set("properties", New("Properties", Arg{"expressions", properties}))
	}
	spec.Set("expression", body)
	return spec, nil
}

// routineIdentifier reads a label or a LEAVE/ITERATE target. Any word is a
// valid name here, including the keywords the statements themselves start
// with (`set: LOOP`, `LEAVE leave`).
func (p *parser) routineIdentifier() (*Expression, error) {
	c := p.curr()
	if c == nil {
		return nil, p.unsupported("routine label without a name")
	}
	if _, ok := p.tables.IDVarTokens[c.Type]; !ok && c.Type != TokVAR && c.Type != TokIDENTIFIER {
		return nil, p.unsupported("routine label that is not a name")
	}
	p.advance()
	return New("Identifier", Arg{"this", c.Text}, Arg{"quoted", c.Type == TokIDENTIFIER}), nil
}

// matchRoutineText consumes the current token when its text is `word`,
// whatever token type the tokenizer gave it.
func (p *parser) matchRoutineText(word string) bool {
	if c := p.curr(); c != nil && strings.EqualFold(c.Text, word) {
		p.advance()
		return true
	}
	return false
}

// parseRoutineStatements reads statements until one of the terminator words,
// which it consumes and returns.
func (p *parser) parseRoutineStatements(terminators ...string) ([]*Expression, string, error) {
	var out []*Expression
	for {
		c := p.curr()
		if c == nil {
			return nil, "", p.unsupported("routine body that never ends")
		}
		for _, term := range terminators {
			if strings.EqualFold(c.Text, term) {
				p.advance()
				return out, strings.ToUpper(term), nil
			}
		}
		if p.match(TokSEMICOLON) {
			continue
		}
		statement, err := p.parseRoutineStatement()
		if err != nil {
			return nil, "", err
		}
		out = append(out, statement)
	}
}

func (p *parser) routineBlock(terminators ...string) (*Expression, string, error) {
	statements, term, err := p.parseRoutineStatements(terminators...)
	if err != nil {
		return nil, "", err
	}
	return New("Block", Arg{"expressions", statements}), term, nil
}

// endOfSimpleStatement is the next `;` outside parentheses, or the end.
func (p *parser) endOfSimpleStatement() int {
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
		}
	}
	return len(p.tokens)
}

// parseCut runs a statement reader over the tokens up to the next `;`, which
// is how the reference's chunks bound DECLARE and SET.
func (p *parser) parseCut(read func() (*Expression, error)) (*Expression, error) {
	whole := p.tokens
	p.tokens = whole[:p.endOfSimpleStatement()]
	out, err := read()
	p.tokens = whole
	return out, err
}

func (p *parser) parseRoutineStatement() (*Expression, error) {
	var label *Expression
	if n := p.next(); n != nil && n.Type == TokCOLON {
		id, err := p.routineIdentifier()
		if err != nil {
			return nil, err
		}
		label = id
		p.match(TokCOLON)
	}
	switch {
	case p.at(TokBEGIN):
		p.advance()
		statements, _, err := p.parseRoutineStatements("END")
		if err != nil {
			return nil, err
		}
		statements = append(statements, New("EndStatement"))
		return New("Block", Arg{"expressions", statements}, Arg{"begin", true}), nil
	case p.matchRoutineText("RETURN"):
		value, err := p.parseDisjunction()
		if err != nil {
			return nil, err
		}
		return New("Return", Arg{"this", value}), nil
	case p.matchRoutineText("IF"):
		return p.parseRoutineIf()
	case p.matchRoutineText("CASE"):
		return p.parseRoutineCase()
	case p.at(TokDECLARE):
		return p.parseCut(p.parseDeclare)
	case p.at(TokSET):
		return p.parseCut(p.parseSet)
	case p.matchRoutineText("ITERATE"):
		id, err := p.routineIdentifier()
		if err != nil {
			return nil, err
		}
		return New("Iterate", Arg{"this", id}), nil
	case p.matchRoutineText("LEAVE"):
		id, err := p.routineIdentifier()
		if err != nil {
			return nil, err
		}
		return New("Leave", Arg{"this", id}), nil
	case p.matchRoutineText("WHILE"):
		cond, err := p.parseDisjunction()
		if err != nil {
			return nil, err
		}
		p.matchRoutineText("DO")
		body, _, err := p.routineBlock("END")
		if err != nil {
			return nil, err
		}
		p.matchRoutineText("WHILE")
		return New("WhileBlock", Arg{"this", cond}, Arg{"body", body}, Arg{"label", label}), nil
	case p.matchRoutineText("LOOP"):
		body, _, err := p.routineBlock("END")
		if err != nil {
			return nil, err
		}
		p.matchRoutineText("LOOP")
		return New("LoopBlock", Arg{"body", body}, Arg{"label", label}), nil
	case p.matchRoutineText("REPEAT"):
		body, _, err := p.routineBlock("UNTIL")
		if err != nil {
			return nil, err
		}
		until, err := p.parseDisjunction()
		if err != nil {
			return nil, err
		}
		if p.matchRoutineText("END") {
			p.matchRoutineText("REPEAT")
		}
		return New("RepeatBlock", Arg{"body", body}, Arg{"until", until}, Arg{"label", label}), nil
	}
	return nil, p.unsupported("routine statement this port does not read")
}

func (p *parser) parseRoutineBranch(terminators ...string) (cond, body *Expression, term string, err error) {
	cond, err = p.parseDisjunction()
	if err != nil {
		return nil, nil, "", err
	}
	p.matchRoutineText("THEN")
	body, term, err = p.routineBlock(terminators...)
	return cond, body, term, err
}

func (p *parser) parseRoutineIf() (*Expression, error) {
	cond, body, term, err := p.parseRoutineBranch("ELSEIF", "ELSE", "END")
	if err != nil {
		return nil, err
	}
	this := New("IfBlock", Arg{"this", cond}, Arg{"true", body})
	tail := this
	for term == "ELSEIF" {
		cond, body, term, err = p.parseRoutineBranch("ELSEIF", "ELSE", "END")
		if err != nil {
			return nil, err
		}
		node := New("IfBlock", Arg{"this", cond}, Arg{"true", body})
		tail.Set("false", node)
		tail = node
	}
	if term == "ELSE" {
		other, _, err := p.routineBlock("END")
		if err != nil {
			return nil, err
		}
		tail.Set("false", other)
	}
	p.matchRoutineText("IF")
	return this, nil
}

func (p *parser) parseRoutineCase() (*Expression, error) {
	var subject *Expression
	if c := p.curr(); c != nil && !strings.EqualFold(c.Text, "WHEN") {
		s, err := p.parseDisjunction()
		if err != nil {
			return nil, err
		}
		subject = s
	}
	term := ""
	if p.matchRoutineText("WHEN") {
		term = "WHEN"
	}
	var ifs []*Expression
	for term == "WHEN" {
		cond, body, next, err := p.parseRoutineBranch("WHEN", "ELSE", "END")
		if err != nil {
			return nil, err
		}
		ifs = append(ifs, New("If", Arg{"this", cond}, Arg{"true", body}))
		term = next
	}
	var def *Expression
	if term == "ELSE" {
		d, _, err := p.routineBlock("END")
		if err != nil {
			return nil, err
		}
		def = d
	}
	p.matchRoutineText("CASE")
	return New("CaseStatement", Arg{"this", subject}, Arg{"ifs", ifs}, Arg{"default", def}), nil
}

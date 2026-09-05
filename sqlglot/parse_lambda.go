package sqlglot

import "strings"

// atLambda reports whether a lambda starts here: one bare name followed by
// `->`, or a parenthesised name list followed by `->`. The parenthesised form
// needs a scan to the matching `)`, because `(x, y)` is otherwise an ordinary
// parenthesised expression and only the arrow after it tells the difference.
func (p *parser) atLambda() bool {
	c := p.curr()
	if c == nil {
		return false
	}
	// DuckDB spells a lambda twice over -- `x -> x + 1` and the same thing
	// written `LAMBDA x : x + 1`. The word is not a token of its own, so the
	// text decides, and only where the dialect reads the form at all.
	if p.tables.ColonLambdaRead && strings.EqualFold(c.Text, "LAMBDA") {
		return true
	}
	// A NUMBER can name a lambda parameter: the reference reads `0 -> x` as a
	// lambda over a parameter called `0`, not as a JSON extraction. The port
	// built a JSONExtract there -- a tree the reference never makes -- which
	// the generator fuzzer found by writing it back as `A(0:)`.
	// A NUMBER or a STRING can name a lambda parameter. The reference reads
	// `A(0 -> x)` as a lambda over a parameter called `0`, and `A('abc' -> x)`
	// as one called `abc` -- QUOTED, since it was written that way. Outside a
	// call the same tokens are a JSON extraction, which is why this only
	// matters here, in argument position. The port built a JSONExtract and
	// wrote it back as `A('':)`, which is not SQL.
	// TokIDENTIFIER too: the port WRITES a string parameter back as a quoted
	// identifier -- `A('abc' -> x)` becomes ``A(`abc` -> x)`` -- so it has to
	// be able to read that.
	//
	// A keyword usable as a bare name -- ALL among them -- names a lambda
	// parameter the same way: the reference's own lambda-arg reader is its
	// general id-var reader, which accepts every keyword in that set, not
	// just VAR and quoted IDENTIFIER. Checking only those two left
	// `A(ALL -> x)` read as a JSON extraction with a Column called ALL, which
	// the generator then could not write back as SQL that reparses -- the
	// fuzzer found it nested inside another JSON-arrow chain.
	if p.atIdentifierWhere(false) || c.Type == TokNUMBER || c.Type == TokSTRING {
		return p.next() != nil && p.next().Type == TokARROW
	}
	if c.Type != TokL_PAREN {
		return false
	}
	depth := 0
	for i := p.index; i < len(p.tokens); i++ {
		switch p.tokens[i].Type {
		case TokL_PAREN:
			depth++
		case TokR_PAREN:
			depth--
			if depth == 0 {
				if i+1 >= len(p.tokens) || p.tokens[i+1].Type != TokARROW {
					return false
				}
				// An arrow after the parentheses is not enough: what is
				// inside them has to be a list of NAMES. `((A)) -> '$[0]'`
				// is a JSON extraction from a parenthesised column, and
				// reading it as a lambda left the port unable to read back
				// what it had written. The generator fuzzer found it.
				return p.namesOnly(p.tokens[p.index+1 : i])
			}
		}
	}
	return false
}

// namesOnly reports whether a run of tokens is a comma-separated list of
// names and nothing else -- which is all a lambda's parameter list may hold.
// A keyword usable as a bare name -- ALL among them -- counts too, the same
// as the single-parameter form above: `A((ALL, B) -> B)` is a lambda over two
// parameters, not a JSON extraction from a tuple.
func (p *parser) namesOnly(run []Token) bool {
	wantName := true
	for _, t := range run {
		if wantName {
			switch t.Type {
			case TokVAR, TokIDENTIFIER, TokNUMBER, TokSTRING:
			default:
				if _, ok := p.tables.IDVarTokens[t.Type]; !ok {
					return false
				}
			}
			wantName = false
			continue
		}
		if t.Type != TokCOMMA {
			return false
		}
		wantName = true
	}
	return !wantName
}

// parseLambdaParamName reads one lambda parameter's name. A NUMBER or a
// STRING can name one -- atLambda already treats both as the start of a
// lambda -- but neither is an identifier anywhere else, so atIdentifierWhere
// must not be widened to accept them; they are read here instead, before
// falling back to the ordinary identifier reader for everything else. A
// string comes back QUOTED, a number does not, matching how the reference
// builds each one.
func (p *parser) parseLambdaParamName() (*Expression, error) {
	c := p.curr()
	switch {
	case c != nil && c.Type == TokSTRING:
		p.advance()
		return New("Identifier", Arg{"this", c.Text}, Arg{"quoted", true}), nil
	case c != nil && c.Type == TokNUMBER:
		p.advance()
		return New("Identifier", Arg{"this", c.Text}, Arg{"quoted", false}), nil
	default:
		return p.parseIdentifier()
	}
}

func (p *parser) parseLambda() (*Expression, error) {
	if p.tables.ColonLambdaRead && strings.EqualFold(p.curr().Text, "LAMBDA") {
		return p.parseColonLambda()
	}
	var params []*Expression
	if p.at(TokL_PAREN) {
		p.advance()
		for !p.at(TokR_PAREN) {
			id, err := p.parseLambdaParamName()
			if err != nil {
				return nil, err
			}
			params = append(params, id)
			if !p.match(TokCOMMA) {
				break
			}
		}
		if !p.match(TokR_PAREN) {
			return nil, p.unsupported("unclosed lambda parameter list")
		}
	} else {
		id, err := p.parseLambdaParamName()
		if err != nil {
			return nil, err
		}
		params = append(params, id)
	}
	if !p.match(TokARROW) {
		return nil, p.unsupported("lambda without ->")
	}
	body, err := p.parseExpression()
	if err != nil {
		return nil, err
	}
	// A reference to a parameter INSIDE the body is not a column of any table:
	// the reference rewrites each one to the parameter's own Identifier, so
	// `b -> b` has an Identifier under the Lambda, not a Column. Leaving them
	// as Columns is a different tree for every lambda there is.
	names := map[string]bool{}
	for _, prm := range params {
		if n, ok := prm.Args["this"].(string); ok {
			names[strings.ToUpper(n)] = true
		}
	}
	replaced, err := p.bindLambdaParams(body, names)
	if err != nil {
		return nil, err
	}
	return New("Lambda", Arg{"this", replaced}, Arg{"expressions", params}, Arg{"colon", nil}), nil
}

// parseColonLambda reads the keyword form: `LAMBDA a, b : body`. Its
// parameters are never parenthesised and the body is separated by a colon,
// but the node is the same Lambda the arrow builds -- with `colon` recorded,
// because the dialect writes back the form that was written.
func (p *parser) parseColonLambda() (*Expression, error) {
	p.advance() // LAMBDA
	var params []*Expression
	for {
		id, err := p.parseIdentifier()
		if err != nil {
			return nil, err
		}
		params = append(params, id)
		if !p.match(TokCOMMA) {
			break
		}
	}
	if !p.match(TokCOLON) {
		return nil, p.unsupported("LAMBDA without a colon")
	}
	body, err := p.parseExpression()
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for _, prm := range params {
		if n, ok := prm.Args["this"].(string); ok {
			names[strings.ToUpper(n)] = true
		}
	}
	replaced, err := p.bindLambdaParams(body, names)
	if err != nil {
		return nil, err
	}
	return New("Lambda", Arg{"this", replaced}, Arg{"expressions", params}, Arg{"colon", true}), nil
}

// bindLambdaParams rewrites Columns that name a parameter into the parameter's
// Identifier, depth first. A QUALIFIED reference -- `x.a.b` inside a lambda --
// the reference turns into a Dot chain instead, which is more machinery than
// this has, so it is refused rather than half-done.
func (p *parser) bindLambdaParams(e *Expression, names map[string]bool) (*Expression, error) {
	return p.bindLambdaParamsWhere(e, names, true)
}

// bindLambdaParamsWhere is bindLambdaParams with one thing it has to know:
// whether the node is the body's own top. A chain that runs past a column's
// four slots is rewritten only where something ENCLOSES it -- the reference
// replaces the outermost Dot in its parent, and at the top of the body there
// is no parent to replace it in, so the column stays. That is the reference
// being accidental rather than deliberate, and the corpus records what it
// made.
func (p *parser) bindLambdaParamsWhere(
	e *Expression, names map[string]bool, atRoot bool,
) (*Expression, error) {
	if e == nil {
		return nil, nil
	}
	// A chain of Dots over a column that names a parameter. The whole chain
	// becomes Identifiers -- the column's own parts and the names dotted onto
	// it -- because the reference rebuilds it from both.
	if e.Class == "Dot" {
		if inner := innermostColumn(e); inner != nil && namesAParameter(inner, names) {
			if atRoot {
				// Nothing encloses the chain, so the reference's replacement
				// lands nowhere and the column survives whole.
				return e, nil
			}
			return chainOfNames(e), nil
		}
	}
	if e.Class == "Column" && namesAParameter(e, names) {
		// A column with no qualifiers IS the parameter; one with them becomes
		// the Dot chain its parts spell, wherever it stands.
		if only, _ := e.Args["this"].(*Expression); !qualifiedColumn(e) {
			return only, nil
		}
		return chainOfNames(e), nil
	}
	for key, value := range e.Args {
		switch v := value.(type) {
		case *Expression:
			out, err := p.bindLambdaParamsWhere(v, names, false)
			if err != nil {
				return nil, err
			}
			e.Args[key] = out
		case []*Expression:
			for i, item := range v {
				out, err := p.bindLambdaParamsWhere(item, names, false)
				if err != nil {
					return nil, err
				}
				v[i] = out
			}
		}
	}
	return e, nil
}

// innermostColumn follows a chain of Dots down its `this` side to the column
// the chain hangs off, or nil where it hangs off something else.
func innermostColumn(e *Expression) *Expression {
	for e != nil && e.Class == "Dot" {
		e, _ = e.Args["this"].(*Expression)
	}
	if e != nil && e.Class == "Column" {
		return e
	}
	return nil
}

// qualifiedColumn reports whether a column carries any of its three
// qualifiers.
func qualifiedColumn(e *Expression) bool {
	for _, key := range []string{"catalog", "db", "table"} {
		if part, _ := e.Args[key].(*Expression); part != nil {
			return true
		}
	}
	return false
}

// namesAParameter reports whether a column's OUTERMOST part is one of the
// lambda's parameters -- for `x.key` the table `x`, not the column `key`.
func namesAParameter(e *Expression, names map[string]bool) bool {
	first, _ := e.Args["this"].(*Expression)
	for _, key := range []string{"catalog", "db", "table"} {
		if part, _ := e.Args[key].(*Expression); part != nil {
			first = part
			break
		}
	}
	if first == nil {
		return false
	}
	n, ok := first.Args["this"].(string)
	return ok && names[strings.ToUpper(n)]
}

// chainOfNames rebuilds a column, and whatever is dotted onto it, as a chain
// of Dots over plain Identifiers -- no Column left in it.
func chainOfNames(e *Expression) *Expression {
	var dotted []*Expression
	for e.Class == "Dot" {
		if part, _ := e.Args["expression"].(*Expression); part != nil {
			dotted = append([]*Expression{part}, dotted...)
		}
		e, _ = e.Args["this"].(*Expression)
	}
	parts := make([]*Expression, 0, 4+len(dotted))
	for _, key := range []string{"catalog", "db", "table", "this"} {
		if part, _ := e.Args[key].(*Expression); part != nil {
			parts = append(parts, part)
		}
	}
	parts = append(parts, dotted...)
	out := parts[0]
	for _, part := range parts[1:] {
		out = New("Dot", Arg{"this", out}, Arg{"expression", part})
	}
	return out
}

// atKwarg reports whether a NAMED argument starts here: a name followed by
// `=>`. PostgreSQL writes `MAKE_INTERVAL(years => 1)` and the reference reads
// the pair where it reads a lambda -- the two markers sit in one table there,
// which is why this stands beside atLambda rather than anywhere else.
func (p *parser) atKwarg() bool {
	c := p.curr()
	if c == nil || !p.atIdentifier() {
		return false
	}
	return p.next() != nil && p.next().Type == TokFARROW
}

// parseKwarg reads `name => value`.
//
// The name becomes a VAR rather than an identifier or a column: the reference
// takes the NAME off whatever it parsed on the left and builds a fresh Var
// from it, so a quoted one loses its quotes here.
func (p *parser) parseKwarg() (*Expression, error) {
	name := p.curr()
	if name.Type == TokIDENTIFIER {
		return nil, p.unsupported("a quoted name for a named argument")
	}
	p.advance()
	p.advance() // =>
	value, err := p.parseExpression()
	if err != nil {
		return nil, err
	}
	return New("Kwarg",
		Arg{"this", New("Var", Arg{"this", name.Text})},
		Arg{"expression", value}), nil
}

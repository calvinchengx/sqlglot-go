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
	if c.Type == TokVAR || c.Type == TokNUMBER || c.Type == TokSTRING ||
		c.Type == TokIDENTIFIER {
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
				return i+1 < len(p.tokens) && p.tokens[i+1].Type == TokARROW
			}
		}
	}
	return false
}

func (p *parser) parseLambda() (*Expression, error) {
	var params []*Expression
	if p.at(TokL_PAREN) {
		p.advance()
		for !p.at(TokR_PAREN) {
			id, err := p.parseIdentifier()
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
	} else if c := p.curr(); c != nil && c.Type == TokSTRING {
		// A string that names a parameter becomes a QUOTED identifier.
		p.advance()
		params = append(params, New("Identifier", Arg{"this", c.Text}, Arg{"quoted", true}))
	} else {
		id, err := p.parseIdentifier()
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

// bindLambdaParams rewrites Columns that name a parameter into the parameter's
// Identifier, depth first. A QUALIFIED reference -- `x.a.b` inside a lambda --
// the reference turns into a Dot chain instead, which is more machinery than
// this has, so it is refused rather than half-done.
func (p *parser) bindLambdaParams(e *Expression, names map[string]bool) (*Expression, error) {
	if e == nil {
		return nil, nil
	}
	if e.Class == "Column" {
		// The reference matches on the column's FIRST part, which for `x.key`
		// is the table `x`, not the column `key`.
		first, _ := e.Args["this"].(*Expression)
		qualified := false
		for _, key := range []string{"catalog", "db", "table"} {
			if part, _ := e.Args[key].(*Expression); part != nil {
				first = part
				qualified = true
			}
		}
		if first != nil {
			if n, ok := first.Args["this"].(string); ok && names[strings.ToUpper(n)] {
				if qualified {
					// `x.key` becomes a Dot chain in the reference; that
					// conversion is not ported, so it is refused.
					return nil, p.unsupported("qualified lambda parameter")
				}
				return first, nil
			}
		}
	}
	for key, value := range e.Args {
		switch v := value.(type) {
		case *Expression:
			out, err := p.bindLambdaParams(v, names)
			if err != nil {
				return nil, err
			}
			e.Args[key] = out
		case []*Expression:
			for i, item := range v {
				out, err := p.bindLambdaParams(item, names)
				if err != nil {
					return nil, err
				}
				v[i] = out
			}
		}
	}
	return e, nil
}

package sqlglot

import "strings"

// parseWindow reads the OVER clause that turns a call into a window function.
// It is entered with OVER current.
//
// The frame keywords are not keywords: UNBOUNDED, PRECEDING, FOLLOWING and
// CURRENT arrive as plain VARs, and ROW is tokenized as STRUCT, so the frame is
// matched by TEXT. The reference stores those bounds as plain strings too --
// `start: "UNBOUNDED"`, `start_side: "PRECEDING"`, `end: "CURRENT ROW"` -- so
// nothing is being invented here, only read back.
func (p *parser) parseWindow(this *Expression) (*Expression, error) {
	p.advance() // OVER
	return p.windowBody(this, "OVER")
}

// parseNamedWindow reads one entry of a `WINDOW w AS (...)` clause. It is the
// same Window node an OVER builds, with the NAME where the call would be and
// `over` left off -- so the body is read by the same code.
func (p *parser) parseNamedWindow() (*Expression, error) {
	name, err := p.parseIdentifier()
	if err != nil {
		return nil, err
	}
	if !p.match(TokALIAS) {
		return nil, p.unsupported("a named window without AS")
	}
	if !p.at(TokL_PAREN) {
		return nil, p.unsupported("a named window without a specification")
	}
	return p.windowBody(name, nil)
}

func (p *parser) windowBody(this *Expression, over any) (*Expression, error) {
	partitionBy := []*Expression{}
	var order, spec, alias *Expression

	if p.at(TokL_PAREN) {
		p.advance()
		if p.match(TokPARTITION_BY) {
			for {
				e, err := p.parseExpression()
				if err != nil {
					return nil, err
				}
				partitionBy = append(partitionBy, e)
				if !p.match(TokCOMMA) {
					break
				}
			}
		}
		if p.match(TokORDER_BY) {
			o, err := p.parseOrder()
			if err != nil {
				return nil, err
			}
			order = o
		}
		if kind := p.frameKind(); kind != "" {
			s, err := p.parseWindowSpec(kind)
			if err != nil {
				return nil, err
			}
			spec = s
		}
		if !p.match(TokR_PAREN) {
			return nil, p.unsupported("unclosed OVER clause")
		}
	} else {
		// `OVER w`, naming a window defined by a WINDOW clause elsewhere.
		id, err := p.parseIdentifier()
		if err != nil {
			return nil, err
		}
		alias = id
	}

	return New("Window",
		Arg{"this", this},
		Arg{"partition_by", partitionBy},
		Arg{"order", order},
		Arg{"spec", spec},
		Arg{"alias", alias},
		Arg{"over", over},
		Arg{"first", nil},
	), nil
}

// frameKind consumes ROWS, RANGE or GROUPS and reports which, or "" if the
// current token is none of them.
func (p *parser) frameKind() string {
	switch {
	case p.at(TokROWS):
		p.advance()
		return "ROWS"
	case p.at(TokRANGE):
		p.advance()
		return "RANGE"
	}
	if c := p.curr(); c != nil && c.Type == TokVAR && strings.EqualFold(c.Text, "GROUPS") {
		p.advance()
		return "GROUPS"
	}
	return ""
}

func (p *parser) parseWindowSpec(kind string) (*Expression, error) {
	args := []Arg{{"kind", kind}}
	if p.match(TokBETWEEN) {
		start, side, err := p.parseFrameBound()
		if err != nil {
			return nil, err
		}
		if !p.match(TokAND) {
			return nil, p.unsupported("frame BETWEEN without AND")
		}
		end, endSide, err := p.parseFrameBound()
		if err != nil {
			return nil, err
		}
		args = append(args,
			Arg{"start", start}, Arg{"start_side", side},
			Arg{"end", end}, Arg{"end_side", endSide})
	} else {
		start, side, err := p.parseFrameBound()
		if err != nil {
			return nil, err
		}
		args = append(args,
			Arg{"start", start}, Arg{"start_side", side},
			Arg{"end", nil}, Arg{"end_side", nil})
	}
	args = append(args, Arg{"exclude", nil})
	return New("WindowSpec", args...), nil
}

// parseFrameBound reads one side of a frame: `UNBOUNDED PRECEDING`,
// `CURRENT ROW`, or an expression followed by PRECEDING or FOLLOWING. The
// bound is a string when it is one of the fixed words and an expression
// otherwise, which is the shape the reference records.
func (p *parser) parseFrameBound() (any, any, error) {
	c := p.curr()
	if c == nil {
		return nil, nil, p.unsupported("frame bound")
	}
	if c.Type == TokVAR && strings.EqualFold(c.Text, "CURRENT") {
		p.advance()
		n := p.curr()
		if n == nil || !strings.EqualFold(n.Text, "ROW") {
			return nil, nil, p.unsupported("CURRENT without ROW in a frame")
		}
		p.advance()
		return "CURRENT ROW", nil, nil
	}
	var bound any
	if c.Type == TokVAR && strings.EqualFold(c.Text, "UNBOUNDED") {
		p.advance()
		bound = "UNBOUNDED"
	} else {
		// Below AND: `BETWEEN 1 AND 3` names two bounds, and an expression
		// reader would take the whole of it as the first.
		e, err := p.parseBitwise()
		if err != nil {
			return nil, nil, err
		}
		bound = e
	}
	s := p.curr()
	if s == nil || s.Type != TokVAR ||
		(!strings.EqualFold(s.Text, "PRECEDING") && !strings.EqualFold(s.Text, "FOLLOWING")) {
		// A bound may say only how FAR: `RANGE BETWEEN 1 AND 3` names two
		// distances and no direction, and the reference records the bound
		// with no side beside it.
		if _, named := bound.(string); named {
			return nil, nil, p.unsupported("frame bound without PRECEDING or FOLLOWING")
		}
		return bound, nil, nil
	}
	p.advance()
	return bound, strings.ToUpper(s.Text), nil
}

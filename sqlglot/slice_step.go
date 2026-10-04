package sqlglot

// parseSliceRest reads the bounds after a slice's first colon, which the
// caller has already consumed.
//
// `x[1:2]` is a slice from 1 to 2, and `x[1:2:3]` carries a step. The
// spelling `:-:` is the end bound -1, with the colon after the dash opening
// the step: `[:-:-1]` is the same slice as `[:-1:-1]`. A unary minus cannot
// take a colon as its operand, so that pair is read here rather than as an
// expression.
//
// A colon where the end bound would be is the step only when it does not open
// a parameter: `[: :a]` is a slice up to the parameter `a`, as the reference
// reads it. Taking it as the step built `[::a]`, which reads back as a cast.
func (p *parser) parseSliceRest(low *Expression) (*Expression, error) {
	var high *Expression
	if p.at(TokDASH) && p.next() != nil && p.next().Type == TokCOLON {
		p.advance()
		high = New("Neg", Arg{"this", New("Literal", Arg{"this", "1"}, Arg{"is_string", false})})
	} else if !p.at(TokR_BRACKET) && !p.at(TokCOMMA) && !p.atSliceStart() {
		e, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		high = e
	}
	var step *Expression
	if p.match(TokCOLON) && !p.at(TokR_BRACKET) && !p.at(TokCOMMA) {
		e, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		step = e
	}
	slice := New("Slice", Arg{"this", low}, Arg{"expression", high})
	if step != nil {
		slice.Set("step", step)
	}
	return slice, nil
}

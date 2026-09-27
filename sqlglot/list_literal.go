package sqlglot

// parseListLiteral reads a list constructor, and the one empty shape that is
// not a constructor.
//
// `LIST[1, 2]` is a List the same way `ARRAY[1, 2]` is an Array: the word is
// part of the literal, not a column being subscripted. A dialect that reads
// LIST some other way first (DuckDB's own LIST type grammar, T-SQL's `[...]`
// already meaning a quoted identifier) never enters here.
//
// An empty pair is not a List with nothing in it. The reference reads
// `LIST[]` as the type ARRAY of a nested LIST that names no member, the same
// array-of-type suffix a bare `INT[]` takes, and writes it back as `LIST[]`
// where that dialect spells an array with brackets after its member.
//
// False means this token is not LIST followed by `[`, so the caller keeps
// going. True means the brackets were this constructor, including when the
// read failed.
func (p *parser) parseListLiteral() (*Expression, bool, error) {
	if !p.tables.HasListConstructor || !p.atPair(TokLIST, TokL_BRACKET) {
		return nil, false, nil
	}
	if p.index+2 < len(p.tokens) && p.tokens[p.index+2].Type == TokR_BRACKET {
		p.advance()
		p.advance()
		p.advance()
		inner := New("DataType",
			Arg{"this", DataTypeKind("LIST")},
			Arg{"nested", true})
		return arrayOf(inner, nil), true, nil
	}
	if p.index+2 >= len(p.tokens) {
		return nil, false, nil
	}
	p.advance()
	p.advance()
	items, err := p.parseBracketItems(true)
	if err != nil {
		return nil, true, err
	}
	return New("List", Arg{"expressions", items}), true, nil
}

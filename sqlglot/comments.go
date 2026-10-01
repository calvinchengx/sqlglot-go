package sqlglot

// takeComments returns the comments of the token just consumed, and only
// once. A later read at the same position gets nothing: the reference gives
// a token's comments to the one node built from it.
func (p *parser) takeComments() []string {
	if p.index == 0 || p.commentsFrom == p.index {
		return nil
	}
	comments := p.tokens[p.index-1].Comments
	if len(comments) == 0 {
		return nil
	}
	p.commentsFrom = p.index
	return append([]string(nil), comments...)
}

// keep gives those comments to e.
func (p *parser) keep(e *Expression) *Expression {
	return putComments(e, p.takeComments())
}

// putComments appends comments onto e. A nil node is left alone so a failing
// parse can pass its result straight through.
func putComments(e *Expression, comments []string) *Expression {
	if e == nil || len(comments) == 0 {
		return e
	}
	e.Comments = append(e.Comments, comments...)
	return e
}

// liftComments moves comments from a child onto the node the reference
// actually keeps them on: a column, a table, an alias.
func liftComments(from, to *Expression) {
	if from == nil || to == nil || from == to || len(from.Comments) == 0 {
		return
	}
	to.Comments = append(to.Comments, from.Comments...)
	from.Comments = nil
}

func liftAll(parts []*Expression, to *Expression) {
	for _, part := range parts {
		liftComments(part, to)
	}
}

func liftPart(part any, to *Expression) {
	id, _ := part.(*Expression)
	liftComments(id, to)
}

// withClause reads a WITH, keeping a comment that stands in front of the
// word. The comment is on the WITH token before it is consumed; taking it
// inside parseWith would rewrite a function that was just edited.
func (p *parser) withClause() (*Expression, error) {
	var leading []string
	if c := p.curr(); c != nil && c.Type == TokWITH && len(c.Comments) > 0 {
		leading = append([]string(nil), c.Comments...)
		c.Comments = nil
	}
	node, err := p.parseWith()
	if err != nil {
		return nil, err
	}
	return putComments(node, leading), nil
}

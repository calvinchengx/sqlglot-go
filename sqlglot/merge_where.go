package sqlglot

// noteMergeWhere stores a WHERE that follows a MERGE action. The clause
// chooses which matched rows the action touches. No WHERE leaves the
// action unchanged.
func (p *parser) noteMergeWhere(action *Expression) (*Expression, error) {
	if action == nil || !p.match(TokWHERE) {
		return action, nil
	}
	cond, err := p.parseDisjunction()
	if err != nil {
		return nil, err
	}
	where := New("Where")
	where.Set("this", cond)
	action.Set("where", where)
	return action, nil
}

package sqlglot

// The statements that change ROWS rather than definitions.
//
// UPDATE, DELETE and MERGE were refused as "not a query" alongside the DDL,
// and they matter to the guard above this port for the same reason a SELECT
// does: an UPDATE whose source is a subquery, or a MERGE whose USING is one,
// reads exactly as much as a query does, and a guard can only refuse what it
// can see.
//
// Unlike a Create, which carries fourteen arguments whether or not the
// statement mentions them, these carry only what was read -- with three
// exceptions the reference sets unasked, noted where they are set.

// parseUpdate reads `UPDATE <table> SET a = 1[, ...] [FROM ...] [WHERE ...]
// [RETURNING ...]`.
func (p *parser) parseUpdate() (*Expression, error) {
	p.advance() // UPDATE

	table, err := p.parseTable()
	if err != nil {
		return nil, err
	}
	if p.at(TokCOMMA) {
		// `UPDATE t1 AS a, t2 AS b SET ...` updates THROUGH a join, which the
		// reference records as joins on the node. Reading it as an update of
		// the first table alone would name a different statement.
		return nil, p.unsupported("UPDATE of more than one table")
	}
	if !p.match(TokSET) {
		return nil, p.unsupported("UPDATE without SET")
	}
	assignments, err := p.parseAssignments()
	if err != nil {
		return nil, err
	}

	node := New("Update", Arg{"this", table}, Arg{"expressions", assignments})
	// Set in SOURCE order, because that is the order the reference assigns
	// them in and therefore the order they dump in. T-SQL writes RETURNING
	// here, in front of the FROM, and the node records it in the place it was
	// read rather than in a fixed slot.
	if err := p.readReturning(node); err != nil {
		return nil, err
	}
	if p.at(TokFROM) {
		from, err := p.parseFrom()
		if err != nil {
			return nil, err
		}
		node.Set("from_", from)
	}
	if p.at(TokWHERE) {
		p.advance()
		where, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		node.Set("where", New("Where", Arg{"this", where}))
	}
	if err := p.readReturning(node); err != nil {
		return nil, err
	}
	if p.curr() != nil {
		return nil, p.unsupported("UPDATE with more than this port reads")
	}
	return node, nil
}

// parseAssignments reads the `a = 1, b = 2` of a SET clause. Each is an
// ordinary equality -- the reference keeps them as EQ nodes rather than as a
// pair -- so anything that is not one is refused rather than reshaped.
func (p *parser) parseAssignments() ([]*Expression, error) {
	var out []*Expression
	for {
		e, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		if e.Class != "EQ" {
			return nil, p.unsupported("a SET item that is not an assignment")
		}
		out = append(out, e)
		if !p.match(TokCOMMA) {
			return out, nil
		}
	}
}

// readReturning sets `returning` on a statement if one is written here. It
// takes the node rather than returning a value because WHERE it is set matters
// as much as whether: the arguments dump in the order they were assigned.
func (p *parser) readReturning(node *Expression) error {
	returning, err := p.parseReturning()
	if err != nil {
		return err
	}
	if returning != nil {
		if _, already := node.Args["returning"]; already {
			return p.unsupported("two RETURNING clauses")
		}
		node.Set("returning", returning)
	}
	return nil
}

// parseReturning reads `RETURNING a, b`. T-SQL spells it OUTPUT and writes it
// in a different place, but its tokenizer gives OUTPUT the same token, so the
// spelling never reaches here: only the position differs.
func (p *parser) parseReturning() (*Expression, error) {
	if !p.at(TokRETURNING) {
		return nil, nil
	}
	p.advance()
	items, err := p.parseExpressionList()
	if err != nil {
		return nil, err
	}
	// `OUTPUT ... INTO @t` writes the rows somewhere as well as returning
	// them -- a table variable, read the same way one is anywhere else, or a
	// plain name.
	var into any = false
	if p.match(TokINTO) {
		if param := p.parseParameter(); param != nil {
			into = param
		} else {
			target, err := p.parseTablePart()
			if err != nil {
				return nil, err
			}
			into = target
		}
	}
	return New("Returning", Arg{"expressions", items}, Arg{"into", into}), nil
}

// parseDelete reads `DELETE FROM <table> [USING ...] [WHERE ...]
// [RETURNING ...]`.
//
// `using` and `cluster` are set whether or not the statement says anything
// about them -- absent is false here, not missing -- which is why they are
// written out below even when nothing was read.
func (p *parser) parseDelete() (*Expression, error) {
	p.advance() // DELETE

	// No RETURNING is read here. T-SQL WRITES one in this position and cannot
	// read its own output back -- see docs/upstream-issues.md -- so there is
	// no tree for this port to agree with, and reading one would agree with
	// nothing.
	if !p.match(TokFROM) {
		// `DELETE x FROM z` names the table twice, once as a target and once
		// as a source; the reference keeps the first under `tables`.
		return nil, p.unsupported("DELETE without FROM")
	}
	table, err := p.parseTable()
	if err != nil {
		return nil, err
	}

	node := New("Delete", Arg{"this", table})
	if p.at(TokUSING) {
		p.advance()
		source, err := p.parseTable()
		if err != nil {
			return nil, err
		}
		// `USING a, b` is a comma JOIN, exactly as it is in a FROM clause, and
		// the reference hangs it off the first table rather than making a
		// second entry. `using` stays a list of one.
		// `USING a, b` is a comma JOIN, exactly as in a FROM clause, and the
		// reference hangs it off the first table rather than making a second
		// entry -- unless the item is a VALUES, which takes no joins, and the
		// comma then separates two entries after all.
		using := []*Expression{source}
		if source.Class == "Values" {
			for p.match(TokCOMMA) {
				next, err := p.parseTable()
				if err != nil {
					return nil, err
				}
				using = append(using, next)
			}
		} else {
			joins, err := p.parseJoins()
			if err != nil {
				return nil, err
			}
			if len(joins) > 0 {
				source.Set("joins", joins)
			}
		}
		node.Set("using", using)
	} else {
		node.Set("using", false)
	}
	node.Set("cluster", false)

	if p.at(TokWHERE) {
		p.advance()
		where, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		node.Set("where", New("Where", Arg{"this", where}))
	}
	if err := p.readReturning(node); err != nil {
		return nil, err
	}
	if p.curr() != nil {
		return nil, p.unsupported("DELETE with more than this port reads")
	}
	return node, nil
}

// parseMerge reads `MERGE INTO <target> USING <source> ON <cond> WHEN ...`.
//
// Two things are matched against each other and the WHENs say what to do about
// each outcome, so the branches are where the statement's meaning is. DuckDB
// spells the match `USING (col)` rather than `ON`, and the reference keeps
// that under a name of its own rather than rewriting it into a condition.
func (p *parser) parseMerge() (*Expression, error) {
	p.advance() // MERGE

	if !p.match(TokINTO) {
		return nil, p.unsupported("MERGE without INTO")
	}
	target, err := p.parseTable()
	if err != nil {
		return nil, err
	}
	if !p.match(TokUSING) {
		return nil, p.unsupported("MERGE without USING")
	}
	source, err := p.parseTable()
	if err != nil {
		return nil, err
	}

	node := New("Merge", Arg{"this", target}, Arg{"using", source})
	if p.at(TokON) {
		p.advance()
		on, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		node.Set("on", on)
	} else {
		node.Set("on", false)
	}
	if p.at(TokUSING) {
		p.advance()
		columns, err := p.parseParenthesisedList()
		if err != nil {
			return nil, err
		}
		// Bare names, the way a JOIN ... USING keeps them: each says which
		// column the two sides are matched on, and refers to neither side.
		ids := make([]*Expression, 0, len(columns))
		for _, c := range columns {
			if c.Class != "Column" {
				return nil, p.unsupported("a MERGE USING member that is not a name")
			}
			ids = append(ids, New("Identifier",
				Arg{"this", c.Name()}, Arg{"quoted", false}))
		}
		node.Set("using_cond", ids)
	} else {
		node.Set("using_cond", false)
	}

	var whens []*Expression
	for p.at(TokWHEN) {
		when, err := p.parseWhen()
		if err != nil {
			return nil, err
		}
		whens = append(whens, when)
	}
	if len(whens) == 0 {
		return nil, p.unsupported("MERGE without a WHEN")
	}
	node.Set("whens", New("Whens", Arg{"expressions", whens}))

	if err := p.readReturning(node); err != nil {
		return nil, err
	}
	if p.curr() != nil {
		return nil, p.unsupported("MERGE with more than this port reads")
	}
	return node, nil
}

// parseWhen reads one branch: `WHEN [NOT] MATCHED [BY SOURCE] [AND cond] THEN
// <action>`.
//
// `matched` and `source` are both on the node whether or not the branch said
// so, and the action is a node of the class it names -- an Update carrying
// only its assignments, an Insert carrying only its columns and values, or a
// bare word for the branches that do nothing at all.
func (p *parser) parseWhen() (*Expression, error) {
	p.advance() // WHEN

	matched := true
	if p.at(TokNOT) {
		p.advance()
		matched = false
	}
	if !p.atWords("MATCHED") {
		return nil, p.unsupported("a MERGE branch without MATCHED")
	}
	p.advance()

	source := false
	if p.atWords("BY", "SOURCE") {
		p.advance()
		p.advance()
		source = true
	} else if p.atWords("BY", "TARGET") {
		p.advance()
		p.advance()
	}

	when := New("When", Arg{"matched", matched}, Arg{"source", source})
	if p.at(TokAND) {
		p.advance()
		condition, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		when.Set("condition", condition)
	}
	if !p.match(TokTHEN) {
		return nil, p.unsupported("a MERGE branch without THEN")
	}
	then, err := p.parseMergeAction()
	if err != nil {
		return nil, err
	}
	when.Set("then", then)
	return when, nil
}

// parseMergeAction reads what one branch does.
func (p *parser) parseMergeAction() (*Expression, error) {
	switch {
	case p.at(TokUPDATE):
		p.advance()
		if !p.match(TokSET) {
			// `WHEN MATCHED THEN UPDATE` updates every column DuckDB can pair
			// up. The absence is recorded rather than left off.
			return New("Update", Arg{"expressions", false}), nil
		}
		assignments, err := p.parseAssignments()
		if err != nil {
			return nil, err
		}
		return New("Update", Arg{"expressions", assignments}), nil
	case p.at(TokINSERT):
		p.advance()
		insert := New("Insert")
		if p.at(TokSTAR) {
			p.advance()
			insert.Set("this", New("Star"))
			return insert, nil
		}
		if p.at(TokL_PAREN) {
			columns, err := p.parseParenthesisedList()
			if err != nil {
				return nil, err
			}
			insert.Set("this", New("Tuple", Arg{"expressions", columns}))
		}
		if p.match(TokVALUES) {
			values, err := p.parseParenthesisedList()
			if err != nil {
				return nil, err
			}
			insert.Set("expression", New("Tuple", Arg{"expressions", values}))
		}
		if _, ok := insert.Args["this"]; !ok {
			if _, ok := insert.Args["expression"]; !ok {
				return nil, p.unsupported("a MERGE INSERT with neither columns nor values")
			}
		}
		return insert, nil
	case p.at(TokDELETE):
		p.advance()
		return New("Var", Arg{"this", "DELETE"}), nil
	case p.atWords("DO", "NOTHING"):
		p.advance()
		p.advance()
		// Two words, one Var: the reference keeps the phrase rather than a
		// flag, and the space is part of the name.
		return New("Var", Arg{"this", "DO NOTHING"}), nil
	}
	return nil, p.unsupported("a MERGE branch action this port does not read")
}

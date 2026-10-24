package sqlglot

import "strings"

// PRQL is a PIPELINE. A statement starts at a table and each transform after
// it rewrites the query built so far --
//
//	from employees | filter age > 25 | derive {name, age}
//
// -- and the pipe is optional, because a newline says the same thing. The port
// reads one and writes SQL, which is what the reference does: its PRQL
// generator is the base one, so a pipeline comes out as the SELECT it means
// (sqlglot/parsers/prql.py, PRQLParser).

// prqlTransformNames are the words that continue a pipeline.
func prqlTransformNames() []string {
	return []string{"DERIVE", "SELECT", "TAKE", "FILTER", "APPEND", "REMOVE",
		"INTERSECT", "SORT", "AGGREGATE"}
}

// parsePRQLStatement reads a pipeline, or an expression standing on its own.
func (p *parser) parsePRQLStatement() (*Expression, error) {
	if p.at(TokFROM) {
		return p.parsePRQLQuery()
	}
	return p.parsePRQLExpression()
}

// parsePRQLQuery reads `from <table>` and then every transform that follows.
func (p *parser) parsePRQLQuery() (*Expression, error) {
	p.advance() // FROM
	table, err := p.parseTableName()
	if err != nil {
		return nil, err
	}
	query := New("Select",
		Arg{"expressions", []*Expression{New("Star")}},
		Arg{"from_", New("From", Arg{"this", table})})
	known := map[string]bool{}
	for _, name := range prqlTransformNames() {
		known[name] = true
	}
	for {
		c := p.curr()
		if c == nil || isTextMatchExcluded(c.Type) {
			break
		}
		name := strings.ToUpper(c.Text)
		if !known[name] {
			break
		}
		p.advance()
		query, err = p.prqlApply(name, query)
		if err != nil {
			return nil, err
		}
	}
	return query, nil
}

// prqlApply applies one transform to the query built so far. A table of
// function values would have to name parser methods at package
// initialisation, which Go cannot order -- so it is a switch.
func (p *parser) prqlApply(name string, q *Expression) (*Expression, error) {
	switch name {
	case "DERIVE":
		return p.prqlSelection(q, p.parsePRQLExpression, true)
	case "SELECT":
		return p.prqlSelection(q, p.parsePRQLExpression, false)
	case "TAKE":
		num := p.parseNumberLiteral()
		if num == nil {
			return nil, p.unsupported("TAKE without a number")
		}
		q.Set("limit", New("Limit", Arg{"expression", num}))
		return q, nil
	case "FILTER":
		condition, err := p.parseDisjunction()
		if err != nil {
			return nil, err
		}
		// `a == null` asks whether it IS null, and `a != null` whether it is
		// not -- an equality against NULL is never true otherwise
		// (PRQLParser._parse_equality).
		condition = prqlNullComparisons(condition)
		// A second FILTER ANDs with the first: `where` on a query that
		// already has one adds to it rather than replacing it, which is what
		// the reference's `query.where(...)` does.
		if existing, _ := q.Args["where"].(*Expression); existing != nil {
			// `and_` wraps the ACCUMULATED side in a Paren when it is itself
			// a connector, which is why three filters nest as
			// `((a) AND b) AND c` rather than flat
			// (sqlglot/expressions/core.py, _combine).
			accumulated := existing.This()
			if isA("Connector", accumulated) {
				accumulated = New("Paren", Arg{"this", accumulated})
			}
			condition = New("And", Arg{"this", accumulated},
				Arg{"expression", condition})
		}
		q.Set("where", New("Where", Arg{"this", condition}))
		return q, nil
	case "APPEND":
		return p.prqlSetOp(q, "Union")
	case "REMOVE":
		return p.prqlSetOp(q, "Except")
	case "INTERSECT":
		return p.prqlSetOp(q, "Intersect")
	case "SORT":
		braced := p.match(TokL_BRACE)
		var items []*Expression
		for {
			item, err := p.parsePRQLOrdered()
			if err != nil {
				return nil, err
			}
			items = append(items, item)
			if !p.match(TokCOMMA) {
				break
			}
		}
		if braced && !p.match(TokR_BRACE) {
			return nil, p.unsupported("SORT without a closing brace")
		}
		q.Set("order", New("Order", Arg{"expressions", items}))
		return q, nil
	case "AGGREGATE":
		return p.prqlSelection(q, p.parsePRQLAggregate, false)
	}
	return q, nil
}

// prqlSelection reads DERIVE/SELECT/AGGREGATE: a braced list or one
// expression, RESOLVED against what the query already projects, so a later
// `select {y = x}` names the expression x was derived from rather than a
// column that does not exist (PRQLParser._parse_selection).
func (p *parser) prqlSelection(query *Expression, parse func() (*Expression, error), appendTo bool) (*Expression, error) {
	var selects []*Expression
	if p.match(TokL_BRACE) {
		for {
			item, err := parse()
			if err != nil {
				return nil, err
			}
			selects = append(selects, item)
			if !p.match(TokCOMMA) {
				break
			}
		}
		if !p.match(TokR_BRACE) {
			return nil, p.unsupported("PRQL selection without a closing brace")
		}
	} else {
		item, err := parse()
		if err != nil {
			return nil, err
		}
		if item != nil {
			selects = append(selects, item)
		}
	}
	existing, _ := query.Args["expressions"].([]*Expression)
	projections := map[string]*Expression{}
	for _, sel := range existing {
		if sel.Class != "Alias" {
			continue
		}
		if alias, _ := sel.Args["alias"].(*Expression); alias != nil {
			projections[strings.ToLower(alias.Name())] = sel.This()
		}
	}
	resolved := make([]*Expression, 0, len(selects))
	for _, sel := range selects {
		resolved = append(resolved, prqlResolveProjection(sel, projections))
	}
	if appendTo {
		query.Set("expressions", append(existing, resolved...))
	} else {
		query.Set("expressions", resolved)
	}
	return query, nil
}

// prqlResolveProjection replaces a COLUMN that names a projection with the
// expression that projection stands for, anywhere in the selection.
func prqlResolveProjection(e *Expression, projections map[string]*Expression) *Expression {
	if e == nil {
		return nil
	}
	if e.Class == "Column" {
		if projection, ok := projections[strings.ToLower(e.Name())]; ok && projection != nil {
			return projection.Copy()
		}
	}
	out := e.shallowCopy()
	for _, key := range append([]string(nil), e.Keys...) {
		switch v := e.Args[key].(type) {
		case *Expression:
			out.Set(key, prqlResolveProjection(v, projections))
		case []*Expression:
			items := make([]*Expression, 0, len(v))
			for _, item := range v {
				items = append(items, prqlResolveProjection(item, projections))
			}
			out.Set(key, items)
		}
	}
	return out
}

// prqlSetOp reads APPEND/REMOVE/INTERSECT: a set operation against a whole
// table, which is `SELECT * FROM <table>`.
func (p *parser) prqlSetOp(query *Expression, class string) (*Expression, error) {
	table, err := p.parseTableName()
	if err != nil {
		return nil, err
	}
	other := New("Select",
		Arg{"expressions", []*Expression{New("Star")}},
		Arg{"from_", New("From", Arg{"this", table})})
	return New(class, Arg{"this", query}, Arg{"expression", other},
		Arg{"distinct", false}), nil
}

// parsePRQLOrdered reads one SORT term: a leading + or - says the direction,
// where SQL would say ASC or DESC (PRQLParser._parse_ordered).
func (p *parser) parsePRQLOrdered() (*Expression, error) {
	p.match(TokPLUS) // an explicit ascending marker, consumed
	desc := p.match(TokDASH)
	column, err := p.parseColumn()
	if err != nil {
		return nil, err
	}
	// A leading - says DESC and puts the nulls last; anything else is
	// ascending with the nulls FIRST, which is PRQL's ordering and is written
	// on the node rather than left to the engine (PRQLParser._parse_ordered).
	args := []Arg{{"this", column}}
	if desc {
		args = append(args, Arg{"desc", true}, Arg{"nulls_first", false})
	} else {
		args = append(args, Arg{"nulls_first", true})
	}
	return New("Ordered", args...), nil
}

// parsePRQLAggregate reads `AVERAGE(x)` or `total = SUM(x)` -- an aggregate
// over ONE column, which is what the pipeline offers
// (PRQLParser._parse_aggregate).
func (p *parser) parsePRQLAggregate() (*Expression, error) {
	var alias *Expression
	if n := p.next(); n != nil && n.Type == TokALIAS {
		alias = p.parsePRQLIdentifierOrNil()
		p.match(TokALIAS)
	}
	c := p.curr()
	if c == nil {
		return nil, p.unsupported("AGGREGATE without a function")
	}
	name := strings.ToUpper(c.Text)
	p.advance()
	column, err := p.parseColumn()
	if err != nil {
		return nil, err
	}
	// The two the reference gives a builder of its own, and then whatever the
	// function table knows: `min`, `max`, `stddev` are the ordinary
	// aggregates over one column (PRQLParser.FUNCTIONS, _parse_aggregate).
	var built *Expression
	switch name {
	case "AVERAGE":
		built = New("Avg", Arg{"this", column})
	case "SUM":
		built = New("Coalesce", Arg{"this", New("Sum", Arg{"this", column})},
			Arg{"expressions", []*Expression{
				New("Literal", Arg{"this", "0"}, Arg{"is_string", false})}})
	default:
		spec, ok := p.tables.Functions[name]
		if !ok {
			return nil, p.unsupported("Unsupported aggregation function " + name)
		}
		built = p.buildFunction(name, spec, []*Expression{column})
	}
	if alias != nil {
		return New("Alias", Arg{"this", built}, Arg{"alias", alias}), nil
	}
	return built, nil
}

// parsePRQLExpression reads `name = expression` as an Alias, which is how a
// PRQL binding is written; `=` is an ALIAS token there
// (PRQLParser._parse_expression).
func (p *parser) parsePRQLExpression() (*Expression, error) {
	if n := p.next(); n != nil && n.Type == TokALIAS {
		alias := p.parsePRQLIdentifierOrNil()
		p.match(TokALIAS)
		inner, err := p.parseAssignment()
		if err != nil {
			return nil, err
		}
		return New("Alias", Arg{"this", inner}, Arg{"alias", alias}), nil
	}
	return p.parseAssignment()
}

// parsePRQLIdentifierOrNil reads a name where one may be absent.
func (p *parser) parsePRQLIdentifierOrNil() *Expression {
	if p.curr() == nil {
		return nil
	}
	id, err := p.parseIdentifier()
	if err != nil {
		return nil
	}
	return id
}

// prqlNullComparisons rewrites an equality against NULL into the IS it means:
// `a == null` is Is(a, NULL) and `a != null` is Not(Is(a, NULL)).
func prqlNullComparisons(e *Expression) *Expression {
	if e == nil {
		return nil
	}
	out := e.shallowCopy()
	for _, key := range append([]string(nil), e.Keys...) {
		switch v := e.Args[key].(type) {
		case *Expression:
			out.Set(key, prqlNullComparisons(v))
		case []*Expression:
			items := make([]*Expression, 0, len(v))
			for _, item := range v {
				items = append(items, prqlNullComparisons(item))
			}
			out.Set(key, items)
		}
	}
	if out.Class != "EQ" && out.Class != "NEQ" {
		return out
	}
	left, right := childOf(out, "this"), childOf(out, "expression")
	var other *Expression
	switch {
	case right != nil && right.Class == "Null":
		other = left
	case left != nil && left.Class == "Null":
		other = right
	default:
		return out
	}
	is := New("Is", Arg{"this", other}, Arg{"expression", New("Null")})
	if out.Class == "NEQ" {
		return New("Not", Arg{"this", is})
	}
	return is
}

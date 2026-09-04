package sqlglot

import "strings"

// FROM and its table references.

// parseFrom is entered with the FROM token current; the caller checked.
func (p *parser) parseFrom() (*Expression, error) {
	p.advance()
	table, err := p.parseTable()
	if err != nil {
		return nil, err
	}
	return New("From", Arg{"this", table}), nil
}

// parseJoins reads every join that follows the FROM clause, comma joins
// included. A comma join is a join: reading `FROM a, b` as `FROM a` is exactly
// the bypass that made this port necessary, so the comma is handled here
// rather than left to be mistaken for the end of the clause.
func (p *parser) parseJoins() ([]*Expression, error) {
	var joins []*Expression
	for {
		join, err := p.parseJoin()
		if err != nil {
			return nil, err
		}
		if join == nil {
			return joins, nil
		}
		joins = append(joins, join)
	}
}

func (p *parser) parseJoin() (*Expression, error) {
	if p.match(TokCOMMA) {
		table, err := p.parseTable()
		if err != nil {
			return nil, err
		}
		join := New("Join", Arg{"this", table})
		if p.tables.JoinsHaveEqualPrecedence {
			join.Set("kind", "CROSS")
		}
		return join, nil
	}

	c := p.curr()
	if c == nil {
		return nil, nil
	}
	// A METHOD says how the rows are matched rather than which of them are
	// kept: NATURAL joins on every column the two tables share.
	var method *Token
	if _, isMethod := p.tables.JoinMethods[c.Type]; isMethod {
		// NATURAL joins on every column the two tables share; POSITIONAL on
		// the row's position in each. Both are the method rather than the
		// side or the kind, and both are written back the same way.
		if !strings.EqualFold(c.Text, "NATURAL") &&
			!strings.EqualFold(c.Text, "POSITIONAL") {
			return nil, p.unsupported("join method " + c.Text)
		}
		method = c
		p.advance()
		c = p.curr()
		if c == nil {
			return nil, p.unsupported("a join method with no join")
		}
	}

	var side, kind *Token
	if _, ok := p.tables.JoinSides[c.Type]; ok {
		side = c
		p.advance()
	}
	if c2 := p.curr(); c2 != nil {
		if _, ok := p.tables.JoinKinds[c2.Type]; ok {
			kind = c2
			p.advance()
		}
	}

	// CROSS APPLY and OUTER APPLY read as a join with a kind but build a
	// Lateral instead, and the kind is recorded on the Lateral rather than on
	// the Join. Both forms are parsed: the guard has been bypassed through
	// APPLY over a function before, and it can only refuse what it can see.
	if p.at(TokAPPLY) && kind != nil && (kind.Type == TokCROSS || kind.Type == TokOUTER) && side == nil {
		p.advance()
		return p.parseApply(kind.Type == TokCROSS)
	}
	// A hint names how the engine should do the join rather than which rows
	// it keeps -- `INNER HASH JOIN` -- and stands between the kind and the
	// JOIN. Only T-SQL has any.
	hint := ""
	if c2 := p.curr(); c2 != nil {
		if _, ok := p.tables.JoinHints[strings.ToUpper(c2.Text)]; ok {
			hint = strings.ToUpper(c2.Text)
			p.advance()
		}
	}
	// STRAIGHT_JOIN is the word AND the join: it carries its own JOIN, so
	// none follows it.
	straight := kind != nil && kind.Type == TokSTRAIGHT_JOIN
	if !p.match(TokJOIN) && !straight {
		if side != nil || kind != nil || hint != "" {
			return nil, p.unsupported("join without JOIN")
		}
		return nil, nil
	}

	table, err := p.parseTable()
	if err != nil {
		return nil, err
	}
	join := New("Join", Arg{"this", table})
	// The reference sets the method before the side, and a dump compares key
	// order.
	if method != nil {
		join.Set("method", strings.ToUpper(method.Text))
	}
	if side != nil {
		join.Set("side", strings.ToUpper(side.Text))
	}
	if kind != nil {
		join.Set("kind", strings.ToUpper(kind.Text))
	}
	if hint != "" {
		join.Set("hint", hint)
	}
	switch {
	case p.match(TokON):
		on, err := p.parseDisjunction()
		if err != nil {
			return nil, err
		}
		join.Set("on", on)
	case p.at(TokUSING):
		// `JOIN b USING (x, y)` joins on the columns both sides share. The
		// reference keeps them as bare IDENTIFIERS, not columns.
		p.advance()
		if !p.match(TokL_PAREN) {
			return nil, p.unsupported("USING without a column list")
		}
		var columns []*Expression
		for {
			column, err := p.parseIdentifier()
			if err != nil {
				return nil, err
			}
			columns = append(columns, column)
			if !p.match(TokCOMMA) {
				break
			}
		}
		if !p.match(TokR_PAREN) {
			return nil, p.unsupported("unclosed USING")
		}
		join.Set("using", columns)
	case p.tables.BareJoinIsOnTrue:
		// Databricks records a bare JOIN as `ON TRUE` rather than leaving the
		// slot empty and writing the comma form. The same relation either way,
		// but a different tree and a different statement to the engine -- so
		// the two executors would not be sending the same SQL.
		join.Set("on", New("Boolean", Arg{"this", true}))
	}
	join.Set("pivots", nil)
	return join, nil
}

// parseApply builds the Lateral that CROSS APPLY and OUTER APPLY produce.
func (p *parser) parseApply(cross bool) (*Expression, error) {
	lateral := New("Lateral")
	switch {
	case p.at(TokL_PAREN):
		sub, err := p.parseSubqueryTable()
		if err != nil {
			return nil, err
		}
		// The alias belongs to the Lateral, not to the subquery inside it.
		alias, _ := sub.Args["alias"].(*Expression)
		sub.Set("alias", nil)
		lateral.Set("this", sub)
		lateral.Set("view", nil)
		lateral.Set("outer", nil)
		lateral.Set("alias", alias)
		lateral.Set("cross_apply", cross)
		lateral.Set("ordinality", nil)
	case p.namesAFunctionCall() || p.atIdentifier():
		target, err := p.parseQualifiedCall()
		if err != nil {
			return nil, err
		}
		// The alias names what the call produced, and it may name the
		// columns too: `APPLY f(x) y(z)`. It belongs to the Lateral rather
		// than to the call inside it.
		alias, err := p.parseTableAlias()
		if err != nil {
			return nil, err
		}
		lateral.Set("this", target)
		lateral.Set("view", nil)
		lateral.Set("outer", nil)
		lateral.Set("alias", alias)
		lateral.Set("cross_apply", cross)
		lateral.Set("ordinality", false)
	default:
		return nil, p.unsupported("APPLY over something the port cannot read")
	}
	return New("Join", Arg{"this", lateral}, Arg{"pivots", nil}), nil
}

// parseQualifiedCall reads `f(…)` or `schema.f(…)`, which the reference
// represents as a chain of Dots ending in the call.
func (p *parser) parseQualifiedCall() (*Expression, error) {
	var this *Expression
	for {
		if p.namesAFunctionCall() {
			fn, err := p.parseFunction()
			if err != nil {
				return nil, err
			}
			if this == nil {
				return fn, nil
			}
			return New("Dot", Arg{"this", this}, Arg{"expression", fn}), nil
		}
		id, err := p.parseTablePart()
		if err != nil {
			return nil, err
		}
		if this == nil {
			this = id
		} else {
			this = New("Dot", Arg{"this", this}, Arg{"expression", id})
		}
		if !p.match(TokDOT) {
			return nil, p.unsupported("APPLY over something that is not a call")
		}
	}
}

// startsATable reports whether a token could begin the name of a table. It
// settles words that are a KEYWORD in one position and a name in another:
// `FROM STREAM t` reads a stream of t, and `FROM stream` a table called
// stream.
func (p *parser) startsATable(t *Token) bool {
	if t == nil {
		return false
	}
	switch t.Type {
	case TokVAR, TokIDENTIFIER, TokL_PAREN:
		return true
	}
	_, name := p.tables.IDVarTokens[t.Type]
	return name
}

func (p *parser) parseTable() (*Expression, error) {
	// DuckDB names a relation in FRONT of it too: `FROM foo: bar` is
	// `FROM bar AS foo`, the same prefix alias the projection list takes.
	if p.tables.PrefixAlias && p.atAliasName() {
		if n := p.next(); n != nil && n.Type == TokCOLON {
			alias, err := p.parseIdentifier()
			if err != nil {
				return nil, err
			}
			p.advance() // the colon
			named, err := p.parseTable()
			if err != nil {
				return nil, err
			}
			if existing, _ := named.Args["alias"].(*Expression); existing != nil {
				return nil, p.unsupported("a relation named twice")
			}
			named.Set("alias", New("TableAlias", Arg{"this", alias}))
			return named, nil
		}
	}
	if p.at(TokLATERAL) {
		return p.parseLateral()
	}
	// `FROM VALUES (1) AS t(c)` is rows written out where a table would go,
	// and the alias belongs to the VALUES rather than to anything wrapping
	// it. Databricks writes the form bare; elsewhere it is parenthesised, and
	// the parentheses are the writer's alone -- the reference keeps the same
	// tree either way.
	if p.at(TokVALUES) {
		return p.parseValuesTable()
	}
	if p.at(TokL_PAREN) {
		return p.parseSubqueryTable()
	}
	// PostgreSQL's ROWS FROM (f(), g()) zips table functions into one
	// relation. It is a Table whose rows_from holds those functions,
	// not a name -- FROM ROWS would be a table called ROWS.
	if p.atWords("ROWS", "FROM") {
		return p.parseRowsFrom()
	}

	// `STREAM t` reads a table as a stream of changes rather than as the rows
	// it holds now. It is a node of its own around the table -- and only when
	// something FOLLOWS the word: a bare `stream` is a table called stream,
	// which is what the reference reads it as.
	if p.at(TokSTREAM) && p.next() != nil && p.startsATable(p.next()) {
		p.advance()
		inner, err := p.parseTable()
		if err != nil {
			return nil, err
		}
		return New("Stream", Arg{"this", inner}), nil
	}

	// T-SQL declares a TABLE VARIABLE and then selects from it by name:
	// `SELECT * FROM @MyTableVar`. The relation is the parameter itself, not
	// a table called @MyTableVar, and the reference puts the Parameter where
	// the name would go. It takes no qualifier and no dots.
	//
	// EVERY spelling, not just T-SQL's: whatever this dialect writes for a
	// parameter has to be readable here, or the port emits SQL it cannot read
	// back. An `@`-only reader here made `USE @0` parse in Databricks and
	// come back as `USE ${0}`, which nothing could then read -- a thousand
	// fuzz findings, and the second time the same shortcut has cost that.
	if param := p.parseParameter(); param != nil {
		return p.tableRest(New("Table", Arg{"this", param}))
	}
	// Databricks reads a bare `{df}` in a FROM clause as a notebook widget
	// naming the table, the same Placeholder any other expression reads it
	// as.
	if widget := p.parseWidgetPlaceholder(); widget != nil {
		return p.tableRest(New("Table", Arg{"this", widget}))
	}

	// PostgreSQL's ONLY says not to read the tables that inherit from this
	// one. It is a flag on the table rather than anything wrapping it.
	only := p.match(TokONLY)

	parts := []*Expression{}
	var fn *Expression
	for {
		// T-SQL skips the schema by writing nothing between two dots:
		// `a..b` is catalog `a`, table `b`, and a db of the EMPTY STRING --
		// not absent, the way a plain `a.b` leaves it -- which is what marks
		// the skip rather than a name that merely was not written.
		if p.at(TokDOT) {
			parts = append(parts, nil)
			p.advance()
			continue
		}
		// A callable in a FROM clause is a table function, not a table. The
		// port builds it rather than refusing: the guard has to SEE that the
		// relation is a function to say so, and a statement it could not read
		// would be refused for the wrong reason. Reading a local file through
		// `main.read_csv_auto('/etc/passwd')` is a real bypass, and the audit
		// line has to name it.
		if p.namesAFunctionCall() {
			// Some names are not a Table here at all: `FROM UNNEST(x)` is an
			// Unnest in the reference. Wrapping the call in a Table writes the
			// same SQL back and is a different tree, so it is refused.
			if c := p.curr(); c != nil {
				if _, isTable := p.tables.TableFunctions[strings.ToUpper(c.Text)]; isTable {
					if strings.ToUpper(c.Text) == "UNNEST" {
						return p.parseUnnest()
					}
					return nil, p.unsupported("table function " + strings.ToUpper(c.Text))
				}
			}
			f, err := p.parseFunction()
			if err != nil {
				return nil, err
			}
			fn = f
			break
		}
		id, err := p.parseTablePart()
		if err != nil {
			return nil, err
		}
		parts = append(parts, id)
		if !p.match(TokDOT) {
			break
		}
	}

	var table *Expression
	names := []string{"db", "catalog"}
	if fn != nil {
		if len(parts) > 2 {
			return nil, p.unsupported("over-qualified table function")
		}
		// A table function always carries both qualifier slots, filled or not.
		table = New("Table", Arg{"this", fn})
		for i := len(parts) - 1; i >= 0; i-- {
			table.Set(names[len(parts)-1-i], parts[i])
		}
		for _, n := range names {
			if _, ok := table.Args[n]; !ok {
				table.Set(n, nil)
			}
		}
	} else {
		if len(parts) > 3 {
			return nil, p.unsupported("over-qualified table")
		}
		// this is the table; the parts before it are db then catalog. The
		// last part is never the empty slot pushed above: the loop only
		// breaks right after a REAL part, so a name ending in a dot is
		// refused earlier still, by parseTablePart reading past the end.
		table = New("Table", Arg{"this", parts[len(parts)-1]})
		for i := len(parts) - 2; i >= 0; i-- {
			// A skipped part was pushed as nil above, and is set as the
			// EMPTY STRING here -- an Identifier the port never builds, but
			// exactly what the reference records for a `.` that named
			// nothing.
			var val any = parts[i]
			if parts[i] == nil {
				val = ""
			}
			table.Set(names[len(parts)-2-i], val)
		}
	}

	if only {
		table.Set("only", true)
	}
	// `t1*` says this table AND everything that inherits from it, which is
	// the default -- the star is written and recorded nowhere.
	if p.at(TokSTAR) {
		p.advance()
	}
	markTemporaryTable(table, p.dialect)
	return p.tableRest(table)
}

// parseRowsFrom reads `ROWS FROM (f(), g() AS t(c INT))`. Each member is a
// table (usually a function), and WITH ORDINALITY plus the outer alias
// belong to the ROWS FROM table, not to the last function.
func (p *parser) parseRowsFrom() (*Expression, error) {
	p.advance() // ROWS
	p.advance() // FROM
	tables, err := p.parseWrappedCSV(p.parseTable)
	if err != nil {
		return nil, err
	}
	if len(tables) == 0 {
		return nil, p.unsupported("ROWS FROM without a function")
	}
	return p.tableRest(New("Table", Arg{"rows_from", tables}))
}

// tableRest reads everything that may FOLLOW a table's name -- its temporal
// clause, its alias, its hints, its sample and its pivots -- in the order the
// reference reads them, which is the order they are written.
func (p *parser) tableRest(table *Expression) (*Expression, error) {
	// T-SQL's temporal clause hangs off the table, BEFORE the alias in the
	// text and before it on the node.
	if p.atWords("FOR", "SYSTEM_TIME") {
		version, err := p.parseSystemTime()
		if err != nil {
			return nil, err
		}
		table.Set("version", version)
	}
	// `TIMESTAMP AS OF '...'` and `VERSION AS OF 3` read the table as it
	// stood then rather than as it stands now, and go before the alias.
	if (p.atWords("TIMESTAMP") || p.atWords("VERSION")) && p.nextWords("AS", "OF") {
		word := strings.ToUpper(p.curr().Text)
		p.advance()
		p.advance()
		p.advance()
		at, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		table.Set("version", New("Version",
			Arg{"this", word}, Arg{"expression", at}, Arg{"kind", "AS OF"}))
	}
	// `AT (VERSION => 3)` may stand where an alias would, and AT is a word an
	// implicit alias would otherwise take: `demo AT (VERSION => 2)` would
	// name the table AT with a column list. Claimed here, before the alias.
	if p.atWords("AT") && p.next() != nil && p.next().Type == TokL_PAREN {
		when, err := p.parseHistoricalData()
		if err != nil {
			return nil, err
		}
		table.Set("when", when)
	}
	alias, err := p.parseTableAlias()
	if err != nil {
		return nil, err
	}
	if alias != nil {
		table.Set("alias", alias)
	}
	// And it may stand AFTER the alias too.
	if p.atWords("AT") && p.next() != nil && p.next().Type == TokL_PAREN {
		when, err := p.parseHistoricalData()
		if err != nil {
			return nil, err
		}
		table.Set("when", when)
	}
	// T-SQL's locking hints come after the alias: `FROM a AS b WITH (NOLOCK)`.
	if p.at(TokWITH) && p.next() != nil && p.next().Type == TokL_PAREN {
		hint, err := p.parseTableHint()
		if err != nil {
			return nil, err
		}
		table.Set("hints", []*Expression{hint})
	}
	// TABLESAMPLE hangs off the TABLE, after its alias.
	if p.at(TokTABLE_SAMPLE) {
		sample, err := p.parseTableSample()
		if err != nil {
			return nil, err
		}
		table.Set("sample", sample)
	}
	// And so do the pivots, as a LIST: `PIVOT(...) PIVOT(...)` chains, and the
	// reference keeps them in the order they were written.
	pivots, err := p.parsePivots()
	if err != nil {
		return nil, err
	}
	if len(pivots) > 0 {
		table.Set("pivots", pivots)
	}
	// WITH ORDINALITY numbers the rows a table function returns, and the
	// ALIAS comes after it rather than before: the reference reads the words
	// last of all and then reaches for an alias again.
	if p.atWords("WITH", "ORDINALITY") {
		p.advance()
		p.advance()
		table.Set("ordinality", true)
		alias, err := p.parseTableAlias()
		if err != nil {
			return nil, err
		}
		if alias != nil {
			table.Set("alias", alias)
		}
	}
	return table, nil
}

// parsePivots reads the run of PIVOT and UNPIVOT clauses after a table.
func (p *parser) parsePivots() ([]*Expression, error) {
	var out []*Expression
	for p.at(TokPIVOT) || p.at(TokUNPIVOT) {
		pivot, err := p.parsePivot()
		if err != nil {
			return nil, err
		}
		out = append(out, pivot)
	}
	return out, nil
}

// PIVOT(<aggregates> FOR <field> IN (<values>)) [AS alias], and the UNPIVOT
// that mirrors it.
//
// The node carries more than the statement says. Four conventions come from
// the dialect and are probed, and the output COLUMNS are DERIVED rather than
// read: the reference takes the product of the IN values with the aliases of
// whichever aggregates have one, joined by an underscore. With no alias
// anywhere there is no suffix and one column per value.
func (p *parser) parsePivot() (*Expression, error) {
	unpivot := p.at(TokUNPIVOT)
	p.advance()
	// `UNPIVOT INCLUDE NULLS (...)` and its EXCLUDE twin, before the
	// parentheses. Absent, the arg stays off the node entirely -- which is a
	// different tree from one carrying false.
	var includeNulls *bool
	if p.atWords("INCLUDE", "NULLS") || p.atWords("EXCLUDE", "NULLS") {
		include := p.atWords("INCLUDE", "NULLS")
		p.advance()
		p.advance()
		includeNulls = &include
	}
	if !p.match(TokL_PAREN) {
		return nil, p.unsupported("PIVOT without a specification")
	}

	var aggregates []*Expression
	// `PIVOT(FIRST(t) AS t, FOR quarter IN (...))` -- DuckDB tolerates a
	// trailing comma before FOR, and the reference reads it, so the loop ends
	// on FOR as well as on a missing comma.
	for !p.at(TokFOR) {
		// An OPERAND, not a full expression: FOR and IN are both range
		// operators, so `SUM(x) FOR y IN (...)` parsed as one expression
		// swallows the whole clause and then refuses it.
		agg, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		agg, err = p.parseAlias(agg)
		if err != nil {
			return nil, err
		}
		aggregates = append(aggregates, unpivotTarget(agg, unpivot))
		if !p.match(TokCOMMA) {
			break
		}
	}
	if len(aggregates) == 0 {
		return nil, p.unsupported("PIVOT without an aggregate")
	}
	if !p.match(TokFOR) {
		return nil, p.unsupported("PIVOT without FOR")
	}

	field, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	if !p.match(TokIN) {
		return nil, p.unsupported("PIVOT without IN")
	}
	if !p.match(TokL_PAREN) {
		// `IN y_enum` names an enum rather than listing values, which is a
		// different shape and not read here.
		return nil, p.unsupported("a PIVOT list that is not parenthesised")
	}
	var values []*Expression
	for {
		v, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		// `IN (q1 AS `Jan-Mar`)` names the output column. The reference
		// builds a PivotAlias for it, not the ordinary Alias.
		if p.match(TokALIAS) {
			name, err := p.parseIdentifier()
			if err != nil {
				return nil, err
			}
			v = New("PivotAlias", Arg{"this", v}, Arg{"alias", name})
		}
		values = append(values, v)
		if !p.match(TokCOMMA) {
			break
		}
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed PIVOT list")
	}
	if !p.match(TokR_PAREN) {
		// A GROUP BY or an ORDER BY inside the parentheses lands here; both
		// are shapes this does not model.
		return nil, p.unsupported("unclosed PIVOT")
	}

	in := New("In", Arg{"this", unpivotTarget(field, unpivot)},
		Arg{"expressions", values})
	args := []Arg{
		{"expressions", aggregates},
		{"fields", []*Expression{in}},
		{"unpivot", unpivot},
	}
	if includeNulls != nil {
		args = append(args, Arg{"include_nulls", *includeNulls})
	} else {
		args = append(args, Arg{"include_nulls", nil})
	}
	args = append(args,
		Arg{"default_on_null", false},
		Arg{"group", nil})

	if unpivot {
		args = append(args, Arg{"value_columns_first", p.tables.UnpivotValueColumnsFirst})
	} else {
		columns, err := p.pivotColumns(aggregates, values)
		if err != nil {
			return nil, err
		}
		args = append(args,
			Arg{"columns", columns},
			Arg{"identify_pivot_strings", p.tables.PivotIdentifiesStrings},
			Arg{"prefixed_pivot_columns", p.tables.PivotPrefixesColumns},
			Arg{"pivot_column_naming", p.tables.PivotColumnNaming})
	}

	// An alias belongs to the LAST pivot in a chain, so it is only taken when
	// another one does not follow.
	if !p.at(TokPIVOT) && !p.at(TokUNPIVOT) {
		alias, err := p.parseTableAlias()
		if err != nil {
			return nil, err
		}
		if alias != nil {
			args = append(args, Arg{"alias", alias})
		}
	}
	return New("Pivot", args...), nil
}

// pivotColumns derives the output column names: the product of the IN values
// with the aliases of whichever aggregates carry one, joined by underscores.
func (p *parser) pivotColumns(aggregates, values []*Expression) ([]*Expression, error) {
	var names []string
	for _, agg := range aggregates {
		if agg.Class == "Alias" {
			if alias, _ := agg.Args["alias"].(*Expression); alias != nil {
				names = append(names, alias.Name())
			}
		}
	}
	// DuckDB names the columns after the AGGREGATES themselves once there is
	// more than one, rendering each back to SQL to do it. That is a
	// derivation over generated text rather than over names, and it is
	// refused rather than approximated.
	if len(names) == 0 && len(aggregates) > 1 &&
		p.tables.PivotColumnNaming == "agg_name_if_aliased_or_multiple" {
		return nil, p.unsupported("a PIVOT naming its columns after several aggregates")
	}

	var out []*Expression
	for _, value := range values {
		// An explicit `<value> AS <alias>` NAMES the output column, so it
		// wins over the value's own name: the reference asks each field for
		// its alias-or-name, and only falls back to the name.
		base := value.Name()
		if value.Class == "PivotAlias" {
			if alias, _ := value.Args["alias"].(*Expression); alias != nil {
				base = alias.Name()
			}
		}
		if len(names) == 0 {
			out = append(out, identifierFor(base))
			continue
		}
		for _, name := range names {
			out = append(out, identifierFor(base+"_"+name))
		}
	}
	return out, nil
}

// identifierFor builds the identifier a derived name becomes, quoted when the
// name could not be written bare.
func identifierFor(name string) *Expression {
	return New("Identifier", Arg{"this", name}, Arg{"quoted", !isBareIdentifier(name)})
}

// unpivotTarget unwraps a Column to the Identifier inside it. An UNPIVOT names
// COLUMNS where a PIVOT holds expressions, and the reference keeps the bare
// name for them.
func unpivotTarget(e *Expression, unpivot bool) *Expression {
	if !unpivot {
		return e
	}
	return bareIdentifier(e)
}

// TABLESAMPLE [method] (<spec>) [REPEATABLE (seed)], where the spec is one of
//
//	BUCKET n OUT OF m [ON field]     bucket_numerator/denominator/field
//	<n> PERCENT   or   <n>%          percent
//	<n> ROWS                         size
//
// The method is a bare word before the parentheses -- DuckDB's RESERVOIR --
// and is kept as a Var rather than as a name.
func (p *parser) parseTableSample() (*Expression, error) {
	p.advance() // TABLESAMPLE

	args := []Arg{}
	method := p.tables.DefaultSampleMethod
	if !p.at(TokL_PAREN) {
		word := p.curr()
		if word == nil {
			return nil, p.unsupported("TABLESAMPLE without a specification")
		}
		p.advance()
		method = strings.ToUpper(word.Text)
	}
	if method != "" {
		args = append(args, Arg{"method", New("Var", Arg{"this", method})})
	}
	if !p.match(TokL_PAREN) {
		return nil, p.unsupported("TABLESAMPLE without a specification")
	}

	if p.atWords("BUCKET") {
		p.advance()
		numerator, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		if !p.atWords("OUT", "OF") {
			return nil, p.unsupported("a TABLESAMPLE bucket without OUT OF")
		}
		p.advance()
		p.advance()
		denominator, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		args = append(args,
			Arg{"bucket_numerator", numerator},
			Arg{"bucket_denominator", denominator})
		if p.match(TokON) {
			// The field is an IDENTIFIER, not a column: the reference keeps
			// the bare name here.
			field, err := p.parseExpression()
			if err != nil {
				return nil, err
			}
			args = append(args, Arg{"bucket_field", bareIdentifier(field)})
		}
	} else {
		// A count, not an expression: `20%` would otherwise read as a modulo
		// and swallow the closing parenthesis.
		size, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		switch {
		case p.match(TokMOD), p.atWords("PERCENT"):
			// `20%` and `20 PERCENT` are the same thing. The percent sign is
			// the modulo token; nothing else can follow a count here.
			if p.atWords("PERCENT") {
				p.advance()
			}
			args = append(args, Arg{"percent", size})
		case p.atWords("ROWS"), p.atWords("ROW"):
			p.advance()
			args = append(args, Arg{"size", size})
		default:
			// A bare count, whose unit is the DIALECT's: PostgreSQL reads
			// `TABLESAMPLE (3)` as three percent and the others as three
			// rows. The same three characters, two sizes of sample.
			if p.tables.BareSampleCountIsPercent {
				args = append(args, Arg{"percent", size})
			} else {
				args = append(args, Arg{"size", size})
			}
		}
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed TABLESAMPLE")
	}

	if p.atWords("REPEATABLE") {
		p.advance()
		if !p.match(TokL_PAREN) {
			return nil, p.unsupported("REPEATABLE without a seed")
		}
		seed, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		if !p.match(TokR_PAREN) {
			return nil, p.unsupported("unclosed REPEATABLE")
		}
		args = append(args, Arg{"seed", seed})
	}
	return New("TableSample", args...), nil
}

// bareIdentifier unwraps a Column down to the Identifier inside it, which is
// what the reference keeps where a bare NAME is wanted.
func bareIdentifier(e *Expression) *Expression {
	if e != nil && e.Class == "Column" {
		if inner, _ := e.Args["this"].(*Expression); inner != nil {
			return inner
		}
	}
	return e
}

// parseSubqueryTable reads a parenthesised FROM item.
//
// Whatever is inside, the reference wraps it in a Subquery: a query, a table,
// a join tree, or another parenthesised item. `FROM (t)` is a Subquery over a
// TABLE rather than over a select, and `FROM (a CROSS JOIN b)` hangs the joins
// off that table -- not off a Select, which is where a join usually lives.
func (p *parser) parseSubqueryTable() (*Expression, error) {
	p.advance() // the opening parenthesis
	var inner *Expression
	var err error
	if p.at(TokVALUES) {
		// The parentheses round a VALUES table are not kept: the alias goes
		// on the VALUES itself rather than on a Subquery wrapping it.
		values, err := p.parseValues()
		if err != nil {
			return nil, err
		}
		if !p.match(TokR_PAREN) {
			return nil, p.unsupported("unclosed VALUES")
		}
		alias, err := p.parseTableAlias()
		if err != nil {
			return nil, err
		}
		if alias != nil {
			values.Set("alias", alias)
		}
		return values, nil
	}
	if p.at(TokSELECT) || p.at(TokWITH) || p.at(TokPIVOT) || p.at(TokUNPIVOT) ||
		p.at(TokFROM) || p.at(TokSUMMARIZE) || p.opensASetOperation() {
		// A pivot STATEMENT reached through a FROM item comes out with
		// unpivot set FALSE, where the very same statement on its own or in
		// a CTE leaves the argument off. That is the reference being
		// inconsistent with itself, not a distinction that means anything --
		// but an argument present-and-false is a different tree from an
		// argument absent, so the port follows it.
		wasFromItem := p.inFromSubquery
		p.inFromSubquery = true
		inner, err = p.parseQuery()
		p.inFromSubquery = wasFromItem
	} else {
		inner, err = p.parseParenthesisedTable()
	}
	if err != nil {
		return nil, err
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed subquery")
	}
	sub := New("Subquery", Arg{"this", inner})
	// An alias written BEFORE the pivot is the subquery's; one written after
	// belongs to the pivot, which takes it itself.
	//
	// More KEYWORDS may name a subquery than may name a column -- `(SELECT 1)
	// apply` names it apply -- and which they are is the dialect's. The wider
	// set is used only here: a bare table takes an implicit alias in a
	// position where the same words still start clauses.
	alias, err := p.parseWideTableAlias()
	if err != nil {
		return nil, err
	}
	pivots, err := p.parsePivots()
	if err != nil {
		return nil, err
	}
	// Set in the reference's order, which puts the pivots before the alias
	// however they were written: argument order is part of the tree here.
	if len(pivots) > 0 {
		sub.Set("pivots", pivots)
	}
	sub.Set("alias", alias)
	// A subquery may be SAMPLED like any other relation.
	var sample *Expression
	if p.at(TokTABLE_SAMPLE) {
		sample, err = p.parseTableSample()
		if err != nil {
			return nil, err
		}
	}
	sub.Set("sample", sample)
	return sub, nil
}

// parseParenthesisedTable reads the table -- or join tree -- inside a pair of
// parentheses in FROM position. The joins hang off the table itself, which is
// the one place they do not belong to a Select.
func (p *parser) parseParenthesisedTable() (*Expression, error) {
	// `FROM (DESCRIBE t)` is a statement inside the parentheses, not a table.
	// Reading the keyword as a name built a Table called DESCRIBE, which is a
	// tree the reference never makes.
	if c := p.curr(); c != nil {
		if _, isStatement := p.tables.StatementTokens[c.Type]; isStatement {
			return nil, p.unsupported("statement where a table goes")
		}
	}
	table, err := p.parseTable()
	if err != nil {
		return nil, err
	}
	// Inside a join tree a VALUES is wrapped in a TABLE, where standing alone
	// as a FROM item it is not -- and the alias moves onto the wrapper with
	// it. Same rows, two shapes, and the position decides.
	if table.Class == "Values" {
		alias, _ := table.Args["alias"].(*Expression)
		table.Set("alias", nil)
		wrapped := New("Table", Arg{"this", table})
		if alias != nil {
			wrapped.Set("alias", alias)
		}
		table = wrapped
	}
	joins, err := p.parseJoins()
	if err != nil {
		return nil, err
	}
	if len(joins) > 0 {
		table.Set("joins", joins)
	}
	return table, nil
}

// namesAFunctionCall reports whether the current token opens a call. It is
// wider than atIdentifier on purpose: DuckDB's `glob` is an operator token
// everywhere else, and `main.glob('/**')` reads the filesystem, so the guard
// has to see the call rather than the parser lose it.
func (p *parser) namesAFunctionCall() bool {
	// A next token implies a current one, so curr is safe below.
	n := p.next()
	if n == nil || n.Type != TokL_PAREN {
		return false
	}
	// FuncTokens ALONE, which is the reference's own rule -- and it already
	// contains VAR and IDENTIFIER, so the extra atIdentifier() branch that
	// used to be here bought nothing and cost correctness: it accepted
	// keywords usable as identifiers, and `FROM SET('a','b')` became a call
	// where the reference reads a table named SET with an alias column list.
	//
	// Not cosmetic. The guard refuses a function used as a table by name, so
	// the two executors were refusing the same statement for DIFFERENT
	// reasons -- and the conformance suite checks the reason.
	_, ok := p.tables.FuncTokens[p.curr().Type]
	return ok
}

func (p *parser) parseTableAlias() (*Expression, error) {
	explicit := p.match(TokALIAS)
	if !explicit && !p.atAliasName() {
		return nil, nil
	}
	id, err := p.parseIdentifier()
	if err != nil {
		return nil, err
	}
	columns, err := p.parseAliasColumns()
	if err != nil {
		return nil, err
	}
	return New("TableAlias", Arg{"this", id}, Arg{"columns", columns}), nil
}

// parseWideTableAlias reads an alias that may be named by a keyword as well
// as by a plain word.
func (p *parser) parseWideTableAlias() (*Expression, error) {
	explicit := p.match(TokALIAS)
	if !explicit && !p.atTableAliasName() {
		return nil, nil
	}
	id, err := p.parseIdentifier()
	if err != nil {
		return nil, err
	}
	columns, err := p.parseAliasColumns()
	if err != nil {
		return nil, err
	}
	return New("TableAlias", Arg{"this", id}, Arg{"columns", columns}), nil
}

// atTableAliasName reports whether the word here may name a TABLE. More
// keywords may than may name a column, and which they are is the dialect's:
// `FROM (SELECT 1) apply` names the subquery apply. The table is a strict
// subset already -- a word that can start a clause is left out of it, or an
// alias written without AS would swallow the clause.
func (p *parser) atTableAliasName() bool {
	c := p.curr()
	if c == nil {
		return false
	}
	if c.Type == TokVAR || c.Type == TokIDENTIFIER {
		return true
	}
	// The generated set leaves out the words that start a clause in the
	// reference, but not the ones that start a clause HERE: a pivot, an
	// unpivot and a sample all follow a relation, and reading PIVOT as the
	// name of one turned `(SELECT 1) PIVOT(...)` into a subquery called
	// PIVOT with a column list. These are the port's own grammar rather
	// than anything about the dialect, so they are named rather than probed.
	switch c.Type {
	case TokPIVOT, TokUNPIVOT, TokTABLE_SAMPLE:
		return false
	}
	_, ok := p.tables.TableAliasTokens[c.Type]
	return ok
}

// parseAliasColumns reads the `(a, b)` that may follow an alias, naming the
// columns of what it aliases. The same shape serves a table alias and a CTE:
// both carry it on a TableAlias.
func (p *parser) parseAliasColumns() ([]*Expression, error) {
	if !p.at(TokL_PAREN) {
		return nil, nil
	}
	p.advance()
	var columns []*Expression
	for !p.at(TokR_PAREN) {
		id, err := p.parseIdentifier()
		if err != nil {
			return nil, err
		}
		// A column may name its TYPE as well as itself, where what is being
		// aliased returns rows the caller has to describe:
		// `JSON_TO_RECORDSET(z) AS y("rank" INT)`.
		if !p.at(TokCOMMA) && !p.at(TokR_PAREN) {
			kind, err := p.parseDataType()
			if err != nil {
				return nil, err
			}
			columns = append(columns,
				New("ColumnDef", Arg{"this", id}, Arg{"kind", kind}))
		} else {
			columns = append(columns, id)
		}
		if !p.match(TokCOMMA) {
			break
		}
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed alias column list")
	}
	return columns, nil
}

// parseUnnest reads `UNNEST(x[, y]) [AS alias(cols)]`, which is an Unnest in
// the reference rather than a Table wrapping a call -- the distinction the
// TableFunctions table exists to catch. Entered with UNNEST current.
func (p *parser) parseUnnest() (*Expression, error) {
	p.advance()
	if !p.match(TokL_PAREN) {
		return nil, p.unsupported("UNNEST without arguments")
	}
	var items []*Expression
	for !p.at(TokR_PAREN) {
		e, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		items = append(items, e)
		if !p.match(TokCOMMA) {
			break
		}
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed UNNEST")
	}
	// WITH ORDINALITY is read BEFORE the alias here, unlike on a table, and
	// it starts life as a plain true.
	ordinality := p.atWords("WITH", "ORDINALITY")
	if ordinality {
		p.advance()
		p.advance()
	}
	alias, err := p.parseTableAlias()
	if err != nil {
		return nil, err
	}
	var offset any = ordinality
	// An alias with MORE columns than there are unnested expressions names
	// the ordinality column with its last one: `UNNEST(x) WITH ORDINALITY AS
	// t(a, b)` numbers the rows into b and leaves a for the values.
	if ordinality && alias != nil {
		columns, _ := alias.Args["columns"].([]*Expression)
		if len(items) < len(columns) {
			offset = columns[len(columns)-1]
			alias.Set("columns", columns[:len(columns)-1])
		}
	}
	// `WITH OFFSET [AS name]` names the same column another way, and the
	// reference writes every spelling of it back as WITH ORDINALITY -- which
	// drops the name. Refuse rather than write a column the statement did not
	// ask for.
	if !ordinality {
		if c := p.curr(); c != nil && c.Type == TokWITH {
			return nil, p.unsupported("UNNEST WITH OFFSET")
		}
	}
	return New("Unnest",
		Arg{"expressions", items},
		Arg{"alias", alias},
		Arg{"offset", offset},
		Arg{"explode_array", nil}), nil
}

// FOR SYSTEM_TIME, which asks a temporal table what it held at some point or
// over some range:
//
//	AS OF <when>              a moment
//	FROM <a> TO <b>           a range, held as a Tuple
//	BETWEEN <a> AND <b>       another range
//	CONTAINED IN (<a>, <b>)   and another
//	ALL                       no bound at all
//
// The kind is the WORDS, kept as a string; `this` is always TIMESTAMP.
func (p *parser) parseSystemTime() (*Expression, error) {
	p.advance() // FOR
	p.advance() // SYSTEM_TIME

	var kind string
	var expression *Expression
	pair := func(sep TokenType, word string) error {
		low, err := p.parseUnary()
		if err != nil {
			return err
		}
		if sep != TokUNKNOWN {
			if !p.match(sep) {
				return p.unsupported("FOR SYSTEM_TIME " + kind + " without " + word)
			}
		} else if !p.atWords(word) {
			return p.unsupported("FOR SYSTEM_TIME " + kind + " without " + word)
		} else {
			p.advance()
		}
		high, err := p.parseUnary()
		if err != nil {
			return err
		}
		expression = New("Tuple", Arg{"expressions", []*Expression{low, high}})
		return nil
	}

	switch {
	case p.atWords("AS", "OF"):
		p.advance()
		p.advance()
		kind = "AS OF"
		e, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		expression = e
	case p.atWords("ALL"):
		p.advance()
		kind = "ALL"
	case p.at(TokFROM):
		p.advance()
		kind = "FROM"
		if err := pair(TokUNKNOWN, "TO"); err != nil {
			return nil, err
		}
	case p.at(TokBETWEEN):
		p.advance()
		kind = "BETWEEN"
		if err := pair(TokAND, "AND"); err != nil {
			return nil, err
		}
	case p.atWords("CONTAINED", "IN"):
		p.advance()
		p.advance()
		kind = "CONTAINED IN"
		if !p.match(TokL_PAREN) {
			return nil, p.unsupported("CONTAINED IN without a range")
		}
		low, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		if !p.match(TokCOMMA) {
			return nil, p.unsupported("CONTAINED IN without two bounds")
		}
		high, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		if !p.match(TokR_PAREN) {
			return nil, p.unsupported("unclosed CONTAINED IN")
		}
		expression = New("Tuple", Arg{"expressions", []*Expression{low, high}})
	default:
		return nil, p.unsupported("FOR SYSTEM_TIME without a bound")
	}
	return New("Version",
		Arg{"this", "TIMESTAMP"},
		Arg{"expression", expression},
		Arg{"kind", kind}), nil
}

// parseHistoricalData reads `AT (VERSION => 3)` or `AT (TIMESTAMP => x)`,
// which names the state of a table at some point rather than its state now.
func (p *parser) parseHistoricalData() (*Expression, error) {
	word := strings.ToUpper(p.curr().Text)
	p.advance()
	p.advance() // the opening parenthesis
	kindToken := p.curr()
	if kindToken == nil {
		return nil, p.unsupported(word + " naming no kind of history")
	}
	kind := strings.ToUpper(kindToken.Text)
	p.advance()
	if !p.match(TokFARROW) {
		return nil, p.unsupported(word + " " + kind + " without a value")
	}
	at, err := p.parseExpression()
	if err != nil {
		return nil, err
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed " + word)
	}
	return New("HistoricalData",
		Arg{"this", word}, Arg{"kind", kind}, Arg{"expression", at}), nil
}

// LATERAL over a subquery or a function, which is a relation that may refer to
// the ones before it: `FROM foo, LATERAL (SELECT ... WHERE bar.id = foo.id)`.
//
// It goes where a table goes, so a comma join and an explicit JOIN both reach
// it by the same route. The alias belongs to the LATERAL rather than to what
// is inside it -- a subquery parsed here would otherwise take it -- and it may
// name the columns: `AS t(v, y)`.
//
// The same node serves CROSS APPLY, which fills a different set of its
// arguments; see parseApply. `view` and `outer` are FALSE here where APPLY
// leaves them off, and `cross_apply` is absent where APPLY sets it.
func (p *parser) parseLateral() (*Expression, error) {
	p.advance() // LATERAL

	var this *Expression
	var ordinality any
	switch {
	case p.at(TokL_PAREN):
		sub, err := p.parseSubqueryTable()
		if err != nil {
			return nil, err
		}
		// A subquery takes any alias that follows it, and here that alias is
		// the LATERAL's. Moved rather than re-parsed.
		this = sub
	case p.at(TokUNNEST) || (p.curr() != nil && strings.EqualFold(p.curr().Text, "UNNEST")):
		// In this position UNNEST is a RELATION, not the function the
		// expression grammar maps it to: the reference builds an Unnest
		// where a call would have built an Explode.
		target, err := p.parseUnnest()
		if err != nil {
			return nil, err
		}
		this = target
	case p.namesAFunctionCall() || p.atIdentifier():
		target, err := p.parseQualifiedCall()
		if err != nil {
			return nil, err
		}
		this = target
	default:
		return nil, p.unsupported("LATERAL over something the port cannot read")
	}

	// The alias belongs to the LATERAL, not to what is inside it. A subquery
	// and an UNNEST both take one while being parsed, so it is moved rather
	// than re-read -- and where one was moved, nothing more is read here.
	var alias *Expression
	if this.Class == "Subquery" || this.Class == "Unnest" {
		if inner, _ := this.Args["alias"].(*Expression); inner != nil {
			alias = inner
			this.Set("alias", nil)
		}
	}
	if alias == nil {
		// Everything else may take WITH ORDINALITY and then an alias, and
		// the reference records the ANSWER here -- false where the words are
		// absent -- while leaving the argument off entirely on the path that
		// moved an alias.
		found := p.atWords("WITH", "ORDINALITY")
		if found {
			p.advance()
			p.advance()
		}
		ordinality = found
		var err error
		alias, err = p.parseTableAlias()
		if err != nil {
			return nil, err
		}
	}
	return New("Lateral",
		Arg{"this", this},
		Arg{"view", false},
		Arg{"outer", false},
		Arg{"alias", alias},
		Arg{"cross_apply", nil},
		Arg{"ordinality", ordinality}), nil
}

// parseTableHint reads `WITH (NOLOCK, INDEX(i))`, the advice T-SQL gives an
// engine about how to read a table.
//
// A bare word is a Var and keeps the case it was written in; anything with
// parentheses is an ordinary call.
func (p *parser) parseTableHint() (*Expression, error) {
	p.advance() // WITH
	p.advance() // the opening parenthesis
	var members []*Expression
	for !p.at(TokR_PAREN) {
		c := p.curr()
		if c == nil {
			return nil, p.unsupported("unclosed table hint")
		}
		if n := p.next(); n != nil && (n.Type == TokCOMMA || n.Type == TokR_PAREN) {
			p.advance()
			members = append(members, New("Var", Arg{"this", c.Text}))
		} else {
			member, err := p.parseExpression()
			if err != nil {
				return nil, err
			}
			members = append(members, member)
		}
		if !p.match(TokCOMMA) {
			break
		}
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed table hint")
	}
	return New("WithTableHint", Arg{"expressions", members}), nil
}

// parseValuesTable reads a bare `VALUES (1), (2) AS t(c)` standing where a
// table would.
func (p *parser) parseValuesTable() (*Expression, error) {
	values, err := p.parseValues()
	if err != nil {
		return nil, err
	}
	alias, err := p.parseTableAlias()
	if err != nil {
		return nil, err
	}
	if alias != nil {
		values.Set("alias", alias)
	}
	return values, nil
}

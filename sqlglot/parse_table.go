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

// bareJoinKind reports whether a join with this KIND -- nil, or a word --
// takes `ON TRUE` in place of a missing ON at all. The reference's own list
// is an ALLOW list, not the CROSS/ARRAY it excludes elsewhere: a LEFT, RIGHT,
// FULL, SEMI or ANTI join with no ON stays exactly as bare as it was written,
// the same as CROSS and ARRAY do.
func bareJoinKind(kind *Token) bool {
	if kind == nil {
		return true
	}
	word := strings.ToUpper(kind.Text)
	return word == "INNER" || word == "OUTER"
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

	// DuckDB names a join's TYPE in a parenthetical of its own -- `JOIN BY
	// (TYPE left) b` -- which is the SIDE and KIND, read after the join is
	// built (DuckDBParser._parse_join).
	duckdbType := ""
	if p.dialect == "duckdb" && p.atWords("JOIN", "BY", "(", "TYPE") {
		p.advance() // JOIN
		p.advance() // BY
		p.advance() // (
		p.advance() // TYPE
		name := p.parseIDVarAny()
		if name == nil {
			return nil, p.unsupported("JOIN BY (TYPE ...) without a type")
		}
		duckdbType = strings.ToUpper(name.Name())
		if !p.match(TokR_PAREN) {
			return nil, p.unsupported("unclosed JOIN BY (TYPE ...)")
		}
		mark := p.index
		_ = mark
	}

	mark := p.index
	c := p.curr()
	if c == nil {
		return nil, nil
	}
	var method, side, kind *Token
	if p.dialect == "clickhouse" {
		// ClickHouse reads the parts in its OWN order -- GLOBAL, then a kind,
		// then a side, then another kind -- and folds the pair into
		// (global, side, kind), so `ANY LEFT JOIN` and `LEFT ANY JOIN` are
		// the same tree. Its JOIN_KINDS carry ALL, ANY, ASOF and ARRAY on top
		// of the base ones (ClickHouseParser._parse_join_parts).
		if p.at(TokGLOBAL) {
			method = p.curr()
			p.advance()
		}
		var kindPre *Token
		if c := p.curr(); c != nil {
			if _, ok := p.tables.JoinKinds[c.Type]; ok {
				kindPre = c
				p.advance()
			}
		}
		if c := p.curr(); c != nil {
			if _, ok := p.tables.JoinSides[c.Type]; ok {
				side = c
				p.advance()
			}
		}
		if c := p.curr(); c != nil {
			if _, ok := p.tables.JoinKinds[c.Type]; ok {
				kind = c
				p.advance()
			}
		}
		// `side or kind, kind_pre or kind`.
		if side == nil {
			side = kind
		}
		if kindPre != nil {
			kind = kindPre
		}
	} else {
		// A METHOD says how the rows are matched rather than which of them
		// are kept: NATURAL joins on every column the two tables share, ASOF
		// on the nearest match rather than an equal one.
		if _, isMethod := p.tables.JoinMethods[c.Type]; isMethod {
			// NATURAL joins on every column the two tables share; POSITIONAL
			// on the row's position in each; ASOF on the nearest row rather
			// than an equal one. All three are the method rather than the
			// side or the kind, and all three are written back the same way.
			if !strings.EqualFold(c.Text, "NATURAL") &&
				!strings.EqualFold(c.Text, "POSITIONAL") &&
				!strings.EqualFold(c.Text, "ASOF") {
				return nil, p.unsupported("join method " + c.Text)
			}
			method = c
			p.advance()
			c = p.curr()
			if c == nil {
				return nil, p.unsupported("a join method with no join")
			}
		}
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
	if duckdbType == "" && !p.match(TokJOIN) && !straight {
		// A side or a kind with no JOIN after it is not a join at all: it
		// belongs to what follows -- a set operation's own side and kind,
		// `SELECT 1 INNER UNION ALL SELECT 3` -- and the reference retreats
		// rather than refusing (Parser._parse_join).
		p.index = mark
		return nil, nil
	}

	table, err := p.parseTable()
	if err != nil {
		return nil, err
	}
	join := New("Join", Arg{"this", table})
	// `ARRAY JOIN arr1, arr2` names more than one array, and they ride as
	// further table references beside the first
	// (Parser._parse_join).
	if p.dialect == "clickhouse" && kind != nil && kind.Type == TokARRAY && p.match(TokCOMMA) {
		var extra []*Expression
		for {
			t, err := p.parseTable()
			if err != nil {
				return nil, err
			}
			extra = append(extra, t)
			if !p.match(TokCOMMA) {
				break
			}
		}
		join.Set("expressions", extra)
	}
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
		columns, err := p.parseUsingColumns()
		if err != nil {
			return nil, err
		}
		join.Set("using", columns)
	// Neither ON nor USING follows straight away, which is also what a JOIN
	// with none AT ALL looks like -- `a JOIN b JOIN c ON ... ON ...` reads b
	// alone here. The reference tries the joins that follow as NESTED under
	// b, and only keeps that reading if it turns out one of THOSE trailing
	// words belongs to b's own join after all: `a JOIN (b JOIN c ON b=c) ON
	// a=b`, the outer condition sitting past the inner one entirely. Method
	// joins, APPLY and UNNEST never take this shape, so they are not tried.
	case method == nil && (kind == nil || kind.Type != TokCROSS) && table.Class != "Unnest":
		mark := p.index
		var nested []*Expression
		for {
			j, err := p.parseJoin()
			if err != nil || j == nil {
				break
			}
			nested = append(nested, j)
		}
		switch {
		case len(nested) > 0 && p.match(TokON):
			on, err := p.parseDisjunction()
			if err != nil {
				return nil, err
			}
			table.Set("joins", nested)
			join.Set("on", on)
		case len(nested) > 0 && p.at(TokUSING):
			p.advance()
			columns, err := p.parseUsingColumns()
			if err != nil {
				return nil, err
			}
			table.Set("joins", nested)
			join.Set("using", columns)
		case p.tables.BareJoinIsOnTrue && bareJoinKind(kind):
			p.index = mark
			join.Set("on", New("Boolean", Arg{"this", true}))
		default:
			p.index = mark
		}
	case p.tables.BareJoinIsOnTrue && method == nil && bareJoinKind(kind):
		// Databricks records a bare JOIN as `ON TRUE` rather than leaving the
		// slot empty and writing the comma form. The same relation either way,
		// but a different tree and a different statement to the engine -- so
		// the two executors would not be sending the same SQL. Only a plain,
		// INNER or OUTER join gets this: CROSS and ARRAY joins never take an
		// ON at all, and one named by its METHOD is not touched either.
		join.Set("on", New("Boolean", Arg{"this", true}))
	}
	join, err = p.attachJoinPivots(join)
	if err != nil {
		return nil, err
	}
	if p.dialect == "clickhouse" {
		// GLOBAL is recorded on the join's own `global_` rather than as its
		// method, and an ARRAY JOIN names COLUMNS, not tables
		// (ClickHouseParser._parse_join).
		if method != nil {
			join.Set("method", nil)
			join.Set("global_", strings.ToUpper(method.Text))
		}
		if k, _ := join.Args["kind"].(string); k == "ARRAY" {
			replaceArrayJoinTables(join)
		}
	}
	if p.dialect == "duckdb" {
		if duckdbType != "" {
			// `BY (TYPE left_outer_join)` sets the SIDE and KIND after the
			// join is built, so they are the node's last arguments.
			parts := strings.Split(strings.TrimSuffix(duckdbType, "_JOIN"), "_")
			if len(parts) > 0 && (parts[0] == "LEFT" || parts[0] == "RIGHT" || parts[0] == "FULL") {
				join.Set("side", parts[0])
				parts = parts[1:]
			}
			if kind := strings.Join(parts, " "); kind != "" {
				join.Set("kind", kind)
			}
		}
		// `[APPROX] NEAREST n BY DISTANCE|SIMILARITY <expr>` follows the join
		// (DuckDBParser._parse_join).
		approximate, hasNearest := false, false
		if p.atWords("APPROX", "NEAREST") {
			p.advance()
			p.advance()
			approximate, hasNearest = true, true
		} else if p.atWords("NEAREST") {
			p.advance()
			hasNearest = true
		}
		if hasNearest {
			c := p.curr()
			if c == nil || c.Type != TokNUMBER {
				return nil, p.unsupported("NEAREST without a count")
			}
			p.advance()
			count := New("Literal", Arg{"this", c.Text}, Arg{"is_string", false})
			if !p.atWords("BY") {
				return nil, p.unsupported("NEAREST without BY")
			}
			p.advance()
			similarity := false
			switch {
			case p.atWords("DISTANCE"):
				p.advance()
			case p.atWords("SIMILARITY"):
				p.advance()
				similarity = true
			default:
				return nil, p.unsupported("NEAREST without DISTANCE or SIMILARITY")
			}
			expr, err := p.parseDisjunction()
			if err != nil {
				return nil, err
			}
			join.Set("nearest", New("Nearest",
				Arg{"this", expr}, Arg{"count", count},
				Arg{"approximate", approximate}, Arg{"similarity", similarity}))
		}
	}
	return join, nil
}

// parseUsingColumns reads `(x, y)` after USING, already past the word
// itself. The reference keeps the columns as bare IDENTIFIERS, not columns.
func (p *parser) parseUsingColumns() ([]*Expression, error) {
	// The parentheses are OPTIONAL: `USING id, name` and `USING (id, name)`
	// are the same join (Parser._parse_using_identifiers).
	wrapped := p.match(TokL_PAREN)
	var columns []*Expression
	for p.atIdentifier() {
		column, err := p.parseIdentifier()
		if err != nil {
			return nil, err
		}
		columns = append(columns, column)
		if !p.match(TokCOMMA) {
			break
		}
	}
	if wrapped && !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed USING")
	}
	return columns, nil
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
	join := New("Join", Arg{"this", lateral}, Arg{"pivots", nil})
	// APPLY is a bare join -- a kind with no ON and no USING -- so where the
	// dialect records a bare join as `ON TRUE`, it records this one that way
	// too. The kind check cannot exclude it: the kind of an APPLY is written
	// on the Lateral, not on the Join (Parser._parse_join, ADD_JOIN_ON_TRUE).
	if p.tables.BareJoinIsOnTrue {
		join.Set("on", New("Boolean", Arg{"this", true}))
	}
	return join, nil
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
	case TokL_BRACE:
		return p.dialect == "dax"
	}
	_, name := p.tables.IDVarTokens[t.Type]
	return name
}

// tableQualifiedParts builds a Table's this/db/catalog from a left-to-right
// dotted sequence, mirroring the reference's own sequential read: catalog and
// db are filled from the first two positions it fills with something REAL --
// a T-SQL `a..b` skip (nil here) does not count as filling one -- and
// everything after that nests in DOTS over the table position instead of
// ever being refused as over-qualified.
//
// A position that is reached but skipped reports as the EMPTY STRING, the
// same as the reference's own `""` for a `.` that named nothing; a position
// never reached at all is left absent (Go nil), which is a different thing
// the dump must tell apart.
func tableQualifiedParts(seq []*Expression) (this, db, catalog any) {
	// Each slot carries the part AND whether it was WRITTEN. The name before
	// any dot may simply not be there -- `FROM .a` -- and the reference has
	// None in that slot, which is dropped. A part AFTER a dot that is not
	// there is the empty string instead, and is KEPT: `FROM a..b` names the
	// empty qualifier (Parser._parse_table_parts, `_parse_table_part() or ""`).
	thisExpr := seq[0]
	thisWritten := thisExpr != nil
	var dbExpr, catalogExpr *Expression
	var dbWritten, catalogWritten bool
	catalogTruthy := false
	for _, next := range seq[1:] {
		if catalogTruthy {
			// Past the first qualifier the name nests in Dots, and the same
			// rule holds inside them: a part that is not there is the empty
			// string, never a dropped key (`a...b`).
			thisExpr = New("Dot",
				Arg{"this", qualifierValue(thisExpr, thisWritten)},
				Arg{"expression", next})
			thisWritten = true
			continue
		}
		catalogExpr, catalogWritten = dbExpr, dbWritten
		dbExpr, dbWritten = thisExpr, thisWritten
		thisExpr = next
		// Past the first dot a missing part is the empty string, so the slot
		// counts as written from here on.
		thisWritten = true
		catalogTruthy = catalogExpr != nil
	}
	return qualifierValue(thisExpr, true), qualifierValue(dbExpr, dbWritten), qualifierValue(catalogExpr, catalogWritten)
}

// qualifierValue turns one slot into the argument to carry: the part itself
// when it was written, the empty string when it was a dot with nothing after
// it, and NOTHING at all -- nil, so the caller sets no key -- when the name
// before any dot was never there.
func qualifierValue(part *Expression, written bool) any {
	if !written {
		return nil
	}
	if part == nil {
		return ""
	}
	return part
}

// redshiftSuperUnpivot reads the relation after UNPIVOT and wraps it. The
// word itself is consumed by the caller. `t UNPIVOT (...)` is the postfix
// clause and is read later, once a table is already underway.
func (p *parser) redshiftSuperUnpivot() (*Expression, error) {
	p.advance()
	relation, err := p.parseTable()
	if err != nil {
		return nil, err
	}
	// `c.c_orders[0]` subscripts the value being unpivoted. The index stays
	// the integer that was written: Redshift's offset does not shift it.
	for p.at(TokL_BRACKET) {
		p.advance()
		if p.at(TokR_BRACKET) {
			return nil, p.unsupported("subscript with no index")
		}
		items, err := p.parseBracketItems(true)
		if err != nil {
			return nil, err
		}
		relation = New("Bracket", Arg{"this", relation}, Arg{"expressions", items})
	}
	return New("Pivot", Arg{"this", relation}, Arg{"unpivot", true}), nil
}

func (p *parser) parseTable() (*Expression, error) {
	if p.dialect == "redshift" && p.at(TokUNPIVOT) {
		return p.redshiftSuperUnpivot()
	}
	// ClickHouse lets an ARRAY take the place of a table in an ARRAY JOIN:
	// `ARRAY JOIN [1, 2, 3] AS arr` (Parser._parse_table, an Alias over an
	// Array rather than a Table).
	if p.dialect == "clickhouse" && p.at(TokL_BRACKET) {
		arr, err := p.parsePrimary()
		if err != nil {
			return nil, err
		}
		ta, err := p.parseTableAlias()
		if err != nil {
			return nil, err
		}
		if ta != nil {
			alias := ta
			if inner, _ := ta.Args["this"].(*Expression); inner != nil {
				alias = inner
			}
			return New("Alias", Arg{"this", arr}, Arg{"alias", alias}), nil
		}
		return arr, nil
	}
	// DuckDB names a relation in FRONT of it too: `FROM foo: bar` is
	// `FROM bar AS foo`, the same prefix alias the projection list takes.
	if p.tables.PrefixAlias && p.atAliasName() {
		if n := p.next(); n != nil && n.Type == TokCOLON {
			alias, err := p.parseIdentifier()
			if err != nil {
				return nil, err
			}
			p.advance() // the colon
			colonComments := p.takeComments()
			named, err := p.parseTable()
			if err != nil {
				return nil, err
			}
			if existing, _ := named.Args["alias"].(*Expression); existing != nil {
				return nil, p.unsupported("a relation named twice")
			}
			ta := New("TableAlias", Arg{"this", alias})
			liftComments(alias, ta)
			putComments(ta, colonComments)
			liftComments(named, ta)
			named.Set("alias", ta)
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
	// A ClickHouse query parameter may name a table: `FROM {table: Identifier}`
	// (ClickHouseParser.PLACEHOLDER_PARSERS, _parse_query_parameter).
	if p.dialect == "clickhouse" && p.at(TokL_BRACE) {
		if ph, handled := p.parseClickhouseQueryParameter(); handled {
			return p.tableRest(New("Table", Arg{"this", ph}))
		}
	}
	// Databricks reads a bare `{df}` in a FROM clause as a notebook widget
	// naming the table, the same Placeholder any other expression reads it
	// as.
	if widget := p.parseWidgetPlaceholder(); widget != nil {
		return p.tableRest(New("Table", Arg{"this", widget}))
	}
	// XMLTABLE shreds a document into rows, and stands where a table does --
	// a function call with a grammar of its own rather than the generic one,
	// which is why it is claimed here before the generic reader gets it.
	if p.atWords("XMLTABLE") && p.next() != nil && p.next().Type == TokL_PAREN {
		xmlTable, err := p.parseXMLTable()
		if err != nil {
			return nil, err
		}
		return p.tableRest(New("Table", Arg{"this", xmlTable}))
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
		//
		// T-SQL's bare table hint looks like a call and is not one:
		// `FROM dbo.t (NOLOCK)`.
		if p.namesAFunctionCall() && !p.atBareTableHintCall() {
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
			f, err := p.tableCall()
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

	// BigQuery reads a trailing star as part of the name -- `x.y*` names every
	// table whose name begins with `y`, and `x.*` names the star itself --
	// and only when the star is written with no space before it
	// (Parser._parse_table_parts, wildcard=True).
	if p.dialect == "bigquery" && p.at(TokSTAR) && p.connectedToPrev() {
		if n := len(parts); n > 0 && parts[n-1] == nil {
			// A bare Identifier, with no `quoted` argument at all
			// (Parser._parse_table_parts).
			parts[n-1] = New("Identifier", Arg{"this", "*"})
		} else if n := len(parts); n > 0 && parts[n-1].Class == "Identifier" {
			parts[n-1].Set("this", parts[n-1].Name()+"*")
		}
		p.advance()
	}

	// The dotted sequence is every part read plus, for a table function, the
	// call itself standing in for the final name -- `a.b.c.f()` qualifies
	// `f()` exactly the way `a.b.c.d` qualifies `d`.
	seq := parts
	if fn != nil {
		seq = append(append([]*Expression{}, parts...), fn)
	}
	this, db, catalog := tableQualifiedParts(seq)
	table := New("Table", Arg{"this", this}, Arg{"db", db}, Arg{"catalog", catalog})
	liftPart(this, table)
	liftPart(db, table)
	liftPart(catalog, table)

	// BigQuery reads a qualified name its own way: a float token can swallow
	// the dot that separated two parts, a name written inside ONE pair of
	// backticks carries every dot, and its INFORMATION_SCHEMA views merge the
	// last two parts (BigQueryParser._parse_table_parts).
	if p.dialect == "bigquery" && fn == nil {
		table = bigqueryTableParts(table)
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
	return p.tableRest(New("Table", Arg{"rows_from", tables}))
}

// parseTablePartition reads `PARTITION(p0)` or `SUBPARTITION(p0)` when the
// list is actually there. The flag is recorded even when it is false, which
// is the tree the reference builds for a plain PARTITION.
func (p *parser) parseTablePartition() (*Expression, error) {
	sub := p.atWords("SUBPARTITION")
	if !sub && !p.at(TokPARTITION) {
		return nil, nil
	}
	if n := p.next(); n == nil || n.Type != TokL_PAREN {
		return nil, nil
	}
	p.advance()
	p.advance()
	var exprs []*Expression
	if !p.at(TokR_PAREN) {
		for {
			e, err := p.parseDisjunction()
			if err != nil {
				return nil, err
			}
			exprs = append(exprs, e)
			if !p.match(TokCOMMA) {
				break
			}
		}
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed partition list")
	}
	if len(exprs) == 0 {
		return nil, p.unsupported("PARTITION without a name")
	}
	return New("Partition", Arg{"subpartition", sub}, Arg{"expressions", exprs}), nil
}

// tableRest reads everything that may FOLLOW a table's name -- its temporal
// clause, its alias, its hints, its sample and its pivots -- in the order the
// reference reads them, which is the order they are written.
// tableAtIndex reads `c.c_orders AS orders AT index`. The reference converts
// the table into a column — its schema qualifier becomes the column's table —
// and the alias wraps that column. `AT (` is time travel and never reaches here.
func (p *parser) tableAtIndex(table *Expression) (*Expression, error) {
	p.advance() // AT
	index, err := p.parseIdentifier()
	if err != nil {
		return nil, err
	}
	column := New("Column", Arg{"this", table.Args["this"]})
	if db, _ := table.Args["db"].(*Expression); db != nil {
		column.Set("table", db)
	}
	if catalog, _ := table.Args["catalog"].(*Expression); catalog != nil {
		column.Set("db", catalog)
	}
	this := any(column)
	if alias, _ := table.Args["alias"].(*Expression); alias != nil {
		this = New("Alias", Arg{"this", column}, Arg{"alias", alias.Args["this"]})
	}
	return New("AtIndex", Arg{"this", this}, Arg{"expression", index}), nil
}

func (p *parser) tableRest(table *Expression) (*Expression, error) {
	// MySQL names the partition being read on the table itself:
	// `FROM t1 PARTITION(p0)`. It is part of the name, and it stands before
	// the alias. A word with no list after it is still an alias. StarRocks
	// and Doris inherit MySQL's SUPPORTS_PARTITION_SELECTION, so they read it
	// too -- REFRESH EXTERNAL TABLE t PARTITION(...) among them.
	if p.dialect == "mysql" || p.dialect == "doris" || p.dialect == "starrocks" {
		part, err := p.parseTablePartition()
		if err != nil {
			return nil, err
		}
		if part != nil {
			table.Set("partition", part)
		}
	}
	// The time-travel clause -- T-SQL's FOR SYSTEM_TIME, Databricks' TIMESTAMP
	// AS OF, Trino's FOR VERSION AS OF -- reads the table as it stood then,
	// and goes before the alias.
	if word, ok := p.versionPhrase(); ok {
		version, err := p.parseVersion(word)
		if err != nil {
			return nil, err
		}
		table.Set("version", version)
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
	// T-SQL reads `dbo.t (NOLOCK)` as a bare table HINT, not as an alias
	// whose column list happens to hold one word: the parenthesis arrives as
	// an unnamed alias, and is moved onto the table as a WithTableHint
	// (TSQLParser._parse_table).
	if (p.dialect == "tsql" || p.dialect == "fabric") && table.Class == "Table" &&
		table.Args["hints"] == nil && alias != nil {
		if hint := bareTableHintOf(alias); hint != nil && tablePartsAreIdentifiers(table) {
			alias.Set("columns", nil)
			// The unnamed alias was only the hint: whatever follows is the
			// table's REAL alias, read now.
			if name, _ := alias.Args["this"].(*Expression); name == nil {
				realAlias, aerr := p.parseTableAlias()
				if aerr != nil {
					return nil, aerr
				}
				table.Set("alias", realAlias)
			}
			table.Set("hints", []*Expression{New("WithTableHint",
				Arg{"expressions", []*Expression{New("Var", Arg{"this", hint.Name()})}})})
		}
	}
	// The time-travel clause may also stand AFTER the alias --
	// `FROM foo AS t0 FOR SYSTEM_TIME AS OF z` (Parser._parse_table).
	if word, ok := p.versionPhrase(); ok {
		version, err := p.parseVersion(word)
		if err != nil {
			return nil, err
		}
		table.Set("version", version)
	}
	// And it may stand AFTER the alias too.
	if p.atWords("AT") && p.next() != nil && p.next().Type == TokL_PAREN {
		when, err := p.parseHistoricalData()
		if err != nil {
			return nil, err
		}
		table.Set("when", when)
	}
	// Redshift names one element of a SUPER array here. The reference returns
	// that node immediately, so hints and pivots are not read after it.
	if table.Class == "Table" && p.atWords("AT") {
		return p.tableAtIndex(table)
	}
	// T-SQL's locking hints come after the alias: `FROM a AS b WITH (NOLOCK)`.
	// RisingWave has no table hints. WITH ( ... ) after a name is the
	// sink's own property list, and reading it here would swallow that.
	if p.dialect != "risingwave" && p.at(TokWITH) && p.next() != nil && p.next().Type == TokL_PAREN {
		hint, err := p.parseTableHint()
		if err != nil {
			return nil, err
		}
		table.Set("hints", []*Expression{hint})
	}
	if p.dialect == "mysql" {
		hints, err := p.parseIndexTableHints()
		if err != nil {
			return nil, err
		}
		if len(hints) > 0 {
			table.Set("hints", hints)
		}
	}
	// TABLESAMPLE hangs off the TABLE, after its alias.
	if p.at(TokTABLE_SAMPLE) {
		sample, err := p.parseTableSample()
		if err != nil {
			return nil, err
		}
		table.Set("sample", sample)
	}
	if err := p.oracleSampleAlias(table); err != nil {
		return nil, err
	}
	// Hive, Spark2 and Spark write the alias AFTER the sample
	// (ALIAS_POST_TABLESAMPLE), where every other dialect writes it before.
	if isHiveFamily(p.dialect) && table.Args["sample"] != nil && table.Args["alias"] == nil {
		alias, err := p.parseTableAlias()
		if err != nil {
			return nil, err
		}
		if alias != nil {
			table.Set("alias", alias)
		}
	}
	// SQLite names the index the table is read through, or that none may be
	// used (the reference's `_parse_table`, after the alias).
	if p.at(TokINDEXED_BY) {
		p.advance()
		indexed, err := p.parseTableName()
		if err != nil {
			return nil, err
		}
		table.Set("indexed", indexed)
	} else if p.atWords("NOT", "INDEXED") {
		p.advance()
		p.advance()
		table.Set("indexed", false)
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
	// ClickHouse's FINAL reads the table as it stands after merges, and wraps
	// the relation it follows (ClickHouseParser._parse_table).
	if p.dialect == "clickhouse" && p.at(TokFINAL) {
		p.advance()
		table = New("Final", Arg{"this", table})
	}
	// ClickHouse names the one output column a `generate_series(n) AS g` gives
	// after the function itself, on the alias
	// (ClickHouseParser._parse_table).
	if p.dialect == "clickhouse" && table.Class == "Table" {
		if inner, _ := table.Args["this"].(*Expression); inner != nil && inner.Class == "GenerateSeries" {
			if alias, _ := table.Args["alias"].(*Expression); alias != nil && alias.Args["columns"] == nil {
				alias.Set("columns", []*Expression{
					New("Identifier", Arg{"this", "generate_series"}, Arg{"quoted", false})})
			}
		}
	}
	return liftTableFromRows(table)
}

// liftTableFromRows turns the table a TABLE(...) call was wrapped in into
// the row source itself. The alias, the joins, the pivots, and the sample
// belong to that source (Parser._parse_table).
func liftTableFromRows(table *Expression) (*Expression, error) {
	if table == nil || table.Class != "Table" {
		return table, nil
	}
	inner, _ := table.Args["this"].(*Expression)
	if inner == nil || inner.Class != "TableFromRows" {
		return table, nil
	}
	for _, key := range []string{"alias", "joins", "pivots", "sample"} {
		if value, ok := table.Args[key]; ok {
			inner.Set(key, value)
		}
	}
	return inner, nil
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
		target := unpivotTarget(p.pivotAggregate(agg), unpivot)
		// An UNPIVOT names its value columns, and a parenthesised list of
		// them is a Tuple whose members are Identifiers too.
		if unpivot && target != nil && target.Class == "Tuple" {
			if members, _ := target.Args["expressions"].([]*Expression); members != nil {
				for i, m := range members {
					members[i] = unpivotTarget(m, true)
				}
			}
		}
		aggregates = append(aggregates, target)
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
	var in *Expression
	var values []*Expression
	if !p.match(TokL_PAREN) {
		// `IN y_enum` names an enum rather than listing values: the reference
		// reads a bare word into its own key, FIELD, and the In carries no
		// list at all -- not an empty one.
		name, err := p.parseIdentifier()
		if err != nil {
			return nil, err
		}
		in = New("In", Arg{"this", unpivotTarget(field, unpivot)}, Arg{"field", name})
	} else {
		for {
			v, err := p.parseUnary()
			if err != nil {
				return nil, err
			}
			// `IN (q1 AS `Jan-Mar`)` names the output column. The reference
			// builds a PivotAlias for it, not the ordinary Alias.
			if p.match(TokALIAS) {
				v, err = p.pivotInAlias(v)
				if err != nil {
					return nil, err
				}
			}
			values = append(values, v)
			if !p.match(TokCOMMA) {
				break
			}
		}
		if !p.match(TokR_PAREN) {
			return nil, p.unsupported("unclosed PIVOT list")
		}
		in = New("In", Arg{"this", unpivotTarget(field, unpivot)},
			Arg{"expressions", values})
	}
	// DuckDB's own PIVOT may GROUP BY inside its own parentheses, the values
	// it groups rather than anything the SELECT around it groups. An ORDER
	// BY there is a shape this still does not model.
	var group *Expression
	if p.at(TokGROUP_BY) {
		p.advance()
		var columns []*Expression
		for {
			col, err := p.parseUnary()
			if err != nil {
				return nil, err
			}
			columns = append(columns, col)
			if !p.match(TokCOMMA) {
				break
			}
		}
		group = New("Group", Arg{"expressions", columns})
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed PIVOT")
	}
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
		Arg{"group", group})

	// The alias belongs to the last pivot in a chain. Oracle records it
	// before the derived column names; an UNPIVOT records it after the
	// value-column flag and has no derived names.
	var alias *Expression
	if !p.at(TokPIVOT) && !p.at(TokUNPIVOT) {
		alias, err = p.parseTableAlias()
		if err != nil {
			return nil, err
		}
	}
	if unpivot {
		args = append(args, Arg{"value_columns_first", p.tables.UnpivotValueColumnsFirst})
		if alias != nil {
			args = append(args, Arg{"alias", alias})
		}
	} else {
		if alias != nil {
			args = append(args, Arg{"alias", alias})
		}
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
	// Spark names the columns after the value ALONE when there is only one
	// aggregate, and after value+aggregate once there are several.
	if p.tables.PivotColumnNaming == "agg_name_if_multiple" && len(aggregates) == 1 {
		names = nil
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
		// StarRocks names a multi-column pivot value by its members in
		// braces: `FOR (c2, c3) IN ((1, 1))` is the column `{1,1}`
		// (StarRocksParser._parse_pivot).
		if p.dialect == "starrocks" && value.Class == "Tuple" {
			members, _ := value.Args["expressions"].([]*Expression)
			parts := make([]string, 0, len(members))
			for _, member := range members {
				text, err := Generate(member, p.dialect)
				if err != nil {
					return nil, err
				}
				parts = append(parts, text)
			}
			base = "{" + strings.Join(parts, ",") + "}"
		}
		if len(names) == 0 {
			out = append(out, identifierFor(base))
			continue
		}
		for _, name := range names {
			// The reference builds the product of the IN values with the
			// aggregate names LAST, and a dialect that PREFIXES its pivot
			// columns moves that name to the front: BigQuery writes `d_x`
			// where the plain spelling is `x_d` (Parser._parse_pivot).
			if p.tables.PivotPrefixesColumns {
				out = append(out, identifierFor(name+"_"+base))
			} else {
				out = append(out, identifierFor(base+"_"+name))
			}
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
	// A dialect with TABLESAMPLE_CSV takes a comma-separated list of SIZES
	// and no parentheses: `SAMPLE 0.33, .25, .1`, and `SAMPLE 5` is one of
	// them (Parser._parse_table_sample).
	if p.tables.TableSampleCSV {
		var items []*Expression
		for {
			item, err := p.parsePrimary()
			if err != nil {
				return nil, err
			}
			items = append(items, item)
			if !p.match(TokCOMMA) {
				break
			}
		}
		return New("TableSample", Arg{"expressions", items}), nil
	}
	// ClickHouse says `SAMPLE <expr>` with no parentheses and no method
	// (Parser._parse_table_sample, TABLESAMPLE_REQUIRES_PARENS).
	if p.dialect == "clickhouse" {
		size, err := p.parseFactor()
		if err != nil {
			return nil, err
		}
		return New("TableSample", Arg{"size", size}), nil
	}

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

	if p.atWords("REPEATABLE") || p.atSampleSeed() {
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

// parseXMLTable reads `XMLTABLE([XMLNAMESPACES(...),] 'path' [PASSING [BY
// VALUE] cols] [RETURNING SEQUENCE BY REF] [COLUMNS coldefs])`, which shreds
// an XML document into rows -- one PATH-carrying column definition per
// column, read the same way a table's own are.
func (p *parser) parseXMLTable() (*Expression, error) {
	p.advance() // XMLTABLE
	p.advance() // the opening parenthesis

	var namespaces []*Expression
	if p.atWords("XMLNAMESPACES") && p.next() != nil && p.next().Type == TokL_PAREN {
		p.advance()
		p.advance()
		for {
			var value *Expression
			if p.match(TokDEFAULT) {
				uri := p.curr()
				if uri == nil || uri.Type != TokSTRING {
					return nil, p.unsupported("XMLNAMESPACES DEFAULT without a string")
				}
				p.advance()
				value = New("Literal", Arg{"this", uri.Text}, Arg{"is_string", true})
			} else {
				uri := p.curr()
				if uri == nil || uri.Type != TokSTRING {
					return nil, p.unsupported("an XML namespace without a URI")
				}
				p.advance()
				literal := New("Literal", Arg{"this", uri.Text}, Arg{"is_string", true})
				aliased, err := p.parseAlias(literal)
				if err != nil {
					return nil, err
				}
				value = aliased
			}
			namespaces = append(namespaces, New("XMLNamespace", Arg{"this", value}))
			if !p.match(TokCOMMA) {
				break
			}
		}
		if !p.match(TokR_PAREN) {
			return nil, p.unsupported("unclosed XMLNAMESPACES")
		}
		p.match(TokCOMMA)
	}

	path := p.curr()
	if path == nil || path.Type != TokSTRING {
		return nil, p.unsupported("XMLTABLE without a path")
	}
	p.advance()
	this := New("Literal", Arg{"this", path.Text}, Arg{"is_string", true})

	var passing []*Expression
	if p.atWords("PASSING") {
		p.advance()
		// BY VALUE says nothing a reference has any other way to write: it
		// is the only way XMLTABLE passes anything.
		if p.atWords("BY") && p.nextWords("VALUE") {
			p.advance()
			p.advance()
		}
		for {
			col, err := p.oracleXMLPassing()
			if err != nil {
				return nil, err
			}
			passing = append(passing, col)
			if !p.match(TokCOMMA) {
				break
			}
		}
	}

	byRef := false
	if p.atWords("RETURNING") && p.nextWords("SEQUENCE") {
		p.advance()
		p.advance()
		if !p.atWords("BY") || !p.nextWords("REF") {
			return nil, p.unsupported("RETURNING SEQUENCE without BY REF")
		}
		p.advance()
		p.advance()
		byRef = true
	}

	var columns []*Expression
	if p.atWords("COLUMNS") {
		p.advance()
		cols, err := p.parseXMLTableColumns()
		if err != nil {
			return nil, err
		}
		columns = cols
	}

	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed XMLTABLE")
	}
	return New("XMLTable",
		Arg{"this", this}, Arg{"namespaces", namespaces}, Arg{"passing", passing},
		Arg{"columns", columns}, Arg{"by_ref", byRef}), nil
}

// parseXMLTableColumns reads XMLTABLE's own column list: name, type, and
// constraints, comma-separated with no parentheses around them -- unlike a
// CREATE TABLE's, which always stand inside a pair.
func (p *parser) parseXMLTableColumns() ([]*Expression, error) {
	var out []*Expression
	for {
		name, err := p.parseIdentifier()
		if err != nil {
			return nil, err
		}
		mark := p.index
		kind, kerr := p.parseColumnType()
		if kerr != nil {
			p.index = mark
			kind = nil
		}
		constraints, err := p.parseColumnConstraints()
		if err != nil {
			return nil, err
		}
		def := New("ColumnDef", Arg{"this", name}, Arg{"kind", kind})
		if len(constraints) > 0 {
			def.Set("constraints", constraints)
		}
		out = append(out, def)
		if !p.match(TokCOMMA) {
			return out, nil
		}
	}
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
		closing := p.takeComments()
		alias, err := p.parseTableAlias()
		if err != nil {
			return nil, err
		}
		if alias != nil {
			values.Set("alias", alias)
		}
		return putComments(values, closing), nil
	}
	if p.dialect == "tsql" && p.at(TokMERGE) {
		// T-SQL lets a MERGE stand where a table would, its OUTPUT rows
		// feeding an outer query -- `INSERT ... SELECT ... FROM (MERGE
		// ... OUTPUT ...) AS Changes(...)`. The reference reads it here,
		// in the very same slot a parenthesised SELECT is read from, and
		// wraps it in the same Subquery.
		inner, err = p.parseMerge(true)
	} else if p.at(TokSELECT) || p.at(TokWITH) || p.at(TokPIVOT) || p.at(TokUNPIVOT) ||
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
	// tree the reference never makes -- DESCRIBE itself reads fine here,
	// closed by the caller's own parenthesis rather than needing to be the
	// whole statement.
	if p.at(TokDESCRIBE) || p.at(TokDESC) {
		return p.parseDescribeBody()
	}
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
	joins, err := p.parseJoins()
	if err != nil {
		return nil, err
	}
	if table.Class == "Values" && len(joins) > 0 {
		alias, _ := table.Args["alias"].(*Expression)
		table.Set("alias", nil)
		wrapped := New("Table", Arg{"this", table})
		if alias != nil {
			wrapped.Set("alias", alias)
		}
		table = wrapped
	}
	if len(joins) > 0 {
		table.Set("joins", joins)
	}
	// A LATERAL VIEW inside the parentheses hangs off the TABLE the joins
	// hang off, the same as outside them it hangs off the SELECT -- the
	// position, not the clause itself, decides where it lands.
	var laterals []*Expression
	for p.atWords("LATERAL", "VIEW") {
		view, err := p.parseLateralView()
		if err != nil {
			return nil, err
		}
		laterals = append(laterals, view)
	}
	if len(laterals) > 0 {
		table.Set("laterals", laterals)
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
	// Teradata's `col (FORMAT '...')` is a column PHRASE rather than a call.
	if p.formatPhraseFollows() {
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
	// DuckDB's nearest-neighbour clause follows the table and is not an
	// alias: `b NEAREST 1 BY ...` / `b APPROX NEAREST ...`
	// (DuckDBParser._parse_table_alias).
	if p.dialect == "duckdb" {
		if p.atWords("NEAREST") {
			if n := p.next(); n != nil && n.Type == TokNUMBER {
				return nil, nil
			}
		}
		if p.atWords("APPROX", "NEAREST") {
			return nil, nil
		}
	}
	explicit := p.match(TokALIAS)
	if !explicit && !p.atAliasName() {
		// T-SQL's bare table hint arrives as an UNNAMED alias whose column
		// list holds one word -- `FROM dbo.t (NOLOCK)` -- and _parse_table
		// turns it into a hint (Parser._parse_table_alias reads the
		// parentheses even with no name).
		if (p.dialect == "tsql" || p.dialect == "fabric") && p.at(TokL_PAREN) {
			columns, cerr := p.parseAliasColumns()
			if cerr != nil {
				return nil, cerr
			}
			if len(columns) == 0 {
				return nil, nil
			}
			return New("TableAlias", Arg{"columns", columns}), nil
		}
		return nil, nil
	}
	// The NAME may be absent where a column list follows: `UNNEST(x) AS (a)`.
	if p.at(TokL_PAREN) {
		columns, err := p.parseAliasColumns()
		if err != nil {
			return nil, err
		}
		if len(columns) == 0 {
			return nil, nil
		}
		return New("TableAlias", Arg{"columns", columns}), nil
	}
	id, err := p.parseIdentifier()
	if err != nil {
		// After a written AS, ANY token names the alias -- `FROM x AS prewhere`
		// (Parser._parse_table_alias, _parse_id_var(any_token=True)).
		if !explicit {
			return nil, err
		}
		if any := p.advanceAnyIdentifier(); any != nil {
			id = any
		} else {
			return nil, err
		}
	}
	columns, err := p.parseAliasColumns()
	if err != nil {
		return nil, err
	}
	node := New("TableAlias", Arg{"this", id}, Arg{"columns", columns})
	liftComments(id, node)
	return node, nil
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
		// After a written AS, ANY token names the alias -- `FROM x AS prewhere`
		// (Parser._parse_table_alias, _parse_id_var(any_token=True)).
		if !explicit {
			return nil, err
		}
		if any := p.advanceAnyIdentifier(); any != nil {
			id = any
		} else {
			return nil, err
		}
	}
	columns, err := p.parseAliasColumns()
	if err != nil {
		return nil, err
	}
	node := New("TableAlias", Arg{"this", id}, Arg{"columns", columns})
	liftComments(id, node)
	return node, nil
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
	// START is never an implicit alias when WITH follows: that would swallow
	// the beginning of a START WITH ... CONNECT BY clause.
	if p.atWords("START", "WITH") {
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
	return p.parseUnnestWithAlias(true)
}

// parseUnnestWithAlias reads an UNNEST. `IN UNNEST(...)` takes the same shape
// but never an alias, so the reference parses it with with_alias=False
// (Parser._parse_in, Parser._parse_unnest).
func (p *parser) parseUnnestWithAlias(withAlias bool) (*Expression, error) {
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
	var alias *Expression
	var err error
	if withAlias {
		alias, err = p.parseTableAlias()
		if err != nil {
			return nil, err
		}
		// BigQuery's UNNEST_COLUMN_ONLY moves the alias's name into `columns`:
		// the name refers to the unnested COLUMN, not to a table
		// (Parser._parse_unnest).
		if p.dialect == "bigquery" && alias != nil {
			if this, _ := alias.Args["this"].(*Expression); this != nil {
				alias.Set("columns", []*Expression{this})
				alias.Set("this", nil)
			}
		}
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
		if c := p.curr(); c != nil && c.Type == TokWITH && p.nextWords("OFFSET") {
			// `WITH OFFSET [AS name]` names the ordinality column, and a name
			// that was not written is `offset` (Parser._parse_unnest). Only
			// the dialects that WRITE it back keep the name, so the rest
			// still refuse rather than silently shortening the statement.
			if p.dialect != "bigquery" {
				return nil, p.unsupported("UNNEST WITH OFFSET")
			}
			p.advance() // WITH
			p.advance() // OFFSET
			p.match(TokALIAS)
			// A name that was not written is `offset`; the reader takes a
			// NAME only where one can stand (Parser._parse_unnest,
			// _parse_id_var).
			var name *Expression
			if p.atIdentifier() {
				name, err = p.parseIdentifier()
				if err != nil {
					return nil, err
				}
			}
			if name == nil {
				name = New("Identifier", Arg{"this", "offset"}, Arg{"quoted", false})
			}
			offset = name
		}
	}
	// StarRocks defaults the UNNEST table alias and column to `unnest` when
	// the statement does not name them (StarRocksParser._parse_unnest).
	if p.dialect == "starrocks" && withAlias {
		unnestIdent := func() *Expression {
			return New("Identifier", Arg{"this", "unnest"}, Arg{"quoted", false})
		}
		if alias == nil {
			alias = New("TableAlias", Arg{"this", unnestIdent()},
				Arg{"columns", []*Expression{unnestIdent()}})
		} else if cols, _ := alias.Args["columns"].([]*Expression); len(cols) == 0 {
			alias.Set("columns", []*Expression{unnestIdent()})
		}
	}
	// BigQuery ANNOTATES the unnested expression to decide whether the array
	// holds structs, and records that as explode_array; the annotation is also
	// what gives the array its `t` (BigQueryParser._parse_unnest).
	var explodeArray any
	if p.dialect == "bigquery" && len(items) > 0 {
		AnnotateFully(items[0], "bigquery")
		if t := items[0].Type; t != nil && typeKind(t) == "ARRAY" {
			if elems, _ := t.Args["expressions"].([]*Expression); len(elems) > 0 {
				for _, el := range elems {
					if typeKind(el) == "STRUCT" {
						explodeArray = true
						break
					}
				}
			}
		}
	}
	return New("Unnest",
		Arg{"expressions", items},
		Arg{"alias", alias},
		Arg{"offset", offset},
		Arg{"explode_array", explodeArray}), nil
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
// The kind is the WORDS, kept as a string; `this` is what the phrase pinned, TIMESTAMP or VERSION.
func (p *parser) parseVersion(word string) (*Expression, error) {
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
		// A point in time; AS OF is optional after a phrase that has not
		// already said it.
		if p.atWords("AS", "OF") {
			p.advance()
			p.advance()
		}
		kind = "AS OF"
		e, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		expression = e
	}
	return New("Version",
		Arg{"this", word},
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

// parseIndexTableHints reads MySQL's `USE|IGNORE|FORCE INDEX|KEY [FOR <what>]
// (<index>, ...)`, any number of them in a row.
func (p *parser) parseIndexTableHints() ([]*Expression, error) {
	var hints []*Expression
	for {
		c := p.curr()
		n := p.next()
		if c == nil || n == nil || (!strings.EqualFold(n.Text, "INDEX") && !strings.EqualFold(n.Text, "KEY")) {
			return hints, nil
		}
		word := strings.ToUpper(c.Text)
		if word != "USE" && word != "IGNORE" && word != "FORCE" {
			return hints, nil
		}
		p.advance()
		p.advance()
		hint := New("IndexTableHint", Arg{"this", word})
		if p.at(TokFOR) {
			p.advance()
			target := p.curr()
			if target == nil {
				return nil, p.unsupported("an index hint FOR nothing")
			}
			p.advance()
			hint.Set("target", strings.ToUpper(target.Text))
		}
		if !p.match(TokL_PAREN) {
			return nil, p.unsupported("an index hint without its list")
		}
		var names []*Expression
		for !p.at(TokR_PAREN) {
			name, err := p.parseIdentifier()
			if err != nil {
				return nil, err
			}
			names = append(names, name)
			if !p.match(TokCOMMA) {
				break
			}
		}
		if !p.match(TokR_PAREN) {
			return nil, p.unsupported("an unclosed index hint")
		}
		if len(names) > 0 {
			hint.Set("expressions", names)
		}
		hints = append(hints, hint)
	}
}

// versionPhrases are the words that open a time-travel clause, in the order
// the reference tries them, with what each pins: a timestamp or a version.
// Dremio adds two of its own.
var versionPhrases = []struct {
	words []string
	this  string
	only  string
}{
	{[]string{"FOR", "SYSTEM_TIME"}, "TIMESTAMP", ""},
	{[]string{"FOR", "SYSTEM", "TIME"}, "TIMESTAMP", ""},
	{[]string{"FOR", "TIMESTAMP"}, "TIMESTAMP", ""},
	{[]string{"FOR", "VERSION"}, "VERSION", ""},
	{[]string{"TIMESTAMP", "AS", "OF"}, "TIMESTAMP", ""},
	{[]string{"VERSION", "AS", "OF"}, "VERSION", ""},
	{[]string{"AT", "TIMESTAMP"}, "TIMESTAMP", "dremio"},
	{[]string{"AT", "SNAPSHOT"}, "VERSION", "dremio"},
}

// versionPhrase consumes the phrase opening a time-travel clause and returns
// what it pins.
func (p *parser) versionPhrase() (string, bool) {
	for _, ph := range versionPhrases {
		if ph.only != "" && ph.only != p.dialect {
			continue
		}
		if p.atWords(ph.words...) {
			for range ph.words {
				p.advance()
			}
			return ph.this, true
		}
	}
	return "", false
}

// splitQualifiedName splits a BigQuery qualified name, masking the dots a
// DOMAIN-scoped project id carries -- `domain.com:project-id` -- so they are
// not read as path separators (BigQueryParser._split_qualified_name).
func splitQualifiedName(name string, minWords int) []string {
	const mask = "\x00"
	pad := func(words []string) []string {
		for len(words) < minWords {
			words = append([]string{""}, words...)
		}
		return words
	}
	if colon := strings.Index(name, ":"); colon != -1 && strings.Contains(name[:colon], ".") {
		masked := strings.ReplaceAll(name[:colon], ".", mask) + name[colon:]
		words := strings.Split(masked, ".")
		out := make([]string, 0, len(words))
		for _, w := range words {
			out = append(out, strings.ReplaceAll(w, mask, "."))
		}
		return pad(out)
	}
	return pad(strings.Split(name, "."))
}

// connectedToPrev reports whether the token now current was written with NO
// space after the one before it -- the reference's `_is_connected`.
func (p *parser) connectedToPrev() bool {
	c := p.curr()
	return c != nil && p.index > 0 && c.Start == p.tokens[p.index-1].End+1
}

// bigqueryTableParts applies BigQuery's own reading of a table name to the
// parts read plainly: a float token can swallow the dot between two parts, a
// name written inside ONE pair of backticks carries every dot, and an
// INFORMATION_SCHEMA view merges its last two parts
// (BigQueryParser._parse_table_parts).
func bigqueryTableParts(table *Expression) *Expression {
	argExpr := func(key string) *Expression {
		e, _ := table.Args[key].(*Expression)
		return e
	}
	isQuoted := func(e *Expression) bool {
		q, _ := e.Args["quoted"].(bool)
		return q
	}
	ident := func(name string, quoted bool) *Expression {
		return New("Identifier", Arg{"this", name}, Arg{"quoted", quoted})
	}
	// The parts of the table in order catalog, db, this -- a Dot in any of
	// them flattens, so a name that was already nested is still seen whole
	// (Table.parts).
	flat := func() []*Expression {
		var out []*Expression
		for _, key := range []string{"catalog", "db", "this"} {
			e := argExpr(key)
			switch {
			case e == nil:
			case e.Class == "Dot":
				out = append(out, flattenDot(e)...)
			default:
				out = append(out, e)
			}
		}
		return out
	}

	// `proj-1.db.tbl` -- the `1.` is tokenized as a float, so the dot that
	// separated the project from the dataset is inside that token. The same
	// happens to the name when there is no dataset: `x-0.y`.
	if argExpr("catalog") == nil {
		if db := argExpr("db"); db != nil {
			if !isQuoted(db) {
				if parts := strings.Split(db.Name(), "."); len(parts) == 2 {
					table.Set("catalog", New("Identifier", Arg{"this", parts[0]}))
					table.Set("db", New("Identifier", Arg{"this", parts[1]}))
				}
			}
		} else if this := argExpr("this"); this != nil && this.Class == "Identifier" && !isQuoted(this) {
			if parts := strings.Split(this.Name(), "."); len(parts) == 2 {
				table.Set("db", New("Identifier", Arg{"this", parts[0]}))
				table.Set("this", New("Identifier", Arg{"this", parts[1]}))
			}
		}
	}

	// A name holding a dot is joined and split again, the dots of a
	// DOMAIN-scoped project id masked: `domain.com:project-id.mydataset.tbl`
	// names three parts, not five.
	var alias *Expression
	if this := argExpr("this"); this != nil && this.Class == "Identifier" {
		var names []string
		hasDot := false
		for _, part := range flat() {
			names = append(names, part.Name())
			if strings.Contains(part.Name(), ".") {
				hasDot = true
			}
		}
		if hasDot {
			alias = this
			words := splitQualifiedName(strings.Join(names, "."), 3)
			quotedIdent := func(w string) *Expression {
				if w == "" {
					return nil
				}
				return ident(w, true)
			}
			// The first three words are the catalog, the dataset and the
			// name; anything past them joins the name into a Dot rather than
			// becoming a fourth qualifier.
			newThis := quotedIdent(words[2])
			for _, w := range words[3:] {
				newThis = New("Dot", Arg{"this", newThis}, Arg{"expression", quotedIdent(w)})
			}
			table = New("Table",
				Arg{"this", newThis},
				Arg{"db", quotedIdent(words[1])},
				Arg{"catalog", quotedIdent(words[0])})
			// The whole name was written inside one pair of backticks, and
			// that is how it goes back out
			// (BigQueryGenerator.table_parts).
			table.quotedTable = true
		}
	}

	// An INFORMATION_SCHEMA view needs a region or dataset in front of it, so
	// the reference merges the last two parts into one name -- otherwise a
	// three-part reference would look like a four-part one, with the region
	// taken for a project (BigQueryParser._parse_table_parts).
	parts := flat()
	if len(parts) > 1 && strings.EqualFold(parts[len(parts)-2].Name(), "INFORMATION_SCHEMA") {
		// The view is aliased by its own name, which keeps the columns of a
		// query that references it qualified the way they were written. A
		// real alias coming up in the token stream overwrites it.
		if table.Args["alias"] == nil {
			a := alias
			if a == nil {
				a = parts[len(parts)-1]
			}
			table.Set("alias", New("TableAlias", Arg{"this", a}))
		}
		view := parts[len(parts)-2].Name() + "." + parts[len(parts)-1].Name()
		table.Set("this", New("Identifier", Arg{"this", view}, Arg{"quoted", true}))
		table.Set("db", nil)
		table.Set("catalog", nil)
		if len(parts) >= 3 {
			table.Set("db", parts[len(parts)-3])
		}
		if len(parts) >= 4 {
			table.Set("catalog", parts[len(parts)-4])
		}
	}
	return table
}

// flattenDot returns the leaves of a Dot chain, left to right.
func flattenDot(e *Expression) []*Expression {
	var out []*Expression
	for _, key := range []string{"this", "expression"} {
		part, _ := e.Args[key].(*Expression)
		if part == nil {
			continue
		}
		if part.Class == "Dot" {
			out = append(out, flattenDot(part)...)
		} else {
			out = append(out, part)
		}
	}
	return out
}

// bareTableHintOf returns the hint an alias's single column names, where that
// column is one of T-SQL's bare table hints.
func bareTableHintOf(alias *Expression) *Expression {
	columns, _ := alias.Args["columns"].([]*Expression)
	if len(columns) != 1 || columns[0].Class != "Identifier" {
		return nil
	}
	quoted, _ := columns[0].Args["quoted"].(bool)
	if quoted || !bareTableHints[strings.ToUpper(columns[0].Name())] {
		return nil
	}
	return columns[0]
}

// tablePartsAreIdentifiers reports whether every part of a table's name is a
// plain identifier.
func tablePartsAreIdentifiers(table *Expression) bool {
	for _, key := range []string{"this", "db", "catalog"} {
		part, _ := table.Args[key].(*Expression)
		if part != nil && part.Class != "Identifier" {
			return false
		}
	}
	return true
}

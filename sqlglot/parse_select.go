package sqlglot

import "strings"

// The SELECT grammar, written against the reference's _parse_select_query.
//
// Argument order matters as much as argument content: the reference dumps a
// node's args in the order they were first assigned, and the comparison is
// exact. So a Select is created with the reference's full prefix of keys --
// most of them empty -- and later clauses fill the slots they reserved. Get the
// order wrong and every statement mismatches even when the tree is right.

// parseQuery reads a statement that produces rows: an optional WITH clause,
// a SELECT, and any set operations chained onto it.
func (p *parser) parseQuery() (*Expression, error) {
	with, err := p.parseWith()
	if err != nil {
		return nil, err
	}
	this, err := p.parseQueryBody()
	if err != nil {
		return nil, err
	}
	if with != nil {
		// The WITH clause is assigned to the whole statement once it is built,
		// so it dumps last -- and lands on the set operation, not on the first
		// query inside it, however early it was written.
		this.Set("with_", with)
	}
	return this, nil
}

// parseSummarize reads `SUMMARIZE [TABLE] <query | string | table>`. The word
// TABLE is recorded whether or not it was written, because the two spellings
// are two nodes and the writer picks by the flag.
func (p *parser) parseSummarize() (*Expression, error) {
	p.advance() // SUMMARIZE
	table := p.match(TokTABLE)
	var this *Expression
	var err error
	switch c := p.curr(); {
	case c == nil:
		return nil, p.unsupported("SUMMARIZE without anything to summarize")
	case c.Type == TokSTRING:
		p.advance()
		this = New("Literal", Arg{"this", c.Text}, Arg{"is_string", true})
	case p.at(TokSELECT):
		this, err = p.parseSelect()
	default:
		this, err = p.parseTable()
	}
	if err != nil {
		return nil, err
	}
	return New("Summarize", Arg{"this", this}, Arg{"table", table}), nil
}

// parseQueryBody is everything a query is APART from its WITH clause. It is
// separate because a WITH may also stand in front of an INSERT or an UPDATE,
// where what follows is not a query at all.
func (p *parser) parseQueryBody() (*Expression, error) {
	// parseSelect's precondition -- the SELECT token is current -- is checked
	// by parseStatement, but a WITH clause reaches here having consumed only
	// the CTEs. DuckDB allows the SELECT to be left out entirely
	// (`WITH t AS (...) FROM t` means `SELECT * FROM t`), and without this
	// check parseSelect advanced PAST the FROM and read the table name as the
	// selected expression: `SELECT t`, a different query that names no table
	// at all. The bare form was already refused; only the WITH path was not.
	// DuckDB has a PIVOT that is a QUERY rather than something hanging off a
	// table: `PIVOT Cities ON Year USING SUM(Population)`. It reaches here by
	// every route a query does -- on its own, inside a CTE, inside a
	// parenthesised FROM item -- so it is recognised here rather than at each.
	if p.at(TokPIVOT) || p.at(TokUNPIVOT) {
		return p.parseStatementPivot()
	}
	// DuckDB's SUMMARIZE describes what is in a table rather than selecting
	// from it. It stands where a query stands -- on its own and inside a
	// parenthesised FROM item -- and takes a query, a string naming a file,
	// or a table.
	if p.at(TokSUMMARIZE) {
		return p.parseSummarize()
	}
	// DuckDB lets the FROM come FIRST, with the projections after it or left
	// out entirely: `FROM t` means `SELECT * FROM t`. Every dialect here reads
	// it, and the tree it makes is an ordinary Select -- with one difference
	// that shows only in this order: a comma join hangs off the TABLE rather
	// than off the query.
	if p.at(TokFROM) {
		this, err := p.parseFromFirst()
		if err != nil {
			return nil, err
		}
		return p.parseSetOperations(this)
	}
	// A query may be WRAPPED, and every pair of parentheses is recorded:
	// `((SELECT 1))` is a Subquery inside a Subquery. Reading it here rather
	// than only at the statement means the nesting works wherever a query
	// goes -- inside a CTE, inside a FROM item, on either side of a UNION.
	if p.opensAParenthesisedQuery() {
		this, err := p.parseScalarSubquery()
		if err != nil {
			return nil, err
		}
		return p.parseSetOperations(this)
	}
	if !p.at(TokSELECT) {
		return nil, p.unsupported("query without SELECT")
	}
	this, err := p.parseSelect()
	if err != nil {
		return nil, err
	}
	return p.parseSetOperations(this)
}

// parseFromFirst reads `FROM <relation> [SELECT <projections>]`.
//
// The projections are assigned FIRST whatever order they were written in, so
// the tree is the one an ordinary SELECT makes; only the joins land somewhere
// else.
func (p *parser) parseFromFirst() (*Expression, error) {
	p.advance() // FROM
	table, err := p.parseTable()
	if err != nil {
		return nil, err
	}
	joins, err := p.parseJoins()
	if err != nil {
		return nil, err
	}
	if len(joins) > 0 {
		table.Set("joins", joins)
	}

	projections := []*Expression{newStar()}
	distinct := false
	if p.match(TokSELECT) {
		distinct = p.match(TokDISTINCT)
		if distinct && p.at(TokON) {
			return nil, p.unsupported("DISTINCT ON")
		}
		projections, err = p.parseProjections()
		if err != nil {
			return nil, err
		}
	}

	sel := New("Select")
	for _, k := range selectPrefix {
		sel.Set(k, nil)
	}
	if distinct {
		sel.Set("distinct", New("Distinct", Arg{"on", nil}))
	}
	sel.Set("expressions", projections)
	sel.Set("from_", New("From", Arg{"this", table}))
	if err := p.parseQueryModifiers(sel); err != nil {
		return nil, err
	}
	return sel, nil
}

// parseWith reads a WITH clause. RECURSIVE changes what the CTE means -- it may
// refer to itself -- and the reference records it as a flag on the With.
func (p *parser) parseWith() (*Expression, error) {
	if !p.match(TokWITH) {
		return nil, nil
	}
	recursive := p.match(TokRECURSIVE)

	var ctes []*Expression
	for {
		// A CTE is named the way a table is, so a STRING there is a QUOTED
		// name rather than a literal.
		alias, err := p.parseTablePart()
		if err != nil {
			return nil, err
		}
		var cteColumns []*Expression
		if p.at(TokL_PAREN) && p.next() != nil && p.next().Type != TokSELECT {
			cols, err := p.parseAliasColumns()
			if err != nil {
				return nil, err
			}
			cteColumns = cols
		}
		// The AS is not always written: `WITH x (SELECT 1)` names the query
		// straight after the alias, and the reference reads it and writes the
		// word back in.
		if !p.match(TokALIAS) && !p.at(TokL_PAREN) {
			return nil, p.unsupported("CTE without AS")
		}
		if !p.match(TokL_PAREN) {
			return nil, p.unsupported("CTE without a parenthesised query")
		}
		inner, err := p.parseQuery()
		if err != nil {
			return nil, err
		}
		if !p.match(TokR_PAREN) {
			return nil, p.unsupported("unclosed CTE")
		}
		ctes = append(ctes, New("CTE",
			Arg{"this", inner},
			Arg{"alias", New("TableAlias", Arg{"this", alias}, Arg{"columns", cteColumns})},
			Arg{"materialized", nil},
			Arg{"key_expressions", nil},
		))
		if !p.match(TokCOMMA) {
			break
		}
	}
	var recursiveArg any
	if recursive {
		recursiveArg = true
	}
	return New("With", Arg{"expressions", ctes}, Arg{"recursive", recursiveArg},
		Arg{"search", nil}, Arg{"udfs", nil}), nil
}

// setOperations are the words that chain two queries together.
var setOperations = map[TokenType]string{
	TokUNION:     "Union",
	TokEXCEPT:    "Except",
	TokINTERSECT: "Intersect",
}

func (p *parser) parseSetOperations(this *Expression) (*Expression, error) {
	for {
		c := p.curr()
		if c == nil {
			return this, nil
		}
		class, ok := setOperations[c.Type]
		if !ok {
			return this, nil
		}
		p.advance()

		// UNION is distinct, UNION ALL is not; the reference records which.
		distinct := true
		switch {
		case p.match(TokALL):
			distinct = false
		case p.match(TokDISTINCT):
			distinct = true
		}
		if c := p.curr(); c != nil && strings.EqualFold(c.Text, "BY") {
			return nil, p.unsupported("set operation BY NAME")
		}

		right, err := p.parseSelectOrParenthesised()
		if err != nil {
			return nil, err
		}
		this = New(class,
			Arg{"this", this}, Arg{"distinct", distinct}, Arg{"by_name", nil},
			Arg{"expression", right}, Arg{"side", nil}, Arg{"kind", nil}, Arg{"on", nil})
		p.liftSetOpModifiers(this, right)
		// A PARENTHESISED right-hand side keeps nothing to lift: the closing
		// parenthesis ended it, so a trailing ORDER BY, LIMIT or OFFSET was
		// never read into it. It belongs to the set operation, and is read
		// here where the operation is the thing being built.
		if right != nil && right.Class == "Subquery" {
			if err := p.parseQueryModifiers(this); err != nil {
				return nil, err
			}
		}
	}
}

// liftSetOpModifiers moves a trailing ORDER BY, LIMIT or OFFSET off the
// right-hand query and onto the set operation. `SELECT a UNION SELECT b LIMIT 1`
// limits the union, not the second query -- the reference parses it onto the
// query and then lifts it, and which of the three lift differs per dialect.
func (p *parser) liftSetOpModifiers(setOp, right *Expression) {
	if !p.tables.ModifiersAttachedToSetOp || right == nil {
		return
	}
	for _, key := range p.tables.SetOpModifiers {
		if value, _ := right.Args[key].(*Expression); value != nil {
			right.Set(key, nil)
			setOp.Set(key, value)
		}
	}
}

func (p *parser) parseSelectOrParenthesised() (*Expression, error) {
	if p.opensAParenthesisedQuery() {
		return p.parseScalarSubquery()
	}
	if !p.at(TokSELECT) {
		return nil, p.unsupported("set operation over something other than a SELECT")
	}
	return p.parseSelect()
}

// opensAParenthesisedQuery reports whether the cursor is on a parenthesis
// that opens a QUERY rather than an expression.
func (p *parser) opensAParenthesisedQuery() bool { return p.queryAt(0) }

// queryAt reports whether the parenthesis `i` tokens ahead opens a query.
//
// A query may be WRAPPED more than once and every pair is recorded --
// `((SELECT 1))` is a Subquery inside a Subquery -- so a parenthesis whose
// next token is another parenthesis has to look further. But it cannot simply
// look past the whole run: `((SELECT 1) + 1)` opens an EXPRESSION whose first
// operand is a query, and reading it as a query stopped at the `+` with the
// parentheses still open.
//
// So what follows the inner group decides: the parenthesis that closes this
// one means the query was the whole of it, a set operator means the query
// continues, and anything else means an expression.
func (p *parser) queryAt(i int) bool {
	if at := p.peekAt(i); at == nil || at.Type != TokL_PAREN {
		return false
	}
	next := p.peekAt(i + 1)
	if next == nil {
		return false
	}
	switch next.Type {
	case TokSELECT, TokWITH, TokFROM, TokPIVOT, TokUNPIVOT, TokSUMMARIZE:
		return true
	case TokL_PAREN:
		if !p.queryAt(i + 1) {
			return false
		}
		after := p.afterGroup(i + 1)
		if after == nil {
			return false
		}
		if after.Type == TokR_PAREN {
			return true
		}
		_, isSetOp := setOperations[after.Type]
		return isSetOp
	}
	return false
}

// afterGroup returns the token following the balanced parenthesis group that
// begins `i` tokens ahead, or nil where the group does not close.
func (p *parser) afterGroup(i int) *Token {
	depth := 0
	for ; ; i++ {
		next := p.peekAt(i)
		if next == nil {
			return nil
		}
		switch next.Type {
		case TokL_PAREN:
			depth++
		case TokR_PAREN:
			if depth--; depth == 0 {
				return p.peekAt(i + 1)
			}
		}
	}
}

// selectPrefix is the key order exp.Select is constructed with.
var selectPrefix = []string{
	"kind", "hint", "distinct", "expressions", "limit", "exclude", "operation_modifiers",
}

// parseHint reads what stands inside `/*+ ... */`: a comma-separated list of
// calls, each naming something the engine should do. The body is tokenized
// and parsed on its own, because the tokenizer keeps the comment whole.
func (p *parser) parseHint(text string) (*Expression, error) {
	body := strings.TrimSpace(text)
	body = strings.TrimPrefix(body, "/*+")
	body = strings.TrimSuffix(body, "*/")
	body = strings.TrimSpace(body)
	if body == "" {
		return nil, p.unsupported("a hint that says nothing")
	}
	tk, err := NewTokenizer(p.dialect)
	if err != nil {
		return nil, err
	}
	toks, err := tk.Tokenize(body)
	if err != nil {
		return nil, p.unsupported("a hint this port cannot read")
	}
	inner := &parser{tokens: toks, cfg: p.cfg, tables: p.tables, dialect: p.dialect}
	var items []*Expression
	for {
		item, err := inner.parseExpression()
		if err != nil {
			return nil, err
		}
		// A bare WORD is a word rather than a column: `/*+ REBALANCE */`
		// names something to do, and nothing is being selected here.
		if item != nil && item.Class == "Column" {
			if name, ok := bareColumnName(item); ok {
				item = New("Var", Arg{"this", name})
			}
		}
		items = append(items, item)
		if !inner.match(TokCOMMA) {
			break
		}
	}
	if inner.curr() != nil {
		return nil, p.unsupported("a hint with more than this port reads")
	}
	return New("Hint", Arg{"expressions", items}), nil
}

// parseSelect is entered with the SELECT token current; parseStatement checked.
func (p *parser) parseSelect() (*Expression, error) {
	p.advance()

	// A HINT tells the engine HOW to run the query. The tokenizer hands over
	// the whole comment as one token; what is inside it is an ordinary list
	// of calls, read by a parser of its own over that text.
	var hint *Expression
	if p.at(TokHINT) {
		read, err := p.parseHint(p.curr().Text)
		if err != nil {
			return nil, err
		}
		p.advance()
		hint = read
	}

	distinct := p.match(TokDISTINCT)
	// `DISTINCT ON (x)` keeps the FIRST row of each group rather than one row
	// per distinct projection, so what it is distinct ON is part of the node.
	var distinctOn *Expression
	if distinct && p.at(TokON) {
		p.advance()
		members, err := p.parseParenthesisedList()
		if err != nil {
			return nil, err
		}
		distinctOn = New("Tuple", Arg{"expressions", members})
	}
	// `SELECT ALL x` is the quantifier, which this port does not carry. But
	// `SELECT All` with nothing after it is a COLUMN called All, and the
	// reference reads it that way -- so the refusal has to look at what
	// follows, or the port cannot read back its own `SELECT All`.
	if p.at(TokALL) && !endsSelectExpression(p.next()) {
		return nil, p.unsupported("SELECT ALL")
	}

	// TOP is read before the projections and lands in the slot Select
	// reserves for `limit` -- which is why a LIMIT written at the end of the
	// statement still dumps before the FROM clause.
	top, err := p.parseTop()
	if err != nil {
		return nil, err
	}

	projections, err := p.parseProjections()
	if err != nil {
		return nil, err
	}

	sel := New("Select")
	for _, k := range selectPrefix {
		sel.Set(k, nil)
	}
	if hint != nil {
		sel.Set("hint", hint)
	}
	if distinct {
		sel.Set("distinct", New("Distinct", Arg{"on", distinctOn}))
	}
	sel.Set("expressions", projections)
	if top != nil {
		sel.Set("limit", top)
	}

	// SELECT … INTO is a query that writes. The guard refuses it as such, and
	// only the tree says so, so it is parsed rather than merely recognised.
	if p.match(TokINTO) {
		if p.atAny(TokTEMPORARY) {
			return nil, p.unsupported("SELECT INTO with a table kind")
		}
		// `INTO UNLOGGED foo` names a KIND before the table. Reading it as the
		// table name made UNLOGGED the target of the write.
		unlogged := false
		if c := p.curr(); c != nil && c.Type == TokVAR && strings.EqualFold(c.Text, "UNLOGGED") {
			p.advance()
			unlogged = true
		}
		target, err := p.parseTable()
		if err != nil {
			return nil, err
		}
		// T-SQL's # marks the TABLE as temporary, and the reference promotes
		// that up onto the INTO as well as leaving it on the name.
		sel.Set("into", New("Into", Arg{"this", target},
			Arg{"temporary", namesATemporaryTable(target)}, Arg{"unlogged", unlogged}))
	}

	if p.at(TokFROM) {
		from, err := p.parseFrom()
		if err != nil {
			return nil, err
		}
		sel.Set("from_", from)
	}

	joins, err := p.parseJoins()
	if err != nil {
		return nil, err
	}
	for _, j := range joins {
		sel.Append("joins", j)
	}

	if err := p.parseQueryModifiers(sel); err != nil {
		return nil, err
	}
	return sel, nil
}

// parseTop reads T-SQL's row limiter. PERCENT is kept rather than refused:
// the guard has to see it to say that a proportion is not a row ceiling, and
// a statement it cannot parse would be refused for the wrong reason.
func (p *parser) parseTop() (*Expression, error) {
	// `SELECT TOP = x` is T-SQL naming a column TOP, not a row limiter with a
	// missing count. This runs before parseProjection, so without the peek the
	// refusal blamed the count -- and the refusal REASON is what the guard
	// records and what decides which grammar gets ported next. A label that
	// names the wrong construct sends that decision the wrong way.
	if p.at(TokTOP) {
		if n := p.next(); n != nil && n.Type == TokEQ {
			return nil, p.unsupported("T-SQL alias assignment")
		}
	}
	if !p.match(TokTOP) {
		return nil, nil
	}
	var count *Expression
	switch {
	// `TOP (expr)` is how T-SQL spells a count that is not a plain number, and
	// the reference stores the expression UNWRAPPED -- `SELECT TOP (A) 0` and
	// `SELECT 0 LIMIT A` are the same tree. Reading only a bare number meant
	// the port could write `TOP (A)`, which it then could not read back.
	case p.at(TokL_PAREN):
		p.advance()
		var inner *Expression
		var err error
		// A count that is a QUERY is stored as the query itself, with no
		// Subquery around it -- the parentheses belong to the TOP.
		if p.at(TokSELECT) {
			inner, err = p.parseQuery()
		} else {
			inner, err = p.parseExpression()
		}
		if err != nil {
			return nil, err
		}
		if !p.match(TokR_PAREN) {
			return nil, p.unsupported("unclosed TOP count")
		}
		count = inner
	case p.curr() != nil && p.curr().Type == TokNUMBER:
		count = New("Literal", Arg{"this", p.curr().Text}, Arg{"is_string", false})
		p.advance()
	default:
		return nil, p.unsupported("TOP without a count")
	}
	limit := New("Limit",
		Arg{"this", nil},
		Arg{"expression", count},
		Arg{"offset", nil},
		Arg{"limit_options", nil},
		Arg{"expressions", nil},
	)
	// The same words a LIMIT takes after its count, in the same order.
	if options := p.parseLimitOptions(); options != nil {
		limit.Set("limit_options", options)
	}
	return limit, nil
}

func (p *parser) parseProjections() ([]*Expression, error) {
	var out []*Expression
	for {
		// A SELECT may name nothing at all -- `SELECT TOP 10 PERCENT` is a
		// whole statement in T-SQL, and `SELECT FROM t` is what the reference
		// writes for a query with no projections. It records no projections
		// rather than an empty list.
		//
		// The test is the reference's own: a projection is missing when the
		// token in front of it cannot begin one, which is exactly the case
		// where the parse fails having consumed nothing.
		start := p.index
		e, err := p.parseProjection()
		if err != nil {
			if len(out) == 0 && p.index == start {
				return nil, nil
			}
			return nil, err
		}
		out = append(out, e)
		if !p.match(TokCOMMA) {
			break
		}
	}
	return out, nil
}

func (p *parser) parseProjection() (*Expression, error) {
	// DuckDB names a projection in FRONT of it: `SELECT foo: 1` is
	// `SELECT 1 AS foo`. Claimed only when a NAME is followed by the colon,
	// so a slice or a parameter elsewhere in the expression is untouched.
	if p.tables.PrefixAlias && p.atAliasName() {
		if n := p.next(); n != nil && n.Type == TokCOLON {
			alias, err := p.parseIdentifier()
			if err != nil {
				return nil, err
			}
			p.advance() // the colon
			named, err := p.parseExpression()
			if err != nil {
				return nil, err
			}
			return New("Alias", Arg{"this", named}, Arg{"alias", alias}), nil
		}
	}
	e, err := p.parseExpression()
	if err != nil {
		return nil, err
	}
	// `alias = expression` names a column with the LEFT-hand side, which is
	// the opposite of what the same tokens mean anywhere else. Only T-SQL
	// reads it that way, and which dialects do is asked rather than assumed.
	//
	// Only where the left is a BARE NAME: `A = A = 0` is a chain of
	// comparisons in the reference too, because what stands left of the
	// outer equals is an equality rather than a name. The whole expression
	// is read first and turned round after, which is how that falls out
	// rather than having to be tested for.
	if p.tables.ProjectionEqualsIsAlias && e != nil && e.Class == "EQ" {
		if left, _ := e.Args["this"].(*Expression); left != nil && left.Class == "Column" {
			if name, ok := bareColumnName(left); ok {
				right, _ := e.Args["expression"].(*Expression)
				return New("Alias",
					Arg{"this", right},
					Arg{"alias", New("Identifier",
						Arg{"this", name}, Arg{"quoted", false})}), nil
			}
		}
	}
	return p.parseAlias(e)
}

// parseAlias attaches an explicit or implicit column alias.
func (p *parser) parseAlias(this *Expression) (*Expression, error) {
	explicit := p.match(TokALIAS)
	if !explicit && !p.atAliasName() {
		return this, nil
	}
	// After a written AS, ANY token that is not reserved is the name: `SELECT
	// 1 AS delete` names a column delete, and `SELECT x AS INTO` one called
	// INTO. Only an identifier can stand there without the word.
	alias, err := p.parseIdentifier()
	if err != nil {
		if !explicit {
			return nil, err
		}
		alias, err = p.parseAnyName()
		if err != nil {
			return nil, err
		}
	}
	return New("Alias", Arg{"this", this}, Arg{"alias", alias}), nil
}

// parseAnyName reads a name where the reference accepts any token: everything
// but the handful it reserves. A STRING there is a QUOTED name rather than a
// literal, which is how `FROM 'x.y'` names a table.
func (p *parser) parseAnyName() (*Expression, error) {
	c := p.curr()
	if c == nil {
		return nil, p.unsupported("identifier")
	}
	if _, reserved := p.tables.ReservedTokens[c.Type]; reserved {
		return nil, p.unsupported("identifier")
	}
	p.advance()
	return New("Identifier",
		Arg{"this", c.Text},
		Arg{"quoted", c.Type == TokIDENTIFIER || c.Type == TokSTRING}), nil
}

// atAliasName reports whether the current token can begin an implicit alias.
// Only a plain word or a quoted identifier qualifies: anything else -- an
// operator, a keyword that starts a clause -- would silently swallow structure.
func (p *parser) atAliasName() bool {
	c := p.curr()
	if c == nil {
		return false
	}
	return c.Type == TokVAR || c.Type == TokIDENTIFIER
}

// parseQueryModifiers reads the trailing clauses in source order, as the
// reference does -- the order they appear in is the order they are assigned,
// and therefore the order they dump in.
func (p *parser) parseQueryModifiers(sel *Expression) error {
	for {
		switch {
		case p.at(TokWHERE):
			p.advance()
			e, err := p.parseExpression()
			if err != nil {
				return err
			}
			if err := p.setOnce(sel, "where", New("Where", Arg{"this", e})); err != nil {
				return err
			}
		case p.at(TokGROUP_BY):
			p.advance()

			// `GROUP BY ALL` is a flag on Group, not a column named "all".
			// Parsing it as an expression built a Group over a Column, which
			// is a different tree for a statement the engine reads as
			// "group by every non-aggregated column".
			if p.at(TokALL) {
				p.advance()
				if err := p.setOnce(sel, "group", New("Group", Arg{"all", true})); err != nil {
					return err
				}
				continue
			}
			// CUBE, ROLLUP and GROUPING SETS look like calls but land on
			// their OWN args of Group rather than in its expression list,
			// and any of them may sit beside plain columns.
			var plain, sets, cube, rollup []*Expression
			for {
				var target *[]*Expression
				var class string
				switch {
				case p.at(TokGROUPING_SETS):
					target, class = &sets, "GroupingSets"
				case p.at(TokCUBE):
					target, class = &cube, "Cube"
				case p.at(TokROLLUP):
					target, class = &rollup, "Rollup"
				}
				if target != nil {
					p.advance()
					members, err := p.parseParenthesisedList()
					if err != nil {
						return err
					}
					*target = append(*target, New(class, Arg{"expressions", members}))
				} else {
					e, err := p.parseExpression()
					if err != nil {
						return err
					}
					plain = append(plain, e)
				}
				if !p.match(TokCOMMA) {
					break
				}
			}
			group := New("Group", Arg{"expressions", plain})
			for _, pair := range []struct {
				key  string
				list []*Expression
			}{{"grouping_sets", sets}, {"cube", cube}, {"rollup", rollup}} {
				if len(pair.list) > 0 {
					group.Set(pair.key, pair.list)
				}
			}
			if err := p.setOnce(sel, "group", group); err != nil {
				return err
			}
		case p.at(TokHAVING):
			p.advance()
			e, err := p.parseExpression()
			if err != nil {
				return err
			}
			if err := p.setOnce(sel, "having", New("Having", Arg{"this", e})); err != nil {
				return err
			}
		case p.at(TokORDER_BY):
			p.advance()
			order, err := p.parseOrder()
			if err != nil {
				return err
			}
			if err := p.setOnce(sel, "order", order); err != nil {
				return err
			}
		// CLUSTER BY, DISTRIBUTE BY and SORT BY are Hive's three ways of
		// saying how rows reach the reducers. Two of them take ORDERED items
		// -- a direction may be written -- and CLUSTER BY takes plain
		// columns, which is the reference's own asymmetry.
		case p.at(TokCLUSTER_BY):
			p.advance()
			var columns []*Expression
			for {
				c, err := p.parseColumn()
				if err != nil {
					return err
				}
				columns = append(columns, c)
				if !p.match(TokCOMMA) {
					break
				}
			}
			if err := p.setOnce(sel, "cluster",
				New("Cluster", Arg{"expressions", columns})); err != nil {
				return err
			}
		case p.at(TokDISTRIBUTE_BY), p.at(TokSORT_BY):
			key, class := "distribute", "Distribute"
			if p.at(TokSORT_BY) {
				key, class = "sort", "Sort"
			}
			p.advance()
			ordered, err := p.parseOrderedList()
			if err != nil {
				return err
			}
			if err := p.setOnce(sel, key,
				New(class, Arg{"expressions", ordered})); err != nil {
				return err
			}
		case p.at(TokLIMIT):
			p.advance()
			// `LIMIT ALL` is PostgreSQL for "no limit", and the reference
			// records it by setting no limit at all rather than by a node
			// meaning "unlimited". Parsing ALL as an expression built a Limit
			// over a column named "all" -- a limit where the reference has
			// none, on the one clause this service rewrites.
			if p.tables.LimitAllMeansNoLimit && p.match(TokALL) {
				continue
			}
			e, err := p.parseExpression()
			if err != nil {
				return err
			}
			options := p.parseLimitOptions()
			limit := New("Limit", Arg{"this", nil}, Arg{"expression", e},
				Arg{"limit_options", options}, Arg{"expressions", nil})
			if err := p.setOnce(sel, "limit", limit); err != nil {
				return err
			}
		case p.at(TokWINDOW):
			// `WINDOW w AS (...), v AS (...)` names windows for OVER to refer
			// to. Each is the same node an OVER builds.
			p.advance()
			var windows []*Expression
			for {
				w, err := p.parseNamedWindow()
				if err != nil {
					return err
				}
				windows = append(windows, w)
				if !p.match(TokCOMMA) {
					break
				}
			}
			if err := p.setOnce(sel, "windows", windows[0]); err != nil {
				return err
			}
			sel.Set("windows", windows)
		case p.at(TokQUALIFY):
			p.advance()
			e, err := p.parseDisjunction()
			if err != nil {
				return err
			}
			if err := p.setOnce(sel, "qualify", New("Qualify", Arg{"this", e})); err != nil {
				return err
			}
		case p.atWords("USING", "SAMPLE"):
			// DuckDB's other sampling spelling, which hangs off the QUERY
			// where TABLESAMPLE hangs off the table -- the same node either
			// way.
			p.advance()
			p.advance()
			sample, err := p.parseUsingSample()
			if err != nil {
				return err
			}
			if err := p.setOnce(sel, "sample", sample); err != nil {
				return err
			}
		case p.atWords("LATERAL", "VIEW"):
			view, err := p.parseLateralView()
			if err != nil {
				return err
			}
			laterals, _ := sel.Args["laterals"].([]*Expression)
			sel.Set("laterals", append(laterals, view))
		case p.atWords("FOR", "XML"), p.atWords("FOR", "JSON"), p.atWords("FOR", "BROWSE"):
			clause, err := p.parseForClause()
			if err != nil {
				return err
			}
			if err := p.setOnce(sel, "for_", clause); err != nil {
				return err
			}
		case p.atWords("FOR", "UPDATE"), p.atWords("FOR", "SHARE"):
			// PostgreSQL's row locking, which the reference keeps as a LIST
			// of Lock nodes distinguished by a single flag.
			p.advance()
			update := p.atWords("UPDATE")
			p.advance()
			locks, _ := sel.Args["locks"].([]*Expression)
			sel.Set("locks", append(locks, New("Lock", Arg{"update", update})))
		case p.at(TokFETCH):
			fetch, err := p.parseFetch()
			if err != nil {
				return err
			}
			if err := p.setOnce(sel, "limit", fetch); err != nil {
				return err
			}
		case p.at(TokOFFSET):
			p.advance()
			e, err := p.parseExpression()
			if err != nil {
				return err
			}
			// `OFFSET 2 ROWS` and `OFFSET 2` are the same tree: the word
			// says nothing the count does not, and T-SQL is the only dialect
			// that writes it.
			if p.atWords("ROWS") || p.atWords("ROW") {
				p.advance()
			}
			offset := New("Offset", Arg{"this", nil}, Arg{"expression", e}, Arg{"expressions", nil})
			if err := p.setOnce(sel, "offset", offset); err != nil {
				return err
			}
		default:
			return nil
		}
	}
}

// setOnce refuses a repeated clause rather than letting the later one win --
// the reference raises there, and silently dropping one would change meaning.
func (p *parser) setOnce(node *Expression, key string, value *Expression) error {
	if existing, ok := node.Args[key]; ok && existing != nil {
		return p.unsupported("repeated " + key + " clause")
	}
	node.Set(key, value)
	return nil
}

func (p *parser) parseOrder() (*Expression, error) {
	ordered, err := p.parseOrderedList()
	if err != nil {
		return nil, err
	}
	return New("Order", Arg{"this", nil}, Arg{"expressions", ordered}, Arg{"siblings", nil}), nil
}

// parseOrderedList reads the comma-separated `x DESC NULLS LAST` items an
// ORDER BY is made of. SORT BY and DISTRIBUTE BY are made of the same ones.
func (p *parser) parseOrderedList() ([]*Expression, error) {
	var ordered []*Expression
	for {
		e, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		desc := false
		hasDirection := false
		switch {
		case p.match(TokDESC):
			desc, hasDirection = true, true
		case p.match(TokASC):
			desc, hasDirection = false, true
		}
		// `NULLS FIRST` / `NULLS LAST` tokenizes as the word NULLS then the
		// word FIRST or LAST, neither being a keyword. When it is written the
		// statement says where NULLs sort; when it is not, the DIALECT does,
		// which is what nullsFirst answers.
		nullsFirst, saidSo := false, false
		if c := p.curr(); c != nil && strings.EqualFold(c.Text, "NULLS") {
			p.advance()
			n := p.curr()
			if n == nil {
				return nil, p.unsupported("NULLS without FIRST or LAST")
			}
			switch {
			case strings.EqualFold(n.Text, "FIRST"):
				nullsFirst, saidSo = true, true
			case strings.EqualFold(n.Text, "LAST"):
				nullsFirst, saidSo = false, true
			default:
				return nil, p.unsupported("NULLS without FIRST or LAST")
			}
			p.advance()
		}
		if c := p.curr(); c != nil && c.Type == TokWITH {
			return nil, p.unsupported("WITH FILL")
		}
		o := New("Ordered", Arg{"this", e})
		if hasDirection {
			o.Set("desc", desc)
		}
		if !saidSo {
			nullsFirst = p.nullsFirst(hasDirection && desc)
		}
		o.Set("nulls_first", nullsFirst)
		o.Set("with_fill", nil)
		ordered = append(ordered, o)
		if !p.match(TokCOMMA) {
			break
		}
	}
	return ordered, nil
}

// nullsFirst answers where NULLs sort when the statement does not say.
//
// It is not "the opposite of DESC": PostgreSQL sorts NULLs last ascending and
// first descending, DuckDB always last, T-SQL the other way round. The rule is
// the reference's, read off the dialect.
func (p *parser) nullsFirst(desc bool) bool {
	ordering := p.tables.NullOrdering
	if ordering == "nulls_are_last" {
		return false
	}
	if desc {
		return ordering != "nulls_are_small"
	}
	return ordering == "nulls_are_small"
}

func (p *parser) parseExpressionList() ([]*Expression, error) {
	var out []*Expression
	for {
		e, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		out = append(out, e)
		if !p.match(TokCOMMA) {
			break
		}
	}
	return out, nil
}

// endsSelectExpression reports whether a token cannot continue a select item,
// which is how a bare `ALL` is told from the `ALL` quantifier in front of one.
func endsSelectExpression(t *Token) bool {
	if t == nil {
		return true // nothing follows at all: a bare column called ALL
	}
	switch t.Type {
	case TokFROM, TokWHERE, TokCOMMA, TokR_PAREN, TokSEMICOLON, TokUNION:
		return true
	}
	return false
}

// FETCH [FIRST|NEXT] [count [PERCENT]] ROW|ROWS [ONLY | WITH TIES]
//
// It lands in the same slot as LIMIT, because it is one: the reference keeps a
// Fetch under `limit`. The direction is a WORD kept as a string rather than a
// node, and everything after the count is a set of flags on a LimitOptions --
// which is why this clause needed the flag work that came before it, or the
// options would have been written without their words.
func (p *parser) parseFetch() (*Expression, error) {
	p.advance() // FETCH

	// The direction is optional: `FETCH 1 ROW` means FIRST, and the reference
	// records FIRST rather than leaving it unset.
	direction := "FIRST"
	if p.atWords("FIRST") || p.atWords("NEXT") {
		direction = strings.ToUpper(p.curr().Text)
		p.advance()
	}

	var count *Expression
	if !p.atLimitOptionWord() {
		e, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		count = e
	}

	percent := false
	if p.atWords("PERCENT") {
		p.advance()
		percent = true
	}
	rows := false
	if p.atWords("ROW") || p.atWords("ROWS") {
		p.advance()
		rows = true
	}
	withTies := false
	switch {
	case p.atWords("ONLY"):
		p.advance()
	case p.atWords("WITH", "TIES"):
		p.advance()
		p.advance()
		withTies = true
	}

	args := []Arg{{"direction", direction}}
	if count != nil {
		args = append(args, Arg{"count", count})
	}
	args = append(args, Arg{"limit_options", New("LimitOptions",
		Arg{"percent", percent}, Arg{"rows", rows}, Arg{"with_ties", withTies})})
	return New("Fetch", args...), nil
}

// atLimitOptionWord reports whether what follows FETCH is already one of the
// option words rather than a count. `FETCH FIRST ROWS ONLY` has no count, and
// reading ROWS as one built a column called ROWS.
func (p *parser) atLimitOptionWord() bool {
	for _, word := range []string{"ROW", "ROWS", "ONLY", "PERCENT", "WITH"} {
		if p.atWords(word) {
			return true
		}
	}
	return false
}

// PIVOT <source> ON <expressions> [USING <aggregates>] [GROUP BY ...], and the
// UNPIVOT that instead takes [INTO NAME <column> VALUE <columns>].
//
// A different grammar from the PIVOT that hangs off a table, and a different
// shape: no derived columns and none of the dialect conventions, because
// nothing here is being named after anything.
//
// Two arguments are asymmetric and both are copied as the reference has them:
// `using` is FALSE when there is no USING, where `unpivot` is absent rather
// than false when it is a PIVOT.
func (p *parser) parseStatementPivot() (*Expression, error) {
	unpivot := p.at(TokUNPIVOT)
	p.advance()

	source, err := p.parseTable()
	if err != nil {
		return nil, err
	}
	if !p.match(TokON) {
		return nil, p.unsupported("a PIVOT statement without ON")
	}

	var on []*Expression
	for {
		e, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		// `ON Year IN (2000, 2010)` lists the values to pivot on, and
		// `ON (jan, feb) AS q1` names a group of columns.
		switch {
		case p.match(TokIN):
			if !p.match(TokL_PAREN) {
				return nil, p.unsupported("a PIVOT list that is not parenthesised")
			}
			var values []*Expression
			for {
				v, verr := p.parseExpression()
				if verr != nil {
					return nil, verr
				}
				values = append(values, v)
				if !p.match(TokCOMMA) {
					break
				}
			}
			if !p.match(TokR_PAREN) {
				return nil, p.unsupported("unclosed PIVOT list")
			}
			e = New("In", Arg{"this", e}, Arg{"expressions", values})
		default:
			e, err = p.parseAlias(e)
			if err != nil {
				return nil, err
			}
		}
		on = append(on, e)
		if !p.match(TokCOMMA) {
			break
		}
	}

	var using []*Expression
	if p.match(TokUSING) {
		for {
			agg, err := p.parseUnary()
			if err != nil {
				return nil, err
			}
			agg, err = p.parseAlias(agg)
			if err != nil {
				return nil, err
			}
			using = append(using, agg)
			if !p.match(TokCOMMA) {
				break
			}
		}
	}

	var group *Expression
	if p.match(TokGROUP_BY) {
		// Only the plain column list. CUBE, ROLLUP, GROUPING SETS and ALL
		// land on their own args of Group and are refused for the same
		// reason they are refused in a SELECT.
		if p.atAny(TokCUBE, TokROLLUP, TokGROUPING_SETS, TokALL) {
			return nil, p.unsupported("GROUP BY " + p.curr().Text)
		}
		es, gerr := p.parseExpressionList()
		if gerr != nil {
			return nil, gerr
		}
		group = New("Group", Arg{"expressions", es})
	}

	var into *Expression
	if p.atWords("INTO", "NAME") {
		p.advance()
		p.advance()
		name, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		if !p.atWords("VALUE") {
			return nil, p.unsupported("INTO NAME without VALUE")
		}
		p.advance()
		var values []*Expression
		for {
			v, verr := p.parseUnary()
			if verr != nil {
				return nil, verr
			}
			values = append(values, v)
			if !p.match(TokCOMMA) {
				break
			}
		}
		into = New("UnpivotColumns", Arg{"this", name}, Arg{"expressions", values})
	}

	args := []Arg{{"this", source}, {"expressions", on}}
	if len(using) > 0 {
		args = append(args, Arg{"using", using})
	} else {
		args = append(args, Arg{"using", false})
	}
	args = append(args, Arg{"group", group})
	switch {
	case unpivot:
		args = append(args, Arg{"unpivot", true})
	case p.inFromSubquery:
		// See parseSubqueryTable: the reference sets this false on that path
		// alone, and leaves it off everywhere else.
		args = append(args, Arg{"unpivot", false})
	default:
		args = append(args, Arg{"unpivot", nil})
	}
	return New("Pivot", append(args, Arg{"into", into})...), nil
}

// FOR XML <options>, FOR JSON <options> and the bare FOR BROWSE.
//
// An option is a WORD from this kind's vocabulary, which may take a second
// word after it -- `ELEMENTS XSINIL`, `BINARY BASE64` -- and a word that is
// not in the vocabulary falls through to a key/value option that may carry a
// parenthesised string. That fall-through is why `PATH` is a plain Var under
// JSON, whose table has it, and an XMLKeyValueOption under XML, whose does
// not. The vocabularies are generated from the reference's own tables.
func (p *parser) parseForClause() (*Expression, error) {
	p.advance() // FOR
	kind := strings.ToUpper(p.curr().Text)
	p.advance()
	if kind == "BROWSE" {
		return New("ForClause", Arg{"kind", kind}), nil
	}

	vocabulary := p.tables.ForClauseOptions[kind]
	var options []*Expression
	for {
		word := p.curr()
		if word == nil {
			return nil, p.unsupported("FOR " + kind + " without an option")
		}
		upper := strings.ToUpper(word.Text)
		if follows, known := vocabulary[upper]; known {
			p.advance()
			// A second word belongs to the first, and the reference keeps
			// the pair as ONE Var rather than as two options.
			for _, follow := range follows {
				if p.atWords(follow) {
					p.advance()
					upper += " " + follow
					break
				}
			}
			options = append(options, New("QueryOption",
				Arg{"this", New("Var", Arg{"this", upper})}))
		} else {
			// A bare WORD here is a Var, not a column: the reference reads a
			// primary-or-var, so `ROOT` and `PATH` name options rather than
			// referring to anything.
			var this *Expression
			var err error
			if p.atIdentifier() {
				this = New("Var", Arg{"this", p.curr().Text})
				p.advance()
			} else if this, err = p.parseUnary(); err != nil {
				return nil, err
			}
			var expression *Expression
			if p.match(TokL_PAREN) {
				expression, err = p.parseExpression()
				if err != nil {
					return nil, err
				}
				if !p.match(TokR_PAREN) {
					return nil, p.unsupported("unclosed FOR " + kind + " option")
				}
			}
			options = append(options, New("QueryOption",
				Arg{"this", New("XMLKeyValueOption",
					Arg{"this", this}, Arg{"expression", expression})}))
		}
		if !p.match(TokCOMMA) {
			break
		}
	}
	return New("ForClause", Arg{"kind", kind}, Arg{"expressions", options}), nil
}

// LATERAL VIEW [OUTER] <call> [alias] [AS <column>[, <column>]]
//
// Hive's, and a CLAUSE of the query rather than a relation in the FROM list --
// the reference keeps a list of them under `laterals`. The alias is always
// present, and empty when the statement named nothing.
//
// The column list after AS is bare and comma-separated, not parenthesised,
// which is why it cannot go through the table-alias reader.
func (p *parser) parseLateralView() (*Expression, error) {
	p.advance() // LATERAL
	p.advance() // VIEW

	outer := false
	if p.atWords("OUTER") {
		p.advance()
		outer = true
	}
	call, err := p.parseUnary()
	if err != nil {
		return nil, err
	}

	var name *Expression
	if p.atTablePart() {
		name, err = p.parseTablePart()
		if err != nil {
			return nil, err
		}
	}
	var columns []*Expression
	if p.match(TokALIAS) {
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
	}
	return New("Lateral",
		Arg{"this", call},
		Arg{"view", true},
		Arg{"outer", outer},
		Arg{"alias", New("TableAlias", Arg{"this", name}, Arg{"columns", columns})},
		Arg{"cross_apply", nil},
		Arg{"ordinality", nil}), nil
}

// USING SAMPLE, in either order:
//
//	USING SAMPLE 5                              a row count
//	USING SAMPLE 10%                            a percentage
//	USING SAMPLE 10 PERCENT (bernoulli)         and the method after it
//	USING SAMPLE reservoir(50 ROWS)             or the method first
//	USING SAMPLE 10% (system, 377)              with a seed beside the method
//
// The default method is not one value but two: a row count is RESERVOIR and a
// percentage is SYSTEM, so it cannot be settled until the unit is known.
func (p *parser) parseUsingSample() (*Expression, error) {
	method := ""
	if !p.at(TokNUMBER) {
		word := p.curr()
		if word == nil {
			return nil, p.unsupported("USING SAMPLE without a specification")
		}
		method = strings.ToUpper(word.Text)
		p.advance()
		if !p.match(TokL_PAREN) {
			return nil, p.unsupported("USING SAMPLE method without a size")
		}
	}

	size, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	percent := false
	switch {
	case p.match(TokMOD), p.atWords("PERCENT"):
		if p.atWords("PERCENT") {
			p.advance()
		}
		percent = true
	case p.atWords("ROWS"), p.atWords("ROW"):
		p.advance()
	}
	if method != "" && !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed USING SAMPLE")
	}

	var seed *Expression
	seedSet := false
	// `(bernoulli)` or `(system, 377)` names the method after the size, and
	// the seed beside it. The seed is recorded FALSE when the parentheses are
	// there without one.
	if method == "" && p.match(TokL_PAREN) {
		word := p.curr()
		if word == nil {
			return nil, p.unsupported("USING SAMPLE without a method")
		}
		method = strings.ToUpper(word.Text)
		p.advance()
		seedSet = true
		if p.match(TokCOMMA) {
			seed, err = p.parseUnary()
			if err != nil {
				return nil, err
			}
		}
		if !p.match(TokR_PAREN) {
			return nil, p.unsupported("unclosed USING SAMPLE method")
		}
	}
	if p.atWords("REPEATABLE") {
		p.advance()
		if !p.match(TokL_PAREN) {
			return nil, p.unsupported("REPEATABLE without a seed")
		}
		seed, err = p.parseUnary()
		if err != nil {
			return nil, err
		}
		if !p.match(TokR_PAREN) {
			return nil, p.unsupported("unclosed REPEATABLE")
		}
		seedSet = true
	}

	if method == "" {
		// The default depends on the UNIT, not on the dialect alone.
		method = "RESERVOIR"
		if percent {
			method = "SYSTEM"
		}
	}
	args := []Arg{{"method", New("Var", Arg{"this", method})}}
	if percent {
		args = append(args, Arg{"percent", size})
	} else {
		args = append(args, Arg{"size", size})
	}
	if seed != nil {
		args = append(args, Arg{"seed", seed})
	} else if seedSet {
		args = append(args, Arg{"seed", false})
	}
	return New("TableSample", args...), nil
}

// parseParenthesisedList reads `(a, b, c)`, the argument list CUBE, ROLLUP and
// GROUPING SETS take. A member may itself be a parenthesised row, and `()` --
// the empty grouping -- is one too.
func (p *parser) parseParenthesisedList() ([]*Expression, error) {
	if !p.match(TokL_PAREN) {
		return nil, p.unsupported("a grouping without its arguments")
	}
	var out []*Expression
	if p.at(TokR_PAREN) {
		p.advance()
		return out, nil
	}
	for {
		// `()` is the EMPTY grouping -- the one that groups everything -- and
		// it is a Tuple of nothing rather than a missing member.
		if p.at(TokL_PAREN) && p.next() != nil && p.next().Type == TokR_PAREN {
			p.advance()
			p.advance()
			out = append(out, New("Tuple"))
		} else {
			e, err := p.parseExpression()
			if err != nil {
				return nil, err
			}
			out = append(out, e)
		}
		if !p.match(TokCOMMA) {
			break
		}
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed grouping")
	}
	return out, nil
}

// opensASetOperation reports whether the cursor is on a parenthesised query
// that a SET OPERATION follows: `((SELECT 1) UNION SELECT 2)` as a FROM item,
// where the cursor sits on the inner parenthesis.
//
// The plainer question -- does this parenthesis open a query -- is the wrong
// one HERE, because a parenthesised JOIN TREE begins the same way:
// `((SELECT 1) CROSS JOIN (SELECT 2))` is a join, not a query, and reading it
// as one stopped at the JOIN with the parentheses still open. So the group is
// skipped whole and the token after it is what decides.
func (p *parser) opensASetOperation() bool {
	if !p.opensAParenthesisedQuery() {
		return false
	}
	after := p.afterGroup(0)
	if after == nil {
		return false
	}
	_, isSetOp := setOperations[after.Type]
	return isSetOp
}

// parseLimitOptions reads what may follow a LIMIT's count: `PERCENT` says the
// count is a share rather than a number, `ROWS`/`ROW` and `ONLY` are noise the
// reference still records, and `WITH TIES` keeps whatever ties the last row.
//
// Nothing written at all is no options node, not an empty one.
func (p *parser) parseLimitOptions() *Expression {
	percent := p.at(TokPERCENT) || p.at(TokMOD)
	if percent {
		p.advance()
	}
	rows := p.at(TokROW) || p.at(TokROWS)
	if rows {
		p.advance()
	}
	p.matchUnquotedWord("ONLY")
	withTies := false
	if p.at(TokWITH) {
		if n := p.next(); n != nil && strings.EqualFold(n.Text, "TIES") {
			p.advance()
			p.advance()
			withTies = true
		}
	}
	if !percent && !rows && !withTies {
		return nil
	}
	return New("LimitOptions",
		Arg{"percent", percent}, Arg{"rows", rows}, Arg{"with_ties", withTies})
}

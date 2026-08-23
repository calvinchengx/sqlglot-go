package sqlglot

import (
	"sort"
	"strconv"
	"strings"
)

// One writer per node class the parser builds. Each is held to the reference's
// own output by the differential, so where a shape looks arbitrary -- AS before
// a table alias, TOP where the dialect wants TOP -- it is arbitrary in the same
// way the reference is.
//
// Writers return a string and nothing else: a failure anywhere is recorded on
// the generator and reported once, by Generate.

var generators map[string]func(*generator, *Expression) string

// Registered in init rather than as a literal: the writers call back into the
// dispatcher, and Go will not let a package-level map close that loop.
func init() {
	generators = map[string]func(*generator, *Expression) string{
		"Select":        (*generator).writeSelect,
		"Union":         (*generator).writeSetOperation,
		"Except":        (*generator).writeSetOperation,
		"Intersect":     (*generator).writeSetOperation,
		"With":          (*generator).writeWith,
		"CTE":           (*generator).writeCTE,
		"TableAlias":    (*generator).writeChildThis,
		"From":          (*generator).writeFrom,
		"Table":         (*generator).writeTable,
		"Join":          (*generator).writeJoin,
		"Lateral":       (*generator).writeLateral,
		"Subquery":      (*generator).writeSubquery,
		"Where":         (*generator).writeWhere,
		"Group":         (*generator).writeGroup,
		"Having":        (*generator).writeHaving,
		"Order":         (*generator).writeOrder,
		"Ordered":       (*generator).writeOrdered,
		"Limit":         (*generator).writeLimit,
		"Offset":        (*generator).writeOffset,
		"Into":          (*generator).writeInto,
		"Star":          (*generator).writeStar,
		"Column":        (*generator).writeColumn,
		"Identifier":    (*generator).writeIdentifier,
		"Literal":       (*generator).writeLiteral,
		"Boolean":       (*generator).writeBoolean,
		"Null":          (*generator).writeNull,
		"Alias":         (*generator).writeAlias,
		"Paren":         (*generator).writeParen,
		"Case":          (*generator).writeCase,
		"If":            (*generator).writeIf,
		"Cast":          (*generator).writeCast,
		"TryCast":       (*generator).writeCast,
		"DataType":      (*generator).writeDataType,
		"DataTypeParam": (*generator).writeChildThis,
		"Anonymous":     (*generator).writeAnonymous,
		"In":            (*generator).writeIn,
		"Between":       (*generator).writeBetween,
		"Dot":           (*generator).writeDot,
		"Distinct":      (*generator).writeDistinct,
		"Array":         (*generator).writeArray,
		"Window":        (*generator).writeWindow,
		"Interval":      (*generator).writeInterval,
		"Lambda":        (*generator).writeLambda,
		"IntervalSpan":  (*generator).writeIntervalSpan,
		"Var":           (*generator).writeVar,
		"WindowSpec":    (*generator).writeWindowSpec,
		"Bracket":       (*generator).writeBracket,
		"Slice":         (*generator).writeSlice,
		"All":           (*generator).writeQuantifier,
		"Any":           (*generator).writeQuantifier,
		"Like":          (*generator).writeLike,
		"ILike":         (*generator).writeLike,
		"Is":            (*generator).writeIs,
	}
}

func (g *generator) writeChildThis(e *Expression) string { return g.child(e, "this") }

func (g *generator) writeSelect(e *Expression) string {
	parts := []string{"SELECT"}
	add := func(s string) {
		if s != "" {
			parts = append(parts, s)
		}
	}

	add(g.child(e, "distinct"))

	// T-SQL says TOP here; every other dialect says LIMIT at the end. The
	// tree is the same either way, which is the point: the guard rewrites one
	// node and the dialect decides where it lands.
	limit, _ := e.Args["limit"].(*Expression)
	if limit != nil && g.tables.LimitIsTop {
		add(g.writeLimitWord(limit, "TOP "))
	}

	add(g.list(e))
	add(g.child(e, "into"))
	add(g.child(e, "from_"))

	// A comma join writes its own comma, so it is appended rather than spaced:
	// `FROM a, b`, not `FROM a , b`.
	joins, _ := e.Args["joins"].([]*Expression)
	for _, j := range joins {
		s := g.node(j)
		if strings.HasPrefix(s, ",") && len(parts) > 0 {
			parts[len(parts)-1] += s
			continue
		}
		add(s)
	}

	add(g.child(e, "where"))
	add(g.child(e, "group"))
	add(g.child(e, "having"))
	add(g.child(e, "order"))

	if limit != nil && !g.tables.LimitIsTop {
		add(g.node(limit))
	}
	add(g.child(e, "offset"))

	return g.withPrefix(e, strings.Join(parts, " "))
}

// withPrefix puts a WITH clause in front of whatever it qualifies.
func (g *generator) withPrefix(e *Expression, body string) string {
	with := g.child(e, "with_")
	if with == "" {
		return body
	}
	return with + " " + body
}

func (g *generator) writeSetOperation(e *Expression) string {
	word := strings.ToUpper(e.Class)
	if e.Args["distinct"] == false {
		word += " ALL"
	}
	parts := []string{g.child(e, "this"), word, g.child(e, "expression")}
	for _, key := range []string{"order", "limit", "offset"} {
		if s := g.child(e, key); s != "" {
			parts = append(parts, s)
		}
	}
	return g.withPrefix(e, strings.Join(parts, " "))
}

func (g *generator) writeWith(e *Expression) string {
	return "WITH " + g.list(e)
}

func (g *generator) writeCTE(e *Expression) string {
	alias := g.child(e, "alias")
	g.qualifyDerivedOutputs(e)
	return alias + " AS (" + g.child(e, "this") + ")"
}

func (g *generator) writeFrom(e *Expression) string { return "FROM " + g.child(e, "this") }

func (g *generator) writeTable(e *Expression) string {
	parts := []string{}
	for _, key := range []string{"catalog", "db", "this"} {
		if s := g.child(e, key); s != "" {
			parts = append(parts, s)
		}
	}
	out := strings.Join(parts, ".")
	if alias := g.child(e, "alias"); alias != "" {
		out += " AS " + alias
	}
	return out
}

func (g *generator) writeJoin(e *Expression) string {
	this := g.child(e, "this")
	// A Lateral writes its own CROSS APPLY; a comma join writes a comma.
	if inner, _ := e.Args["this"].(*Expression); inner != nil && inner.Class == "Lateral" {
		return this
	}
	words := []string{}
	for _, key := range []string{"side", "kind"} {
		if s, ok := e.Args[key].(string); ok && s != "" {
			words = append(words, s)
		}
	}
	if len(words) == 0 {
		if _, hasOn := e.Args["on"].(*Expression); !hasOn {
			return ", " + this
		}
	}
	words = append(words, "JOIN", this)
	if on := g.child(e, "on"); on != "" {
		words = append(words, "ON", on)
	}
	return strings.Join(words, " ")
}

func (g *generator) writeLateral(e *Expression) string {
	word := "OUTER APPLY "
	if e.Args["cross_apply"] == true {
		word = "CROSS APPLY "
	}
	this := g.child(e, "this")
	if alias := g.child(e, "alias"); alias != "" {
		return word + this + " AS " + alias
	}
	return word + this
}

func (g *generator) writeSubquery(e *Expression) string {
	g.qualifyDerivedOutputs(e)
	out := "(" + g.child(e, "this") + ")"
	if alias := g.child(e, "alias"); alias != "" {
		out += " AS " + alias
	}
	return out
}

func (g *generator) writeWhere(e *Expression) string  { return "WHERE " + g.child(e, "this") }
func (g *generator) writeHaving(e *Expression) string { return "HAVING " + g.child(e, "this") }
func (g *generator) writeInto(e *Expression) string   { return "INTO " + g.child(e, "this") }

func (g *generator) writeGroup(e *Expression) string {
	// `GROUP BY ALL` is carried as a flag, so the expression list is empty and
	// writing it alone produced a bare "GROUP BY".
	if all, _ := e.Args["all"].(bool); all {
		return "GROUP BY ALL"
	}
	return "GROUP BY " + g.list(e)
}

func (g *generator) writeOrder(e *Expression) string {
	return "ORDER BY " + g.list(e)
}

func (g *generator) writeOrdered(e *Expression) string {
	this := g.child(e, "this")
	// A direction that was written is written back; one that was not stays
	// unwritten, so `ORDER BY x` and `ORDER BY x ASC` stay distinct.
	switch e.Args["desc"] {
	case true:
		return this + " DESC"
	case false:
		return this + " ASC"
	}
	return this
}

func (g *generator) writeLimit(e *Expression) string { return g.writeLimitWord(e, "LIMIT ") }

// writeLimitWord is the same node under either spelling: TOP in front of the
// projections, LIMIT after the query.
func (g *generator) writeLimitWord(e *Expression, word string) string {
	count := g.child(e, "expression")
	// T-SQL requires parentheses around a TOP that is not a plain literal, and
	// the reference writes them: `SELECT TOP (A) 0`. Without them the output is
	// not valid T-SQL and neither the reference nor this port can read it back
	// -- a generator emitting SQL nobody can parse is worse than one that
	// declines. Found by fuzzing the generator's output through the parser.
	if inner, _ := e.Args["expression"].(*Expression); inner != nil &&
		word == "TOP " && inner.Class != "Literal" {
		count = "(" + count + ")"
	}
	out := word + count
	if opts, _ := e.Args["limit_options"].(*Expression); opts != nil && opts.Args["percent"] == true {
		out += " PERCENT"
	}
	return out
}

func (g *generator) writeOffset(e *Expression) string {
	return "OFFSET " + g.child(e, "expression")
}

func (g *generator) writeStar(*Expression) string { return "*" }
func (g *generator) writeNull(*Expression) string { return "NULL" }

// writeDistinct serves both `SELECT DISTINCT`, where the node is bare, and
// `COUNT(DISTINCT a)`, where it carries the arguments it distinguishes.
func (g *generator) writeDistinct(e *Expression) string {
	if items := g.list(e); items != "" {
		return "DISTINCT " + items
	}
	return "DISTINCT"
}

func (g *generator) writeColumn(e *Expression) string {
	parts := []string{}
	for _, key := range []string{"catalog", "db", "table", "this"} {
		if s := g.child(e, key); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, ".")
}

func (g *generator) writeIdentifier(e *Expression) string {
	name, _ := e.Args["this"].(string)
	// A reserved word must be quoted even when the caller wrote it bare:
	// DuckDB reserves `all`, and `SELECT 1 AS all` is a syntax error unquoted.
	quoted := e.Args["quoted"] == true
	if !quoted && g.tables.ReservedKeywords[strings.ToUpper(name)] {
		quoted = true
	}
	if quoted {
		// The delimiters the dialect WRITES, which are not always the ones it
		// reads: T-SQL accepts "x" and writes [x]. A closing delimiter inside
		// the name is doubled, or the name would end early.
		open, close := g.tables.IdentifierStart, g.tables.IdentifierEnd
		return open + strings.ReplaceAll(name, close, close+close) + close
	}
	return name
}

func (g *generator) writeLiteral(e *Expression) string {
	text, _ := e.Args["this"].(string)
	if e.Args["is_string"] == true {
		return "'" + escapeStringBody(text, g.cfg.StringEscapes) + "'"
	}
	return text
}

// escapeStringBody escapes a quote the way THIS DIALECT escapes it.
//
// It used to double the quote for every dialect, which is right for T-SQL,
// PostgreSQL and DuckDB and wrong for Databricks, where the escape is a
// backslash. A string holding a single quote came out as four quotes, and
// Databricks reads that as two adjacent empty strings concatenated -- a
// different value, silently. The reference writes a backslash-escaped quote.
// Found by fuzzing what the generator writes back through the parser.
//
// The set it reads is the TOKENIZER's own, generated per dialect from the
// reference, so the two halves cannot disagree about what an escape is.
func escapeStringBody(text string, escapes set) string {
	if _, doubles := escapes["'"]; doubles || len(escapes) == 0 {
		return strings.ReplaceAll(text, "'", "''")
	}
	// One escape character, chosen deterministically: map iteration order must
	// never decide what SQL this emits.
	chars := make([]string, 0, len(escapes))
	for c := range escapes {
		chars = append(chars, c)
	}
	sort.Strings(chars)
	escape := chars[0]
	// The escape escapes itself, and must be done first, or the one written
	// for a quote below would be escaped in turn.
	text = strings.ReplaceAll(text, escape, escape+escape)
	return strings.ReplaceAll(text, "'", escape+"'")
}

func (g *generator) writeBoolean(e *Expression) string {
	// T-SQL has no boolean literal. The reference rewrites TRUE to 1 where a
	// value is wanted and to `1 = 1` where a condition is, and `a IS TRUE` to
	// `a = 1` -- a transform over the tree that depends on where the literal
	// sits. None of that is ported, and writing TRUE anyway handed the engine
	// a statement it rejects, from a query the Python executor ran fine.
	if !g.tables.WritesBooleanLiteral {
		return g.fail("Boolean")
	}
	if e.Args["this"] == true {
		return "TRUE"
	}
	return "FALSE"
}

func (g *generator) writeAlias(e *Expression) string {
	return g.child(e, "this") + " AS " + g.child(e, "alias")
}

func (g *generator) writeParen(e *Expression) string { return "(" + g.child(e, "this") + ")" }

func (g *generator) writeCase(e *Expression) string {
	out := "CASE"
	if subject := g.child(e, "this"); subject != "" {
		out += " " + subject
	}
	// The branches are rendered HERE rather than through the node dispatch:
	// exp.If is both a CASE branch and the standalone IF(a, b, c) function,
	// and the two are written nothing alike.
	branches, _ := e.Args["ifs"].([]*Expression)
	parts := make([]string, 0, len(branches))
	for _, b := range branches {
		if b == nil || b.Class != "If" {
			return g.fail("CASE branch")
		}
		parts = append(parts, "WHEN "+g.child(b, "this")+" THEN "+g.child(b, "true"))
	}
	if len(parts) > 0 {
		out += " " + strings.Join(parts, " ")
	}
	if deflt := g.child(e, "default"); deflt != "" {
		out += " ELSE " + deflt
	}
	return out + " END"
}

// writeIf is reached only for a STANDALONE If -- CASE renders its own
// branches. The reference writes this one three different ways: T-SQL as
// IIF with the condition coerced to a comparison, Databricks as IF, and
// PostgreSQL and DuckDB as a CASE expression. None of that is ported, and
// writing the branch form here produced `SELECT WHEN x > 0 THEN 'positive'`,
// which is not SQL. Refused until the dialect rules are ported.
func (g *generator) writeIf(e *Expression) string {
	return g.fail("If")
}

func (g *generator) writeCast(e *Expression) string {
	word := "CAST"
	if e.Class == "TryCast" {
		word = "TRY_CAST"
	}
	return word + "(" + g.child(e, "this") + " AS " + g.child(e, "to") + ")"
}

func (g *generator) writeDataType(e *Expression) string {
	kind, _ := e.Args["this"].(DataTypeKind)
	out, ok := g.tables.TypeSQL[string(kind)]
	if !ok {
		return g.fail("DataType." + string(kind))
	}
	if params := g.list(e); params != "" {
		out += "(" + params + ")"
	}
	return out
}

func (g *generator) writeAnonymous(e *Expression) string { return g.anonymous(e, true) }

func (g *generator) anonymous(e *Expression, upper bool) string {
	name, quoted := anonymousName(e)
	if upper {
		name = strings.ToUpper(name)
	}
	if quoted {
		// The reference uppercases the name and THEN quotes it, brackets in
		// T-SQL and double quotes elsewhere: `"myfunc"()` comes back as
		// `[MYFUNC]()`. Dropping the quotes wrote a different identifier.
		open, close := g.tables.IdentifierStart, g.tables.IdentifierEnd
		name = open + strings.ReplaceAll(name, close, close+close) + close
	}
	return name + "(" + g.list(e) + ")"
}

func (g *generator) writeIn(e *Expression) string {
	return g.child(e, "this") + " IN (" + g.list(e) + ")"
}

func (g *generator) writeBetween(e *Expression) string {
	return g.child(e, "this") + " BETWEEN " + g.child(e, "low") + " AND " + g.child(e, "high")
}

// writeDot joins a qualifier to what it qualifies. A call written after a dot
// keeps the case it was written in, where a bare one is upper-cased -- the
// reference writes `dbo.f(1)` but `F(1)`, and the two executors have to agree
// on which.
func (g *generator) writeDot(e *Expression) string {
	left := g.child(e, "this")
	if fn, _ := e.Args["expression"].(*Expression); fn != nil && fn.Class == "Anonymous" {
		return left + "." + g.anonymous(fn, false)
	}
	return left + "." + g.child(e, "expression")
}

// writeLike carries the negation on the node, as the reference does: NOT LIKE
// is one operator, not a Not wrapping a Like.
func (g *generator) writeLike(e *Expression) string {
	op := g.tables.BinarySQL[e.Class]
	if e.Args["negate"] == true {
		op = "NOT " + op
	}
	return g.binary(e, op)
}

// writeIs does the same for IS NOT NULL.
func (g *generator) writeIs(e *Expression) string {
	op := "IS"
	if e.Args["negate"] == true {
		op = "IS NOT"
	}
	return g.binary(e, op)
}

// qualifyDerivedOutputs names the unnamed columns of a CTE or derived table,
// where the dialect insists on it. T-SQL does; nothing else here does.
//
// It mutates the tree, as the reference does -- and it matters that both
// executors do it, because a Python executor emitting `SELECT a AS a` and a Go
// one emitting `SELECT a` would be sending the engine different statements for
// the same question.
func (g *generator) qualifyDerivedOutputs(e *Expression) {
	if !g.tables.QualifiesDerivedOutputs {
		return
	}
	alias, _ := e.Args["alias"].(*Expression)
	if alias == nil || alias.Args["columns"] != nil {
		return
	}
	query, _ := e.Args["this"].(*Expression)
	if query == nil || query.Class != "Select" {
		return
	}
	projections, _ := query.Args["expressions"].([]*Expression)
	for i, projection := range projections {
		// Already named, or a star expansion, which names nothing.
		if projection == nil || projection.Class == "Alias" || isStarProjection(projection) {
			continue
		}
		name, quoted := outputName(projection)
		if name == "" {
			// A positional name is a name, so it needs no quoting whatever
			// the projection it replaced would have wanted.
			name, quoted = "_col_"+strconv.Itoa(i), false
		}
		projections[i] = New("Alias",
			Arg{"this", projection},
			Arg{"alias", New("Identifier", Arg{"this", name}, Arg{"quoted", quoted})})
	}
	query.Set("expressions", projections)
}

// outputName is the name a projection already carries: a column's own name, a
// literal's text. Anything else has none, and gets a positional one. A column
// keeps the quoting it was written with; a synthesised name is quoted only if
// it would not otherwise be a name.
func outputName(e *Expression) (string, bool) {
	switch e.Class {
	case "Column":
		id, _ := e.Args["this"].(*Expression)
		return id.Name(), id != nil && id.Args["quoted"] == true
	case "Literal":
		name, _ := e.Args["this"].(string)
		return name, !isPlainName(name)
	}
	return "", false
}

// isStarProjection reports whether a projection is `*` or `t.*`, neither of
// which names a column to alias.
func isStarProjection(e *Expression) bool {
	if e.Class == "Star" {
		return true
	}
	if e.Class == "Column" {
		if this, _ := e.Args["this"].(*Expression); this != nil && this.Class == "Star" {
			return true
		}
	}
	return false
}

// isPlainName is the reference's SAFE_IDENTIFIER_RE: a name that needs no
// quoting is a letter or underscore followed by word characters.
func isPlainName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		letter := r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		digit := i > 0 && r >= '0' && r <= '9'
		if !letter && !digit {
			return false
		}
	}
	return true
}

// writeArray uses the dialect's own delimiters: `[1, 2]` in DuckDB,
// `ARRAY[1, 2]` in PostgreSQL, `ARRAY(1, 2)` elsewhere.
func (g *generator) writeArray(e *Expression) string {
	// The delimiters are for a literal list. Over a QUERY, DuckDB writes
	// `ARRAY((SELECT ...))` rather than `[(SELECT ...)]` -- a different
	// spelling for a different thing -- so that form is refused.
	items, _ := e.Args["expressions"].([]*Expression)
	for _, item := range items {
		if holdsAQuery(item) {
			return g.fail("array over a query")
		}
	}
	return g.tables.ArrayOpen + g.list(e) + g.tables.ArrayClose
}

func holdsAQuery(e *Expression) bool {
	if e == nil {
		return false
	}
	switch e.Class {
	case "Select", "Subquery", "Union", "Except", "Intersect":
		return true
	case "Paren":
		inner, _ := e.Args["this"].(*Expression)
		return holdsAQuery(inner)
	}
	return false
}

// writeBracket is reached only where the dialect does NOT rewrite a subscript;
// the parser refuses it where it does, so nothing here has to shift an index.
func (g *generator) writeBracket(e *Expression) string {
	// A dialect may write a subscript as a CALL: Databricks renders a Bracket
	// as ELEMENT_AT(x, i). Its own spelling wins where it has one, or the port
	// would emit `x[i]` for a statement the reference writes as a function.
	if spec, ok := g.functionSpelling(e); ok {
		return g.namedFunction(e, spec)
	}
	return g.child(e, "this") + "[" + g.list(e) + "]"
}

func (g *generator) writeSlice(e *Expression) string {
	return g.child(e, "this") + ":" + g.child(e, "expression")
}

// writeQuantifier refuses: how the reference spaces one depends on the
// operator ABOVE it -- `x = ANY(ARRAY[1])` is written tight and
// `x LIKE ALL (ARRAY[1])` is not, from the same Paren underneath. That rule is
// not ported, so the port reads the quantifier and declines to write it rather
// than emit a statement spaced differently from the one the Python executor
// sends.
func (g *generator) writeQuantifier(*Expression) string { return g.fail("quantifier") }

// writeWindow writes the OVER clause. A named window (`OVER w`) carries an
// alias instead of a body.
func (g *generator) writeWindow(e *Expression) string {
	out := g.child(e, "this") + " OVER "
	if alias := g.child(e, "alias"); alias != "" {
		return out + alias
	}
	parts := []string{}
	if items, _ := e.Args["partition_by"].([]*Expression); len(items) > 0 {
		rendered := make([]string, 0, len(items))
		for _, item := range items {
			rendered = append(rendered, g.node(item))
		}
		parts = append(parts, "PARTITION BY "+strings.Join(rendered, ", "))
	}
	if order := g.child(e, "order"); order != "" {
		parts = append(parts, order)
	}
	if spec := g.child(e, "spec"); spec != "" {
		parts = append(parts, spec)
	}
	return out + "(" + strings.Join(parts, " ") + ")"
}

// writeWindowSpec writes the frame. A single bound is written back in the
// BETWEEN form with CURRENT ROW supplied, which is what the reference does:
// `ROWS 3 PRECEDING` comes back as `ROWS BETWEEN 3 PRECEDING AND CURRENT ROW`.
func (g *generator) writeWindowSpec(e *Expression) string {
	kind, _ := e.Args["kind"].(string)
	start := g.frameBound(e, "start", "start_side")
	end := g.frameBound(e, "end", "end_side")
	if end == "" {
		end = "CURRENT ROW"
	}
	if start == "" {
		return g.fail("WindowSpec without a start")
	}
	return kind + " BETWEEN " + start + " AND " + end
}

func (g *generator) frameBound(e *Expression, key, sideKey string) string {
	var text string
	switch v := e.Args[key].(type) {
	case string:
		text = v
	case *Expression:
		text = g.node(v)
	}
	if text == "" {
		return ""
	}
	if side, _ := e.Args[sideKey].(string); side != "" {
		return text + " " + side
	}
	return text
}

func (g *generator) writeVar(e *Expression) string {
	name, _ := e.Args["this"].(string)
	return name
}

// writeInterval puts the unit where the dialect puts it: PostgreSQL writes
// `INTERVAL '1 DAY'`, everyone else `INTERVAL '1' DAY`. A span (HOUR TO
// SECOND) is never folded into the string.
func (g *generator) writeInterval(e *Expression) string {
	this, _ := e.Args["this"].(*Expression)
	unit, _ := e.Args["unit"].(*Expression)
	if unit == nil {
		return "INTERVAL " + g.node(this)
	}
	if g.tables.IntervalUnitInsideString && unit.Class == "Var" &&
		this != nil && this.Class == "Literal" && this.Args["is_string"] == true {
		text, _ := this.Args["this"].(string)
		return "INTERVAL '" + escapeStringBody(text, g.cfg.StringEscapes) +
			" " + g.node(unit) + "'"
	}
	return "INTERVAL " + g.node(this) + " " + g.node(unit)
}

func (g *generator) writeIntervalSpan(e *Expression) string {
	return g.child(e, "this") + " TO " + g.child(e, "expression")
}

// writeLambda: one parameter is written bare, more than one parenthesised --
// `x -> x > 1` and `(x, y) -> x > y`.
func (g *generator) writeLambda(e *Expression) string {
	params, _ := e.Args["expressions"].([]*Expression)
	rendered := make([]string, 0, len(params))
	for _, prm := range params {
		rendered = append(rendered, g.node(prm))
	}
	head := strings.Join(rendered, ", ")
	if len(params) != 1 {
		head = "(" + head + ")"
	}
	return head + " -> " + g.child(e, "this")
}

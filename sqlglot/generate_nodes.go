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
		"Select":            (*generator).writeSelect,
		"Union":             (*generator).writeSetOperation,
		"Except":            (*generator).writeSetOperation,
		"Intersect":         (*generator).writeSetOperation,
		"With":              (*generator).writeWith,
		"CTE":               (*generator).writeCTE,
		"TableAlias":        (*generator).writeTableAlias,
		"From":              (*generator).writeFrom,
		"Table":             (*generator).writeTable,
		"Join":              (*generator).writeJoin,
		"Lateral":           (*generator).writeLateral,
		"Subquery":          (*generator).writeSubquery,
		"Where":             (*generator).writeWhere,
		"Group":             (*generator).writeGroup,
		"Having":            (*generator).writeHaving,
		"Order":             (*generator).writeOrder,
		"Ordered":           (*generator).writeOrdered,
		"Limit":             (*generator).writeLimit,
		"Offset":            (*generator).writeOffset,
		"Into":              (*generator).writeInto,
		"Star":              (*generator).writeStar,
		"Column":            (*generator).writeColumn,
		"Identifier":        (*generator).writeIdentifier,
		"Literal":           (*generator).writeLiteral,
		"National":          (*generator).writeNational,
		"Boolean":           (*generator).writeBoolean,
		"Null":              (*generator).writeNull,
		"Alias":             (*generator).writeAlias,
		"Paren":             (*generator).writeParen,
		"Case":              (*generator).writeCase,
		"If":                (*generator).writeIf,
		"Cast":              (*generator).writeCast,
		"TryCast":           (*generator).writeCast,
		"DataType":          (*generator).writeDataType,
		"DataTypeParam":     (*generator).writeChildThis,
		"Placeholder":       (*generator).writePlaceholder,
		"Escape":            (*generator).writeEscape,
		"Parameter":         (*generator).writeParameter,
		"ColumnDef":         (*generator).writeColumnDef,
		"Anonymous":         (*generator).writeAnonymous,
		"In":                (*generator).writeIn,
		"Between":           (*generator).writeBetween,
		"Dot":               (*generator).writeDot,
		"Distinct":          (*generator).writeDistinct,
		"Array":             (*generator).writeArray,
		"Window":            (*generator).writeWindow,
		"Interval":          (*generator).writeInterval,
		"Lambda":            (*generator).writeLambda,
		"Struct":            (*generator).writeStruct,
		"Unnest":            (*generator).writeUnnest,
		"AtTimeZone":        (*generator).writeAtTimeZone,
		"JSONPath":          (*generator).writeJSONPath,
		"JSONKeyValue":      (*generator).writeJSONKeyValue,
		"Pivot":             (*generator).writePivot,
		"Version":           (*generator).writeVersion,
		"ForClause":         (*generator).writeForClause,
		"QueryOption":       (*generator).writeQueryOption,
		"XMLKeyValueOption": (*generator).writeXMLKeyValueOption,
		"JSONExtract":       (*generator).writeJSONExtractOp,
		"JSONExtractScalar": (*generator).writeJSONExtractOp,
		"PropertyEQ":        (*generator).writePropertyEQ,
		"Filter":            (*generator).writeFilter,
		"Extract":           (*generator).writeSyntaxFunction,
		"Trim":              (*generator).writeSyntaxFunction,
		"Substring":         (*generator).writeSyntaxFunction,
		"StrPosition":       (*generator).writeSyntaxFunction,
		"IntervalSpan":      (*generator).writeIntervalSpan,
		"Var":               (*generator).writeVar,
		"WindowSpec":        (*generator).writeWindowSpec,
		"Bracket":           (*generator).writeBracket,
		"Slice":             (*generator).writeSlice,
		"All":               (*generator).writeQuantifier,
		"Any":               (*generator).writeQuantifier,
		"Like":              (*generator).writeLike,
		"ILike":             (*generator).writeLike,
		"Is":                (*generator).writeIs,
	}
}

func (g *generator) writeChildThis(e *Expression) string { return g.child(e, "this") }

func (g *generator) writeSelect(e *Expression) string {
	parts := []string{"SELECT"}
	add := func(s string) {
		// A clause that brings its OWN leading separator is joined without a
		// second one. The reference's writers return `" FETCH ..."` with the
		// space attached, and the templates are recorded as the reference
		// returns them, so trimming here is what keeps the two models -- its
		// separator-carrying one and this one's join-with-a-space -- agreeing.
		s = strings.TrimLeft(s, " ")
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

	// A FETCH is written AFTER the offset where a LIMIT is written before it:
	// `OFFSET 5 FETCH NEXT 1 ROWS ONLY` but `LIMIT 1 OFFSET 2`. Both live in
	// the same slot, so it is the CLASS that decides, not the slot.
	if limit != nil && !g.tables.LimitIsTop && limit.Class != "Fetch" {
		add(g.node(limit))
	}
	add(g.child(e, "offset"))
	if limit != nil && !g.tables.LimitIsTop && limit.Class == "Fetch" {
		add(g.node(limit))
	}
	// FOR XML / FOR JSON / FOR BROWSE comes after the query's own clauses.
	add(g.child(e, "for_"))
	// Row locking comes last, and there may be more than one of them.
	locks, _ := e.Args["locks"].([]*Expression)
	for _, lock := range locks {
		add(g.node(lock))
	}

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
	// The temporal clause comes before the alias, as it does in the text.
	if version := g.child(e, "version"); version != "" {
		parts := strings.SplitN(out, " AS ", 2)
		if len(parts) == 2 {
			out = parts[0] + " " + version + " AS " + parts[1]
		} else {
			out += " " + version
		}
	}
	// TABLESAMPLE hangs off the table, after the alias. Its own template
	// carries the space in front of it, the way the reference returns it.
	out += g.child(e, "sample")
	// Then the pivots, in the order they were written, each carrying its own
	// leading space too.
	pivots, _ := e.Args["pivots"].([]*Expression)
	for _, pivot := range pivots {
		out += g.node(pivot)
	}
	return out + g.joins(e)
}

// joins writes the joins hanging off a FROM item. Inside a parenthesised item
// they belong to the table or subquery itself rather than to a Select, which
// is the one place a join does not live on the query. A comma join writes its
// own comma and is appended rather than spaced.
func (g *generator) joins(e *Expression) string {
	joins, _ := e.Args["joins"].([]*Expression)
	out := ""
	for _, j := range joins {
		s := g.node(j)
		if strings.HasPrefix(s, ",") {
			out += s
			continue
		}
		out += " " + s
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
			// A comma join over an UNNEST is rewritten by the reference into
			// `JOIN ... ON TRUE`, which is a different tree and not a
			// spelling of this one.
			if inner, _ := e.Args["this"].(*Expression); inner != nil && inner.Class == "Unnest" {
				return g.fail("comma join over an UNNEST")
			}
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
	// The parentheses wrap what the subquery IS; a join hanging off it comes
	// after them, the same way it comes after a table.
	out := "(" + g.child(e, "this") + ")"
	// The ALIAS names the subquery and the joins come after it, exactly as
	// they do for a table. Writing the joins first produced
	// `((A), A AS A AS A)` from `((A) A, A A)` -- the alias pushed past the
	// comma onto the joined table, which already had one.
	if alias := g.child(e, "alias"); alias != "" {
		out += " AS " + alias
	}
	// A pivot hangs off a subquery the same way it hangs off a table, and
	// carries its own leading space.
	pivots, _ := e.Args["pivots"].([]*Expression)
	for _, pivot := range pivots {
		out += g.node(pivot)
	}
	return out + g.joins(e)
}

func (g *generator) writeWhere(e *Expression) string {
	// The WHERE operand is a condition, which is what decides how a boolean
	// there is written.
	if child, _ := e.Args["this"].(*Expression); child != nil {
		g.markCondition(child)
	}
	return "WHERE " + g.child(e, "this")
}
func (g *generator) writeHaving(e *Expression) string { return "HAVING " + g.child(e, "this") }

// writeInto keeps the table KIND that sits before the name: `INTO UNLOGGED foo`.
func (g *generator) writeInto(e *Expression) string {
	out := "INTO "
	if unlogged, _ := e.Args["unlogged"].(bool); unlogged && g.tables.WritesIntoUnlogged {
		out += "UNLOGGED "
	}
	return out + g.child(e, "this")
}

func (g *generator) writeGroup(e *Expression) string {
	// The leading space is the SEPARATOR, carried the way the reference
	// carries it -- `g.sql(group)` there is " GROUP BY b". A query's clause
	// joiner trims it back off; the statement-level PIVOT template does not,
	// and without it wrote `USING SUM(x)GROUP BY y`.
	//
	// `GROUP BY ALL` is carried as a flag, so the expression list is empty and
	// writing it alone produced a bare "GROUP BY".
	if all, _ := e.Args["all"].(bool); all {
		return " GROUP BY ALL"
	}
	return " GROUP BY " + g.list(e)
}

func (g *generator) writeOrder(e *Expression) string {
	// As a query clause the Order stands alone; inside a call it wraps the
	// argument it follows, and that argument is written before the keyword.
	if this := g.child(e, "this"); this != "" {
		return this + " ORDER BY " + g.list(e)
	}
	return "ORDER BY " + g.list(e)
}

func (g *generator) writeOrdered(e *Expression) string {
	this := g.child(e, "this")
	// A direction that was written is written back; one that was not stays
	// unwritten, so `ORDER BY x` and `ORDER BY x ASC` stay distinct.
	desc := false
	switch e.Args["desc"] {
	case true:
		desc = true
		this += " DESC"
	case false:
		this += " ASC"
	}
	// NULLS FIRST/LAST is written only when it DIFFERS from where this dialect
	// puts nulls by default for that direction -- otherwise the clause says
	// nothing and the reference leaves it out. T-SQL has no clause at all.
	if !g.tables.WritesNullsOrdering {
		return this
	}
	nullsFirst, ok := e.Args["nulls_first"].(bool)
	if !ok || nullsFirst == g.defaultNullsFirst(desc) {
		return this
	}
	if nullsFirst {
		return this + " NULLS FIRST"
	}
	return this + " NULLS LAST"
}

// defaultNullsFirst is where this dialect puts nulls when the statement does
// not say -- probed per direction rather than derived, since deriving it a
// second time here got Databricks wrong.
func (g *generator) defaultNullsFirst(desc bool) bool {
	if desc {
		return g.tables.DefaultNullsFirstDesc
	}
	return g.tables.DefaultNullsFirstAsc
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

// writeNational writes a national string. It has a writer of its own
// rather than a template because a template substitutes text VERBATIM,
// and the body needs the same quote escaping any other string gets --
// without it a body containing quotes came out unterminated, which the
// generator fuzzer found 55 times in a single run.
func (g *generator) writeNational(e *Expression) string {
	text, _ := e.Args["this"].(string)
	return "N'" + escapeStringBody(text, g.cfg.StringEscapes) + "'"
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

// writeBoolean writes the spelling this dialect uses in this POSITION. T-SQL
// has no boolean literal: it writes 1 where a value is wanted and (1 = 1)
// where a condition is, so the answer depends on where the node sits rather
// than on the node.
func (g *generator) writeBoolean(e *Expression) string {
	isTrue := e.Args["this"] == true
	if g.conditions[e] {
		if isTrue {
			return g.tables.Boolean.TrueCondition
		}
		return g.tables.Boolean.FalseCondition
	}
	if isTrue {
		return g.tables.Boolean.TrueValue
	}
	return g.tables.Boolean.FalseValue
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
		if cond, _ := b.Args["this"].(*Expression); cond != nil {
			g.markCondition(cond)
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
// writeIf writes a conditional. Most dialects spell it `CASE WHEN … END` and
// Databricks `IF(…)`; T-SQL spells it `IIF(cond <> 0, …)`, comparing the
// condition against zero because it has no boolean type.
//
// That `<> 0` is the catch: the reference appends it only where the condition
// is NOT already a comparison, and a template cannot tell. Applying it anyway
// wrote `IIF(cond <> 0 <> 0, …)`. So where the template carries it and the
// condition is already a condition, this refuses rather than doubling it.
func (g *generator) writeIf(e *Expression) string {
	for _, candidate := range g.tables.SyntaxSQL[e.Class] {
		if !strings.Contains(candidate.Template, "<> 0") {
			continue
		}
		if cond, _ := e.Args["this"].(*Expression); isKnownBoolean(cond) {
			return g.fail("If over a condition, where the dialect compares against zero")
		}
	}
	out, ok := g.syntaxTemplate(e)
	if !ok {
		return g.fail("If")
	}
	return out
}

func (g *generator) writeCast(e *Expression) string {
	word := "CAST"
	if e.Class == "TryCast" {
		word = "TRY_CAST"
	}
	return word + "(" + g.child(e, "this") + " AS " + g.child(e, "to") + ")"
}

// writePlaceholder writes a bound parameter. The spelling is the dialect's:
// `$name` in DuckDB, `%(name)s` in PostgreSQL, `:name` elsewhere.
func (g *generator) writePlaceholder(e *Expression) string {
	// The name is a bare string in most dialects and an IDENTIFIER in
	// PostgreSQL, whose `%(name)s` form the reference reads that way. Handling
	// only the string dropped every PostgreSQL name and wrote `%s`.
	var name string
	switch v := e.Args["this"].(type) {
	case string:
		name = v
	case *Expression:
		name = g.node(v)
	}
	if name == "" {
		if jdbc, _ := e.Args["jdbc"].(bool); jdbc {
			return g.tables.Placeholder.AnonymousJDBCSQL
		}
		return g.tables.Placeholder.Anonymous
	}
	// A name holding a DOLLAR is refused. `$` opens dollar-quoting on the way
	// back in, so `Placeholder(¹$)` writes `$¹$`, which the port then cannot
	// read: it is looking for a closing `$¹$` that is not there. The
	// reference writes the same `$¹$` and gets away with it only because it
	// keeps the trailing comment that follows in the statement it came from.
	//
	// Found by the generator fuzzer on `$¹$---- `, where both build the very
	// same node and only the writing differs.
	if strings.Contains(name, "$") {
		return g.fail(e.Class + " whose name holds a dollar")
	}
	return strings.ReplaceAll(g.tables.Placeholder.Named, "{name}", name)
}

// writeParameter writes `@x` -- a different node from a bound parameter, and
// spelled differently again: `$x` in DuckDB and PostgreSQL, `${x}` in
// Databricks.
func (g *generator) writeParameter(e *Expression) string {
	this, _ := e.Args["this"].(*Expression)
	if this == nil {
		return g.fail("Parameter without a name")
	}
	return strings.ReplaceAll(g.tables.Placeholder.Parameter, "{name}", g.node(this))
}

// writeEscape writes the escape character a LIKE was given. Every dialect
// this port configures spells it the same way.
func (g *generator) writeEscape(e *Expression) string {
	return g.child(e, "this") + " ESCAPE " + g.child(e, "expression")
}

func (g *generator) writeDataType(e *Expression) string {
	// An INTERVAL type's `this` is an Interval NODE carrying the unit, not a
	// type name: `CAST(x AS INTERVAL DAY)`.
	if inner, ok := e.Args["this"].(*Expression); ok && inner != nil {
		if inner.Class != "Interval" {
			return g.fail("DataType over " + inner.Class)
		}
		unit, _ := inner.Args["unit"].(*Expression)
		if unit == nil {
			return "INTERVAL"
		}
		return "INTERVAL " + g.node(unit)
	}
	kind, _ := e.Args["this"].(DataTypeKind)
	out, ok := g.tables.TypeSQL[string(kind)]
	if !ok {
		return g.fail("DataType." + string(kind))
	}
	params := g.list(e)
	if params == "" {
		// A nested type with no members is written bare: PostgreSQL's ARRAY.
		return out
	}
	nested, _ := e.Args["nested"].(bool)
	if !nested {
		return out + "(" + params + ")"
	}
	// An ARRAY is the one nested type that does not wrap its name around its
	// member -- DuckDB suffixes brackets to the member itself -- so it takes a
	// template rather than the delimiters the others share.
	if kind == "ARRAY" {
		ct := g.tables.CompositeType
		tmpl := ct.ArrayTemplate
		if values, _ := e.Args["values"].([]*Expression); len(values) > 0 {
			if len(values) != 1 {
				return g.fail("array type with several sizes")
			}
			tmpl = strings.ReplaceAll(ct.ArraySizedTemplate, "{size}", g.node(values[0]))
		}
		return strings.ReplaceAll(tmpl, "{inner}", params)
	}
	return out + g.tables.CompositeType.StructOpen + params + g.tables.CompositeType.StructClose
}

// writeColumnDef writes one named field of a STRUCT-like type. The separator
// is the dialect's: Databricks writes `a: INT` where the others write `a INT`.
func (g *generator) writeColumnDef(e *Expression) string {
	return g.child(e, "this") + g.tables.CompositeType.StructFieldSep + g.child(e, "kind")
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
	// A subquery brings its own parentheses; a list is given them here.
	if q, ok := e.Args["query"].(*Expression); ok && q != nil {
		return g.childOperand(e, "this") + " IN " + g.node(q)
	}
	return g.childOperand(e, "this") + " IN (" + g.list(e) + ")"
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
	// `a IS TRUE` is not `a IS 1`: in a dialect with no boolean the reference
	// rewrites the whole comparison to `a = 1`, which is a different NODE, not
	// a spelling of this one.
	if !g.tables.WritesBooleanLiteral {
		for _, key := range []string{"this", "expression"} {
			if child, _ := e.Args[key].(*Expression); child != nil && child.Class == "Boolean" {
				return g.fail("IS over a boolean in a dialect that has none")
			}
		}
	}
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
	// Written back in the dialect's own numbering: the parser subtracted the
	// offset, so writing adds it again. Without this the port read `a[1]`
	// correctly and then wrote `a[0]`, which is a different element.
	this, _ := e.Args["this"].(*Expression)
	items, _ := e.Args["expressions"].([]*Expression)
	shifted, ok := ApplyIndexOffset(this, items, g.tables.IndexOffset, g.dialect)
	if !ok {
		return g.fail("subscript the port cannot type")
	}
	parts := make([]string, 0, len(shifted))
	for _, item := range shifted {
		parts = append(parts, g.node(item))
	}
	return g.child(e, "this") + "[" + strings.Join(parts, ", ") + "]"
}

func (g *generator) writeSlice(e *Expression) string {
	return g.child(e, "this") + ":" + g.child(e, "expression")
}

// writeQuantifier writes ALL or ANY before its operand. The two are spaced
// differently -- `ALL (x)` and `ANY(x)` -- and that is a property of the class,
// not of the operator above it. Comparing an ALL against an ANY suggested
// otherwise, and this used to refuse on the strength of that.
func (g *generator) writeQuantifier(e *Expression) string {
	this, _ := e.Args["this"].(*Expression)
	if this == nil {
		return g.fail("quantifier without an operand")
	}
	// Over a query the spacing and the parentheses both differ from the array
	// form, so it takes its own template. The template supplies the
	// parentheses, which is why a Subquery operand is unwrapped first rather
	// than rendered -- otherwise the query arrives already parenthesised and
	// comes out with two pairs.
	inner, key := this, e.Class+"Unwrapped"
	if inner.Class == "Subquery" {
		// A subquery arrives with its own parentheses; a bare query does not,
		// and the reference spaces the two differently -- `ANY (SELECT 1)`
		// against `ANY(SELECT 1)`. Both spellings are probed.
		inner, _ = inner.Args["this"].(*Expression)
		key = e.Class
	}
	if inner != nil && isQuery(inner) {
		tmpl, ok := g.tables.QuantifierQuerySQL[key]
		if !ok {
			return g.fail("quantifier over a query")
		}
		return strings.ReplaceAll(tmpl, "{query}", g.node(inner))
	}
	// An operand that brings its own parentheses is written tight against the
	// word and one that does not is spaced: `ANY(x)` but `ANY ('a', 'b')`.
	// Both spellings are probed; the condition is the reference's own.
	spelling := e.Class
	if inner != nil && inner.Class != "Paren" {
		spelling = e.Class + "Value"
	}
	prefix, found := g.tables.QuantifierSQL[spelling]
	if !found {
		if prefix, found = g.tables.QuantifierSQL[e.Class]; !found {
			return g.fail("quantifier")
		}
	}
	return prefix + g.child(e, "this")
}

// isQuery reports whether a node is a query rather than a value.
func isQuery(e *Expression) bool {
	switch e.Class {
	case "Select", "Union", "Intersect", "Except":
		return true
	}
	return false
}

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

// writeSyntaxFunction fills the template the reference itself produced for
// this class, this dialect and this set of present arguments. None of these is
// written `NAME(a, b, c)` and no two dialects agree -- DuckDB writes STRPOS,
// T-SQL CHARINDEX, PostgreSQL POSITION(a IN b) -- so nothing here is spelled
// out, only substituted.
func (g *generator) writeSyntaxFunction(e *Expression) string {
	if out, ok := g.syntaxTemplate(e); ok {
		return out
	}
	return g.fail(e.Class)
}

// syntaxTemplate fills the template recorded for this class and this set of
// present arguments, or reports that there is none.
func (g *generator) syntaxTemplate(e *Expression) (string, bool) {
	if g.refuseSensitive(e) {
		return "", true
	}
	present := map[string]bool{}
	for key, value := range e.Args {
		switch v := value.(type) {
		case *Expression:
			if v != nil {
				present[key] = true
			}
		case []*Expression:
			if len(v) > 0 {
				present[key] = true
			}
		case string:
			if v != "" {
				present[key] = true
			}
		case bool:
			// A flag counts. `WITH UNIQUE KEYS` is carried as one, and with
			// bools invisible here the template that writes only the pairs
			// matched a node that also carried the flag -- and DROPPED it,
			// writing a statement that says something else.
			if v {
				present[key] = true
			}
		}
	}
	for _, candidate := range g.tables.SyntaxSQL[e.Class] {
		if len(candidate.Keys) != len(present) {
			continue
		}
		// Never write a call the PARSER would refuse to read. T-SQL spells a
		// Sha as `HASHBYTES('SHA1', x)`, and HASHBYTES is a builder that
		// inspects its first argument to decide the class -- which the port
		// refuses rather than half-implement. Writing it anyway produced SQL
		// the port could not read back: the generator fuzzer found it, and
		// the adjudicator called it the port's own.
		if g.parserWouldRefuse(templateName(candidate.Template)) {
			continue
		}
		// A template that starts with an argument writes it with nothing in
		// front, so an operand that would need brackets is refused rather than
		// written flat -- the template knows no precedence.
		if strings.HasPrefix(candidate.Template, "{") {
			key := candidate.Template[1:strings.Index(candidate.Template, "}")]
			child, _ := e.Args[key].(*Expression)
			if child != nil && !isSafeLeadingOperand(g, child) {
				continue
			}
		}
		matches := true
		for _, key := range candidate.Keys {
			if !present[key] {
				matches = false
				break
			}
		}
		// A value the template does not SHOW is a condition on it: LTRIM and
		// RTRIM are the same class and the same argument keys, and only the
		// position tells them apart.
		for _, req := range candidate.Required {
			// Not a string comparison: a requirement can be a FLAG now, and
			// a bool compared as a string never matched -- which refused
			// every JSON_OBJECT, whose node always carries two of them.
			if !g.sameConst(e.Args[req.Key], req.Value) {
				matches = false
				break
			}
		}
		if !matches {
			continue
		}
		out := candidate.Template
		for _, key := range candidate.Marked {
			marker := "{" + key + "}"
			// A marker the template QUOTES wants the argument's name, not its
			// rendering: `DATE_TRUNC('{unit}', x)` around a string literal
			// would otherwise write ''ISOWEEK''.
			if quoted := strings.Contains(out, "'"+marker+"'"); quoted {
				if child, _ := e.Args[key].(*Expression); child != nil {
					out = strings.ReplaceAll(out, marker,
						escapeStringBody(child.Name(), g.cfg.StringEscapes))
					continue
				}
			}
			var text string
			switch v := e.Args[key].(type) {
			case *Expression:
				text = g.node(v)
			case []*Expression:
				parts := make([]string, 0, len(v))
				for _, item := range v {
					parts = append(parts, g.node(item))
				}
				text = strings.Join(parts, ", ")
			case string:
				text = v
			}
			out = strings.ReplaceAll(out, "{"+key+"}", text)
		}
		return out, true
	}
	return "", false
}

// writeTableAlias writes the alias and, when it has one, the column list that
// names what it aliases: `x AS y(a, b)`.
func (g *generator) writeTableAlias(e *Expression) string {
	out := g.child(e, "this")
	columns, _ := e.Args["columns"].([]*Expression)
	if len(columns) == 0 {
		return out
	}
	rendered := make([]string, 0, len(columns))
	for _, c := range columns {
		rendered = append(rendered, g.node(c))
	}
	return out + "(" + strings.Join(rendered, ", ") + ")"
}

func (g *generator) writeStruct(e *Expression) string { return "{" + g.list(e) + "}" }

// writePropertyEQ writes a struct entry. The key is an Identifier in the tree
// and a quoted string on the page.
func (g *generator) writePropertyEQ(e *Expression) string {
	key, _ := e.Args["this"].(*Expression)
	name := ""
	if key != nil {
		name, _ = key.Args["this"].(string)
	}
	return "'" + escapeStringBody(name, g.cfg.StringEscapes) + "': " + g.child(e, "expression")
}

func (g *generator) writeFilter(e *Expression) string {
	return g.child(e, "this") + " FILTER(" + g.child(e, "expression") + ")"
}

// writeJSONPath assembles a path from the pieces the dialect uses. DuckDB
// quotes the whole thing and keeps the $ root; Databricks does neither, and
// the two spell a key needing quotes differently.
func (g *generator) writeJSONPath(e *Expression) string {
	parts, _ := e.Args["expressions"].([]*Expression)
	// A path does not render the same wherever it appears: Databricks writes
	// the same one as `c1:item[1].price` under a JSONExtract and as
	// `'$.item[1].price'` under a JSONExtractScalar. The owner is what says
	// which, and only the separators change.
	pieces := g.tables.JSONPath
	// A quote in a key is doubled only where the path is written INSIDE a
	// string. Databricks' colon form is bare SQL, so `col:["fr'uit"]` keeps
	// its quote; an empty escape set is not the same thing, since that still
	// means "double it".
	escapeQuote := true
	if over, ok := g.tables.JSONPathByClass[g.pathOwner]; ok {
		pieces.Key, pieces.Subscript, pieces.QuotedKey = over.Key, over.Subscript, over.QuotedKey
		pieces.KeyAfter = over.KeyAfter
		escapeQuote = over.EscapesQuote
	}
	keys := 0
	out := pieces.Open
	for _, part := range parts {
		switch part.Class {
		case "JSONPathRoot":
			// The root is already in Open.
		case "JSONPathKey", "JSONPathSubscript":
			// A WILDCARD stands where a name or an index would: `$.y.*` and
			// `$.y[*]`. It carries no text of its own, so it is written by
			// putting the star in the slot rather than by escaping anything.
			if inner, ok := part.Args["this"].(*Expression); ok && inner != nil {
				if inner.Class != "JSONPathWildcard" {
					return g.fail(inner.Class + " in a JSON path")
				}
				form := pieces.Key
				if part.Class == "JSONPathSubscript" {
					form = pieces.Subscript
				}
				form = strings.ReplaceAll(form, "{key}", "*")
				out += strings.ReplaceAll(form, "{index}", "*")
				continue
			}
			if part.Class == "JSONPathSubscript" {
				n, _ := part.Args["this"].(int)
				out += strings.ReplaceAll(pieces.Subscript,
					"{index}", strconv.Itoa(n))
				continue
			}
			// The whole path is written INSIDE a string literal, so a quote
			// in a key has to be escaped for that literal or the statement
			// ends early. `j -> '"a''b"'` came out with an unterminated
			// string, which the generator fuzzer found by feeding a path made
			// of quote characters.
			name, _ := part.Args["this"].(string)
			// The separator before the FIRST key is not always the one before
			// the rest: Databricks writes `c1:item.price`, a colon and then a
			// dot. Probed from one key alone, every later separator was
			// missing and `c1:item[1].price` came out `c1:item[1]price`.
			form := pieces.Key
			if keys > 0 && pieces.KeyAfter != "" {
				form = pieces.KeyAfter
			}
			keys++
			// A key the SOURCE quoted stays quoted, whatever it looks like:
			// `raw:store['bicycle']` is written `raw:store["bicycle"]`, not
			// `raw:store.bicycle`. The colon form records that on the segment
			// where the arrow form does not, so a bare-looking name is only
			// bare when nothing says otherwise.
			quoted, hasFlag := part.Args["quoted"].(bool)
			if (hasFlag && quoted) || !isBareIdentifier(name) {
				form = pieces.QuotedKey
			}
			body := name
			if escapeQuote {
				body = escapeStringBody(name, g.cfg.StringEscapes)
			}
			segment := strings.ReplaceAll(form, "{key}", body)
			// A segment that renders to NOTHING cannot be read back.
			// Databricks spells a key as bare text with no delimiter, so a key
			// with no name leaves `0:` -- which the reference accepts and this
			// port does not. The test is on what was WRITTEN, not on the name:
			// where the form carries a separator, an empty key survives it.
			//
			// Found by the generator fuzzer on `0->''`.
			if segment == "" {
				return g.fail("a JSON path key that writes nothing")
			}
			out += segment
		default:
			return g.fail(part.Class)
		}
	}
	// A path that writes NOTHING cannot be read back. Databricks spells the
	// root as the empty string and a key as bare text, so `0 -> ''` -- whose
	// path is a bare root -- comes out `0:`, which the reference accepts and
	// this port does not. Dialects that spell the root `$` are unaffected,
	// which is why the test is on what was written rather than on the parts.
	//
	// Found by the generator fuzzer.
	if out+pieces.Close == "" {
		return g.fail("a JSON path that writes nothing")
	}
	return out + pieces.Close
}

// writeJSONExtractOp writes `->` and `->>` the way the dialect writes them,
// where it writes them as an operator at all. PostgreSQL explodes the path
// into one argument per part and T-SQL duplicates the whole expression into
// ISNULL(JSON_QUERY, JSON_VALUE); neither has a template, so both refuse.
//
// The left side must be an ATOM. The reference parenthesises by precedence and
// a template substitutes text knowing none, so anything that could need
// brackets is refused rather than written flat.
func (g *generator) writeJSONExtractOp(e *Expression) string {
	// A dialect with no single path literal writes one operator -- or one
	// argument -- PER PART, so the node has to be folded rather than spelled.
	if per, ok := g.tables.JSONPerPartSQL[e.Class]; ok {
		return g.writeJSONPerPart(e, per)
	}
	form, ok := g.tables.JSONExtractSQL[e.Class]
	if !ok {
		return g.fail(e.Class)
	}
	// A class whose path is written with its own separators brings its own
	// call too: the two were measured from one rendering, and mixing a form
	// probed against the standalone pieces with corrected pieces writes the
	// dot after the root twice.
	if over, ok := g.tables.JSONPathByClass[e.Class]; ok && over.Form != "" {
		form = over.Form
	}
	this, _ := e.Args["this"].(*Expression)
	if !isAtomForOperator(this) {
		return g.fail(e.Class + " over a compound expression")
	}
	path, _ := e.Args["expression"].(*Expression)
	// Anything that is not a PATH is written where the path would go, exactly
	// as it was read. A path the reference could not read stays the string it
	// was written as -- `0 -> '[""@""]'` -- and so does an operand that was
	// never a path to begin with: `'{"x": 1}' -> c` keeps the column. The
	// operand has to be an atom, because the form around it is an operator
	// and a template substitutes text knowing no precedence.
	if path != nil && path.Class != "JSONPath" {
		if !isAtomForOperator(path) {
			return g.fail(e.Class + " over a compound path")
		}
		// A call whose path form QUOTES its argument needs the other form
		// here, or a column comes out as the string `'$path_col'`.
		if over, ok := g.tables.JSONPathByClass[e.Class]; ok {
			if over.PlainForm == "" {
				return g.fail(e.Class + " over a path it cannot write plainly")
			}
			form = over.PlainForm
		}
		// The RIGHT operand is parenthesised where the left is not: the
		// reference writes `a -> (b * c)` but `POWER(0, 0) -> '$'`. That is
		// what left-associativity looks like written down -- the left side
		// cannot re-associate and the right side can.
		text := g.node(path)
		if isA("Binary", path) {
			text = "(" + text + ")"
		}
		out := strings.ReplaceAll(form, "{this}", g.node(this))
		return strings.ReplaceAll(out, "{path}", text)
	}
	if path == nil || path.Class != "JSONPath" {
		return g.fail(e.Class + " without a path")
	}
	out := strings.ReplaceAll(form, "{this}", g.node(this))
	// The path's separators depend on the call it lands in, so the owner is
	// set while it renders and restored after -- a chain writes one extraction
	// inside another and the inner one must not keep the outer one's spelling.
	saved := g.pathOwner
	g.pathOwner = e.Class
	text := g.node(path)
	g.pathOwner = saved
	return strings.ReplaceAll(out, "{path}", text)
}

// isAtomForOperator reports whether a node needs no parentheses beside an
// operator, whatever the precedence table says.
//
// A JSON extract counts: `j -> 'a' -> 'b'` nests to the left and the reference
// writes it flat, so refusing a chain because its left side is not a leaf
// turned away the very form this writer exists to produce.
func isAtomForOperator(e *Expression) bool {
	if e == nil {
		return false
	}
	switch e.Class {
	case "Column", "Literal", "Identifier", "Paren", "Star", "Null", "Boolean",
		"JSONExtract", "JSONExtractScalar":
		return true
	}
	// Anything that binds TIGHTER than the arrow needs no parentheses in front
	// of it: `POWER(0, 0) -> '$'` is what the reference writes for `0^0->''`.
	// This list used to be the safe one for the arrow at the tightest level of
	// all; now that it sits where the reference puts it -- looser than
	// arithmetic, a cast or a unary minus -- those operands are atoms too.
	//
	// What is still refused is an operand that binds LOOSER: a comparison, an
	// IS, a connector. Those can only get here already parenthesised, and a
	// Paren is an atom above.
	if isA("Binary", e) && !isA("Connector", e) && !isA("Predicate", e) {
		return true
	}
	switch e.Class {
	case "Cast", "TryCast", "Neg", "BitwiseNot", "Anonymous", "Bracket":
		return true
	}
	return isA("Unary", e) && e.Class != "Not"
}

// writeUnnest writes the call and then its alias. The function spelling comes
// from the dialect's own table -- Databricks says EXPLODE -- but the alias sits
// OUTSIDE the call there and inside it in Databricks, so a dialect that puts it
// inside is refused rather than written with it in the wrong place.
func (g *generator) writeUnnest(e *Expression) string {
	spec, ok := g.functionSpelling(e)
	if !ok {
		// A writer registered by class SHADOWS the template fallback, so a
		// dialect whose Unnest spelling only exists as a template was refused
		// by this function before the template was ever consulted.
		if out, ok := g.syntaxTemplate(e); ok {
			return out
		}
		return g.fail("Unnest")
	}
	call := g.namedFunction(e, spec)
	alias, _ := e.Args["alias"].(*Expression)
	if alias == nil {
		return call
	}
	if spec.Name != "UNNEST" {
		return g.fail("Unnest with an alias in " + g.dialect)
	}
	return call + " AS " + g.node(alias)
}

func (g *generator) writeAtTimeZone(e *Expression) string {
	return g.child(e, "this") + " AT TIME ZONE " + g.child(e, "zone")
}

// isSafeLeadingOperand reports whether a node can be written at the start of a
// template with nothing in front of it. An operator node cannot: the reference
// brackets it by precedence and a template cannot.
func isSafeLeadingOperand(g *generator, e *Expression) bool {
	if _, isBinary := g.tables.BinarySQL[e.Class]; isBinary {
		return false
	}
	if _, isUnary := g.tables.UnarySQL[e.Class]; isUnary {
		return false
	}
	return true
}

// writeJSONPerPart folds a path into one operator per part -- `j -> 'a' -> 'b'`
// -- or into one argument per part when the node is not restricted to JSON
// types: `JSON_EXTRACT_PATH(j, 'a', 'b')`. Which of the two is decided by the
// same flag the parser read off the reference.
func (g *generator) writeJSONPerPart(e *Expression, per JSONPerPart) string {
	this, _ := e.Args["this"].(*Expression)
	if !isAtomForOperator(this) {
		return g.fail(e.Class + " over a compound expression")
	}
	path, _ := e.Args["expression"].(*Expression)
	if path == nil || path.Class != "JSONPath" {
		// There are no PARTS to fold when the operand was never a path. This
		// form exists to spread one across several operators or arguments,
		// and it has nothing to say about a column.
		return g.fail(e.Class + " without a path")
	}
	parts, _ := path.Args["expressions"].([]*Expression)
	names := make([]string, 0, len(parts))
	for _, part := range parts {
		switch part.Class {
		case "JSONPathRoot":
			// The root is not a part here: it names the value being indexed,
			// which is already `this`.
		case "JSONPathKey":
			name, _ := part.Args["this"].(string)
			escaped := escapeStringBody(name, g.cfg.StringEscapes)
			// A key holding a QUOTE is refused here rather than written. The
			// reference does not escape it in this form -- it writes
			// `JSON_EXTRACT_PATH(col, 'fr'uit')` and then cannot read that
			// back, which is upstream sqlglot bug (8) -- so the two choices
			// were to reproduce SQL that does not tokenize or to differ from
			// the reference. Refusing is neither.
			if escaped != name {
				return g.fail(e.Class + " over a key holding a quote")
			}
			names = append(names, escaped)
		default:
			return g.fail(e.Class + " over a " + part.Class)
		}
	}
	if len(names) == 0 {
		return g.fail(e.Class + " with an empty path")
	}
	onlyJSON, _ := e.Args["only_json_types"].(bool)
	if !onlyJSON {
		// The function form takes every part as an argument.
		out := strings.ReplaceAll(per.Call, "{this}", g.node(this))
		head, tail, found := strings.Cut(out, "'{part}'")
		if !found {
			return g.fail(e.Class)
		}
		return head + "'" + strings.Join(names, "', '") + "'" + tail
	}
	out := g.node(this)
	for _, name := range names {
		// An empty key VANISHES in the chain form, which spells a key as bare
		// text: Databricks writes `0:` for a key with no name, and this port
		// cannot read that back. The function form above quotes each key, so
		// an empty one survives there and only the chain needs the guard.
		//
		// The reference writes `0:` too and does read it back. This is the
		// port being narrower, not the reference being wrong -- so it refuses
		// rather than emitting what it cannot re-read. Found by the generator
		// fuzzer on `0->''`.
		if name == "" {
			return g.fail(e.Class + " over a key with no name")
		}
		step := strings.ReplaceAll(per.Chain, "{this}", out)
		out = strings.ReplaceAll(step, "{part}", name)
	}
	return out
}

// templateName is the function name a syntax template writes, or "" where the
// template does not begin with one.
func templateName(template string) string {
	open := strings.Index(template, "(")
	if open <= 0 {
		return ""
	}
	name := template[:open]
	if !isBareIdentifier(name) {
		return ""
	}
	return strings.ToUpper(name)
}

// writeJSONKeyValue writes one pair inside JSON_OBJECT. The separator is per
// dialect -- DuckDB uses the comma that also separates the pairs, the others a
// colon -- and it cannot live in the template table, which rejects a
// marker-operator-marker form as an infix operator whose precedence a template
// cannot know. A pair is only ever written inside its own parentheses, so
// there is no precedence for it to get wrong.
func (g *generator) writeJSONKeyValue(e *Expression) string {
	form := g.tables.JSONKeyValueSQL
	if form == "" {
		return g.fail(e.Class)
	}
	out := strings.ReplaceAll(form, "{this}", g.child(e, "this"))
	return strings.ReplaceAll(out, "{expression}", g.child(e, "expression"))
}

// writePivot writes the postfix `PIVOT(<aggregates> FOR <field> IN (<values>))`
// that hangs off a table, and the UNPIVOT that mirrors it.
//
// Written here rather than as a template because the template machinery
// records one spelling per SET of present arguments, and a pivot carries four
// dialect conventions that are on the node without ever being written -- so
// every combination of them would be a separate template of the same shape.
//
// The space in front is the separator, the way the reference returns it.
func (g *generator) writePivot(e *Expression) string {
	// The STATEMENT form is a different shape -- `PIVOT t ON x USING SUM(y)`,
	// with the source under `this` -- and the template table already spells
	// it. Only the postfix form, which hangs off a table and has no source of
	// its own, is written here.
	if source, _ := e.Args["this"].(*Expression); source != nil {
		if out, ok := g.syntaxTemplate(e); ok {
			return out
		}
		return g.fail(e.Class + " as a statement")
	}
	word := "PIVOT"
	if unpivot, _ := e.Args["unpivot"].(bool); unpivot {
		word = "UNPIVOT"
	}
	aggregates, _ := e.Args["expressions"].([]*Expression)
	fields, _ := e.Args["fields"].([]*Expression)
	if len(aggregates) == 0 || len(fields) != 1 {
		return g.fail(e.Class + " without an aggregate and one field")
	}
	parts := make([]string, 0, len(aggregates))
	for _, agg := range aggregates {
		parts = append(parts, g.node(agg))
	}
	// The NULLS clause brings a space before the parenthesis with it, where a
	// bare PIVOT has none: `UNPIVOT INCLUDE NULLS (...)` but `UNPIVOT(...)`.
	if include, ok := e.Args["include_nulls"].(bool); ok {
		if include {
			word += " INCLUDE NULLS "
		} else {
			word += " EXCLUDE NULLS "
		}
	}
	out := " " + word + "(" + strings.Join(parts, ", ") + " FOR " + g.node(fields[0]) + ")"
	if alias := g.child(e, "alias"); alias != "" {
		out += " AS " + alias
	}
	return out
}

// writeVersion writes FOR SYSTEM_TIME. The template table holds the shape, but
// a RANGE cannot go through it: the two bounds are held as a Tuple, which
// would render `(c, d)`, and the dialect writes `c TO d` or `c AND d`
// depending on the kind. The separating word is probed per kind.
func (g *generator) writeVersion(e *Expression) string {
	kind, _ := e.Args["kind"].(string)
	rendered := ""
	if expr, _ := e.Args["expression"].(*Expression); expr != nil {
		sep := g.tables.VersionRangeSep[kind]
		items, _ := expr.Args["expressions"].([]*Expression)
		if sep != "" && expr.Class == "Tuple" && len(items) == 2 {
			rendered = g.node(items[0]) + " " + sep + " " + g.node(items[1])
		} else {
			rendered = g.node(expr)
		}
	}
	for _, candidate := range g.tables.SyntaxSQL[e.Class] {
		wantsExpression := strings.Contains(candidate.Template, "{expression}")
		if wantsExpression != (rendered != "") {
			continue
		}
		out := strings.ReplaceAll(candidate.Template, "{kind}", kind)
		return strings.ReplaceAll(out, "{expression}", rendered)
	}
	return g.fail(e.Class)
}

// writeForClause writes `FOR XML ...`, `FOR JSON ...` and the bare FOR BROWSE.
func (g *generator) writeForClause(e *Expression) string {
	kind, _ := e.Args["kind"].(string)
	options, _ := e.Args["expressions"].([]*Expression)
	if len(options) == 0 {
		return "FOR " + kind
	}
	parts := make([]string, 0, len(options))
	for _, option := range options {
		parts = append(parts, g.node(option))
	}
	return "FOR " + kind + " " + strings.Join(parts, ", ")
}

// A QueryOption is a wrapper and writes whatever it holds.
func (g *generator) writeQueryOption(e *Expression) string { return g.child(e, "this") }

// An option written as a WORD, or a word with a parenthesised string after it.
func (g *generator) writeXMLKeyValueOption(e *Expression) string {
	out := g.child(e, "this")
	if expression := g.child(e, "expression"); expression != "" {
		out += "(" + expression + ")"
	}
	return out
}

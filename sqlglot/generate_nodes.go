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
		"Select":                              (*generator).writeSelect,
		"Union":                               (*generator).writeSetOperation,
		"Except":                              (*generator).writeSetOperation,
		"Intersect":                           (*generator).writeSetOperation,
		"With":                                (*generator).writeWith,
		"CTE":                                 (*generator).writeCTE,
		"TableAlias":                          (*generator).writeTableAlias,
		"From":                                (*generator).writeFrom,
		"Table":                               (*generator).writeTable,
		"Join":                                (*generator).writeJoin,
		"Lateral":                             (*generator).writeLateral,
		"Subquery":                            (*generator).writeSubquery,
		"Where":                               (*generator).writeWhere,
		"Group":                               (*generator).writeGroup,
		"Having":                              (*generator).writeHaving,
		"Order":                               (*generator).writeOrder,
		"Ordered":                             (*generator).writeOrdered,
		"Limit":                               (*generator).writeLimit,
		"Offset":                              (*generator).writeOffset,
		"Into":                                (*generator).writeInto,
		"Star":                                (*generator).writeStar,
		"Column":                              (*generator).writeColumn,
		"Identifier":                          (*generator).writeIdentifier,
		"Literal":                             (*generator).writeLiteral,
		"National":                            (*generator).writeNational,
		"RawString":                           (*generator).writeQuotedString,
		"ByteString":                          (*generator).writeQuotedString,
		"UnicodeString":                       (*generator).writeQuotedString,
		"HexString":                           (*generator).writeQuotedString,
		"BitString":                           (*generator).writeQuotedString,
		"Boolean":                             (*generator).writeBoolean,
		"Null":                                (*generator).writeNull,
		"Alias":                               (*generator).writeAlias,
		"Paren":                               (*generator).writeParen,
		"Case":                                (*generator).writeCase,
		"If":                                  (*generator).writeIf,
		"Cast":                                (*generator).writeCast,
		"TryCast":                             (*generator).writeCast,
		"DataType":                            (*generator).writeDataType,
		"DataTypeParam":                       (*generator).writeChildThis,
		"Placeholder":                         (*generator).writePlaceholder,
		"Escape":                              (*generator).writeEscape,
		"Parameter":                           (*generator).writeParameter,
		"ColumnDef":                           (*generator).writeColumnDef,
		"Create":                              (*generator).writeCreate,
		"Alter":                               (*generator).writeAlter,
		"AlterRename":                         (*generator).writeAlterRename,
		"RenameColumn":                        (*generator).writeRenameColumn,
		"AddConstraint":                       (*generator).writeAddConstraint,
		"AlterColumn":                         (*generator).writeAlterColumn,
		"ColumnConstraint":                    (*generator).writeColumnConstraint,
		"Reference":                           (*generator).writeReference,
		"Index":                               (*generator).writeIndex,
		"ColumnPosition":                      (*generator).writeColumnPosition,
		"WithDataProperty":                    (*generator).writeWithDataProperty,
		"AlterSet":                            (*generator).writeAlterSet,
		"Partition":                           (*generator).writePartition,
		"OnConflict":                          (*generator).writeOnConflict,
		"Pragma":                              (*generator).writePragma,
		"Comment":                             (*generator).writeComment,
		"Grant":                               (*generator).writeGrant,
		"Revoke":                              (*generator).writeGrant,
		"GrantPrivilege":                      (*generator).writeGrantPrivilege,
		"GrantPrincipal":                      (*generator).writeGrantPrincipal,
		"TruncateTable":                       (*generator).writeTruncate,
		"Use":                                 (*generator).writeUse,
		"Attach":                              (*generator).writeAttach,
		"Detach":                              (*generator).writeDetach,
		"AttachOption":                        (*generator).writeAttachOption,
		"Install":                             (*generator).writeInstall,
		"Command":                             (*generator).writeCommand,
		"Cache":                               (*generator).writeCache,
		"Uncache":                             (*generator).writeUncache,
		"Describe":                            (*generator).writeDescribe,
		"Analyze":                             (*generator).writeAnalyze,
		"AnalyzeStatistics":                   (*generator).writeAnalyzeStatistics,
		"Transaction":                         (*generator).writeTransaction,
		"Commit":                              (*generator).writeTransaction,
		"Rollback":                            (*generator).writeTransaction,
		"WithTableHint":                       (*generator).writeTableHint,
		"UserDefinedFunction":                 (*generator).writeUserDefinedFunction,
		"Return":                              (*generator).writeReturn,
		"ReturnsProperty":                     (*generator).writeReturnsProperty,
		"LanguageProperty":                    (*generator).writeLanguageProperty,
		"StabilityProperty":                   (*generator).writeStabilityProperty,
		"StrictProperty":                      (*generator).writeStrictProperty,
		"CalledOnNullInputProperty":           (*generator).writeCalledOnNullInputProperty,
		"SqlReadWriteProperty":                (*generator).writeSqlReadWriteProperty,
		"SetConfigProperty":                   (*generator).writeSetConfigProperty,
		"Heredoc":                             (*generator).writeHeredoc,
		"Set":                                 (*generator).writeSet,
		"SetItem":                             (*generator).writeSetItem,
		"InOutColumnConstraint":               (*generator).writeInOutConstraint,
		"PrimaryKey":                          (*generator).writePrimaryKey,
		"ForeignKey":                          (*generator).writeForeignKey,
		"Constraint":                          (*generator).writeConstraint,
		"CheckColumnConstraint":               (*generator).writeCheckConstraint,
		"ComputedColumnConstraint":            (*generator).writeComputedConstraint,
		"GeneratedAsIdentityColumnConstraint": (*generator).writeGeneratedAsIdentity,
		"NotNullColumnConstraint":             (*generator).writeNotNullConstraint,
		"DefaultColumnConstraint":             (*generator).writeDefaultConstraint,
		"PrimaryKeyColumnConstraint":          (*generator).writePrimaryKeyConstraint,
		"UniqueColumnConstraint":              (*generator).writeUniqueConstraint,
		"AutoIncrementColumnConstraint":       (*generator).writeAutoIncrementConstraint,
		"CommentColumnConstraint":             (*generator).writeCommentConstraint,
		"CollateColumnConstraint":             (*generator).writeCollateConstraint,
		"Insert":                              (*generator).writeInsert,
		"Update":                              (*generator).writeUpdate,
		"Delete":                              (*generator).writeDelete,
		"Merge":                               (*generator).writeMerge,
		"Whens":                               (*generator).writeWhens,
		"When":                                (*generator).writeWhen,
		"Returning":                           (*generator).writeReturning,
		"Values":                              (*generator).writeValues,
		"Drop":                                (*generator).writeDrop,
		"Schema":                              (*generator).writeSchema,
		"Anonymous":                           (*generator).writeAnonymous,
		"In":                                  (*generator).writeIn,
		"Between":                             (*generator).writeBetween,
		"Dot":                                 (*generator).writeDot,
		"Distinct":                            (*generator).writeDistinct,
		"Array":                               (*generator).writeArray,
		"Window":                              (*generator).writeWindow,
		"Interval":                            (*generator).writeInterval,
		"Lambda":                              (*generator).writeLambda,
		"Struct":                              (*generator).writeStruct,
		"Unnest":                              (*generator).writeUnnest,
		"AtTimeZone":                          (*generator).writeAtTimeZone,
		"JSONPath":                            (*generator).writeJSONPath,
		"JSONKeyValue":                        (*generator).writeJSONKeyValue,
		"Pivot":                               (*generator).writePivot,
		"Version":                             (*generator).writeVersion,
		"Tuple":                               (*generator).writeTuple,
		"ToMap":                               (*generator).writeToMap,
		"GroupConcat":                         (*generator).writeGroupConcat,
		"GroupingSets":                        (*generator).writeGrouping,
		"Cube":                                (*generator).writeGrouping,
		"Rollup":                              (*generator).writeGrouping,
		"ForClause":                           (*generator).writeForClause,
		"QueryOption":                         (*generator).writeQueryOption,
		"XMLKeyValueOption":                   (*generator).writeXMLKeyValueOption,
		"JSONExtract":                         (*generator).writeJSONExtractOp,
		"JSONExtractScalar":                   (*generator).writeJSONExtractOp,
		"PropertyEQ":                          (*generator).writePropertyEQ,
		"Filter":                              (*generator).writeFilter,
		"Extract":                             (*generator).writeSyntaxFunction,
		"Trim":                                (*generator).writeSyntaxFunction,
		"Substring":                           (*generator).writeSyntaxFunction,
		"StrPosition":                         (*generator).writeSyntaxFunction,
		"IntervalSpan":                        (*generator).writeIntervalSpan,
		"Var":                                 (*generator).writeVar,
		"WindowSpec":                          (*generator).writeWindowSpec,
		"Bracket":                             (*generator).writeBracket,
		"Slice":                               (*generator).writeSlice,
		"All":                                 (*generator).writeQuantifier,
		"Any":                                 (*generator).writeQuantifier,
		"Like":                                (*generator).writeLike,
		"ILike":                               (*generator).writeLike,
		"Is":                                  (*generator).writeIs,
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
	// A FETCH lands in the same slot as a LIMIT but is not one: T-SQL writes
	// a limit as TOP and a fetch where it stands, and writing the fetch as
	// TOP left the count behind -- `SELECT TOP  *`.
	if limit != nil && limit.Class == "Limit" && g.tables.LimitIsTop {
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
	// A FETCH is written here whatever the dialect does with a LIMIT: T-SQL
	// writes a limit as TOP and has no other place to put it, but it writes a
	// fetch at the end like everyone else.
	if limit != nil && limit.Class == "Fetch" {
		add(g.node(limit))
	}
	// A sample hanging off the QUERY rather than a table is the same node
	// under a different word: DuckDB says USING SAMPLE here and TABLESAMPLE
	// there. Both words are probed, and the template carries the other one.
	if sample := g.child(e, "sample"); sample != "" {
		if g.tables.SelectSampleWord != "" && g.tables.TableSampleWord != "" {
			sample = strings.Replace(sample,
				g.tables.TableSampleWord, g.tables.SelectSampleWord, 1)
		}
		add(sample)
	}
	// The WINDOW clause comes BEFORE the QUALIFY that refers to its names.
	windows, _ := e.Args["windows"].([]*Expression)
	if len(windows) > 0 {
		names := make([]string, 0, len(windows))
		for _, w := range windows {
			names = append(names, g.node(w))
		}
		add("WINDOW " + strings.Join(names, ", "))
	}
	add(g.child(e, "qualify"))
	// A LATERAL VIEW is a clause of the query, and there may be several.
	laterals, _ := e.Args["laterals"].([]*Expression)
	for _, lateral := range laterals {
		add(g.node(lateral))
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

// withPrefix puts a WITH clause in front of whatever it qualifies -- a query,
// or a statement that CHANGES something: `WITH a AS (...) UPDATE a SET c = 1`
// reads through the CTE and writes through it too.
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
	if recursive, _ := e.Args["recursive"].(bool); recursive {
		return "WITH RECURSIVE " + g.list(e)
	}
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
	// The partition names WHICH partition is written, and hangs off the table
	// because it is part of naming the target.
	if partition := g.child(e, "partition"); partition != "" {
		out += " " + partition
	}
	// The locking hints come after the alias too. Every dialect but T-SQL
	// DROPS them: a hint is advice about how to READ rather than part of what
	// is read, and losing one only ever makes the read stricter -- which is
	// why they are dropped here rather than refused.
	if hints, _ := e.Args["hints"].([]*Expression); len(hints) > 0 && g.tables.TableHintsWritten {
		for _, hint := range hints {
			out += " " + g.node(hint)
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
	// An APPLY writes its own words and is the whole join. A plain LATERAL is
	// not: it goes where a table goes, so the comma or the JOIN and its ON
	// are still the enclosing join's to write.
	if inner, _ := e.Args["this"].(*Expression); inner != nil && inner.Class == "Lateral" {
		if _, apply := inner.Args["cross_apply"].(bool); apply {
			return this
		}
	}
	words := []string{}
	for _, key := range []string{"side", "kind"} {
		if s, ok := e.Args[key].(string); ok && s != "" {
			words = append(words, s)
		}
	}
	usingColumns, _ := e.Args["using"].([]*Expression)
	if len(words) == 0 && len(usingColumns) == 0 {
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
	if len(usingColumns) > 0 {
		names := make([]string, 0, len(usingColumns))
		for _, column := range usingColumns {
			names = append(names, g.node(column))
		}
		words = append(words, "USING ("+strings.Join(names, ", ")+")")
	}
	return strings.Join(words, " ")
}

func (g *generator) writeLateral(e *Expression) string {
	// One node, three spellings, told apart by which of its arguments are
	// filled. APPLY sets cross_apply and leaves view off; LATERAL VIEW sets
	// view; a plain LATERAL sets neither.
	if _, apply := e.Args["cross_apply"].(bool); apply {
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
	if view, _ := e.Args["view"].(bool); view {
		out := "LATERAL VIEW "
		if outer, _ := e.Args["outer"].(bool); outer {
			out += "OUTER "
		}
		out += g.child(e, "this")
		// The alias is always PRESENT on a view and empty when the statement
		// named nothing. It is spelled here rather than by the table-alias
		// writer: a view names its columns `t AS a, b`, where a table would
		// have written `t(a, b)`.
		if alias, _ := e.Args["alias"].(*Expression); alias != nil {
			if name, _ := alias.Args["this"].(*Expression); name != nil {
				out += " " + g.node(name)
			}
			columns, _ := alias.Args["columns"].([]*Expression)
			if len(columns) > 0 {
				names := make([]string, 0, len(columns))
				for _, column := range columns {
					names = append(names, g.node(column))
				}
				out += " AS " + strings.Join(names, ", ")
			}
		}
		return out
	}
	out := "LATERAL " + g.child(e, "this")
	if alias := g.child(e, "alias"); alias != "" {
		out += " AS " + alias
	}
	return out
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
	out += g.joins(e)
	// A subquery takes the same modifiers a query does, and they go
	// OUTSIDE the parentheses: `(SELECT 1) ORDER BY x LIMIT 1` orders
	// the subquery, not the SELECT inside it, and the two are
	// different trees for what an engine runs the same way.
	for _, key := range []string{"order", "limit", "offset"} {
		if s := g.child(e, key); s != "" {
			out += " " + s
		}
	}
	return out
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
	// The plain columns first, then each grouping in the order the reference
	// writes them, whatever order they were written in.
	parts := []string{}
	if plain := g.list(e); plain != "" {
		parts = append(parts, plain)
	}
	for _, key := range []string{"grouping_sets", "cube", "rollup"} {
		items, _ := e.Args[key].([]*Expression)
		for _, item := range items {
			parts = append(parts, g.node(item))
		}
	}
	if len(parts) == 0 {
		return g.fail("GROUP BY with nothing to group by")
	}
	return " GROUP BY " + strings.Join(parts, ", ")
}

// writeStar writes `*` and what may follow it: EXCEPT drops columns, REPLACE
// swaps them. Both are lists on the Star itself.
func (g *generator) writeStar(e *Expression) string {
	out := "*"
	for _, part := range []struct{ key, word string }{
		{"except_", "EXCEPT"}, {"replace", "REPLACE"}, {"rename", "RENAME"},
	} {
		items, _ := e.Args[part.key].([]*Expression)
		if len(items) == 0 {
			continue
		}
		names := make([]string, 0, len(items))
		for _, item := range items {
			names = append(names, g.node(item))
		}
		out += " " + part.word + " (" + strings.Join(names, ", ") + ")"
	}
	return out
}

// writeToMap writes DuckDB's map literal: the word, then the struct it holds
// written with braces rather than as a struct.
func (g *generator) writeToMap(e *Expression) string {
	inner, _ := e.Args["this"].(*Expression)
	if inner == nil || inner.Class != "Struct" {
		return g.fail(e.Class)
	}
	items, _ := inner.Args["expressions"].([]*Expression)
	parts := make([]string, 0, len(items))
	for _, item := range items {
		// The entries are spelled here rather than by the PropertyEQ writer,
		// which names a struct FIELD and would quote a numeric key into a
		// string and lose an array one entirely.
		key, _ := item.Args["this"].(*Expression)
		value, _ := item.Args["expression"].(*Expression)
		if key == nil || value == nil {
			return g.fail(e.Class + " with an entry that is not a pair")
		}
		parts = append(parts, g.node(key)+": "+g.node(value))
	}
	return "MAP {" + strings.Join(parts, ", ") + "}"
}

// writeTuple writes a row. The template table has one for a tuple that HOLDS
// something, and the empty `()` -- the grouping that groups everything -- has
// no members for a template keyed on them to match.
func (g *generator) writeTuple(e *Expression) string {
	return "(" + g.list(e) + ")"
}

// A grouping writes its name and its members: `CUBE (a, b)`, and the empty
// `GROUPING SETS ()` writes its parentheses with nothing between them.
func (g *generator) writeGrouping(e *Expression) string {
	// Registered for these three classes and no others, so there is no fourth
	// case to fall through to.
	word := map[string]string{
		"GroupingSets": "GROUPING SETS", "Cube": "CUBE", "Rollup": "ROLLUP",
	}[e.Class]
	return word + " (" + g.list(e) + ")"
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
	// T-SQL requires parentheses around a TOP that is not a plain NUMBER, and
	// the reference writes them: `SELECT TOP (A) 0`, `SELECT TOP ('') 0`.
	// Without them the output is not valid T-SQL and neither the reference nor
	// this port can read it back -- a generator emitting SQL nobody can parse
	// is worse than one that declines. Both halves of this were found by
	// fuzzing the generator's output back through the parser: first a column
	// count, then a STRING one, which is a Literal like a number is and is not
	// a number.
	//
	// A NEGATIVE count is parenthesised here and is not by the reference,
	// which writes `SELECT TOP -1 x` and then cannot read it back. That is the
	// one place this writer does not follow it; see docs/upstream-issues.md.
	if inner, _ := e.Args["expression"].(*Expression); inner != nil &&
		word == "TOP " && !isPlainNumber(inner) {
		count = "(" + count + ")"
	}
	out := word + count
	if opts, _ := e.Args["limit_options"].(*Expression); opts != nil && opts.Args["percent"] == true {
		out += " PERCENT"
	}
	return out
}

func (g *generator) writeOffset(e *Expression) string {
	out := "OFFSET " + g.child(e, "expression")
	// T-SQL says ROWS after the count; nobody else does, and the word is on
	// no node -- the two spellings are the same tree.
	if word := g.tables.OffsetRowsWord; word != "" {
		out += " " + word
	}
	return out
}

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
		if key == "table" && g.mergeTarget[e] {
			// This column is assigned by a MERGE branch in a dialect that
			// writes no qualifier there. The tree keeps it; the spelling
			// does not.
			continue
		}
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
	if !quoted && !readableAsABareName(name) {
		// A name the tokenizer would not give back. The port builds one only
		// from a token the tokenizer could not classify -- a stray backtick
		// among them -- and writing it bare produced SQL nothing can read.
		// The generator fuzzer found it.
		return g.fail(e.Class + " whose name is not a name")
	}
	// T-SQL's mark for a temporary table, which the parser took off the name
	// and recorded here. It goes INSIDE the quoting -- `[#foo]`, not `#[foo]`
	// -- because it is part of the name as the engine reads it.
	prefix := ""
	switch {
	case e.Args["global_"] == true:
		prefix = "##"
	case e.Args["temporary"] == true:
		prefix = "#"
	}
	if quoted {
		// The delimiters the dialect WRITES, which are not always the ones it
		// reads: T-SQL accepts "x" and writes [x]. A closing delimiter inside
		// the name is doubled, or the name would end early.
		open, close := g.tables.IdentifierStart, g.tables.IdentifierEnd
		return open + prefix + strings.ReplaceAll(name, close, close+close) + close
	}
	return prefix + name
}

func (g *generator) writeLiteral(e *Expression) string {
	text, _ := e.Args["this"].(string)
	if e.Args["is_string"] == true {
		return "'" + escapeStringBody(text, g.cfg.StringEscapes) + "'"
	}
	return text
}

// writeQuotedString writes one of the string classes the tokenizer tells
// apart. The spelling is the dialect's -- `0x1F`, `x'1F'`, `UNHEX('1F')` --
// and so is whether the body takes a string's own escaping, because a
// template substitutes text VERBATIM and a body holding a quote would end
// the string early otherwise.
func (g *generator) writeQuotedString(e *Expression) string {
	spec, ok := g.tables.StringClassSQL[e.Class]
	if !ok {
		// This dialect writes the value in a way that loses it -- the neutral
		// one writes a byte string as `''` -- so it is refused instead.
		return g.fail(e.Class + ", which this dialect writes in a way that loses it")
	}
	template, escapes, _ := strings.Cut(spec, "\t")
	body, _ := e.Args["this"].(string)
	switch escapes {
	case "byte":
		// A byte string goes further than a quoted one: a CONTROL character
		// is written back as the two characters that spell it, so the tab a
		// statement wrote as `\t` comes back as `\t` rather than as a tab.
		body = escapeControlCharacters(body)
		body = escapeStringBody(body, g.cfg.StringEscapes)
	case "true":
		body = escapeStringBody(body, g.cfg.StringEscapes)
	}
	out := strings.ReplaceAll(template, "{body}", body)
	// A unicode string may name the character its escapes start with.
	if escape := g.child(e, "escape"); escape != "" {
		out += " UESCAPE " + escape
	}
	return out
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
	// And a name that is not a NAME is refused for the same reason: the
	// spelling puts it back bare, and `:"//"` wrote `$//`, which the port
	// then could not read. Only the plain-string form is checked -- the
	// PostgreSQL spelling wraps the name and needs no rule.
	if _, plain := e.Args["this"].(string); plain && !readableAsABareName(name) {
		return g.fail(e.Class + " whose name is not a name")
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
	name := g.node(this)
	// The spelling puts the name back BARE, so a name that is not a name
	// makes SQL nothing can read -- `SET @<nul> = 0` written for PostgreSQL
	// is `SET $<nul> = 0`, where the dollar opens a quote that never closes.
	// The same rule turns a Placeholder away. The generator fuzzer found it.
	if !readableAsABareName(name) {
		return g.fail(e.Class + " whose name is not a name")
	}
	return strings.ReplaceAll(g.tables.Placeholder.Parameter, "{name}", name)
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
		// A type carrying PARAMETERS may take a different name from the bare
		// one: Databricks writes VARCHAR as STRING and VARCHAR(255) as itself.
		if sized, ok := g.tables.SizedTypeSQL[string(kind)]; ok {
			return sized + "(" + params + ")"
		}
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
	// A COLUMN of a table is `a STRING`; a FIELD of a struct is `a: STRING` in
	// Databricks. Same node, and the parent says which.
	sep := g.tables.CompositeType.StructFieldSep
	if g.inColumnList {
		sep = " "
	}
	leading, trailing := "", ""
	if items, _ := e.Args["constraints"].([]*Expression); len(items) > 0 {
		for _, item := range items {
			// A parameter's MODE is written in front of it in PostgreSQL and
			// after its type everywhere else. It is the only constraint that
			// moves, and it is the only one the list holds unwrapped.
			if item.Class == "InOutColumnConstraint" && g.tables.ParameterModePrefix {
				leading = g.node(item) + " "
				continue
			}
			trailing += " " + g.node(item)
		}
	}
	// The column's TYPE is not a column list, however deep the struct in it
	// goes: `a STRUCT<c: MAP<...>>` keeps the field separator inside.
	was := g.inColumnList
	g.inColumnList = false
	kind := g.child(e, "kind")
	g.inColumnList = was
	// Two constraints change the TYPE beside them. A computed column has no
	// declared type in T-SQL, and an identity column is widened to BIGINT in
	// Databricks, which supports no narrower one.
	switch {
	case !g.tables.ComputedKeepsType && hasConstraint(e, "ComputedColumnConstraint"):
		kind = ""
	case g.tables.IdentityWidensType &&
		hasConstraint(e, "GeneratedAsIdentityColumnConstraint") &&
		integerTypes[typeKind(kindOf(e))]:
		kind = "BIGINT"
	}
	// Where a newly added column goes comes after everything else it says.
	if position := g.child(e, "position"); position != "" {
		trailing += " " + position
	}
	if kind == "" {
		// A VIEW's column has no type -- it names a result the query already
		// produced -- so there is nothing for the separator to separate.
		return leading + g.child(e, "this") + trailing
	}
	return leading + g.child(e, "this") + sep + kind + trailing
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
	return name + "(" + g.callList(e) + ")"
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
	// A NAMED window -- one entry of a WINDOW clause -- is the same node with
	// the name where the call would be and no OVER. `over` says which.
	out := g.child(e, "this") + " OVER "
	if _, over := e.Args["over"].(string); !over {
		out = g.child(e, "this") + " AS "
	}
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

// writeStruct writes a struct literal. Its fields are FIELDS however deep
// inside a call the struct sits, so the named-argument spelling is put down
// here rather than carried in from above.
func (g *generator) writeStruct(e *Expression) string {
	was := g.inCallArgs
	g.inCallArgs = false
	out := "{" + g.list(e) + "}"
	g.inCallArgs = was
	return out
}

// writePropertyEQ writes a struct entry. The key is an Identifier in the tree
// and a quoted string on the page.
func (g *generator) writePropertyEQ(e *Expression) string {
	key, _ := e.Args["this"].(*Expression)
	name := ""
	if key != nil {
		name, _ = key.Args["this"].(string)
	}
	// The same node has two spellings, and the PARENT decides which. As a
	// field of a struct it is `'k': v`; as a named ARGUMENT of a call it is
	// `k := v`, the way it was written.
	if g.inCallArgs {
		return g.node(key) + " := " + g.child(e, "expression")
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

// writeGroupConcat writes the node that `... WITHIN GROUP (ORDER BY ...)`
// FOLDS into: a GroupConcat whose first argument is an ordering of what was
// there before.
//
// Where the ordering goes on the way back out is per dialect and probed --
// inside the first argument, or unfolded into a WITHIN GROUP again. A dialect
// that does neither puts it somewhere this port cannot spell (DuckDB and
// PostgreSQL attach it to the SEPARATOR), and is refused rather than written
// in the wrong place.
func (g *generator) writeGroupConcat(e *Expression) string {
	order, _ := e.Args["this"].(*Expression)
	// Nothing folded in, or a dialect that writes the ordering where it
	// already is: the ordinary spelling serves.
	if order == nil || order.Class != "Order" || g.tables.GroupConcatOrder == "inline" {
		return g.spell(e)
	}
	switch g.tables.GroupConcatOrder {
	case "after_separator":
		// PostgreSQL and DuckDB write the ordering INSIDE the call, after the
		// separator: `STRING_AGG(x, ',' ORDER BY y DESC)`. The reference
		// reaches the same place by rebuilding the argument text with the
		// ordering appended, so the surgery here is its surgery.
		out := g.spell(g.withoutFoldedOrder(e, order))
		if !strings.HasSuffix(out, ")") {
			return g.fail(e.Class + " whose spelling is not a call")
		}
		ordering := New("Order", Arg{"expressions", order.Args["expressions"]})
		return out[:len(out)-1] + " " + strings.TrimSpace(g.node(ordering)) + ")"
	case "within_group":
		// Written as it arrived: the call over the ordered argument alone,
		// and the ordering after it.
		out := g.spell(g.withoutFoldedOrder(e, order))
		ordering := New("Order", Arg{"expressions", order.Args["expressions"]})
		return out + " WITHIN GROUP (" + strings.TrimSpace(g.node(ordering)) + ")"
	}
	return g.fail(e.Class + " over an ordering this dialect writes elsewhere")
}

// writeCreate writes the statements that make things. The kind is a WORD on
// the node and the name is wrapped in a Schema when there are columns, so the
// shape of `this` is what says which spelling this is.
func (g *generator) writeCreate(e *Expression) string {
	kind, _ := e.Args["kind"].(string)
	out := g.withPrefix(e, "CREATE") + " "
	if replace, _ := e.Args["replace"].(bool); replace {
		out += "OR REPLACE "
	}
	if unique, _ := e.Args["unique"].(bool); unique {
		out += "UNIQUE "
	}
	if g.hasTemporaryProperty(e) {
		this, _ := e.Args["this"].(*Expression)
		switch {
		// T-SQL says it in the NAME rather than with the word, and where the
		// name already carries the mark there is nothing more to write: the
		// property and the mark are two records of one fact, and the writer
		// of the name has put it back already.
		case namesATemporaryTable(createdTable(this)):
		case !g.tables.TemporaryWritten[kind]:
			// Not every dialect has the modifier, and the ones that do not
			// say it some other way. Writing the object under a DIFFERENT
			// NAME is not a spelling difference, and Databricks writes a
			// temporary TABLE with a storage format it was never given --
			// both say something the statement did not.
			return g.fail(e.Class + " TEMPORARY " + kind + ", which this dialect writes another way")
		default:
			out += "TEMPORARY "
		}
	}
	out += kind + " "
	if concurrently, _ := e.Args["concurrently"].(bool); concurrently {
		// Only an index takes this, and it goes after the kind rather than
		// before it.
		out += "CONCURRENTLY "
	}
	if exists, _ := e.Args["exists"].(bool); exists {
		// T-SQL drops these words from a VIEW and turns a TABLE into a
		// conditional EXEC. Writing the statement without them makes it do
		// something it was told not to.
		if !g.tables.CreateExistsWritten[kind] {
			return g.fail(e.Class + " IF NOT EXISTS, which this dialect writes another way")
		}
		out += "IF NOT EXISTS "
	}
	if kind == "FUNCTION" {
		return g.writeFunctionRest(e, out)
	}
	if kind == "VIEW" && !g.tables.ViewColumnCommentWritten && g.viewColumnHasComment(e) {
		// The comment says what the column is FOR, and this dialect writes
		// the name alone.
		return g.fail(e.Class + " VIEW whose column comments this dialect drops")
	}
	if expr, _ := e.Args["expression"].(*Expression); expr != nil && g.tables.RewritesCreateAsSelect {
		// This dialect has no such statement and the reference turns it into
		// another one -- `SELECT * INTO x FROM (...)`. That is a rewrite, not
		// a spelling, and writing the statement as itself would be SQL the
		// engine cannot run.
		return g.fail(e.Class + " as a query, which this dialect writes another way")
	}
	// T-SQL writes only two parts of a three-part name, dropping the catalog
	// -- which names a different object. The same rule turns a DROP away, and
	// it belongs to the dialect's naming rather than to either statement.
	if g.tables.TruncatesCatalog && namesACatalog(e.Args["this"]) {
		return g.fail(e.Class + " of a three-part name this dialect shortens")
	}
	out += g.child(e, "this")
	if expression := g.child(e, "expression"); expression != "" {
		out += " AS " + expression
	}
	// The properties written AFTER the query, which is where the words that
	// say whether it filled the table go.
	if properties, _ := e.Args["properties"].(*Expression); properties != nil {
		items, _ := properties.Args["expressions"].([]*Expression)
		for _, item := range items {
			if item.Class == "WithDataProperty" {
				out += " " + g.node(item)
			}
		}
	}
	return out
}

// namesACatalog reports whether a CREATE's target carries all three parts of
// a name. The target may be the table itself or a Schema wrapping it.
func namesACatalog(target any) bool {
	node, _ := target.(*Expression)
	if node == nil {
		return false
	}
	if node.Class == "Schema" {
		node, _ = node.Args["this"].(*Expression)
		if node == nil {
			return false
		}
	}
	catalog, _ := node.Args["catalog"].(*Expression)
	return catalog != nil
}

// hasTemporaryProperty reports whether this CREATE was written TEMPORARY. The
// reference keeps it as one of a LIST of properties rather than as a flag,
// so it is looked for rather than read.
func (g *generator) hasTemporaryProperty(e *Expression) bool {
	properties, _ := e.Args["properties"].(*Expression)
	if properties == nil {
		return false
	}
	items, _ := properties.Args["expressions"].([]*Expression)
	for _, item := range items {
		if item.Class == "TemporaryProperty" {
			return true
		}
	}
	return false
}

// viewColumnHasComment reports whether any of a view's named columns carries
// a comment.
func (g *generator) viewColumnHasComment(e *Expression) bool {
	schema, _ := e.Args["this"].(*Expression)
	if schema == nil || schema.Class != "Schema" {
		return false
	}
	columns, _ := schema.Args["expressions"].([]*Expression)
	for _, column := range columns {
		if items, _ := column.Args["constraints"].([]*Expression); len(items) > 0 {
			return true
		}
	}
	return false
}

// writeSchema writes a name with its column list: `t (a INT, b TEXT)`.
func (g *generator) writeSchema(e *Expression) string {
	items, _ := e.Args["expressions"].([]*Expression)
	if len(items) == 0 {
		return g.child(e, "this")
	}
	was := g.inColumnList
	g.inColumnList = true
	parts := make([]string, 0, len(items))
	for _, item := range items {
		parts = append(parts, g.node(item))
	}
	g.inColumnList = was
	columns := "(" + strings.Join(parts, ", ") + ")"
	// A Schema with no name is a bare column list -- the shape a table-level
	// UNIQUE keeps its columns in -- and there is nothing for the space to
	// separate.
	if name := g.child(e, "this"); name != "" {
		return name + " " + columns
	}
	return columns
}

// writeInsert writes `INSERT [OVERWRITE] INTO <target> <values-or-query>`.
func (g *generator) writeInsert(e *Expression) string {
	out := g.withPrefix(e, "INSERT") + " "
	if overwrite, _ := e.Args["overwrite"].(bool); overwrite {
		out += "OVERWRITE TABLE "
	} else {
		out += "INTO "
	}
	exists := ""
	if yes, _ := e.Args["exists"].(bool); yes {
		exists = " IF EXISTS"
	}
	was := g.inColumnList
	g.inColumnList = true
	target := g.child(e, "this")
	g.inColumnList = was
	body := g.child(e, "expression")
	// A WITH inside the query is written in FRONT of the whole statement:
	// `WITH cte AS (...) INSERT INTO t SELECT ...`. The tree has it where it
	// was written; only the spelling moves it.
	if expr, _ := e.Args["expression"].(*Expression); expr != nil && g.tables.HoistsInsertWith {
		if with, _ := expr.Args["with_"].(*Expression); with != nil {
			prefix := g.node(with)
			if rest, found := strings.CutPrefix(body, prefix+" "); found {
				body = rest
				out = prefix + " " + out
			}
		}
	}
	target += exists
	conflict := g.child(e, "conflict")
	returning := g.child(e, "returning")
	if g.tables.ReturningEnd {
		return clauses(out+target, body, conflict, returning)
	}
	return clauses(out+target, returning, body, conflict)
}

// writeValues writes the rows of a VALUES clause.
// writeValues writes the rows of a VALUES clause -- as the body of an INSERT,
// where it stands bare, or as a TABLE, where all but Databricks wrap it and
// it carries an alias of its own.
func (g *generator) writeValues(e *Expression) string {
	out := "VALUES " + g.list(e)
	if !standsWhereATableGoes(e) {
		return out
	}
	if g.tables.ValuesTableWrapped {
		out = "(" + out + ")"
	}
	if alias := g.child(e, "alias"); alias != "" {
		out += " AS " + alias
	}
	return out
}

// writeDrop writes `DROP <kind> [IF EXISTS] <name>`.
func (g *generator) writeDrop(e *Expression) string {
	kind, _ := e.Args["kind"].(string)
	out := "DROP " + kind + " "
	if exists, _ := e.Args["exists"].(bool); exists {
		out += "IF EXISTS "
	}
	tables, _ := e.Args["tables"].([]*Expression)
	names := make([]string, 0, len(tables))
	for _, t := range tables {
		// T-SQL writes only the last two parts of a three-part name here,
		// dropping the catalog. That LOSES a part of the name, so the port
		// refuses rather than writing something that names another object.
		if catalog, _ := t.Args["catalog"].(*Expression); catalog != nil &&
			g.tables.TruncatesCatalog {
			return g.fail(e.Class + " of a three-part name this dialect shortens")
		}
		names = append(names, g.node(t))
	}
	return out + strings.Join(names, ", ")
}

// writeColumnConstraint writes one thing said about a column. The wrapper is
// uniform and the KIND carries the meaning, so this only unwraps.
func (g *generator) writeColumnConstraint(e *Expression) string {
	if name := g.child(e, "this"); name != "" {
		return "CONSTRAINT " + name + " " + g.child(e, "kind")
	}
	return g.child(e, "kind")
}

// writeReference writes what a column points AT: the table, the columns of it
// when they were named, and what happens to this row when that one changes.
func (g *generator) writeReference(e *Expression) string {
	out := "REFERENCES " + g.child(e, "this")
	options, _ := e.Args["options"].([]string)
	for _, option := range options {
		out += " " + option
	}
	return out
}

// The constraint kinds, each a fixed phrase or a phrase with one operand.
func (g *generator) writeNotNullConstraint(e *Expression) string {
	if allow, _ := e.Args["allow_null"].(bool); allow {
		return "NULL"
	}
	return "NOT NULL"
}

func (g *generator) writeDefaultConstraint(e *Expression) string {
	return "DEFAULT " + g.child(e, "this")
}

func (g *generator) writePrimaryKeyConstraint(e *Expression) string {
	out := "PRIMARY KEY"
	// The direction is written only when the statement said one; the argument
	// is absent otherwise, which is not the same as false.
	if desc, ok := e.Args["desc"].(bool); ok {
		if desc {
			return out + " DESC"
		}
		return out + " ASC"
	}
	return out
}

// writeUniqueConstraint writes UNIQUE, over a column list when it has one.
// writeAutoIncrementConstraint writes an auto-numbering column. The spelling
// is the dialect's whole answer -- `AUTO_INCREMENT`, `IDENTITY`, or the long
// GENERATED form -- and DuckDB writes nothing at all, which drops the
// numbering, so the port refuses there instead.
func (g *generator) writeAutoIncrementConstraint(e *Expression) string {
	if spelling := g.tables.AutoIncrementSQL; spelling != "" {
		return spelling
	}
	return g.fail(e.Class + ", which this dialect writes nowhere")
}

func (g *generator) writeUniqueConstraint(e *Expression) string {
	if !g.tables.UniqueConstraintWritten {
		// This dialect has no such constraint and the reference DROPS it,
		// which silently gives up the guarantee the statement was making.
		return g.fail(e.Class + ", which this dialect writes nowhere")
	}
	if columns, _ := e.Args["this"].(*Expression); columns != nil {
		return "UNIQUE " + g.node(columns)
	}
	return "UNIQUE"
}

// writePrimaryKey writes a key over the columns of the whole table.
func (g *generator) writePrimaryKey(e *Expression) string {
	return "PRIMARY KEY (" + g.list(e) + ")"
}

// writeForeignKey writes the columns that point at another table, and where.
func (g *generator) writeForeignKey(e *Expression) string {
	return "FOREIGN KEY (" + g.list(e) + ") " + g.child(e, "reference")
}

// writeConstraint writes a NAMED constraint on the table. It holds a LIST of
// the kinds it names -- one in practice, but the shape is a list.
func (g *generator) writeConstraint(e *Expression) string {
	items, _ := e.Args["expressions"].([]*Expression)
	parts := make([]string, 0, len(items))
	for _, item := range items {
		parts = append(parts, g.node(item))
	}
	return "CONSTRAINT " + g.child(e, "this") + " " + strings.Join(parts, " ")
}

// writeCheckConstraint writes a condition every row has to satisfy.
// writeComputedConstraint writes a column the engine fills for itself. The
// spelling is the dialect's whole answer -- PostgreSQL keeps the words
// GENERATED ALWAYS and STORED, and the neutral dialect writes only `AS x`.
func (g *generator) writeComputedConstraint(e *Expression) string {
	spelling := g.tables.ComputedColumnSpelling
	if spelling == "" {
		return g.fail(e.Class + ", which this dialect writes another way")
	}
	return strings.ReplaceAll(spelling, "{expr}", g.child(e, "this"))
}

// writeGeneratedAsIdentity writes a column the engine numbers for itself.
func (g *generator) writeGeneratedAsIdentity(e *Expression) string {
	// T-SQL has a short spelling of its own -- `IDENTITY(7, 9)` -- with
	// nowhere to put CYCLE, ON NULL or ALWAYS. A column that says one of
	// those is refused rather than written without it.
	if short := g.tables.IdentityShortSQL; short != "" {
		if always, _ := e.Args["this"].(bool); always {
			return g.fail(e.Class + " ALWAYS, which this dialect writes as BY DEFAULT")
		}
		if cycle, _ := e.Args["cycle"].(bool); cycle {
			return g.fail(e.Class + " CYCLE, which this dialect writes nowhere")
		}
		if onNull, _ := e.Args["on_null"].(bool); onNull {
			return g.fail(e.Class + " ON NULL, which this dialect writes nowhere")
		}
		if value := g.child(e, "expression"); value != "" {
			return g.fail(e.Class + " over an expression, which this dialect writes nowhere")
		}
		start, increment := g.child(e, "start"), g.child(e, "increment")
		if start == "" {
			start = "1"
		}
		if increment == "" {
			increment = "1"
		}
		short = strings.ReplaceAll(short, "{start}", start)
		return strings.ReplaceAll(short, "{increment}", increment)
	}
	if !g.tables.IdentityWritten {
		// T-SQL has no such constraint and rewrites every one into
		// `IDENTITY(start, increment)`, which drops CYCLE and ON NULL with
		// it.
		return g.fail(e.Class + ", which this dialect writes another way")
	}
	out := "GENERATED "
	if always, _ := e.Args["this"].(bool); always {
		out += "ALWAYS"
	} else {
		out += "BY DEFAULT"
		if onNull, _ := e.Args["on_null"].(bool); onNull {
			out += " ON NULL"
		}
	}
	// An identity carrying an EXPRESSION is a computed column the engine does
	// not store; the reference records it here rather than as a Computed.
	if value := g.child(e, "expression"); value != "" {
		return out + " AS (" + value + ")"
	}
	out += " AS IDENTITY"
	var options []string
	if start := g.child(e, "start"); start != "" {
		options = append(options, "START WITH "+start)
	}
	if increment := g.child(e, "increment"); increment != "" {
		options = append(options, "INCREMENT BY "+increment)
	}
	if cycle, _ := e.Args["cycle"].(bool); cycle {
		options = append(options, "CYCLE")
	}
	if len(options) == 0 {
		return out
	}
	return out + " (" + strings.Join(options, " ") + ")"
}

func (g *generator) writeCheckConstraint(e *Expression) string {
	return "CHECK (" + g.child(e, "this") + ")"
}

func (g *generator) writeCommentConstraint(e *Expression) string {
	return "COMMENT " + g.child(e, "this")
}

func (g *generator) writeCollateConstraint(e *Expression) string {
	return "COLLATE " + g.child(e, "this")
}

// writeAlter writes `ALTER TABLE [IF EXISTS] <name> <actions>`.
func (g *generator) writeAlter(e *Expression) string {
	kind, _ := e.Args["kind"].(string)
	out := "ALTER " + kind + " "
	if exists, _ := e.Args["exists"].(bool); exists {
		out += "IF EXISTS "
	}
	out += g.child(e, "this")
	actions, _ := e.Args["actions"].([]*Expression)
	was := g.inColumnList
	g.inColumnList = true
	parts := make([]string, 0, len(actions))
	for i, action := range actions {
		switch action.Class {
		case "ColumnDef":
			// T-SQL says neither the word COLUMN nor a second ADD: it writes
			// `ADD a INT, b INT` for the same list of definitions.
			lead := ""
			if i == 0 || g.tables.AlterRepeatsAdd {
				lead = "ADD "
				if g.tables.AlterAddColumnWord {
					lead += "COLUMN "
				}
			}
			if exists, _ := action.Args["exists"].(bool); exists {
				lead += "IF NOT EXISTS "
			}
			parts = append(parts, lead+g.node(action))
		case "Drop":
			// The Drop written here names a COLUMN and takes no kind word of
			// its own; the statement-level DROP writes one.
			tables, _ := action.Args["tables"].([]*Expression)
			if len(tables) != 1 {
				g.inColumnList = was
				return g.fail(e.Class + " dropping more than one column")
			}
			text := "DROP COLUMN "
			if exists, _ := action.Args["exists"].(bool); exists {
				text += "IF EXISTS "
			}
			text += g.node(tables[0])
			if cascade, _ := action.Args["cascade"].(bool); cascade {
				text += " CASCADE"
			}
			if restrict, _ := action.Args["restrict"].(bool); restrict {
				text += " RESTRICT"
			}
			parts = append(parts, text)
		case "Select", "Union", "Except", "Intersect":
			// A VIEW is altered by being GIVEN a new query, and the query is
			// introduced the same way a CREATE introduces one.
			parts = append(parts, "AS "+g.node(action))
		default:
			parts = append(parts, g.node(action))
		}
	}
	g.inColumnList = was
	return out + " " + strings.Join(parts, ", ")
}

// writeAddConstraint writes the constraints an ALTER adds. They are a LIST on
// the node, as the reference keeps them.
func (g *generator) writeAddConstraint(e *Expression) string {
	items, _ := e.Args["expressions"].([]*Expression)
	parts := make([]string, 0, len(items))
	for _, item := range items {
		parts = append(parts, g.node(item))
	}
	return "ADD " + strings.Join(parts, ", ")
}

// writeRenameColumn writes the new name one column takes.
func (g *generator) writeRenameColumn(e *Expression) string {
	out := "RENAME COLUMN "
	if exists, _ := e.Args["exists"].(bool); exists {
		out += "IF EXISTS "
	}
	return out + g.child(e, "this") + " TO " + g.child(e, "to")
}

// writeAlterColumn writes what an ALTER says about one column. Exactly one of
// the four slots is filled, and which one it is says what the statement does.
func (g *generator) writeAlterColumn(e *Expression) string {
	out := "ALTER COLUMN " + g.child(e, "this")
	switch {
	case e.Args["dtype"] != nil:
		// The phrase in front of the new type is the dialect's: `SET DATA
		// TYPE` in most, `TYPE` in Databricks, nothing at all in T-SQL.
		if word := g.tables.AlterColumnTypeWord; word != "" {
			out += " " + word
		}
		out += " " + g.child(e, "dtype")
		if using := g.child(e, "using"); using != "" {
			out += " USING " + using
		}
	case e.Args["default"] != nil:
		out += " SET DEFAULT " + g.child(e, "default")
	case e.Args["comment"] != nil:
		out += " COMMENT " + g.child(e, "comment")
	default:
		if drop, _ := e.Args["drop"].(bool); drop {
			return out + " DROP DEFAULT"
		}
		return g.fail(e.Class + " that says nothing about the column")
	}
	return out
}

// writeAlterRename writes the new name a table takes.
func (g *generator) writeAlterRename(e *Expression) string {
	target, _ := e.Args["this"].(*Expression)
	if target == nil {
		return g.fail(e.Class + " with no target")
	}
	switch g.tables.RenameTarget {
	case "whole":
		return "RENAME TO " + g.node(target)
	case "name":
		// The new table lives where the old one did, so the qualifier is not
		// written. Only the name is.
		name, _ := target.Args["this"].(*Expression)
		if name == nil {
			return g.fail(e.Class + " with no name")
		}
		return "RENAME TO " + g.node(name)
	}
	// This dialect writes another statement entirely -- T-SQL calls a stored
	// procedure -- which is a transformation rather than a spelling.
	return g.fail(e.Class + ", which this dialect writes another way")
}

// writeUpdate writes `UPDATE <table> SET a = 1 [FROM ...] [WHERE ...]`.
//
// It serves two shapes. As a statement it has a target and a SET list; as the
// action of a MERGE branch it has the list alone, or -- DuckDB's `WHEN MATCHED
// THEN UPDATE` -- neither.
func (g *generator) writeUpdate(e *Expression) string {
	head := g.withPrefix(e, "UPDATE")
	if this := g.child(e, "this"); this != "" {
		head += " " + this
	}
	if items, _ := e.Args["expressions"].([]*Expression); len(items) > 0 {
		head += " SET " + g.list(e)
	}
	returning := g.child(e, "returning")
	from := g.child(e, "from_")
	where := g.child(e, "where")
	if g.tables.ReturningEnd {
		return clauses(head, from, where, returning)
	}
	// T-SQL writes it here, between the assignments and the FROM.
	return clauses(head, returning, from, where)
}

// clauses joins what a statement is made of, skipping the parts it has none
// of. The writers return their clause without a leading space, so the spaces
// belong to whoever puts them in a row.
func clauses(parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, " ")
}

// writeDelete writes `DELETE FROM <table> [USING ...] [WHERE ...]`.
func (g *generator) writeDelete(e *Expression) string {
	this := g.child(e, "this")
	if this == "" {
		return g.fail(e.Class + " with no table")
	}
	if tables, _ := e.Args["tables"].([]*Expression); len(tables) > 0 {
		// `DELETE x FROM z` names the target separately from the source; the
		// port does not read that shape and does not write it either.
		return g.fail(e.Class + " naming its target twice")
	}
	body := "FROM " + this
	if using, _ := e.Args["using"].([]*Expression); len(using) > 0 {
		parts := make([]string, 0, len(using))
		for _, t := range using {
			// A comma join hangs off the table it follows and is written by
			// the table, which is why `USING a, b` is one entry here.
			parts = append(parts, g.node(t))
		}
		body += " USING " + strings.Join(parts, ", ")
	}
	cluster := g.child(e, "cluster")
	where := g.child(e, "where")
	returning := g.child(e, "returning")
	verb := g.withPrefix(e, "DELETE")
	if g.tables.ReturningEnd {
		return clauses(verb, body, cluster, where, returning)
	}
	return clauses(verb, returning, body, cluster, where)
}

// writeReturning writes the clause that says which rows a write hands back.
// T-SQL calls it OUTPUT; writeUpdate and writeDelete put it where the dialect
// wants it.
func (g *generator) writeReturning(e *Expression) string {
	if into, _ := e.Args["into"].(*Expression); into != nil {
		return g.fail(e.Class + " with an INTO")
	}
	return g.tables.ReturningWord + " " + g.list(e)
}

// writeMerge writes `MERGE INTO <target> USING <source> ON <cond> WHEN ...`.
//
// DuckDB's match is a column list rather than a condition, and it takes the
// place of the ON entirely rather than sitting beside it.
func (g *generator) writeMerge(e *Expression) string {
	this, using := g.child(e, "this"), g.child(e, "using")
	target, _ := e.Args["this"].(*Expression)
	whens, _ := e.Args["whens"].(*Expression)
	if this == "" || using == "" || target == nil || whens == nil {
		// All three are what a MERGE IS: two relations and what to do about
		// how they line up. Writing it without one names no statement.
		return g.fail(e.Class + " missing what it matches or what it does")
	}
	if g.tables.MergeWithoutTarget {
		g.markMergeTargets(target, whens)
	}
	out := g.withPrefix(e, "MERGE INTO "+this+" USING "+using)
	if on := g.child(e, "on"); on != "" {
		out += " ON " + on
	} else if columns, _ := e.Args["using_cond"].([]*Expression); len(columns) > 0 {
		parts := make([]string, 0, len(columns))
		for _, c := range columns {
			parts = append(parts, g.node(c))
		}
		out += " USING (" + strings.Join(parts, ", ") + ")"
	}
	return clauses(out, g.child(e, "whens"), g.child(e, "returning"))
}

// writeWhens writes the branches, in the order they were written.
func (g *generator) writeWhens(e *Expression) string {
	items, _ := e.Args["expressions"].([]*Expression)
	parts := make([]string, 0, len(items))
	for _, item := range items {
		parts = append(parts, g.node(item))
	}
	return strings.Join(parts, " ")
}

// writeWhen writes one branch.
//
// The INSERT and UPDATE spellings are built HERE rather than by their own
// writers: a branch's `INSERT (a, b) VALUES (1, 2)` names no table and is not
// the statement of the same name.
func (g *generator) writeWhen(e *Expression) string {
	out := "WHEN "
	if matched, _ := e.Args["matched"].(bool); matched {
		out += "MATCHED"
	} else {
		out += "NOT MATCHED"
	}
	if source, _ := e.Args["source"].(bool); source {
		out += " BY SOURCE"
	}
	if condition := g.child(e, "condition"); condition != "" {
		out += " AND " + condition
	}
	out += " THEN "

	then, _ := e.Args["then"].(*Expression)
	if then == nil {
		return g.fail(e.Class + " with no action")
	}
	switch then.Class {
	case "Insert":
		action := "INSERT"
		if this := g.child(then, "this"); this != "" {
			action += " " + this
		}
		if values := g.child(then, "expression"); values != "" {
			action += " VALUES " + values
		}
		return out + action
	case "Update":
		if items, _ := then.Args["expressions"].([]*Expression); len(items) > 0 {
			return out + "UPDATE SET " + g.list(then)
		}
		return out + "UPDATE"
	}
	return out + g.node(then)
}

// markMergeTargets finds the columns a MERGE branch ASSIGNS whose qualifier
// names the target table -- by its own name or by its alias -- so that
// writeColumn leaves the qualifier off.
//
// Only the assigned side. The condition, the right-hand side of an assignment
// and the VALUES of an INSERT all still refer to two tables and keep their
// qualifiers; the column LIST of an INSERT does not.
func (g *generator) markMergeTargets(table, whens *Expression) {
	targets := map[string]bool{}
	if name, _ := table.Args["this"].(*Expression); name != nil {
		targets[g.normalized(name)] = true
	}
	if alias, _ := table.Args["alias"].(*Expression); alias != nil {
		if name, _ := alias.Args["this"].(*Expression); name != nil {
			targets[g.normalized(name)] = true
		}
	}

	branches, _ := whens.Args["expressions"].([]*Expression)
	for _, when := range branches {
		then, _ := when.Args["then"].(*Expression)
		if then == nil {
			continue
		}
		switch then.Class {
		case "Update":
			g.markAssignedTargets(then, targets)
		case "Insert":
			columns, _ := then.Args["this"].(*Expression)
			if columns == nil || columns.Class != "Tuple" {
				continue
			}
			items, _ := columns.Args["expressions"].([]*Expression)
			for _, item := range items {
				g.markIfTarget(item, targets)
			}
		}
	}
}

// markAssignedTargets walks an UPDATE branch for every equality in it, as the
// reference does: an assignment nested inside another expression is assigned
// just the same. A subquery is its own scope and is left alone.
func (g *generator) markAssignedTargets(e *Expression, targets map[string]bool) {
	if e == nil || e.Class == "Select" || e.Class == "Subquery" {
		return
	}
	if e.Class == "EQ" {
		if lhs, _ := e.Args["this"].(*Expression); lhs != nil {
			g.markIfTarget(lhs, targets)
		}
	}
	for _, arg := range e.Args {
		switch child := arg.(type) {
		case *Expression:
			g.markAssignedTargets(child, targets)
		case []*Expression:
			for _, one := range child {
				g.markAssignedTargets(one, targets)
			}
		}
	}
}

func (g *generator) markIfTarget(column *Expression, targets map[string]bool) {
	if column == nil || column.Class != "Column" {
		return
	}
	qualifier, _ := column.Args["table"].(*Expression)
	if qualifier == nil || !targets[g.normalized(qualifier)] {
		return
	}
	if g.mergeTarget == nil {
		g.mergeTarget = map[*Expression]bool{}
	}
	g.mergeTarget[column] = true
}

// normalized is the case a name is COMPARED in, which is not always the case
// it is written in: PostgreSQL folds a bare name and keeps a quoted one, so
// `X.a` names the same table as `x` and `"X".a` does not.
func (g *generator) normalized(identifier *Expression) string {
	name, _ := identifier.Args["this"].(string)
	fold := g.tables.NormalizeUnquoted
	if identifier.Args["quoted"] == true {
		fold = g.tables.NormalizeQuoted
	}
	switch fold {
	case "lower":
		return strings.ToLower(name)
	case "upper":
		return strings.ToUpper(name)
	}
	return name
}

// writeFunctionRest writes everything after `CREATE ... FUNCTION `, which is
// the one CREATE whose properties are not all in one place: a return type goes
// after the parameter list in most dialects and INSIDE the body in DuckDB,
// and the rest go after the parameter list or -- in DuckDB -- nowhere at all.
func (g *generator) writeFunctionRest(e *Expression, out string) string {
	out += g.child(e, "this")

	var afterParams, afterAs []string
	properties, _ := e.Args["properties"].(*Expression)
	if properties != nil {
		items, _ := properties.Args["expressions"].([]*Expression)
		for _, item := range items {
			if item.Class == "TemporaryProperty" {
				continue // already written, in front of the kind
			}
			place := "schema"
			if item.Class == "ReturnsProperty" {
				place = g.tables.FunctionReturnsPlace[returnsShape(item)]
			} else if !g.tables.FunctionPropertiesWritten {
				place = ""
			}
			switch place {
			case "schema":
				afterParams = append(afterParams, g.node(item))
			case "alias":
				afterAs = append(afterAs, "TABLE")
			default:
				// The dialect writes this nowhere, so the function it makes
				// would promise less than the statement did.
				return g.fail(item.Class + ", which this dialect writes nowhere")
			}
		}
	}
	for _, part := range afterParams {
		out += " " + part
	}

	body, _ := e.Args["expression"].(*Expression)
	if body == nil {
		if len(afterAs) > 0 {
			return g.fail(e.Class + " whose return type has no body to sit in")
		}
		return out
	}
	// Databricks turns a table-valued function's body into a RETURN before
	// writing it, whatever it was written as, and then writes no AS in front
	// of one.
	isReturn := body.Class == "Return"
	if !isReturn && g.tables.FunctionWrapsTableBody && g.returnsATable(properties) {
		return out + " " + g.tables.ReturnWord + " " + g.node(body)
	}
	if isReturn && !g.tables.FunctionReturnAs {
		return out + " " + g.node(body)
	}
	out += " AS"
	for _, part := range afterAs {
		out += " " + part
	}
	return out + " " + g.node(body)
}

// returnsShape names which of the three shapes a RETURNS property holds, which
// is what decides where the dialect writes it.
func returnsShape(e *Expression) string {
	this, _ := e.Args["this"].(*Expression)
	switch {
	case this == nil:
		return "type"
	case this.Class == "Schema":
		return "schema"
	case this.Class == "Var":
		return "table"
	}
	return "type"
}

// returnsATable reports whether these properties say the function returns one.
func (g *generator) returnsATable(properties *Expression) bool {
	if properties == nil {
		return false
	}
	items, _ := properties.Args["expressions"].([]*Expression)
	for _, item := range items {
		if item.Class == "ReturnsProperty" {
			if isTable, _ := item.Args["is_table"].(bool); isTable {
				return true
			}
		}
	}
	return false
}

// The properties a function is described by. Each is a fixed phrase or a
// phrase with one operand, and the reference writes the operand of the two
// that have one WITHOUT quotes -- the string is the phrase, not a value.
func (g *generator) writeReturnsProperty(e *Expression) string {
	if null, _ := e.Args["null"].(bool); null {
		return "RETURNS NULL ON NULL INPUT"
	}
	out := "RETURNS "
	// T-SQL NAMES the table a function returns, and the name sits between the
	// word and the table it names.
	if named := g.child(e, "table"); named != "" {
		out += named + " "
	}
	return out + g.child(e, "this")
}

func (g *generator) writeLanguageProperty(e *Expression) string {
	return "LANGUAGE " + g.child(e, "this")
}

func (g *generator) writeStabilityProperty(e *Expression) string {
	this, _ := e.Args["this"].(*Expression)
	if this == nil {
		return g.fail(e.Class + " with no word")
	}
	word, _ := this.Args["this"].(string)
	return word
}

func (g *generator) writeStrictProperty(*Expression) string { return "STRICT" }

// writeHeredoc writes a body the reference never read. The tag is not on the
// node, so every dialect writes the bare `$$`.
func (g *generator) writeHeredoc(e *Expression) string {
	body, _ := e.Args["this"].(string)
	return "$$" + body + "$$"
}

// writeSetConfigProperty writes the session setting a function runs under.
func (g *generator) writeSetConfigProperty(e *Expression) string {
	return g.child(e, "this")
}

// writeSet writes `SET a = 1`, the items it holds separated by commas.
func (g *generator) writeSet(e *Expression) string {
	if unset, _ := e.Args["unset"].(bool); unset {
		return g.fail(e.Class + " that unsets rather than sets")
	}
	return "SET " + g.list(e)
}

// writeSetItem writes one setting. The two sides are held as an equality and
// the dialect decides what goes between them -- T-SQL writes nothing at all.
func (g *generator) writeSetItem(e *Expression) string {
	item, _ := e.Args["this"].(*Expression)
	if item == nil || item.Class != "EQ" {
		return g.fail(e.Class + " that is not a setting")
	}
	out := ""
	if kind, _ := e.Args["kind"].(string); kind != "" {
		// The scope word says WHICH setting is being changed -- a global one
		// or this session's. A dialect that has no such word writes none, and
		// the port refuses rather than changing the wrong scope.
		if !g.tables.SetItemKindWritten[kind] {
			return g.fail(e.Class + " " + kind + ", a scope this dialect writes away")
		}
		out = kind + " "
	}
	// A VARIABLE takes an equals sign even where a SETTING does not: T-SQL
	// writes `SET XACT_ABORT ON` and `SET @count = 1`, which are two
	// statements wearing the same word.
	separator := g.tables.SetItemSeparator
	if target, _ := item.Args["this"].(*Expression); target != nil &&
		target.Class == "Parameter" {
		separator = g.tables.SetItemVariableSeparator
	}
	return out + g.child(item, "this") + separator + g.child(item, "expression")
}

func (g *generator) writeCalledOnNullInputProperty(*Expression) string {
	return "CALLED ON NULL INPUT"
}

func (g *generator) writeSqlReadWriteProperty(e *Expression) string {
	phrase, _ := e.Args["this"].(string)
	return phrase
}

// writeUserDefinedFunction writes a function's name and its parameters.
func (g *generator) writeUserDefinedFunction(e *Expression) string {
	items, _ := e.Args["expressions"].([]*Expression)
	was := g.inColumnList
	g.inColumnList = true
	parts := make([]string, 0, len(items))
	for _, item := range items {
		parts = append(parts, g.node(item))
	}
	g.inColumnList = was
	return g.child(e, "this") + "(" + strings.Join(parts, ", ") + ")"
}

// writeInOutConstraint writes which way a parameter carries its value.
//
// PostgreSQL writes it in FRONT of the parameter; everywhere else it follows
// the type, and INOUT is two words there. Same node, two places.
func (g *generator) writeInOutConstraint(e *Expression) string {
	input, _ := e.Args["input_"].(bool)
	output, _ := e.Args["output"].(bool)
	variadic, _ := e.Args["variadic"].(bool)
	key := ""
	switch {
	case variadic:
		key = "variadic"
	case input && output:
		key = "inout"
	case output:
		key = "out"
	case input:
		key = "in"
	default:
		return g.fail(e.Class + " that says nothing")
	}
	// PostgreSQL spells both directions as one word; everyone else as two.
	return g.tables.ParameterModeWords[key]
}

// writeReturn writes what a function hands back. DuckDB writes no word at
// all, leaving the body where the word would have been.
func (g *generator) writeReturn(e *Expression) string {
	if word := g.tables.ReturnWord; word != "" {
		return word + " " + g.child(e, "this")
	}
	return g.child(e, "this")
}

// readableAsABareName reports whether a name written without quotes would be
// read back as the same name. It is the tokenizer's own rule for what may
// continue a word, plus the symbols a dialect lets a name hold.
func readableAsABareName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if isIdentifierChar(r) || r == '$' || r == '#' || r == '@' {
			continue
		}
		return false
	}
	return true
}

// hasConstraint reports whether a column definition carries one of a KIND.
func hasConstraint(e *Expression, class string) bool {
	items, _ := e.Args["constraints"].([]*Expression)
	for _, item := range items {
		if item.Class == class {
			return true
		}
		if kind, _ := item.Args["kind"].(*Expression); kind != nil && kind.Class == class {
			return true
		}
	}
	return false
}

// kindOf is a column definition's declared type, or nil.
func kindOf(e *Expression) *Expression {
	kind, _ := e.Args["kind"].(*Expression)
	return kind
}

// writeIndex writes an index's name, the table it is on, and the columns.
//
// The name may be absent -- PostgreSQL lets the server choose one -- and
// Databricks puts the word TABLE between the two.
func (g *generator) writeIndex(e *Expression) string {
	out := ""
	if name := g.child(e, "this"); name != "" {
		out = name + " "
	}
	out += g.tables.IndexOnWord + " " + g.child(e, "table")
	params, _ := e.Args["params"].(*Expression)
	if params == nil {
		return g.fail(e.Class + " over no columns")
	}
	columns, _ := params.Args["columns"].([]*Expression)
	parts := make([]string, 0, len(columns))
	for _, column := range columns {
		parts = append(parts, g.node(column))
	}
	return out + "(" + strings.Join(parts, ", ") + ")"
}

// escapeControlCharacters turns the control characters a C-style escape can
// spell back into that escape. A literal backslash is left alone -- the
// reference does not double it here -- and so is a NUL, which has no spelling.
func escapeControlCharacters(body string) string {
	var out strings.Builder
	for _, r := range body {
		switch r {
		case '\a':
			out.WriteString(`\a`)
		case '\b':
			out.WriteString(`\b`)
		case '\f':
			out.WriteString(`\f`)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		case '\v':
			out.WriteString(`\v`)
		default:
			out.WriteRune(r)
		}
	}
	return out.String()
}

// writeTableHint writes the advice a table was given about how to read it.
func (g *generator) writeTableHint(e *Expression) string {
	return "WITH (" + g.list(e) + ")"
}

// writeTruncate writes the tables a TRUNCATE empties.
func (g *generator) writeTruncate(e *Expression) string {
	out := "TRUNCATE TABLE "
	if exists, _ := e.Args["exists"].(bool); exists {
		out += "IF EXISTS "
	}
	tables, _ := e.Args["expressions"].([]*Expression)
	parts := make([]string, 0, len(tables))
	for _, table := range tables {
		text := g.node(table)
		// ONLY says this table and not the ones that inherit from it.
		if only, _ := table.Args["only"].(bool); only {
			text = "ONLY " + text
		}
		parts = append(parts, text)
	}
	out += strings.Join(parts, ", ")
	if identity, _ := e.Args["identity"].(string); identity != "" {
		out += " " + identity + " IDENTITY"
	}
	if option, _ := e.Args["option"].(string); option != "" {
		out += " " + option
	}
	return out
}

// writeUse writes the database, schema or role a session moves to.
func (g *generator) writeUse(e *Expression) string {
	out := "USE "
	if kind := g.child(e, "kind"); kind != "" {
		out += kind + " "
	}
	return out + g.child(e, "this")
}

// writeTransaction writes BEGIN, COMMIT and ROLLBACK.
//
// T-SQL says TRANSACTION after the verb and DROPS the name that follows -- so
// a `ROLLBACK TO b` written there would roll back everything rather than to
// the savepoint, which is a different action and is refused instead.
func (g *generator) writeTransaction(e *Expression) string {
	verb := map[string]string{
		"Transaction": "BEGIN", "Commit": "COMMIT", "Rollback": "ROLLBACK",
	}[e.Class]
	if savepoint := g.child(e, "savepoint"); savepoint != "" {
		if !g.tables.TransactionNameWritten {
			return g.fail(e.Class + " to a savepoint, which this dialect writes away")
		}
		return verb + " TO " + savepoint
	}
	if word := g.tables.TransactionWord; word != "" {
		verb += " " + word
	}
	if name := g.child(e, "this"); name != "" {
		if !g.tables.TransactionNameWritten {
			return g.fail(e.Class + " with a name, which this dialect writes away")
		}
		return verb + " " + name
	}
	return verb
}

// writeGrant writes a permission handed over, and the REVOKE that takes it
// back.
func (g *generator) writeGrant(e *Expression) string {
	verb, joiner := "GRANT", "TO"
	if e.Class == "Revoke" {
		verb, joiner = "REVOKE", "FROM"
	}
	out := verb + " "
	grantOption, _ := e.Args["grant_option"].(bool)
	// On a REVOKE the option comes FIRST and takes away the right to pass the
	// privilege on; on a GRANT it comes last and hands that right over.
	if verb == "REVOKE" && grantOption {
		out += "GRANT OPTION FOR "
	}
	privileges, _ := e.Args["privileges"].([]*Expression)
	parts := make([]string, 0, len(privileges))
	for _, privilege := range privileges {
		parts = append(parts, g.node(privilege))
	}
	out += strings.Join(parts, ", ") + " ON "
	if kind, _ := e.Args["kind"].(string); kind != "" {
		out += kind + " "
	}
	out += g.child(e, "securable") + " " + joiner + " "

	principals, _ := e.Args["principals"].([]*Expression)
	parts = parts[:0]
	for _, principal := range principals {
		parts = append(parts, g.node(principal))
	}
	out += strings.Join(parts, ", ")
	if verb == "GRANT" && grantOption {
		out += " WITH GRANT OPTION"
	}
	if cascade, _ := e.Args["cascade"].(string); cascade != "" {
		out += " " + cascade
	}
	return out
}

// writeGrantPrivilege writes one right.
func (g *generator) writeGrantPrivilege(e *Expression) string {
	return g.child(e, "this")
}

// writeGrantPrincipal writes who the right is handed to, with the word that
// says what sort of principal it is when the statement said one.
func (g *generator) writeGrantPrincipal(e *Expression) string {
	out := ""
	if kind, _ := e.Args["kind"].(string); kind != "" {
		out = kind + " "
	}
	return out + g.child(e, "this")
}

// writeComment writes the note left on a table, a view or a column.
func (g *generator) writeComment(e *Expression) string {
	kind, _ := e.Args["kind"].(string)
	return "COMMENT ON " + kind + " " + g.child(e, "this") +
		" IS " + g.child(e, "expression")
}

// writePragma writes the engine setting a PRAGMA reads or changes.
func (g *generator) writePragma(e *Expression) string {
	return "PRAGMA " + g.child(e, "this")
}

// writeOnConflict writes what an INSERT does when a row is already there.
//
// The keys are written as ORDERED members, which is why one may carry a NULLS
// clause: the conflict is decided by an index, and this names which one.
func (g *generator) writeOnConflict(e *Expression) string {
	out := "ON CONFLICT"
	if constraint := g.child(e, "constraint"); constraint != "" {
		out += " ON CONSTRAINT " + constraint
	}
	if keys, _ := e.Args["conflict_keys"].([]*Expression); len(keys) > 0 {
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			parts = append(parts, g.node(key))
		}
		out += "(" + strings.Join(parts, ", ") + ")"
	}
	if predicate := g.child(e, "index_predicate"); predicate != "" {
		out += " " + predicate
	}
	action, _ := e.Args["action"].(*Expression)
	if action == nil {
		return g.fail(e.Class + " that says nothing to do")
	}
	name, _ := action.Args["this"].(string)
	out += " " + name
	if items, _ := e.Args["expressions"].([]*Expression); len(items) > 0 {
		out += " SET " + g.list(e)
	}
	if where := g.child(e, "where"); where != "" {
		out += " " + where
	}
	return out
}

// writePartition writes which partition of a table is being written.
func (g *generator) writePartition(e *Expression) string {
	return strings.ReplaceAll(g.tables.PartitionSQL, "{members}", g.list(e))
}

// writeAlterSet writes what an `ALTER TABLE ... SET` sets.
//
// Only PostgreSQL writes the words that say WHAT is set; everywhere else the
// reference writes a bare `ALTER TABLE t SET`, which sets nothing at all, so
// the port refuses rather than writing that.
func (g *generator) writeAlterSet(e *Expression) string {
	if settings, _ := e.Args["expressions"].([]*Expression); len(settings) > 0 {
		list := g.list(e)
		if g.tables.AlterSetWrapsOptions {
			return "SET (" + list + ")"
		}
		return "SET " + list
	}
	for _, key := range []string{"option", "tablespace", "access_method"} {
		value := g.child(e, key)
		if value == "" {
			continue
		}
		if !g.tables.AlterSetOptionWritten {
			return g.fail(e.Class + ", which this dialect writes away")
		}
		switch key {
		case "tablespace":
			return "SET TABLESPACE " + value
		case "access_method":
			return "SET ACCESS METHOD " + value
		}
		return "SET " + value
	}
	return g.fail(e.Class + " that sets nothing")
}

// writeWithDataProperty writes whether a table made from a query is FILLED
// from it or only shaped by it.
func (g *generator) writeWithDataProperty(e *Expression) string {
	if !g.tables.WithDataWritten {
		// The words say whether the table has rows in it. A dialect that
		// writes them nowhere would make a copy where an empty table was
		// asked for, or the other way round.
		return g.fail(e.Class + ", which this dialect writes nowhere")
	}
	out := "WITH "
	if no, _ := e.Args["no"].(bool); no {
		out += "NO "
	}
	out += "DATA"
	if statistics, ok := e.Args["statistics"].(bool); ok {
		if statistics {
			return out + " AND STATISTICS"
		}
		return out + " AND NO STATISTICS"
	}
	return out
}

// writeColumnPosition writes where a newly added column goes among the ones
// already there.
func (g *generator) writeColumnPosition(e *Expression) string {
	where, _ := e.Args["position"].(string)
	if this := g.child(e, "this"); this != "" {
		return where + " " + this
	}
	return where
}

// standsWhereATableGoes reports whether a VALUES is being used as a TABLE
// rather than as the body of an INSERT.
//
// The reference asks the same question the same way -- by looking for a FROM
// or a JOIN above it -- because nothing on the node itself says which it is.
// A DELETE's USING counts too: it holds relations.
func standsWhereATableGoes(e *Expression) bool {
	for at := e.Parent; at != nil; at = at.Parent {
		switch at.Class {
		case "From", "Join", "Delete", "Table":
			return true
		case "Insert", "Select", "Union", "Except", "Intersect":
			return false
		}
	}
	return false
}

// writeAttach writes `ATTACH [IF NOT EXISTS] <this> [(options)]`.
//
// The word DATABASE never comes back: it is optional on the way in and the
// reference drops it, so `ATTACH DATABASE 'f'` and `ATTACH 'f'` are the same
// statement written the one way.
func (g *generator) writeAttach(e *Expression) string {
	out := "ATTACH"
	if exists, _ := e.Args["exists"].(bool); exists {
		out += " IF NOT EXISTS"
	}
	out += " " + g.child(e, "this")
	if options, _ := e.Args["expressions"].([]*Expression); len(options) > 0 {
		out += " (" + g.list(e) + ")"
	}
	return out
}

// writeDetach writes `DETACH [DATABASE IF EXISTS] <this>`.
//
// DuckDB requires the word DATABASE where IF EXISTS is written and accepts it
// nowhere else that matters, so the reference puts it in exactly there. The
// word is not in the tree: `DETACH DATABASE db` and `DETACH db` both come
// back as `DETACH db`, and `DETACH IF EXISTS f` gains the word it was
// written without.
func (g *generator) writeDetach(e *Expression) string {
	out := "DETACH"
	if exists, _ := e.Args["exists"].(bool); exists {
		out += " DATABASE IF EXISTS"
	}
	return out + " " + g.child(e, "this")
}

// writeAttachOption writes one setting of an ATTACH: a name, and a value
// separated from it by nothing but a space.
func (g *generator) writeAttachOption(e *Expression) string {
	out := g.child(e, "this")
	if value := g.child(e, "expression"); value != "" {
		out += " " + value
	}
	return out
}

// writeInstall writes `[FORCE] INSTALL <this> [FROM <source>]`.
//
// Only DuckDB has the statement; every other dialect's generator refuses it
// rather than writing something that dialect cannot run. The port reaches
// that case only when a tree parsed as DuckDB is written back as something
// else, because nowhere else is INSTALL a keyword to begin with.
func (g *generator) writeInstall(e *Expression) string {
	if g.dialect != "duckdb" {
		return g.fail("INSTALL, which this dialect has no spelling for")
	}
	out := "INSTALL "
	if force, _ := e.Args["force"].(bool); force {
		out = "FORCE " + out
	}
	out += g.child(e, "this")
	if from := g.child(e, "from_"); from != "" {
		out += " FROM " + from
	}
	return out
}

// writeCommand writes back a statement the tokenizer took verbatim: the
// keyword, and the text after it exactly as it was written.
func (g *generator) writeCommand(e *Expression) string {
	this, _ := e.Args["this"].(string)
	payload := ""
	if lit, ok := e.Args["expression"].(*Expression); ok {
		payload, _ = lit.Args["this"].(string)
	}
	if payload = strings.TrimSpace(payload); payload == "" {
		return this
	}
	return this + " " + payload
}

// writeCache writes `CACHE [LAZY] TABLE <table> [OPTIONS(k = v)] [AS <query>]`.
// The word TABLE is always written, whether or not it was read.
func (g *generator) writeCache(e *Expression) string {
	out := "CACHE"
	if lazy, _ := e.Args["lazy"].(bool); lazy {
		out += " LAZY"
	}
	out += " TABLE " + g.child(e, "this")
	if options, _ := e.Args["options"].([]*Expression); len(options) == 2 {
		out += " OPTIONS(" + g.node(options[0]) + " = " + g.node(options[1]) + ")"
	}
	if body := g.child(e, "expression"); body != "" {
		out += " AS " + body
	}
	return out
}

// writeUncache writes `UNCACHE TABLE [IF EXISTS] <table>`.
func (g *generator) writeUncache(e *Expression) string {
	out := "UNCACHE TABLE"
	if exists, _ := e.Args["exists"].(bool); exists {
		out += " IF EXISTS"
	}
	return out + " " + g.child(e, "this")
}

// writeDescribe writes `DESCRIBE [<style>] <subject> [AS JSON]`.
func (g *generator) writeDescribe(e *Expression) string {
	out := "DESCRIBE"
	if style, _ := e.Args["style"].(string); style != "" {
		out += " " + style
	}
	out += " " + g.child(e, "this")
	if asJSON, _ := e.Args["as_json"].(bool); asJSON {
		out += " AS JSON"
	}
	return out
}

// writeAnalyze writes `ANALYZE {options} {kind} {tables} {partition}
// {statistics}`, in that order whatever order they were read in.
func (g *generator) writeAnalyze(e *Expression) string {
	out := "ANALYZE"
	if options, _ := e.Args["options"].([]string); len(options) > 0 {
		out += " " + strings.Join(options, " ")
	}
	if kind, _ := e.Args["kind"].(string); kind != "" {
		out += " " + kind
	}
	if tables, _ := e.Args["tables"].([]*Expression); len(tables) > 0 {
		parts := make([]string, 0, len(tables))
		for _, table := range tables {
			parts = append(parts, g.node(table))
		}
		out += " " + strings.Join(parts, ", ")
	}
	if partition := g.child(e, "partition"); partition != "" {
		out += " " + partition
	}
	if statistics := g.child(e, "expression"); statistics != "" {
		out += " " + statistics
	}
	return out
}

// writeAnalyzeStatistics writes `COMPUTE [DELTA] STATISTICS [what] [columns]`.
func (g *generator) writeAnalyzeStatistics(e *Expression) string {
	out, _ := e.Args["kind"].(string)
	if option, _ := e.Args["option"].(string); option != "" {
		out += " " + option
	}
	out += " STATISTICS"
	if what, _ := e.Args["this"].(string); what != "" {
		out += " " + what
	}
	if columns, _ := e.Args["expressions"].([]*Expression); len(columns) > 0 {
		out += " " + g.list(e)
	}
	return out
}

// isPlainNumber reports whether a node is a number WRITTEN as one: a numeric
// literal and nothing else. A negated literal counts as a number to the
// reference and does not here, because the two differ in whether the result
// can be read again.
func isPlainNumber(e *Expression) bool {
	if e == nil || e.Class != "Literal" {
		return false
	}
	str, _ := e.Args["is_string"].(bool)
	return !str
}

// withoutFoldedOrder is the call as it was before the ordering was folded
// into its first argument: the same node, with that argument put back.
func (g *generator) withoutFoldedOrder(e, order *Expression) *Expression {
	unfolded := New(e.Class)
	for _, key := range e.Keys {
		unfolded.Set(key, e.Args[key])
	}
	unfolded.Set("this", order.Args["this"])
	return unfolded
}

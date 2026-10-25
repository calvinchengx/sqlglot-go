package sqlglot

import "strings"

// isMySQLFamily reports whether a dialect inherits MySQL's parser rules:
// Doris, SingleStore and StarRocks subclass MySQL and take its ALTER
// dispatch unchanged.
func isMySQLFamily(dialect string) bool {
	switch dialect {
	case "mysql", "doris", "singlestore", "starrocks":
		return true
	}
	return false
}

// parseLeadingHint reads a MySQL optimizer hint sitting between the verb and
// its target: `UPDATE /*+ MAX_EXECUTION_TIME(1) */ t` and the same shape on
// DELETE. The hint is absent on every other statement, and the caller sets it
// first so the dump order matches the reference.
func (p *parser) parseLeadingHint() (*Expression, error) {
	if !p.at(TokHINT) {
		return nil, nil
	}
	read, err := p.parseHint(p.curr().Text)
	if err != nil {
		return nil, err
	}
	p.advance()
	return read, nil
}

// parseMySQLProfileTypes reads the comma-separated profile kinds of
// `SHOW PROFILE BLOCK IO, PAGE FAULTS`. A kind is either one word
// (ALL, CPU, IPC, MEMORY, SOURCE, SWAPS) or a pair the reference stores as
// one Var (BLOCK IO, CONTEXT SWITCHES, PAGE FAULTS). An unmatched word ends
// the list and is left for FOR QUERY, OFFSET, or LIMIT.
func (p *parser) parseMySQLProfileTypes() []*Expression {
	var out []*Expression
	for {
		item := p.parseMySQLProfileType()
		if item == nil {
			return out
		}
		out = append(out, item)
		if !p.match(TokCOMMA) {
			return out
		}
	}
}

func (p *parser) parseMySQLProfileType() *Expression {
	c := p.curr()
	if c == nil || c.Type == TokIDENTIFIER {
		return nil
	}
	word := strings.ToUpper(c.Text)
	pair := func(second, recorded string) *Expression {
		n := p.next()
		if n == nil || n.Type == TokIDENTIFIER || !strings.EqualFold(n.Text, second) {
			return nil
		}
		p.advance()
		p.advance()
		return New("Var", Arg{"this", recorded})
	}
	switch word {
	case "ALL", "CPU", "IPC", "MEMORY", "SOURCE", "SWAPS":
		p.advance()
		return New("Var", Arg{"this", word})
	case "BLOCK":
		return pair("IO", "BLOCK IO")
	case "CONTEXT":
		return pair("SWITCHES", "CONTEXT SWITCHES")
	case "PAGE":
		return pair("FAULTS", "PAGE FAULTS")
	}
	return nil
}

// parseAlterOptions reads the properties that follow an ALTER TABLE's
// actions: `ADD COLUMN x INT, ALGORITHM=INPLACE, LOCK=EXCLUSIVE`. The comma
// is still current. Anything that is not a property is left where it stands,
// because a comma there still separates two actions.

// parseMySQLIndexOptions reads what an index may say after its columns:
// KEY_BLOCK_SIZE, USING, WITH PARSER, COMMENT, VISIBLE, INVISIBLE,
// ENGINE_ATTRIBUTE, SECONDARY_ENGINE_ATTRIBUTE. Each one is its own
// IndexConstraintOption, and the loop stops at the first word that is none
// of them.
func (p *parser) parseMySQLIndexOptions() ([]*Expression, error) {
	var out []*Expression
	for {
		var opt *Expression
		switch {
		case p.atWords("KEY_BLOCK_SIZE"):
			p.advance()
			p.match(TokEQ)
			n := p.curr()
			if n == nil || n.Type != TokNUMBER {
				return nil, p.unsupported("KEY_BLOCK_SIZE without a number")
			}
			p.advance()
			opt = New("IndexConstraintOption", Arg{"key_block_size",
				New("Literal", Arg{"this", n.Text}, Arg{"is_string", false})})
		case p.match(TokUSING):
			c := p.curr()
			if c == nil {
				return nil, p.unsupported("USING without an index type")
			}
			p.advance()
			opt = New("IndexConstraintOption", Arg{"using", c.Text})
		case p.atWords("WITH", "PARSER"):
			p.advance()
			p.advance()
			c := p.curr()
			if c == nil || c.Type == TokIDENTIFIER {
				return nil, p.unsupported("WITH PARSER without a name")
			}
			p.advance()
			opt = New("IndexConstraintOption", Arg{"parser",
				New("Var", Arg{"this", c.Text})})
		case p.at(TokCOMMENT):
			p.advance()
			text := p.tryParseStringLiteral()
			if text == nil {
				return nil, p.unsupported("COMMENT without a string")
			}
			opt = New("IndexConstraintOption", Arg{"comment", text})
		case p.atWords("VISIBLE"):
			p.advance()
			opt = New("IndexConstraintOption", Arg{"visible", true})
		case p.atWords("INVISIBLE"):
			p.advance()
			opt = New("IndexConstraintOption", Arg{"visible", false})
		case p.atWords("ENGINE_ATTRIBUTE"):
			p.advance()
			p.match(TokEQ)
			text := p.tryParseStringLiteral()
			if text == nil {
				return nil, p.unsupported("ENGINE_ATTRIBUTE without a string")
			}
			opt = New("IndexConstraintOption", Arg{"engine_attr", text})
		case p.atWords("SECONDARY_ENGINE_ATTRIBUTE"):
			p.advance()
			p.match(TokEQ)
			text := p.tryParseStringLiteral()
			if text == nil {
				return nil, p.unsupported("SECONDARY_ENGINE_ATTRIBUTE without a string")
			}
			opt = New("IndexConstraintOption", Arg{"secondary_engine_attr", text})
		default:
			return out, nil
		}
		out = append(out, opt)
	}
}

// parseMySQLInsertSet reads `INSERT INTO t SET a = 1, b = DEFAULT` and
// records it as the column list plus a VALUES row the reference writes back.
// DEFAULT is the word itself, a Var, because that is what the reference
// stores when the dialect supports it.
func (p *parser) parseMySQLInsertSet(this *Expression) (*Expression, *Expression, error) {
	p.advance() // SET
	var cols, vals []*Expression
	for {
		col, err := p.parseColumn()
		if err != nil {
			return nil, nil, err
		}
		if !p.match(TokEQ) {
			return nil, nil, p.unsupported("INSERT SET without an assignment")
		}
		id := col
		if col.Class == "Column" {
			inner, _ := col.Args["this"].(*Expression)
			if inner == nil {
				return nil, nil, p.unsupported("INSERT SET without a column")
			}
			id = inner
		}
		var value *Expression
		if p.at(TokDEFAULT) || p.atWords("DEFAULT") {
			p.advance()
			value = New("Var", Arg{"this", "DEFAULT"})
		} else {
			value, err = p.parseDisjunction()
			if err != nil {
				return nil, nil, err
			}
		}
		cols = append(cols, id)
		vals = append(vals, value)
		if !p.match(TokCOMMA) {
			break
		}
	}
	table := this
	if this != nil && this.Class == "Schema" {
		if inner, ok := this.Args["this"].(*Expression); ok && inner != nil {
			table = inner
		}
	}
	schema := New("Schema", Arg{"this", table}, Arg{"expressions", cols})
	values := New("Values", Arg{"expressions", []*Expression{
		New("Tuple", Arg{"expressions", vals}),
	}})
	return schema, values, nil
}

// parseMySQLValuesFunc reads `VALUES(a)` inside an expression, the row just
// inserted, which ON DUPLICATE KEY UPDATE refers to. It is an ordinary call
// named VALUES, not the VALUES clause that introduces the inserted rows.
func (p *parser) parseMySQLValuesFunc() (*Expression, error) {
	p.advance() // VALUES
	if !p.match(TokL_PAREN) {
		return nil, p.unsupported("VALUES without parentheses")
	}
	var ids []*Expression
	if !p.at(TokR_PAREN) {
		for {
			id, err := p.parseIdentifier()
			if err != nil {
				return nil, err
			}
			ids = append(ids, id)
			if !p.match(TokCOMMA) {
				break
			}
		}
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed VALUES")
	}
	return New("Anonymous", Arg{"this", "VALUES"}, Arg{"expressions", ids}), nil
}

// parseJSONTable reads `JSON_TABLE(doc, 'path' COLUMNS(...))`. Entered with
// the name current and the opening parenthesis after it. NESTED columns and
// ON EMPTY / ON ERROR handling are refused: neither shape is in the pinned
// statements, and guessing one would build a tree the reference does not.
func (p *parser) parseJSONTable() (*Expression, error) {
	p.advance() // JSON_TABLE
	if !p.match(TokL_PAREN) {
		return nil, p.unsupported("JSON_TABLE without parentheses")
	}
	this, err := p.parseBitwise()
	if err != nil {
		return nil, err
	}
	if p.atWords("FORMAT", "JSON") {
		p.advance()
		p.advance()
		this = New("FormatJson", Arg{"this", this})
	}
	var path *Expression
	if p.match(TokCOMMA) {
		path = p.tryParseStringLiteral()
		if path == nil {
			return nil, p.unsupported("JSON_TABLE without a path")
		}
	}
	errorHandling := p.oracleJSONOn("ERROR")
	emptyHandling := p.oracleJSONOn("EMPTY")
	if p.at(TokDEFAULT) {
		return nil, p.unsupported("JSON_TABLE error handling")
	}
	schema, err := p.parseJSONSchema()
	if err != nil {
		return nil, err
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed JSON_TABLE")
	}
	node := New("JSONTable", Arg{"this", this}, Arg{"schema", schema})
	if path != nil {
		node.Set("path", path)
	}
	if errorHandling != "" {
		node.Set("error_handling", errorHandling)
	}
	if emptyHandling != "" {
		node.Set("empty_handling", emptyHandling)
	}
	return node, nil
}

func (p *parser) parseJSONSchema() (*Expression, error) {
	if !p.atWords("COLUMNS") {
		return nil, p.unsupported("JSON_TABLE without COLUMNS")
	}
	p.advance()
	if !p.match(TokL_PAREN) {
		return nil, p.unsupported("JSON_TABLE COLUMNS without parentheses")
	}
	var cols []*Expression
	if !p.at(TokR_PAREN) {
		for {
			col, err := p.parseJSONColumnDef()
			if err != nil {
				return nil, err
			}
			cols = append(cols, col)
			if !p.match(TokCOMMA) {
				break
			}
		}
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed JSON_TABLE COLUMNS")
	}
	return New("JSONSchema", Arg{"expressions", cols}), nil
}

func (p *parser) parseJSONColumnDef() (*Expression, error) {
	if p.atWords("NESTED") {
		return nil, p.unsupported("JSON_TABLE NESTED column")
	}
	name, err := p.parseIdentifier()
	if err != nil {
		return nil, err
	}
	ordinality := false
	if p.atWords("FOR", "ORDINALITY") {
		p.advance()
		p.advance()
		ordinality = true
	}
	var kind *Expression
	if !p.at(TokCOMMA) && !p.at(TokR_PAREN) && !p.atWords("FORMAT") && !p.atWords("PATH") {
		kind, err = p.parseDataType()
		if err != nil {
			return nil, err
		}
	}
	formatJSON := false
	if p.atWords("FORMAT", "JSON") {
		p.advance()
		p.advance()
		formatJSON = true
	}
	var path any = false
	if p.atWords("PATH") {
		p.advance()
		lit := p.tryParseStringLiteral()
		if lit == nil {
			return nil, p.unsupported("JSON_TABLE PATH without a string")
		}
		path = lit
	}
	node := New("JSONColumnDef", Arg{"this", name})
	if kind != nil {
		node.Set("kind", kind)
	}
	node.Set("path", path)
	node.Set("ordinality", ordinality)
	node.Set("format_json", formatJSON)
	return node, nil
}

// readHintItem reads one hint item. cont is false when a missing comma ends
// the list: MySQL keeps going, because its items are separated by spaces,
// and every other dialect refuses whatever is left.
func (p *parser) readHintItem() (item *Expression, cont bool, err error) {
	if p.curr() == nil {
		return nil, false, nil
	}
	before := p.index
	item, err = p.parseExpression()
	if err != nil {
		return nil, false, err
	}
	if p.index == before {
		return nil, false, p.unsupported("a hint with more than this port reads")
	}
	// A bare WORD is a word rather than a column: `/*+ REBALANCE */`
	// names something to do, and nothing is being selected here.
	if item != nil && item.Class == "Column" {
		if name, ok := bareColumnName(item); ok {
			item = New("Var", Arg{"this", name})
		}
	}
	if p.match(TokCOMMA) || p.dialect == "mysql" {
		return item, p.curr() != nil, nil
	}
	return item, false, nil
}

// withMySQLColumnPrefix wraps `username(16)` as a column prefix. Anywhere
// else the identifier is returned as it was.
func (p *parser) withMySQLColumnPrefix(id *Expression) (*Expression, error) {
	if !isMySQLFamily(p.dialect) || !p.at(TokL_PAREN) {
		return id, nil
	}
	p.advance()
	n := p.curr()
	if n == nil || n.Type != TokNUMBER {
		return nil, p.unsupported("a column prefix that is not a number")
	}
	p.advance()
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed column prefix")
	}
	return New("ColumnPrefix",
		Arg{"this", id},
		Arg{"expression", New("Literal", Arg{"this", n.Text}, Arg{"is_string", false})}), nil
}

// atMySQLAlterOption reports whether the comma still current opens ALTER
// options (`ALGORITHM=INPLACE`) rather than another action. The comma stays
// put either way.
func (p *parser) atMySQLAlterOption() bool {
	if p.dialect != "mysql" || !p.at(TokCOMMA) {
		return false
	}
	mark := p.index
	p.advance()
	_, _, isProp := p.atProperty()
	if !isProp {
		// A key the property table knows no word for is still an option
		// where it is written `key = value`, as KEY_BLOCK_SIZE is.
		_, isProp, _ = p.tryParseKeyValueProperty()
	}
	p.index = mark
	return isProp
}

// parseUnrecognizedAlter is the reference's fallback for an ALTER whose kind
// is not TABLE, VIEW, or INDEX. SESSION is a real Alter this port still
// refuses. Everything else is a command of the raw text.
func (p *parser) parseUnrecognizedAlter(start Token, kind string) (*Expression, error) {
	if kind == "SESSION" {
		return nil, p.unsupported("ALTER " + kind)
	}
	return p.parseAsCommand(start), nil
}

// alterOptions reads MySQL's properties after an ALTER TABLE's actions.
// Every other statement carries an empty list, which is what the node had
// before those properties were read.
func (p *parser) alterOptions(kind string) []*Expression {
	if kind != "TABLE" || !isMySQLFamily(p.dialect) {
		return []*Expression{}
	}
	// The table's properties may also come AFTER the actions, with or
	// without a comma in front of them: `ADD COLUMN c INT
	// SECONDARY_ENGINE_ATTRIBUTE='{}'` is one Alter holding both
	// (Parser._parse_alter, ALTER_TABLE_MIXED_ACTIONS).
	var out []*Expression
	for {
		mark := p.index
		if p.at(TokCOMMA) {
			p.advance()
		}
		props, err := p.parseTableProperties()
		if err != nil || len(props) == 0 {
			p.index = mark
			break
		}
		out = append(out, props...)
	}
	return out
}

func (p *parser) parseCreateDatabase(table *Expression, kind string, replace, refresh, exists bool) (*Expression, error) {
	props, err := p.parseTableProperties()
	if err != nil {
		return nil, err
	}
	if p.curr() != nil {
		return nil, p.unsupported("CREATE " + kind + " with more than a name")
	}
	args := []Arg{
		{"this", table},
		{"kind", kind},
		{"replace", replace},
		{"refresh", refresh},
		{"unique", false},
		{"exists", exists},
	}
	if len(props) > 0 {
		args = append(args, Arg{"properties", New("Properties", Arg{"expressions", props})})
	}
	args = append(args,
		Arg{"indexes", []*Expression{}},
		Arg{"concurrently", false},
	)
	return New("Create", args...), nil
}

// parseMySQLPrimaryKeyName reads `PRIMARY KEY pk_name (` and
// `PRIMARY KEY "pk_name" (`. A double-quoted name is a string token, and the
// reference stores it as a quoted identifier. No name leaves the key unnamed.
func (p *parser) parseMySQLPrimaryKeyName() (*Expression, error) {
	if p.dialect != "mysql" {
		return nil, nil
	}
	c := p.curr()
	n := p.next()
	if c == nil || n == nil || n.Type != TokL_PAREN {
		return nil, nil
	}
	switch c.Type {
	case TokSTRING:
		p.advance()
		return New("Identifier", Arg{"this", c.Text}, Arg{"quoted", true}), nil
	case TokVAR, TokIDENTIFIER:
		return p.parseIdentifier()
	}
	return nil, nil
}

func (p *parser) parseMySQLUsingIndexType() (any, error) {
	if p.dialect != "mysql" || !p.match(TokUSING) {
		return false, nil
	}
	c := p.curr()
	if c == nil {
		return nil, p.unsupported("USING without an index type")
	}
	p.advance()
	return c.Text, nil
}

func (p *parser) parseSQLSecurityProperty() (*Expression, error) {
	p.advance()
	c := p.curr()
	if c == nil {
		return nil, p.unsupported("SQL SECURITY without a mode")
	}
	word := strings.ToUpper(c.Text)
	if word != "DEFINER" && word != "INVOKER" {
		return nil, p.unsupported("SQL SECURITY " + word)
	}
	p.advance()
	return New("SqlSecurityProperty", Arg{"this", word}), nil
}

func (p *parser) parseMySQLShowProfile() (types []*Expression, query, offset, limit *Expression, err error) {
	types = p.parseMySQLProfileTypes()
	if p.matchWords("FOR", "QUERY") {
		query = p.tryParseNumberLiteral()
		if query == nil {
			return nil, nil, nil, nil, p.unsupported("SHOW PROFILE FOR QUERY without a number")
		}
	}
	if p.matchUnquotedWord("OFFSET") {
		offset = p.tryParseNumberLiteral()
		if offset == nil {
			return nil, nil, nil, nil, p.unsupported("SHOW PROFILE OFFSET without a number")
		}
	}
	if p.matchUnquotedWord("LIMIT") {
		limit = p.tryParseNumberLiteral()
		if limit == nil {
			return nil, nil, nil, nil, p.unsupported("SHOW PROFILE LIMIT without a number")
		}
	}
	return types, query, offset, limit, nil
}

func (p *parser) parseMySQLSyntaxFunction(upper string) (*Expression, bool, error) {
	if upper == "JSON_TABLE" && p.dialect == "oracle" {
		fn, err := p.parseJSONTable()
		return fn, true, err
	}
	if p.dialect != "mysql" {
		return nil, false, nil
	}
	switch upper {
	case "JSON_TABLE":
		fn, err := p.parseJSONTable()
		return fn, true, err
	case "VALUES":
		fn, err := p.parseMySQLValuesFunc()
		return fn, true, err
	}
	return nil, false, nil
}

func (p *parser) parseMySQLAutoIncrementAction() (*Expression, error) {
	spec, n, ok := p.atProperty()
	if !ok {
		return nil, p.unsupported("AUTO_INCREMENT")
	}
	for i := 0; i < n; i++ {
		p.advance()
	}
	return p.parseProperty(spec)
}

func (p *parser) parseOnUpdateConstraint() (*Expression, error) {
	p.advance()
	p.advance()
	value, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	return New("OnUpdateColumnConstraint", Arg{"this", value}), nil
}

// parseMySQLUniqueConstraint is MySQL's own `_parse_unique`. Three things
// differ from the reference's base rule: no NULLS flag is recorded, the key
// parts are COLUMNS rather than bare names, and index options may follow the
// list. The index type may be written on either side of the list, and the
// one before it wins.
func (p *parser) parseMySQLUniqueConstraint() (*Expression, error) {
	p.advance() // UNIQUE
	if p.atWords("KEY") || p.atWords("INDEX") {
		p.advance()
	}
	// `_parse_unique_key`: a name, unless the word standing here starts a
	// different constraint (`UNIQUE COMMENT 'x'` names no index) or is the
	// index type.
	var name *Expression
	if !p.at(TokL_PAREN) && !p.at(TokUSING) {
		startsConstraint := false
		if c := p.curr(); c != nil && c.Type != TokIDENTIFIER {
			_, startsConstraint = mysqlConstraintStarts[strings.ToUpper(c.Text)]
		}
		if !startsConstraint && p.atIdentifier() {
			id, err := p.parseIdentifier()
			if err != nil {
				return nil, err
			}
			name = id
		}
	}
	indexType, err := p.parseMySQLIndexTypeAttr()
	if err != nil {
		return nil, err
	}
	if !p.at(TokL_PAREN) {
		return New("UniqueColumnConstraint",
			Arg{"this", name}, Arg{"index_type", indexType}), nil
	}
	parts, err := p.parseMySQLIndexKeyParts()
	if err != nil {
		return nil, err
	}
	schema := New("Schema", Arg{"expressions", parts})
	if name != nil {
		schema = New("Schema", Arg{"this", name}, Arg{"expressions", parts})
	}
	// `index_type or self._parse_index_type()`: only read a second one when
	// the first was not written.
	if indexType == nil {
		if indexType, err = p.parseMySQLIndexTypeAttr(); err != nil {
			return nil, err
		}
	}
	options, err := p.parseMySQLIndexOptions()
	if err != nil {
		return nil, err
	}
	node := New("UniqueColumnConstraint",
		Arg{"this", schema}, Arg{"index_type", indexType})
	if len(options) > 0 {
		node.Set("options", options)
	}
	return node, nil
}

// parseMySQLIndexTypeAttr is `_parse_index_type`: the word after USING, or
// nil when there is none.
func (p *parser) parseMySQLIndexTypeAttr() (any, error) {
	if !p.match(TokUSING) {
		return nil, nil
	}
	c := p.curr()
	if c == nil {
		return nil, p.unsupported("USING without an index type")
	}
	p.advance()
	return c.Text, nil
}

// parseMySQLIndexKeyParts reads an index's parenthesised key parts. A part is
// a column, which may carry a prefix length and an explicit direction
// (MySQLParser._parse_index_key_part).
func (p *parser) parseMySQLIndexKeyParts() ([]*Expression, error) {
	if !p.match(TokL_PAREN) {
		return nil, p.unsupported("an index without its columns")
	}
	var out []*Expression
	for {
		part, err := p.parseMySQLIndexKeyPart()
		if err != nil {
			return nil, err
		}
		out = append(out, part)
		if !p.match(TokCOMMA) {
			break
		}
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed column list")
	}
	return out, nil
}

func (p *parser) parseMySQLIndexKeyPart() (*Expression, error) {
	var part *Expression
	var err error
	// key_part: {col_name [(length)] | (expr)} -- only a prefixed column is
	// followed by "(" (MySQLParser._parse_index_constraint_part).
	if !p.at(TokL_PAREN) && p.next() != nil && p.next().Type == TokL_PAREN {
		id, ierr := p.parseIdentifier()
		if ierr != nil {
			return nil, ierr
		}
		part, err = p.withMySQLColumnPrefix(id)
	} else {
		part, err = p.parseDisjunction()
	}
	if err != nil {
		return nil, err
	}
	desc, hasDirection := false, false
	switch {
	case p.match(TokDESC):
		desc, hasDirection = true, true
	case p.match(TokASC):
		desc, hasDirection = false, true
	}
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
	// Only an EXPLICIT direction keeps the Ordered wrapper: an implicit one
	// would carry this dialect's NULL ordering into another
	// (MySQLParser._parse_index_key_part).
	if !hasDirection {
		return part, nil
	}
	ordered := New("Ordered", Arg{"this", part}, Arg{"desc", desc})
	if !saidSo {
		nullsFirst = p.nullsFirst(desc)
	}
	ordered.Set("nulls_first", nullsFirst)
	return ordered, nil
}

// mysqlConstraintStarts are the words that begin a column constraint rather
// than name an index (MySQL CONSTRAINTS / Parser.CONSTRAINT_PARSERS).
var mysqlConstraintStarts = map[string]struct{}{
	"AUTOINCREMENT": {}, "AUTO_INCREMENT": {}, "BINARY": {}, "BUCKET": {},
	"CASESPECIFIC": {}, "CHECK": {}, "CLUSTERED": {}, "COLLATE": {},
	"COMMENT": {}, "COMPRESS": {}, "DEFAULT": {}, "ENCODE": {},
	"EPHEMERAL": {}, "EXCLUDE": {}, "FOREIGN KEY": {}, "FORMAT": {},
	"FULLTEXT": {}, "GENERATED": {}, "IDENTITY": {}, "INDEX": {},
	"INLINE": {}, "INVISIBLE": {}, "KEY": {}, "LIKE": {},
	"NONCLUSTERED": {}, "NOT": {}, "NULL": {}, "ON": {}, "PATH": {},
	"PERIOD": {}, "PRIMARY KEY": {}, "REFERENCES": {}, "SPATIAL": {},
	"TITLE": {}, "TRUNCATE": {}, "TTL": {}, "UNIQUE": {}, "UPPERCASE": {},
	"WITH": {}, "ZEROFILL": {},
}

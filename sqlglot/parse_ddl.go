package sqlglot

import (
	"strings"
	"unicode"
)

// The statement grammar for things that are not queries.
//
// These were refused wholesale as "not a query" -- 829 of the corpus, and the
// largest single gap left in it. They matter to the guard above this port for
// the same reason a SELECT does: it can only refuse what it can SEE, and a
// CREATE that reads a local file or a TABLE FUNCTION is exactly what it exists
// to notice.

// parseCreate reads `CREATE [OR REPLACE] [TEMPORARY] <kind> [IF NOT EXISTS]
// <name> ...`, in the two shapes the corpus is mostly made of: a column list,
// and a query.
//
// The reference records the kind as a WORD and wraps the name in a Schema when
// there are columns, so `CREATE TABLE t (a INT)` and `CREATE TABLE t AS
// SELECT` differ in what `this` holds, not just in what follows it.
func (p *parser) parseCreate() (*Expression, error) {
	p.advance() // CREATE

	replace := false
	if p.atWords("OR", "REPLACE") {
		p.advance()
		p.advance()
		replace = true
	}
	// TEMPORARY is not a flag on the node: the reference keeps it as a
	// PROPERTY, in a list of them, which is why it needs a node of its own
	// rather than a boolean.
	temporary := false
	if p.atWords("TEMPORARY") || p.atWords("TEMP") {
		p.advance()
		temporary = true
	}
	// UNIQUE is a flag on the node rather than a property, and only an INDEX
	// takes it.
	unique := false
	if p.atWords("UNIQUE") && p.next() != nil && strings.EqualFold(p.next().Text, "INDEX") {
		p.advance()
		unique = true
	}
	// The other modifiers are properties too, and none of them is read here;
	// a statement carrying one is refused rather than built without it.
	for _, word := range []string{"GLOBAL", "LOCAL",
		"MATERIALIZED", "EXTERNAL", "SECURE", "TRANSIENT", "UNIQUE"} {
		if p.atWords(word) {
			return nil, p.unsupported("CREATE " + word)
		}
	}

	kindToken := p.curr()
	if kindToken == nil {
		return nil, p.unsupported("CREATE without a kind")
	}
	kind := strings.ToUpper(kindToken.Text)
	if kind != "TABLE" && kind != "VIEW" && kind != "FUNCTION" && kind != "INDEX" {
		return nil, p.unsupported("CREATE " + kind)
	}
	p.advance()

	if kind == "INDEX" {
		return p.parseIndexRest(replace, unique, temporary)
	}

	exists := false
	if p.atWords("IF", "NOT", "EXISTS") {
		p.advance()
		p.advance()
		p.advance()
		exists = true
	}

	table, err := p.parseTableName()
	if err != nil {
		return nil, err
	}

	if kind == "FUNCTION" {
		return p.parseFunctionRest(table, replace, exists, temporary)
	}

	var this, expression *Expression
	switch {
	case p.at(TokL_PAREN):
		// A TABLE's columns each carry a type; a VIEW's are names the query's
		// results are given, and carry only what is said ABOUT them.
		var columns []*Expression
		var err error
		if kind == "VIEW" {
			columns, err = p.parseViewColumns()
		} else {
			columns, err = p.parseColumnDefs()
		}
		if err != nil {
			return nil, err
		}
		this = New("Schema", Arg{"this", table}, Arg{"expressions", columns})
		// A view may name its columns AND supply the query.
		if p.match(TokALIAS) {
			query, err := p.parseCreateBody()
			if err != nil {
				return nil, err
			}
			expression = query
		}
	case p.match(TokALIAS):
		this = table
		query, err := p.parseCreateBody()
		if err != nil {
			return nil, err
		}
		expression = query
	default:
		return nil, p.unsupported("CREATE " + kind + " without columns or a query")
	}
	if p.curr() != nil {
		return nil, p.unsupported("CREATE " + kind + " with more than this port reads")
	}

	var properties *Expression
	if temporary {
		properties = New("Properties", Arg{"expressions",
			[]*Expression{New("TemporaryProperty")}})
	}

	// Every one of these is ON the node, in this order, whether or not the
	// statement said anything about it: an argument present-and-false is a
	// different tree from one absent, and the reference sets them all.
	return New("Create",
		Arg{"this", this},
		Arg{"kind", kind},
		Arg{"replace", replace},
		Arg{"refresh", false},
		Arg{"unique", false},
		Arg{"expression", expression},
		Arg{"exists", exists},
		Arg{"properties", properties},
		Arg{"indexes", []*Expression{}},
		Arg{"no_schema_binding", nil},
		Arg{"begin", nil},
		Arg{"clone", nil},
		Arg{"concurrently", false},
		Arg{"clustered", nil},
	), nil
}

// parseColumnDefs reads the parenthesised `(a INT, b TEXT)`. Only NAME TYPE is
// read: a constraint, a default, a generated column -- anything that is not
// simply a name and a type -- is refused rather than dropped, because dropping
// one changes what the table IS.
func (p *parser) parseColumnDefs() ([]*Expression, error) {
	p.advance() // the opening parenthesis
	var out []*Expression
	for {
		// A constraint on the TABLE stands where a column definition would,
		// and is told from one by the word it starts with.
		if p.atTableConstraint() {
			constraint, err := p.parseTableConstraint()
			if err != nil {
				return nil, err
			}
			out = append(out, constraint)
			if !p.match(TokCOMMA) {
				break
			}
			continue
		}
		name, err := p.parseIdentifier()
		if err != nil {
			return nil, err
		}
		kind, err := p.parseDataType()
		if err != nil {
			return nil, err
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
			break
		}
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed column list")
	}
	return out, nil
}

// parseTableName reads just the NAME of a table -- `t`, `db.t`, `cat.db.t` --
// and nothing that may follow one. The table parser cannot serve here: it
// reads `t (a INT)` as a call to a table function and `t AS SELECT` as a table
// with an alias, both of which are what a CREATE puts after the name.
func (p *parser) parseTableName() (*Expression, error) {
	var parts []*Expression
	for {
		id, err := p.parseIdentifier()
		if err != nil {
			return nil, err
		}
		parts = append(parts, id)
		if !p.match(TokDOT) {
			break
		}
	}
	if len(parts) > 3 {
		return nil, p.unsupported("an over-qualified table name")
	}
	// Nearest first, as the reference fills them.
	names := []string{"this", "db", "catalog"}
	table := New("Table")
	for i := range parts {
		table.Set(names[i], parts[len(parts)-1-i])
	}
	return table, nil
}

// writeClasses are the statements that CHANGE something. A guard deciding
// whether a statement is read-only asks this rather than asking whether the
// statement parsed: until DDL came into scope the two were the same question,
// because a write could not be read at all.
var writeClasses = map[string]bool{
	"Create":        true,
	"Drop":          true,
	"Insert":        true,
	"Update":        true,
	"Delete":        true,
	"Merge":         true,
	"Alter":         true,
	"AlterTable":    true,
	"TruncateTable": true,
	"Grant":         true,
	"Revoke":        true,
	"Comment":       true,
	"Copy":          true,
	"Analyze":       true,
	"Cache":         true,
	"Uncache":       true,
	"Set":           true,
	"Use":           true,
	"Pragma":        true,
	// A transaction verb changes no rows by itself, but it is not read-only
	// either: what follows it is held open, committed or thrown away. A guard
	// that let one past would be letting the session be steered.
	"Transaction": true,
	"Commit":      true,
	"Rollback":    true,
}

// IsWrite reports whether a parsed statement changes anything.
//
// The guard above this port used to learn that from ErrNotAQuery, which said
// "this is a write" only because a write could not be READ. Now that they can
// be read, the fact has to be asked for, and a caller that keeps using the
// error will see a CREATE go past as though it were a query.
func IsWrite(e *Expression) bool {
	return e != nil && writeClasses[e.Class]
}

// parseInsert reads `INSERT [OVERWRITE] INTO <table> [(cols)] <values-or-query>`.
//
// Eighteen arguments sit on the node whether the statement mentions them or
// not, the same way a Create carries fourteen.
func (p *parser) parseInsert() (*Expression, error) {
	p.advance() // INSERT

	overwrite := false
	if p.atWords("OVERWRITE") {
		p.advance()
		overwrite = true
	}
	// INTO is optional after OVERWRITE, where TABLE takes its place.
	if !p.match(TokINTO) && !p.atWords("TABLE") {
		return nil, p.unsupported("INSERT without INTO")
	}
	if p.atWords("TABLE") {
		p.advance()
	}

	table, err := p.parseTableName()
	if err != nil {
		return nil, err
	}
	this := table
	if p.at(TokL_PAREN) {
		columns, err := p.parseInsertColumns()
		if err != nil {
			return nil, err
		}
		this = New("Schema", Arg{"this", table}, Arg{"expressions", columns})
	}

	var expression *Expression
	switch {
	case p.at(TokVALUES):
		expression, err = p.parseValues()
	case p.at(TokSELECT), p.at(TokWITH):
		expression, err = p.parseQuery()
	default:
		return nil, p.unsupported("INSERT without VALUES or a query")
	}
	if err != nil {
		return nil, err
	}
	if p.curr() != nil {
		return nil, p.unsupported("INSERT with more than this port reads")
	}

	return New("Insert",
		Arg{"hint", nil}, Arg{"is_function", false}, Arg{"this", this},
		Arg{"stored", false}, Arg{"by_name", false}, Arg{"exists", false},
		Arg{"where", nil}, Arg{"using", nil}, Arg{"partition", false},
		Arg{"settings", false}, Arg{"default", false},
		Arg{"expression", expression},
		Arg{"conflict", nil}, Arg{"returning", nil},
		Arg{"overwrite", overwrite}, Arg{"alternative", nil},
		Arg{"ignore", false}, Arg{"source", false},
	), nil
}

// parseInsertColumns reads the `(a, b)` naming which columns are written. They
// are bare identifiers, not the definitions a CREATE takes.
func (p *parser) parseInsertColumns() ([]*Expression, error) {
	p.advance() // the opening parenthesis
	var out []*Expression
	for {
		id, err := p.parseIdentifier()
		if err != nil {
			return nil, err
		}
		// Bare IDENTIFIERS, not columns: nothing here refers to anything, it
		// only names which columns are being written.
		out = append(out, id)
		if !p.match(TokCOMMA) {
			break
		}
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed column list")
	}
	return out, nil
}

// parseValues reads `VALUES (1, 2), (3, 4)` -- a list of rows.
func (p *parser) parseValues() (*Expression, error) {
	p.advance() // VALUES
	var rows []*Expression
	for {
		if !p.at(TokL_PAREN) {
			return nil, p.unsupported("a VALUES row that is not parenthesised")
		}
		row, err := p.parseParenthesisedList()
		if err != nil {
			return nil, err
		}
		// `VALUES (DEFAULT)` names the column's default rather than referring
		// to anything, and the reference keeps the WORD as a Var.
		for i, member := range row {
			if member.Class == "Column" && strings.EqualFold(member.Name(), "DEFAULT") {
				row[i] = New("Var", Arg{"this", strings.ToUpper(member.Name())})
			}
		}
		rows = append(rows, New("Tuple", Arg{"expressions", row}))
		if !p.match(TokCOMMA) {
			break
		}
	}
	return New("Values", Arg{"expressions", rows}), nil
}

// parseDrop reads `DROP <kind> [IF EXISTS] <name>`. The names go in a LIST,
// because some dialects drop several at once.
func (p *parser) parseDrop() (*Expression, error) {
	p.advance() // DROP

	kindToken := p.curr()
	if kindToken == nil {
		return nil, p.unsupported("DROP without a kind")
	}
	kind := strings.ToUpper(kindToken.Text)
	if kind != "TABLE" && kind != "VIEW" {
		return nil, p.unsupported("DROP " + kind)
	}
	p.advance()

	exists := false
	if p.atWords("IF", "EXISTS") {
		p.advance()
		p.advance()
		exists = true
	}
	table, err := p.parseTableName()
	if err != nil {
		return nil, err
	}
	if p.curr() != nil {
		return nil, p.unsupported("DROP with more than this port reads")
	}
	return New("Drop",
		Arg{"exists", exists},
		Arg{"tables", []*Expression{table}},
		Arg{"expressions", nil},
		Arg{"kind", kind},
		Arg{"temporary", false}, Arg{"materialized", false},
		Arg{"cascade", false}, Arg{"restrict", false},
		Arg{"constraints", false}, Arg{"purge", false},
		Arg{"cluster", nil}, Arg{"concurrently", false},
		Arg{"sync", false}, Arg{"iceberg", false}, Arg{"force", false},
	), nil
}

// parseColumnConstraints reads what may follow a column's type. Each is a
// ColumnConstraint wrapping a node of its own kind, which is how the reference
// keeps them: the wrapper is uniform and the kind carries the meaning.
//
// What is not here is refused rather than skipped. A GENERATED column, a
// REFERENCES, a named CONSTRAINT -- each says something about the table that
// dropping it would lose.
func (p *parser) parseColumnConstraints() ([]*Expression, error) {
	var out []*Expression
	for {
		var kind *Expression
		switch {
		case p.atWords("NOT", "NULL"):
			p.advance()
			p.advance()
			kind = New("NotNullColumnConstraint")
		case p.at(TokNULL):
			p.advance()
			kind = New("NotNullColumnConstraint", Arg{"allow_null", true})
		case p.atWords("DEFAULT"):
			p.advance()
			value, err := p.parseUnary()
			if err != nil {
				return nil, err
			}
			kind = New("DefaultColumnConstraint", Arg{"this", value})
		case p.atWords("PRIMARY KEY"):
			// One TOKEN, not two words: the tokenizer joins them.
			p.advance()
			kind = New("PrimaryKeyColumnConstraint")
			// `PRIMARY KEY ASC` records the direction; without a word the
			// argument is left off rather than set false.
			switch {
			case p.atWords("ASC"):
				p.advance()
				kind.Set("desc", false)
			case p.atWords("DESC"):
				p.advance()
				kind.Set("desc", true)
			}
		case p.atWords("UNIQUE"):
			p.advance()
			kind = New("UniqueColumnConstraint",
				Arg{"nulls", false}, Arg{"index_type", false})
		case p.atWords("AUTO_INCREMENT"), p.atWords("AUTOINCREMENT"):
			p.advance()
			kind = New("AutoIncrementColumnConstraint")
		case p.atWords("COMMENT"):
			p.advance()
			c := p.curr()
			if c == nil || c.Type != TokSTRING {
				return nil, p.unsupported("COMMENT without a string")
			}
			p.advance()
			kind = New("CommentColumnConstraint",
				Arg{"this", New("Literal", Arg{"this", c.Text}, Arg{"is_string", true})})
		case p.atWords("GENERATED"):
			p.advance()
			generated, err := p.parseGenerated()
			if err != nil {
				return nil, err
			}
			kind = generated
		case p.atWords("CHECK"):
			p.advance()
			if !p.match(TokL_PAREN) {
				return nil, p.unsupported("CHECK without a condition")
			}
			condition, err := p.parseExpression()
			if err != nil {
				return nil, err
			}
			if !p.match(TokR_PAREN) {
				return nil, p.unsupported("unclosed CHECK")
			}
			kind = New("CheckColumnConstraint",
				Arg{"this", condition}, Arg{"enforced", false})
		case p.at(TokREFERENCES):
			p.advance()
			target, err := p.parseTableName()
			if err != nil {
				return nil, err
			}
			this := target
			if p.at(TokL_PAREN) {
				columns, err := p.parseInsertColumns()
				if err != nil {
					return nil, err
				}
				// The referenced COLUMNS wrap the table in a Schema, the same
				// shape a CREATE gives a table with a column list. Reference's
				// own `expressions` stays empty; the reference fills the
				// Schema, not the constraint.
				this = New("Schema", Arg{"this", target}, Arg{"expressions", columns})
			}
			options, err := p.parseKeyConstraintOptions()
			if err != nil {
				return nil, err
			}
			kind = New("Reference", Arg{"this", this})
			if len(options) > 0 {
				kind.Set("options", options)
			}
		case p.at(TokCOLLATE):
			p.advance()
			// A QUOTED collation is an Identifier and a bare one a Column --
			// the same split COLLATE has as an operator, and the reason it
			// needed a reader of its own there too.
			c := p.curr()
			if c == nil {
				return nil, p.unsupported("COLLATE without a collation")
			}
			if c.Type == TokIDENTIFIER {
				p.advance()
				kind = New("CollateColumnConstraint",
					Arg{"this", New("Identifier", Arg{"this", c.Text}, Arg{"quoted", true})})
				break
			}
			name, err := p.parseColumn()
			if err != nil {
				return nil, err
			}
			kind = New("CollateColumnConstraint", Arg{"this", name})
		case p.at(TokCONSTRAINT):
			// A NAMED constraint: the name goes on the wrapper and the kind
			// that follows goes where an unnamed one's would. Read here rather
			// than in the loop below so the name is set BEFORE the kind, which
			// is the order the reference assigns them in.
			p.advance()
			name, err := p.parseIdentifier()
			if err != nil {
				return nil, err
			}
			inner, err := p.parseColumnConstraints()
			if err != nil {
				return nil, err
			}
			if len(inner) != 1 {
				return nil, p.unsupported("a named constraint that is not one thing")
			}
			named := New("ColumnConstraint", Arg{"this", name})
			named.Set("kind", inner[0].Args["kind"])
			return append(out, named), nil
		default:
			// The list ends at a comma, at the closing parenthesis, or at the
			// end of the statement -- an ALTER TABLE ADD COLUMN has neither of
			// the first two. Anything ELSE is a constraint this port cannot
			// read, and is refused rather than skipped.
			if p.curr() != nil && !p.at(TokCOMMA) && !p.at(TokR_PAREN) {
				return nil, p.unsupported("a column constraint this port does not read")
			}
			return out, nil
		}
		out = append(out, New("ColumnConstraint", Arg{"kind", kind}))
	}
}

// parseAlter reads `ALTER TABLE [IF EXISTS] <name> <action>`, in the three
// shapes the corpus mostly holds: adding a column, dropping one, and renaming
// the table.
//
// The actions are a LIST, because some dialects take several at once, and each
// is a node of its own -- an ADD is the very ColumnDef a CREATE builds.
func (p *parser) parseAlter() (*Expression, error) {
	p.advance() // ALTER

	kindToken := p.curr()
	if kindToken == nil {
		return nil, p.unsupported("ALTER without a kind")
	}
	kind := strings.ToUpper(kindToken.Text)
	if kind != "TABLE" && kind != "VIEW" {
		return nil, p.unsupported("ALTER " + kind)
	}
	p.advance()

	exists := false
	if p.atWords("IF", "EXISTS") {
		p.advance()
		p.advance()
		exists = true
	}
	table, err := p.parseTableName()
	if err != nil {
		return nil, err
	}

	var actions []*Expression
	if kind == "VIEW" {
		// A view is altered by being GIVEN a new query, and the query itself
		// is the action.
		if !p.match(TokALIAS) {
			return nil, p.unsupported("ALTER VIEW without a query")
		}
		query, err := p.parseQuery()
		if err != nil {
			return nil, err
		}
		actions = []*Expression{query}
	} else {
		actions, err = p.parseAlterActions()
		if err != nil {
			return nil, err
		}
	}
	if p.curr() != nil {
		return nil, p.unsupported("ALTER with more than this port reads")
	}
	return New("Alter",
		Arg{"this", table},
		Arg{"kind", kind},
		Arg{"exists", exists},
		Arg{"actions", actions},
		Arg{"only", false},
		Arg{"options", []*Expression{}},
		Arg{"cluster", nil},
		Arg{"not_valid", false},
		Arg{"check", false},
		Arg{"cascade", false},
		Arg{"iceberg", false},
	), nil
}

// parseAlterActions reads everything this ALTER does, comma-separated.
//
// The commas are not always between whole actions: T-SQL writes `ADD a INT,
// b INT`, where only the first says ADD and the rest continue it. So an item
// that does not begin with an action word carries on the one before it.
func (p *parser) parseAlterActions() ([]*Expression, error) {
	var actions []*Expression
	for {
		var action *Expression
		var err error
		if len(actions) > 0 && !p.atAlterActionWord() {
			if actions[len(actions)-1].Class != "ColumnDef" {
				return nil, p.unsupported("an ALTER TABLE action with no verb")
			}
			action, err = p.parseAddedColumn(false)
		} else {
			action, err = p.parseAlterAction()
		}
		if err != nil {
			return nil, err
		}
		actions = append(actions, action)
		if !p.match(TokCOMMA) {
			return actions, nil
		}
	}
}

// atAlterActionWord reports whether a word that begins an action is current.
func (p *parser) atAlterActionWord() bool {
	return p.at(TokDROP) || p.at(TokALTER) ||
		p.atWords("ADD") || p.atWords("RENAME")
}

// parseAlterAction reads one thing this ALTER does.
func (p *parser) parseAlterAction() (*Expression, error) {
	switch {
	case p.atWords("ADD"):
		p.advance()
		// A constraint added to the table is wrapped in an AddConstraint,
		// which holds a LIST of them; a column is not wrapped at all.
		if p.atTableConstraint() {
			constraint, err := p.parseTableConstraint()
			if err != nil {
				return nil, err
			}
			return New("AddConstraint",
				Arg{"expressions", []*Expression{constraint}}), nil
		}
		// The word COLUMN is optional: T-SQL writes it nowhere and reads it
		// anywhere.
		if p.atWords("COLUMN") {
			p.advance()
		}
		exists := false
		if p.atWords("IF", "NOT", "EXISTS") {
			p.advance()
			p.advance()
			p.advance()
			exists = true
		}
		return p.parseAddedColumn(exists)
	case p.at(TokDROP):
		p.advance()
		if p.atWords("COLUMN") {
			p.advance()
		}
		exists := false
		if p.atWords("IF", "EXISTS") {
			p.advance()
			p.advance()
			exists = true
		}
		name, err := p.parseColumn()
		if err != nil {
			return nil, err
		}
		cascade, restrict := false, false
		switch {
		case p.atWords("CASCADE"):
			p.advance()
			cascade = true
		case p.atWords("RESTRICT"):
			p.advance()
			restrict = true
		}
		return New("Drop",
			Arg{"exists", exists},
			Arg{"tables", []*Expression{name}},
			Arg{"expressions", nil},
			Arg{"kind", "COLUMN"},
			Arg{"temporary", false}, Arg{"materialized", false},
			Arg{"cascade", cascade}, Arg{"restrict", restrict},
			Arg{"constraints", false}, Arg{"purge", false},
			Arg{"cluster", nil}, Arg{"concurrently", false},
			Arg{"sync", false}, Arg{"iceberg", false}, Arg{"force", false},
		), nil
	case p.atWords("RENAME", "TO"):
		p.advance()
		p.advance()
		target, err := p.parseTableName()
		if err != nil {
			return nil, err
		}
		return New("AlterRename", Arg{"this", target}), nil
	case p.atWords("RENAME", "COLUMN"):
		p.advance()
		p.advance()
		exists := false
		if p.atWords("IF", "EXISTS") {
			p.advance()
			p.advance()
			exists = true
		}
		from, err := p.parseColumn()
		if err != nil {
			return nil, err
		}
		if !p.atWords("TO") {
			return nil, p.unsupported("RENAME COLUMN without TO")
		}
		p.advance()
		to, err := p.parseColumn()
		if err != nil {
			return nil, err
		}
		return New("RenameColumn",
			Arg{"this", from}, Arg{"to", to}, Arg{"exists", exists}), nil
	case p.at(TokALTER):
		p.advance()
		if p.atWords("COLUMN") {
			p.advance()
		}
		return p.parseAlteredColumn()
	}
	return nil, p.unsupported("an ALTER TABLE action this port does not read")
}

// parseAddedColumn reads one column definition an ALTER adds.
//
// The definition carries one argument the same definition inside a CREATE
// does not: whether the column had to be absent.
func (p *parser) parseAddedColumn(exists bool) (*Expression, error) {
	name, err := p.parseIdentifier()
	if err != nil {
		return nil, err
	}
	kind, err := p.parseDataType()
	if err != nil {
		return nil, err
	}
	constraints, err := p.parseColumnConstraints()
	if err != nil {
		return nil, err
	}
	if constraints == nil {
		constraints = []*Expression{}
	}
	return New("ColumnDef",
		Arg{"this", name}, Arg{"kind", kind},
		Arg{"constraints", constraints},
		Arg{"position", nil},
		Arg{"exists", exists}), nil
}

// parseAlteredColumn reads what an `ALTER COLUMN` says about one column: a new
// type, a new default, the removal of one, or a comment. Each lands in a slot
// of its own on the node rather than in a shared one.
func (p *parser) parseAlteredColumn() (*Expression, error) {
	name, err := p.parseIdentifier()
	if err != nil {
		return nil, err
	}
	action := New("AlterColumn", Arg{"this", name})
	switch {
	case p.atWords("SET", "DATA", "TYPE"), p.atWords("TYPE"):
		if p.atWords("TYPE") {
			p.advance()
		} else {
			p.advance()
			p.advance()
			p.advance()
		}
		kind, err := p.parseDataType()
		if err != nil {
			return nil, err
		}
		action.Set("dtype", kind)
		// Both are on the node whenever a type is, whether or not the
		// statement said anything about them.
		action.Set("collate", false)
		if p.at(TokUSING) {
			p.advance()
			using, err := p.parseExpression()
			if err != nil {
				return nil, err
			}
			action.Set("using", using)
		} else {
			action.Set("using", false)
		}
	case p.atWords("SET", "DEFAULT"):
		p.advance()
		p.advance()
		value, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		action.Set("default", value)
	case p.at(TokDROP) && p.next() != nil && strings.EqualFold(p.next().Text, "DEFAULT"):
		p.advance()
		p.advance()
		action.Set("drop", true)
	case p.atWords("COMMENT"):
		p.advance()
		c := p.curr()
		if c == nil || c.Type != TokSTRING {
			return nil, p.unsupported("COMMENT without a string")
		}
		p.advance()
		action.Set("comment",
			New("Literal", Arg{"this", c.Text}, Arg{"is_string", true}))
	default:
		return nil, p.unsupported("an ALTER COLUMN action this port does not read")
	}
	return action, nil
}

// parseKeyConstraintOptions reads what may follow a REFERENCES: `ON DELETE
// CASCADE`, `ON UPDATE SET NULL`, and the rest of that small vocabulary.
//
// The reference keeps each as a STRING rather than as a node, so the phrase is
// rebuilt here in the spelling it keeps -- upper-cased, one option per entry.
// Anything outside the vocabulary is refused rather than guessed at: an
// unrecognised word after ON is the difference between deleting the children
// and refusing to.
func (p *parser) parseKeyConstraintOptions() ([]string, error) {
	var options []string
	for p.at(TokON) {
		p.advance()
		event := p.curr()
		if event == nil {
			return nil, p.unsupported("ON without an event")
		}
		p.advance()
		var action string
		switch {
		case p.atWords("NO", "ACTION"):
			p.advance()
			p.advance()
			action = "NO ACTION"
		case p.atWords("CASCADE"):
			p.advance()
			action = "CASCADE"
		case p.atWords("RESTRICT"):
			p.advance()
			action = "RESTRICT"
		case p.at(TokSET) && p.next() != nil && p.next().Type == TokNULL:
			p.advance()
			p.advance()
			action = "SET NULL"
		case p.at(TokSET) && p.next() != nil && strings.EqualFold(p.next().Text, "DEFAULT"):
			p.advance()
			p.advance()
			action = "SET DEFAULT"
		default:
			return nil, p.unsupported("a key constraint action this port does not read")
		}
		// The EVENT keeps the case it was written in; only the action is
		// spelled by the table above.
		options = append(options, "ON "+event.Text+" "+action)
	}
	return options, nil
}

// parseViewColumns reads the `(a, b COMMENT 'b')` a view may name its results
// with.
//
// Unlike a table's, these have no types, and the two spellings build different
// nodes: a bare name is an Identifier and a name with something said about it
// is a ColumnDef with no kind at all.
func (p *parser) parseViewColumns() ([]*Expression, error) {
	p.advance() // the opening parenthesis
	var out []*Expression
	for {
		name, err := p.parseIdentifier()
		if err != nil {
			return nil, err
		}
		constraints, err := p.parseColumnConstraints()
		if err != nil {
			return nil, err
		}
		if len(constraints) > 0 {
			out = append(out, New("ColumnDef",
				Arg{"this", name}, Arg{"constraints", constraints}))
		} else {
			out = append(out, name)
		}
		if !p.match(TokCOMMA) {
			break
		}
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed column list")
	}
	return out, nil
}

// atTableConstraint reports whether a constraint on the TABLE starts here
// rather than a column definition. A column is a name followed by a type; each
// of these is a keyword the reference reserves in this position.
func (p *parser) atTableConstraint() bool {
	return p.at(TokCONSTRAINT) || p.at(TokPRIMARY_KEY) ||
		p.at(TokFOREIGN_KEY) || p.atWords("UNIQUE") || p.atWords("CHECK")
}

// parseTableConstraint reads one constraint on the table as a whole.
//
// A NAMED one is a Constraint holding a LIST of the kinds it names, which is
// a shape of its own rather than the wrapper a named COLUMN constraint uses --
// the two look alike in the text and are different nodes.
func (p *parser) parseTableConstraint() (*Expression, error) {
	if p.at(TokCONSTRAINT) {
		p.advance()
		name, err := p.parseIdentifier()
		if err != nil {
			return nil, err
		}
		kind, err := p.parseTableConstraintKind()
		if err != nil {
			return nil, err
		}
		return New("Constraint",
			Arg{"this", name},
			Arg{"expressions", []*Expression{kind}}), nil
	}
	return p.parseTableConstraintKind()
}

// parseTableConstraintKind reads the constraint itself, named or not.
func (p *parser) parseTableConstraintKind() (*Expression, error) {
	switch {
	case p.at(TokPRIMARY_KEY):
		p.advance()
		members, err := p.parseKeyColumns()
		if err != nil {
			return nil, err
		}
		key := New("PrimaryKey", Arg{"expressions", members})
		// The parameters are on the node whether or not anything was said
		// about them, holding only the flag that says so.
		key.Set("include", New("IndexParameters", Arg{"with_storage", false}))
		return key, nil
	case p.at(TokFOREIGN_KEY):
		p.advance()
		columns, err := p.parseInsertColumns()
		if err != nil {
			return nil, err
		}
		if !p.at(TokREFERENCES) {
			return nil, p.unsupported("FOREIGN KEY without REFERENCES")
		}
		reference, err := p.parseColumnConstraints()
		if err != nil {
			return nil, err
		}
		if len(reference) != 1 {
			return nil, p.unsupported("FOREIGN KEY with more than a reference")
		}
		return New("ForeignKey",
			Arg{"expressions", columns},
			Arg{"reference", reference[0].Args["kind"]}), nil
	case p.atWords("UNIQUE"):
		p.advance()
		columns, err := p.parseInsertColumns()
		if err != nil {
			return nil, err
		}
		// The arguments are in the order the reference assigns them, which is
		// not the order they are written in.
		return New("UniqueColumnConstraint",
			Arg{"nulls", false},
			Arg{"this", New("Schema", Arg{"expressions", columns})},
			Arg{"index_type", false}), nil
	case p.atWords("CHECK"):
		p.advance()
		if !p.match(TokL_PAREN) {
			return nil, p.unsupported("CHECK without a condition")
		}
		condition, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		if !p.match(TokR_PAREN) {
			return nil, p.unsupported("unclosed CHECK")
		}
		return New("CheckColumnConstraint",
			Arg{"this", condition}, Arg{"enforced", false}), nil
	}
	return nil, p.unsupported("a table constraint this port does not read")
}

// parseKeyColumns reads the `(a, b)` a key is over.
//
// T-SQL reads them the way it reads an index -- each may carry a direction --
// so a member is an Ordered over a Column there and a bare Identifier
// everywhere else. Same statement, two shapes, and the dialect decides.
func (p *parser) parseKeyColumns() ([]*Expression, error) {
	if !p.tables.PrimaryKeyMembersOrdered {
		return p.parseInsertColumns()
	}
	if !p.match(TokL_PAREN) {
		return nil, p.unsupported("a key without its columns")
	}
	var out []*Expression
	for {
		column, err := p.parseColumn()
		if err != nil {
			return nil, err
		}
		member := New("Ordered", Arg{"this", column})
		desc := false
		switch {
		case p.atWords("DESC"):
			p.advance()
			desc = true
			member.Set("desc", true)
		case p.atWords("ASC"):
			p.advance()
			member.Set("desc", false)
		}
		member.Set("nulls_first", p.nullsFirst(desc))
		out = append(out, member)
		if !p.match(TokCOMMA) {
			break
		}
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed key column list")
	}
	return out, nil
}

// parseFunctionRest reads everything after `CREATE FUNCTION <name>`.
//
// A function is the one CREATE whose parts are not in a fixed order: the
// properties may come before the body, after it, or on both sides, and the
// reference keeps them in the order they were WRITTEN rather than in the order
// it knows them by. So there is one loop, and each turn of it reads whichever
// part is next.
func (p *parser) parseFunctionRest(name *Expression, replace, exists, temporary bool) (*Expression, error) {
	this := name
	if p.at(TokL_PAREN) {
		params, err := p.parseFunctionParams()
		if err != nil {
			return nil, err
		}
		udf := New("UserDefinedFunction", Arg{"this", name})
		if len(params) > 0 {
			udf.Set("expressions", params)
		}
		// Always true here: a function written with parentheses is what this
		// node is for, and one written without them never reaches it.
		udf.Set("wrapped", true)
		this = udf
	}

	var properties []*Expression
	if temporary {
		properties = append(properties, New("TemporaryProperty"))
	}
	var expression *Expression
	for {
		property, err := p.parseFunctionProperty()
		if err != nil {
			return nil, err
		}
		if property != nil {
			properties = append(properties, property)
			continue
		}
		body, returns, err := p.parseFunctionBody()
		if err != nil {
			return nil, err
		}
		if body == nil {
			break
		}
		if expression != nil {
			return nil, p.unsupported("a function with two bodies")
		}
		expression = body
		if returns != nil {
			properties = append(properties, returns)
		}
	}
	if p.curr() != nil {
		return nil, p.unsupported("CREATE FUNCTION with more than this port reads")
	}

	var props *Expression
	if len(properties) > 0 {
		props = New("Properties", Arg{"expressions", properties})
	}
	var begin any = false
	if expression != nil && expression.Class == "Heredoc" {
		begin = nil
	}
	return New("Create",
		Arg{"this", this},
		Arg{"kind", "FUNCTION"},
		Arg{"replace", replace},
		Arg{"refresh", false},
		Arg{"unique", false},
		Arg{"expression", expression},
		Arg{"exists", exists},
		Arg{"properties", props},
		Arg{"indexes", []*Expression{}},
		Arg{"no_schema_binding", nil},
		// A function carries this where a table does not: the reference sets
		// it false rather than leaving it off -- except when the body is a
		// heredoc, where it never gets as far as setting it.
		Arg{"begin", begin},
		Arg{"clone", nil},
		Arg{"concurrently", false},
		Arg{"clustered", nil},
	), nil
}

// parseFunctionParams reads the parenthesised parameter list.
//
// Two spellings live in it. `add(INT, INT)` names no parameters at all and the
// reference keeps each TYPE as a bare Identifier; `add(a INT)` names one and
// builds a ColumnDef. Which it is depends on whether anything follows the word.
func (p *parser) parseFunctionParams() ([]*Expression, error) {
	p.advance() // the opening parenthesis
	var out []*Expression
	if p.at(TokR_PAREN) {
		p.advance()
		return out, nil
	}
	for {
		param, err := p.parseFunctionParam()
		if err != nil {
			return nil, err
		}
		out = append(out, param)
		if !p.match(TokCOMMA) {
			break
		}
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed parameter list")
	}
	return out, nil
}

// parseFunctionParam reads one parameter.
func (p *parser) parseFunctionParam() (*Expression, error) {
	mode, wasModeWord := p.parseParameterMode()
	var name *Expression
	if named := p.parseParameterName(); named != nil {
		// T-SQL names a function's parameters the way it names variables, and
		// the reference keeps the marker: `@bar` is a Parameter, not an
		// identifier that happens to start with a symbol.
		name = named
	} else if mode == nil && wasModeWord {
		// The word turned out to be the parameter's NAME -- `foo(variadic
		// INT[])` declares one called variadic -- and the tokenizer gives it a
		// keyword's token, which is not one an identifier may usually take.
		c := p.curr()
		p.advance()
		name = New("Identifier", Arg{"this", c.Text}, Arg{"quoted", false})
	} else {
		id, err := p.parseIdentifier()
		if err != nil {
			return nil, err
		}
		name = id
	}
	// Nothing after the name means the name WAS the type: `add(INT, INT)`
	// declares two unnamed parameters, and the reference keeps each as a bare
	// Identifier rather than as a definition with no name.
	if p.at(TokCOMMA) || p.at(TokR_PAREN) {
		return name, nil
	}
	kind, err := p.parseDataType()
	if err != nil {
		return nil, err
	}
	def := New("ColumnDef", Arg{"this", name}, Arg{"kind", kind})
	var constraints []*Expression
	if mode != nil {
		// The mode goes in the constraint list UNWRAPPED, unlike everything
		// else that lands there.
		constraints = append(constraints, mode)
	}
	rest, err := p.parseColumnConstraints()
	if err != nil {
		return nil, err
	}
	constraints = append(constraints, rest...)
	if len(constraints) > 0 {
		def.Set("constraints", constraints)
	}
	return def, nil
}

// parseParameterMode reads `IN`, `OUT`, `INOUT` or `VARIADIC` when one of them
// is a MODE rather than a name.
//
// `foo(out INT)` declares a parameter called `out` of type INT; `foo(OUT b
// INT)` declares an output parameter called `b`. The word is a mode only when
// a name AND a type follow it, so the decision needs a look at what comes
// after -- and the port rewinds when the guess is wrong.
func (p *parser) parseParameterMode() (mode *Expression, wasModeWord bool) {
	input, output, variadic := false, false, false
	switch {
	case p.atWords("INOUT"):
		input, output = true, true
	case p.atWords("IN"):
		input = true
	case p.atWords("OUT"):
		output = true
	case p.at(TokVARIADIC), p.atWords("VARIADIC"):
		variadic = true
	default:
		return nil, false
	}
	mark := p.index
	p.advance()
	// The word is a mode only if a NAME and a TYPE both follow it. Looking
	// for a name and "something after it" is not enough: `variadic INT[]`
	// declares a parameter CALLED variadic, and the `[` after INT would pass
	// that weaker test while the whole thing is one type, not two things. So
	// the rest is parsed for real and the position put back if it fails --
	// and nothing is asked about what comes AFTER the type, because a
	// constraint may follow one: `OUT a INT NOT NULL` is still a mode.
	after := p.index
	if _, err := p.parseIdentifier(); err != nil {
		p.index = mark
		return nil, true
	}
	if _, err := p.parseDataType(); err != nil {
		p.index = mark
		return nil, true
	}
	p.index = after
	return New("InOutColumnConstraint",
		Arg{"input_", input}, Arg{"output", output}, Arg{"variadic", variadic}), true
}

// parseFunctionProperty reads one of the words a function may be described by,
// or nil when none is here.
func (p *parser) parseFunctionProperty() (*Expression, error) {
	switch {
	case p.at(TokLANGUAGE) || p.atWords("LANGUAGE"):
		p.advance()
		c := p.curr()
		if c == nil {
			return nil, p.unsupported("LANGUAGE without a language")
		}
		p.advance()
		// The name keeps the case it was written in.
		return New("LanguageProperty",
			Arg{"this", New("Var", Arg{"this", c.Text})}), nil
	case p.atWords("RETURNS", "NULL", "ON", "NULL", "INPUT"):
		for range 5 {
			p.advance()
		}
		// The reference records this as a second RETURNS property rather than
		// as a kind of its own.
		return New("ReturnsProperty", Arg{"is_table", false}, Arg{"null", true}), nil
	case p.atWords("RETURNS"):
		p.advance()
		return p.parseReturnsProperty()
	case p.atWords("IMMUTABLE"), p.atWords("STABLE"), p.atWords("VOLATILE"):
		word := strings.ToUpper(p.curr().Text)
		p.advance()
		return New("StabilityProperty",
			Arg{"this", New("Literal", Arg{"this", word}, Arg{"is_string", true})}), nil
	case p.atWords("STRICT"):
		p.advance()
		return New("StrictProperty"), nil
	case p.atWords("CALLED", "ON", "NULL", "INPUT"):
		for range 4 {
			p.advance()
		}
		return New("CalledOnNullInputProperty"), nil
	case p.atWords("READS", "SQL", "DATA"):
		for range 3 {
			p.advance()
		}
		return New("SqlReadWriteProperty", Arg{"this", "READS SQL DATA"}), nil
	case p.atWords("MODIFIES", "SQL", "DATA"):
		for range 3 {
			p.advance()
		}
		return New("SqlReadWriteProperty", Arg{"this", "MODIFIES SQL DATA"}), nil
	case p.atWords("CONTAINS", "SQL"):
		p.advance()
		p.advance()
		return New("SqlReadWriteProperty", Arg{"this", "CONTAINS SQL"}), nil
	case p.at(TokSET):
		p.advance()
		name, err := p.parseColumn()
		if err != nil {
			return nil, err
		}
		// `TO` and `=` are the same thing here and the reference keeps neither
		// -- the item is an equality either way, and the dialect decides how
		// it is spelled back.
		if !p.match(TokALIAS) && !p.atWords("TO") && !p.at(TokEQ) {
			// `SET foo FROM CURRENT` takes its value from the session; the
			// reference gives up on it and keeps the raw text, which is not
			// a tree this port can build.
			return nil, p.unsupported("a function SET this port does not read")
		}
		if p.atWords("TO") || p.at(TokEQ) {
			p.advance()
		}
		value, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		if p.curr() != nil {
			// The reference reads a SET as a setting only when it ENDS the
			// statement; with anything after it, it gives up and swallows the
			// rest as raw text. That is not a tree this port builds.
			return nil, p.unsupported("a function SET with more after it")
		}
		item := New("SetItem", Arg{"this",
			New("EQ", Arg{"this", name}, Arg{"expression", value})})
		return New("SetConfigProperty", Arg{"this", New("Set",
			Arg{"expressions", []*Expression{item}},
			Arg{"unset", false}, Arg{"tag", false})}), nil
	}
	return nil, nil
}

// parseReturnsProperty reads what follows RETURNS: a type, the word TABLE, or
// TABLE with the columns it returns.
func (p *parser) parseReturnsProperty() (*Expression, error) {
	// T-SQL NAMES the table a function returns -- `RETURNS @foo TABLE (...)`
	// -- and the name lands on this property rather than on the schema, after
	// the flag that says there is one.
	named := p.parseParameterName()
	if named != nil && !p.atWords("TABLE") {
		return nil, p.unsupported("a named RETURNS that is not a table")
	}
	if !p.atWords("TABLE") {
		kind, err := p.parseDataType()
		if err != nil {
			return nil, err
		}
		return New("ReturnsProperty", Arg{"this", kind}, Arg{"is_table", false}), nil
	}
	p.advance()
	// Bare TABLE is a WORD, not a schema: the shape is what the writer looks
	// at, so the two cannot be merged.
	this := New("Var", Arg{"this", "TABLE"})
	property := New("ReturnsProperty")
	if p.at(TokL_PAREN) {
		columns, err := p.parseColumnDefs()
		if err != nil {
			return nil, err
		}
		property.Set("this", New("Schema",
			Arg{"this", this}, Arg{"expressions", columns}))
	} else {
		property.Set("this", this)
	}
	property.Set("is_table", true)
	if named != nil {
		property.Set("table", named)
	}
	return property, nil
}

// parseFunctionBody reads what the function DOES, and -- for the one dialect
// that spells a return type there -- the property that came with it.
//
// Returns (nil, nil, nil) when no body starts here.
func (p *parser) parseFunctionBody() (body, returns *Expression, err error) {
	switch {
	case p.atWords("RETURN"):
		p.advance()
		inner, err := p.parseReturnBody()
		if err != nil {
			return nil, nil, err
		}
		return New("Return", Arg{"this", inner}), nil, nil
	case p.match(TokALIAS):
		// `AS TABLE <query>` is DuckDB's way of saying the function returns a
		// table; elsewhere those words are not a return type at all, and
		// reading them as one would build a property the reference never made.
		if p.tables.FunctionAsTableRead && p.atWords("TABLE") {
			p.advance()
			query, err := p.parseQuery()
			if err != nil {
				return nil, nil, err
			}
			return query, New("ReturnsProperty",
				Arg{"this", New("Schema", Arg{"this", New("Var", Arg{"this", "TABLE"})})},
				Arg{"is_table", true}), nil
		}
		if p.atWords("RETURN") {
			p.advance()
			inner, err := p.parseReturnBody()
			if err != nil {
				return nil, nil, err
			}
			return New("Return", Arg{"this", inner}), nil, nil
		}
		if c := p.curr(); c != nil && c.Type == TokSTRING {
			p.advance()
			return New("Literal", Arg{"this", c.Text}, Arg{"is_string", true}), nil, nil
		}
		// `AS $$ ... $$` holds a body in another language entirely, and the
		// reference keeps the text without reading it. The TAG between the
		// dollars is not kept, so `$FOO$ ... $FOO$` comes back as `$$ ... $$`.
		if c := p.curr(); c != nil && c.Type == TokHEREDOC_STRING {
			p.advance()
			return New("Heredoc", Arg{"this", c.Text}), nil, nil
		}
		inner, err := p.parseReturnBody()
		if err != nil {
			return nil, nil, err
		}
		return inner, nil, nil
	}
	return nil, nil, nil
}

// parseReturnBody reads the expression or query a function hands back.
func (p *parser) parseReturnBody() (*Expression, error) {
	if p.at(TokSELECT) || p.at(TokWITH) {
		return p.parseQuery()
	}
	return p.parseExpression()
}

// parseParameterName reads `@name` where a parameter's name is spelled that
// way, and nil where it is not.
func (p *parser) parseParameterName() *Expression {
	c := p.curr()
	if c == nil || c.Type != TokPARAMETER || c.Text != "@" ||
		p.tables.Placeholder.AtName != "Parameter" {
		return nil
	}
	n := p.next()
	if !isParameterName(n) {
		return nil
	}
	p.advance()
	p.advance()
	return New("Parameter", Arg{"this", New("Var", Arg{"this", n.Text})})
}

// parseGenerated reads what follows GENERATED on a column.
//
// Three constructs share the word, and what comes after AS decides which: a
// column the engine fills from a SEQUENCE (`AS IDENTITY`), one it computes
// and stores (`AS (expr) STORED`), and one it computes without storing
// (`AS (expr)`) -- which the reference records as an identity carrying an
// expression rather than as a computed column, odd as that reads.
func (p *parser) parseGenerated() (*Expression, error) {
	always := false
	onNull := false
	switch {
	case p.atWords("ALWAYS"):
		p.advance()
		always = true
	case p.atWords("BY", "DEFAULT"):
		p.advance()
		p.advance()
		if p.atWords("ON", "NULL") {
			p.advance()
			p.advance()
			onNull = true
		}
	default:
		return nil, p.unsupported("GENERATED without ALWAYS or BY DEFAULT")
	}
	if !p.match(TokALIAS) {
		return nil, p.unsupported("GENERATED without AS")
	}

	if p.at(TokL_PAREN) {
		// The parentheses are not kept: the reference kept the expression
		// alone here, unlike T-SQL's own `AS (x) PERSISTED`, which keeps them.
		p.advance()
		value, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		if !p.match(TokR_PAREN) {
			return nil, p.unsupported("unclosed generated expression")
		}
		if p.atWords("STORED") {
			p.advance()
			return New("ComputedColumnConstraint", Arg{"this", value}), nil
		}
		if !always {
			return nil, p.unsupported("a computed column that is not ALWAYS")
		}
		// Without STORED it is a computed column in Databricks and an
		// identity CARRYING an expression everywhere else. One statement,
		// two nodes, and the dialect decides which.
		if p.tables.GeneratedExpressionIsComputed {
			return New("ComputedColumnConstraint", Arg{"this", value}), nil
		}
		return New("GeneratedAsIdentityColumnConstraint",
			Arg{"this", true}, Arg{"expression", value}), nil
	}
	if !p.atWords("IDENTITY") {
		// `GENERATED ALWAYS AS ROW START` and its like say what the column
		// tracks rather than how it is filled.
		return nil, p.unsupported("a GENERATED column this port does not read")
	}
	p.advance()

	identity := New("GeneratedAsIdentityColumnConstraint", Arg{"this", always})
	if !always {
		identity.Set("on_null", onNull)
	}
	if !p.at(TokL_PAREN) {
		return identity, nil
	}
	p.advance()
	var start, increment *Expression
	cycle := false
	for !p.at(TokR_PAREN) {
		switch {
		case p.atWords("START", "WITH"):
			p.advance()
			p.advance()
			value, err := p.parseExpression()
			if err != nil {
				return nil, err
			}
			start = value
		case p.atWords("INCREMENT", "BY"):
			p.advance()
			p.advance()
			value, err := p.parseExpression()
			if err != nil {
				return nil, err
			}
			increment = value
		case p.atWords("CYCLE"):
			p.advance()
			cycle = true
		default:
			return nil, p.unsupported("an IDENTITY option this port does not read")
		}
	}
	p.advance() // the closing parenthesis
	// Set in the reference's order, which is not the order they may be
	// written in.
	if start != nil {
		identity.Set("start", start)
	}
	if increment != nil {
		identity.Set("increment", increment)
	}
	if cycle {
		identity.Set("cycle", true)
	}
	return identity, nil
}

// parseCreateBody reads the query a CREATE is given.
//
// The parentheses around one are KEPT: `CREATE TABLE t AS (SELECT 1)` holds a
// Subquery where `CREATE TABLE t AS SELECT 1` holds the Select itself, and the
// two are different trees for what an engine runs the same way.
func (p *parser) parseCreateBody() (*Expression, error) {
	if !p.at(TokL_PAREN) {
		return p.parseQuery()
	}
	p.advance()
	inner, err := p.parseQuery()
	if err != nil {
		return nil, err
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed query")
	}
	return New("Subquery", Arg{"this", inner}), nil
}

// parseIndexRest reads everything after `CREATE [UNIQUE] INDEX`.
//
// The name is OPTIONAL -- PostgreSQL lets the server choose one -- and the
// columns are ORDERED members, each of which may say where its nulls go.
func (p *parser) parseIndexRest(replace, unique, temporary bool) (*Expression, error) {
	if temporary {
		return nil, p.unsupported("CREATE TEMPORARY INDEX")
	}
	concurrently := false
	// A QUOTED name that happens to spell the word is a name, not the option:
	// `CREATE INDEX "concurrently" ON t(x)` names an index.
	if c := p.curr(); c != nil && c.Type != TokIDENTIFIER && p.atWords("CONCURRENTLY") {
		p.advance()
		concurrently = true
	}
	exists := false
	if p.atWords("IF", "NOT", "EXISTS") {
		p.advance()
		p.advance()
		p.advance()
		exists = true
	}

	index := New("Index")
	if !p.atWords("ON") {
		name, err := p.parseIdentifier()
		if err != nil {
			return nil, err
		}
		index.Set("this", name)
	}
	if !p.atWords("ON") {
		return nil, p.unsupported("CREATE INDEX without ON")
	}
	p.advance()
	table, err := p.parseTableName()
	if err != nil {
		return nil, err
	}
	index.Set("table", table)
	if !p.at(TokL_PAREN) {
		// `USING gin(...)` names the method the index is built with, and the
		// rest of that vocabulary -- INCLUDE, WHERE, an operator class -- is
		// not read here either.
		return nil, p.unsupported("CREATE INDEX with more than columns")
	}
	columns, err := p.parseIndexColumns()
	if err != nil {
		return nil, err
	}
	params := New("IndexParameters", Arg{"columns", columns})
	params.Set("with_storage", false)
	index.Set("params", params)
	if p.curr() != nil {
		return nil, p.unsupported("CREATE INDEX with more than this port reads")
	}

	return New("Create",
		Arg{"this", index},
		Arg{"kind", "INDEX"},
		Arg{"replace", replace},
		Arg{"refresh", false},
		Arg{"unique", unique},
		Arg{"expression", nil},
		Arg{"exists", exists},
		Arg{"properties", nil},
		Arg{"indexes", []*Expression{}},
		Arg{"no_schema_binding", nil},
		Arg{"begin", nil},
		Arg{"clone", nil},
		Arg{"concurrently", concurrently},
		Arg{"clustered", nil},
	), nil
}

// parseIndexColumns reads the `(a, b DESC NULLS LAST)` an index is over. Each
// member is an Ordered, whether or not it says anything about order.
func (p *parser) parseIndexColumns() ([]*Expression, error) {
	p.advance() // the opening parenthesis
	var out []*Expression
	for {
		column, err := p.parseColumn()
		if err != nil {
			return nil, err
		}
		member := New("Ordered", Arg{"this", column})
		desc := false
		switch {
		case p.atWords("DESC"):
			p.advance()
			desc = true
			member.Set("desc", true)
		case p.atWords("ASC"):
			p.advance()
			member.Set("desc", false)
		}
		switch {
		case p.atWords("NULLS", "FIRST"):
			p.advance()
			p.advance()
			member.Set("nulls_first", true)
		case p.atWords("NULLS", "LAST"):
			p.advance()
			p.advance()
			member.Set("nulls_first", false)
		default:
			member.Set("nulls_first", p.nullsFirst(desc))
		}
		out = append(out, member)
		if !p.match(TokCOMMA) {
			break
		}
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed index column list")
	}
	return out, nil
}

// The statements that are barely statements: a table emptied, a database
// chosen, a transaction opened or closed. Each is small, and each CHANGES
// something, which is why the guard above this port has to see them.

// parseTruncate reads `TRUNCATE TABLE [IF EXISTS] [ONLY] t[, ...]
// [RESTART|CONTINUE IDENTITY] [CASCADE|RESTRICT]`.
func (p *parser) parseTruncate() (*Expression, error) {
	p.advance() // TRUNCATE
	if !p.atWords("TABLE") {
		return nil, p.unsupported("TRUNCATE without TABLE")
	}
	p.advance()
	exists := false
	if p.atWords("IF", "EXISTS") {
		p.advance()
		p.advance()
		exists = true
	}

	var tables []*Expression
	for {
		only := false
		if p.atWords("ONLY") {
			p.advance()
			only = true
		}
		table, err := p.parseTableName()
		if err != nil {
			return nil, err
		}
		// `t2*` says the table AND everything that inherits from it, which is
		// the default -- the star is written and recorded nowhere.
		if p.at(TokSTAR) {
			p.advance()
		}
		if only {
			table.Set("only", true)
		}
		tables = append(tables, table)
		if !p.match(TokCOMMA) {
			break
		}
	}

	node := New("TruncateTable", Arg{"expressions", tables},
		Arg{"is_database", false}, Arg{"exists", exists})
	switch {
	case p.atWords("RESTART", "IDENTITY"):
		p.advance()
		p.advance()
		node.Set("identity", "RESTART")
	case p.atWords("CONTINUE", "IDENTITY"):
		p.advance()
		p.advance()
		node.Set("identity", "CONTINUE")
	}
	switch {
	case p.atWords("CASCADE"):
		p.advance()
		node.Set("option", "CASCADE")
	case p.atWords("RESTRICT"):
		p.advance()
		node.Set("option", "RESTRICT")
	}
	if p.curr() != nil {
		return nil, p.unsupported("TRUNCATE with more than this port reads")
	}
	return node, nil
}

// parseUse reads `USE [SCHEMA|CATALOG|...] <name>`. The kind is a WORD and
// lands on the node before the name, whatever order the two are read in.
func (p *parser) parseUse() (*Expression, error) {
	p.advance() // USE
	node := New("Use")
	for _, word := range []string{"SCHEMA", "CATALOG", "DATABASE", "WAREHOUSE", "ROLE"} {
		if c := p.curr(); c != nil && c.Type != TokIDENTIFIER && p.atWords(word) {
			p.advance()
			node.Set("kind", New("Var", Arg{"this", word}))
			break
		}
	}
	table, err := p.parseTableName()
	if err != nil {
		return nil, err
	}
	node.Set("this", table)
	if p.curr() != nil {
		return nil, p.unsupported("USE with more than this port reads")
	}
	return node, nil
}

// parseTransaction reads BEGIN, COMMIT and ROLLBACK, each of which may name
// the transaction it acts on -- and ROLLBACK may name a SAVEPOINT instead,
// which is a different argument because it is a different action.
func (p *parser) parseTransaction() (*Expression, error) {
	verb := strings.ToUpper(p.curr().Text)
	// T-SQL's bare BEGIN opens a BLOCK -- `BEGIN ... END` -- and takes the
	// word TRANSACTION to mean the other thing. The reference gives up on the
	// block form and keeps the raw text, which is not a tree this port makes.
	if verb == "BEGIN" && !p.tables.BareBeginIsATransaction {
		if n := p.next(); n == nil ||
			(!strings.EqualFold(n.Text, "TRANSACTION") && !strings.EqualFold(n.Text, "TRAN")) {
			return nil, p.unsupported("BEGIN opening a block")
		}
	}
	p.advance()
	class := map[string]string{
		"BEGIN": "Transaction", "COMMIT": "Commit", "ROLLBACK": "Rollback",
	}[verb]
	node := New(class)

	if verb == "ROLLBACK" && p.atWords("TO") {
		p.advance()
		name, err := p.parseIdentifier()
		if err != nil {
			return nil, err
		}
		node.Set("savepoint", name)
		if p.curr() != nil {
			return nil, p.unsupported(verb + " with more than this port reads")
		}
		return node, nil
	}
	// The word is optional and says nothing: `COMMIT` and `COMMIT TRANSACTION`
	// are the same node, and only T-SQL writes it back.
	if p.atWords("TRANSACTION") || p.atWords("TRAN") || p.atWords("WORK") {
		p.advance()
	}
	if p.curr() != nil {
		name, err := p.parsePrimary()
		if err != nil {
			return nil, err
		}
		if name.Class == "Column" {
			// A bare word here NAMES the transaction; the reference keeps the
			// identifier rather than a reference to a column.
			name = name.This()
		}
		node.Set("this", name)
	}
	if p.curr() != nil {
		return nil, p.unsupported(verb + " with more than this port reads")
	}
	return node, nil
}

// parseGrant reads `GRANT <privileges> ON [<kind>] <name> TO <principals>
// [WITH GRANT OPTION]`, and the REVOKE that undoes it.
//
// A permission change is not a query and is not read-only, and it is one of
// the few statements whose whole point is what a caller is allowed to do
// next -- which is exactly what a guard is for.
func (p *parser) parseGrant() (*Expression, error) {
	class := "Grant"
	if p.at(TokREVOKE) {
		class = "Revoke"
	}
	p.advance()

	node := New(class)
	// `REVOKE GRANT OPTION FOR SELECT` takes away the right to pass the
	// privilege on rather than the privilege itself.
	grantOption := false
	if class == "Revoke" && p.atWords("GRANT", "OPTION", "FOR") {
		p.advance()
		p.advance()
		p.advance()
		grantOption = true
	}

	privileges, err := p.parsePrivileges()
	if err != nil {
		return nil, err
	}
	node.Set("privileges", privileges)

	if !p.atWords("ON") {
		return nil, p.unsupported(class + " without ON")
	}
	p.advance()
	// The kind is a WORD and is written only when it was: `ON TABLE t` and
	// `ON t` are different trees.
	for _, word := range []string{"TABLE", "VIEW", "FUNCTION", "SCHEMA", "DATABASE"} {
		if c := p.curr(); c != nil && c.Type != TokIDENTIFIER && p.atWords(word) {
			p.advance()
			node.Set("kind", word)
			break
		}
	}
	securable, err := p.parseTableName()
	if err != nil {
		return nil, err
	}
	node.Set("securable", securable)

	if class == "Grant" && !p.atWords("TO") {
		return nil, p.unsupported("GRANT without TO")
	}
	if class == "Revoke" && !p.atWords("FROM") && !p.at(TokFROM) {
		return nil, p.unsupported("REVOKE without FROM")
	}
	p.advance()

	var principals []*Expression
	for {
		principal := New("GrantPrincipal")
		// The word is a KIND only when a name follows it. `FROM user
		// RESTRICT` revokes from a principal CALLED user -- reading the word
		// as a kind took RESTRICT for the name and the restriction with it.
		kind := ""
		for _, word := range []string{"ROLE", "USER", "GROUP"} {
			c := p.curr()
			if c == nil || c.Type == TokIDENTIFIER || !p.atWords(word) {
				continue
			}
			if n := p.next(); n == nil || endsAPrincipal(n) {
				break
			}
			p.advance()
			kind = word
			break
		}
		name, err := p.parseIdentifier()
		if err != nil {
			return nil, err
		}
		principal.Set("this", name)
		if kind == "" {
			principal.Set("kind", false)
		} else {
			principal.Set("kind", kind)
		}
		principals = append(principals, principal)
		if !p.match(TokCOMMA) {
			break
		}
	}
	node.Set("principals", principals)

	if class == "Grant" && p.atWords("WITH", "GRANT", "OPTION") {
		p.advance()
		p.advance()
		p.advance()
		grantOption = true
	}
	node.Set("grant_option", grantOption)
	switch {
	case p.atWords("RESTRICT"):
		p.advance()
		node.Set("cascade", "RESTRICT")
	case p.atWords("CASCADE"):
		p.advance()
		node.Set("cascade", "CASCADE")
	}
	if p.curr() != nil {
		// `GRANT ... AS role` says who is doing the granting, and the
		// reference gives up on it and keeps the raw text.
		return nil, p.unsupported(class + " with more than this port reads")
	}
	return node, nil
}

// parsePrivileges reads the comma-separated rights a GRANT hands over. Each is
// a WORD, and some are two: `ALL PRIVILEGES` is one privilege, not two.
func (p *parser) parsePrivileges() ([]*Expression, error) {
	var out []*Expression
	for {
		c := p.curr()
		if c == nil {
			return nil, p.unsupported("a privilege with no name")
		}
		name := strings.ToUpper(c.Text)
		p.advance()
		if name == "ALL" && p.atWords("PRIVILEGES") {
			p.advance()
			name = "ALL PRIVILEGES"
		}
		out = append(out, New("GrantPrivilege",
			Arg{"this", New("Var", Arg{"this", name})}))
		if !p.match(TokCOMMA) {
			return out, nil
		}
	}
}

// endsAPrincipal reports whether a token closes the list of principals rather
// than naming one. The words that may follow the list are few, and they are
// what tells `FROM user RESTRICT` from `FROM ROLE public`.
func endsAPrincipal(t *Token) bool {
	if t == nil {
		return true
	}
	switch strings.ToUpper(t.Text) {
	case "RESTRICT", "CASCADE", "WITH", "AS":
		return true
	}
	return t.Type == TokCOMMA || t.Type == TokSEMICOLON
}

// parseComment reads `COMMENT ON <kind> <name> IS '<text>'`.
//
// The name is a COLUMN where the kind says so and a table-shaped name
// otherwise -- the same words, two nodes, and the kind is what decides.
func (p *parser) parseComment() (*Expression, error) {
	p.advance() // COMMENT
	if !p.atWords("ON") {
		return nil, p.unsupported("COMMENT without ON")
	}
	p.advance()
	c := p.curr()
	if c == nil {
		return nil, p.unsupported("COMMENT without a kind")
	}
	kind := strings.ToUpper(c.Text)
	switch kind {
	case "TABLE", "VIEW", "COLUMN", "TYPE", "SEQUENCE", "SCHEMA", "DATABASE", "INDEX":
	default:
		// A PROCEDURE or FUNCTION is named with its SIGNATURE, which is a
		// third shape again.
		return nil, p.unsupported("COMMENT ON " + kind)
	}
	p.advance()

	var this *Expression
	var err error
	if kind == "COLUMN" {
		this, err = p.parseColumn()
	} else {
		this, err = p.parseTableName()
	}
	if err != nil {
		return nil, err
	}
	if !p.atWords("IS") {
		return nil, p.unsupported("COMMENT without IS")
	}
	p.advance()
	text := p.curr()
	if text == nil || text.Type != TokSTRING {
		// `IS NULL` removes the comment, which the reference does not read
		// either.
		return nil, p.unsupported("COMMENT without a string")
	}
	p.advance()
	if p.curr() != nil {
		return nil, p.unsupported("COMMENT with more than this port reads")
	}
	return New("Comment",
		Arg{"this", this},
		Arg{"kind", kind},
		Arg{"expression", New("Literal",
			Arg{"this", text.Text}, Arg{"is_string", true})},
		Arg{"exists", false},
		Arg{"materialized", false},
	), nil
}

// parseSet reads `SET [<scope>] <name> = <value>[, ...]`.
//
// The `=` is optional -- T-SQL writes `SET XACT_ABORT ON` -- and the reference
// records an equality either way, so the sign is a spelling rather than part
// of the tree.
func (p *parser) parseSet() (*Expression, error) {
	p.advance() // SET
	var items []*Expression
	for {
		item, err := p.parseSetStatementItem()
		if err != nil {
			return nil, err
		}
		items = append(items, item)
		if !p.match(TokCOMMA) {
			break
		}
	}
	if p.curr() != nil {
		return nil, p.unsupported("SET with more than this port reads")
	}
	return New("Set", Arg{"expressions", items},
		Arg{"unset", false}, Arg{"tag", false}), nil
}

// parseSetStatementItem reads one setting, with the scope word that may come
// in front of it.
func (p *parser) parseSetStatementItem() (*Expression, error) {
	kind := ""
	for word, records := range map[string]string{
		"GLOBAL": "GLOBAL", "SESSION": "SESSION", "LOCAL": "LOCAL",
		"VARIABLE": "VARIABLE", "VAR": "VARIABLE",
	} {
		c := p.curr()
		if c == nil || c.Type == TokIDENTIFIER || !p.atWords(word) {
			continue
		}
		// The word is a SCOPE only when a name follows it; `SET local = 1`
		// sets a setting CALLED local.
		if n := p.next(); n == nil || n.Type == TokEQ || strings.EqualFold(n.Text, "TO") {
			break
		}
		p.advance()
		kind = records
		break
	}

	var name *Expression
	var err error
	switch {
	case p.at(TokL_PAREN):
		// `SET VARIABLE (v1, v2) = (SELECT 1, 2)` sets several at once from
		// one query, and the names are a Tuple.
		members, err := p.parseParenthesisedList()
		if err != nil {
			return nil, err
		}
		name = New("Tuple", Arg{"expressions", members})
	default:
		name = p.parseParameterName()
		if name == nil {
			name, err = p.parseColumn()
			if err != nil {
				return nil, err
			}
		}
	}

	// `TO` and `=` are the same thing, and a setting may be written with
	// neither: `SET XACT_ABORT ON`.
	if !p.at(TokEQ) && !p.atWords("TO") {
		if p.curr() == nil {
			return nil, p.unsupported("SET without a value")
		}
	} else {
		p.advance()
	}
	value, err := p.parseSetValue()
	if err != nil {
		return nil, err
	}

	item := New("SetItem",
		Arg{"this", New("EQ", Arg{"this", name}, Arg{"expression", value})})
	if kind != "" {
		item.Set("kind", kind)
	}
	return item, nil
}

// parseSetValue reads what a setting is set TO. A bare word is a Var rather
// than a column: nothing here refers to anything.
func (p *parser) parseSetValue() (*Expression, error) {
	c := p.curr()
	if c == nil {
		return nil, p.unsupported("SET without a value")
	}
	// A WORD standing alone here is a Var whatever the tokenizer called it:
	// `SET XACT_ABORT ON` sets a setting to the word ON, and ON is a keyword
	// that no expression rule would accept in this position.
	if n := p.next(); (n == nil || n.Type == TokCOMMA || n.Type == TokSEMICOLON) &&
		isBareWord(c.Text) && c.Type != TokIDENTIFIER && c.Type != TokSTRING {
		p.advance()
		return New("Var", Arg{"this", c.Text}), nil
	}
	return p.parseExpression()
}

// isBareWord reports whether a token's text is a word rather than punctuation
// or a number -- which is what makes it a name for something. A NUMBER passes
// the character test and is not a word: `SET x = 1` sets a value, not a name.
func isBareWord(text string) bool {
	if text == "" {
		return false
	}
	for i, r := range text {
		if !isIdentifierChar(r) {
			return false
		}
		if i == 0 && unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// parsePragma reads `PRAGMA <whatever>`.
//
// What follows the word is an ordinary EXPRESSION -- a name, a qualified call,
// or an equality -- and the reference keeps it as one rather than giving the
// statement a grammar of its own.
func (p *parser) parsePragma() (*Expression, error) {
	p.advance() // PRAGMA
	this, err := p.parseExpression()
	if err != nil {
		return nil, err
	}
	if p.curr() != nil {
		return nil, p.unsupported("PRAGMA with more than this port reads")
	}
	return New("Pragma", Arg{"this", this}), nil
}

package sqlglot

import "strings"

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
	if kind != "TABLE" && kind != "VIEW" {
		return nil, p.unsupported("CREATE " + kind)
	}
	p.advance()

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
			query, err := p.parseQuery()
			if err != nil {
				return nil, err
			}
			expression = query
		}
	case p.match(TokALIAS):
		this = table
		query, err := p.parseQuery()
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
	if kind != "TABLE" {
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

	action, err := p.parseAlterAction()
	if err != nil {
		return nil, err
	}
	if p.curr() != nil {
		return nil, p.unsupported("ALTER with more than this port reads")
	}
	return New("Alter",
		Arg{"this", table},
		Arg{"kind", kind},
		Arg{"exists", exists},
		Arg{"actions", []*Expression{action}},
		Arg{"only", false},
		Arg{"options", []*Expression{}},
		Arg{"cluster", nil},
		Arg{"not_valid", false},
		Arg{"check", false},
		Arg{"cascade", false},
		Arg{"iceberg", false},
	), nil
}

// parseAlterAction reads the one thing this ALTER does.
func (p *parser) parseAlterAction() (*Expression, error) {
	switch {
	case p.atWords("ADD"):
		p.advance()
		if !p.atWords("COLUMN") {
			return nil, p.unsupported("ALTER TABLE ADD without COLUMN")
		}
		p.advance()
		if p.atWords("IF", "NOT", "EXISTS") {
			// The reference records this on the ColumnDef, which this port
			// does not read -- so the statement is refused rather than built
			// without it.
			return nil, p.unsupported("ADD COLUMN IF NOT EXISTS")
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
		// The definition an ALTER adds carries one argument the same definition
		// inside a CREATE does not: whether the column had to be absent.
		if constraints == nil {
			constraints = []*Expression{}
		}
		return New("ColumnDef",
			Arg{"this", name}, Arg{"kind", kind},
			Arg{"constraints", constraints},
			Arg{"position", nil},
			Arg{"exists", false}), nil
	case p.at(TokDROP):
		p.advance()
		if !p.atWords("COLUMN") {
			return nil, p.unsupported("ALTER TABLE DROP without COLUMN")
		}
		p.advance()
		name, err := p.parseColumn()
		if err != nil {
			return nil, err
		}
		return New("Drop",
			Arg{"exists", false},
			Arg{"tables", []*Expression{name}},
			Arg{"expressions", nil},
			Arg{"kind", "COLUMN"},
			Arg{"temporary", false}, Arg{"materialized", false},
			Arg{"cascade", false}, Arg{"restrict", false},
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
	}
	return nil, p.unsupported("an ALTER TABLE action this port does not read")
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

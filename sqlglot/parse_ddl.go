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
	// The modifiers the reference keeps as PROPERTIES rather than as flags are
	// not read here; a statement carrying one is refused rather than built
	// without it.
	for _, word := range []string{"TEMPORARY", "TEMP", "GLOBAL", "LOCAL",
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
	if kind != "TABLE" {
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
		columns, err := p.parseColumnDefs()
		if err != nil {
			return nil, err
		}
		this = New("Schema", Arg{"this", table}, Arg{"expressions", columns})
	case p.match(TokALIAS):
		this = table
		query, err := p.parseQuery()
		if err != nil {
			return nil, err
		}
		expression = query
	default:
		return nil, p.unsupported("CREATE TABLE without columns or a query")
	}
	if p.curr() != nil {
		return nil, p.unsupported("CREATE TABLE with more than this port reads")
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
		Arg{"properties", nil},
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
		default:
			if !p.at(TokCOMMA) && !p.at(TokR_PAREN) {
				return nil, p.unsupported("a column constraint this port does not read")
			}
			return out, nil
		}
		out = append(out, New("ColumnConstraint", Arg{"kind", kind}))
	}
}

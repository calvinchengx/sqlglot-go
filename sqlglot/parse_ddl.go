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
		if !p.at(TokCOMMA) && !p.at(TokR_PAREN) {
			return nil, p.unsupported("a column with more than a name and a type")
		}
		out = append(out, New("ColumnDef", Arg{"this", name}, Arg{"kind", kind}))
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

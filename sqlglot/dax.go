package sqlglot

// DAX's parser accepts EVALUATE and nothing else, so the table generator's
// probes (a SELECT, a CREATE) raise on it. The vocabulary it shares is the
// neutral one, and a quoted name is written in single quotes. The tokenizer
// is that same neutral configuration with the quote, identifier, comment, and
// string-escape tables DAX actually uses. Both are registered here rather than
// generated: the generated dialect map is one composite literal, and growing
// it is a verbosity regression on the whole of it.
func init() {
	if src := parserTables[""]; src != nil {
		tables := *src
		tables.IdentifierStart = "'"
		tables.IdentifierEnd = "'"
		parserTables["dax"] = &tables
	}
	base := dialectConfigs[""]
	if base == nil {
		return
	}
	cfg := *base
	cfg.Name = "dax"
	cfg.trie = nil
	cfg.Tables = nil
	cfg.Quotes = map[string]string{`"`: `"`}
	cfg.FormatStrings = map[string]FormatString{
		`N"`: {End: `"`, Type: TokNATIONAL_STRING},
		`n"`: {End: `"`, Type: TokNATIONAL_STRING},
	}
	cfg.Identifiers = map[string]string{
		`'`: `'`,
		`[`: `]`,
	}
	comments := make(map[string]string, len(base.Comments)+1)
	for k, v := range base.Comments {
		comments[k] = v
	}
	comments["//"] = ""
	cfg.Comments = comments
	cfg.StringEscapes = map[string]struct{}{`"`: {}}
	cfg.StringEscapePreferred = `"`
	cfg.ByteStringEscapes = map[string]struct{}{`"`: {}}
	dialectConfigs["dax"] = &cfg
}

// EVALUATE names a table. FILTER, column brackets, and ORDER BY are later
// mechanisms; anything after the table name is left unread so it is refused
// as trailing tokens rather than taken as a different statement.

func (p *parser) opensDAXEvaluate() bool {
	return p.dialect == "dax" && p.atWords("EVALUATE")
}

func (p *parser) parseDAXEvaluate() (*Expression, error) {
	p.advance()
	name, err := p.parseTablePart()
	if err != nil {
		return nil, err
	}
	if name == nil {
		return nil, p.unsupported("EVALUATE without a table")
	}
	table := New("Table", Arg{"this", name})
	liftPart(name, table)
	return New("Select",
		Arg{"expressions", []*Expression{New("Star")}},
		Arg{"from_", New("From", Arg{"this", table})},
	), nil
}

// opensDAXFilter reports an EVALUATE whose table is a FILTER call. A plain
// EVALUATE stays on the path that already reads a bare table name.
func (p *parser) opensDAXFilter() bool {
	return p.dialect == "dax" && p.atWords("EVALUATE", "FILTER")
}

func (p *parser) parseDAXFilterQuery() (*Expression, error) {
	p.advance()
	return p.parseDAXFilter()
}

// parseDAXFilter reads FILTER(table, condition). A FILTER nested in the table
// place is the same call, and its condition is AND-ed with this one.
func (p *parser) parseDAXFilter() (*Expression, error) {
	query, cond, err := p.readDAXFilterParts()
	if err == nil {
		query = daxAddWhere(query, cond)
	}
	return query, err
}

func (p *parser) readDAXFilterParts() (query, cond *Expression, err error) {
	for _, tok := range []TokenType{TokFILTER, TokL_PAREN} {
		if !p.match(tok) {
			return nil, nil, p.unsupported("FILTER")
		}
	}
	if query, err = p.parseDAXFilteredTable(); err != nil {
		return nil, nil, err
	}
	if !p.match(TokCOMMA) {
		return nil, nil, p.unsupported("FILTER without a condition")
	}
	if cond, err = p.parseDisjunction(); err != nil {
		return nil, nil, err
	}
	if cond == nil || !p.match(TokR_PAREN) {
		return nil, nil, p.unsupported("unclosed FILTER")
	}
	return query, cond, nil
}

// A plain table is handed back to the EVALUATE reader, which already knows
// how to spell that name as SELECT * FROM. Building the same select here
// would be a second copy of that reader.
func (p *parser) parseDAXFilteredTable() (*Expression, error) {
	if p.at(TokFILTER) {
		return p.parseDAXFilter()
	}
	name, err := p.parseTablePart()
	if err != nil {
		return nil, err
	}
	if name == nil {
		return nil, p.unsupported("FILTER without a table")
	}
	spelled, err := Generate(name, "dax")
	if err != nil {
		return nil, err
	}
	return ParseOne("EVALUATE "+spelled, "dax")
}

// daxAddWhere attaches a condition. A second FILTER AND-s its condition onto
// the one already there, which is what the reference's where() does.
func daxAddWhere(query, cond *Expression) *Expression {
	pred := cond
	if existing, _ := query.Args["where"].(*Expression); existing != nil {
		pred = New("And",
			Arg{"this", existing.Args["this"]},
			Arg{"expression", cond},
		)
	}
	query.Set("where", New("Where", Arg{"this", pred}))
	return query
}

// daxBracketColumn reads Table[Column]. The bracketed name is its own
// identifier, and the reference joins it onto the column just read.
func (p *parser) daxBracketColumn(col *Expression) (*Expression, error) {
	if p.dialect != "dax" || col == nil || col.Class != "Column" || !p.atIdentifier() {
		return col, nil
	}
	name, _ := col.Args["this"].(*Expression)
	if name == nil || name.Class != "Identifier" {
		return col, nil
	}
	column, err := p.parseIdentifier()
	if err != nil {
		return nil, err
	}
	if column == nil {
		return col, nil
	}
	out := New("Column", Arg{"this", column}, Arg{"table", name})
	liftPart(column, out)
	liftPart(name, out)
	return out, nil
}

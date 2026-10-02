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

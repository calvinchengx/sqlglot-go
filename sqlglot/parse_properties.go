package sqlglot

import (
	"sort"
	"strings"
)

// parseTableProperties reads the words a CREATE may say about the thing it
// makes -- `USING PARQUET`, `PARTITIONED BY (a INT)`, `CLUSTER BY (c)` -- until
// one of them is not a property.
//
// Each word's node and the shape of what follows it are GENERATED, because the
// reference keeps a little grammar per word and none of it is readable as data.
// The words that are not classified are left where they are, and the statement
// is refused for having more in it than this port reads.
func (p *parser) parseTableProperties() ([]*Expression, error) {
	var out []*Expression
	for {
		// `PARTITION OF` is not in the generated table of property words: it
		// carries a whole grammar of its own rather than a word and a shape.
		if p.atWords("PARTITION", "OF") {
			prop, err := p.parsePartitionedOf()
			if err != nil {
				return nil, err
			}
			out = append(out, prop)
			continue
		}
		spec, consumed, ok := p.atProperty()
		if !ok {
			return out, nil
		}
		for i := 0; i < consumed; i++ {
			p.advance()
		}
		if spec.Shape == "wrapped-properties" {
			// The word opens a LIST of other properties and contributes none
			// of its own: `WITH (FORMAT='parquet')` is a FileFormatProperty
			// beside the rest, not a property called WITH.
			//
			// Except where no list follows it: T-SQL writes `WITH
			// SCHEMABINDING` on a view, one bare word saying something about
			// the view rather than a list of settings for it.
			if !p.at(TokL_PAREN) {
				attr := p.curr()
				if attr == nil {
					return nil, p.unsupported("WITH naming nothing")
				}
				p.advance()
				out = append(out, New("ViewAttributeProperty",
					Arg{"this", strings.ToUpper(attr.Text)}))
				continue
			}
			inner, err := p.parseWrappedProperties()
			if err != nil {
				return nil, err
			}
			out = append(out, inner...)
			continue
		}
		prop, err := p.parseProperty(spec)
		if err != nil {
			return nil, err
		}
		out = append(out, prop)
	}
}

// atProperty reports which property word stands at the cursor, and how many
// tokens it takes. The longest match wins: `PARTITION BY` and `PARTITION` are
// both words, and reading the shorter one leaves a BY behind that nothing else
// can use.
//
// A two-word property may arrive as ONE token or as two -- the tokenizer joins
// `CLUSTER BY` and leaves `PARTITIONED BY` apart -- so both are tried.
func (p *parser) atProperty() (PropertySpec, int, bool) {
	words := make([]string, 0, len(p.tables.PropertySpecs))
	for word := range p.tables.PropertySpecs {
		words = append(words, word)
	}
	sort.Slice(words, func(i, j int) bool { return len(words[i]) > len(words[j]) })
	for _, word := range words {
		fields := strings.Fields(word)
		if p.atWords(fields...) {
			return p.tables.PropertySpecs[word], len(fields), true
		}
		if c := p.curr(); c != nil && strings.EqualFold(strings.Join(strings.Fields(c.Text), " "), word) {
			return p.tables.PropertySpecs[word], 1, true
		}
	}
	return PropertySpec{}, 0, false
}

// parseWrappedProperties reads `(a = 1, b = 2)` -- a list of properties under
// one word. Each item is read as a property in its own right, and an item that
// names none of them is a plain key and value.
func (p *parser) parseWrappedProperties() ([]*Expression, error) {
	if !p.match(TokL_PAREN) {
		return nil, p.unsupported("property list without a list")
	}
	var out []*Expression
	for {
		// One property carries the WITH it was written inside: the reference
		// records `WITH(SYSTEM_VERSIONING=ON)` as a single property with a
		// flag saying so, and writes the word back itself.
		if prop, own, err := p.parseBespokeProperty(true); err != nil {
			return nil, err
		} else if own {
			out = append(out, prop)
			if !p.match(TokCOMMA) {
				break
			}
			continue
		}
		spec, consumed, ok := p.atProperty()
		if ok && spec.Shape != "wrapped-properties" {
			for i := 0; i < consumed; i++ {
				p.advance()
			}
			prop, err := p.parseProperty(spec)
			if err != nil {
				return nil, err
			}
			out = append(out, prop)
		} else {
			pair, err := p.parseKeyValueProperty()
			if err != nil {
				return nil, err
			}
			out = append(out, pair)
		}
		if !p.match(TokCOMMA) {
			break
		}
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed property list")
	}
	return out, nil
}

// parseBespokeProperty reads the two properties whose grammar is their own
// rather than one of the shapes the probe records: each takes ON or OFF and
// may carry a parenthesised list of settings under it.
//
// The reference reads the settings with a loop that advances only when it
// recognises one, so anything else in there never returns -- see
// docs/upstream-issues.md. This refuses instead.
func (p *parser) parseBespokeProperty(inWith bool) (*Expression, bool, error) {
	switch {
	case p.atWords("SYSTEM_VERSIONING"):
		prop, err := p.parseSystemVersioning(inWith)
		return prop, true, err
	case p.atWords("DATA_DELETION"):
		prop, err := p.parseDataDeletion()
		return prop, true, err
	}
	return nil, false, nil
}

// parseDataDeletion reads `DATA_DELETION=ON`, `=OFF`, and the settings that
// say which column dates a row and how long it is kept.
func (p *parser) parseDataDeletion() (*Expression, error) {
	p.advance() // DATA_DELETION
	p.match(TokEQ)
	// ON where it says so and where it says nothing; only OFF turns it off.
	on := true
	switch {
	case p.at(TokON):
		p.advance()
	case p.atWords("OFF"):
		p.advance()
		on = false
	}
	prop := New("DataDeletionProperty", Arg{"on", on})
	if !p.match(TokL_PAREN) {
		return prop, nil
	}
	for !p.at(TokR_PAREN) {
		switch {
		case p.atWords("FILTER_COLUMN"):
			p.advance()
			if !p.match(TokEQ) {
				return nil, p.unsupported("FILTER_COLUMN without a column")
			}
			column, err := p.parseColumn()
			if err != nil {
				return nil, err
			}
			prop.Set("filter_column", column)
		case p.atWords("RETENTION_PERIOD"):
			p.advance()
			if !p.match(TokEQ) {
				return nil, p.unsupported("RETENTION_PERIOD without a period")
			}
			period, err := p.parseRetentionPeriod()
			if err != nil {
				return nil, err
			}
			prop.Set("retention_period", period)
		default:
			return nil, p.unsupported("a DATA_DELETION setting this port does not read")
		}
		if !p.match(TokCOMMA) {
			break
		}
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed DATA_DELETION settings")
	}
	return prop, nil
}

// parseRetentionPeriod reads `5 MONTHS` or `INFINITE`, which the reference
// keeps as one Var holding both words.
func (p *parser) parseRetentionPeriod() (*Expression, error) {
	text := ""
	if c := p.curr(); c != nil && c.Type == TokNUMBER {
		p.advance()
		text = c.Text + " "
	}
	unit := p.curr()
	if unit == nil {
		return nil, p.unsupported("a retention period with no unit")
	}
	p.advance()
	return New("Var", Arg{"this", text + unit.Text}), nil
}

// parseSystemVersioning reads `SYSTEM_VERSIONING=ON`, `=OFF`, and the
// parenthesised settings the ON form may carry. The `with_` flag records
// whether the property was written inside a WITH, because the writer puts that
// word back itself.
func (p *parser) parseSystemVersioning(inWith bool) (*Expression, error) {
	p.advance() // SYSTEM_VERSIONING
	p.match(TokEQ)
	prop := New("WithSystemVersioningProperty", Arg{"on", true}, Arg{"with_", inWith})
	if p.atWords("OFF") {
		p.advance()
		prop.Set("on", false)
		return prop, nil
	}
	if !p.match(TokON) {
		return nil, p.unsupported("SYSTEM_VERSIONING that is neither ON nor OFF")
	}
	if !p.match(TokL_PAREN) {
		return prop, nil
	}
	for !p.at(TokR_PAREN) {
		switch {
		case p.atWords("HISTORY_TABLE"):
			p.advance()
			if !p.match(TokEQ) {
				return nil, p.unsupported("HISTORY_TABLE without a table")
			}
			table, err := p.parseTableName()
			if err != nil {
				return nil, err
			}
			prop.Set("this", table)
		case p.atWords("DATA_CONSISTENCY_CHECK"):
			p.advance()
			if !p.match(TokEQ) {
				return nil, p.unsupported("DATA_CONSISTENCY_CHECK without a setting")
			}
			c := p.curr()
			if c == nil {
				return nil, p.unsupported("DATA_CONSISTENCY_CHECK without a setting")
			}
			p.advance()
			prop.Set("data_consistency", strings.ToUpper(c.Text))
		case p.atWords("HISTORY_RETENTION_PERIOD"):
			p.advance()
			if !p.match(TokEQ) {
				return nil, p.unsupported("HISTORY_RETENTION_PERIOD without a period")
			}
			period, err := p.parseRetentionPeriod()
			if err != nil {
				return nil, err
			}
			prop.Set("retention_period", period)
		default:
			return nil, p.unsupported("a SYSTEM_VERSIONING setting this port does not read")
		}
		if !p.match(TokCOMMA) {
			break
		}
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed SYSTEM_VERSIONING settings")
	}
	return prop, nil
}

// parseKeyValueProperty reads `k = v`, the property with no word of its own.
// The key keeps whatever it was written as -- a quoted string stays a literal,
// a bare word becomes a Var -- and so does the value.
func (p *parser) parseKeyValueProperty() (*Expression, error) {
	c := p.curr()
	if c == nil {
		return nil, p.unsupported("property without a name")
	}
	var key *Expression
	switch {
	case c.Type == TokSTRING:
		p.advance()
		key = New("Literal", Arg{"this", c.Text}, Arg{"is_string", true})
	case p.atIdentifier():
		p.advance()
		key = New("Var", Arg{"this", c.Text})
	default:
		return nil, p.unsupported("property whose name is not a word")
	}
	if !p.match(TokEQ) {
		return nil, p.unsupported("property without a value")
	}
	// A value that is a WORD is a Var, not a column: `WITH (allow_page_locks
	// = on)` says a setting is on, and ON is a keyword the expression reader
	// will not take at all. Only a LITERAL is read as one.
	value, err := p.parseSettingValue()
	if err != nil {
		return nil, err
	}
	return New("Property", Arg{"this", key}, Arg{"value", value}), nil
}

// parseSettingValue reads what a SETTING is set to: a literal as itself, and
// anything else as the bare word it was written as, in the case it was written
// in. Its sibling parsePropertyValue upper-cases the word, because the
// properties it reads are ones the reference upper-cases.
func (p *parser) parseSettingValue() (*Expression, error) {
	c := p.curr()
	if c == nil {
		return nil, p.unsupported("property without a value")
	}
	// Read it as an expression first: a string, a number, TRUE and NULL are
	// all values in their own right. What comes back as a bare COLUMN is not
	// one -- nothing is being selected here -- so the word is kept instead.
	mark := p.index
	value, err := p.parseExpression()
	if err != nil {
		p.index = mark
		if word := p.curr(); word != nil && word.Type != TokL_PAREN &&
			word.Type != TokCOMMA && word.Type != TokR_PAREN {
			p.advance()
			return New("Var", Arg{"this", word.Text}), nil
		}
		return nil, p.unsupported("property whose value is not a word")
	}
	if value != nil && value.Class == "Column" {
		if name, ok := bareColumnName(value); ok {
			return New("Var", Arg{"this", name}), nil
		}
	}
	return value, nil
}

// bareColumnName is the text of a column that names nothing but itself -- no
// table, no database, no quotes -- which is the shape a bare WORD takes.
func bareColumnName(e *Expression) (string, bool) {
	for _, key := range []string{"table", "db", "catalog"} {
		if part, _ := e.Args[key].(*Expression); part != nil {
			return "", false
		}
	}
	id, _ := e.Args["this"].(*Expression)
	if id == nil || id.Class != "Identifier" {
		return "", false
	}
	if quoted, _ := id.Args["quoted"].(bool); quoted {
		return "", false
	}
	name, _ := id.Args["this"].(string)
	return name, name != ""
}

// parseProperty reads what follows a property's word, in the shape the probe
// recorded for it.
func (p *parser) parseProperty(spec PropertySpec) (*Expression, error) {
	switch spec.Shape {
	case "bare":
		return New(spec.Class), nil
	case "value":
		if spec.Equals {
			p.match(TokEQ)
		}
		value, err := p.parsePropertyValue()
		if err != nil {
			return nil, err
		}
		return New(spec.Class, Arg{"this", value}), nil
	case "table":
		table, err := p.parseTableName()
		if err != nil {
			return nil, err
		}
		return New(spec.Class, Arg{"this", table}), nil
	case "schema":
		if spec.Equals {
			p.match(TokEQ)
		}
		// The property need not carry a LIST at all: Postgres slices a table
		// by an expression over the key -- `PARTITION BY RANGE(population)`
		// -- and what stands there is then a field rather than a schema.
		if !p.at(TokL_PAREN) {
			// A word that takes a NAME before its list reads the name here
			// and the list below: `ON b (c)` names a partition scheme and
			// the column it splits by, and reading the whole of it as one
			// call wrote it back upper-cased and without its space.
			if spec.Named {
				name, err := p.parseIdentifier()
				if err != nil {
					return nil, err
				}
				if !p.at(TokL_PAREN) {
					return New(spec.Class, Arg{"this", name}), nil
				}
				columns, err := p.parseSchemaProperty()
				if err != nil {
					return nil, err
				}
				return New(spec.Class, Arg{"this",
					New("Schema", Arg{"this", name}, Arg{"expressions", columns})}), nil
			}
			field, err := p.parseBitwise()
			if err != nil {
				return nil, err
			}
			return New(spec.Class, Arg{"this", field}), nil
		}
		// The columns keep their types where they were written with them:
		// `PARTITIONED BY (a INT)` is a Schema of ColumnDefs, and the same
		// property over bare names is a Schema of Identifiers.
		columns, err := p.parseSchemaProperty()
		if err != nil {
			return nil, err
		}
		return New(spec.Class, Arg{"this", New("Schema", Arg{"expressions", columns})}), nil
	case "wrapped-columns", "wrapped-tables":
		items, err := p.parseWrappedPropertyList(spec.Shape == "wrapped-tables")
		if err != nil {
			return nil, err
		}
		return New(spec.Class, Arg{"expressions", items}), nil
	}
	return nil, p.unsupported("property " + spec.Class)
}

// parsePropertyValue reads the value a property carries: a string stays a
// literal and a bare word becomes a Var, which is what the reference builds
// for a word that names nothing in particular.
func (p *parser) parsePropertyValue() (*Expression, error) {
	c := p.curr()
	if c == nil {
		return nil, p.unsupported("property without a value")
	}
	if c.Type == TokSTRING {
		p.advance()
		return New("Literal", Arg{"this", c.Text}, Arg{"is_string", true}), nil
	}
	if !p.atIdentifier() {
		return nil, p.unsupported("property whose value is not a word")
	}
	p.advance()
	return New("Var", Arg{"this", strings.ToUpper(c.Text)}), nil
}

// parseWrappedPropertyList reads a parenthesised list of names, as columns or
// as tables depending on what the property holds.
func (p *parser) parseWrappedPropertyList(tables bool) ([]*Expression, error) {
	if !p.match(TokL_PAREN) {
		return nil, p.unsupported("property without a list")
	}
	var out []*Expression
	for {
		if tables {
			table, err := p.parseTableName()
			if err != nil {
				return nil, err
			}
			out = append(out, table)
		} else {
			column, err := p.parseColumn()
			if err != nil {
				return nil, err
			}
			out = append(out, column)
		}
		if !p.match(TokCOMMA) {
			break
		}
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed property list")
	}
	return out, nil
}

// parseSchemaProperty reads the parenthesised list a property carries as a
// Schema. A name written with a TYPE is a ColumnDef and one written without is
// a bare Identifier -- `PARTITIONED BY (a INT)` and `PARTITIONED BY (a)` are
// the same property over two different shapes.
func (p *parser) parseSchemaProperty() ([]*Expression, error) {
	if !p.match(TokL_PAREN) {
		return nil, p.unsupported("property without a column list")
	}
	var out []*Expression
	for {
		name, err := p.parseIdentifier()
		if err != nil {
			return nil, err
		}
		if p.at(TokR_PAREN) || p.at(TokCOMMA) {
			out = append(out, name)
		} else {
			kind, err := p.parseDataType()
			if err != nil {
				return nil, err
			}
			out = append(out, New("ColumnDef", Arg{"this", name}, Arg{"kind", kind}))
		}
		if !p.match(TokCOMMA) {
			break
		}
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed property column list")
	}
	return out, nil
}

// parseWrappedCSV reads `(a, b, c)` where each member is read by the caller's
// own function. The list is the shape; what a member MAY be differs from one
// clause to the next, which is why the reader is passed in.
func (p *parser) parseWrappedCSV(member func() (*Expression, error)) ([]*Expression, error) {
	if !p.match(TokL_PAREN) {
		return nil, p.unsupported("a list without its parentheses")
	}
	var out []*Expression
	for {
		e, err := member()
		if err != nil {
			return nil, err
		}
		out = append(out, e)
		if !p.match(TokCOMMA) {
			break
		}
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed list")
	}
	return out, nil
}

// parsePartitionedOf reads Postgres's `PARTITION OF <parent> <bound>`, which
// says the table being created holds one slice of another table's rows.
//
// It is a property rather than a clause of its own because the reference keeps
// it beside PARTITIONED BY in the same list -- and a table may carry both, the
// first saying which slice of its parent it is and the second saying how it is
// sliced further.
func (p *parser) parsePartitionedOf() (*Expression, error) {
	p.advance() // PARTITION
	p.advance() // OF

	parent, err := p.parseTableName()
	if err != nil {
		return nil, err
	}
	// The parent may be named WITH columns -- `PARTITION OF measurement
	// (unitsales DEFAULT 0)` -- which are this slice's overrides of the
	// parent's definitions. They hang off the parent, not off the new table.
	this := parent
	if p.at(TokL_PAREN) {
		columns, err := p.parseColumnDefs()
		if err != nil {
			return nil, err
		}
		this = New("Schema", Arg{"this", parent}, Arg{"expressions", columns})
	}

	var bound *Expression
	switch {
	case p.match(TokDEFAULT):
		// The slice that takes whatever the other slices do not.
		bound = New("Var", Arg{"this", "DEFAULT"})
	case p.atWords("FOR", "VALUES"):
		p.advance()
		p.advance()
		if bound, err = p.parsePartitionBoundSpec(); err != nil {
			return nil, err
		}
	default:
		return nil, p.unsupported("PARTITION OF without DEFAULT or FOR VALUES")
	}
	return New("PartitionedOfProperty", Arg{"this", this}, Arg{"expression", bound}), nil
}

// parsePartitionBoundSpec reads which rows a partition holds, in one of the
// three spellings Postgres allows: a list of values, a half-open range, or a
// hash of the key. One node carries all three, and which of its four arguments
// are set says which spelling was written.
func (p *parser) parsePartitionBoundSpec() (*Expression, error) {
	// The bounds of a range may be the ends of the key's order rather than
	// values in it. MINVALUE and MAXVALUE are words, not columns -- reading
	// them as columns would make them refer to something.
	bound := func() (*Expression, error) {
		for _, word := range []string{"MINVALUE", "MAXVALUE"} {
			if p.atWords(word) {
				p.advance()
				return New("Var", Arg{"this", word}), nil
			}
		}
		return p.parseBitwise()
	}

	switch {
	case p.match(TokIN):
		values, err := p.parseWrappedCSV(p.parseBitwise)
		if err != nil {
			return nil, err
		}
		return New("PartitionBoundSpec", Arg{"this", values}), nil

	case p.match(TokFROM):
		from, err := p.parseWrappedCSV(bound)
		if err != nil {
			return nil, err
		}
		if !p.atWords("TO") {
			return nil, p.unsupported("a partition range without TO")
		}
		p.advance()
		to, err := p.parseWrappedCSV(bound)
		if err != nil {
			return nil, err
		}
		return New("PartitionBoundSpec",
			Arg{"from_expressions", from}, Arg{"to_expressions", to}), nil

	case p.atWords("WITH"):
		// `WITH (MODULUS 3, REMAINDER 2)`: the two numbers name a slice of a
		// hash rather than a range of the key. The words between them are
		// fixed, so the node keeps only the numbers.
		p.advance()
		if !p.match(TokL_PAREN) || !p.atWords("MODULUS") {
			return nil, p.unsupported("a hash partition without MODULUS")
		}
		p.advance()
		modulus, err := p.parseBitwise()
		if err != nil {
			return nil, err
		}
		if !p.match(TokCOMMA) || !p.atWords("REMAINDER") {
			return nil, p.unsupported("a hash partition without REMAINDER")
		}
		p.advance()
		remainder, err := p.parseBitwise()
		if err != nil {
			return nil, err
		}
		if !p.match(TokR_PAREN) {
			return nil, p.unsupported("unclosed hash partition")
		}
		return New("PartitionBoundSpec",
			Arg{"this", modulus}, Arg{"expression", remainder}), nil
	}
	return nil, p.unsupported("a partition bound this port does not read")
}

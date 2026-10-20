package sqlglot

import "strings"

// Teradata's CREATE TABLE reads a list of settings BETWEEN its name and its
// columns, after a comma:
//
//	CREATE TABLE a, NO FALLBACK, NO LOG, FREESPACE=1 PERCENT (a INT)
//
// The reference has a reader of its own for them -- `_parse_property_before`,
// annotated "only used for teradata" -- because most of the words that open
// one are FLAGS on the property that follows rather than properties: NO,
// DUAL, BEFORE, LOCAL, AFTER, MINIMUM, MAXIMUM and DEFAULT carry into
// JOURNAL, FALLBACK, DATABLOCKSIZE and MERGEBLOCKRATIO.
//
// Every flag is read whether or not the word after it uses it, which is what
// the reference does: they are collected first and handed to whichever parser
// the next word names.
func (p *parser) parseTablePropertiesBefore() ([]*Expression, error) {
	var out []*Expression
	for {
		prop, err := p.parsePropertyBefore()
		if err != nil {
			return nil, err
		}
		if prop == nil {
			return out, nil
		}
		out = append(out, prop)
	}
}

// parsePropertyBefore reads one, comma and flags included.
func (p *parser) parsePropertyBefore() (*Expression, error) {
	p.match(TokCOMMA)
	no := p.matchUnquotedWord("NO")
	dual := p.matchUnquotedWord("DUAL")
	before := p.matchUnquotedWord("BEFORE")
	def := p.matchUnquotedWord("DEFAULT")
	local := ""
	if p.atWords("NOT", "LOCAL") {
		p.advance()
		p.advance()
		local = "NOT LOCAL"
	} else if p.matchUnquotedWord("LOCAL") {
		local = "LOCAL"
	}
	after := p.matchUnquotedWord("AFTER")
	minimum := p.matchUnquotedWord("MINIMUM") || p.matchUnquotedWord("MIN")
	maximum := p.matchUnquotedWord("MAXIMUM") || p.matchUnquotedWord("MAX")

	switch {
	case p.atWords("JOURNAL"):
		p.advance()
		// In the order the reference builds them, which is the order the
		// flags are read in (Parser._parse_property_before's kwargs).
		var args []Arg
		if no {
			args = append(args, Arg{"no", true})
		}
		if dual {
			args = append(args, Arg{"dual", true})
		}
		if before {
			args = append(args, Arg{"before", true})
		}
		if local != "" {
			args = append(args, Arg{"local", local})
		}
		if after {
			args = append(args, Arg{"after", true})
		}
		return New("JournalProperty", args...), nil

	case p.atWords("LOG"):
		p.advance()
		if no {
			return New("LogProperty", Arg{"no", true}), nil
		}
		return New("LogProperty"), nil

	case p.atWords("FALLBACK"):
		p.advance()
		protection := p.matchUnquotedWord("PROTECTION")
		var noArg any
		if no {
			noArg = true
		}
		return New("FallbackProperty", Arg{"no", noArg},
			Arg{"protection", protection}), nil

	case p.atWords("FREESPACE"):
		p.advance()
		p.match(TokEQ)
		size := p.parseNumberLiteral()
		percent := p.match(TokPERCENT)
		return New("FreespaceProperty", Arg{"this", size},
			Arg{"percent", percent}), nil

	case p.atWords("CHECKSUM"):
		p.advance()
		p.match(TokEQ)
		// ON and OFF are both recorded: OFF is the value FALSE rather than
		// the absence of one (Parser._parse_checksum).
		var on any
		if p.matchUnquotedWord("ON") {
			on = true
		} else if p.matchUnquotedWord("OFF") {
			on = false
		}
		// `default` is the MATCH itself, so it is recorded as false where it
		// was not written rather than left out (Parser._parse_checksum).
		return New("ChecksumProperty", Arg{"on", on},
			Arg{"default", p.matchUnquotedWord("DEFAULT")}), nil

	case p.atWords("MERGEBLOCKRATIO"):
		p.advance()
		if p.match(TokEQ) {
			size := p.parseNumberLiteral()
			return New("MergeBlockRatioProperty", Arg{"this", size},
				Arg{"percent", p.match(TokPERCENT)}), nil
		}
		// Both are recorded, and `default` as the match itself rather than
		// only when set: `NO MERGEBLOCKRATIO` says default FALSE
		// (Parser._parse_mergeblockratio).
		var noArg any
		if no {
			noArg = true
		}
		return New("MergeBlockRatioProperty", Arg{"no", noArg},
			Arg{"default", def}), nil

	case p.atWords("DATABLOCKSIZE"):
		p.advance()
		p.match(TokEQ)
		size := p.parseNumberLiteral()
		units := ""
		for _, u := range []string{"BYTES", "KBYTES", "KILOBYTES"} {
			if c := p.curr(); c != nil && c.Type != TokIDENTIFIER &&
				strings.EqualFold(c.Text, u) {
				units = c.Text
				p.advance()
				break
			}
		}
		// The reference sets every one of these, so an absent flag is a
		// present nil rather than a missing key.
		var unitsArg, defArg, minArg, maxArg any
		if units != "" {
			unitsArg = units
		}
		var sizeArg any
		if size != nil {
			sizeArg = size
		}
		if def {
			defArg = true
		}
		if minimum {
			minArg = true
		}
		if maximum {
			maxArg = true
		}
		return New("DataBlocksizeProperty", Arg{"size", sizeArg},
			Arg{"units", unitsArg}, Arg{"default", defArg},
			Arg{"minimum", minArg}, Arg{"maximum", maxArg}), nil

	case p.atWords("BLOCKCOMPRESSION"):
		p.advance()
		p.match(TokEQ)
		var always, manual, never, isDefault bool
		var autotemp *Expression
		switch {
		case p.atWords("AUTOTEMP"):
			p.advance()
			// The columns inside the parentheses are a SCHEMA, read the same
			// way a table's own are (Parser._parse_blockcompression).
			columns, err := p.parseColumnDefs()
			if err != nil {
				return nil, err
			}
			autotemp = New("Schema", Arg{"expressions", columns})
		case p.matchUnquotedWord("ALWAYS"):
			always = true
		case p.matchUnquotedWord("DEFAULT"):
			isDefault = true
		case p.matchUnquotedWord("MANUAL"):
			manual = true
		case p.matchUnquotedWord("NEVER"):
			never = true
		}
		return New("BlockCompressionProperty", Arg{"always", always},
			Arg{"manual", manual}, Arg{"never", never},
			Arg{"default", isDefault}, Arg{"autotemp", autotemp}), nil

	case p.atWords("WITH"):
		p.advance()
		// `WITH JOURNAL TABLE=x.y.z` names the table the journal lives in.
		if p.matchUnquotedWord("JOURNAL") {
			p.match(TokTABLE)
			p.match(TokEQ)
			table, err := p.parseTableName()
			if err != nil {
				return nil, err
			}
			return New("WithJournalTableProperty", Arg{"this", table}), nil
		}
		// `WITH [NO] [CONCURRENT] ISOLATED LOADING [FOR ALL]` says which
		// writes are kept apart from reads. The flags here are read AFTER the
		// WITH rather than before it (Parser._parse_withisolatedloading).
		isoNo := p.matchUnquotedWord("NO")
		concurrent := p.matchUnquotedWord("CONCURRENT")
		if !p.atWords("ISOLATED", "LOADING") {
			return nil, p.unsupported("WITH something that is not a setting")
		}
		p.advance()
		p.advance()
		var target *Expression
		for _, option := range []string{"ALL", "INSERT", "NONE"} {
			if p.atWords("FOR") && p.next() != nil &&
				strings.EqualFold(p.next().Text, option) {
				p.advance()
				p.advance()
				// The Var carries the WORDS as written, FOR included: the
				// writer then adds its own space in front of them
				// (Parser._parse_var_from_options).
				target = New("Var", Arg{"this", "FOR " + strings.ToUpper(option)})
				break
			}
		}
		return New("IsolatedLoadingProperty", Arg{"no", isoNo},
			Arg{"concurrent", concurrent}, Arg{"target", target}), nil
	}
	return nil, nil
}

// parseNumberLiteral reads a bare NUMBER as the literal the reference builds
// for it. Anything else is no number at all, which is what the flag-only
// forms of these properties look like.
func (p *parser) parseNumberLiteral() *Expression {
	c := p.curr()
	if c == nil || c.Type != TokNUMBER {
		return nil
	}
	p.advance()
	return New("Literal", Arg{"this", c.Text}, Arg{"is_string", false})
}

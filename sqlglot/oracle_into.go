package sqlglot

// The reference's token for this clause is one word, BULK_COLLECT_INTO.
func spellOracleBulkCollect() {
	dialectConfigs["oracle"].Keywords["BULK COLLECT INTO"] = TokBULK_COLLECT_INTO
	dialectConfigs["oracle"].trie = nil
}

// oracleBulkCollect reads BULK COLLECT INTO after the projections. The
// clause is one token, so it is not a name and not an alias.
func (p *parser) oracleBulkCollect(sel *Expression) error {
	if p.dialect != "oracle" || sel == nil || sel.Args["into"] != nil || !p.at(TokBULK_COLLECT_INTO) {
		return nil
	}
	p.advance()
	var cols []*Expression
	for {
		col, err := p.parseColumn()
		if err != nil {
			return err
		}
		if _, set := col.Args["join_mark"]; !set && p.tables.SupportsColumnJoinMarks {
			col.Set("join_mark", false)
		}
		cols = append(cols, col)
		if !p.match(TokCOMMA) {
			break
		}
	}
	into := New("Into", Arg{"bulk_collect", true})
	if len(cols) == 1 {
		id, _ := cols[0].Args["this"].(*Expression)
		into = New("Into", Arg{"this", New("Table", Arg{"this", id})}, Arg{"bulk_collect", true})
	} else {
		into.Set("expressions", cols)
	}
	sel.Set("into", into)
	if p.at(TokFROM) {
		from, err := p.parseFrom()
		if err != nil {
			return err
		}
		sel.Set("from_", from)
	}
	joins, err := p.parseJoins()
	if err != nil {
		return err
	}
	for _, j := range joins {
		sel.Append("joins", j)
	}
	return p.parseQueryModifiers(sel)
}

func init() {
	generators["Into"] = (*generator).writeOracleInto
}

func (g *generator) writeOracleInto(e *Expression) string {
	bulk, _ := e.Args["bulk_collect"].(bool)
	if g.dialect != "oracle" || !bulk {
		return g.writeInto(e)
	}
	names := g.child(e, "this")
	if names == "" {
		names = g.list(e)
	}
	return "BULK COLLECT INTO " + names
}

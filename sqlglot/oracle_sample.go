package sqlglot

import "maps"

// Oracle spells a table sample as SAMPLE, counts a bare size as a
// percent, and writes the alias after the clause.
func init() {
	cfg := dialectConfigs["oracle"]
	if cfg == nil {
		return
	}
	keys := maps.Clone(cfg.Keywords)
	keys["SAMPLE"] = TokTABLE_SAMPLE
	cfg.Keywords = keys
	cfg.trie = nil

	tables := parserTables["oracle"]
	if tables == nil {
		return
	}
	tables.BareSampleCountIsPercent = true
	sample := tables.TableSample
	sample.Keywords = "SAMPLE"
	sample.SizeIsRows = false
	sample.SizeIsPercent = true
	tables.TableSample = sample
}

// oracleSampleAlias reads the alias Oracle writes after SAMPLE.
// Every other dialect already took the alias, before the clause.
func (p *parser) oracleSampleAlias(table *Expression) error {
	if p.dialect != "oracle" || table == nil || table.Args["alias"] != nil {
		return nil
	}
	alias, err := p.parseTableAlias()
	if err != nil || alias == nil {
		return err
	}
	table.Set("alias", alias)
	return nil
}

// atSampleSeed reports the dialect's own seed word when a parenthesised
// seed follows it. REPEATABLE is the other spelling, checked beside this.
func (p *parser) atSampleSeed() bool {
	word := p.tables.TableSample.SeedKeyword
	return word != "" && p.atWords(word) && p.next() != nil && p.next().Type == TokL_PAREN
}

// writeTableSpelling writes a table. Oracle puts the sample before the
// alias, so the alias is held off the shared writer and put back after.
func (g *generator) writeTableSpelling(e *Expression) string {
	alias, _ := e.Args["alias"].(*Expression)
	if g.dialect != "oracle" || alias == nil || e.Args["sample"] == nil {
		return g.writeTable(e)
	}
	e.Set("alias", nil)
	defer e.Set("alias", alias)
	return g.writeTable(e) + " " + g.node(alias)
}

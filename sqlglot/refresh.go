package sqlglot

// parseRefresh reads REFRESH [EXTERNAL TABLE | TABLE | MATERIALIZED VIEW] target.
// A REFRESH with no kind and a target that is not a string is the whole
// statement kept as a command: building a Refresh there would name a table
// the statement never named.
func (p *parser) parseRefresh() (*Expression, error) {
	start := *p.curr()
	p.advance()
	kind := ""
	switch {
	case p.takeWords("EXTERNAL", "TABLE"):
		kind = "EXTERNAL TABLE"
	case p.match(TokTABLE):
		kind = "TABLE"
	case p.takeWords("MATERIALIZED", "VIEW"):
		kind = "MATERIALIZED VIEW"
	}
	// StarRocks names the materialized view with its parts alone -- its own
	// PARTITION START/END clause follows, and reading a whole table would
	// take PARTITION for a partition selection (StarRocksParser._parse_refresh).
	starrocksMV := p.dialect == "starrocks" && kind == "MATERIALIZED VIEW"
	var this *Expression
	if p.at(TokSTRING) {
		tok := p.curr()
		p.advance()
		this = New("Literal", Arg{"this", tok.Text}, Arg{"is_string", true})
	} else {
		var err error
		if starrocksMV {
			this, err = p.parseTableName()
		} else {
			this, err = p.parseTable()
		}
		if err != nil {
			return nil, err
		}
	}
	if kind == "" && this.Class != "Literal" {
		return p.parseAsCommand(start), nil
	}
	if starrocksMV {
		var partitionStart, partitionEnd *Expression
		if p.takeWords("PARTITION", "START") {
			var err error
			partitionStart, err = p.parseRefreshPartitionBound()
			if err != nil {
				return nil, err
			}
			if p.takeWords("END") {
				partitionEnd, err = p.parseRefreshPartitionBound()
				if err != nil {
					return nil, err
				}
			}
		}
		force := p.takeWords("FORCE")
		var mode any
		switch {
		case p.takeWords("WITH", "SYNC", "MODE"):
			mode = "SYNC"
		case p.takeWords("WITH", "ASYNC", "MODE"):
			mode = "ASYNC"
		}
		return New("Refresh",
			Arg{"this", this}, Arg{"kind", kind},
			Arg{"partition_start", partitionStart}, Arg{"partition_end", partitionEnd},
			Arg{"force", force}, Arg{"mode", mode}), nil
	}
	return New("Refresh", Arg{"this", this}, Arg{"kind", kind}), nil
}

// parseRefreshPartitionBound reads one `('2020-02-01')` bound.
func (p *parser) parseRefreshPartitionBound() (*Expression, error) {
	if !p.match(TokL_PAREN) {
		return nil, p.unsupported("PARTITION START without a parenthesised bound")
	}
	lit := p.tryParseStringLiteral()
	if lit == nil {
		return nil, p.unsupported("PARTITION bound that is not a string")
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed PARTITION bound")
	}
	return lit, nil
}

func (g *generator) writeRefresh(e *Expression) string {
	this := g.child(e, "this")
	target, _ := e.Args["this"].(*Expression)
	if target != nil && target.Class == "Literal" {
		return "REFRESH " + this
	}
	kind, _ := e.Args["kind"].(string)
	if kind == "" {
		return "REFRESH " + this
	}
	// StarRocks' own clause: partition bounds, FORCE, and the sync mode
	// (StarRocksGenerator.refresh_sql).
	if g.dialect == "starrocks" {
		out := "REFRESH " + kind + " " + this
		if start := g.child(e, "partition_start"); start != "" {
			out += " PARTITION START (" + start + ")"
			if end := g.child(e, "partition_end"); end != "" {
				out += " END (" + end + ")"
			}
		}
		if e.Args["force"] == true {
			out += " FORCE"
		}
		if mode, _ := e.Args["mode"].(string); mode != "" {
			out += " WITH " + mode + " MODE"
		}
		return out
	}
	return "REFRESH " + kind + " " + this
}

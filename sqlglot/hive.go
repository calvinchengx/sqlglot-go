package sqlglot

import "fmt"

// isHiveFamily reports whether a dialect inherits Hive's own parser and
// generator behaviour -- Hive itself, and the two Spark subclasses that add
// to it rather than replacing it.
func isHiveFamily(dialect string) bool {
	return dialect == "hive" || dialect == "spark2" || dialect == "spark"
}

// hiveParameterReader reports the dialects whose parameter reader is Hive's.
// It is NOT the same set as isHiveFamily: Databricks reaches it through Spark,
// and that reader is the one that records whether a `:` and a second name
// followed as FALSE rather than leaving the argument out
// (HiveParser._parse_parameter).
func hiveParameterReader(dialect string) bool {
	return isHiveFamily(dialect) || dialect == "databricks"
}

// isTrueBoolean reports whether a node is the literal TRUE.
func isTrueBoolean(e *Expression) bool {
	if e == nil || e.Class != "Boolean" {
		return false
	}
	b, _ := e.Args["this"].(bool)
	return b
}

// hiveOwnedFunction reports the names buildHiveFunction builds by hand, so the
// generic dispatch does not turn them away for having no recorded signature.
func hiveOwnedFunction(upper string) bool {
	switch upper {
	case "STRUCT", "NAMED_STRUCT", "INITCAP",
		"FIRST", "FIRST_VALUE", "LAST", "LAST_VALUE", "ANY_VALUE",
		"GET_JSON_OBJECT":
		return true
	}
	return false
}

// buildHiveFunction builds the Hive functions whose shape the generic
// signature-driven builder cannot express (HiveParser.FUNCTIONS and its own
// _to_prop_eq). It returns nil for anything it does not own.
func (p *parser) buildHiveFunction(upper string, args []*Expression) *Expression {
	switch upper {
	case "STRUCT":
		// Hive names each unnamed member `colN` (HiveParser._to_prop_eq), so
		// `STRUCT(1, 2)` holds PropertyEQ(col1, 1), PropertyEQ(col2, 2). A
		// member that is already a COLUMN keeps its own name as the key.
		items := make([]*Expression, 0, len(args))
		for i, e := range args {
			if e == nil || e.Class == "PropertyEQ" || e.Class == "Star" {
				items = append(items, e)
				continue
			}
			var key *Expression
			if e.Class == "Column" {
				key, _ = e.Args["this"].(*Expression)
			} else {
				key = New("Identifier", Arg{"this", fmt.Sprintf("col%d", i+1)}, Arg{"quoted", false})
			}
			items = append(items, New("PropertyEQ", Arg{"this", key}, Arg{"expression", e}))
		}
		return New("Struct", Arg{"expressions", items})

	case "FIRST", "FIRST_VALUE", "LAST", "LAST_VALUE":
		// Hive's build_with_ignore_nulls: a second TRUE wraps the aggregate
		// in IgnoreNulls, and a second FALSE leaves it bare.
		if len(args) >= 1 {
			class := map[string]string{
				"FIRST": "First", "FIRST_VALUE": "FirstValue",
				"LAST": "Last", "LAST_VALUE": "LastValue",
			}[upper]
			this := New(class, Arg{"this", args[0]})
			if len(args) > 1 && isTrueBoolean(args[1]) {
				return New("IgnoreNulls", Arg{"this", this})
			}
			return this
		}

	case "ANY_VALUE":
		// Spark's own build_with_ignore_nulls entry; Hive and Spark2 build
		// it through the generic signature.
		if (p.dialect == "spark" || p.dialect == "spark2") && len(args) >= 1 {
			this := New("AnyValue", Arg{"this", args[0]})
			if len(args) > 1 && isTrueBoolean(args[1]) {
				return New("IgnoreNulls", Arg{"this", this})
			}
			return this
		}

	case "GET_JSON_OBJECT":
		if len(args) == 2 {
			// The reference runs the path through the dialect's own
			// to_json_path, which PARSES a string into a JSONPath.
			expr := args[1]
			if isStringLiteral(expr) {
				text, _ := expr.Args["this"].(string)
				if parsed, err := parseJSONPath(text, true); err == nil && parsed != nil {
					expr = parsed
				}
			}
			return New("JSONExtractScalar",
				Arg{"this", args[0]}, Arg{"expression", expr})
		}

	case "NAMED_STRUCT":
		// Spark's NAMED_STRUCT('k', v, ...) is a Struct of PropertyEQ pairs
		// (Spark2Parser._build_named_struct).
		var items []*Expression
		for i := 0; i+1 < len(args); i += 2 {
			key := args[i]
			if key == nil {
				continue
			}
			items = append(items, New("PropertyEQ",
				Arg{"this", New("Identifier", Arg{"this", key.Name()}, Arg{"quoted", false})},
				Arg{"expression", args[i+1]}))
		}
		return New("Struct", Arg{"expressions", items})

	case "INITCAP":
		// Hive's own default delimiter set, which is whitespace rather than
		// the base dialect's punctuation.
		if len(args) >= 1 {
			delimiter := argAt(args, 1)
			if delimiter == nil {
				delimiter = New("Literal",
					Arg{"this", " \t\n\r\f\v\x1c\x1d\x1e\x1f"}, Arg{"is_string", true})
			}
			return New("Initcap", Arg{"this", args[0]}, Arg{"expression", delimiter})
		}
	}
	return nil
}

// sparkJoinHint reports the names Spark2 and Spark read as a join hint rather
// than as a function call (Spark2Parser.FUNCTION_PARSERS).
func sparkJoinHint(upper string) bool {
	switch upper {
	case "BROADCAST", "BROADCASTJOIN", "MAPJOIN", "MERGE", "SHUFFLEMERGE",
		"MERGEJOIN", "SHUFFLE_HASH", "SHUFFLE_REPLICATE_NL":
		return true
	}
	return false
}

// parseJoinHintFunction reads `NAME(table, ...)` into a JoinHint. The name is
// already consumed.
func (p *parser) parseJoinHintFunction(name string) (*Expression, error) {
	if !p.match(TokL_PAREN) {
		return nil, p.unsupported("a join hint without its tables")
	}
	var args []*Expression
	if !p.at(TokR_PAREN) {
		for {
			table, err := p.parseTable()
			if err != nil {
				return nil, err
			}
			args = append(args, table)
			if !p.match(TokCOMMA) {
				break
			}
		}
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed join hint")
	}
	return New("JoinHint", Arg{"this", name}, Arg{"expressions", args}), nil
}

// parseClusteredBy reads `CLUSTERED BY (cols) [SORTED BY (cols)] INTO n
// BUCKETS` (the reference's _parse_clustered_by). The leading words are
// already current.
func (p *parser) parseClusteredBy() (*Expression, error) {
	p.advance() // CLUSTERED
	p.advance() // BY
	if !p.match(TokL_PAREN) {
		return nil, p.unsupported("CLUSTERED BY without its columns")
	}
	var exprs []*Expression
	for !p.at(TokR_PAREN) {
		col, err := p.parseColumn()
		if err != nil {
			return nil, err
		}
		exprs = append(exprs, col)
		if !p.match(TokCOMMA) {
			break
		}
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed CLUSTERED BY")
	}
	var sortedBy []*Expression
	if p.atWords("SORTED", "BY") {
		p.advance()
		p.advance()
		if !p.match(TokL_PAREN) {
			return nil, p.unsupported("SORTED BY without its columns")
		}
		for !p.at(TokR_PAREN) {
			ordered, err := p.parseOrderedColumn()
			if err != nil {
				return nil, err
			}
			sortedBy = append(sortedBy, ordered)
			if !p.match(TokCOMMA) {
				break
			}
		}
		if !p.match(TokR_PAREN) {
			return nil, p.unsupported("unclosed SORTED BY")
		}
	}
	if !p.match(TokINTO) {
		return nil, p.unsupported("CLUSTERED BY without INTO")
	}
	c := p.curr()
	if c == nil || c.Type != TokNUMBER {
		return nil, p.unsupported("CLUSTERED BY without a bucket count")
	}
	buckets := New("Literal", Arg{"this", c.Text}, Arg{"is_string", false})
	p.advance()
	p.matchUnquotedWord("BUCKETS")
	args := []Arg{{"expressions", exprs}}
	if len(sortedBy) > 0 {
		args = append(args, Arg{"sorted_by", sortedBy})
	}
	args = append(args, Arg{"buckets", buckets})
	return New("ClusteredByProperty", args...), nil
}

// parseOrderedColumn reads one column of a SORTED BY list.
func (p *parser) parseOrderedColumn() (*Expression, error) {
	this, err := p.parseColumn()
	if err != nil {
		return nil, err
	}
	var desc *bool
	switch {
	case p.match(TokDESC):
		b := true
		desc = &b
	case p.match(TokASC):
		b := false
		desc = &b
	}
	nullsFirst, saidSo := false, false
	if p.atWords("NULLS", "FIRST") {
		p.advance()
		p.advance()
		nullsFirst, saidSo = true, true
	} else if p.atWords("NULLS", "LAST") {
		p.advance()
		p.advance()
		nullsFirst, saidSo = false, true
	}
	if !saidSo {
		nullsFirst = p.nullsFirst(desc != nil && *desc)
	}
	args := []Arg{{"this", this}}
	if desc != nil {
		args = append(args, Arg{"desc", *desc})
	}
	args = append(args, Arg{"nulls_first", nullsFirst})
	return New("Ordered", args...), nil
}

// buildHiveTransform reads Hive's `TRANSFORM(args) [USING script [AS schema]]`
// (HiveParser._parse_transform). The name is already consumed. It returns nil
// when the call is not a TRANSFORM, so the caller reads it as a function.
func (p *parser) buildHiveTransform() (*Expression, error) {
	if !p.at(TokL_PAREN) {
		return nil, nil
	}
	args, err := p.parseWrappedCSV(p.parseLambdaOrExpression)
	if err != nil {
		// Not a TRANSFORM this reader can take: leave it to the generic
		// signature rather than refusing the statement here.
		return nil, nil
	}
	// A ROW FORMAT may stand between the arguments and USING, and another
	// after the schema.
	rowFormatBefore := p.parseRowFormatIfAny()
	if !p.match(TokUSING) {
		// No USING: this is the ordinary TRANSFORM, built by the generic
		// signature rather than here.
		return nil, nil
	}
	c := p.curr()
	if c == nil || c.Type != TokSTRING {
		return nil, p.unsupported("TRANSFORM USING without a script")
	}
	script := New("Literal", Arg{"this", c.Text}, Arg{"is_string", true})
	p.advance()
	p.match(TokALIAS)
	var schema *Expression
	if p.at(TokL_PAREN) {
		cols, err := p.parseColumnDefs()
		if err != nil {
			return nil, err
		}
		schema = New("Schema", Arg{"expressions", cols})
	}
	rowFormatAfter := p.parseRowFormatIfAny()
	return New("QueryTransform",
		Arg{"expressions", args}, Arg{"command_script", script}, Arg{"schema", schema},
		Arg{"row_format_before", rowFormatBefore}, Arg{"row_format_after", rowFormatAfter}), nil
}

// parseRowFormatIfAny reads a ROW FORMAT where one stands, or nothing.
func (p *parser) parseRowFormatIfAny() *Expression {
	if !p.atWords("ROW", "FORMAT") {
		return nil
	}
	prop, err := p.parseRowFormat()
	if err != nil {
		return nil
	}
	return prop
}

// parseLambdaOrExpression reads a lambda where there is one and an ordinary
// expression otherwise, which is what Hive's TRANSFORM arguments are.
func (p *parser) parseLambdaOrExpression() (*Expression, error) {
	mark := p.index
	if lam, err := p.parseLambda(); err == nil && lam != nil {
		return lam, nil
	}
	p.index = mark
	return p.parseExpression()
}

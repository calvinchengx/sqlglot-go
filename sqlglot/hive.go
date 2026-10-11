package sqlglot

import "fmt"

// isHiveFamily reports whether a dialect inherits Hive's own parser and
// generator behaviour -- Hive itself, and the two Spark subclasses that add
// to it rather than replacing it.
func isHiveFamily(dialect string) bool {
	return dialect == "hive" || dialect == "spark2" || dialect == "spark"
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
		"FIRST", "FIRST_VALUE", "LAST", "LAST_VALUE",
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

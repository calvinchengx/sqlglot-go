package sqlglot

import "strings"

// hiveNode writes the classes Hive's generator handles its own way
// (HiveGenerator.TRANSFORMS). The second return says whether the class was
// handled here.
func (g *generator) hiveNode(e *Expression) (string, bool) {
	// The rename family: a node written under a different name, with its own
	// arguments in their own order.
	rename := map[string]string{
		"AnyValue":       "FIRST",
		"ArrayConcat":    "CONCAT",
		"ArrayUniqueAgg": "COLLECT_SET",
		"DayOfMonth":     "DAYOFMONTH",
		"DayOfWeek":      "DAYOFWEEK",
		"FromBase64":     "UNBASE64",
		"IsNan":          "ISNAN",
		"NumberToStr":    "FORMAT_NUMBER",
		"Quantile":       "PERCENTILE",
		"ApproxQuantile": "PERCENTILE_APPROX",
		"RegexpSplit":    "SPLIT",
		"StarMap":        "MAP",
		"TimeStrToDate":  "TO_DATE",
		"TimeStrToUnix":  "UNIX_TIMESTAMP",
		"TimeToUnix":     "UNIX_TIMESTAMP",
		"ToBase64":       "BASE64",
		"Unicode":        "ASCII",
		"UnixToTimeStr":  "FROM_UNIXTIME",
		"WeekOfYear":     "WEEKOFYEAR",
	}
	if name, ok := rename[e.Class]; ok {
		// Spark2/Spark spell ANY_VALUE under its own name.
		if e.Class == "AnyValue" && g.dialect != "hive" {
			name = "ANY_VALUE"
		}
		return g.hiveCall(name, e), true
	}

	switch e.Class {
	case "TsOrDsAdd", "DateAdd":
		return "DATE_ADD(" + g.child(e, "this") + ", " + g.child(e, "expression") + ")", true
	case "DateSub":
		return "DATE_SUB(" + g.child(e, "this") + ", " + g.child(e, "expression") + ")", true
	case "DateDiff", "TsOrDsDiff":
		// Spark names the unit when one is given, and its two dates are
		// written expression-first: `DATEDIFF(MONTH, a, b)`.
		if g.dialect != "hive" {
			if u := g.child(e, "unit"); u != "" {
				return "DATEDIFF(" + u + ", " + g.child(e, "expression") + ", " + g.child(e, "this") + ")", true
			}
		}
		return "DATEDIFF(" + g.child(e, "this") + ", " + g.child(e, "expression") + ")", true
	case "Unnest":
		return "EXPLODE(" + g.list(e) + ")", true
	case "JSONExtract", "JSONExtractScalar":
		// Both are the same function, which names no scalar-ness.
		return "GET_JSON_OBJECT(" + g.child(e, "this") + ", " + g.child(e, "expression") + ")", true
	case "JSONFormat":
		return "TO_JSON(" + g.child(e, "this") + ")", true
	case "TsOrDsToDate":
		// Spark drops a date cast carrying only the default format.
		if g.dialect != "hive" {
			if g.child(e, "format") == "'yyyy-MM-dd'" {
				return "TO_DATE(" + g.child(e, "this") + ")", true
			}
		}
	case "MonthsBetween":
		parts := []string{g.child(e, "this"), g.child(e, "expression")}
		// Spark may name the roundoff flag; Hive never does.
		if ro := g.child(e, "roundoff"); ro != "" {
			parts = append(parts, ro)
		}
		return "MONTHS_BETWEEN(" + strings.Join(parts, ", ") + ")", true
	case "RegexpLike":
		return g.binary(e, "RLIKE"), true
	case "MD5Digest":
		return "UNHEX(MD5(" + g.child(e, "this") + "))", true
	case "PrimaryKeyColumnConstraint":
		return "PRIMARY KEY", true
	case "OnProperty", "NotForReplicationColumnConstraint":
		return "", true
	case "Create":
		if out, ok := g.hiveCreate(e); ok {
			return out, true
		}
	case "PartitionedByProperty":
		// Hive's PARTITIONED BY names the columns only, even where the
		// schema beside it restates their types.
		if g.dialect == "hive" {
			if schema, _ := e.Args["this"].(*Expression); schema != nil && schema.Class == "Schema" {
				var parts []string
				for _, c := range g.expressionsOf(schema) {
					if c != nil && c.Class == "ColumnDef" {
						parts = append(parts, g.child(c, "this"))
					} else {
						parts = append(parts, g.node(c))
					}
				}
				return "PARTITIONED BY (" + strings.Join(parts, ", ") + ")", true
			}
		}
		return "PARTITIONED BY " + g.child(e, "this"), true
	case "PartitionedByBucket":
		return "BUCKET(" + g.child(e, "expression") + ", " + g.child(e, "this") + ")", true
	case "PartitionByTruncate":
		return "TRUNCATE(" + g.child(e, "expression") + ", " + g.child(e, "this") + ")", true
	case "TimestampTrunc":
		if g.dialect == "hive" {
			unit := g.child(e, "unit")
			if unit != "" {
				unit = "'" + unit + "'"
			}
			return "TRUNC(" + g.child(e, "this") + ", " + unit + ")", true
		}
		// Spark2/Spark spell it DATE_TRUNC('<unit>', x).
		unit := g.child(e, "unit")
		if unit != "" {
			unit = "'" + unit + "'"
		}
		return "DATE_TRUNC(" + unit + ", " + g.child(e, "this") + ")", true
	case "IgnoreNulls":
		// The FIRST/LAST family takes the flag as a second TRUE argument
		// (`FIRST(x, TRUE)`); anything else keeps the base `x IGNORE NULLS`
		// (HiveGenerator.IGNORE_NULLS_FUNCS).
		inner, _ := e.Args["this"].(*Expression)
		if (g.dialect == "hive" || g.dialect == "spark2") && inner != nil {
			name := map[string]string{
				"First": "FIRST", "FirstValue": "FIRST_VALUE",
				"Last": "LAST", "LastValue": "LAST_VALUE",
			}[inner.Class]
			if name != "" {
				return name + "(" + g.child(inner, "this") + ", TRUE)", true
			}
		}
	case "TimeToStr":
		// Hive's DATE_FORMAT drops a TimeStrToTime wrapper on its argument.
		inner := g.child(e, "this")
		if t, _ := e.Args["this"].(*Expression); t != nil && t.Class == "TimeStrToTime" {
			inner = g.child(t, "this")
		}
		return "DATE_FORMAT(" + inner + ", " + g.child(e, "format") + ")", true
	case "ClusteredColumnConstraint", "NonClusteredColumnConstraint":
		return "(" + g.list(e) + ")", true
	case "JSONPathKey":
		// Hive writes a path's key as its bare NAME, however it is spelled
		// (HiveGenerator.json_path_key_only_name).
		return g.child(e, "this"), true
	case "Schema":
		// Hive's ADD COLUMNS (...): the Schema is the ALTER's action, not the
		// name it alters.
		if g.dialect == "hive" && e.Parent != nil && e.Parent.Class == "Alter" &&
			e.Parent.Args["this"] != e {
			return "COLUMNS " + g.spell(e), true
		}
	case "AlterColumn":
		if to := g.child(e, "rename_to"); to != "" {
			// Spark renders a rename-to-itself as a type restatement:
			// `ALTER COLUMN a TYPE <type>`.
			if dt := g.child(e, "dtype"); dt != "" && to == g.child(e, "this") &&
				(g.dialect == "spark2" || g.dialect == "spark") {
				return "ALTER COLUMN " + g.child(e, "this") + " TYPE " + dt, true
			}
			out := "CHANGE COLUMN " + g.child(e, "this") + " " + to
			if dt := g.child(e, "dtype"); dt != "" {
				out += " " + dt
			}
			if c, _ := e.Args["comment"].(*Expression); c != nil {
				out += " COMMENT " + g.node(c)
			}
			return out, true
		}
		// Spark's `ALTER COLUMN a TYPE <type>` restates the type only.
		if dt := g.child(e, "dtype"); dt != "" {
			return "ALTER COLUMN " + g.child(e, "this") + " TYPE " + dt, true
		}
	}
	return "", false
}

// hiveCreate writes a Hive CREATE TABLE whose PARTITIONED BY names columns
// WITH their types: those definitions belong to the schema, and the partition
// keeps the bare names (Hive's own partitionable-schema convention). The move
// is made on a COPY, so the caller's tree is untouched.
func (g *generator) hiveCreate(e *Expression) (string, bool) {
	schema, _ := e.Args["this"].(*Expression)
	props, _ := e.Args["properties"].(*Expression)
	if schema == nil || schema.Class != "Schema" || props == nil {
		return "", false
	}
	items, _ := props.Args["expressions"].([]*Expression)
	var moved []*Expression
	rewritten := make([]*Expression, 0, len(items))
	for _, p := range items {
		if p != nil && p.Class == "PartitionedByProperty" {
			if ps, _ := p.Args["this"].(*Expression); ps != nil && ps.Class == "Schema" {
				var names []*Expression
				for _, c := range g.expressionsOf(ps) {
					if c != nil && c.Class == "ColumnDef" {
						moved = append(moved, c)
						names = append(names, New("Identifier",
							Arg{"this", g.child(c, "this")}, Arg{"quoted", false}))
					} else {
						names = append(names, c)
					}
				}
				if len(moved) > 0 {
					p = New("PartitionedByProperty", Arg{"this", New("Schema", Arg{"expressions", names})})
				}
			}
		}
		rewritten = append(rewritten, p)
	}
	if len(moved) == 0 {
		return "", false
	}
	// Rebuild the copy with the moved definitions appended to the schema.
	copyCreate := e.shallowCopy()
	newSchema := schema.shallowCopy()
	newSchema.Set("expressions", append(append([]*Expression{}, g.expressionsOf(schema)...), moved...))
	copyCreate.Set("this", newSchema)
	newProps := props.shallowCopy()
	newProps.Set("expressions", rewritten)
	copyCreate.Set("properties", newProps)
	return g.spell(copyCreate), true
}

// hiveCall writes a call under a different name, passing the node's own
// arguments through in order.
func (g *generator) hiveCall(name string, e *Expression) string {
	var parts []string
	for _, key := range []string{"this", "expression", "quantile", "accuracy", "error_tolerance", "unit", "limit"} {
		if v := g.child(e, key); v != "" {
			parts = append(parts, v)
		}
	}
	if exprs, _ := e.Args["expressions"].([]*Expression); len(exprs) > 0 {
		parts = append(parts, g.list(e))
	}
	return name + "(" + strings.Join(parts, ", ") + ")"
}

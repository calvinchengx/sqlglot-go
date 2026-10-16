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
		out := "EXPLODE(" + g.list(e) + ")"
		if alias, _ := e.Args["alias"].(*Expression); alias != nil {
			out += " AS " + g.node(alias)
		}
		return out, true
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
		// The property's own value, which is a Schema carrying the partition
		// columns WITH their types (HiveGenerator.TRANSFORMS).
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
		if isHiveFamily(g.dialect) && e.Parent != nil && e.Parent.Class == "Alter" &&
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

// hiveCreate gives a CREATE's PARTITIONED BY the definitions of the columns
// it names. In Hive the property acts as an extension of the table's schema,
// so a property written as an array of NAMES takes the definitions the schema
// holds and those definitions come OUT of the schema
// (HiveGenerator.TRANSFORMS, transforms.move_schema_columns_to_partitioned_by).
//
// A property that already carries a Schema -- the dialect's own round trip --
// is left alone, and the move is made on a COPY so the caller's tree is
// untouched.
func (g *generator) hiveCreate(e *Expression) (string, bool) {
	// Spark 3 writes the DATASOURCE shape instead: the definitions go INTO
	// the table's schema and the property keeps their names
	// (Spark2Generator.TRANSFORMS,
	// transforms.move_partitioned_by_to_schema_columns).
	if g.dialect != "hive" {
		return g.sparkMoveDefinitionsToSchema(e)
	}
	schema, _ := e.Args["this"].(*Expression)
	props, _ := e.Args["properties"].(*Expression)
	kind, _ := e.Args["kind"].(string)
	if schema == nil || schema.Class != "Schema" || props == nil ||
		(kind != "TABLE" && kind != "VIEW") {
		return "", false
	}
	items, _ := props.Args["expressions"].([]*Expression)
	var prop *Expression
	for _, p := range items {
		if p != nil && p.Class == "PartitionedByProperty" {
			prop = p
			break
		}
	}
	if prop == nil {
		return "", false
	}
	named, _ := prop.Args["this"].(*Expression)
	if named == nil || named.Class == "Schema" {
		return "", false
	}
	columns := map[string]bool{}
	for _, c := range g.expressionsOf(named) {
		if c != nil {
			columns[strings.ToUpper(c.Name())] = true
		}
	}
	if len(columns) == 0 {
		return "", false
	}
	var kept, moved []*Expression
	for _, c := range g.expressionsOf(schema) {
		if c != nil && columns[strings.ToUpper(c.Name())] {
			moved = append(moved, c)
			continue
		}
		kept = append(kept, c)
	}
	if len(moved) == 0 {
		return "", false
	}
	copyCreate := e.shallowCopy()
	newSchema := schema.shallowCopy()
	newSchema.Set("expressions", kept)
	copyCreate.Set("this", newSchema)
	newProps := props.shallowCopy()
	rewritten := make([]*Expression, 0, len(items))
	for _, p := range items {
		if p == prop {
			rewritten = append(rewritten, New("PartitionedByProperty",
				Arg{"this", New("Schema", Arg{"expressions", moved})}))
			continue
		}
		rewritten = append(rewritten, p)
	}
	newProps.Set("expressions", rewritten)
	copyCreate.Set("properties", newProps)
	// Through the CREATE's OWN writer: `spell` skips the per-class writers,
	// and a CREATE has no template of its own, so it refused the statement
	// outright.
	return g.writeCreate(copyCreate), true
}

// sparkMoveDefinitionsToSchema gives a Spark CREATE the DATASOURCE shape: the
// partition columns' definitions move into the table's schema and
// PARTITIONED BY keeps their names, as a Tuple
// (transforms.move_partitioned_by_to_schema_columns).
func (g *generator) sparkMoveDefinitionsToSchema(e *Expression) (string, bool) {
	schema, _ := e.Args["this"].(*Expression)
	props, _ := e.Args["properties"].(*Expression)
	if schema == nil || schema.Class != "Schema" || props == nil {
		return "", false
	}
	items, _ := props.Args["expressions"].([]*Expression)
	var prop *Expression
	for _, p := range items {
		if p != nil && p.Class == "PartitionedByProperty" {
			prop = p
			break
		}
	}
	if prop == nil {
		return "", false
	}
	ps, _ := prop.Args["this"].(*Expression)
	if ps == nil || ps.Class != "Schema" {
		return "", false
	}
	defs := g.expressionsOf(ps)
	if len(defs) == 0 {
		return "", false
	}
	names := make([]*Expression, 0, len(defs))
	for _, d := range defs {
		// Every one must be a DEFINITION: a bare name is already the shape
		// this transform produces, and moving it again would lose nothing
		// and say nothing.
		if d == nil || d.Class != "ColumnDef" || d.Args["kind"] == nil {
			return "", false
		}
		names = append(names, New("Identifier",
			Arg{"this", g.child(d, "this")}, Arg{"quoted", false}))
	}
	copyCreate := e.shallowCopy()
	newSchema := schema.shallowCopy()
	newSchema.Set("expressions",
		append(append([]*Expression{}, g.expressionsOf(schema)...), defs...))
	copyCreate.Set("this", newSchema)
	newProps := props.shallowCopy()
	rewritten := make([]*Expression, 0, len(items))
	for _, p := range items {
		if p == prop {
			rewritten = append(rewritten, New("PartitionedByProperty",
				Arg{"this", New("Tuple", Arg{"expressions", names})}))
			continue
		}
		rewritten = append(rewritten, p)
	}
	newProps.Set("expressions", rewritten)
	copyCreate.Set("properties", newProps)
	return g.writeCreate(copyCreate), true
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

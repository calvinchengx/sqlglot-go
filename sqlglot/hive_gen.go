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
		return g.hiveCall(name, e), true
	}

	switch e.Class {
	case "TsOrDsAdd", "DateAdd":
		return "DATE_ADD(" + g.child(e, "this") + ", " + g.child(e, "expression") + ")", true
	case "DateSub":
		return "DATE_SUB(" + g.child(e, "this") + ", " + g.child(e, "expression") + ")", true
	case "DateDiff", "TsOrDsDiff":
		return "DATEDIFF(" + g.child(e, "this") + ", " + g.child(e, "expression") + ")", true
	case "Unnest":
		return "EXPLODE(" + g.list(e) + ")", true
	case "JSONExtract", "JSONExtractScalar":
		// Both are the same function, which names no scalar-ness.
		return "GET_JSON_OBJECT(" + g.child(e, "this") + ", " + g.child(e, "expression") + ")", true
	case "JSONFormat":
		return "TO_JSON(" + g.child(e, "this") + ")", true
	case "MonthsBetween":
		return "MONTHS_BETWEEN(" + g.child(e, "this") + ", " + g.child(e, "expression") + ")", true
	case "RegexpLike":
		return g.binary(e, "RLIKE"), true
	case "MD5Digest":
		return "UNHEX(MD5(" + g.child(e, "this") + "))", true
	case "PrimaryKeyColumnConstraint":
		return "PRIMARY KEY", true
	case "OnProperty", "NotForReplicationColumnConstraint":
		return "", true
	case "PartitionedByProperty":
		return "PARTITIONED BY " + g.child(e, "this"), true
	case "PartitionedByBucket":
		return "BUCKET(" + g.child(e, "expression") + ", " + g.child(e, "this") + ")", true
	case "PartitionByTruncate":
		return "TRUNCATE(" + g.child(e, "expression") + ", " + g.child(e, "this") + ")", true
	case "TimestampTrunc":
		unit := g.child(e, "unit")
		if unit != "" {
			unit = "'" + unit + "'"
		}
		return "TRUNC(" + g.child(e, "this") + ", " + unit + ")", true
	case "IgnoreNulls":
		// Hive writes the flag as a second TRUE argument: `FIRST(x, TRUE)`
		// (HiveGenerator.ignorenulls_sql).
		inner, _ := e.Args["this"].(*Expression)
		if inner != nil {
			name := map[string]string{
				"First": "FIRST", "FirstValue": "FIRST_VALUE",
				"Last": "LAST", "LastValue": "LAST_VALUE",
			}[inner.Class]
			if name == "" {
				name = strings.ToUpper(inner.Class)
			}
			return name + "(" + g.child(inner, "this") + ", TRUE)", true
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
		// Hive's ADD COLUMNS (...): the Schema is the ALTER's action.
		if g.dialect == "hive" && e.Parent != nil && e.Parent.Class == "Alter" {
			return "COLUMNS " + g.spell(e), true
		}
	case "AlterColumn":
		if to := g.child(e, "rename_to"); to != "" {
			out := "CHANGE COLUMN " + g.child(e, "this") + " " + to
			if dt := g.child(e, "dtype"); dt != "" {
				out += " " + dt
			}
			if c, _ := e.Args["comment"].(*Expression); c != nil {
				out += " COMMENT " + g.node(c)
			}
			return out, true
		}
	}
	return "", false
}

// hiveCall writes a call under a different name, passing the node's own
// arguments through in order.
func (g *generator) hiveCall(name string, e *Expression) string {
	var parts []string
	for _, key := range []string{"this", "expression", "quantile", "accuracy", "error_tolerance", "unit"} {
		if v := g.child(e, key); v != "" {
			parts = append(parts, v)
		}
	}
	if exprs, _ := e.Args["expressions"].([]*Expression); len(exprs) > 0 {
		parts = append(parts, g.list(e))
	}
	return name + "(" + strings.Join(parts, ", ") + ")"
}

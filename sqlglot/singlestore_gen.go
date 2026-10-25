package sqlglot

import (
	"fmt"
	"strings"
)

// singlestoreNode writes the classes SingleStore's generator handles its own
// way (SingleStoreGenerator.TRANSFORMS). The second return says whether the
// class was handled here, so the caller falls through to the ordinary writers
// and templates otherwise.
func (g *generator) singlestoreNode(e *Expression) (string, bool) {
	switch e.Class {
	case "Cast":
		// `x :> TYPE`, where every other dialect writes CAST.
		return g.child(e, "this") + " :> " + g.child(e, "to"), true
	case "TryCast":
		return g.child(e, "this") + " !:> " + g.child(e, "to"), true

	case "TsOrDsToDate":
		if f, _ := e.Args["format"].(*Expression); f != nil {
			return "TO_DATE(" + g.singlestoreFormattedArgs(e) + ")", true
		}
		return "DATE(" + g.child(e, "this") + ")", true
	case "StrToTime":
		return "TO_TIMESTAMP(" + g.singlestoreFormattedArgs(e) + ")", true
	case "ToChar":
		return "TO_CHAR(" + g.singlestoreFormattedArgs(e) + ")", true

	case "StrToDate":
		return "STR_TO_DATE(" + g.singlestoreFormattedArgs(e) + ")", true
	case "TimeToStr":
		return "DATE_FORMAT(" + g.singlestoreFormattedArgs(e) + ")", true
	case "UnixToTime", "UnixToStr":
		return "FROM_UNIXTIME(" + g.singlestoreFormattedArgs(e) + ")", true
	case "UnixToTimeStr":
		return "FROM_UNIXTIME(" + g.child(e, "this") + ") :> TEXT", true

	case "DateBin":
		parts := []string{g.child(e, "this"), g.child(e, "expression")}
		if origin := g.child(e, "origin"); origin != "" {
			parts = append(parts, origin)
		}
		return "TIME_BUCKET(" + strings.Join(parts, ", ") + ")", true

	case "JSONExtract", "JSONBExtract", "JSONExtractScalar", "JSONBExtractScalar":
		return g.singlestoreJSONExtract(e), true

	case "Collate":
		// `x :> LONGTEXT COLLATE 'c'`, and no second cast where the cast is
		// already there (SingleStoreGenerator.collate_sql).
		if cast := childOf(e, "this"); cast != nil && cast.Class == "Cast" {
			if to := childOf(cast, "to"); to != nil {
				if k, _ := to.Args["this"].(DataTypeKind); k == "LONGTEXT" {
					return g.node(cast) + " COLLATE " + g.child(e, "expression"), true
				}
			}
		}
		return g.child(e, "this") + " :> LONGTEXT COLLATE " + g.child(e, "expression"), true

	case "JSONExists":
		if from, _ := e.Args["from_dcolonqmark"].(bool); from {
			path, _ := e.Args["path"].(string)
			return g.child(e, "this") + "::?" + path, true
		}
		path, _ := e.Args["path"].(string)
		return "JSON_MATCH_ANY_EXISTS(" + g.child(e, "this") + ", '" + path + "')", true
	case "JSONBExists":
		path, _ := e.Args["path"].(string)
		return "BSON_MATCH_ANY_EXISTS(" + g.child(e, "this") + ", '" + path + "')", true

	case "JSONFormat":
		return "JSON_PRETTY(" + g.child(e, "this") + ")", true
	case "JSONArray":
		return "JSON_BUILD_ARRAY(" + g.list(e) + ")", true
	case "JSONObject":
		return "JSON_BUILD_OBJECT(" + g.list(e) + ")", true
	case "JSONArrayAgg":
		out := "JSON_AGG(" + g.child(e, "this")
		if order := g.child(e, "order"); order != "" {
			out += " " + order
		}
		return out + ")", true

	case "DayOfWeek":
		return "DAYOFWEEK(" + g.singlestoreDateInner(e) + ")", true
	case "Day", "DayOfMonth":
		return "DAY(" + g.singlestoreDateInner(e) + ")", true
	case "DayOfWeekIso":
		return "((DAYOFWEEK(" + g.child(e, "this") + ") % 7) + 1)", true
	case "Hll", "ApproxDistinct":
		parts := []string{g.child(e, "this")}
		if exprs := g.expressionsOf(e); len(exprs) > 0 {
			parts = append(parts, g.list(e))
		}
		return "APPROX_COUNT_DISTINCT(" + strings.Join(parts, ", ") + ")", true
	case "Variance":
		return "VAR_SAMP(" + g.child(e, "this") + ")", true
	case "VariancePop":
		return "VAR_POP(" + g.child(e, "this") + ")", true
	case "ApproxQuantile":
		parts := []string{g.child(e, "this")}
		if q := g.child(e, "quantile"); q != "" {
			parts = append(parts, q)
		}
		if et := g.child(e, "error_tolerance"); et != "" {
			parts = append(parts, et)
		}
		return "APPROX_PERCENTILE(" + strings.Join(parts, ", ") + ")", true
	case "Contains":
		return "INSTR(" + g.child(e, "this") + ", " + g.child(e, "expression") + ")", true

	case "RegexpExtractAll":
		parts := []string{g.child(e, "this"), g.child(e, "expression")}
		if p := g.child(e, "parameters"); p != "" {
			parts = append(parts, p)
		}
		return "REGEXP_MATCH(" + strings.Join(parts, ", ") + ")", true
	case "RegexpExtract":
		parts := []string{g.child(e, "this"), g.child(e, "expression")}
		for _, k := range []string{"position", "occurrence", "parameters"} {
			if v := g.child(e, k); v != "" {
				parts = append(parts, v)
			}
		}
		return "REGEXP_SUBSTR(" + strings.Join(parts, ", ") + ")", true

	case "Reduce":
		parts := []string{g.child(e, "initial"), g.child(e, "this"), g.child(e, "merge")}
		return "REDUCE(" + strings.Join(parts, ", ") + ")", true

	case "TruncateTable":
		var parts []string
		for _, t := range g.expressionsOf(e) {
			parts = append(parts, "TRUNCATE "+g.node(t))
		}
		return strings.Join(parts, "; "), true
	case "RenameColumn":
		return "CHANGE " + g.child(e, "this") + " " + g.child(e, "to"), true
	case "ComputedColumnConstraint":
		notNull := ""
		if nn, _ := e.Args["not_null"].(bool); nn {
			notNull = " NOT NULL"
		}
		typ := g.child(e, "data_type")
		if typ == "" {
			typ = "AUTO"
		}
		return "AS " + g.child(e, "this") + " PERSISTED " + typ + notNull, true
	case "DataType":
		if kind, _ := e.Args["this"].(DataTypeKind); string(kind) == "VECTOR" {
			return g.singlestoreVector(e), true
		} else if string(kind) == "ENUM" {
			return "ENUM(" + g.list(e) + ")", true
		}

	case "MatchAgainst":
		cols := g.expressionsOf(e)
		inner := ""
		if len(cols) == 1 && cols[0] != nil && cols[0].Class == "Table" {
			// SingleStore's MATCH names a TABLE rather than columns.
			inner = "TABLE " + g.node(cols[0])
		} else {
			inner = g.list(e)
		}
		out := "MATCH(" + inner + ") AGAINST(" + g.child(e, "this")
		if mod, _ := e.Args["modifier"].(string); mod != "" {
			out += " " + mod
		}
		return out + ")", true

	case "DateAdd", "DatetimeAdd":
		return "DATE_ADD(" + g.child(e, "this") + ", INTERVAL " + g.child(e, "expression") + " " + g.child(e, "unit") + ")", true
	case "DateSub", "DatetimeSub":
		return "DATE_SUB(" + g.child(e, "this") + ", INTERVAL " + g.child(e, "expression") + " " + g.child(e, "unit") + ")", true
	}
	return "", false
}

// singlestoreDateInner writes a date field's argument, dropping a
// TsOrDsToDate wrapper the way the reference's own DAYOFWEEK/DAY writers do --
// the wrapper is re-parseable but never written inside them.
func (g *generator) singlestoreDateInner(e *Expression) string {
	if t, _ := e.Args["this"].(*Expression); t != nil && t.Class == "TsOrDsToDate" {
		return g.child(t, "this")
	}
	return g.child(e, "this")
}

// expressionsOf returns a node's `expressions` list.
func (g *generator) expressionsOf(e *Expression) []*Expression {
	exprs, _ := e.Args["expressions"].([]*Expression)
	return exprs
}

// singlestoreVector writes `VECTOR(size, type)`, reversing the storage order
// and the short element-type alias (INT -> I32) -- SingleStoreGenerator
// .datatype_sql.
func (g *generator) singlestoreVector(e *Expression) string {
	exprs := g.expressionsOf(e)
	if len(exprs) == 1 {
		return "VECTOR(" + g.node(exprs[0]) + ")"
	}
	if len(exprs) != 2 {
		return g.spell(e)
	}
	name := g.node(exprs[0])
	for short, long := range g.tables.VectorTypeAliases {
		if long == name {
			name = short
			break
		}
	}
	return "VECTOR(" + g.node(exprs[1]) + ", " + name + ")"
}

// singlestoreFormattedArgs writes `this, '<format>'`, translating the stored
// format back through MySQL's INVERSE mapping -- the reference's
// MYSQL_INVERSE_TIME_MAPPING, which SingleStore's date functions name
// explicitly rather than using the dialect's own.
func (g *generator) singlestoreFormattedArgs(e *Expression) string {
	this := g.child(e, "this")
	f, _ := e.Args["format"].(*Expression)
	if f == nil {
		return this
	}
	mapping := g.tables.InverseTimeMapping
	if mt := parserTables["mysql"]; mt != nil {
		mapping = mt.InverseTimeMapping
	}
	if text, ok := f.Args["this"].(string); ok {
		f = New("Literal", Arg{"this", formatTime(text, mapping)}, Arg{"is_string", true})
	}
	return this + ", " + g.node(f)
}

// singlestoreJSONExtract writes SingleStore's JSON/BSON extraction functions,
// whose name says both the class and the value type, with the path SEGMENTS
// laid out as separate arguments (`JSON_EXTRACT_STRING(a, 'b', 2)`).
func (g *generator) singlestoreJSONExtract(e *Expression) string {
	name := map[string]string{
		"JSONExtract":        "JSON_EXTRACT_JSON",
		"JSONBExtract":       "BSON_EXTRACT_BSON",
		"JSONExtractScalar":  "JSON_EXTRACT_STRING",
		"JSONBExtractScalar": "BSON_EXTRACT_STRING",
	}[e.Class]
	if strings.Contains(e.Class, "Scalar") {
		if jt, _ := e.Args["json_type"].(string); jt != "" {
			prefix := "JSON_EXTRACT_"
			if strings.HasPrefix(e.Class, "JSONB") {
				prefix = "BSON_EXTRACT_"
			}
			name = prefix + jt
		}
	}
	parts := []string{g.child(e, "this")}
	parts = append(parts, g.singlestoreJSONPathArgs(e)...)
	return name + "(" + strings.Join(parts, ", ") + ")"
}

// singlestoreJSONPathArgs writes a JSONPath's segments as the separate string
// arguments SingleStore's extraction functions take -- a key becomes its name,
// a subscript its number.
func (g *generator) singlestoreJSONPathArgs(e *Expression) []string {
	path, _ := e.Args["expression"].(*Expression)
	if path == nil || path.Class != "JSONPath" {
		if path != nil {
			return []string{g.node(path)}
		}
		return nil
	}
	segs, _ := path.Args["expressions"].([]*Expression)
	var out []string
	for _, seg := range segs {
		if seg == nil {
			continue
		}
		switch seg.Class {
		case "JSONPathRoot":
			continue
		case "JSONPathKey":
			out = append(out, g.node(New("Literal", Arg{"this", seg.Name()}, Arg{"is_string", true})))
		case "JSONPathSubscript":
			out = append(out, "'"+fmt.Sprint(seg.Args["this"])+"'")
		default:
			out = append(out, g.node(seg))
		}
	}
	return out
}

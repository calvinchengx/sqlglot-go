package sqlglot

import "strings"

// ClickHouse's own type names, and the `Nullable(...)` wrapper around them
// (ClickHouseGenerator.datatype_sql, STRING_TYPE_MAPPING, NON_NULLABLE_TYPES).
var clickhouseStringTypes = map[DataTypeKind]bool{
	"BLOB": true, "CHAR": true, "LONGBLOB": true, "LONGTEXT": true,
	"MEDIUMBLOB": true, "MEDIUMTEXT": true, "TINYBLOB": true, "TINYTEXT": true,
	"TEXT": true, "VARBINARY": true, "VARCHAR": true,
}

// clickhouseNonNullableTypes are the composite kinds ClickHouse never wraps:
// `Nullable(Array(...))` is not a valid type there.
var clickhouseNonNullableTypes = map[DataTypeKind]bool{
	"ARRAY": true, "MAP": true, "STRUCT": true, "POINT": true, "RING": true,
	"LINESTRING": true, "MULTILINESTRING": true, "POLYGON": true, "MULTIPOLYGON": true,
}

// clickhouseNullable writes a type the way ClickHouse does.
//
// Every string type collapses to `String` -- the others are aliases, and any
// length is dropped -- and the type is then wrapped in `Nullable(...)` when
// the tree says so, or when it says nothing and the type is neither a Map's
// KEY nor one of the composites ClickHouse refuses to wrap. A type the parser
// read as plainly non-nullable (`nullable` false, ClickHouse's default) is
// written bare.
//
// The template the probe recorded for this class already carries the wrapper
// -- it was taken from a node whose nullability was UNSET, which is exactly
// when the reference wraps -- so it is taken back off first and put back only
// where the rule says it belongs.
func (g *generator) clickhouseNullable(e *Expression, body string) string {
	body = stripNullableWrapper(body)
	kind, _ := e.Args["this"].(DataTypeKind)
	if clickhouseStringTypes[kind] {
		body = "String"
	}
	switch e.Args["nullable"] {
	case true:
		return "Nullable(" + body + ")"
	case false:
		return body
	}
	// Unset. A Map's key is written unwrapped: ClickHouse requires a key it
	// can hash, and `Nullable` is not one.
	if parent := e.Parent; parent != nil && parent.Class == "DataType" {
		if pk, _ := parent.Args["this"].(DataTypeKind); pk == "MAP" {
			if items, _ := parent.Args["expressions"].([]*Expression); len(items) > 0 && items[0] == e {
				return body
			}
		}
	}
	if clickhouseNonNullableTypes[kind] {
		return body
	}
	return "Nullable(" + body + ")"
}

// stripNullableWrapper removes one `Nullable(...)` from around a type body.
func stripNullableWrapper(body string) string {
	const prefix = "Nullable("
	if strings.HasPrefix(body, prefix) && strings.HasSuffix(body, ")") {
		inner := body[len(prefix) : len(body)-1]
		// Only where the wrapper closes at the very END: `Nullable(a, b)` is
		// not a wrapper this can take off.
		if depth := strings.Count(inner, "(") - strings.Count(inner, ")"); depth == 0 {
			return inner
		}
	}
	return body
}

// clickhouseStripNullable removes the `Nullable(` a recorded ClickHouse type
// template leads with, and the wrapper's own closing paren where the template
// carries it. A probe renders the type with nullability UNSET, which is
// exactly when ClickHouse wraps, so the template comes back wrapped; a sized
// type's template holds only the OPENING, its close supplied by the size's
// own parentheses, so the two shapes are stripped differently:
//
//	Nullable(Enum)         -> Enum        (a balanced, fully-wrapped body)
//	Nullable(FixedString   -> FixedString (an opening only; the size closes it)
//
// clickhouseNullable decides afterwards whether to wrap it again.
func clickhouseStripNullable(s string) string {
	const prefix = "Nullable("
	if !strings.HasPrefix(s, prefix) {
		return s
	}
	inner := s[len(prefix):]
	// One unmatched close at the very end is the wrapper's own, carried by a
	// template that is otherwise balanced. An opening-only template has none.
	if strings.HasSuffix(inner, ")") &&
		strings.Count(inner, "(") == strings.Count(inner, ")")-1 {
		return inner[:len(inner)-1]
	}
	return inner
}

// replaceArrayJoinTables turns every Table under a ClickHouse ARRAY JOIN into
// the Column it names, recursing the way the reference finds them
// (ClickHouseParser._parse_join, Table.to_column). An aliased table becomes
// an Alias over the column; nothing else is touched.
func replaceArrayJoinTables(e *Expression) {
	for _, key := range append([]string(nil), e.Keys...) {
		switch v := e.Args[key].(type) {
		case *Expression:
			if v == nil {
				continue
			}
			if v.Class == "Table" {
				e.Set(key, arrayJoinTableToColumn(v))
				continue
			}
			replaceArrayJoinTables(v)
		case []*Expression:
			for i, c := range v {
				if c == nil {
					continue
				}
				if c.Class == "Table" {
					v[i] = arrayJoinTableToColumn(c)
					continue
				}
				replaceArrayJoinTables(c)
			}
			e.Set(key, v)
		}
	}
}

// arrayJoinTableToColumn is Table.to_column for an ARRAY JOIN: the reference's
// `alias_(col, alias.this)` keeps an alias the table carried.
func arrayJoinTableToColumn(t *Expression) *Expression {
	// A table whose LAST part is not a NAME -- `ARRAY JOIN splitByString(...)`
	// -- is that node itself rather than a Column over it, which is what the
	// reference's `Table.to_column` returns (Table.to_column).
	parts := tableParts(t)
	if len(parts) > 0 && parts[len(parts)-1].Class != "Identifier" {
		col := parts[len(parts)-1]
		if alias, _ := t.Args["alias"].(*Expression); alias != nil {
			name := alias
			if alias.Class == "TableAlias" {
				name = alias.This()
			}
			return New("Alias", Arg{"this", col}, Arg{"alias", name})
		}
		return col
	}
	col := tableToColumn(t)
	if col == nil {
		return t
	}
	if alias, _ := t.Args["alias"].(*Expression); alias != nil {
		// The reference unwraps the table's alias to the name it holds
		// (Table.to_column: `alias_(col, alias.this)`).
		name := alias
		if alias.Class == "TableAlias" {
			name = alias.This()
		}
		return New("Alias", Arg{"this", col}, Arg{"alias", name})
	}
	return col
}

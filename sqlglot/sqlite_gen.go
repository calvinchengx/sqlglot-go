package sqlglot

import "strings"

// sqliteNode writes the classes SQLite's generator handles its own way
// (SQLiteGenerator.TRANSFORMS). The second return says whether the class was
// handled here.
func (g *generator) sqliteNode(e *Expression) (string, bool) {
	switch e.Class {
	case "JSONExtract", "JSONExtractScalar":
		// Only the FUNCTION form writes JSON_EXTRACT; the `->`/`->>` form
		// keeps its arrow. The two are told apart by the flags the function
		// builder sets.
		_, hasSubtype := e.Args["json_subtype"]
		extra, _ := e.Args["expressions"].([]*Expression)
		if !hasSubtype && len(extra) == 0 {
			return "", false
		}
		parts := []string{g.child(e, "this")}
		if pathNode, _ := e.Args["expression"].(*Expression); pathNode != nil {
			if pathNode.Class == "JSONPath" {
				parts = append(parts, "'"+sqlitePathText(g.child(e, "expression"))+"'")
			} else {
				parts = append(parts, g.node(pathNode))
			}
		}
		for _, x := range extra {
			parts = append(parts, g.node(x))
		}
		return "JSON_EXTRACT(" + strings.Join(parts, ", ") + ")", true

	case "GroupConcat":
		// SQLite folds the separator into a DISTINCT argument, and an EMPTY
		// one there is dropped again on the way out.
		parts := []string{}
		inner, _ := e.Args["this"].(*Expression)
		if inner != nil && inner.Class == "Distinct" {
			innerCopy := inner.shallowCopy()
			items, _ := inner.Args["expressions"].([]*Expression)
			kept := make([]*Expression, 0, len(items))
			for _, it := range items {
				if it != nil && it.Class == "Literal" {
					if text, _ := it.Args["this"].(string); text == "" {
						continue
					}
				}
				kept = append(kept, it)
			}
			innerCopy.Set("expressions", kept)
			parts = append(parts, g.node(innerCopy))
		} else {
			parts = append(parts, g.child(e, "this"))
		}
		if sep := g.child(e, "separator"); sep != "" {
			parts = append(parts, sep)
		}
		return "GROUP_CONCAT(" + strings.Join(parts, ", ") + ")", true

	case "WindowSpec":
		// SQLite writes `RANGE CURRENT ROW` for that one frame.
		kind, _ := e.Args["kind"].(string)
		start, _ := e.Args["start"].(string)
		if strings.EqualFold(kind, "RANGE") && strings.EqualFold(start, "CURRENT ROW") {
			return "RANGE CURRENT ROW", true
		}

	case "Transaction":
		// SQLite names the lock mode between BEGIN and TRANSACTION.
		if this, _ := e.Args["this"].(string); this != "" {
			return "BEGIN " + this + " TRANSACTION", true
		}

	case "TableAlias":
		// SQLite has no table alias column list (SUPPORTS_TABLE_ALIAS_COLUMNS
		// off) -- but only on a TABLE, not on a CTE, which keeps its columns.
		if e.Parent != nil && e.Parent.Class != "With" && e.Parent.Class != "CTE" {
			if cols, _ := e.Args["columns"].([]*Expression); len(cols) > 0 {
				alias := e.shallowCopy()
				alias.Set("columns", nil)
				return g.node(alias), true
			}
		}

	case "Create":
		if out, ok := g.sqliteCreate(e); ok {
			return out, true
		}
	case "ComputedColumnConstraint":
		// SQLite writes the expression in parentheses, the storage word as
		// STORED, and any NOT NULL after it.
		expr := g.child(e, "this")
		if inner, _ := e.Args["this"].(*Expression); inner != nil && inner.Class != "Paren" {
			expr = "(" + expr + ")"
		}
		out := "AS " + expr
		if persisted, _ := e.Args["persisted"].(bool); persisted {
			out += " STORED"
		}
		if notNull, _ := e.Args["not_null"].(bool); notNull {
			out += " NOT NULL"
		}
		return out, true

	case "CurrentDate":
		return "CURRENT_DATE", true
	case "CurrentTime":
		return "CURRENT_TIME", true
	case "CurrentTimestamp":
		return "CURRENT_TIMESTAMP", true
	case "CurrentVersion":
		return "SQLITE_VERSION()", true
	}
	return "", false
}

// sqliteCreate moves a single-column PRIMARY KEY from the table level onto
// the column (SQLiteGenerator._transform_create), on a copy.
func (g *generator) sqliteCreate(e *Expression) (string, bool) {
	schema, _ := e.Args["this"].(*Expression)
	if schema == nil || schema.Class != "Schema" {
		return "", false
	}
	items, _ := schema.Args["expressions"].([]*Expression)
	defs := map[string]*Expression{}
	var pk *Expression
	for _, it := range items {
		if it == nil {
			continue
		}
		switch it.Class {
		case "ColumnDef":
			defs[it.Name()] = it
		case "PrimaryKey":
			pk = it
		}
	}
	if pk == nil {
		return "", false
	}
	pkItems, _ := pk.Args["expressions"].([]*Expression)
	if len(pkItems) != 1 {
		return "", false
	}
	col := defs[pkItems[0].Name()]
	if col == nil {
		return "", false
	}
	newCol := col.shallowCopy()
	cons, _ := col.Args["constraints"].([]*Expression)
	newCons := append(append([]*Expression{}, cons...),
		New("ColumnConstraint", Arg{"kind", New("PrimaryKeyColumnConstraint")}))
	newCol.Set("constraints", newCons)
	var newItems []*Expression
	for _, it := range items {
		if it == pk {
			continue
		}
		if it == col {
			newItems = append(newItems, newCol)
			continue
		}
		newItems = append(newItems, it)
	}
	copyCreate := e.shallowCopy()
	newSchema := schema.shallowCopy()
	newSchema.Set("expressions", newItems)
	copyCreate.Set("this", newSchema)
	return g.spell(copyCreate), true
}

// sqlitePathText recovers the path string a JSONPath was parsed from.
func sqlitePathText(path string) string {
	if strings.HasPrefix(path, "'") && strings.HasSuffix(path, "'") {
		return path[1 : len(path)-1]
	}
	return path
}

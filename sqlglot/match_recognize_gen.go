package sqlglot

import "strings"

func (g *generator) writeMatchRecognize(e *Expression) string {
	var parts []string
	if cols, _ := e.Args["partition_by"].([]*Expression); len(cols) > 0 {
		names := make([]string, len(cols))
		for i, col := range cols {
			names[i] = g.node(col)
		}
		parts = append(parts, "PARTITION BY "+strings.Join(names, ", "))
	}
	if order := g.child(e, "order"); order != "" {
		parts = append(parts, order)
	}
	if measures, _ := e.Args["measures"].([]*Expression); len(measures) > 0 {
		items := make([]string, len(measures))
		for i, measure := range measures {
			items[i] = g.node(measure)
		}
		parts = append(parts, "MEASURES "+strings.Join(items, ", "))
	}
	if rows := g.child(e, "rows"); rows != "" {
		parts = append(parts, rows)
	}
	if after := g.child(e, "after"); after != "" {
		parts = append(parts, after)
	}
	if pattern := g.child(e, "pattern"); pattern != "" {
		parts = append(parts, "PATTERN ("+pattern+")")
	}
	if defs, _ := e.Args["define"].([]*Expression); len(defs) > 0 {
		items := make([]string, len(defs))
		for i, def := range defs {
			// DEFINE names the variable first. The ordinary alias writer puts the expression first.
			items[i] = g.child(def, "alias") + " AS " + g.child(def, "this")
		}
		parts = append(parts, "DEFINE "+strings.Join(items, ", "))
	}
	out := "MATCH_RECOGNIZE (" + strings.Join(parts, " ") + ")"
	if alias := g.child(e, "alias"); alias != "" {
		out += " " + alias
	}
	return out
}

// writeFirstOrLast keeps FIRST and LAST inside MATCH_RECOGNIZE. Presto and
// Trino otherwise spell both ARBITRARY, which is a different function.
func (g *generator) writeFirstOrLast(e *Expression) string {
	if (g.dialect == "presto" || g.dialect == "trino") && hasAncestor(e, "MatchRecognize") {
		args := g.child(e, "this")
		if extra := g.child(e, "expression"); extra != "" {
			args += ", " + extra
		}
		return strings.ToUpper(e.Class) + "(" + args + ")"
	}
	return g.spell(e)
}

func hasAncestor(e *Expression, class string) bool {
	for parent := e.Parent; parent != nil; parent = parent.Parent {
		if parent.Class == class {
			return true
		}
	}
	return false
}

func (g *generator) writeMatchRecognizeMeasure(e *Expression) string {
	this := g.child(e, "this")
	if frame, ok := e.Args["window_frame"].(string); ok && frame != "" {
		return frame + " " + this
	}
	return this
}

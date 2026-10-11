package sqlglot

import "strings"

// writePartitionedByProperty writes a table's PARTITION BY property.
//
// Doris wraps the column list in ONE pair of parentheses and writes the
// columns FLAT (partitionedbyproperty_sql in the reference). The generic
// template -- `PARTITION BY {this}` -- fills in the Schema's own rendering,
// which already carries a pair of parentheses, so it comes out doubled:
// `PARTITION BY ((c1, c2))`. Doris therefore writes the property itself and
// every other dialect keeps the template.
func (g *generator) writePartitionedByProperty(e *Expression) string {
	if g.dialect == "doris" {
		this, _ := e.Args["this"].(*Expression)
		if this != nil && this.Class == "Schema" {
			items, _ := this.Args["expressions"].([]*Expression)
			parts := make([]string, 0, len(items))
			for _, item := range items {
				parts = append(parts, g.node(item))
			}
			return "PARTITION BY (" + strings.Join(parts, ", ") + ")"
		}
		return "PARTITION BY (" + g.node(this) + ")"
	}
	return g.spell(e)
}

// writePartitionByRangePropertyDynamic writes Doris's dynamic range partition:
// `FROM ('start') TO ('end') INTERVAL n unit`.
func (g *generator) writePartitionByRangePropertyDynamic(e *Expression) string {
	start := g.child(e, "start")
	end := g.child(e, "end")
	out := "FROM (" + start + ") TO (" + end + ")"
	if every, _ := e.Args["every"].(*Expression); every != nil {
		out += " INTERVAL " + g.child(every, "this") + " " + g.child(every, "unit")
	}
	return out
}

// writeUniqueKeyProperty writes a composite key. Doris writes a bare KEY
// inside a MATERIALIZED VIEW -- the MV's own key, not a uniqueness constraint
// (DorisGenerator.uniquekeyproperty_sql); every other dialect keeps the
// template's `UNIQUE KEY (...)`.
func (g *generator) writeUniqueKeyProperty(e *Expression) string {
	if g.dialect == "doris" && dorisInsideMaterializedView(e) {
		return "KEY (" + g.list(e) + ")"
	}
	return g.spell(e)
}

func dorisInsideMaterializedView(e *Expression) bool {
	for p := e.Parent; p != nil; p = p.Parent {
		if p.Class != "Create" {
			continue
		}
		props, _ := p.Args["properties"].(*Expression)
		if props == nil {
			return false
		}
		items, _ := props.Args["expressions"].([]*Expression)
		for _, item := range items {
			if item != nil && item.Class == "MaterializedProperty" {
				return true
			}
		}
		return false
	}
	return false
}

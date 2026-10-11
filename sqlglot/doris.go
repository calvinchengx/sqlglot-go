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

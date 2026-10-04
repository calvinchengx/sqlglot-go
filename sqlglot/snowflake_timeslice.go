package sqlglot

import "strings"

// Snowflake writes TIME_SLICE with the unit as a string, whatever form
// the unit was read in. A missing unit is the day.
func init() {
	generators["TimeSlice"] = (*generator).writeSnowflakeTimeSlice
}

func (g *generator) writeSnowflakeTimeSlice(e *Expression) string {
	if g.dialect != "snowflake" {
		return g.spell(e)
	}
	parts := make([]string, 0, 4)
	if this := g.child(e, "this"); this != "" {
		parts = append(parts, this)
	}
	if expression := g.child(e, "expression"); expression != "" {
		parts = append(parts, expression)
	}
	if unit := snowflakeSliceUnit(g, e); unit != "" {
		parts = append(parts, unit)
	}
	if kind := g.child(e, "kind"); kind != "" {
		parts = append(parts, kind)
	}
	return "TIME_SLICE(" + strings.Join(parts, ", ") + ")"
}

func snowflakeSliceUnit(g *generator, e *Expression) string {
	unit, _ := e.Args["unit"].(*Expression)
	if unit == nil {
		return "'DAY'"
	}
	if unit.Class == "Var" || unit.Class == "Literal" {
		name, _ := unit.Args["this"].(string)
		return "'" + strings.ReplaceAll(name, "'", "''") + "'"
	}
	return g.child(e, "unit")
}

package sqlglot

// Snowflake writes an StPoint as ST_MAKEPOINT. The node stays an StPoint.
// Other dialects keep the neutral spelling.
func init() {
	generators["StPoint"] = writeSnowflakeStMakePoint
}

func writeSnowflakeStMakePoint(g *generator, e *Expression) string {
	if g.dialect != "snowflake" {
		return g.spell(e)
	}
	spec, ok := g.functionSpelling(e)
	if !ok {
		return g.fail("StPoint")
	}
	spec.Name = "ST_MAKEPOINT"
	return g.namedFunction(e, spec)
}

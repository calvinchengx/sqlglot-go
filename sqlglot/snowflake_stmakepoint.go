package sqlglot

// Snowflake writes an StPoint as ST_MAKEPOINT. The node stays an StPoint.
func init() {
	generators["StPoint"] = writeSnowflakeStMakePoint
}

func writeSnowflakeStMakePoint(g *generator, e *Expression) string {
	spec, ok := g.functionSpelling(e)
	if !ok {
		return g.fail("StPoint")
	}
	spec.Name = "ST_MAKEPOINT"
	return g.namedFunction(e, spec)
}

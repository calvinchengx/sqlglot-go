package sqlglot

// The reference's `function_fallback_sql` writes the name a call was WRITTEN
// with, and BigQuery, ClickHouse and Drill keep every one of them
// (Dialect.ORIGINAL_NAME_META_KEY). The port records the name on the node;
// these are the classes whose writer is that fallback rather than a method of
// its own.
func init() {
	for _, dialect := range []string{"bigquery", "clickhouse", "drill"} {
		tables := parserTables[dialect]
		if tables == nil {
			continue
		}
		if tables.ConsultsOriginalName == nil {
			tables.ConsultsOriginalName = map[string]struct{}{}
		}
		for _, class := range []string{"Substring", "Struct"} {
			tables.ConsultsOriginalName[class] = struct{}{}
		}
	}
}

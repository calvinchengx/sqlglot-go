package sqlglot

import "maps"

// Snowflake's sample deviation is STDDEV. STDDEV_SAMP is the same node,
// and it is written STDDEV.
func init() {
	aliasSnowflakeSampleDeviation(parserTables["snowflake"], "STDDEV_SAMP")
}

func aliasSnowflakeSampleDeviation(tables *ParserTables, name string) {
	if tables == nil || name == "" {
		return
	}
	functions := maps.Clone(tables.Functions)
	sample := functions["STDDEV"]
	if sample.Class != "Stddev" {
		return
	}
	functions[name] = sample
	tables.Functions = functions
}

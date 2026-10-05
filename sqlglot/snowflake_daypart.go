package sqlglot

import "maps"

// Snowflake writes these date parts as one word. The node keeps the
// underscored class.
func init() {
	spellSnowflakeDayPart(parserTables["snowflake"])
}

func spellSnowflakeDayPart(tables *ParserTables) {
	if tables == nil {
		return
	}
	owned := maps.Clone(tables.FunctionSQL)
	owned["DayOfMonth"] = snowflakeDayPart(owned["DayOfMonth"], "DAYOFMONTH")
	owned["DayOfWeek"] = snowflakeDayPart(owned["DayOfWeek"], "DAYOFWEEK")
	owned["DayOfWeekIso"] = snowflakeDayPart(owned["DayOfWeekIso"], "DAYOFWEEKISO")
	owned["DayOfYear"] = snowflakeDayPart(owned["DayOfYear"], "DAYOFYEAR")
	owned["YearOfWeek"] = snowflakeDayPart(owned["YearOfWeek"], "YEAROFWEEK")
	owned["YearOfWeekIso"] = snowflakeDayPart(owned["YearOfWeekIso"], "YEAROFWEEKISO")
	tables.FunctionSQL = owned
}

func snowflakeDayPart(prior []FuncSQL, name string) []FuncSQL {
	if len(prior) != 2 {
		return prior
	}
	first := prior[0]
	second := prior[1]
	first.Name = name
	second.Name = name
	return []FuncSQL{first, second}
}

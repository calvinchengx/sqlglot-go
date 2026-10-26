package sqlglot

import "strings"

// makeIntervalUnits maps the name a MAKE_INTERVAL argument is written with to
// the unit it fills (PostgresParser.MAKE_INTERVAL_KWARGS). A positional
// argument keeps the slot it was written in.
var makeIntervalUnits = map[string]string{
	"years": "year", "months": "month", "weeks": "week", "days": "day",
	"hours": "hour", "mins": "minute", "secs": "second",
}

// makeIntervalNamed builds PostgreSQL's MAKE_INTERVAL: an argument written
// `days => 3` fills `day`, and one written bare fills the next slot of the
// node's own order.
func makeIntervalNamed(args []*Expression) *Expression {
	node := New("MakeInterval")
	slots := []string{"year", "month", "week", "day", "hour", "minute", "second"}
	for i, arg := range args {
		if i >= len(slots) {
			break
		}
		key := slots[i]
		if arg != nil && arg.Class == "Kwarg" {
			// The name a Kwarg was written with is its own `this`, which is a
			// Var: the node's own Name() is empty.
			if name := childOf(arg, "this"); name != nil {
				if unit, ok := makeIntervalUnits[strings.ToLower(name.Name())]; ok {
					key = unit
				}
			}
		}
		node.Set(key, arg)
	}
	return node
}

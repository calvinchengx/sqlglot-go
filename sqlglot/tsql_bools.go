package sqlglot

// ensureBoolsTSQL wraps a bare value used as a CONDITION in an explicit
// comparison with zero. T-SQL has no boolean type, so `IIF(a, 1, 2)` and
// `WHERE 1` are written back `IIF(a <> 0, 1, 2)` and `WHERE 1 <> 0`
// (TSQLGenerator.ENSURE_BOOLS -> transforms.ensure_bools).
//
// The positions are the reference's own: both sides of a connector, what a
// NOT negates, an If's condition (unless it is a CASE's own operand), and a
// WHERE or HAVING.
func ensureBoolsTSQL(e *Expression) *Expression {
	if e == nil {
		return e
	}
	e.Walk(func(node *Expression) bool {
		switch node.Class {
		case "And", "Or":
			ensureBoolAt(node, "this")
			ensureBoolAt(node, "expression")
		case "Not":
			ensureBoolAt(node, "this")
		case "If":
			// `CASE x WHEN 1 THEN ...` holds a VALUE, not a condition, and
			// the reference leaves it alone.
			if parent := node.Parent; parent != nil && parent.Class == "Case" &&
				childOf(parent, "this") != nil {
				break
			}
			ensureBoolAt(node, "this")
		case "Where", "Having":
			ensureBoolAt(node, "this")
		}
		return true
	})
	return e
}

// ensureBoolAt replaces a numeric literal or an untyped column in one
// position with `value <> 0`.
func ensureBoolAt(parent *Expression, key string) {
	value, _ := parent.Args[key].(*Expression)
	if value == nil || value.Class == "SubqueryPredicate" {
		return
	}
	numeric := value.Class == "Literal" && value.Args["is_string"] != true
	untypedColumn := value.Class == "Column" && value.Type == nil
	if !numeric && !untypedColumn {
		return
	}
	parent.Set(key, New("NEQ", Arg{"this", value}, Arg{"expression", numberLit(0, true)}))
}

package sqlglot

import "testing"

// Redshift writes UNPIVOT in front of a SUPER value. The relation is a pivot,
// and a parse or generate error fails the row.
func TestRedshiftSuperUnpivot(t *testing.T) {
	sql := "SELECT attr AS attr, JSON_TYPEOF(val) AS value_type FROM customer_orders_lineitem AS c, UNPIVOT c.c_orders[0] WHERE c_custkey = 9451"
	tree, err := ParseOne(sql, "redshift")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	joins, _ := tree.Args["joins"].([]*Expression)
	if len(joins) != 1 || joins[0].Class != "Join" {
		t.Fatalf("from-list join = %#v", joins)
	}
	pivot, _ := joins[0].Args["this"].(*Expression)
	unpivot, _ := pivot.Args["unpivot"].(bool)
	if pivot == nil || pivot.Class != "Pivot" || !unpivot || pivot.Args["this"].(*Expression).Class != "Bracket" {
		t.Fatalf("UNPIVOT was not a pivot over a subscript: %#v", pivot)
	}
	got, err := Generate(tree, "redshift")
	if err != nil || got != sql {
		t.Fatalf("Generate = %q, %v", got, err)
	}
}

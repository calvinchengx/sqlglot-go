package sqlglot

import "testing"

// Redshift names one element of a SUPER array with AT. The table becomes a
// column, and a parse or generate error fails the row.
func TestRedshiftAtIndex(t *testing.T) {
	sql := "SELECT c_name, orders.o_orderkey AS orderkey, index AS orderkey_index FROM customer_orders_lineitem AS c, c.c_orders AS orders AT index ORDER BY orderkey_index"
	tree, err := ParseOne(sql, "redshift")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	joins, _ := tree.Args["joins"].([]*Expression)
	at, _ := joins[0].Args["this"].(*Expression)
	if len(joins) != 1 || at == nil || at.Class != "AtIndex" {
		t.Fatalf("array element was not an AtIndex: %#v", joins)
	}
	alias, _ := at.Args["this"].(*Expression)
	if alias == nil || alias.Class != "Alias" || alias.Args["this"].(*Expression).Class != "Column" {
		t.Fatalf("AtIndex this = %#v", alias)
	}
	got, err := Generate(tree, "redshift")
	if err != nil || got != sql {
		t.Fatalf("Generate = %q, %v", got, err)
	}

	unpivot := "SELECT attr AS attr, JSON_TYPEOF(val) AS value_type FROM customer_orders_lineitem AS c, UNPIVOT c.c_orders AS val AT attr WHERE c_custkey = 9451"
	tree, err = ParseOne(unpivot, "redshift")
	if err != nil {
		t.Fatalf("ParseOne(UNPIVOT): %v", err)
	}
	if got, err = Generate(tree, "redshift"); err != nil || got != unpivot {
		t.Fatalf("Generate(UNPIVOT) = %q, %v", got, err)
	}
}

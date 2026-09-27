package sqlglot

import "testing"

// A Redshift GRANT may name the columns a privilege covers. Those columns
// belong to the privilege, and a parse or generate error fails the row.
func TestRedshiftColumnGrant(t *testing.T) {
	sql := "GRANT SELECT(cust_name, cust_phone), UPDATE(cust_contact_preference) ON cust_profile TO GROUP sales_group"
	tree, err := ParseOne(sql, "redshift")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	privs, _ := tree.Args["privileges"].([]*Expression)
	cols, _ := privs[0].Args["expressions"].([]*Expression)
	if tree.Class != "Grant" || len(privs) != 2 || len(cols) != 2 || cols[0].Class != "Column" {
		t.Fatalf("column grant was not a Grant of column privileges: %s", tree.Class)
	}
	if mark, ok := cols[0].Args["join_mark"].(bool); !ok || mark {
		t.Fatalf("redshift column join mark = %#v", cols[0].Args["join_mark"])
	}
	got, err := Generate(tree, "redshift")
	if err != nil || got != sql {
		t.Fatalf("Generate = %q, %v", got, err)
	}

	all := "GRANT ALL(cust_name, cust_phone, cust_contact_preference) ON cust_profile TO GROUP sales_admin"
	tree, err = ParseOne(all, "redshift")
	if err != nil {
		t.Fatalf("ParseOne(ALL): %v", err)
	}
	if got, err = Generate(tree, "redshift"); err != nil || got != all {
		t.Fatalf("Generate(ALL) = %q, %v", got, err)
	}
}

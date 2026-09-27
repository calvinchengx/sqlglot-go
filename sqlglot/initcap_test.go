package sqlglot

import "testing"

// Presto has no INITCAP. The reference stores the call with the default
// delimiter class and writes it back as a REGEXP_REPLACE.
func TestPrestoInitcap(t *testing.T) {
	sql := "INITCAP(col)"
	tree, err := ParseOne(sql, "presto")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	col, _ := tree.Args["this"].(*Expression)
	delim, _ := tree.Args["expression"].(*Expression)
	text, _ := delim.Args["this"].(string)
	if tree.Class != "Initcap" || col.Class != "Column" || delim.Class != "Literal" || text != initcapDefaultDelimiters {
		t.Fatalf("INITCAP = %s", tree.Class)
	}
	got, err := Generate(tree, "presto")
	want := "REGEXP_REPLACE(col, '(\\w)(\\w*)', x -> UPPER(x[1]) || LOWER(x[2]))"
	if err != nil || got != want {
		t.Fatalf("Generate = %q, %v", got, err)
	}
}

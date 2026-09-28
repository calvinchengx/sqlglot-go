package sqlglot

import "testing"

func TestPrestoJSONPathUnion(t *testing.T) {
	cases := []string{
		"SELECT JSON_EXTRACT(x, '$[1,0]')",
		"SELECT JSON_EXTRACT_SCALAR(x, '$[1,0]')",
		`SELECT JSON_EXTRACT(x, '$["a",""]')`,
	}
	for _, sql := range cases {
		tree, err := ParseOne(sql, "presto")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", sql, err)
		}
		got, gerr := Generate(tree, "presto")
		if gerr != nil || got != sql {
			t.Fatalf("Generate(%q) = %q, %v", sql, got, gerr)
		}
	}
}

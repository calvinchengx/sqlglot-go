package sqlglot

import "testing"

// Presto writes a JSON_OBJECT pair as KEY … VALUE. The tree is the same pair
// a colon would build, and it is written back with the colon.
func TestPrestoJSONObjectKeyValue(t *testing.T) {
	sql := "SELECT JSON_OBJECT(KEY 'key1' VALUE 1, KEY 'key2' VALUE TRUE)"
	tree, err := ParseOne(sql, "presto")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	call, _ := tree.Args["expressions"].([]*Expression)
	pairs, _ := call[0].Args["expressions"].([]*Expression)
	key, _ := pairs[0].Args["this"].(*Expression)
	if call[0].Class != "JSONObject" || len(pairs) != 2 || pairs[0].Class != "JSONKeyValue" || key.Class != "Literal" {
		t.Fatalf("JSON_OBJECT pairs = %s", call[0].Class)
	}
	got, err := Generate(tree, "presto")
	if err != nil || got != "SELECT JSON_OBJECT('key1': 1, 'key2': TRUE)" {
		t.Fatalf("Generate = %q, %v", got, err)
	}
}

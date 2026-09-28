package sqlglot

import "testing"

// RisingWave's CREATE SINK puts the query first and then says where the rows
// go: a WITH list, a format, and one encode for the payload and one for the key.
func TestRisingWaveCreateSink(t *testing.T) {
	sql := "CREATE SINK my_sink AS SELECT * FROM A WITH (connector='kafka', topic='my_topic') FORMAT PLAIN ENCODE PROTOBUF (A=1, B=2) KEY ENCODE PROTOBUF (A=3, B=4)"
	tree, err := ParseOne(sql, "risingwave")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	kind, _ := tree.Args["kind"].(string)
	props, _ := tree.Args["properties"].(*Expression)
	items, _ := props.Args["expressions"].([]*Expression)
	if tree.Class != "Create" || kind != "SINK" || len(items) != 5 {
		t.Fatalf("create %s kind %s properties %d", tree.Class, kind, len(items))
	}
	if items[2].Class != "FileFormatProperty" || items[3].Class != "EncodeProperty" || items[4].Class != "EncodeProperty" {
		t.Fatalf("tail %s %s %s", items[2].Class, items[3].Class, items[4].Class)
	}
	key, _ := items[4].Args["key"].(bool)
	if !key {
		t.Fatal("key encode has no key flag")
	}
	got, err := Generate(tree, "risingwave")
	if err != nil || got != sql {
		t.Fatalf("Generate = %q, %v", got, err)
	}
}

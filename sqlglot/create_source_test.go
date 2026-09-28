package sqlglot

import "testing"

func TestRisingWaveCreateSource(t *testing.T) {
	sql := "CREATE SOURCE from_kafka (*, gen_i32_field INT AS int32_field + 2, gen_i64_field INT AS int64_field + 2, WATERMARK FOR time_col AS time_col - INTERVAL '5 SECOND') INCLUDE header foo VARCHAR AS myheader INCLUDE key AS mykey WITH (connector='kafka', topic='my_topic') FORMAT PLAIN ENCODE PROTOBUF (A=1, B=2) KEY ENCODE PROTOBUF (A=3, B=4)"
	tree, err := ParseOne(sql, "risingwave")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	schema, _ := tree.Args["this"].(*Expression)
	cols, _ := schema.Args["expressions"].([]*Expression)
	kind, _ := tree.Args["kind"].(string)
	if tree.Class != "Create" || kind != "SOURCE" || schema.Class != "Schema" || len(cols) != 4 || cols[0].Class != "Star" || cols[3].Class != "WatermarkColumnConstraint" {
		t.Fatalf("source schema %s %s %d", tree.Class, schema.Class, len(cols))
	}
	got, err := Generate(tree, "risingwave")
	if err != nil || got != sql {
		t.Fatalf("Generate = %q, %v", got, err)
	}
}

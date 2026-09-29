package sqlglot

import "testing"

func TestDatabricksApproxQuantileFollowsItsValue(t *testing.T) {
	tree, err := ParseOne("PERCENTILE_APPROX(3, 0.2)", "databricks")
	if err != nil {
		t.Fatal(err)
	}
	got, gerr := Generate(Annotate(tree, "databricks"), "databricks")
	if gerr != nil || got != "INT" {
		t.Fatal(got, gerr)
	}
	tree, err = ParseOne("APPROX_PERCENTILE(3.1, array(0.2, 0.3))", "databricks")
	if err != nil {
		t.Fatal(err)
	}
	got, gerr = Generate(Annotate(tree, "databricks"), "databricks")
	if gerr != nil || got != "ARRAY<DOUBLE>" {
		t.Fatal(got, gerr)
	}
	tree, err = ParseOne("PERCENTILE_APPROX(x, array(0.2))", "databricks")
	if err != nil {
		t.Fatal(err)
	}
	got, gerr = Generate(Annotate(tree, "databricks"), "databricks")
	if gerr != nil || got != "UNKNOWN" {
		t.Fatal(got, gerr)
	}
	bare := New("ApproxQuantile", Arg{"this", New("Gap")})
	got, gerr = Generate(Annotate(bare, "databricks"), "databricks")
	if gerr != nil || got != "UNKNOWN" {
		t.Fatal(got, gerr)
	}
}

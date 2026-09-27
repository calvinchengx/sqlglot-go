package sqlglot

import "testing"

// Dremio DATETYPE of three integers folds to a date literal. Any other
// argument is concatenated and cast to DATE. A parse or generate error fails
// the row. A call that is not three arguments stays refused.
func TestDateType(t *testing.T) {
	cases := [][2]string{
		{"DATETYPE(2024, 2, 2)", "DATE('2024-02-02')"},
		{"DATETYPE(x, y, z)", "CAST(CONCAT(x, '-', y, '-', z) AS DATE)"},
		{"DATETYPE(2024, y, 2)", "CAST(CONCAT(2024, '-', y, '-', 2) AS DATE)"},
	}
	written := 0
	for _, c := range cases {
		tree, err := ParseOne(c[0], "dremio")
		if err != nil {
			t.Errorf("ParseOne(%q): %v", c[0], err)
			continue
		}
		got, err := Generate(tree, "dremio")
		if err != nil {
			t.Errorf("Generate(%q): %v", c[0], err)
			continue
		}
		written++
		if got != c[1] {
			t.Errorf("%s\n  want %s\n  got  %s", c[0], c[1], got)
		}
	}
	if written != len(cases) {
		t.Errorf("wrote %d statements, want %d", written, len(cases))
	}
	if _, err := ParseOne("DATETYPE(x, y)", "dremio"); err == nil {
		t.Error("DATETYPE with two arguments parsed")
	}
}

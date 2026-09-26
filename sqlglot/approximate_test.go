package sqlglot

import "testing"

// Redshift writes APPROXIMATE in front of COUNT(DISTINCT) and
// PERCENTILE_DISC(...) WITHIN GROUP. A word that is not one of those is a
// column named APPROXIMATE. A parse or generate error fails the row.
func TestApproximate(t *testing.T) {
	cases := [][3]string{
		{"redshift", "SELECT APPROXIMATE COUNT(DISTINCT y)",
			"SELECT APPROXIMATE COUNT(DISTINCT y)"},
		{"redshift", "SELECT APPROXIMATE AS y",
			"SELECT APPROXIMATE AS y"},
		{"redshift", "SELECT APPROXIMATE PERCENTILE_DISC(0.5) WITHIN GROUP (ORDER BY totalprice)",
			"SELECT APPROXIMATE PERCENTILE_DISC(0.5) WITHIN GROUP (ORDER BY totalprice)"},
		{"redshift", "SELECT t.APPROXIMATE",
			"SELECT t.APPROXIMATE"},
	}
	written := 0
	for _, c := range cases {
		tree, err := ParseOne(c[1], c[0])
		if err != nil {
			t.Errorf("[%s] ParseOne(%q): %v", c[0], c[1], err)
			continue
		}
		got, err := Generate(tree, c[0])
		if err != nil {
			t.Errorf("[%s] Generate(%q): %v", c[0], c[1], err)
			continue
		}
		written++
		if got != c[2] {
			t.Errorf("[%s] %s\n  want %s\n  got  %s", c[0], c[1], c[2], got)
		}
	}
	if written != len(cases) {
		t.Errorf("wrote %d statements, want %d", written, len(cases))
	}

	for _, sql := range []string{
		"SELECT APPROXIMATE COUNT(y)",
		"SELECT APPROXIMATE COUNT(DISTINCT a, b)",
		"SELECT APPROXIMATE PERCENTILE_DISC(0.5)",
		"SELECT APPROXIMATE PERCENTILE_DISC(0.5) WITHIN GROUP (ORDER BY a, b)",
		"SELECT APPROXIMATE PERCENTILE_DISC(0.5) WITHIN GROUP (ORDER BY totalprice DESC)",
	} {
		if _, err := ParseOne(sql, "redshift"); err == nil {
			t.Errorf("ParseOne(%q) succeeded; that form is not one approximate aggregate", sql)
		}
	}
}

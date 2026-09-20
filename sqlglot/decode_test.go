package sqlglot

import "testing"

// DECODE is a value match at three arguments or more and a character-set
// decode below that; the port reads both the way the reference does, and
// writes back the ones its dialect has a spelling for.
func TestDecode(t *testing.T) {
	for _, c := range [][2]string{
		{"SELECT DECODE(a, 1, 'one')", "SELECT DECODE(a, 1, 'one')"},
		{"SELECT DECODE(a, NULL, 'n', 'd')", "SELECT DECODE(a, NULL, 'n', 'd')"},
		{"SELECT DECODE(x, a, b, c, d)", "SELECT DECODE(x, a, b, c, d)"},
	} {
		tree, err := ParseOne(c[0], "redshift")
		if err != nil {
			t.Errorf("%s: %v", c[0], err)
			continue
		}
		if got, err := Generate(tree, "redshift"); err != nil || got != c[1] {
			t.Errorf("%s: got %q, %v", c[0], got, err)
		}
	}

	cases := map[string]string{
		"SELECT DECODE(a, 1, 'one')": "DecodeCase",
		"SELECT DECODE(a, 'utf8')":   "Decode",
		"SELECT DECODE(a)":           "Decode",
	}
	for sql, class := range cases {
		tree, err := ParseOne(sql, "redshift")
		if err != nil {
			t.Errorf("%s: %v", sql, err)
			continue
		}
		if len(tree.FindAll(class)) != 1 {
			t.Errorf("%s: want one %s", sql, class)
		}
	}

	for _, sql := range []string{"SELECT DECODE()", "SELECT DECODE(a, 1", "SELECT DECODE(a,)"} {
		if _, err := ParseOne(sql, "redshift"); err == nil {
			t.Errorf("%s: read a malformed DECODE", sql)
		}
	}
}

package sqlglot

import "testing"

// Redshift's CREATE TABLE distribution and sort clauses. DISTSTYLE is a
// value property the generated table already reads; DISTKEY and SORTKEY
// put their names in `this`, so they are read beside it. A parse or
// generate error fails the row.
func TestCreateDistAndSortKey(t *testing.T) {
	cases := [][3]string{
		{"redshift", "CREATE TABLE SOUP (LIKE other_table) DISTKEY(soup1) SORTKEY(soup2) DISTSTYLE ALL",
			"CREATE TABLE SOUP (LIKE other_table) DISTKEY(soup1) SORTKEY(soup2) DISTSTYLE ALL"},
		{"redshift", "CREATE TABLE sales (salesid INTEGER NOT NULL) DISTKEY(listid) COMPOUND SORTKEY(listid, sellerid) DISTSTYLE AUTO",
			"CREATE TABLE sales (salesid INTEGER NOT NULL) DISTKEY(listid) COMPOUND SORTKEY(listid, sellerid) DISTSTYLE AUTO"},
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
}

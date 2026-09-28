package sqlglot

import "testing"

func TestMySQLBareSetItems(t *testing.T) {
	cases := []string{
		"SET CHARACTER SET utf8",
		"SET CHARACTER SET 'utf8'",
		"SET CHARACTER SET DEFAULT",
		"SET NAMES utf8",
		"SET NAMES 'utf8'",
		"SET NAMES DEFAULT",
		"SET NAMES utf8 COLLATE utf8_unicode_ci",
		"SET NAMES 'utf8' COLLATE 'utf8_unicode_ci'",
		"SET TRANSACTION READ ONLY",
		"SET GLOBAL TRANSACTION ISOLATION LEVEL SERIALIZABLE",
		"SET GLOBAL TRANSACTION ISOLATION LEVEL REPEATABLE READ, READ WRITE",
	}
	for _, sql := range cases {
		tree, err := ParseOne(sql, "mysql")
		if err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		got, gerr := Generate(tree, "mysql")
		if gerr != nil || got != sql {
			t.Fatalf("%s wrote %q (%v)", sql, got, gerr)
		}
	}
}

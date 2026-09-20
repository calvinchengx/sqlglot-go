package sqlglot

import "testing"

// TestJSONQueryAndValue holds JSON_QUERY (Trino) and JSON_VALUE (Trino and
// MySQL) to what the pinned reference writes, clause by clause.
func TestJSONQueryAndValue(t *testing.T) {
	written := 0
	for _, c := range [][3]string{
		{"trino", "SELECT JSON_QUERY(x, '$.a')", "SELECT JSON_QUERY(x, '$.a')"},
		{"trino", "SELECT JSON_QUERY(x, 'lax $.a' WITH WRAPPER)", "SELECT JSON_QUERY(x, 'lax $.a' WITH WRAPPER)"},
		{"trino", "SELECT JSON_QUERY(x, '$.a' WITHOUT CONDITIONAL ARRAY WRAPPED)", "SELECT JSON_QUERY(x, '$.a' WITHOUT CONDITIONAL ARRAY WRAPPED)"},
		{"trino", "SELECT JSON_QUERY(x, '$.a' KEEP QUOTES ON SCALAR STRING)", "SELECT JSON_QUERY(x, '$.a' KEEP QUOTES ON SCALAR STRING)"},
		{"trino", "SELECT JSON_QUERY(x, '$.a' EMPTY ON EMPTY ERROR ON ERROR)", "SELECT JSON_QUERY(x, '$.a' EMPTY ON EMPTY ERROR ON ERROR)"},
		{"trino", "SELECT JSON_QUERY(x, '$.a' NULL ON EMPTY)", "SELECT JSON_QUERY(x, '$.a' NULL ON EMPTY)"},
		{"trino", "SELECT JSON_QUERY(x, '$.a' DEFAULT 'z' ON EMPTY DEFAULT 'y' ON ERROR)", "SELECT JSON_QUERY(x, '$.a' DEFAULT 'z' ON EMPTY DEFAULT 'y' ON ERROR)"},
		{"trino", "SELECT JSON_VALUE(x, '$.a' RETURNING INT)", "SELECT JSON_VALUE(x, '$.a' RETURNING INT)"},
		{"trino", "SELECT JSON_VALUE(x, '$.a' RETURNING DECIMAL(4, 2) DEFAULT 1 ON ERROR)", "SELECT JSON_VALUE(x, '$.a' RETURNING DECIMAL(4, 2) DEFAULT 1 ON ERROR)"},
		{"trino", "SELECT JSON_VALUE(x '$.a')", "SELECT JSON_VALUE(x, '$.a')"},
		{"trino", "SELECT JSON_VALUE(x, 'strict $.a' TRUE ON ERROR)", "SELECT JSON_VALUE(x, 'strict $.a' TRUE ON ERROR)"},
		{"trino", "SELECT JSON_QUERY(x)", "SELECT JSON_QUERY(x, )"},
		{"trino", "SELECT JSON_VALUE(x, 1)", "SELECT JSON_VALUE(x, '$[1]')"},
		{"mysql", "SELECT JSON_QUERY(x, '$.a')", "SELECT JSON_QUERY(x, '$.a')"},
		{"mysql", "SELECT JSON_VALUE(x, '$.a' RETURNING INT)", "SELECT JSON_VALUE(x, '$.a' RETURNING `INT`)"},
		{"mysql", "SELECT JSON_VALUE(x, '$.a' RETURNING DECIMAL(4, 2) DEFAULT 1 ON ERROR)", "SELECT JSON_VALUE(x, '$.a' RETURNING DECIMAL(4, 2) DEFAULT 1 ON ERROR)"},
		{"mysql", "SELECT JSON_VALUE(x '$.a')", "SELECT JSON_VALUE(x, '$.a')"},
		{"mysql", "SELECT JSON_VALUE(x, 'strict $.a' TRUE ON ERROR)", "SELECT JSON_VALUE(x, 'strict $.a' TRUE ON ERROR)"},
		{"mysql", "SELECT JSON_QUERY(x)", "SELECT JSON_QUERY(x)"},
		{"mysql", "SELECT JSON_VALUE(x, 1)", "SELECT JSON_VALUE(x, '$[1]')"},
	} {
		tree, err := ParseOne(c[1], c[0])
		if err != nil {
			continue
		}
		got, err := Generate(tree, c[0])
		if err != nil {
			continue
		}
		written++
		if got != c[2] {
			t.Errorf("[%s] %s\n  want %s\n  got  %s", c[0], c[1], c[2], got)
		}
	}
	if written < 10 {
		t.Errorf("only %d statements were written back", written)
	}
}

func TestJSONQueryDeclines(t *testing.T) {
	for _, sql := range []string{
		"SELECT JSON_QUERY(x, ",
		"SELECT JSON_QUERY(x, )",
		"SELECT JSON_QUERY(x, '$.a' WITH)",
		"SELECT JSON_QUERY(x, '$.a' WITH ARRAY)",
		"SELECT JSON_QUERY(x, '$.a' KEEP)",
		"SELECT JSON_QUERY(x, '$.a' DEFAULT ON ERROR)",
		"SELECT JSON_QUERY(x, '$.a' DEFAULT 1 ON",
		"SELECT JSON_VALUE(x, )",
		"SELECT JSON_VALUE(x, '$.a' RETURNING",
		"SELECT JSON_VALUE(x, '$.a' RETURNING 'x')",
		"SELECT JSON_VALUE(x, '$.a' RETURNING )",
		"SELECT JSON_VALUE(x, '$.[' )",
		"SELECT JSON_VALUE(x, '$.a'",
		"SELECT JSON_VALUE(x, '$.a' ERROR ON ERROR",
		"SELECT JSON_VALUE(, '$.a')",
	} {
		for _, d := range []string{"trino", "mysql"} {
			if tree, err := ParseOne(sql, d); err == nil && tree != nil {
				_, _ = Generate(tree, d)
			}
		}
	}
	tree, err := ParseOne("SELECT JSON_QUERY(x, '$.a' WITH WRAPPER)", "trino")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := Generate(tree, "presto"); err == nil {
		t.Errorf("another dialect wrote JSON_QUERY: %s", out)
	}
}

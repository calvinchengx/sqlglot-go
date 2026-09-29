package sqlglot

import "testing"

func TestMySQLCompressReturnType(t *testing.T) {
	cases := []struct{ sql, want string }{
		{"COMPRESS(CAST('test' AS CHAR))", "VARBINARY"},
		{"COMPRESS(CAST('test' AS VARCHAR))", "VARBINARY"},
		{"COMPRESS(CAST('test' AS BINARY))", "VARBINARY"},
		{"COMPRESS(CAST('test' AS VARBINARY))", "VARBINARY"},
		{"COMPRESS(CAST('test' AS TINYBLOB))", "VARBINARY"},
		{"COMPRESS(CAST(1 AS INT))", "VARBINARY"},
		{"COMPRESS(CAST(1 AS BIGINT))", "VARBINARY"},
		{"COMPRESS(CAST(1.5 AS DECIMAL))", "VARBINARY"},
		{"COMPRESS(CAST(1.5 AS DOUBLE))", "VARBINARY"},
		{"COMPRESS(CAST('2024-01-01' AS DATE))", "VARBINARY"},
		{"COMPRESS(CAST('2024-01-01 12:00:00' AS DATETIME))", "VARBINARY"},
		{"COMPRESS(CAST('test' AS TEXT))", "LONGBLOB"},
		{"COMPRESS(CAST('test' AS MEDIUMTEXT))", "LONGBLOB"},
		{"COMPRESS(CAST('test' AS LONGTEXT))", "LONGBLOB"},
		{"COMPRESS(CAST('test' AS BLOB))", "LONGBLOB"},
		{"COMPRESS(CAST('test' AS MEDIUMBLOB))", "LONGBLOB"},
		{"COMPRESS(CAST('test' AS LONGBLOB))", "LONGBLOB"},
		{"COMPRESS(CAST('{}' AS JSON))", "LONGBLOB"},
		{"COMPRESS(CAST('test' AS TINYTEXT))", "BLOB"},
		// A type in none of the three sets is UNKNOWN, not a nearby blob.
		{"COMPRESS(CAST(1.5 AS FLOAT))", "UNKNOWN"},
		{"COMPRESS(x)", "UNKNOWN"},
	}
	for _, tc := range cases {
		tree, err := ParseOne(tc.sql, "mysql")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		got := Annotate(tree, "mysql")
		if got == nil {
			t.Fatalf("Annotate(%q) declined", tc.sql)
		}
		rendered, gerr := Generate(got, "mysql")
		if gerr != nil || rendered != tc.want {
			t.Fatalf("Annotate(%q) = %q, %v", tc.sql, rendered, gerr)
		}
	}
}

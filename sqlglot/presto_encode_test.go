package sqlglot

import "testing"

func TestPrestoEncodeDecodeInvalidCharset(t *testing.T) {
	cases := []struct{ sql, want string }{
		{"ENCODE(x, 'invalid')", "TO_UTF8(x)"},
		{"DECODE(x, 'invalid')", "FROM_UTF8(x)"},
	}
	for _, tc := range cases {
		tree, err := ParseOne(tc.sql, "presto")
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", tc.sql, err)
		}
		got, gerr := Generate(tree, "presto")
		if gerr != nil || got != tc.want {
			t.Fatalf("Generate(%q) = %q, %v", tc.sql, got, gerr)
		}
	}
}

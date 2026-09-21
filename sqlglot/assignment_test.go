package sqlglot

import "testing"

// A bare `name := value` is an assignment wherever it stands, not a struct
// field: the generator fuzzer found the port writing `C:=0` back as `'C': 0`
// in DuckDB, which it then could not read.
func TestBareAssignment(t *testing.T) {
	written := 0
	for _, c := range [][3]string{
		{"", "C:=0", "C := 0"},
		{"", "C := 0", "C := 0"},
		{"", "a:=1", "a := 1"},
		{"", "SELECT a := 1", "SELECT a := 1"},
		{"", "SELECT x, a := 1 FROM t", "SELECT x, a := 1 FROM t"},
		{"", "SELECT STRUCT(a := 1)", "SELECT STRUCT(1 AS a)"},
		{"", "SELECT {'a': 1}", "SELECT STRUCT(1 AS a)"},
		{"tsql", "C:=0", "C := 0"},
		{"tsql", "C := 0", "C := 0"},
		{"tsql", "a:=1", "a := 1"},
		{"tsql", "SELECT a := 1", "SELECT a := 1"},
		{"tsql", "SELECT x, a := 1 FROM t", "SELECT x, a := 1 FROM t"},
		{"tsql", "SELECT STRUCT(a := 1)", "SELECT STRUCT(1 AS a)"},
		{"tsql", "SELECT {'a': 1}", "SELECT STRUCT(1 AS a)"},
		{"postgres", "C:=0", "C := 0"},
		{"postgres", "C := 0", "C := 0"},
		{"postgres", "a:=1", "a := 1"},
		{"postgres", "SELECT a := 1", "SELECT a := 1"},
		{"postgres", "SELECT x, a := 1 FROM t", "SELECT x, a := 1 FROM t"},
		{"postgres", "SELECT STRUCT(a := 1)", "SELECT STRUCT(1 AS a)"},
		{"postgres", "SELECT {'a': 1}", "SELECT STRUCT(1 AS a)"},
		{"duckdb", "C:=0", "C := 0"},
		{"duckdb", "C := 0", "C := 0"},
		{"duckdb", "a:=1", "a := 1"},
		{"duckdb", "SELECT a := 1", "SELECT a := 1"},
		{"duckdb", "SELECT x, a := 1 FROM t", "SELECT x, a := 1 FROM t"},
		{"duckdb", "SELECT STRUCT(a := 1)", "SELECT {'a': 1}"},
		{"duckdb", "SELECT {'a': 1}", "SELECT {'a': 1}"},
		{"databricks", "C:=0", "C := 0"},
		{"databricks", "C := 0", "C := 0"},
		{"databricks", "a:=1", "a := 1"},
		{"databricks", "SELECT a := 1", "SELECT a := 1"},
		{"databricks", "SELECT x, a := 1 FROM t", "SELECT x, a := 1 FROM t"},
		{"databricks", "SELECT STRUCT(a := 1)", "SELECT STRUCT(1 AS a)"},
		{"redshift", "C:=0", "C := 0"},
		{"redshift", "C := 0", "C := 0"},
		{"redshift", "a:=1", "a := 1"},
		{"redshift", "SELECT a := 1", "SELECT a := 1"},
		{"redshift", "SELECT x, a := 1 FROM t", "SELECT x, a := 1 FROM t"},
		{"redshift", "SELECT STRUCT(a := 1)", "SELECT STRUCT(1 AS a)"},
		{"redshift", "SELECT {'a': 1}", "SELECT STRUCT(1 AS a)"},
		{"materialize", "C:=0", "C => 0"},
		{"materialize", "C := 0", "C => 0"},
		{"materialize", "a:=1", "a => 1"},
		{"materialize", "SELECT a := 1", "SELECT a => 1"},
		{"materialize", "SELECT x, a := 1 FROM t", "SELECT x, a => 1 FROM t"},
		{"materialize", "SELECT STRUCT(a := 1)", "SELECT STRUCT(1 AS a)"},
		{"materialize", "SELECT {'a': 1}", "SELECT STRUCT(1 AS a)"},
		{"risingwave", "C:=0", "C := 0"},
		{"risingwave", "C := 0", "C := 0"},
		{"risingwave", "a:=1", "a := 1"},
		{"risingwave", "SELECT a := 1", "SELECT a := 1"},
		{"risingwave", "SELECT x, a := 1 FROM t", "SELECT x, a := 1 FROM t"},
		{"risingwave", "SELECT STRUCT(a := 1)", "SELECT STRUCT(1 AS a)"},
		{"risingwave", "SELECT {'a': 1}", "SELECT STRUCT(1 AS a)"},
		{"fabric", "C:=0", "C := 0"},
		{"fabric", "C := 0", "C := 0"},
		{"fabric", "a:=1", "a := 1"},
		{"fabric", "SELECT a := 1", "SELECT a := 1"},
		{"fabric", "SELECT x, a := 1 FROM t", "SELECT x, a := 1 FROM t"},
		{"fabric", "SELECT STRUCT(a := 1)", "SELECT STRUCT(1 AS a)"},
		{"fabric", "SELECT {'a': 1}", "SELECT STRUCT(1 AS a)"},
		{"presto", "C:=0", "C := 0"},
		{"presto", "C := 0", "C := 0"},
		{"presto", "a:=1", "a := 1"},
		{"presto", "SELECT a := 1", "SELECT a := 1"},
		{"presto", "SELECT x, a := 1 FROM t", "SELECT x, a := 1 FROM t"},
		{"presto", "SELECT STRUCT(a := 1)", "SELECT CAST(ROW(1) AS ROW(a INTEGER))"},
		{"presto", "SELECT {'a': 1}", "SELECT CAST(ROW(1) AS ROW(a INTEGER))"},
		{"trino", "C:=0", "C := 0"},
		{"trino", "C := 0", "C := 0"},
		{"trino", "a:=1", "a := 1"},
		{"trino", "SELECT a := 1", "SELECT a := 1"},
		{"trino", "SELECT x, a := 1 FROM t", "SELECT x, a := 1 FROM t"},
		{"trino", "SELECT STRUCT(a := 1)", "SELECT CAST(ROW(1) AS ROW(a INTEGER))"},
		{"trino", "SELECT {'a': 1}", "SELECT CAST(ROW(1) AS ROW(a INTEGER))"},
		{"dremio", "C:=0", "C := 0"},
		{"dremio", "C := 0", "C := 0"},
		{"dremio", "a:=1", "a := 1"},
		{"dremio", "SELECT a := 1", "SELECT a := 1"},
		{"dremio", "SELECT x, a := 1 FROM t", "SELECT x, a := 1 FROM t"},
		{"dremio", "SELECT STRUCT(a := 1)", "SELECT STRUCT(1 AS a)"},
		{"dremio", "SELECT {'a': 1}", "SELECT STRUCT(1 AS a)"},
		{"mysql", "C:=0", "C := 0"},
		{"mysql", "C := 0", "C := 0"},
		{"mysql", "a:=1", "a := 1"},
		{"mysql", "SELECT a := 1", "SELECT a := 1"},
		{"mysql", "SELECT x, a := 1 FROM t", "SELECT x, a := 1 FROM t"},
		{"mysql", "SELECT STRUCT(a := 1)", "SELECT STRUCT(1 AS a)"},
		{"mysql", "SELECT {'a': 1}", "SELECT STRUCT(1 AS a)"},
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
	if written < 40 {
		t.Errorf("only %d statements were written back", written)
	}
}

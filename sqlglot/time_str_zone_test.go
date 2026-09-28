package sqlglot

import "testing"

func TestTimeStrToTimeZone(t *testing.T) {
	sql := "SELECT TIME_STR_TO_TIME('2023-01-01 13:14:15-08:00', 'America/Los_Angeles')"
	want := "SELECT TIMESTAMP('2023-01-01 13:14:15-08:00')"
	tree, err := ParseOne(sql, "mysql")
	if err != nil {
		t.Fatalf("ParseOne: %v", err)
	}
	got, gerr := Generate(tree, "mysql")
	if gerr != nil || got != want {
		t.Fatalf("Generate = %q, %v", got, gerr)
	}
}

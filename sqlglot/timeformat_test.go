package sqlglot

import "testing"

// formatTime is a longest-match replacement: `yyyy` must beat `yy`, and text
// that matches nothing is carried through untouched.
func TestFormatTime(t *testing.T) {
	mapping := map[string]string{"yyyy": "%Y", "yy": "%y", "MM": "%m", "dd": "%d"}
	for _, c := range []struct{ in, want string }{
		{"yyyy-MM-dd", "%Y-%m-%d"},
		{"yy", "%y"},
		{"yyyy", "%Y"},
		{"", ""},
		{"zzz", "zzz"},
		{"yyyyMMdd", "%Y%m%d"},
	} {
		if got := formatTime(c.in, mapping); got != c.want {
			t.Errorf("formatTime(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// An empty mapping is the identity, which is how DuckDB reaches this at
	// all: its formats are already the reference's spelling.
	if got := formatTime("%Y-%m-%d", nil); got != "%Y-%m-%d" {
		t.Errorf("empty mapping changed %q to %q", "%Y-%m-%d", got)
	}
}

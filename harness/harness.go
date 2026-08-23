// Package harness verifies the port against the Python reference.
//
// testdata/expected holds one JSON file per statement: the SQL, its dialect,
// and the tree the reference produced (serde.dump format). The port parses
// the same SQL, dumps its own tree the same way, and the two are diffed.
// A mismatch is a failing test; the reference is the oracle.
//
// Coverage is reported as a number per dialect -- what fraction of the
// reference corpus the port reproduces identically -- and written to
// testdata/coverage.json so the README can show it and CI can refuse a
// regression. A statement the port cannot parse counts against coverage;
// it never counts as a silent divergence.
package harness

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// RefToken is one token as the reference produced it. The short field names
// are the oracle's, kept short because there are a few hundred thousand of them.
type RefToken struct {
	Type     string   `json:"t"`
	Text     string   `json:"x"`
	Line     int      `json:"l"`
	Col      int      `json:"c"`
	Start    int      `json:"s"`
	End      int      `json:"e"`
	Comments []string `json:"o,omitempty"`
}

func (t RefToken) String() string {
	s := fmt.Sprintf("%s(%q) @%d:%d [%d..%d]", t.Type, t.Text, t.Line, t.Col, t.Start, t.End)
	if len(t.Comments) > 0 {
		s += fmt.Sprintf(" comments=%q", t.Comments)
	}
	return s
}

// Expectation is one reference statement, its token stream and its tree.
type Expectation struct {
	Key     string           `json:"-"`
	Dialect string           `json:"dialect"`
	SQL     string           `json:"sql"`
	Tokens  []RefToken       `json:"tokens"`
	Tree    []map[string]any `json:"tree"`
}

// Index lists every expectation and the reference commit they came from.
type Index struct {
	Reference  string `json:"reference"`
	Count      int    `json:"count"`
	Statements []struct {
		Key     string `json:"key"`
		Dialect string `json:"dialect"`
		SQL     string `json:"sql"`
	} `json:"statements"`
}

// Load reads every expectation under dir.
func Load(dir string) (*Index, []Expectation, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "index.json")) //nolint:gosec // G304: a test fixture directory the caller names
	if err != nil {
		return nil, nil, fmt.Errorf("no index at %s (run harness/oracle.py): %w", dir, err)
	}
	var idx Index
	if err := json.Unmarshal(raw, &idx); err != nil {
		return nil, nil, err
	}
	out := make([]Expectation, 0, len(idx.Statements))
	for _, s := range idx.Statements {
		b, err := os.ReadFile(filepath.Join(dir, s.Key+".json")) //nolint:gosec // G304: keys come from the index this harness wrote
		if err != nil {
			return nil, nil, err
		}
		var e Expectation
		if err := json.Unmarshal(b, &e); err != nil {
			return nil, nil, fmt.Errorf("%s: %w", s.Key, err)
		}
		e.Key = s.Key
		out = append(out, e)
	}
	return &idx, out, nil
}

// Normalise strips what the port does not reproduce and the comparison must
// not care about: source positions ("m") and comments ("o"). Everything
// else -- class, parent index, arg key, list flag, leaf value -- is
// structure, and must match exactly.
func Normalise(tree []map[string]any) []map[string]any {
	out := make([]map[string]any, len(tree))
	for i, rec := range tree {
		n := map[string]any{}
		for k, v := range rec {
			if k == "m" || k == "o" {
				continue
			}
			// A type annotation is its own nested record list, and carries
			// positions of its own; they are metadata there too.
			if k == "t" {
				n[k] = normaliseNested(v)
				continue
			}
			n[k] = v
		}
		out[i] = n
	}
	return out
}

func normaliseNested(v any) any {
	var records []any
	switch t := v.(type) {
	case []any:
		records = t
	case []map[string]any:
		// The port's own dump is typed; the reference's arrives from JSON.
		records = make([]any, len(t))
		for i, r := range t {
			records[i] = r
		}
	default:
		return v
	}
	out := make([]any, 0, len(records))
	for _, r := range records {
		rec, ok := r.(map[string]any)
		if !ok {
			out = append(out, r)
			continue
		}
		n := map[string]any{}
		for k, val := range rec {
			if k == "m" || k == "o" {
				continue
			}
			n[k] = val
		}
		out = append(out, n)
	}
	return out
}

// Diff returns a human-readable description of the first disagreement
// between two normalised dumps, or "" if they match.
func Diff(want, got []map[string]any) string {
	canon := func(m map[string]any) string {
		b, _ := json.Marshal(sortedCopy(m))
		return string(b)
	}
	n := len(want)
	if len(got) < n {
		n = len(got)
	}
	for i := 0; i < n; i++ {
		w, g := canon(want[i]), canon(got[i])
		if w != g {
			return fmt.Sprintf("record %d\n  want %s\n  got  %s", i, w, g)
		}
	}
	if len(want) != len(got) {
		return fmt.Sprintf("record count: want %d, got %d", len(want), len(got))
	}
	return ""
}

// sortedCopy makes json.Marshal deterministic: Go maps marshal with sorted
// keys already, but numeric leaves arrive as float64 from JSON and as int
// from the port, so both are coerced to the same representation first.
func sortedCopy(m map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		out[k] = coerce(v)
	}
	return out
}

func coerce(v any) any {
	switch x := v.(type) {
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case []any:
		for i := range x {
			x[i] = coerce(x[i])
		}
		return x
	case map[string]any:
		return sortedCopy(x)
	}
	return v
}

// Coverage is the per-dialect tally a run produces.
type Coverage struct {
	Reference string                     `json:"reference"`
	Total     int                        `json:"total"`
	Matched   int                        `json:"matched"`
	ByDialect map[string]DialectCoverage `json:"by_dialect"`
}

// DialectCoverage counts one dialect's outcomes.
type DialectCoverage struct {
	Total      int `json:"total"`
	Matched    int `json:"matched"`
	Unparsed   int `json:"unparsed"`   // the port refused or errored
	Mismatched int `json:"mismatched"` // parsed, but a different tree
}

// Percent is matched/total, to one decimal.
func (c DialectCoverage) Percent() float64 {
	if c.Total == 0 {
		return 0
	}
	return float64(int(float64(c.Matched)/float64(c.Total)*1000)) / 10
}

// Write persists the tally, with dialects in a stable order.
func (c *Coverage) Write(path string) error {
	b, err := json.MarshalIndent(c, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

// Dialects lists the tally's dialect names, sorted, "neutral" for "".
func (c *Coverage) Dialects() []string {
	out := make([]string, 0, len(c.ByDialect))
	for d := range c.ByDialect {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

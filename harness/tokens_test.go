package harness_test

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"testing"

	"github.com/calvinchengx/sqlglot-go/harness"
	"github.com/calvinchengx/sqlglot-go/sqlglot"
)

// TestTokensAgainstReference holds the port's tokenizer to the reference's,
// token for token, across the whole corpus. Unlike the parser, the tokenizer
// has no gap tier: a statement the reference tokenizes, the port must tokenize
// identically. There is nowhere for a tokenizer to legitimately give up -- if
// it cannot lex a statement, the parser above it cannot see it at all.
func TestTokensAgainstReference(t *testing.T) {
	idx, exps, err := harness.Load("../testdata/expected")
	if err != nil {
		t.Fatalf("load expectations: %v", err)
	}
	t.Logf("reference %s: %d statements", idx.Reference[:12], len(exps))

	byDialect := map[string]*tokResult{}
	reported := 0

	for _, e := range exps {
		name := e.Dialect
		if name == "" {
			name = "neutral"
		}
		r, ok := byDialect[name]
		if !ok {
			r = &tokResult{}
			byDialect[name] = r
		}

		got, err := sqlglot.Tokenize(e.SQL, e.Dialect)
		if err != nil {
			r.failed++
			if reported < 20 {
				reported++
				t.Errorf("[%s] %s\n  tokenizer refused a statement the reference lexed: %v", name, e.SQL, err)
			}
			continue
		}

		if d := diffTokens(e.Tokens, got); d != "" {
			r.failed++
			if reported < 20 {
				reported++
				t.Errorf("[%s] %s\n  %s", name, e.SQL, d)
			}
			continue
		}
		r.ok++
	}

	total, failed := 0, 0
	names := make([]string, 0, len(byDialect))
	for n := range byDialect {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		r := byDialect[n]
		total += r.ok + r.failed
		failed += r.failed
		t.Logf("  %-11s %4d/%-4d", n, r.ok, r.ok+r.failed)
	}
	t.Logf("tokenizer: %d/%d statements match the reference exactly", total-failed, total)
	if failed > 20 {
		t.Errorf("%d statements diverge in total (first 20 shown)", failed)
	}

	writeTokenCoverage(t, idx.Reference, byDialect, total-failed, total)
}

type tokResult struct{ ok, failed int }

func writeTokenCoverage(t *testing.T, reference string, byDialect map[string]*tokResult, matched, total int) {
	t.Helper()
	out := map[string]any{"reference": reference, "matched": matched, "total": total}
	per := map[string]any{}
	for n, r := range byDialect {
		per[n] = map[string]int{"matched": r.ok, "total": r.ok + r.failed}
	}
	out["by_dialect"] = per
	b, err := json.MarshalIndent(out, "", " ")
	if err != nil {
		t.Fatalf("marshal token coverage: %v", err)
	}
	if err := os.WriteFile("../testdata/token_coverage.json", append(b, '\n'), 0o600); err != nil {
		t.Fatalf("write token coverage: %v", err)
	}
}

// diffTokens reports the first disagreement, and nothing after it: one precise
// difference is actionable where a hundred consequent ones are noise.
func diffTokens(want []harness.RefToken, got []sqlglot.Token) string {
	for i := range want {
		if i >= len(got) {
			return fmt.Sprintf("token %d: reference has %s, port ended after %d tokens", i, want[i], len(got))
		}
		w, g := want[i], asRefToken(got[i])
		if w.Type != g.Type || w.Text != g.Text || w.Line != g.Line || w.Col != g.Col ||
			w.Start != g.Start || w.End != g.End || !sameComments(w.Comments, g.Comments) {
			return fmt.Sprintf("token %d:\n    want %s\n    got  %s", i, w, g)
		}
	}
	if len(got) > len(want) {
		return fmt.Sprintf("token %d: reference ended after %d tokens, port has %s", len(want), len(want), asRefToken(got[len(want)]))
	}
	return ""
}

func asRefToken(t sqlglot.Token) harness.RefToken {
	name := t.Type.String()
	if len(name) > len("TokenType.") {
		name = name[len("TokenType."):]
	}
	return harness.RefToken{
		Type: name, Text: t.Text, Line: t.Line, Col: t.Col,
		Start: t.Start, End: t.End, Comments: t.Comments,
	}
}

func sameComments(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

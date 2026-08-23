package harness

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/calvinchengx/sqlglot-go/sqlglot"
)

// TestGapReport tallies WHY the port refuses what it refuses.
//
// It asserts nothing -- coverage is enforced by TestAgainstReference. It exists
// so the next rule to write is chosen by what actually blocks the corpus rather
// than by what seems important. Run it with:
//
//	go test ./harness/ -run TestGapReport -v
func TestGapReport(t *testing.T) {
	_, cases, err := Load("../testdata/expected")
	if err != nil {
		t.Fatal(err)
	}

	// Refusals read "unsupported: <reason> at <token>"; the token is incidental.
	reason := regexp.MustCompile(`unsupported statement: (.*?) at "`)

	counts := map[string]int{}
	examples := map[string][]string{}
	total := 0
	for _, c := range cases {
		if _, perr := sqlglot.ParseOne(c.SQL, c.Dialect); perr == nil {
			continue
		} else {
			total++
			key := "other"
			if m := reason.FindStringSubmatch(perr.Error()); m != nil {
				key = m[1]
			} else if strings.Contains(perr.Error(), "Error tokenizing") {
				key = "tokenizer refused the statement"
			}
			counts[key]++
			if len(examples[key]) < 4 {
				examples[key] = append(examples[key], truncate(c.SQL, 62))
			}
		}
	}

	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
		return keys[i] < keys[j]
	})

	t.Logf("%d statements refused, by reason:", total)
	for _, k := range keys {
		t.Logf("  %5d  %s", counts[k], k)
		for _, e := range examples[k] {
			t.Logf("            %s", e)
		}
	}
}

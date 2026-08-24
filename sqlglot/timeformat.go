package sqlglot

import "sort"

// formatTime rewrites a time format from the dialect's spelling into the
// reference's canonical one: T-SQL's `yyyy-MM-dd` is `%Y-%m-%d`. It is a
// longest-match replacement left to right, which is what the reference's trie
// walk amounts to -- `yyyy` must win over `yy`, and a run that matches nothing
// is carried through unchanged.
func formatTime(s string, mapping map[string]string) string {
	if s == "" || len(mapping) == 0 {
		return s
	}
	keys := make([]string, 0, len(mapping))
	for k := range mapping {
		keys = append(keys, k)
	}
	// Longest first, so the longest match at each position is the one tried.
	sort.Slice(keys, func(i, j int) bool {
		if len(keys[i]) != len(keys[j]) {
			return len(keys[i]) > len(keys[j])
		}
		return keys[i] < keys[j]
	})

	var out []byte
	for i := 0; i < len(s); {
		matched := false
		for _, k := range keys {
			if k != "" && i+len(k) <= len(s) && s[i:i+len(k)] == k {
				out = append(out, mapping[k]...)
				i += len(k)
				matched = true
				break
			}
		}
		if !matched {
			out = append(out, s[i])
			i++
		}
	}
	return string(out)
}

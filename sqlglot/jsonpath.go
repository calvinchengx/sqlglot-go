package sqlglot

import (
	"strconv"
	"strings"
	"unicode"
)

// parseJSONPath turns the string on the right of `->` into the JSONPath tree
// the reference builds. Only the three parts the corpus actually contains are
// supported -- root, key and subscript -- and anything else is refused rather
// than approximated: wildcards, recursive descent, slices, unions, filters and
// scripts all exist in sqlglot and none of them is here.
//
// The path always starts with a root, whether or not the string says `$`: the
// reference canonicalises `'a'` to `'$.a'`, which is why a bare name still
// produces two parts.
func parseJSONPath(path string) (*Expression, error) {
	parts := []*Expression{New("JSONPathRoot")}
	i := 0
	if strings.HasPrefix(path, "$") {
		i = 1
	}
	for i < len(path) {
		switch path[i] {
		case '.':
			if strings.HasPrefix(path[i:], "..") {
				return nil, errUnsupportedJSONPath("recursive descent")
			}
			i++
			// `$.y.*` is a wildcard KEY: every member of y.
			if i < len(path) && path[i] == '*' {
				parts = append(parts, New("JSONPathKey",
					Arg{"this", New("JSONPathWildcard")}))
				i++
				continue
			}
			key, next, err := readJSONPathKey(path, i)
			if err != nil {
				return nil, err
			}
			parts = append(parts, New("JSONPathKey", Arg{"this", key}))
			i = next
		case '[':
			node, next, err := readJSONPathBracket(path, i+1)
			if err != nil {
				return nil, err
			}
			parts = append(parts, node)
			i = next
		default:
			// A bare leading name, as in `'a'`.
			key, next, err := readJSONPathKey(path, i)
			if err != nil {
				return nil, err
			}
			if next == i {
				return nil, errUnsupportedJSONPath("path segment")
			}
			parts = append(parts, New("JSONPathKey", Arg{"this", key}))
			i = next
		}
	}
	return New("JSONPath", Arg{"expressions", parts}), nil
}

// readJSONPathKey reads a key: a double-quoted name, or a bare run up to the
// next separator. A `*` is a wildcard, which is not supported.
func readJSONPathKey(path string, i int) (string, int, error) {
	if i < len(path) && path[i] == '"' {
		end := strings.IndexByte(path[i+1:], '"')
		if end < 0 {
			return "", 0, errUnsupportedJSONPath("unterminated quoted key")
		}
		return path[i+1 : i+1+end], i + end + 2, nil
	}
	start := i
	for i < len(path) && path[i] != '.' && path[i] != '[' {
		switch {
		case path[i] == '*' || path[i] == '?' || path[i] == '(' || path[i] == '@':
			return "", 0, errUnsupportedJSONPath("path expression")
		case !isJSONPathVarByte(path[i]):
			return "", 0, errNotAJSONPath
		}
		i++
	}
	return path[start:i], i, nil
}

func readJSONPathBracket(path string, i int) (*Expression, int, error) {
	end := strings.IndexByte(path[i:], ']')
	if end < 0 {
		return nil, 0, errUnsupportedJSONPath("unclosed subscript")
	}
	body := path[i : i+end]
	next := i + end + 1
	// A quoted key is ONE quoted string, not merely something that starts and
	// ends with a quote: `[""@""]` is three things and the reference keeps the
	// whole path as a literal rather than reading a key called `"@"`.
	if len(body) >= 2 && body[0] == '"' && body[len(body)-1] == '"' &&
		!strings.Contains(body[1:len(body)-1], `"`) {
		return New("JSONPathKey", Arg{"this", body[1 : len(body)-1]}), next, nil
	}
	// `$.y[*]` is a wildcard SUBSCRIPT: every element of y. The other bracket
	// forms -- slices, unions, filters -- are still refused.
	if body == "*" {
		return New("JSONPathSubscript", Arg{"this", New("JSONPathWildcard")}), next, nil
	}
	n, err := strconv.Atoi(body)
	if err != nil {
		return nil, 0, errUnsupportedJSONPath("subscript " + body)
	}
	return New("JSONPathSubscript", Arg{"this", n}), next, nil
}

func errUnsupportedJSONPath(what string) error {
	return &UnsupportedError{Construct: "JSON path: " + what}
}

// errNotAJSONPath means the string is not a path at all. The reference's
// tokenizer fails on it and to_json_path hands the original literal back
// untouched, so `'/duck/0'` stays a string rather than becoming a path or a
// refusal. That is a different outcome from a path this port cannot READ,
// which stays a refusal.
var errNotAJSONPath = &UnsupportedError{Construct: "not a JSON path"}

// isJSONPathVarByte reports whether a byte can appear in a bare key. The
// reference tokenizes anything else as its own token and then has nowhere to
// put it -- which is how `/duck/0` and `en-US` end up as plain literals.
func isJSONPathVarByte(b byte) bool {
	return b == '_' || b == ' ' ||
		(b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// pythonInt reads a string exactly as Python's int() does, which is the test
// the reference applies to decide whether a JSON path segment is a subscript
// or a key. `'00'` is 0, `'-2'` is -2, `' 5'` is 5 and `'1_0'` is 10; `'1a'`
// and `”` are not integers at all.
//
// The digits are Go's unicode.IsDigit, which is category Nd -- and Nd is
// exactly the set Python's int() accepts, checked over every code point. That
// is NOT the same set as Python's str.isdigit(), which also takes superscripts
// and needed a generated table; using that one here would read '²' as 2.
func pythonInt(text string) (int, bool) {
	runes := []rune(text)
	i, j := 0, len(runes)
	for i < j && isPythonSpace(runes[i]) {
		i++
	}
	for j > i && isPythonSpace(runes[j-1]) {
		j--
	}
	if i == j {
		return 0, false
	}
	sign := 1
	if runes[i] == '+' || runes[i] == '-' {
		if runes[i] == '-' {
			sign = -1
		}
		i++
	}
	// An underscore may SEPARATE digits and may not lead, trail or double up.
	value, digits, prevUnderscore := 0, 0, false
	for ; i < j; i++ {
		r := runes[i]
		if r == '_' {
			if digits == 0 || prevUnderscore {
				return 0, false
			}
			prevUnderscore = true
			continue
		}
		if !unicode.IsDigit(r) {
			return 0, false
		}
		prevUnderscore = false
		digits++
		value = value*10 + digitValue(r)
	}
	if digits == 0 || prevUnderscore {
		return 0, false
	}
	return sign * value, true
}

// digitValue is the numeric value of a decimal digit in any script: Nd runs in
// blocks of ten from a zero, so the offset from that zero is the value.
func digitValue(r rune) int {
	for zero := r; ; zero-- {
		if !unicode.IsDigit(zero) {
			return int(r - zero - 1)
		}
		if r-zero > 9 {
			return 0
		}
	}
}

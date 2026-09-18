package sqlglot

import (
	"strconv"
	"strings"
	"unicode"
)

// ParseJSONPath parses a bare JSONPath selector the way the reference's
// `sqlglot.jsonpath.parse` does for its own base dialect: no dialect-specific
// tokenizer override, so `-` is never a var character.
func ParseJSONPath(path string) (*Expression, error) {
	return parseJSONPath(path, false)
}

// jpTokenKind is a JSONPath token's kind. JSONPath has its own tiny grammar,
// unrelated to the SQL tokenizer's TokenType -- reusing that one would carry
// a hundred token kinds a JSONPath selector never needs.
type jpTokenKind int

const (
	jpDollar jpTokenKind = iota
	jpDot
	jpColon
	jpComma
	jpDash
	jpStar
	jpQuestion
	jpAt
	jpLParen
	jpRParen
	jpLBracket
	jpRBracket
	jpString
	// jpIdentifier is a DOUBLE-quoted string, kept apart from jpString
	// (single-quoted) because the two are not interchangeable after a DOT:
	// `$."a b"` is a key, `$.'a b'` is refused. Inside a bracket the two are
	// the same thing, and parseLiteral accepts either.
	jpIdentifier
	jpNumber
	jpVar
)

// jpToken is one token: its kind, its TEXT (for a string, the content
// between the quotes with backslash-escapes kept literal, matching the
// reference's own STRING_ESCAPES=["\\"] -- an escape is not decoded, only
// prevented from ending the string early), and its RUNE offsets into the
// path, which a filter or script needs to recover its own source text.
type jpToken struct {
	kind       jpTokenKind
	text       string
	start, end int
}

// jsonPathTokenize turns a JSONPath selector into its tokens. dashInKeys is
// the Hive family's own JSONPathTokenizer: `-` is a var character there,
// the same as a letter, where the base tokenizer gives it a token of its own.
func jsonPathTokenize(path []rune, dashInKeys bool) ([]jpToken, error) {
	var toks []jpToken
	n := len(path)
	i := 0
	isSpecial := func(r rune) bool {
		return strings.ContainsRune("()[]:,.?@$*'\"", r)
	}
	for i < n {
		r := path[i]
		switch {
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			i++
		case r == '.':
			start := i
			i++
			if i < n && path[i] == '.' {
				i++
			}
			toks = append(toks, jpToken{jpDot, string(path[start:i]), start, i})
		case r == '\'' || r == '"':
			start := i
			quote := r
			i++
			var sb strings.Builder
			closed := false
			for i < n {
				c := path[i]
				if c == '\\' && i+1 < n {
					// An escaped DELIMITER loses its backslash -- `["\""]`'s
					// key is one quote character, not two. Anything else a
					// backslash precedes, `☺` included, keeps it: the
					// reference does not decode escapes, only uses the
					// backslash to keep the next character from closing the
					// string early.
					if path[i+1] != quote {
						sb.WriteRune(c)
					}
					sb.WriteRune(path[i+1])
					i += 2
					continue
				}
				if c == quote {
					i++
					closed = true
					break
				}
				sb.WriteRune(c)
				i++
			}
			if !closed {
				return nil, errUnsupportedJSONPath("unterminated string")
			}
			kind := jpString
			if quote == '"' {
				kind = jpIdentifier
			}
			toks = append(toks, jpToken{kind, sb.String(), start, i})
		case r >= '0' && r <= '9':
			start := i
			for i < n && path[i] >= '0' && path[i] <= '9' {
				i++
			}
			toks = append(toks, jpToken{jpNumber, string(path[start:i]), start, i})
		case r == '(':
			toks = append(toks, jpToken{jpLParen, "(", i, i + 1})
			i++
		case r == ')':
			toks = append(toks, jpToken{jpRParen, ")", i, i + 1})
			i++
		case r == '[':
			toks = append(toks, jpToken{jpLBracket, "[", i, i + 1})
			i++
		case r == ']':
			toks = append(toks, jpToken{jpRBracket, "]", i, i + 1})
			i++
		case r == ':':
			toks = append(toks, jpToken{jpColon, ":", i, i + 1})
			i++
		case r == ',':
			toks = append(toks, jpToken{jpComma, ",", i, i + 1})
			i++
		case r == '?':
			toks = append(toks, jpToken{jpQuestion, "?", i, i + 1})
			i++
		case r == '@':
			toks = append(toks, jpToken{jpAt, "@", i, i + 1})
			i++
		case r == '$':
			toks = append(toks, jpToken{jpDollar, "$", i, i + 1})
			i++
		case r == '*':
			toks = append(toks, jpToken{jpStar, "*", i, i + 1})
			i++
		case r == '-' && !dashInKeys:
			toks = append(toks, jpToken{jpDash, "-", i, i + 1})
			i++
		default:
			// A maximal run of characters that are none of the above: a
			// letter, digit, underscore, or anything outside ASCII, and --
			// for the Hive family -- a dash too. A run may start with a
			// digit only by falling through from a NUMBER that failed to
			// end the switch above, which cannot happen; a leading digit is
			// always its own NUMBER token, and a var run never starts on one.
			start := i
			for i < n {
				c := path[i]
				if c == ' ' || c == '\t' || c == '\n' || c == '\r' || isSpecial(c) {
					break
				}
				if c == '-' && !dashInKeys {
					break
				}
				i++
			}
			if i == start {
				return nil, errUnsupportedJSONPath("unexpected character")
			}
			toks = append(toks, jpToken{jpVar, string(path[start:i]), start, i})
		}
	}
	return toks, nil
}

// jpParser walks a token stream the same way the reference's closures over
// `i` do -- an index into the slice, moved by match and read by curr/prev.
type jpParser struct {
	toks []jpToken
	i    int
	path []rune
}

func (p *jpParser) curr() *jpToken {
	if p.i < len(p.toks) {
		return &p.toks[p.i]
	}
	return nil
}

func (p *jpParser) at(k jpTokenKind) bool {
	c := p.curr()
	return c != nil && c.kind == k
}

func (p *jpParser) advance() *jpToken {
	p.i++
	return &p.toks[p.i-1]
}

func (p *jpParser) match(k jpTokenKind) *jpToken {
	if p.at(k) {
		return p.advance()
	}
	return nil
}

// jpNoValue is parseLiteral's "nothing matched" sentinel -- the reference's
// own bare `False`, kept distinct from every real literal a JSONPath segment
// can hold (a string, an int, a Wildcard, a Script, a Filter).
type jpNoValue struct{}

// parseLiteral reads one value a bracket segment or a slice bound can be: a
// string, `*`, a `?`-filter or `(`-script (raw text, read past any NESTED
// bracket without trying to understand it), or a signed integer. jpNoValue
// means none of those was here -- not an error, since the caller may be
// reading an EMPTY slice bound (`$[:5]`) rather than a broken one.
func (p *jpParser) parseLiteral() (any, error) {
	if t := p.match(jpString); t != nil {
		return t.text, nil
	}
	if t := p.match(jpIdentifier); t != nil {
		return t.text, nil
	}
	if p.match(jpStar) != nil {
		return New("JSONPathWildcard"), nil
	}
	opened := p.match(jpQuestion)
	if opened == nil {
		opened = p.match(jpLParen)
	}
	if opened != nil {
		script := opened.kind == jpLParen
		startIdx := p.i
		for {
			if p.match(jpLBracket) != nil {
				if _, err := p.parseBracket(); err != nil {
					return nil, err
				}
			}
			c := p.curr()
			if c == nil || c.kind == jpRBracket {
				break
			}
			p.advance()
		}
		var end int
		if c := p.curr(); c != nil {
			end = c.start
		} else if len(p.toks) > 0 {
			end = p.toks[len(p.toks)-1].end
		}
		start := len(p.path)
		if startIdx < len(p.toks) {
			start = p.toks[startIdx].start
		}
		if start > end {
			start = end
		}
		text := string(p.path[start:end])
		if script {
			return New("JSONPathScript", Arg{"this", text}), nil
		}
		return New("JSONPathFilter", Arg{"this", text}), nil
	}
	sign := ""
	if p.match(jpDash) != nil {
		sign = "-"
	}
	if t := p.match(jpNumber); t != nil {
		n, err := strconv.Atoi(sign + t.text)
		if err != nil {
			return nil, errUnsupportedJSONPath("subscript " + sign + t.text)
		}
		return n, nil
	}
	if sign != "" {
		return nil, errUnsupportedJSONPath("a lone -")
	}
	return jpNoValue{}, nil
}

// parseSlice reads a bracket's ONE segment: a bare literal, or `start:end`
// / `start:end:step` where any of the three may be empty. Returning the bare
// literal when neither colon appears is what lets `$[0]` build a subscript
// rather than a one-sided slice.
func (p *jpParser) parseSlice() (any, error) {
	start, err := p.parseLiteral()
	if err != nil {
		return nil, err
	}
	var end, step any = nil, nil
	if p.match(jpColon) != nil {
		end, err = p.parseLiteral()
		if err != nil {
			return nil, err
		}
		if p.match(jpColon) != nil {
			step, err = p.parseLiteral()
			if err != nil {
				return nil, err
			}
		}
	}
	if end == nil && step == nil {
		return start, nil
	}
	slice := New("JSONPathSlice")
	setSliceBound(slice, "start", start)
	setSliceBound(slice, "end", end)
	setSliceBound(slice, "step", step)
	return slice, nil
}

// setSliceBound sets a JSONPathSlice bound the way the reference's own dump
// does: omitted when the bound was never reached (nil), and an EXPLICIT
// `false` when it was reached but empty (`$[:5]`'s start) -- the two look
// the same rendered, but only one survives a round trip through the tree.
func setSliceBound(slice *Expression, key string, v any) {
	if v == nil {
		return
	}
	if _, empty := v.(jpNoValue); empty {
		slice.Set(key, false)
		return
	}
	slice.Set(key, v)
}

// parseBracket reads what stands inside `[...]`: one segment, or several
// comma-separated into a JSONPathUnion. A segment that is a plain string
// becomes a JSONPathKey; a Script or Filter becomes a JSONPathSelector; a
// slice or a bare int or Wildcard becomes a JSONPathSubscript.
func (p *jpParser) parseBracket() (*Expression, error) {
	literal, err := p.parseSlice()
	if err != nil {
		return nil, err
	}
	_, empty := literal.(jpNoValue)
	if _, isStr := literal.(string); !isStr && empty {
		return nil, errUnsupportedJSONPath("empty segment")
	}
	indexes := []any{literal}
	for p.match(jpComma) != nil {
		literal, err = p.parseSlice()
		if err != nil {
			return nil, err
		}
		_, isStr := literal.(string)
		if _, empty := literal.(jpNoValue); isStr || !empty {
			indexes = append(indexes, literal)
		}
	}
	var node *Expression
	if len(indexes) == 1 {
		node = bracketItemNode(indexes[0])
	} else {
		// A union's own members are not wrapped in a node of their own kind
		// the way every other list in this port is -- the reference keeps a
		// bare int or string exactly as parseLiteral returned it, alongside
		// a real Wildcard/Script/Filter node where one of those was given.
		node = New("JSONPathUnion", Arg{"expressions", indexes})
	}
	if p.match(jpRBracket) == nil {
		return nil, errUnsupportedJSONPath("unclosed bracket")
	}
	return node, nil
}

// bracketItemNode is what a SINGLE bracket segment builds, by the shape the
// reference's own `_parse_bracket` reads off it: a string names a KEY, a
// Script or Filter is read through a SELECTOR, and everything else --
// a bare int, a Wildcard, a Slice -- is a SUBSCRIPT.
func bracketItemNode(v any) *Expression {
	switch val := v.(type) {
	case string:
		return New("JSONPathKey", Arg{"this", val})
	case *Expression:
		if val.Class == "JSONPathScript" || val.Class == "JSONPathFilter" {
			return New("JSONPathSelector", Arg{"this", val})
		}
		return New("JSONPathSubscript", Arg{"this", val})
	default:
		return New("JSONPathSubscript", Arg{"this", val})
	}
}

// parseVarText merges consecutive VAR-shaped tokens into one key, reading the
// raw SOURCE SUBSTRING rather than concatenating token text -- BigQuery
// allows spaces inside a key (`$. a b c '` is the key `" a b c "`), and only
// the source positions see those spaces; the tokens themselves do not carry
// them.
func (p *jpParser) parseVarText() string {
	prevEnd := 0
	if p.i > 0 {
		prevEnd = p.toks[p.i-1].end
	}
	for p.at(jpVar) {
		p.advance()
	}
	var end int
	if c := p.curr(); c != nil {
		end = c.start
	} else {
		end = len(p.path)
	}
	return string(p.path[prevEnd:end])
}

// parseJSONPath is the reference's `sqlglot.jsonpath.parse`: a selector
// string in, a JSONPath tree out. Canonicalised the same way -- the tree
// always starts with a root, whether or not the string itself opened with
// `$`, so a bare `field` still parses as `$.field`.
//
// One honest gap remains against the CTS: Python's int is unbounded, and a
// subscript or slice bound larger than Go's own int overflows rather than
// being read -- seven of the suite's 526 cases exist for no reason but to
// probe exactly that, and stay declined instead of losing precision quietly.
func parseJSONPath(path string, dashInKeys bool) (*Expression, error) {
	runes := []rune(path)
	toks, err := jsonPathTokenize(runes, dashInKeys)
	if err != nil {
		return nil, err
	}
	p := &jpParser{toks: toks, path: runes}
	p.match(jpDollar)

	parts := []*Expression{New("JSONPathRoot")}
	for p.curr() != nil {
		switch {
		case p.match(jpDot) != nil, p.match(jpColon) != nil:
			recursive := p.prevText() == ".."
			var value any
			switch {
			case p.at(jpVar):
				value = p.parseVarText()
			case p.match(jpIdentifier) != nil:
				value = p.prevText()
			case p.match(jpStar) != nil:
				value = New("JSONPathWildcard")
			default:
				value = nil
			}
			switch {
			case recursive:
				parts = append(parts, New("JSONPathRecursive", jsonPathRecursiveArgs(value)...))
			case value != nil:
				parts = append(parts, New("JSONPathKey", jsonPathKeyArg(value)))
			default:
				return nil, errUnsupportedJSONPath("key name or * after .")
			}
		case p.match(jpLBracket) != nil:
			node, berr := p.parseBracket()
			if berr != nil {
				return nil, berr
			}
			parts = append(parts, node)
		case p.at(jpVar):
			parts = append(parts, New("JSONPathKey", Arg{"this", p.parseVarText()}))
		case p.match(jpIdentifier) != nil:
			parts = append(parts, New("JSONPathKey", Arg{"this", p.prevText()}))
		case p.match(jpStar) != nil:
			parts = append(parts, New("JSONPathWildcard"))
		default:
			return nil, errUnsupportedJSONPath("unexpected token")
		}
	}
	return New("JSONPath", Arg{"expressions", parts}), nil
}

func (p *jpParser) prevText() string {
	if p.i == 0 {
		return ""
	}
	return p.toks[p.i-1].text
}

func jsonPathKeyArg(value any) Arg {
	if s, ok := value.(string); ok {
		return Arg{"this", s}
	}
	return Arg{"this", value}
}

func jsonPathRecursiveArgs(value any) []Arg {
	if value == nil {
		return nil
	}
	return []Arg{jsonPathKeyArg(value)}
}

func errUnsupportedJSONPath(what string) error {
	return &UnsupportedError{Construct: "JSON path: " + what}
}

// jsonPathKeptAsLiteral reports the SQL/JSON mode prefixes the reference
// keeps as written. `lax $.b` and `strict $.b` are not path syntax: the
// tokenizer fails on them and to_json_path hands the original string back
// rather than parsing `$` after the mode word. A path this port cannot
// read for any other reason is still refused in a function argument --
// Databricks turns `$.x-y` into a key, and keeping a Literal there would
// be a different tree.
func jsonPathKeptAsLiteral(text string) bool {
	s := strings.TrimLeftFunc(text, unicode.IsSpace)
	return strings.HasPrefix(s, "lax") || strings.HasPrefix(s, "strict")
}

// duckdbKeepsJSONPointer reports whether DuckDB's own to_json_path skips
// parsing this string as a path at all: a JSON Pointer, where every path
// starts with `/`, or its `[#-i]` back-of-list subscript. Trying to read
// either as JSONPath syntax would misread it, so the dialect avoids it
// entirely rather than fail into a warning.
func duckdbKeepsJSONPointer(text string) bool {
	return strings.HasPrefix(text, "/") || strings.Contains(text, "[#")
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

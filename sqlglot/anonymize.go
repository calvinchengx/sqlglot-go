package sqlglot

import (
	"math/big"
	"sort"
	"strings"
	"unicode"
)

// anonymizedTypes are the token kinds that hold something worth scrubbing --
// an identifier, a literal, a bare word. A keyword token is never one of
// these, so `SELECT`, `FROM`, `WINDOW` survive untouched: the reference's
// own comment is that a caller debugging SQL the guard refused still needs
// to see its SHAPE, only the values in it are sensitive.
var anonymizedTypes = map[TokenType]bool{
	TokBIT_STRING:      true,
	TokBYTE_STRING:     true,
	TokHEX_STRING:      true,
	TokHEREDOC_STRING:  true,
	TokIDENTIFIER:      true,
	TokNATIONAL_STRING: true,
	TokNUMBER:          true,
	TokRAW_STRING:      true,
	TokSTRING:          true,
	TokUNICODE_STRING:  true,
	TokVAR:             true,
}

// quotedTypes is anonymizedTypes minus the two kinds that never carry a
// quote mark of their own -- a NUMBER and a bare VAR -- which is what tells
// render's own _fit whether to look for one at all.
var quotedTypes = map[TokenType]bool{
	TokBIT_STRING:      true,
	TokBYTE_STRING:     true,
	TokHEX_STRING:      true,
	TokHEREDOC_STRING:  true,
	TokIDENTIFIER:      true,
	TokNATIONAL_STRING: true,
	TokRAW_STRING:      true,
	TokSTRING:          true,
	TokUNICODE_STRING:  true,
}

// rewrittenTypes are tokens `anonymize` builds or blanks itself -- a HINT's
// text and the synthetic UNKNOWN tail after a tokenize failure -- so render
// uses them verbatim rather than trying to fit them into a span a second time.
var rewrittenTypes = map[TokenType]bool{
	TokHINT:    true,
	TokUNKNOWN: true,
}

const anonymizeAlphabet = "abcdefghijklmnopqrstuvwxyz"

// Anonymize replaces sensitive tokens in sql -- identifiers, strings,
// numbers -- with fixed-width, length-preserving, consistent aliases (the
// same alias for every occurrence of the same original text), and blanks
// comment bodies and hint bodies, matching the reference's own
// sqlglot.anonymize.anonymize + render run together. The result is always
// the same LENGTH as sql: every alias is fitted back into the exact span its
// original token held, and the gaps between tokens keep their whitespace and
// comment markers with the comment BODY blanked.
//
// Never fails: a statement the tokenizer cannot finish still comes back --
// with a synthetic tail standing in for whatever it could not read -- since
// this exists to make SQL safe to log even when it is broken.
func Anonymize(sql, dialect string) string {
	cfg, ok := ConfigFor(dialect)
	if !ok {
		return sql
	}
	tokens := anonymizeTokenize(sql, cfg)
	tokens = anonymizeTokens(tokens, cfg)
	return renderAnonymized(sql, tokens, cfg)
}

// anonymizeTokenize tokenizes sql, keeping whatever the tokenizer managed
// before failing rather than discarding it -- the reference's own tokenizer
// keeps its partial token list on the exception it raises, which `Tokenize`'s
// public contract does not expose, so this reaches into the Tokenizer that
// built it instead. A failure appends the reference's own synthetic tail: the
// first two characters of what was left, so the delimiter that choked it is
// still visible, then dots to the same total length.
func anonymizeTokenize(sql string, cfg *Config) []Token {
	t := &Tokenizer{cfg: cfg}
	tokens, err := t.Tokenize(sql)
	if err == nil {
		return tokens
	}
	tokens = t.tokens
	runes := []rune(sql)
	length := len(runes)
	start := 0
	if n := len(tokens); n > 0 {
		start = tokens[n-1].End + 1
	}
	for start < length && unicode.IsSpace(runes[start]) {
		start++
	}
	if start >= length {
		return tokens
	}
	visible := start + 2
	if visible > length {
		visible = length
	}
	text := string(runes[start:visible]) + strings.Repeat(".", length-visible)
	line := 1 + strings.Count(string(runes[:start]), "\n")
	lastNL := strings.LastIndexByte(string(runes[:start]), '\n')
	col := start - lastNL
	return append(tokens, Token{
		Type: TokUNKNOWN, Text: text, Start: start, End: length - 1,
		Line: line, Col: col,
	})
}

// anonymizeTokens is the reference's own `anonymize` loop, over an already-
// tokenized list: blank every token's comments, blank a HINT's body, and
// replace every ANONYMIZED token's text with a counter-derived alias -- the
// SAME alias for a text seen before in this call, a fresh one otherwise.
func anonymizeTokens(tokens []Token, cfg *Config) []Token {
	out := make([]Token, len(tokens))
	copy(out, tokens)

	tables := cfg.Tables
	hintStart := cfg.HintStart
	hintEnd := cfg.Comments[hintStart]

	type seenKey struct {
		number bool
		text   string
	}
	seen := map[seenKey]string{}
	counter := 0

	for i := range out {
		tok := &out[i]
		if len(tok.Comments) > 0 {
			blanked := make([]string, len(tok.Comments))
			for j, c := range tok.Comments {
				blanked[j] = blankText(c)
			}
			tok.Comments = blanked
		}

		if tok.Type == TokHINT && hintStart != "" {
			text, stop := blankComment([]rune(tok.Text), 0, hintStart, hintEnd, hintEnd != "", cfg.NestedComments)
			runes := []rune(tok.Text)
			tok.Text = text + blankText(string(runes[stop:]))
			continue
		}

		if tok.Type == TokVAR && i+1 < len(out) && out[i+1].Type == TokL_PAREN && tables != nil {
			// NamedFunctions is every name the reference reads into a node
			// of its own -- the union the reference itself consults, FUNCTIONS
			// and FUNCTION_PARSERS together -- not the narrower Functions
			// table, which only holds the ones this port has WORKED OUT how
			// to build; a name recognized but not yet built is still a
			// function name and still not sensitive.
			if _, named := tables.NamedFunctions[strings.ToUpper(tok.Text)]; named {
				continue
			}
		}
		if !anonymizedTypes[tok.Type] || tok.Text == "" {
			continue
		}

		isNumber := tok.Type == TokNUMBER
		key := seenKey{isNumber, tok.Text}
		alias, ok := seen[key]
		if !ok {
			if isNumber {
				alias = numberAlias(counter, tok.Text)
			} else {
				alias = aliasFor(counter, tok.Text)
			}
			seen[key] = alias
			counter++
		}
		tok.Text = alias
	}
	return out
}

// aliasFor is the reference's `_alias`: counter, spelled in base 26 over
// `a`..`z`, right-padded with leading `a`s to the number of NON-whitespace
// characters `text` holds, then poured back into text's own shape -- a
// whitespace character keeps its place, everything else takes the next
// letter. `"my table"`'s two words each keep their own run of letters
// rather than becoming one run the space would then sit inside oddly.
func aliasFor(counter int, text string) string {
	var digits []byte
	c := counter
	for c > 0 {
		var d int
		c, d = c/len(anonymizeAlphabet), c%len(anonymizeAlphabet)
		digits = append(digits, anonymizeAlphabet[d])
	}
	for i, j := 0, len(digits)-1; i < j; i, j = i+1, j-1 {
		digits[i], digits[j] = digits[j], digits[i]
	}
	nonSpace := 0
	for _, r := range text {
		if !unicode.IsSpace(r) {
			nonSpace++
		}
	}
	letters := string(digits)
	if len(letters) < nonSpace {
		letters = strings.Repeat("a", nonSpace-len(letters)) + letters
	}
	var out strings.Builder
	i := 0
	for _, r := range text {
		if unicode.IsSpace(r) {
			out.WriteRune(r)
		} else {
			out.WriteByte(letters[i])
			i++
		}
	}
	return out.String()
}

// numberAlias is the reference's `_number_alias`: a counter-derived number
// the same WIDTH as the original in every part -- sign, integer digits,
// fraction digits, exponent digits -- so `1.50` becomes some other `d.dd`
// and `1e5` some other `de5`. Values wrap with big.Int rather than a machine
// word because the width itself can run past what one holds: a number over
// 4000 characters takes the reference's other path entirely, replacing only
// the FIRST digit seen with `1` and every digit after it with `0`, which
// blurs a value that long rather than trying to give it a matching fake one.
func numberAlias(counter int, text string) string {
	if len(text) > 4000 {
		var out strings.Builder
		digitsSeen := false
		for _, r := range text {
			if r >= '0' && r <= '9' {
				if digitsSeen {
					out.WriteByte('0')
				} else {
					out.WriteByte('1')
				}
				digitsSeen = true
			} else {
				out.WriteRune(r)
			}
		}
		return out.String()
	}

	sep := ""
	if strings.Contains(text, "e") {
		sep = "e"
	} else if strings.Contains(text, "E") {
		sep = "E"
	}
	mantissa, exponent := text, ""
	if sep != "" {
		parts := strings.SplitN(text, sep, 2)
		mantissa, exponent = parts[0], parts[1]
	}
	sign := ""
	if strings.HasPrefix(exponent, "-") || strings.HasPrefix(exponent, "+") {
		sign, exponent = exponent[:1], exponent[1:]
	}
	exponentLength := len(exponent)

	integer, fraction, hasDot := mantissa, "", false
	if dot := strings.IndexByte(mantissa, '.'); dot >= 0 {
		integer, fraction, hasDot = mantissa[:dot], mantissa[dot+1:], true
	}
	integerLength := len(integer)
	digits := integerLength + len(fraction)

	bigCounter := big.NewInt(int64(counter))
	ten := big.NewInt(10)
	pow := new(big.Int).Exp(ten, big.NewInt(int64(digits-1)), nil)
	span := new(big.Int).Mul(big.NewInt(9), pow)
	mantissaValue := new(big.Int).Mod(bigCounter, span)
	mantissaValue.Add(mantissaValue, pow)
	result := mantissaValue.String()
	if hasDot {
		result = result[:integerLength] + "." + result[integerLength:]
	}
	if sep != "" {
		result += sep + sign
		if exponentLength > 0 {
			powE := new(big.Int).Exp(ten, big.NewInt(int64(exponentLength-1)), nil)
			spanE := new(big.Int).Mul(big.NewInt(9), powE)
			quotient := new(big.Int).Div(bigCounter, span)
			expValue := new(big.Int).Mod(quotient, spanE)
			expValue.Add(expValue, powE)
			result += expValue.String()
		}
	}
	return result
}

// blankText replaces every non-whitespace character with a `.`, keeping
// whatever whitespace (and line structure) the original had.
func blankText(s string) string {
	var out strings.Builder
	for _, r := range s {
		if unicode.IsSpace(r) {
			out.WriteRune(r)
		} else {
			out.WriteRune('.')
		}
	}
	return out.String()
}

// blankComment blanks the BODY of the comment opening at i in runes, up to
// its own close marker (or end of line, for a `hasEnd=false` line comment),
// nested comments counted if the dialect allows them. Returns the blanked
// text and the rune index just past it.
func blankComment(runes []rune, i int, start, end string, hasEnd, nested bool) (string, int) {
	startRunes := []rune(start)
	body := i + len(startRunes)
	if !hasEnd {
		stop := indexRune(runes, '\n', body)
		if stop < 0 {
			stop = len(runes)
		}
		return start + blankText(string(runes[body:stop])), stop
	}
	endRunes := []rune(end)
	depth := 1
	j := body
	for j < len(runes) {
		if nested && hasPrefixAt(runes, startRunes, j) {
			depth++
			j += len(startRunes)
		} else if hasPrefixAt(runes, endRunes, j) {
			j += len(endRunes)
			depth--
			if depth == 0 {
				return start + blankText(string(runes[body:j-len(endRunes)])) + end, j
			}
		} else {
			j++
		}
	}
	return start + blankText(string(runes[body:])), len(runes)
}

func indexRune(runes []rune, target rune, from int) int {
	for i := from; i < len(runes); i++ {
		if runes[i] == target {
			return i
		}
	}
	return -1
}

func hasPrefixAt(runes, prefix []rune, at int) bool {
	if at+len(prefix) > len(runes) {
		return false
	}
	for i, r := range prefix {
		if runes[at+i] != r {
			return false
		}
	}
	return true
}

// redact blanks whatever stands BETWEEN two tokens -- and after the last
// one -- keeping whitespace and comment markers, comment BODIES blanked, and
// blanking everything else (an operator between statements that never
// became a token of its own, in a dialect that reads it loosely). Comments
// are tried longest-marker-first so a two-character opener is not read as
// two one-character ones.
func redact(runes []rune, comments [][2]string, nested bool) string {
	if len(runes) == 0 {
		return ""
	}
	allSpace := true
	for _, r := range runes {
		if !unicode.IsSpace(r) {
			allSpace = false
			break
		}
	}
	if allSpace {
		return string(runes)
	}
	var out strings.Builder
	i := 0
	for i < len(runes) {
		matched := false
		for _, c := range comments {
			start, end := c[0], c[1]
			if hasPrefixAt(runes, []rune(start), i) {
				text, next := blankComment(runes, i, start, end, end != "", nested)
				out.WriteString(text)
				i = next
				matched = true
				break
			}
		}
		if matched {
			continue
		}
		if unicode.IsSpace(runes[i]) {
			out.WriteRune(runes[i])
		} else {
			out.WriteByte('.')
		}
		i++
	}
	return out.String()
}

// renderAnonymized is the reference's `render`: rebuilds a string the exact
// LENGTH of sql, one token at a time. A token this port anonymized has its
// alias FITTED into the span it originally held; a HINT or the synthetic
// UNKNOWN tail is already the right shape and used as written; anything else
// -- a keyword, punctuation -- is copied from sql verbatim. The gaps between
// tokens are redacted rather than reconstructed.
func renderAnonymized(sql string, tokens []Token, cfg *Config) string {
	runes := []rune(sql)
	comments := sortedComments(cfg)
	quotes := sortedQuotes(cfg)

	var out strings.Builder
	prev := 0
	for _, tok := range tokens {
		gapEnd := tok.Start
		if gapEnd > len(runes) {
			gapEnd = len(runes)
		}
		if prev > gapEnd {
			prev = gapEnd
		}
		out.WriteString(redact(runes[prev:gapEnd], comments, cfg.NestedComments))
		prev = tok.End + 1
		spanEnd := prev
		if spanEnd > len(runes) {
			spanEnd = len(runes)
		}
		spanStart := tok.Start
		if spanStart > spanEnd {
			spanStart = spanEnd
		}
		span := runes[spanStart:spanEnd]
		switch {
		case rewrittenTypes[tok.Type]:
			out.WriteString(tok.Text)
		case anonymizedTypes[tok.Type]:
			out.WriteString(fitAlias(span, tok.Text, quotes, tok.Type))
		default:
			out.WriteString(string(span))
		}
	}
	if prev > len(runes) {
		prev = len(runes)
	}
	out.WriteString(redact(runes[prev:], comments, cfg.NestedComments))
	return out.String()
}

// fitAlias fits alias into the QUOTED region of span, so the two are always
// the same length: the quote marks (or a heredoc's own tag, which is part of
// ITS delimiter, `$tag$body$tag$`) survive untouched, and the alias fills
// whatever stands between them, right-padded with `a` (or `0` for a number)
// if it is shorter.
func fitAlias(span []rune, alias string, quotes [][2]string, tokType TokenType) string {
	start, end := 0, 0
	if quotedTypes[tokType] {
		for _, q := range quotes {
			open, close := []rune(q[0]), []rune(q[1])
			if len(span) >= len(open)+len(close) && hasPrefixAt(span, open, 0) &&
				hasPrefixAt(span, close, len(span)-len(close)) {
				start, end = len(open), len(close)
				if tokType == TokHEREDOC_STRING {
					openEnd := indexOfRunes(span, close, start)
					closeStart := lastIndexOfRunes(span, open, len(span)-end)
					if openEnd >= 0 && start <= openEnd && openEnd < closeStart {
						start, end = openEnd+len(close), len(span)-closeStart
					}
				}
				break
			}
		}
	}
	width := len(span) - start - end
	if width < 0 {
		width = 0
	}
	pad := byte('a')
	if tokType == TokNUMBER {
		pad = '0'
	}
	aliasRunes := []rune(alias)
	if len(aliasRunes) > width {
		aliasRunes = aliasRunes[:width]
	}
	if len(aliasRunes) < width {
		padded := make([]rune, width)
		fillFrom := width - len(aliasRunes)
		for i := 0; i < fillFrom; i++ {
			padded[i] = rune(pad)
		}
		copy(padded[fillFrom:], aliasRunes)
		aliasRunes = padded
	}
	var out strings.Builder
	out.WriteString(string(span[:start]))
	out.WriteString(string(aliasRunes))
	out.WriteString(string(span[len(span)-end:]))
	return out.String()
}

func indexOfRunes(haystack, needle []rune, from int) int {
	for i := from; i+len(needle) <= len(haystack); i++ {
		if hasPrefixAt(haystack, needle, i) {
			return i
		}
	}
	return -1
}

func lastIndexOfRunes(haystack, needle []rune, upto int) int {
	for i := upto - len(needle); i >= 0; i-- {
		if hasPrefixAt(haystack, needle, i) {
			return i
		}
	}
	return -1
}

// sortedComments is the reference's own comment table, LONGEST marker
// first, so a two-character opener is matched before a one-character one
// that happens to be its prefix.
func sortedComments(cfg *Config) [][2]string {
	out := make([][2]string, 0, len(cfg.Comments))
	for start, end := range cfg.Comments {
		out = append(out, [2]string{start, end})
	}
	sort.Slice(out, func(i, j int) bool {
		return len([]rune(out[i][0])) > len([]rune(out[j][0]))
	})
	return out
}

// sortedQuotes merges the reference's own three delimiter tables -- string
// quotes, format-string prefixes (a heredoc's `$tag$`), and identifier
// quotes -- into the one list `_fit` tries each of, longest OPEN and then
// longest CLOSE marker first.
func sortedQuotes(cfg *Config) [][2]string {
	merged := map[string]string{}
	for open, close := range cfg.Quotes {
		merged[open] = close
	}
	for open, fs := range cfg.FormatStrings {
		merged[open] = fs.End
	}
	for open, close := range cfg.Identifiers {
		merged[open] = close
	}
	out := make([][2]string, 0, len(merged))
	for open, close := range merged {
		out = append(out, [2]string{open, close})
	}
	sort.Slice(out, func(i, j int) bool {
		oi, oj := []rune(out[i][0]), []rune(out[j][0])
		if len(oi) != len(oj) {
			return len(oi) > len(oj)
		}
		ci, cj := []rune(out[i][1]), []rune(out[j][1])
		return len(ci) > len(cj)
	})
	return out
}

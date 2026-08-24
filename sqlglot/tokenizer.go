package sqlglot

import (
	"fmt"
	"math/big"
	"strings"
	"unicode"
)

// Token is one lexical token. The fields, and their meanings, are the
// reference's: Start and End are inclusive rune offsets into the statement,
// Line and Col are 1-based, and Comments holds the comments attached to this
// token -- leading comments attach to the token that follows, trailing ones to
// the token that precedes.
type Token struct {
	Type     TokenType
	Text     string
	Line     int
	Col      int
	Start    int
	End      int
	Comments []string
}

func (t Token) String() string {
	return fmt.Sprintf("<Token token_type: %s, text: %s, line: %d, col: %d, start: %d, end: %d>",
		t.Type, t.Text, t.Line, t.Col, t.Start, t.End)
}

// TokenError reports a statement the tokenizer cannot lex -- an unterminated
// string, most often. It carries the surrounding text, as the reference does,
// because the offset alone is rarely enough to see what went wrong.
type TokenError struct {
	Msg   string
	Start int
	End   int
}

func (e *TokenError) Error() string { return e.Msg }

// Tokenizer turns SQL into tokens for one dialect. It is a direct port of the
// reference's TokenizerCore: same scan order, same lookahead, same positions.
// It is not safe for concurrent use; make one per goroutine, they are cheap.
type Tokenizer struct {
	cfg  *Config
	sql  []rune
	size int

	tokens        []Token
	start         int
	current       int
	line          int
	col           int
	comments      []string
	char          rune
	charSet       bool
	end           bool
	peek          rune
	peekSet       bool
	prevTokenLine int
}

// NewTokenizer returns a tokenizer for a dialect. The empty name is the
// dialect-neutral default.
func NewTokenizer(dialect string) (*Tokenizer, error) {
	cfg, ok := ConfigFor(dialect)
	if !ok {
		return nil, fmt.Errorf("sqlglot: no tokenizer for dialect %q (have %v)", dialect, Dialects())
	}
	t := &Tokenizer{cfg: cfg}
	t.reset()
	return t, nil
}

// Config exposes the dialect tables this tokenizer reads.
func (t *Tokenizer) Config() *Config { return t.cfg }

func (t *Tokenizer) reset() {
	t.sql = nil
	t.size = 0
	t.tokens = nil
	t.start = 0
	t.current = 0
	t.line = 1
	t.col = 0
	t.comments = nil
	t.char, t.charSet = 0, false
	t.peek, t.peekSet = 0, false
	t.end = false
	t.prevTokenLine = -1
}

// Tokenize returns the tokens of one SQL string.
func (t *Tokenizer) Tokenize(sql string) (toks []Token, err error) {
	t.reset()
	t.sql = []rune(sql)
	t.size = len(t.sql)

	defer func() {
		if r := recover(); r != nil {
			start := max(t.current-50, 0)
			end := max(min(t.current+50, t.size-1), start)
			err = &TokenError{
				Msg:   fmt.Sprintf("Error tokenizing '%s': %v", string(t.sql[start:end]), r),
				Start: start,
				End:   end,
			}
			toks = nil
		}
	}()

	t.scan(false)
	return t.tokens, nil
}

// Tokenize is the one-shot form: tokenize a statement in a dialect.
func Tokenize(sql, dialect string) ([]Token, error) {
	t, err := NewTokenizer(dialect)
	if err != nil {
		return nil, err
	}
	return t.Tokenize(sql)
}

func (t *Tokenizer) scan(checkSemicolon bool) {
	for t.size > 0 && !t.end {
		current := t.current

		// Skip spaces in a loop rather than through advance(), as the
		// reference does -- the positions it produces depend on it.
		for current < t.size {
			c := t.sql[current]
			if c == ' ' || c == '\t' {
				current++
			} else {
				break
			}
		}

		offset := 1
		if current > t.current {
			offset = current - t.current
		}

		t.start = current
		t.advance(offset, false)

		if !t.charIsSpace() {
			switch {
			case isDigitChar(t.char, t.charSet):
				t.scanNumber()
			default:
				if end, ok := t.cfg.Identifiers[t.charStr()]; ok {
					t.scanIdentifier(end)
				} else {
					t.scanKeywords()
				}
			}
		}

		if checkSemicolon && t.peekSet && t.peek == ';' {
			break
		}
	}

	if len(t.tokens) > 0 && len(t.comments) > 0 {
		t.appendComments(t.comments)
	}
}

func (t *Tokenizer) appendComments(c []string) {
	i := len(t.tokens) - 1
	t.tokens[i].Comments = append(t.tokens[i].Comments, c...)
}

func (t *Tokenizer) charStr() string {
	if !t.charSet {
		return ""
	}
	return string(t.char)
}

func (t *Tokenizer) peekStr() string {
	if !t.peekSet {
		return ""
	}
	return string(t.peek)
}

func (t *Tokenizer) charIsSpace() bool { return t.charSet && isPythonSpace(t.char) }

// at emulates Python's sequence indexing, negative wrap included: the
// reference relies on it when it backs up over a numeric literal's suffix.
func (t *Tokenizer) at(i int) (rune, bool) {
	if i < 0 {
		i += t.size
	}
	if i < 0 || i >= t.size {
		panic(fmt.Sprintf("index %d out of range for statement of %d characters", i, t.size))
	}
	return t.sql[i], true
}

func (t *Tokenizer) advance(i int, alnum bool) {
	c := t.char
	if t.charSet && (c == '\n' || c == '\r') {
		//nolint:staticcheck // QF1001: kept in the reference's shape, so the port reads against it
		if !(c == '\r' && t.peekSet && t.peek == '\n') {
			t.col = i
			t.line++
		}
	} else {
		t.col += i
	}

	t.current += i
	t.end = t.current >= t.size
	t.char, t.charSet = t.at(t.current - 1)
	if t.end {
		t.peek, t.peekSet = 0, false
	} else {
		t.peek, t.peekSet = t.sql[t.current], true
	}

	if alnum && t.charSet && isAlnum(t.char) {
		col, cur, e := t.col, t.current, t.end
		pk, pkSet := t.peek, t.peekSet
		for pkSet && isAlnum(pk) {
			col++
			cur++
			e = cur >= t.size
			if e {
				pk, pkSet = 0, false
			} else {
				pk, pkSet = t.sql[cur], true
			}
		}
		t.col, t.current, t.end = col, cur, e
		t.peek, t.peekSet = pk, pkSet
		t.char, t.charSet = t.at(cur - 1)
	}
}

func (t *Tokenizer) chars(size int) string {
	if size == 1 {
		return t.charStr()
	}
	start := t.current - 1
	end := start + size
	if end <= t.size {
		return string(t.sql[pyBound(start, t.size):pyBound(end, t.size)])
	}
	return ""
}

func (t *Tokenizer) text() string { return string(t.sql[t.start:t.current]) }

func (t *Tokenizer) add(tt TokenType, text string, hasText bool) {
	t.prevTokenLine = t.line

	if len(t.comments) > 0 && tt == TokSEMICOLON && len(t.tokens) > 0 {
		t.appendComments(t.comments)
		t.comments = nil
	}

	if !hasText {
		text = string(t.sql[t.start:t.current])
	}

	t.tokens = append(t.tokens, Token{
		Type:     tt,
		Text:     text,
		Line:     t.line,
		Col:      t.col,
		Start:    t.start,
		End:      t.current - 1,
		Comments: t.comments,
	})
	t.comments = nil

	// A command's payload is taken verbatim as a string, but only where a
	// command can start: at the very beginning, or after a ; or BEGIN.
	if _, isCommand := t.cfg.Commands[tt]; !isCommand {
		return
	}
	if t.peekSet && t.peek == ';' {
		return
	}
	atStart := len(t.tokens) == 1
	if !atStart {
		if _, ok := t.cfg.CommandPrefixTokens[t.tokens[len(t.tokens)-2].Type]; !ok {
			return
		}
	}
	start := t.current
	n := len(t.tokens)
	t.scan(true)
	t.tokens = t.tokens[:n]
	payload := trimPythonSpace(string(t.sql[start:t.current]))
	if payload != "" {
		t.add(TokSTRING, payload, true)
	}
}

func (t *Tokenizer) scanKeywords() {
	sql := t.sql
	sqlSize := t.size
	size := 0
	word, hasWord := "", false
	chars := t.charStr()
	char, charEmpty := t.char, !t.charSet
	prevSpace := false
	skip := false
	trie := t.cfg.trie
	_, singleToken := t.cfg.SingleTokens[chars]

	for chars != "" {
		if !skip {
			sub := trie.get(upperASCII(char))
			if sub == nil {
				break
			}
			trie = sub
			if trie.word {
				word, hasWord = chars, true
			}
		}

		end := t.current + size
		size++

		if end < sqlSize {
			char, charEmpty = sql[end], false
			if _, ok := t.cfg.SingleTokens[string(char)]; ok {
				singleToken = true
			}
			isSp := isPythonSpace(char)
			if !isSp || !prevSpace {
				if isSp {
					char = ' '
				}
				chars += string(char)
				prevSpace = isSp
				skip = false
			} else {
				skip = true
			}
		} else {
			charEmpty = true
			break
		}
	}

	if hasWord {
		if t.scanString(word) {
			return
		}
		if t.scanComment(word) {
			return
		}
		if prevSpace || singleToken || charEmpty {
			t.advance(size-1, false)
			word = strings.ToUpper(word)
			t.add(t.cfg.Keywords[word], word, true)
			return
		}
	}

	if tt, ok := t.cfg.SingleTokens[t.charStr()]; ok {
		t.add(tt, t.charStr(), true)
		return
	}

	t.scanVar()
}

func (t *Tokenizer) scanComment(commentStart string) bool {
	commentEnd, ok := t.cfg.Comments[commentStart]
	if !ok {
		return false
	}

	commentStartLine := t.line
	commentStartSize := len([]rune(commentStart))

	if commentEnd != "" {
		t.advance(commentStartSize, false)

		commentCount := 1
		commentEndSize := len([]rune(commentEnd))

		for !t.end {
			if t.chars(commentEndSize) == commentEnd {
				commentCount--
				if commentCount == 0 {
					break
				}
			}

			t.advance(1, true)

			// Some dialects nest comments -- databricks, duckdb, postgres.
			if t.cfg.NestedComments && !t.end && t.chars(commentEndSize) == commentStart {
				t.advance(commentStartSize, false)
				commentCount++
			}
		}

		body := []rune(t.text())
		t.comments = append(t.comments, string(body[pyBound(commentStartSize, len(body)):pySliceEnd(-commentEndSize+1, len(body))]))
		t.advance(commentEndSize-1, false)
	} else {
		for !t.end && t.peek != '\n' && t.peek != '\r' {
			t.advance(1, true)
		}
		body := []rune(t.text())
		t.comments = append(t.comments, string(body[pyBound(commentStartSize, len(body)):]))
	}

	if commentStart == t.cfg.HintStart && len(t.tokens) > 0 {
		if _, ok := t.cfg.TokensPrecedingHint[t.tokens[len(t.tokens)-1].Type]; ok {
			t.add(TokHINT, "", false)
		}
	}

	// A comment on the same line as the previous token belongs to it.
	if commentStartLine == t.prevTokenLine {
		t.appendComments(t.comments)
		t.comments = nil
		t.prevTokenLine = t.line
	}

	return true
}

func (t *Tokenizer) scanNumber() {
	if t.char == '0' {
		switch upperASCII(t.peek) {
		case 'B':
			if t.cfg.HasBitStrings {
				t.scanBits()
			} else {
				t.add(TokNUMBER, "", false)
			}
			return
		case 'X':
			if t.cfg.HasHexStrings {
				t.scanHex()
			} else {
				t.add(TokNUMBER, "", false)
			}
			return
		}
	}

	decimal := false
	scientific := 0

	isUnderscoreSeparated := false
	numberText := ""
	numericLiteral := ""
	var numericType TokenType
	hasNumericType := false

	for {
		switch {
		case isDigitChar(t.peek, t.peekSet):
			// Consume a run of digits in one advance, as the reference does.
			end := t.current + 1
			for end < t.size && isDigitChar(t.sql[end], true) {
				end++
			}
			t.advance(end-t.current, false)
		case t.peekSet && t.peek == '.' && !decimal:
			if (len(t.tokens) > 0 && t.tokens[len(t.tokens)-1].Type == TokPARAMETER) || !t.cfg.NumbersCanHaveDecimals {
				goto done
			}
			decimal = true
			t.advance(1, false)
		case t.peekSet && (t.peek == '-' || t.peek == '+') && scientific == 1:
			if t.current+1 < t.size && isDigitChar(t.sql[t.current+1], true) {
				scientific++
				t.advance(1, false)
			} else {
				goto done
			}
		case upperASCII(t.peek) == 'E' && t.peekSet && scientific == 0:
			scientific++
			t.advance(1, false)
		case t.peekSet && t.peek == '_' && t.cfg.NumbersCanBeUnderscoreSeparated:
			isUnderscoreSeparated = true
			t.advance(1, false)
		case t.peekSet && isIdentifierChar(t.peek):
			numberText = t.text()

			for t.peekSet && !isPythonSpace(t.peek) {
				if _, ok := t.cfg.SingleTokens[string(t.peek)]; ok {
					break
				}
				numericLiteral += string(t.peek)
				t.advance(1, false)
			}

			if mapped, ok := t.cfg.NumericLiterals[strings.ToUpper(numericLiteral)]; ok {
				numericType, hasNumericType = t.cfg.Keywords[mapped]
			}

			if hasNumericType {
				goto done
			} else if t.cfg.IdentifiersCanStartWithDigit {
				t.add(TokVAR, "", false)
				return
			}

			t.advance(-len([]rune(numericLiteral)), false)
			goto done
		default:
			goto done
		}
	}

done:
	if numberText == "" {
		numberText = string(t.sql[t.start:t.current])
	}

	// 100_000 is the number 100000.
	if isUnderscoreSeparated {
		numberText = strings.ReplaceAll(numberText, "_", "")
	}

	t.add(TokNUMBER, numberText, true)

	// 123L is 123::BIGINT, so that it parses as a cast.
	if hasNumericType {
		t.add(TokDCOLON, "::", true)
		t.add(numericType, numericLiteral, true)
	}
}

func (t *Tokenizer) scanBits() {
	t.advance(1, false)
	value := t.extractValue()
	if _, ok := parseIntLiteral(value, 2); ok {
		t.add(TokBIT_STRING, trimPrefixRunes(value, 2), true)
	} else {
		t.add(TokIDENTIFIER, "", false)
	}
}

func (t *Tokenizer) scanHex() {
	t.advance(1, false)
	value := t.extractValue()
	if _, ok := parseIntLiteral(value, 16); ok {
		t.add(TokHEX_STRING, trimPrefixRunes(value, 2), true)
	} else {
		t.add(TokIDENTIFIER, "", false)
	}
}

func (t *Tokenizer) extractValue() string {
	for {
		c := trimPythonSpace(t.peekStr())
		if c == "" {
			break
		}
		if _, ok := t.cfg.SingleTokens[c]; ok {
			break
		}
		t.advance(1, true)
	}
	return t.text()
}

func (t *Tokenizer) scanString(start string) bool {
	base := 0
	tokenType := TokSTRING

	end, isQuote := t.cfg.Quotes[start]
	if !isQuote {
		fs, isFormat := t.cfg.FormatStrings[start]
		if !isFormat {
			return false
		}
		end, tokenType = fs.End, fs.Type

		switch tokenType {
		case TokHEX_STRING:
			base = 16
		case TokBIT_STRING:
			base = 2
		case TokHEREDOC_STRING:
			t.advance(1, false)

			tag := ""
			if t.charStr() != end {
				tag = t.extractString(end, nil, true, !t.cfg.HeredocTagIsIdentifier)
			}

			if tag != "" && t.cfg.HeredocTagIsIdentifier && (t.end || allDigits(tag) || hasSpace(tag)) {
				if !t.end {
					t.advance(-1, false)
				}
				t.advance(-len([]rune(tag)), false)
				t.add(t.cfg.HeredocStringAlternative, "", false)
				return true
			}

			end = start + tag + end
		}
	}

	t.advance(len([]rune(start)), false)

	escapes := t.cfg.StringEscapes
	if tokenType == TokBYTE_STRING {
		escapes = t.cfg.ByteStringEscapes
	}
	text := t.extractString(end, escapes, tokenType == TokRAW_STRING, true)

	if base != 0 && text != "" {
		if _, ok := parseIntLiteral(text, base); !ok {
			panic(fmt.Sprintf("Numeric string contains invalid characters from %d:%d", t.line, t.start))
		}
	}

	t.add(tokenType, text, true)
	return true
}

func (t *Tokenizer) scanIdentifier(identifierEnd string) {
	t.advance(1, false)
	escapes := make(set, len(t.cfg.IdentifierEscapes)+1)
	for k := range t.cfg.IdentifierEscapes {
		escapes[k] = struct{}{}
	}
	escapes[identifierEnd] = struct{}{}
	text := t.extractString(identifierEnd, escapes, false, true)
	t.add(TokIDENTIFIER, text, true)
}

func (t *Tokenizer) scanVar() {
	for t.peekSet && !isPythonSpace(t.peek) {
		p := string(t.peek)
		if _, isVarSingle := t.cfg.VarSingleTokens[p]; !isVarSingle {
			if _, isSingle := t.cfg.SingleTokens[p]; isSingle {
				break
			}
		}
		t.advance(1, true)
	}

	if len(t.tokens) > 0 && t.tokens[len(t.tokens)-1].Type == TokPARAMETER {
		t.add(TokVAR, "", false)
		return
	}
	tt, ok := t.cfg.Keywords[strings.ToUpper(string(t.sql[t.start:t.current]))]
	if !ok {
		tt = TokVAR
	}
	t.add(tt, "", false)
}

func (t *Tokenizer) extractString(delimiter string, escapes set, rawString, raiseUnmatched bool) string {
	text := ""
	delimRunes := []rune(delimiter)
	delimSize := len(delimRunes)
	if escapes == nil {
		escapes = t.cfg.StringEscapes
	}
	unescaped := t.cfg.UnescapedSequences
	sql := t.sql

	// Fast path: a one-character delimiter with nothing to unescape in between.
	if delimSize == 1 {
		pos := t.current - 1
		end := indexRuneFrom(sql, delimRunes[0], pos)

		_, delimIsEscape := escapes[delimiter]
		_, backslashIsEscape := escapes["\\"]

		if end != -1 &&
			(end+1 >= t.size || sql[end+1] != delimRunes[0] || !delimIsEscape) &&
			//nolint:staticcheck // QF1001: kept in the reference's shape, so the port reads against it
			(!(len(unescaped) > 0 || backslashIsEscape) || indexRuneRange(sql, '\\', pos, end) == -1) {
			if newlines := countRuneRange(sql, '\n', pos, end); newlines > 0 {
				t.line += newlines
				t.col = end - lastIndexRuneRange(sql, '\n', pos, end)
			} else {
				t.col += end - pos
			}

			t.current = end + 1
			t.end = t.current >= t.size
			t.char, t.charSet = sql[end], true
			if t.end {
				t.peek, t.peekSet = 0, false
			} else {
				t.peek, t.peekSet = sql[t.current], true
			}
			return string(sql[pos:end])
		}
	}

	for {
		if !rawString && len(unescaped) > 0 && t.peekSet {
			if _, ok := escapes[t.charStr()]; ok {
				if seq, ok := unescaped[t.charStr()+t.peekStr()]; ok {
					t.advance(2, false)
					text += seq
					continue
				}
			}
		}

		_, charIsEscape := escapes[t.charStr()]
		isValidCustomEscape := len(t.cfg.EscapeFollowChars) > 0 && t.charSet && t.char == '\\'
		if isValidCustomEscape {
			if _, ok := t.cfg.EscapeFollowChars[t.peekStr()]; ok {
				isValidCustomEscape = false
			}
		}

		// An escaped quote just before the closing delimiter has to be eaten
		// here, or the delimiter check below would end the string early. Only
		// multi-character delimiters made of quote characters are affected.
		_, peekIsQuote := t.cfg.Quotes[t.peekStr()]
		escapedDelimiter := t.peekStr() == delimiter ||
			(delimSize > 1 && t.peekSet && t.peek == delimRunes[0] && peekIsQuote)

		_, peekIsEscape := escapes[t.peekStr()]
		_, charIsQuote := t.cfg.Quotes[t.charStr()]

		if (t.cfg.StringEscapesAllowedInRawStrings || !rawString) &&
			charIsEscape &&
			(escapedDelimiter || peekIsEscape || isValidCustomEscape) &&
			(!charIsQuote || (t.charSet && t.peekSet && t.char == t.peek)) {
			switch {
			case escapedDelimiter:
				if rawString {
					text += t.charStr() + t.peekStr()
				} else {
					text += t.peekStr()
				}
			//nolint:staticcheck // QF1001: kept in the reference's shape, so the port reads against it
			case isValidCustomEscape && !(t.charSet && t.peekSet && t.char == t.peek):
				text += t.peekStr()
			default:
				text += t.charStr() + t.peekStr()
			}

			if t.current+1 < t.size {
				t.advance(2, false)
			} else {
				panic(fmt.Sprintf("Missing %s from %d:%d", delimiter, t.line, t.current))
			}
		} else {
			if t.chars(delimSize) == delimiter {
				if delimSize > 1 {
					t.advance(delimSize-1, false)
				}
				break
			}

			if t.end {
				if !raiseUnmatched {
					return text + t.charStr()
				}
				panic(fmt.Sprintf("Missing %s from %d:%d", delimiter, t.line, t.start))
			}

			current := t.current - 1
			t.advance(1, true)
			text += string(sql[current : t.current-1])
		}
	}

	return text
}

// --- Python semantics the port depends on -----------------------------------

// upperASCII is the reference's _CHAR_UPPER: ASCII lowercase only, deliberately
// not a Unicode fold, so a non-ASCII character reaches the trie unchanged.
func upperASCII(r rune) rune {
	if r >= 'a' && r <= 'z' {
		return r - 32
	}
	return r
}

func isDigitChar(r rune, set bool) bool { return set && r >= '0' && r <= '9' }

// isAlnum is Python's str.isalnum for one character.
func isAlnum(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsNumber(r)
}

// isIdentifierChar is Python's str.isidentifier for one character.
func isIdentifierChar(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.In(r, unicode.Nl, unicode.Mn, unicode.Mc, unicode.Nd, unicode.Pc)
}

// allDigits is Python's `str.isdigit()`, which the reference calls on a
// heredoc tag to decide whether it is a tag at all.
//
// Go's unicode.IsDigit is category Nd -- decimal digits -- and Python's is
// wider: it also covers the characters Unicode gives a Digit numeric type,
// which are the superscript and subscript forms. `$¹$` is therefore NOT a
// heredoc tag to the reference, and reading it as one left the port
// hunting for a closing `$¹$` that was never there. The generator fuzzer
// found it.
//
// The wider set is not exposed by Go's unicode package, so the ranges are
// listed. It does NOT include the other numerics -- Python calls `½`
// numeric but not a digit -- which is why unicode.IsNumber is wrong here.
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsDigit(r) && !isDigitTyped(r) {
			return false
		}
	}
	return true
}

// isDigitTyped covers the characters Python calls digits and Go's Nd does
// not. The set is generated; see harness/gen_digits.py.
func isDigitTyped(r rune) bool {
	for _, run := range digitTypedRanges {
		if r >= run[0] && r <= run[1] {
			return true
		}
	}
	return false
}

func hasSpace(s string) bool { return strings.IndexFunc(s, isPythonSpace) >= 0 }

// pyBound clamps an index the way a Python slice bound does.
func pyBound(i, n int) int {
	if i < 0 {
		i += n
		if i < 0 {
			return 0
		}
	}
	if i > n {
		return n
	}
	return i
}

// pySliceEnd resolves a slice stop, where a negative stop counts from the end
// and a zero stop means zero -- the distinction the reference's comment slicing
// depends on.
func pySliceEnd(stop, n int) int {
	if stop < 0 {
		stop += n
		if stop < 0 {
			return 0
		}
	}
	if stop > n {
		return n
	}
	return stop
}

// parseIntLiteral is Python's int(text, base): an optional base prefix and
// underscores are allowed, and the value is unbounded.
func parseIntLiteral(text string, base int) (*big.Int, bool) {
	s := strings.ReplaceAll(text, "_", "")
	prefix := map[int]string{2: "b", 16: "x", 8: "o"}[base]
	if prefix != "" && len(s) > 2 && s[0] == '0' && (s[1] == prefix[0] || s[1] == prefix[0]-32) {
		s = s[2:]
	}
	if s == "" {
		return nil, false
	}
	n, ok := new(big.Int).SetString(s, base)
	return n, ok
}

func trimPrefixRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return ""
	}
	return string(r[n:])
}

func indexRuneFrom(s []rune, r rune, from int) int {
	if from < 0 {
		from = 0
	}
	for i := from; i < len(s); i++ {
		if s[i] == r {
			return i
		}
	}
	return -1
}

func indexRuneRange(s []rune, r rune, from, to int) int {
	if from < 0 {
		from = 0
	}
	if to > len(s) {
		to = len(s)
	}
	for i := from; i < to; i++ {
		if s[i] == r {
			return i
		}
	}
	return -1
}

func lastIndexRuneRange(s []rune, r rune, from, to int) int {
	if from < 0 {
		from = 0
	}
	if to > len(s) {
		to = len(s)
	}
	for i := to - 1; i >= from; i-- {
		if s[i] == r {
			return i
		}
	}
	return -1
}

func countRuneRange(s []rune, r rune, from, to int) int {
	if from < 0 {
		from = 0
	}
	if to > len(s) {
		to = len(s)
	}
	n := 0
	for i := from; i < to; i++ {
		if s[i] == r {
			n++
		}
	}
	return n
}

// isPythonSpace is Python's `str.isspace()`, which the reference's tokenizer
// splits words on and which Go's unicode.IsSpace is NOT.
//
// The two agree everywhere except the four ASCII separators 0x1C-0x1F -- file,
// group, record and unit separator -- which Python calls whitespace and Go does
// not. So `a\x1db` lexed as two tokens in the reference and one in the port,
// and every statement containing one parsed into a different tree.
//
// The same class of bug as reading offsets in bytes where the reference counts
// runes: a table can be generated, but the LANGUAGE's own semantics have to be
// ported by hand, and they are invisible until something exercises them. The
// fuzzed differential did.
func isPythonSpace(r rune) bool {
	if r >= 0x1C && r <= 0x1F {
		return true
	}
	return unicode.IsSpace(r)
}

// trimPythonSpace is Python's `str.strip()`, which the reference calls to
// decide where a value ends. strings.TrimSpace is Go's definition and stops
// four characters short -- see isPythonSpace. `0X<RS>` kept the separator
// inside the token here and dropped it there.
func trimPythonSpace(s string) string {
	return strings.TrimFunc(s, isPythonSpace)
}

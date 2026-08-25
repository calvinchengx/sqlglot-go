package sqlglot

import (
	"errors"
	"fmt"
	"strings"
)

// ErrUnsupported reports a statement the port cannot parse yet.
//
// It is deliberately distinct from a syntax error. The guard above this parser
// turns it into a refusal, and the differential harness counts it as a gap in
// coverage -- never as a match. A construct the port does not understand must
// never become a tree that merely looks plausible.
var ErrUnsupported = errors.New("sqlglot-go: unsupported statement")

// ErrNotAQuery reports a statement the port recognises but does not parse:
// DDL and DML, which a read-only guard refuses on sight.
//
// It is deliberately distinct from ErrUnsupported. The guard above this parser
// has to refuse `DROP TABLE t` for being a write, not for being unreadable --
// the conformance suite checks the reason -- and naming the statement is all
// that takes. Building the tree would be work with no consumer: nothing here
// will ever execute one.
var ErrNotAQuery = errors.New("sqlglot-go: not a query")

// NotAQueryError names the statement. The guard above reports "DROP is not
// allowed; this endpoint is read-only", so it needs the word, not just the
// fact -- and reading it back out of a message would be a parser of its own.
type NotAQueryError struct{ Kind string }

func (e *NotAQueryError) Error() string { return ErrNotAQuery.Error() + ": " + e.Kind }

// Is makes errors.Is(err, ErrNotAQuery) hold for this error too.
func (e *NotAQueryError) Is(target error) bool { return target == ErrNotAQuery }

// ErrMultipleStatements reports more than one statement in the input. A guard
// that permitted the first and ignored the rest would be no guard at all.
var ErrMultipleStatements = errors.New("sqlglot-go: more than one statement")

// ParseOne parses a single statement in a dialect and returns its tree.
func ParseOne(sql, dialect string) (*Expression, error) {
	tk, err := NewTokenizer(dialect)
	if err != nil {
		return nil, err
	}
	toks, err := tk.Tokenize(sql)
	if err != nil {
		return nil, err
	}
	cfg := tk.Config()
	p := &parser{tokens: toks, cfg: cfg, tables: cfg.Tables, dialect: dialect}
	return p.parseOne()
}

// parser walks a token stream. The cursor mirrors the reference's: index
// addresses the current token, with prev and next either side, and every rule
// is written against curr/prev the way the reference's rules are.
type parser struct {
	// inCallArgs is set while reading a call's own argument list, where a
	// trailing IGNORE NULLS belongs to the CALL rather than to the argument.
	inCallArgs bool
	// inFromSubquery marks a query being parsed as a parenthesised FROM item,
	// which is the one path on which the reference stamps `unpivot` false.
	inFromSubquery bool
	tokens         []Token
	index          int
	cfg            *Config
	tables         *ParserTables
	dialect        string
}

func (p *parser) curr() *Token {
	if p.index < len(p.tokens) {
		return &p.tokens[p.index]
	}
	return nil
}

// next is the token after the current one.
func (p *parser) next() *Token {
	if i := p.index + 1; i < len(p.tokens) {
		return &p.tokens[i]
	}
	return nil
}

func (p *parser) advance() { p.index++ }

// peekAt is next() for a further offset, for the handful of rules that need
// to see more than one token ahead before committing.
func (p *parser) peekAt(n int) *Token {
	if i := p.index + n; i < len(p.tokens) {
		return &p.tokens[i]
	}
	return nil
}

func (p *parser) at(tt TokenType) bool {
	c := p.curr()
	return c != nil && c.Type == tt
}

func (p *parser) atAny(tts ...TokenType) bool {
	c := p.curr()
	if c == nil {
		return false
	}
	for _, tt := range tts {
		if c.Type == tt {
			return true
		}
	}
	return false
}

// match consumes the current token when it has this type.
// atPair reports whether the current and next tokens have these types.
func (p *parser) atPair(a, b TokenType) bool {
	n := p.next()
	return p.at(a) && n != nil && n.Type == b
}

func (p *parser) match(tt TokenType) bool {
	if p.at(tt) {
		p.advance()
		return true
	}
	return false
}

// UnsupportedError names the construct the port could not read.
//
// The construct is the field worth recording. A consumer that wants to know
// what to build next needs "PIVOT" or "window function", and reading that back
// out of a formatted message would be a parser of its own -- the same reason
// NotAQueryError carries Kind.
//
// Construct is SQL VOCABULARY and safe to log: the seven call sites that
// interpolate do so with a function name, a type, a join method or a clause
// keyword, never with a caller's identifier. Token is the opposite -- it is
// whatever the statement said at the point of failure, which can be a table
// or column name -- so it is available to a caller that has a reason for it
// and is deliberately not part of what this package encourages logging.
type UnsupportedError struct {
	Construct string
	Token     string
	// TokenIsKeyword reports whether Token is dialect vocabulary rather than
	// something the caller wrote. Only a keyword is safe to record.
	TokenIsKeyword bool
}

// Label is the construct, named as precisely as can be done SAFELY -- the
// string to count when deciding what to build next.
//
// Construct alone is not always enough. A window function and a PIVOT both
// stop the parser at "trailing tokens", the second largest refusal bucket in
// the fixture corpus, and the two need entirely different work. What separates
// them is the token: OVER, PIVOT. Those are keywords -- dialect vocabulary,
// fixed and public -- so appending one narrows the label without recording
// anything the caller wrote. When the token is an identifier, a string or a
// number, it is omitted, because a refusal on `WHERE email = '...'` must not
// put that anywhere.
func (e *UnsupportedError) Label() string {
	if e.TokenIsKeyword && !strings.Contains(e.Construct, e.Token) {
		return e.Construct + " at " + strings.ToUpper(e.Token)
	}
	return e.Construct
}

func (e *UnsupportedError) Error() string {
	return fmt.Sprintf("%s: %s at %q", ErrUnsupported.Error(), e.Construct, e.Token)
}

// Is makes errors.Is(err, ErrUnsupported) hold for this error too.
func (e *UnsupportedError) Is(target error) bool { return target == ErrUnsupported }

func (p *parser) unsupported(what string) error {
	c := p.curr()
	text := "end of statement"
	keyword := false
	if c != nil {
		text = c.Text
		// Everything that is not a name, a string or a number came out of the
		// dialect's own keyword trie.
		keyword = c.Type != TokIDENTIFIER && c.Type != TokVAR &&
			c.Type != TokSTRING && c.Type != TokNUMBER
	}
	return &UnsupportedError{Construct: what, Token: text, TokenIsKeyword: keyword}
}

// parseOne parses exactly one statement and insists the whole token stream was
// consumed. Leftover tokens mean the port understood less than it thought, so
// the result is refused rather than returned.
func (p *parser) parseOne() (*Expression, error) {
	this, err := p.parseStatement()
	if err != nil {
		return nil, err
	}
	if p.match(TokSEMICOLON) && p.curr() != nil {
		return nil, fmt.Errorf("%w: %s", ErrMultipleStatements, p.curr().Text)
	}
	if p.curr() != nil {
		return nil, p.unsupported("trailing tokens")
	}
	return this, nil
}

// parseStatement returns a tree or an error, never (nil, nil).
//
// Anything that is not a query and does not begin a statement of its own is a
// bare expression, as in the reference -- most of sqlglot's own fixture corpus
// is expressions rather than whole statements.
func (p *parser) parseStatement() (*Expression, error) {
	if p.at(TokSELECT) || p.at(TokWITH) || p.at(TokPIVOT) || p.at(TokUNPIVOT) {
		return p.parseQuery()
	}
	if p.at(TokCREATE) {
		return p.parseCreate()
	}
	if c := p.curr(); c != nil {
		if _, isStatement := p.tables.StatementTokens[c.Type]; isStatement {
			return nil, &NotAQueryError{Kind: strings.ToUpper(c.Text)}
		}
	}
	e, err := p.parseExpression()
	if err != nil {
		return nil, err
	}
	return p.parseAlias(e)
}

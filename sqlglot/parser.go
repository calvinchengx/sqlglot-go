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
	tokens  []Token
	index   int
	cfg     *Config
	tables  *ParserTables
	dialect string
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

func (p *parser) unsupported(what string) error {
	c := p.curr()
	text := "end of statement"
	if c != nil {
		text = c.Text
	}
	return fmt.Errorf("%w: %s at %q", ErrUnsupported, what, text)
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
	if p.at(TokSELECT) || p.at(TokWITH) {
		return p.parseQuery()
	}
	if c := p.curr(); c != nil {
		if _, isStatement := p.tables.StatementTokens[c.Type]; isStatement {
			return nil, fmt.Errorf("%w: %s", ErrNotAQuery, strings.ToUpper(c.Text))
		}
	}
	e, err := p.parseExpression()
	if err != nil {
		return nil, err
	}
	return p.parseAlias(e)
}

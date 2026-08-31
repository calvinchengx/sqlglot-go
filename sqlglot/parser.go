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
	// inColumnType marks the type of a COLUMN being read, where a fixed-size
	// array is a type in every dialect -- outside one, only the dialects that
	// have them read `INT[3]` that way.
	inColumnType bool
	dialect      string
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
	if p.at(TokWITH) {
		// A WITH may stand in front of a statement that is not a query at
		// all: `WITH a AS (SELECT * FROM b) UPDATE a SET c = 1` reads from b
		// and writes to a. The clause is parsed first and assigned LAST,
		// which is where the reference puts it however early it was written.
		with, err := p.parseWith()
		if err != nil {
			return nil, err
		}
		this, err := p.parseStatementBody()
		if err != nil {
			return nil, err
		}
		if with != nil {
			this.Set("with_", with)
		}
		return this, nil
	}
	if p.at(TokSELECT) || p.at(TokPIVOT) || p.at(TokUNPIVOT) || p.at(TokFROM) ||
		p.at(TokSUMMARIZE) {
		return p.parseQuery()
	}
	return p.parseStatementBody()
}

// parseStatementBody reads a statement once any WITH clause in front of it has
// been taken off.
func (p *parser) parseStatementBody() (*Expression, error) {
	if p.at(TokCREATE) {
		return p.parseCreate()
	}
	if p.at(TokINSERT) {
		return p.parseInsert()
	}
	if p.at(TokDROP) {
		return p.parseDrop()
	}
	if p.at(TokALTER) {
		return p.parseAlter()
	}
	if p.at(TokUPDATE) {
		return p.parseUpdate()
	}
	if p.at(TokDELETE) {
		return p.parseDelete()
	}
	if p.at(TokMERGE) {
		return p.parseMerge()
	}
	if p.at(TokTRUNCATE) {
		return p.parseTruncate()
	}
	if p.at(TokGRANT) || p.at(TokREVOKE) {
		return p.parseGrant()
	}
	if p.at(TokCOMMENT) {
		return p.parseComment()
	}
	if p.at(TokSET) {
		return p.parseSet()
	}
	if p.at(TokPRAGMA) {
		return p.parsePragma()
	}
	if p.at(TokUSE) {
		return p.parseUse()
	}
	if p.at(TokATTACH) || p.at(TokDETACH) {
		return p.parseAttachDetach()
	}
	if p.at(TokINSTALL) || p.at(TokFORCE) {
		return p.parseInstall()
	}
	if p.at(TokCACHE) {
		return p.parseCache()
	}
	if p.at(TokUNCACHE) {
		return p.parseUncache()
	}
	if p.at(TokDESCRIBE) || p.at(TokDESC) {
		return p.parseDescribe()
	}
	if p.at(TokANALYZE) {
		return p.parseAnalyze()
	}
	if p.at(TokLOAD) {
		return p.parseLoadData()
	}
	if p.at(TokDECLARE) {
		return p.parseDeclare()
	}
	if p.at(TokBEGIN) || p.at(TokCOMMIT) || p.at(TokROLLBACK) ||
		(p.at(TokEND) && p.tables.EndCommits) {
		return p.parseTransaction()
	}
	if p.at(TokKILL) {
		return p.parseKill()
	}
	if p.at(TokCOPY) {
		return p.parseCopy()
	}
	if p.at(TokEXECUTE) && p.tables.ExecuteBuildsExecute {
		return p.parseExecute()
	}
	if p.at(TokSHOW) && len(p.tables.ShowKinds) > 0 {
		return p.parseShow()
	}
	if p.at(TokSELECT) || p.at(TokPIVOT) || p.at(TokUNPIVOT) || p.at(TokFROM) {
		return p.parseQueryBody()
	}
	// After every statement with a grammar of its own, and before the ones
	// this port only names: the reference asks in that order too, so a
	// keyword that is BOTH -- DuckDB's SHOW -- is read as the statement
	// rather than kept as text.
	if p.atCommand() {
		return p.parseCommand()
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
	// A statement that is an expression may still go on: a SET OPERATION
	// takes any expression on its left -- `1 UNION SELECT 2` is a Union --
	// and a query in parentheses then takes the modifiers a query takes.
	// `(SELECT 1) UNION SELECT 2` and `(SELECT 1) ORDER BY x LIMIT 1` are
	// both of those, and `(SELECT 1) + 1` is neither: reading the
	// parentheses as a query BEFORE the expression parser saw them refused
	// the sum for having tokens left over, which the generator fuzzer found
	// on the port's own output.
	e, err = p.parseSetOperations(e)
	if err != nil {
		return nil, err
	}
	if takesQueryModifiers[e.Class] {
		if err := p.parseQueryModifiers(e); err != nil {
			return nil, err
		}
	}
	return p.parseAlias(e)
}

// takesQueryModifiers are the classes an ORDER BY, a LIMIT or an OFFSET may
// follow where they stand on their own. It is the reference's MODIFIABLES:
// a query, a table, or rows written out. `x ORDER BY y` is not a statement,
// and reading one would order a column.
var takesQueryModifiers = map[string]bool{
	"Select": true, "Union": true, "Except": true, "Intersect": true,
	"Subquery": true, "Table": true, "Values": true,
}

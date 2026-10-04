package sqlglot

import (
	"maps"
	"strings"
)

// SHOW is a statement in Snowflake, not a command whose tail is text.
// The phrases and the clauses after them are the reference parser's own.
func init() {
	installSnowflakeShow(dialectConfigs["snowflake"], parserTables["snowflake"])
}

func installSnowflakeShow(cfg *Config, tables *ParserTables) {
	if cfg == nil || tables == nil {
		return
	}
	commands := maps.Clone(cfg.Commands)
	delete(commands, TokSHOW)
	cfg.Commands = commands
	// APPLICATION PACKAGE is one scope. PACKAGE is a keyword there, so the
	// token is not a plain name.
	keys := maps.Clone(cfg.Keywords)
	keys["PACKAGE"] = TokPACKAGE
	cfg.Keywords = keys
	cfg.trie = nil
	kinds := map[string]struct{}{}
	for _, phrase := range snowflakeShowPhrases {
		kinds[strings.Join(phrase.words, " ")] = struct{}{}
	}
	tables.ShowKinds = kinds
	generators["Show"] = (*generator).writeSnowflakeShow
}

// A phrase is what may follow SHOW. TERSE on a key listing is noise: the
// reference records no terse flag for it, and writes the kind alone.
type snowflakeShowPhrase struct {
	words   []string
	this    string
	terse   bool
	iceberg bool
}

var snowflakeShowPhrases = []snowflakeShowPhrase{
	{[]string{"TERSE", "ICEBERG", "TABLES"}, "TABLES", true, true},
	{[]string{"TERSE", "PRIMARY", "KEYS"}, "PRIMARY KEYS", false, false},
	{[]string{"TERSE", "IMPORTED", "KEYS"}, "IMPORTED KEYS", false, false},
	{[]string{"TERSE", "UNIQUE", "KEYS"}, "UNIQUE KEYS", false, false},
	{[]string{"ICEBERG", "TABLES"}, "TABLES", false, true},
	{[]string{"PRIMARY", "KEYS"}, "PRIMARY KEYS", false, false},
	{[]string{"IMPORTED", "KEYS"}, "IMPORTED KEYS", false, false},
	{[]string{"UNIQUE", "KEYS"}, "UNIQUE KEYS", false, false},
	{[]string{"FILE", "FORMATS"}, "FILE FORMATS", false, false},
	{[]string{"TERSE", "DATABASES"}, "DATABASES", true, false},
	{[]string{"TERSE", "SCHEMAS"}, "SCHEMAS", true, false},
	{[]string{"TERSE", "OBJECTS"}, "OBJECTS", true, false},
	{[]string{"TERSE", "TABLES"}, "TABLES", true, false},
	{[]string{"TERSE", "VIEWS"}, "VIEWS", true, false},
	{[]string{"TERSE", "SEQUENCES"}, "SEQUENCES", true, false},
	{[]string{"TERSE", "USERS"}, "USERS", true, false},
	{[]string{"DATABASES"}, "DATABASES", false, false},
	{[]string{"SCHEMAS"}, "SCHEMAS", false, false},
	{[]string{"OBJECTS"}, "OBJECTS", false, false},
	{[]string{"TABLES"}, "TABLES", false, false},
	{[]string{"VIEWS"}, "VIEWS", false, false},
	{[]string{"SEQUENCES"}, "SEQUENCES", false, false},
	{[]string{"STAGES"}, "STAGES", false, false},
	{[]string{"COLUMNS"}, "COLUMNS", false, false},
	{[]string{"USERS"}, "USERS", false, false},
	{[]string{"FUNCTIONS"}, "FUNCTIONS", false, false},
	{[]string{"PROCEDURES"}, "PROCEDURES", false, false},
	{[]string{"WAREHOUSES"}, "WAREHOUSES", false, false},
}

func (p *parser) takeSnowflakeShowPhrase() (snowflakeShowPhrase, bool) {
	for _, phrase := range snowflakeShowPhrases {
		if p.matchWords(phrase.words...) {
			return phrase, true
		}
	}
	return snowflakeShowPhrase{}, false
}

// parseSnowflakeShow reads SHOW the way the reference's Snowflake parser does.
// A kind outside that list is refused: a command tree would mean less than
// the statement that was written.
func (p *parser) parseSnowflakeShow() (*Expression, error) {
	p.advance() // SHOW
	phrase, ok := p.takeSnowflakeShowPhrase()
	if !ok {
		return nil, p.unsupported("SHOW of something this port does not read")
	}
	history := p.matchWords("HISTORY")
	var like *Expression
	if p.match(TokLIKE) {
		like = p.tryParseStringLiteral()
		if like == nil {
			return nil, p.unsupported("SHOW LIKE without a string")
		}
	}
	var scope *Expression
	scopeKind := ""
	if p.match(TokIN) {
		var err error
		scope, scopeKind, err = p.snowflakeShowScope(phrase.this)
		if err != nil {
			return nil, err
		}
	}
	var startsWith *Expression
	if p.matchWords("STARTS", "WITH") {
		startsWith = p.tryParseStringLiteral()
		if startsWith == nil {
			return nil, p.unsupported("SHOW STARTS WITH without a string")
		}
	}
	limit, err := p.snowflakeShowLimit()
	if err != nil {
		return nil, err
	}
	var from *Expression
	if p.match(TokFROM) {
		from = p.tryParseStringLiteral()
		if from == nil {
			return nil, p.unsupported("SHOW FROM without a string")
		}
	}
	privileges, err := p.snowflakeShowPrivileges()
	if err != nil {
		return nil, err
	}
	var scopeKindVal any
	if scopeKind != "" {
		scopeKindVal = scopeKind
	}
	starts := any(false)
	if startsWith != nil {
		starts = startsWith
	}
	return New("Show",
		Arg{"terse", phrase.terse},
		Arg{"iceberg", phrase.iceberg},
		Arg{"this", phrase.this},
		Arg{"history", history},
		Arg{"like", like},
		Arg{"scope", scope},
		Arg{"scope_kind", scopeKindVal},
		Arg{"starts_with", starts},
		Arg{"limit", limit},
		Arg{"from_", from},
		Arg{"privileges", privileges},
	), nil
}

// snowflakeShowScope reads the IN clause. ACCOUNT, CLASS, and APPLICATION
// are words of their own. A database object kind names itself. Anything
// else is a schema for the kinds that live in one, and a table otherwise.
func (p *parser) snowflakeShowScope(kind string) (*Expression, string, error) {
	switch {
	case p.matchWords("ACCOUNT"):
		return nil, "ACCOUNT", nil
	case p.matchWords("CLASS"):
		return p.snowflakeNamedScope("CLASS")
	case p.matchWords("APPLICATION"):
		scopeKind := "APPLICATION"
		if p.matchWords("PACKAGE") {
			scopeKind = "APPLICATION PACKAGE"
		}
		return p.snowflakeNamedScope(scopeKind)
	}
	if word, ok := p.snowflakeCreatableKind(); ok {
		if !p.atTablePart() {
			return nil, word, nil
		}
		scope, err := p.parseTableName()
		return scope, word, err
	}
	if p.curr() == nil {
		return nil, "", nil
	}
	scopeKind := "TABLE"
	if snowflakeSchemaKind(kind) {
		scopeKind = "SCHEMA"
	}
	return p.snowflakeNamedScope(scopeKind)
}

// snowflakeNamedScope reads the object named after IN, when one is written.
// A bare IN DATABASE names the kind and nothing after it.
func (p *parser) snowflakeNamedScope(kind string) (*Expression, string, error) {
	var name *Expression
	var err error
	if p.atTablePart() {
		name, err = p.parseTableName()
	}
	return name, kind, err
}

func (p *parser) snowflakeCreatableKind() (string, bool) {
	c := p.curr()
	if c == nil || !snowflakeCreatableToken(c.Type) {
		return "", false
	}
	p.advance()
	return strings.ToUpper(c.Text), true
}

func snowflakeCreatableToken(tok TokenType) bool {
	switch tok {
	case TokDATABASE, TokDICTIONARY, TokFILE_FORMAT, TokMODEL, TokNAMESPACE,
		TokSCHEMA, TokSEMANTIC_VIEW, TokSEQUENCE, TokSINK, TokSOURCE, TokSTAGE,
		TokSTORAGE_INTEGRATION, TokSTREAMLIT, TokTABLE, TokTAG, TokVIEW, TokWAREHOUSE:
		return true
	default:
		return false
	}
}

func snowflakeSchemaKind(kind string) bool {
	switch kind {
	case "OBJECTS", "TABLES", "VIEWS", "SEQUENCES", "UNIQUE KEYS", "IMPORTED KEYS":
		return true
	default:
		return false
	}
}

func (p *parser) snowflakeShowLimit() (*Expression, error) {
	if !p.match(TokLIMIT) {
		return nil, nil
	}
	if p.curr() == nil || p.curr().Type != TokNUMBER {
		return nil, p.unsupported("SHOW LIMIT without a count")
	}
	count := New("Literal", Arg{"this", p.curr().Text}, Arg{"is_string", false})
	p.advance()
	return New("Limit",
		Arg{"this", nil},
		Arg{"expression", count},
		Arg{"offset", nil},
		Arg{"limit_options", nil},
		Arg{"expressions", nil},
	), nil
}

func (p *parser) snowflakeShowPrivileges() (any, error) {
	if !p.matchWords("WITH", "PRIVILEGES") {
		return false, nil
	}
	var items []*Expression
	for {
		c := p.curr()
		if c == nil || c.Type == TokCOMMA {
			break
		}
		p.advance()
		items = append(items, New("Var", Arg{"this", strings.ToUpper(c.Text)}))
		if !p.match(TokCOMMA) {
			break
		}
	}
	if len(items) == 0 {
		return nil, p.unsupported("SHOW WITH PRIVILEGES naming none")
	}
	return items, nil
}

func (g *generator) writeSnowflakeShow(e *Expression) string {
	if g.dialect != "snowflake" {
		return g.writeShow(e)
	}
	terse, iceberg := "", ""
	if on, _ := e.Args["terse"].(bool); on {
		terse = "TERSE "
	}
	if on, _ := e.Args["iceberg"].(bool); on {
		iceberg = "ICEBERG "
	}
	name, _ := e.Args["this"].(string)
	history := ""
	if on, _ := e.Args["history"].(bool); on {
		history = " HISTORY"
	}
	like := ""
	if text := g.child(e, "like"); text != "" {
		like = " LIKE " + text
	}
	scopeKind, _ := e.Args["scope_kind"].(string)
	if scopeKind != "" {
		scopeKind = " IN " + scopeKind
	}
	scope := ""
	if text := g.child(e, "scope"); text != "" {
		scope = " " + text
	}
	starts := ""
	if text := g.child(e, "starts_with"); text != "" {
		starts = " STARTS WITH " + text
	}
	limit := ""
	if text := g.child(e, "limit"); text != "" {
		limit = " " + text
	}
	from := ""
	if text := g.child(e, "from_"); text != "" {
		from = " FROM " + text
	}
	privileges := ""
	if items, _ := e.Args["privileges"].([]*Expression); len(items) > 0 {
		parts := make([]string, 0, len(items))
		for _, item := range items {
			parts = append(parts, g.node(item))
		}
		privileges = " WITH PRIVILEGES " + strings.Join(parts, ", ")
	}
	return "SHOW " + terse + iceberg + name + history + like + scopeKind + scope + starts + limit + from + privileges
}

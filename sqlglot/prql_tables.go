package sqlglot

// PRQL is the one dialect whose PARSER tables are not derived, and the reason
// is structural rather than convenient.
//
// Every other dialect's tables come from asking the pinned reference SQL
// questions -- `CREATE PROCEDURE foo AS SELECT 1`, the property shapes, the
// quantifier templates, the function builders. PRQL answers NONE of them: it
// is a pipeline language, not SQL, and reads 0 of 10 representative probes.
//
// So its TOKENIZER is generated like everyone else's -- that part is just
// spellings -- and its PARSER is the hand-written pipeline in prql.go, running
// over the NEUTRAL tables. This is the single place the port's rule "tables
// are generated, never written by hand" is waived, and it is waived for the
// single dialect that is not a SQL dialect.
func init() {
	if parserTables["prql"] != nil {
		return
	}
	base := parserTables[""]
	copied := *base
	// The two maps the reference overrides for PRQL, and the only two it
	// overrides: `&&` is AND and `||` is OR there, where SQL spells them with
	// words (PRQLParser.CONJUNCTION, DISJUNCTION).
	copied.Conjunction = make(map[TokenType]string, len(base.Conjunction)+1)
	for token, class := range base.Conjunction {
		copied.Conjunction[token] = class
	}
	copied.Conjunction[TokDAMP] = "And"
	copied.Disjunction = make(map[TokenType]string, len(base.Disjunction)+1)
	for token, class := range base.Disjunction {
		copied.Disjunction[token] = class
	}
	copied.Disjunction[TokDPIPE] = "Or"
	// `||` is OR here rather than concatenation, which is a table flag of its
	// own because the two readings are decided in different places
	// (PRQL.DPIPE_IS_STRING_CONCAT).
	copied.DPipeIsStringConcat = false
	parserTables["prql"] = &copied
}

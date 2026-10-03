package sqlglot

import "strings"

// Oracle spells VARCHAR as VARCHAR2, and a length may name its unit:
// VARCHAR2(2328 CHAR). The unit sits beside the number, not after a comma.
func init() {
	noteOracleType("VARCHAR2", TokVARCHAR, "VARCHAR", "VARCHAR2")
	noteOracleType("NVARCHAR2", TokNVARCHAR, "NVARCHAR", "NVARCHAR2")
	// A sized parameter already writes its value and then its unit.
	// Attach options are the same two fields.
	generators["DataTypeParam"] = (*generator).writeAttachOption
}

func noteOracleType(word string, tt TokenType, kind, spelling string) {
	if cfg := dialectConfigs["oracle"]; cfg != nil {
		copied := map[string]TokenType{}
		for k, v := range cfg.Keywords {
			copied[k] = v
		}
		copied[word] = tt
		cfg.Keywords = copied
		cfg.trie = nil
	}
	if tables := parserTables["oracle"]; tables != nil {
		spellings := map[string]string{}
		for k, v := range tables.TypeSQL {
			spellings[k] = v
		}
		spellings[kind] = spelling
		tables.TypeSQL = spellings
	}
}

// typeSizeParam is a numeric type parameter. Oracle may follow the
// number with CHAR or BYTE.
func (p *parser) typeSizeParam(lit *Expression) *Expression {
	param := New("DataTypeParam", Arg{"this", lit})
	c := p.curr()
	if p.dialect != "oracle" || c == nil {
		return param
	}
	unit := ""
	switch c.Type {
	case TokCHAR:
		unit = "CHAR"
	case TokTINYINT:
		if strings.EqualFold(c.Text, "byte") {
			unit = "BYTE"
		}
	}
	if unit == "" {
		return param
	}
	p.advance()
	param.Set("expression", New("Var", Arg{"this", unit}))
	return param
}

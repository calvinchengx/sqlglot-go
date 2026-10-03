package sqlglot

import "strings"

// oracleDropForeignKey reads ALTER TABLE ... DROP FOREIGN KEY name.
// The key is a table-level thing, so its name is a table reference.
func (p *parser) oracleDropForeignKey() (*Expression, bool, error) {
	if p.dialect != "oracle" || !p.at(TokDROP) || p.next() == nil {
		return nil, false, nil
	}
	if !strings.EqualFold(p.next().Text, "FOREIGN KEY") {
		return nil, false, nil
	}
	p.advance()
	p.advance()
	name, err := p.parseTableName()
	if err != nil {
		return nil, true, err
	}
	return New("Drop",
		Arg{"exists", false},
		Arg{"tables", []*Expression{name}},
		Arg{"kind", "FOREIGN KEY"},
		Arg{"temporary", false},
		Arg{"materialized", false},
		Arg{"cascade", false},
		Arg{"restrict", false},
		Arg{"constraints", false},
		Arg{"purge", false},
		Arg{"concurrently", false},
		Arg{"sync", false},
		Arg{"iceberg", false},
		Arg{"force", false},
	), true, nil
}

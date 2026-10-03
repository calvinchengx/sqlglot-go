package sqlglot

import "strings"

// Oracle writes a zoned timestamp as TIMESTAMP(n) WITH TIME ZONE. The
// kind stays TIMESTAMPTZ. A bare expression is a type only when those
// words follow it; TIMESTAMP '...' stays a typed literal.
func init() {
	noteOracleTimeZone(parserTables["oracle"])
}

func noteOracleTimeZone(tables *ParserTables) {
	if tables == nil || tables.TypeSQL == nil {
		return
	}
	tables.TzToWithTimeZone = true
	tables.TypeSQL["TIMESTAMPTZ"] = "TIMESTAMP WITH TIME ZONE"
	tables.SizedTypeSQL = map[string]string{"TIMESTAMPTZ": "TIMESTAMP"}
}

func (p *parser) oracleZonedTimestamp() (*Expression, bool, error) {
	if p.dialect != "oracle" || !p.zoneWordsFollowType() {
		return nil, false, nil
	}
	dt, err := p.parseDataType()
	return dt, true, err
}

func (p *parser) zoneWordsFollowType() bool {
	c := p.curr()
	if c == nil {
		return false
	}
	if _, timestamp := p.tables.TimestampTypeTokens[c.Type]; !timestamp {
		return false
	}
	if n := p.next(); n != nil && n.Type == TokSTRING {
		return false
	}
	open := p.peekAt(1)
	if open != nil && open.Type == TokL_PAREN {
		close := p.peekAt(3)
		if close == nil || close.Type != TokR_PAREN {
			return false
		}
		return p.withTimeZoneFrom(4)
	}
	return p.withTimeZoneFrom(1)
}

func (p *parser) withTimeZoneFrom(at int) bool {
	with := p.peekAt(at)
	time := p.peekAt(at + 1)
	zone := p.peekAt(at + 2)
	if with == nil || time == nil || zone == nil {
		return false
	}
	return strings.EqualFold(with.Text, "WITH") &&
		strings.EqualFold(time.Text, "TIME") &&
		strings.EqualFold(zone.Text, "ZONE")
}

package sqlglot

import "errors"

// ErrUnsupported is returned for any statement the port does not yet parse.
//
// This is the port's fail-closed property, stated as a type: a consumer that
// guards SQL treats this error as a REFUSAL, never as "probably fine". The
// harness counts it as a coverage gap, never as a silent divergence.
var ErrUnsupported = errors.New("sqlglot-go: unsupported statement")

// ParseOne parses a single statement in the named dialect ("" for the
// dialect-neutral grammar) and returns its tree.
//
// The port is built outward from what a read-only guard needs (see
// docs/17-sqlglot-go.md in data-agent-service); until a construct is ported
// this returns ErrUnsupported for it.
func ParseOne(sql string, dialect string) (*Expression, error) {
	return nil, ErrUnsupported
}

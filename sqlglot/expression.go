// Package sqlglot is a Go port of sqlglot's expression model and parser,
// scoped to what a read-only SQL guard needs first and growing outward.
//
// The architecture -- a tokenizer, a data-driven parser over dispatch tables,
// an expression tree in which every construct is a visible node, and dialects
// as override sets -- is Toby Mao's, from github.com/tobymao/sqlglot (MIT).
// See NOTICE for the reference commit this port is measured against.
package sqlglot

import "encoding/json"

// Expression is one node of a parsed statement.
//
// It mirrors sqlglot's Expression closely enough that a tree dumped by the
// Python reference and a tree built here can be compared field by field:
// a class name, and named args each holding a child, a list of children, or
// a leaf value. That comparability is the whole verification strategy, so
// the shape is not negotiable -- where this port disagrees with the
// reference, the port is wrong until shown otherwise.
type Expression struct {
	// Class is the reference's node class name, e.g. "Select", "Table",
	// "Identifier". Kept as the Python spelling so dumps compare directly.
	Class string
	// Args are the node's named children and leaf values, in the reference's
	// key names ("this", "expressions", "from_", "db", "quoted", …). Keys
	// records insertion order, because the reference's dump order depends on
	// it and so the comparison does too.
	Args map[string]any
	Keys []string
	// Parent is set by the parser; never serialised.
	Parent *Expression
}

// Arg is one named argument of a node, in the order it was given.
type Arg struct {
	Key   string
	Value any
}

// New builds a node and wires its children's Parent pointers.
//
// Arguments are variadic pairs rather than a map because the reference's dump
// order follows arg declaration order, and Go map iteration is randomised --
// a map here would make the dump differ run to run and the differential
// comparison flap.
func New(class string, args ...Arg) *Expression {
	e := &Expression{Class: class, Args: map[string]any{}}
	for _, a := range args {
		e.Set(a.Key, a.Value)
	}
	return e
}

// Set assigns one arg, adopting any child expressions.
//
// A typed nil child is normalised to an absent one. The reference stores None
// there, and an absent arg produces no dump record at all -- so a *Expression
// that happens to be nil must not become a leaf record holding "null", which is
// what a naive store would do and what the differential would then report as a
// mismatch on every optional clause.
func (e *Expression) Set(key string, value any) {
	switch v := value.(type) {
	case *Expression:
		if v == nil {
			value = nil
			break
		}
		v.Parent = e
	case []*Expression:
		if len(v) == 0 {
			value = nil
			break
		}
		for _, c := range v {
			if c != nil {
				c.Parent = e
			}
		}
	}
	if _, seen := e.Args[key]; !seen {
		e.Keys = append(e.Keys, key)
	}
	e.Args[key] = value
}

// This is the conventional primary child, as in the reference.
func (e *Expression) This() *Expression {
	if e == nil {
		return nil
	}
	c, _ := e.Args["this"].(*Expression)
	return c
}

// Name is the reference's `.name`: the string of `this` when it is an
// Identifier or literal, else "".
func (e *Expression) Name() string {
	if e == nil {
		return ""
	}
	switch v := e.Args["this"].(type) {
	case string:
		return v
	case *Expression:
		if v.Class == "Identifier" || v.Class == "Literal" {
			return v.Name()
		}
	}
	return ""
}

// Walk visits the node and every descendant, depth first, in arg-key order
// so traversal is deterministic. Returning false from fn stops descent into
// that node's children.
func (e *Expression) Walk(fn func(*Expression) bool) {
	if e == nil {
		return
	}
	if !fn(e) {
		return
	}
	for _, key := range e.Keys {
		switch v := e.Args[key].(type) {
		case *Expression:
			v.Walk(fn)
		case []*Expression:
			for _, c := range v {
				c.Walk(fn)
			}
		}
	}
}

// FindAll returns every descendant (including the node itself) whose Class
// is one of those named -- the reference's `find_all`.
func (e *Expression) FindAll(classes ...string) []*Expression {
	want := map[string]bool{}
	for _, c := range classes {
		want[c] = true
	}
	var out []*Expression
	e.Walk(func(n *Expression) bool {
		if want[n.Class] {
			out = append(out, n)
		}
		return true
	})
	return out
}

// Dump renders the tree in the reference's `serde.dump()` format, so a Go
// tree and a Python tree compare as the SAME kind of value.
//
// The format is a flat list of records in pre-order. Each record carries the
// node's class ("c"), the index of its parent ("i"), the arg key it sits
// under ("k"), and "a": true when it is one element of a list. A LEAF value
// is its own record with "v" -- not a field on its parent. Order follows the
// reference's iterative algorithm exactly: children are pushed onto a stack
// in reverse so they pop in declaration order, which means arg order is part
// of the contract and Args is kept as an ordered sequence rather than a map.
//
// Two reference fields are deliberately not produced: "m" (source positions)
// and "o" (comments). They are metadata, not structure, and the comparison
// in the harness strips them from the reference before diffing.
func (e *Expression) Dump() []map[string]any {
	type frame struct {
		node   any
		parent int
		key    string
		isList bool
	}
	var out []map[string]any
	stack := []frame{{node: e, parent: -1}}
	i := 0
	for len(stack) > 0 {
		f := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		rec := map[string]any{}
		if f.parent >= 0 {
			rec["i"] = f.parent
			rec["k"] = f.key
		}
		if f.isList {
			rec["a"] = true
		}
		out = append(out, rec)
		switch n := f.node.(type) {
		case *Expression:
			rec["c"] = qualifiedClass(n.Class)
			for idx := len(n.Keys) - 1; idx >= 0; idx-- {
				k := n.Keys[idx]
				switch v := n.Args[k].(type) {
				case []*Expression:
					for j := len(v) - 1; j >= 0; j-- {
						stack = append(stack, frame{node: v[j], parent: i, key: k, isList: true})
					}
				case nil:
				default:
					stack = append(stack, frame{node: v, parent: i, key: k})
				}
			}
		case DataTypeKind:
			rec["c"] = "DataType.Type"
			rec["v"] = string(n)
		default:
			rec["v"] = n
		}
		i++
	}
	return out
}

// DataTypeKind is the reference's DataType.Type enum member, which dumps as
// its own record class rather than as a plain leaf.
type DataTypeKind string

// DumpJSON is Dump as bytes, for a test's diff output.
func (e *Expression) DumpJSON() []byte {
	b, _ := json.MarshalIndent(e.Dump(), "", " ")
	return b
}

// qualifiedClass maps a short class name to the reference's qualified name
// ("Select" -> "sqlglot.expressions.query.Select"). The reference qualifies
// by the module the class lives in; this table is that layout, and a class
// missing from it is a port bug the harness will report as a mismatch.
func qualifiedClass(class string) string {
	if mod, ok := classModule[class]; ok {
		return "sqlglot.expressions." + mod + "." + class
	}
	return "sqlglot.expressions.core." + class
}

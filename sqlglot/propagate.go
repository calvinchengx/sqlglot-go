package sqlglot

// propagateConstants is the reference's `propagate_constants`: within a
// conjunction that has no OR anywhere in it (a single DNF conjunct -- an OR
// would need distributing first, which this does not attempt), find every
// `column = literal` and substitute that literal for every OTHER occurrence
// of that column anywhere in the same conjunction's scope, including inside
// a CASE branch's own result.
//
//	SELECT * FROM t WHERE a = b AND b = 5  -->  SELECT * FROM t WHERE a = 5 AND b = 5
//
// It only ever runs on a non-root And whose own parent is a DIFFERENT class:
// an And directly nested under another And is what flattening merges away,
// and processing it here too would be redundant work on a shape that is not
// going to survive as written.
func propagateConstants(e, parent *Expression) *Expression {
	if e.Class != "And" || (parent != nil && parent.Class == "And") {
		return e
	}
	if scopeContainsOr(e) {
		return e
	}
	mapping := collectConstantMapping(e)
	if len(mapping) == 0 {
		return e
	}
	substituteConstants(e, mapping)
	return e
}

// constEntry is one column's known constant value: node is the SPECIFIC
// column that supplied it, kept so that column's own occurrence is never
// replaced by a copy of itself, and value is the literal.
type constEntry struct {
	node  *Expression
	value *Expression
}

// collectConstantMapping finds every `column = literal` reachable from e
// without crossing into a nested subquery's own scope or descending into an
// IF's condition -- an EQ that only decides one CASE branch says nothing
// about the column's value everywhere else. A column found more than once
// keeps its LAST value, matching the reference's own dict overwrite.
func collectConstantMapping(e *Expression) []constEntry {
	var mapping []constEntry
	pruneIf := func(n *Expression) bool { return n.Class == "If" }
	walkScope(e, pruneIf, func(_, child *Expression, _ func(*Expression)) {
		if child.Class != "EQ" {
			return
		}
		l := childOf(child, "this")
		r := childOf(child, "expression")
		if l == nil || l.Class != "Column" || r == nil || r.Class != "Literal" {
			return
		}
		for i := range mapping {
			if mapping[i].node.Equal(l) {
				mapping[i] = constEntry{node: l, value: r}
				return
			}
		}
		mapping = append(mapping, constEntry{node: l, value: r})
	})
	return mapping
}

// substituteConstants replaces every column reachable from e that matches a
// mapping entry -- other than the exact node that supplied it -- with a copy
// of that entry's literal. A column directly under `IS NULL` is left alone:
// substituting there would rewrite a real null-check into a check against
// the literal, which is a different question, not a spelling change.
func substituteConstants(e *Expression, mapping []constEntry) {
	walkScope(e, nil, func(parent, child *Expression, replace func(*Expression)) {
		if child.Class != "Column" {
			return
		}
		for _, m := range mapping {
			if child == m.node || !child.Equal(m.node) {
				continue
			}
			if parent != nil && parent.Class == "Is" && isNull(childOf(parent, "expression")) {
				return
			}
			replace(m.value.Copy())
			return
		}
	})
}

// scopeContainsOr reports whether an OR is reachable from e without crossing
// into a nested subquery -- the reference's own `normalized(e, dnf=True)`
// check, specialised to what deciding it actually takes here: a conjunction
// with an OR anywhere inside it is not a single DNF conjunct, and
// propagating through it without first distributing that OR out would be
// guessing at a form the tree is not yet in.
func scopeContainsOr(e *Expression) bool {
	found := false
	walkScope(e, nil, func(_, child *Expression, _ func(*Expression)) {
		if child.Class == "Or" {
			found = true
		}
	})
	return found
}

// walkScope calls visit for every descendant of e reachable without
// crossing into a nested SELECT (a different scope), passing each child's
// immediate parent and a closure that replaces that child in place. prune,
// when it returns true for a node, stops descent into that node's own
// children -- the node itself is still visited first, matching the
// reference's own `walk_in_scope(..., prune=...)`.
func walkScope(e *Expression, prune func(*Expression) bool, visit func(parent, child *Expression, replace func(*Expression))) {
	if e == nil {
		return
	}
	for _, key := range e.Keys {
		switch v := e.Args[key].(type) {
		case *Expression:
			if v == nil {
				continue
			}
			k := key
			visit(e, v, func(n *Expression) { e.Set(k, n) })
			if v.Class == "Select" || (prune != nil && prune(v)) {
				continue
			}
			walkScope(v, prune, visit)
		case []*Expression:
			for i, c := range v {
				if c == nil {
					continue
				}
				idx := i
				list := v
				visit(e, c, func(n *Expression) {
					list[idx] = n
					n.Parent = e
				})
				if c.Class == "Select" || (prune != nil && prune(c)) {
					continue
				}
				walkScope(c, prune, visit)
			}
		}
	}
}

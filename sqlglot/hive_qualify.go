package sqlglot

// eliminateQualify is the reference's own `eliminate_qualify`: a SELECT with a
// QUALIFY clause is rewritten into an outer select over a subquery that
// projects the window functions the clause reads, and the clause itself
// becomes a WHERE (transforms.py, eliminate_qualify).
//
// It reports false where the statement is one the reference refuses -- a
// QUALIFY inside a FROM-first multi-table INSERT branch shares its parent's
// FROM and cannot be wrapped.
func eliminateQualify(sel *Expression) (*Expression, bool) {
	qualify, _ := sel.Args["qualify"].(*Expression)
	if qualify == nil {
		return sel, true
	}
	// Only a branch of a FROM-first multi-table insert is refused: its FROM
	// is the parent's, so the query cannot be wrapped
	// (transforms.py, eliminate_qualify).
	if parent := sel.Parent; parent != nil && parent.Class == "Insert" {
		if grand := parent.Parent; grand != nil && grand.Class == "MultitableInserts" {
			return nil, false
		}
	}
	selects, _ := sel.Args["expressions"].([]*Expression)
	taken := map[string]bool{}
	for _, s := range selects {
		taken[aliasOrName(s)] = true
	}
	// A projection with no name of its own is given one, so the outer query
	// can refer to it.
	inner := make([]*Expression, 0, len(selects))
	for _, s := range selects {
		if aliasOrName(s) == "" {
			name := findNewName(taken, "_c")
			taken[name] = true
			s = New("Alias", Arg{"this", s}, Arg{"alias",
				New("Identifier", Arg{"this", name}, Arg{"quoted", false})})
		}
		inner = append(inner, s)
	}
	// The outer select names what the inner one projected, keeping the
	// quoting each name was written with.
	outer := make([]*Expression, 0, len(inner))
	for _, s := range inner {
		name := aliasOrName(s)
		quoted := false
		identifier := childOf(s, "this")
		if alias, _ := s.Args["alias"].(*Expression); alias != nil {
			identifier = alias
		}
		if identifier != nil {
			quoted, _ = identifier.Args["quoted"].(bool)
		}
		outer = append(outer, New("Column", Arg{"this",
			New("Identifier", Arg{"this", name}, Arg{"quoted", quoted})}))
	}

	filters := childOf(qualify, "this")
	byAlias := map[string]*Expression{}
	for _, s := range inner {
		if s.Class == "Alias" {
			if alias, _ := s.Args["alias"].(*Expression); alias != nil {
				byAlias[alias.Name()] = childOf(s, "this")
			}
		}
	}

	named := map[string]bool{}
	for _, s := range inner {
		named[aliasOrName(s)] = true
	}
	star := false
	for _, s := range inner {
		if s.Class == "Star" {
			star = true
		}
	}
	candidates := []string{"Window"}
	if !star {
		candidates = append(candidates, "Column")
	}
	for _, candidate := range filters.FindAll(candidates...) {
		if candidate.Class == "Window" {
			// A window the filter reads is projected under a new name, and
			// the filter reads that name instead.
			if len(byAlias) > 0 {
				for _, column := range candidate.FindAll("Column") {
					if expr := byAlias[column.Name()]; expr != nil {
						replaceNode(column, expr)
					}
				}
			}
			name := findNewName(named, "_w")
			inner = append(inner, New("Alias", Arg{"this", candidate.Copy()},
				Arg{"alias", New("Identifier", Arg{"this", name}, Arg{"quoted", false})}))
			named[name] = true
			column := New("Column", Arg{"this",
				New("Identifier", Arg{"this", name}, Arg{"quoted", false})})
			if candidate.Parent != nil && candidate.Parent.Class == "Qualify" {
				filters = column
			} else {
				replaceNode(candidate, column)
			}
			continue
		}
		// A column the filter reads and the query does not project is added
		// to the subquery, unless a window already computes it.
		if !named[candidate.Name()] && findAncestorOfClass(candidate, "Window") == nil {
			inner = append(inner, candidate.Copy())
		}
	}

	innerSelect := New("Select")
	for _, k := range selectPrefix {
		innerSelect.Set(k, nil)
	}
	innerSelect.Set("expressions", inner)
	innerSelect.Set("from_", sel.Args["from_"])
	for _, key := range []string{"where", "group", "having", "windows", "order", "limit", "offset", "distribute", "sort", "cluster"} {
		if v, ok := sel.Args[key]; ok && v != nil {
			innerSelect.Set(key, v)
		}
	}
	subquery := New("Subquery", Arg{"this", innerSelect},
		Arg{"alias", New("TableAlias", Arg{"this",
			New("Identifier", Arg{"this", "_t"}, Arg{"quoted", false})})})

	rewritten := New("Select")
	for _, k := range selectPrefix {
		rewritten.Set(k, nil)
	}
	rewritten.Set("expressions", outer)
	rewritten.Set("from_", New("From", Arg{"this", subquery}))
	rewritten.Set("where", New("Where", Arg{"this", filters}))
	return rewritten, true
}

// aliasOrName is the reference's `alias_or_name`: the alias where there is
// one, and the node's own name otherwise.
func aliasOrName(e *Expression) string {
	if e == nil {
		return ""
	}
	if alias, _ := e.Args["alias"].(*Expression); alias != nil {
		return alias.Name()
	}
	return e.Name()
}

// findNewName is the reference's helper of the same name: the base name, or
// the first `base_2`, `base_3`... that is not taken.
func findNewName(taken map[string]bool, base string) string {
	if !taken[base] {
		return base
	}
	for i := 2; ; i++ {
		name := base + "_" + itoa(i)
		if !taken[name] {
			return name
		}
	}
}

// findAncestorOfClass walks up from a node looking for a class.
func findAncestorOfClass(e *Expression, class string) *Expression {
	for p := e.Parent; p != nil; p = p.Parent {
		if p.Class == class {
			return p
		}
	}
	return nil
}

// itoa is strconv.Itoa without the import at every call site.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var digits []byte
	for i > 0 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
		i /= 10
	}
	return string(digits)
}

// writesQualifyAsHive reports whether the dialect has no QUALIFY of its own
// and rewrites the query instead (HiveGenerator.TRANSFORMS,
// transforms.eliminate_qualify).
func writesQualifyAsHive(dialect string) bool {
	switch dialect {
	case "hive", "spark", "spark2":
		return true
	}
	return false
}

// replaceNode puts a new node where an old one stood, in its parent's own
// argument list.
func replaceNode(old, replacement *Expression) {
	parent := old.Parent
	if parent == nil || replacement == nil {
		return
	}
	for _, key := range parent.Keys {
		switch v := parent.Args[key].(type) {
		case *Expression:
			if v == old {
				parent.Args[key] = replacement
				replacement.Parent = parent
				return
			}
		case []*Expression:
			for i, item := range v {
				if item == old {
					v[i] = replacement
					replacement.Parent = parent
					return
				}
			}
		}
	}
}

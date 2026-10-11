package sqlglot

import "strings"

// writePartitionedByProperty writes a table's PARTITION BY property.
//
// Doris wraps the column list in ONE pair of parentheses and writes the
// columns FLAT (partitionedbyproperty_sql in the reference). The generic
// template -- `PARTITION BY {this}` -- fills in the Schema's own rendering,
// which already carries a pair of parentheses, so it comes out doubled:
// `PARTITION BY ((c1, c2))`. Doris therefore writes the property itself and
// every other dialect keeps the template.
func (g *generator) writePartitionedByProperty(e *Expression) string {
	if g.dialect == "starrocks" {
		// StarRocks partitions by an expression and writes no parentheses.
		if this, _ := e.Args["this"].(*Expression); this != nil && this.Class == "Schema" {
			items, _ := this.Args["expressions"].([]*Expression)
			parts := make([]string, 0, len(items))
			for _, item := range items {
				parts = append(parts, g.node(item))
			}
			return "PARTITION BY " + strings.Join(parts, ", ")
		}
		return "PARTITION BY " + g.child(e, "this")
	}
	if g.dialect == "doris" {
		this, _ := e.Args["this"].(*Expression)
		if this != nil && this.Class == "Schema" {
			items, _ := this.Args["expressions"].([]*Expression)
			parts := make([]string, 0, len(items))
			for _, item := range items {
				parts = append(parts, g.node(item))
			}
			return "PARTITION BY (" + strings.Join(parts, ", ") + ")"
		}
		return "PARTITION BY (" + g.node(this) + ")"
	}
	return g.spell(e)
}

// writePartitionByRangePropertyDynamic writes a dynamic range partition.
// StarRocks spells it `START ('a') END ('b') EVERY (n U)`; Doris spells it
// `FROM ('a') TO ('b') INTERVAL n U`.
func (g *generator) writePartitionByRangePropertyDynamic(e *Expression) string {
	start := g.child(e, "start")
	end := g.child(e, "end")
	if g.dialect == "starrocks" {
		out := "START (" + start + ") END (" + end + ")"
		if every, _ := e.Args["every"].(*Expression); every != nil {
			// The reference rewrites an INTERVAL's string quantity as a
			// number before writing it -- `EVERY (INTERVAL '1' YEAR)` is
			// written `EVERY (INTERVAL 1 YEAR)`. Do it on a copy so the
			// caller's tree keeps the string it was parsed with.
			rendered := every
			if every.Class == "Interval" {
				rendered = every.Copy()
				if lit, _ := rendered.Args["this"].(*Expression); lit != nil && lit.Class == "Literal" {
					lit.Set("is_string", false)
				}
			}
			out += " EVERY (" + g.node(rendered) + ")"
		}
		return out
	}
	out := "FROM (" + start + ") TO (" + end + ")"
	if every, _ := e.Args["every"].(*Expression); every != nil {
		out += " INTERVAL " + g.child(every, "this") + " " + g.child(every, "unit")
	}
	return out
}

// writeUniqueKeyProperty writes a composite key. Doris writes a bare KEY
// inside a MATERIALIZED VIEW -- the MV's own key, not a uniqueness constraint
// (DorisGenerator.uniquekeyproperty_sql); every other dialect keeps the
// template's `UNIQUE KEY (...)`.
func (g *generator) writeUniqueKeyProperty(e *Expression) string {
	if g.dialect == "doris" && dorisInsideMaterializedView(e) {
		return "KEY (" + g.list(e) + ")"
	}
	return g.spell(e)
}

func dorisInsideMaterializedView(e *Expression) bool {
	for p := e.Parent; p != nil; p = p.Parent {
		if p.Class != "Create" {
			continue
		}
		props, _ := p.Args["properties"].(*Expression)
		if props == nil {
			return false
		}
		items, _ := props.Args["expressions"].([]*Expression)
		for _, item := range items {
			if item != nil && item.Class == "MaterializedProperty" {
				return true
			}
		}
		return false
	}
	return false
}

// starrocksEliminateBetween rewrites StarRocks' unsupported DELETE BETWEEN into
// explicit comparisons (StarRocksGenerator._eliminate_between_in_delete), on a
// copy so the caller's tree is untouched.
func starrocksEliminateBetween(where *Expression) {
	for _, b := range where.FindAll("Between") {
		this, _ := b.Args["this"].(*Expression)
		low, _ := b.Args["low"].(*Expression)
		high, _ := b.Args["high"].(*Expression)
		if this == nil || low == nil || high == nil {
			continue
		}
		and := New("And",
			Arg{"this", New("GTE", Arg{"this", this.Copy()}, Arg{"expression", low})},
			Arg{"expression", New("LTE", Arg{"this", this.Copy()}, Arg{"expression", high})})
		replaceInParent(b, and)
	}
}

func hasGenerateDateArrayUnnest(e *Expression) bool {
	for _, u := range e.FindAll("Unnest") {
		exprs, _ := u.Args["expressions"].([]*Expression)
		for _, x := range exprs {
			if x != nil && x.Class == "GenerateDateArray" {
				return true
			}
		}
	}
	return false
}

func replaceInParent(old, new *Expression) {
	p := old.Parent
	if p == nil {
		return
	}
	for _, k := range p.Keys {
		switch v := p.Args[k].(type) {
		case *Expression:
			if v == old {
				p.Set(k, new)
				return
			}
		case []*Expression:
			for i, x := range v {
				if x == old {
					v[i] = new
					new.Parent = p
					return
				}
			}
		}
	}
}

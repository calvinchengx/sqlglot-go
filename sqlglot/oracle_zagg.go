package sqlglot

func init() {
	generators["JSONArrayAgg"] = (*generator).writeJSONArrayAgg
	parserTables["oracle"].SyntaxFunctions["JSON_ARRAYAGG"] = struct{}{}
	spell := parserTables["oracle"].SyntaxSQL["JSONArrayAgg"]
	parserTables["oracle"].SyntaxSQL["JSONArrayAgg"] = append(append([]SyntaxTemplate{}, spell...),
		SyntaxTemplate{
			Keys:     []string{"this", "return_type"},
			Marked:   []string{"this", "return_type"},
			Required: []FuncConst{{Key: "strict", Value: false}},
			Template: "JSON_ARRAYAGG({this} RETURNING {return_type})",
		},
		SyntaxTemplate{
			Keys:     []string{"this", "order", "null_handling", "return_type", "strict"},
			Marked:   []string{"this", "order", "null_handling", "return_type"},
			Required: []FuncConst{{Key: "strict", Value: true}},
			Template: "JSON_ARRAYAGG({this} {order} {null_handling} RETURNING {return_type} STRICT)",
		},
	)
}

func (p *parser) oracleJSONArrayTail(node *Expression) error {
	if p.atWords("RETURNING") {
		p.advance()
		rt, err := p.parsePostfix()
		if err != nil {
			return err
		}
		node.Set("return_type", rt)
		node.Set("strict", false)
		if p.atWords("STRICT") {
			p.advance()
			node.Set("strict", true)
		}
		return nil
	}
	p.advance()
	node.Set("strict", true)
	return nil
}

func spellOracleJSONArrayAgg(node *Expression) *Expression {
	this, _ := node.Args["this"].(*Expression)
	_, strictSet := node.Args["strict"]
	_, hasReturn := node.Args["return_type"]
	format := this != nil && this.Class == "FormatJson"
	if !format && !strictSet && !hasReturn {
		return node
	}
	out := New("JSONArrayAgg")
	if nulls, _ := node.Args["null_handling"].(string); nulls != "" {
		out.Set("null_handling", nulls)
	}
	if hasReturn {
		out.Set("return_type", node.Args["return_type"])
	}
	if strictSet {
		out.Set("strict", node.Args["strict"])
	}
	out.Set("this", this)
	if order, _ := node.Args["order"].(*Expression); order != nil {
		out.Set("order", order)
	}
	return out
}

// writeJSONArrayAgg is the reference's own jsonarrayagg_sql: the value, then
// an ordering, a null handling, a RETURNING type and a STRICT, each written
// only where the tree carries one. Another dialect spells the same node its
// own way, which the probed tables already know.
func (g *generator) writeJSONArrayAgg(e *Expression) string {
	if g.dialect != "oracle" {
		return g.spell(e)
	}
	out := "JSON_ARRAYAGG(" + g.child(e, "this")
	if order := g.child(e, "order"); order != "" {
		out += " " + order
	}
	if nulls, _ := e.Args["null_handling"].(string); nulls != "" {
		out += " " + nulls
	}
	if ret := g.child(e, "return_type"); ret != "" {
		out += " RETURNING " + ret
	}
	if strict, _ := e.Args["strict"].(bool); strict {
		out += " STRICT"
	}
	return out + ")"
}

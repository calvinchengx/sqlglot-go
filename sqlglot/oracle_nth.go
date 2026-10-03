package sqlglot

// Oracle places FROM FIRST or FROM LAST between NTH_VALUE and its
// null treatment and OVER clause. The words are a flag on the call,
// not an argument, and LAST is that flag set false.
func (p *parser) oracleNthValueFrom(this *Expression, err error) (*Expression, error) {
	if err != nil || p.dialect != "oracle" || this == nil || this.Class != "NthValue" {
		return this, err
	}
	if !p.atWords("FROM", "FIRST") && !p.atWords("FROM", "LAST") {
		return this, nil
	}
	fromFirst := p.atWords("FROM", "FIRST")
	p.advance()
	p.advance()
	// The join mark belongs on the finished column expression. It was
	// stamped on the call before FROM was visible.
	_, marked := this.Args["join_mark"]
	if marked {
		this.Set("join_mark", nil)
	}
	this.Set("from_first", fromFirst)
	if word := p.atNullsModifier(); word != "" {
		p.advance()
		p.advance()
		this = New(word, Arg{"this", this})
	}
	if p.at(TokOVER) {
		window, werr := p.parseWindow(this)
		if werr != nil {
			return nil, werr
		}
		this = window
	}
	if marked {
		this.Set("join_mark", false)
	}
	return this, nil
}

func init() {
	generators["NthValue"] = writeNthValueFrom
}

// writeNthValueFrom writes FROM FIRST or FROM LAST after the call. The
// recorded spelling would put the flag inside the parentheses.
func writeNthValueFrom(g *generator, e *Expression) string {
	if g.dialect != "oracle" {
		return g.spell(e)
	}
	val, keys, ok := detachArg(e, "from_first")
	if !ok || val == nil {
		return g.spell(e)
	}
	defer func() {
		e.Keys = keys
		e.Args["from_first"] = val
	}()
	out := g.spell(e)
	if g.err != nil {
		return out
	}
	word := "LAST"
	if on, _ := val.(bool); on {
		word = "FIRST"
	}
	return out + " FROM " + word
}

// detachArg lifts one argument off a node so a spelling that treats it
// as absent can be used, and returns the key order to put back.
func detachArg(e *Expression, key string) (any, []string, bool) {
	val, ok := e.Args[key]
	if !ok {
		return nil, nil, false
	}
	keys := append([]string(nil), e.Keys...)
	delete(e.Args, key)
	kept := make([]string, 0, len(e.Keys))
	for _, k := range e.Keys {
		if k != key {
			kept = append(kept, k)
		}
	}
	e.Keys = kept
	return val, keys, true
}

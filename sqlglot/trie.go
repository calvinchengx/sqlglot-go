package sqlglot

import "strings"

// trieNode is the reference's keyword trie: nested maps keyed by character,
// with a marker for "a keyword ends here". Only multi-character keywords that a
// plain word scan would not find go in -- those containing a space or a
// single-token character, which is the reference's own admission rule.
type trieNode struct {
	kids map[rune]*trieNode
	word bool
}

func (n *trieNode) get(r rune) *trieNode {
	if n == nil {
		return nil
	}
	return n.kids[r]
}

func (n *trieNode) insert(key string) {
	cur := n
	for _, r := range key {
		if cur.kids == nil {
			cur.kids = map[rune]*trieNode{}
		}
		next, ok := cur.kids[r]
		if !ok {
			next = &trieNode{}
			cur.kids[r] = next
		}
		cur = next
	}
	cur.word = true
}

// buildTrie admits a key when it contains a space or any single-token
// character, because those are exactly the keys the word scanner cannot reach
// on its own. Same predicate as the reference's __init_subclass__.
func (c *Config) buildTrie() *trieNode {
	root := &trieNode{}
	admit := func(key string) {
		if !strings.Contains(key, " ") {
			multi := false
			for single := range c.SingleTokens {
				if strings.Contains(key, single) {
					multi = true
					break
				}
			}
			if !multi {
				return
			}
		}
		root.insert(strings.ToUpper(key))
	}
	for k := range c.Keywords {
		admit(k)
	}
	for k := range c.Comments {
		admit(k)
	}
	for k := range c.Quotes {
		admit(k)
	}
	for k := range c.FormatStrings {
		admit(k)
	}
	return root
}

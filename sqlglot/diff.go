package sqlglot

import (
	"container/heap"
	"reflect"
)

// EditKind names which change a DiffEdit represents.
type EditKind string

const (
	EditInsert EditKind = "Insert"
	EditRemove EditKind = "Remove"
	EditMove   EditKind = "Move"
	EditUpdate EditKind = "Update"
	EditKeep   EditKind = "Keep"
)

// DiffEdit is one entry in an edit script -- the reference's Insert, Remove,
// Move, Update and Keep dataclasses collapsed into one shape. Insert and
// Remove carry only Expression; Move, Update and Keep carry Source and
// Target, the two ends of what got matched.
type DiffEdit struct {
	Kind       EditKind
	Expression *Expression
	Source     *Expression
	Target     *Expression
}

// diffUpdatableTypes are the classes an Update edit may name outright: a
// value on the node itself changed (a Literal's text, a Table's name) rather
// than its shape. Anything else that is not identical becomes an Insert
// paired with a Remove -- there is no smaller edit that says what changed.
var diffUpdatableTypes = map[string]bool{
	"Alias": true, "Boolean": true, "Column": true, "DataType": true,
	"Lambda": true, "Literal": true, "Table": true, "Window": true,
}

// diffIgnoredLeafTypes are excluded from matching entirely: an Identifier
// carries no meaning on its own that its parent (a Column, a Table) does
// not already carry in its own generated text, and matching them as their
// own leaves double-counts the same name.
var diffIgnoredLeafTypes = map[string]bool{"Identifier": true}

// Diff returns the edit script that turns source into target: the reference's
// own `sqlglot.diff.diff`, the Change Distiller algorithm (Fluri & Pinzger,
// building on Chawathe et al.) run over two already-parsed trees. Both trees
// are copied first, so the ones passed in are never mutated.
//
// deltaOnly excludes Keep edits, which is what the reference's own tests
// hold the algorithm to -- and what a caller comparing two versions of a
// query actually wants: everything that agreed contributes nothing to read.
func Diff(source, target *Expression, dialect string, deltaOnly bool) []DiffEdit {
	cd := &changeDistiller{f: 0.6, t: 0.6, dialect: dialect, bigramCache: map[*Expression]map[string]int{}}
	return cd.diff(source.Copy(), target.Copy(), deltaOnly)
}

type changeDistiller struct {
	f, t    float64
	dialect string

	source, target       *Expression
	sourceBFS, targetBFS []*Expression
	unmatchedSource      map[*Expression]bool
	unmatchedTarget      map[*Expression]bool
	bigramCache          map[*Expression]map[string]int
}

func (cd *changeDistiller) diff(source, target *Expression, deltaOnly bool) []DiffEdit {
	cd.source, cd.target = source, target
	cd.sourceBFS = filterIgnoredLeaves(bfsExpressions(source))
	cd.targetBFS = filterIgnoredLeaves(bfsExpressions(target))

	cd.unmatchedSource = map[*Expression]bool{}
	for _, n := range cd.sourceBFS {
		cd.unmatchedSource[n] = true
	}
	cd.unmatchedTarget = map[*Expression]bool{}
	for _, n := range cd.targetBFS {
		cd.unmatchedTarget[n] = true
	}

	matching := cd.computeMatchingSet()
	return cd.generateEditScript(matching, deltaOnly)
}

// bfsExpressions is the reference's own `Expr.bfs()`: a level-order walk
// over the args a node HOLDS (`iter_expressions`, which is `args.values()`
// and never the type annotation, unlike Dump's own traversal).
func bfsExpressions(root *Expression) []*Expression {
	var out []*Expression
	queue := []*Expression{root}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		out = append(out, n)
		for _, key := range n.Keys {
			switch v := n.Args[key].(type) {
			case *Expression:
				if v != nil {
					queue = append(queue, v)
				}
			case []*Expression:
				for _, c := range v {
					if c != nil {
						queue = append(queue, c)
					}
				}
			}
		}
	}
	return out
}

func filterIgnoredLeaves(nodes []*Expression) []*Expression {
	out := make([]*Expression, 0, len(nodes))
	for _, n := range nodes {
		if !diffIgnoredLeafTypes[n.Class] {
			out = append(out, n)
		}
	}
	return out
}

// directChildren is `iter_expressions`: a node's own *Expression/[]*Expression
// args, in declared order, skipping nil.
func directChildren(e *Expression) []*Expression {
	var out []*Expression
	for _, key := range e.Keys {
		switch v := e.Args[key].(type) {
		case *Expression:
			if v != nil {
				out = append(out, v)
			}
		case []*Expression:
			for _, c := range v {
				if c != nil {
					out = append(out, c)
				}
			}
		}
	}
	return out
}

// expressionOnlyArgs is `_expression_only_args`: directChildren with
// Identifiers dropped, the same exclusion diffIgnoredLeafTypes names
// everywhere else.
func expressionOnlyArgs(e *Expression) []*Expression {
	var out []*Expression
	for _, c := range directChildren(e) {
		if !diffIgnoredLeafTypes[c.Class] {
			out = append(out, c)
		}
	}
	return out
}

// expressionLeaves is `_get_expression_leaves`: a node with no non-Identifier
// child expression IS a leaf; otherwise its leaves are its children's.
func expressionLeaves(e *Expression) []*Expression {
	var out []*Expression
	hasChildExprs := false
	for _, c := range directChildren(e) {
		if diffIgnoredLeafTypes[c.Class] {
			continue
		}
		hasChildExprs = true
		out = append(out, expressionLeaves(c)...)
	}
	if !hasChildExprs {
		out = append(out, e)
	}
	return out
}

// nonExpressionLeaf is one (key, value) pair from `_get_non_expression_leaves`:
// an arg whose value is neither an *Expression nor a list of them -- a bare
// string, a bool, a number, a []string. Two nodes with the same shape but
// different values here are the same STRUCTURE holding different data, which
// is exactly what an Update edit means.
type nonExpressionLeaf struct {
	key   string
	value any
}

func nonExpressionLeaves(e *Expression) []nonExpressionLeaf {
	var out []nonExpressionLeaf
	for _, key := range e.Keys {
		v := e.Args[key]
		if v == nil {
			continue
		}
		switch vv := v.(type) {
		case *Expression:
			continue
		case []*Expression:
			continue
		case []string:
			out = append(out, nonExpressionLeaf{key, vv})
		default:
			out = append(out, nonExpressionLeaf{key, vv})
		}
	}
	return out
}

func nonExpressionLeavesEqual(a, b []nonExpressionLeaf) bool {
	if len(a) != len(b) {
		return false
	}
	am := make(map[string]any, len(a))
	for _, l := range a {
		am[l.key] = l.value
	}
	for _, l := range b {
		v, ok := am[l.key]
		if !ok || !reflect.DeepEqual(v, l.value) {
			return false
		}
	}
	return true
}

// isSameType is `_is_same_type`: the same Go Class, with two classes needing
// more than that -- a Join also needs the same side (LEFT/RIGHT/full outer,
// which changes what the join MEANS), and an Anonymous call also needs the
// same function name (its only distinguishing feature; two different
// functions both called through Anonymous are not a "same node, different
// value" Update).
func isSameType(source, target *Expression) bool {
	if source.Class != target.Class {
		return false
	}
	switch source.Class {
	case "Join":
		return argEqual(source.Args["side"], target.Args["side"])
	case "Anonymous":
		return argEqual(source.Args["this"], target.Args["this"])
	}
	return true
}

// argEqual compares one arg value the way two nodes' OWN content is
// compared, not the way reflect.DeepEqual would: an *Expression carries a
// Parent pointer back into its tree, so DeepEqual-ing two of them recurses
// into ancestor context that has nothing to do with what the arg itself
// holds -- Expression.Equal exists precisely to stop there. Anything else
// (a string, a bool, a []string) has no such pointer and DeepEqual is exact.
func argEqual(a, b any) bool {
	ae, aOK := a.(*Expression)
	be, bOK := b.(*Expression)
	if aOK || bOK {
		return aOK && bOK && ae.Equal(be)
	}
	return reflect.DeepEqual(a, b)
}

// parentSimilarityScore is `_parent_similarity_score`: how far up the tree
// source and target's ancestors keep matching in TYPE, both sides at once --
// a tiebreaker between two leaf matches with the same text similarity, in
// favor of the one sitting in the more similar place.
func parentSimilarityScore(source, target *Expression) int {
	if source == nil || target == nil || source.Class != target.Class {
		return 0
	}
	return 1 + parentSimilarityScore(source.Parent, target.Parent)
}

// bigramHisto is `_bigram_histo`: the reference generates each node's own
// SQL text and counts every overlapping 2-character run in it, which is what
// diceCoefficient compares -- a similarity measure over SPELLING rather than
// structure, cheap to compute and forgiving of the kind of small edit this
// algorithm exists to describe.
func (cd *changeDistiller) bigramHisto(e *Expression) map[string]int {
	if h, ok := cd.bigramCache[e]; ok {
		return h
	}
	text, err := Generate(e, cd.dialect)
	if err != nil {
		text = ""
	}
	histo := map[string]int{}
	runes := []rune(text)
	for i := 0; i+1 < len(runes); i++ {
		histo[string(runes[i:i+2])]++
	}
	cd.bigramCache[e] = histo
	return histo
}

func (cd *changeDistiller) diceCoefficient(source, target *Expression) float64 {
	sourceHisto := cd.bigramHisto(source)
	targetHisto := cd.bigramHisto(target)
	total := 0
	for _, c := range sourceHisto {
		total += c
	}
	for _, c := range targetHisto {
		total += c
	}
	if total == 0 {
		if source.Equal(target) {
			return 1.0
		}
		return 0.0
	}
	overlap := 0
	for g, sc := range sourceHisto {
		if tc, ok := targetHisto[g]; ok {
			if sc < tc {
				overlap += sc
			} else {
				overlap += tc
			}
		}
	}
	return 2 * float64(overlap) / float64(total)
}

// leafCandidate is one entry the leaf-matching heap orders by: highest
// similarity first, ties broken by parent similarity, ties broken by
// insertion order -- the reference's own tuple ordering, `heapq` needing a
// total order and neither Expr having one of its own.
type leafCandidate struct {
	negScore       float64
	negParentScore int
	order          int
	source, target *Expression
}

type leafHeap []leafCandidate

func (h leafHeap) Len() int { return len(h) }
func (h leafHeap) Less(i, j int) bool {
	if h[i].negScore != h[j].negScore {
		return h[i].negScore < h[j].negScore
	}
	if h[i].negParentScore != h[j].negParentScore {
		return h[i].negParentScore < h[j].negParentScore
	}
	return h[i].order < h[j].order
}
func (h leafHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *leafHeap) Push(x any)   { *h = append(*h, x.(leafCandidate)) }
func (h *leafHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}

// computeLeafMatchingSet is `_compute_leaf_matching_set`: every same-typed
// leaf pair whose text similarity clears `f` is a CANDIDATE match; the
// candidates are then taken best-first, each committed only if both its
// leaves are still unmatched -- a greedy matching, not an optimal one, which
// is the algorithm's own tradeoff for staying fast on a real query's tree.
func (cd *changeDistiller) computeLeafMatchingSet() map[*Expression]*Expression {
	sourceLeaves := expressionLeaves(cd.source)
	targetLeaves := expressionLeaves(cd.target)

	var candidates leafHeap
	order := 0
	for _, sl := range sourceLeaves {
		for _, tl := range targetLeaves {
			if !isSameType(sl, tl) {
				continue
			}
			score := cd.diceCoefficient(sl, tl)
			if score >= cd.f {
				heap.Push(&candidates, leafCandidate{-score, -parentSimilarityScore(sl, tl), order, sl, tl})
				order++
			}
		}
	}

	matching := map[*Expression]*Expression{}
	for candidates.Len() > 0 {
		c := heap.Pop(&candidates).(leafCandidate)
		if cd.unmatchedSource[c.source] && cd.unmatchedTarget[c.target] {
			matching[c.source] = c.target
			delete(cd.unmatchedSource, c.source)
			delete(cd.unmatchedTarget, c.target)
		}
	}
	return matching
}

// computeMatchingSet is `_compute_matching_set`: leaves first, then every
// remaining unmatched pair of the SAME type is tried by how much of its own
// leaf set already matched (>= 0.8 alone is enough; otherwise a looser leaf
// threshold PLUS a Dice-coefficient check over the two subtrees' own text).
// `adjustedT` drops the leaf threshold for small subtrees, where four or
// fewer leaves make the ordinary threshold too strict to ever pass.
func (cd *changeDistiller) computeMatchingSet() map[*Expression]*Expression {
	leavesMatching := cd.computeLeafMatchingSet()
	matching := map[*Expression]*Expression{}
	for s, t := range leavesMatching {
		matching[s] = t
	}

	orderedSource := make([]*Expression, 0, len(cd.unmatchedSource))
	for _, n := range cd.sourceBFS {
		if cd.unmatchedSource[n] {
			orderedSource = append(orderedSource, n)
		}
	}
	orderedTarget := make([]*Expression, 0, len(cd.unmatchedTarget))
	targetStillIn := map[*Expression]bool{}
	for _, n := range cd.targetBFS {
		if cd.unmatchedTarget[n] {
			orderedTarget = append(orderedTarget, n)
			targetStillIn[n] = true
		}
	}

	for _, sourceNode := range orderedSource {
		for _, targetNode := range orderedTarget {
			if !targetStillIn[targetNode] {
				continue
			}
			if !isSameType(sourceNode, targetNode) {
				continue
			}
			sourceLeafIDs := leafSet(expressionLeaves(sourceNode))
			targetLeafIDs := leafSet(expressionLeaves(targetNode))
			maxLeaves := len(sourceLeafIDs)
			if len(targetLeafIDs) > maxLeaves {
				maxLeaves = len(targetLeafIDs)
			}
			var leafSimilarity float64
			if maxLeaves > 0 {
				common := 0
				for s, t := range leavesMatching {
					if sourceLeafIDs[s] && targetLeafIDs[t] {
						common++
					}
				}
				leafSimilarity = float64(common) / float64(maxLeaves)
			}
			adjustedT := cd.t
			minLeaves := len(sourceLeafIDs)
			if len(targetLeafIDs) < minLeaves {
				minLeaves = len(targetLeafIDs)
			}
			if minLeaves <= 4 {
				adjustedT = 0.4
			}
			if leafSimilarity >= 0.8 ||
				(leafSimilarity >= adjustedT && cd.diceCoefficient(sourceNode, targetNode) >= cd.f) {
				matching[sourceNode] = targetNode
				delete(cd.unmatchedSource, sourceNode)
				delete(cd.unmatchedTarget, targetNode)
				delete(targetStillIn, targetNode)
				break
			}
		}
	}
	return matching
}

func leafSet(leaves []*Expression) map[*Expression]bool {
	out := make(map[*Expression]bool, len(leaves))
	for _, l := range leaves {
		out[l] = true
	}
	return out
}

// generateEditScript is `_generate_edit_script`: everything left unmatched is
// a plain Remove or Insert; everything matched is at minimum a Keep, or an
// Update when its own scalar args differ, or -- for a node NOT in
// diffUpdatableTypes -- always an Update, since there is no smaller edit for
// a shape change than "this became that". A pair of IDENTICAL matched nodes
// that no longer sit under correspondingly-matched parents is a Move: the
// same value, read from a different place in the tree.
func (cd *changeDistiller) generateEditScript(matching map[*Expression]*Expression, deltaOnly bool) []DiffEdit {
	var out []DiffEdit
	for _, n := range cd.sourceBFS {
		if cd.unmatchedSource[n] {
			out = append(out, DiffEdit{Kind: EditRemove, Expression: n})
		}
	}
	for _, n := range cd.targetBFS {
		if cd.unmatchedTarget[n] {
			out = append(out, DiffEdit{Kind: EditInsert, Expression: n})
		}
	}

	for _, sourceNode := range cd.sourceBFS {
		targetNode, ok := matching[sourceNode]
		if !ok {
			continue
		}
		identical := sourceNode.Equal(targetNode)
		if !diffUpdatableTypes[sourceNode.Class] || identical {
			if identical {
				sp, tp := sourceNode.Parent, targetNode.Parent
				switch {
				case sp != nil && tp == nil, sp == nil && tp != nil:
					out = append(out, DiffEdit{Kind: EditMove, Source: sourceNode, Target: targetNode})
				case sp != nil && tp != nil && matching[sp] != tp:
					out = append(out, DiffEdit{Kind: EditMove, Source: sourceNode, Target: targetNode})
				}
			} else {
				out = append(out, cd.generateMoveEdits(sourceNode, targetNode, matching)...)
			}

			sourceLeaves := nonExpressionLeaves(sourceNode)
			targetLeaves := nonExpressionLeaves(targetNode)
			if !nonExpressionLeavesEqual(sourceLeaves, targetLeaves) {
				out = append(out, DiffEdit{Kind: EditUpdate, Source: sourceNode, Target: targetNode})
			} else if !deltaOnly {
				out = append(out, DiffEdit{Kind: EditKeep, Source: sourceNode, Target: targetNode})
			}
		} else {
			out = append(out, DiffEdit{Kind: EditUpdate, Source: sourceNode, Target: targetNode})
		}
	}
	return out
}

// generateMoveEdits is `_generate_move_edits`: among a matched pair's own
// direct children, whichever ones are NOT part of the longest common
// subsequence (by whether a child's match partner sits at the corresponding
// position) moved -- reordered arguments, not a value change.
func (cd *changeDistiller) generateMoveEdits(source, target *Expression, matching map[*Expression]*Expression) []DiffEdit {
	sourceArgs := expressionOnlyArgs(source)
	targetArgs := expressionOnlyArgs(target)

	lcs := lcsExpressions(sourceArgs, targetArgs, matching)
	inLCS := make(map[*Expression]bool, len(lcs))
	for _, e := range lcs {
		inLCS[e] = true
	}

	var out []DiffEdit
	for _, a := range sourceArgs {
		if !inLCS[a] && !cd.unmatchedSource[a] {
			out = append(out, DiffEdit{Kind: EditMove, Source: a, Target: matching[a]})
		}
	}
	return out
}

// lcsExpressions is `_lcs`, specialised to a source arg matching a target
// arg exactly where `matching` says it does -- the standard O(len*len)
// dynamic-programming longest common subsequence.
func lcsExpressions(a, b []*Expression, matching map[*Expression]*Expression) []*Expression {
	la, lb := len(a), len(b)
	table := make([][][]*Expression, la+1)
	for i := range table {
		table[i] = make([][]*Expression, lb+1)
	}
	for i := 0; i <= la; i++ {
		for j := 0; j <= lb; j++ {
			switch {
			case i == 0 || j == 0:
				table[i][j] = nil
			case matching[a[i-1]] == b[j-1]:
				table[i][j] = append(append([]*Expression(nil), table[i-1][j-1]...), a[i-1])
			default:
				if len(table[i-1][j]) > len(table[i][j-1]) {
					table[i][j] = table[i-1][j]
				} else {
					table[i][j] = table[i][j-1]
				}
			}
		}
	}
	return table[la][lb]
}

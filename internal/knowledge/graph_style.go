package knowledge

import (
	"fmt"
	"math"

	"github.com/google/uuid"
)

const (
	// RootWeight is the weight of every root node (no primary parent).
	RootWeight = 11
	// MinWeight and MaxWeight bound the weights of all non-root nodes.
	MinWeight = 1
	MaxWeight = 10
	// DefaultColor is used for nodes that are not reachable from a root.
	DefaultColor = "#6366f1"
)

// AnnotateGraph sets Weight and Color of every node of g.
//
// Weight: roots get 11. All other nodes are scaled linearly between 1 (fewest
// recursive primary-parent descendants) and 10 (most) and rounded. If all
// non-root nodes have the same count they all get 1.
//
// Color: the root has t=0.5 and width w=1. The children of a node split the
// hue range [t-w/2, t+w/2] of their parent into equal parts (in node order);
// the color of a node is rainbow(t).
func AnnotateGraph(g *Graph) {
	nodes := g.Elements.Nodes
	index := make(map[uuid.UUID]int, len(nodes))
	for i := range nodes {
		index[nodes[i].Data.ID] = i
	}
	children := make([][]int, len(nodes))
	var roots []int
	for i := range nodes {
		p := nodes[i].Data.Parent
		if p == nil {
			roots = append(roots, i)
			continue
		}
		pi, ok := index[*p]
		if !ok {
			roots = append(roots, i)
			continue
		}
		children[pi] = append(children[pi], i)
	}

	for i := range nodes {
		nodes[i].Data.Weight = MinWeight
		nodes[i].Data.Color = DefaultColor
	}

	// Iterative traversal from the roots (also guards against cycles, which
	// are then simply never reached).
	type item struct {
		idx  int
		t, w float64
	}
	counts := make([]int, len(nodes))
	visited := make([]bool, len(nodes))
	var order []int
	var stack []item
	for _, r := range roots {
		stack = append(stack, item{r, 0.5, 1})
	}
	for len(stack) > 0 {
		it := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if visited[it.idx] {
			continue
		}
		visited[it.idx] = true
		order = append(order, it.idx)
		nodes[it.idx].Data.Color = hexColor(rainbow(it.t))
		kids := children[it.idx]
		if len(kids) == 0 {
			continue
		}
		cw := it.w / float64(len(kids))
		for k, c := range kids {
			stack = append(stack, item{c, it.t - it.w/2 + cw*(float64(k)+0.5), cw})
		}
	}

	// Recursive child counts: children are always visited after their parent,
	// so walking the order backwards accumulates bottom-up.
	parentOf := func(i int) (int, bool) {
		p := nodes[i].Data.Parent
		if p == nil {
			return 0, false
		}
		pi, ok := index[*p]
		return pi, ok
	}
	for k := len(order) - 1; k >= 0; k-- {
		i := order[k]
		if pi, ok := parentOf(i); ok && visited[pi] {
			counts[pi] += counts[i] + 1
		}
	}

	isRoot := func(i int) bool { _, ok := parentOf(i); return !ok }
	lo, hi := math.MaxInt, -1
	for _, i := range order {
		if isRoot(i) {
			continue
		}
		lo, hi = min(lo, counts[i]), max(hi, counts[i])
	}
	for _, i := range order {
		switch {
		case isRoot(i):
			nodes[i].Data.Weight = RootWeight
		case hi == lo:
			nodes[i].Data.Weight = MinWeight
		default:
			f := float64(counts[i]-lo) / float64(hi-lo)
			nodes[i].Data.Weight = int(math.Round(MinWeight + f*(MaxWeight-MinWeight)))
		}
	}
}

// rainbow maps t (any real, taken modulo 1) onto the continuous hue wheel.
func rainbow(t float64) [3]float64 {
	hue := (math.Mod(math.Mod(t, 1)+1, 1)) * 6
	f := func(x float64) float64 {
		return math.Max(0, math.Min(1, math.Abs(math.Mod(x, 6)-3)-1))
	}
	return [3]float64{f(hue), f(hue + 4), f(hue + 2)}
}

func hexColor(c [3]float64) string {
	return fmt.Sprintf("#%02x%02x%02x",
		int(math.Round(c[0]*255)), int(math.Round(c[1]*255)), int(math.Round(c[2]*255)))
}

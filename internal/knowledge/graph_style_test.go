package knowledge

import (
	"testing"

	"github.com/google/uuid"
)

func TestAnnotateGraph(t *testing.T) {
	ids := make([]uuid.UUID, 5)
	for i := range ids {
		ids[i] = uuid.New()
	}
	// root -> node1 -> node2 -> node3 ; node1 -> node4
	parent := map[int]int{1: 0, 2: 1, 3: 2, 4: 1}
	g := Graph{}
	for i := range ids {
		d := GraphNodeData{ID: ids[i]}
		if p, ok := parent[i]; ok {
			d.Parent = &ids[p]
		}
		g.Elements.Nodes = append(g.Elements.Nodes, GraphNode{Data: d})
	}
	AnnotateGraph(&g)

	want := []int{11, 10, 4, 1, 1}
	for i, w := range want {
		if got := g.Elements.Nodes[i].Data.Weight; got != w {
			t.Errorf("node %d weight = %d, want %d", i, got, w)
		}
	}
	if c := g.Elements.Nodes[0].Data.Color; c != "#00ffff" {
		t.Errorf("root color = %s, want #00ffff", c)
	}
	// node1 is the only child of root (t=0.5, w=1); its children node2 and
	// node4 split that range: t=0.25 and t=0.75. node3 is node2's only child.
	if got, want := g.Elements.Nodes[4].Data.Color, hexColor(rainbow(0.75)); got != want {
		t.Errorf("node4 color = %s, want %s", got, want)
	}
}

func TestRainbow(t *testing.T) {
	cases := map[float64]string{0: "#ff0000", 1.0 / 3: "#00ff00", 2.0 / 3: "#0000ff", 1: "#ff0000"}
	for in, want := range cases {
		if got := hexColor(rainbow(in)); got != want {
			t.Errorf("rainbow(%v) = %s, want %s", in, got, want)
		}
	}
}

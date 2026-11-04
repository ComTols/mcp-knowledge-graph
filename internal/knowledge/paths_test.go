package knowledge_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mcp-knowledge-graph/internal/knowledge"
)

// graphBuilder builds AncestorGraphs with readable node names.
type graphBuilder struct {
	ids   map[string]uuid.UUID
	g     knowledge.AncestorGraph
	clock time.Time
}

func newGraph() *graphBuilder {
	b := &graphBuilder{
		ids:   map[string]uuid.UUID{"root": knowledge.RootID},
		g:     knowledge.AncestorGraph{Titles: map[uuid.UUID]string{knowledge.RootID: "root"}},
		clock: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	return b
}

func (b *graphBuilder) id(name string) uuid.UUID {
	if id, ok := b.ids[name]; ok {
		return id
	}
	id := uuid.New()
	b.ids[name] = id
	b.g.Titles[id] = name
	return id
}

// edge adds parent -> child; edges are created in call order.
func (b *graphBuilder) edge(child, parent string, primary bool) *graphBuilder {
	b.clock = b.clock.Add(time.Second)
	b.g.Edges = append(b.g.Edges, knowledge.ParentEdge{
		ChildID: b.id(child), ParentID: b.id(parent), IsPrimary: primary, CreatedAt: b.clock,
	})
	return b
}

func titles(p knowledge.Path) []string {
	out := make([]string, len(p))
	for i, e := range p {
		out[i] = e.Title
	}
	return out
}

func TestAllPathsCanonicalFirst(t *testing.T) {
	// root -> Minkter -> Backend -> Hardening
	// root -> AI -> Hardening (secondary, but older edge)
	b := newGraph().
		edge("Minkter", "root", true).
		edge("AI", "root", true).
		edge("Backend", "Minkter", true).
		edge("Hardening", "AI", false).
		edge("Hardening", "Backend", true)

	set, err := b.g.AllPaths(b.id("Hardening"), knowledge.PathLimits{})
	require.NoError(t, err)
	require.Len(t, set.Paths, 2)
	assert.False(t, set.Truncated)
	assert.Equal(t, []string{"root", "Minkter", "Backend", "Hardening"}, titles(set.Paths[0]))
	assert.Equal(t, []string{"root", "AI", "Hardening"}, titles(set.Paths[1]))
	for _, p := range set.Paths {
		assert.NoError(t, knowledge.ValidatePath(p, b.id("Hardening")))
	}
}

func TestAllPathsRootItself(t *testing.T) {
	set, err := newGraph().g.AllPaths(knowledge.RootID, knowledge.PathLimits{})
	require.NoError(t, err)
	require.Len(t, set.Paths, 1)
	assert.Equal(t, []string{"root"}, titles(set.Paths[0]))
}

func TestAllPathsTruncatesCombinatorialExplosion(t *testing.T) {
	// A ladder of diamonds: every level doubles the number of paths.
	b := newGraph()
	prev := "root"
	for i := range 8 {
		l, r, join := "L"+string(rune('0'+i)), "R"+string(rune('0'+i)), "J"+string(rune('0'+i))
		b.edge(l, prev, true).edge(r, prev, true).edge(join, l, true).edge(join, r, false)
		prev = join
	}
	set, err := b.g.AllPaths(b.id(prev), knowledge.PathLimits{MaxPaths: 5})
	require.NoError(t, err)
	assert.Len(t, set.Paths, 5)
	assert.True(t, set.Truncated)
	for _, p := range set.Paths {
		assert.NoError(t, knowledge.ValidatePath(p, b.id(prev)))
	}
}

func TestAllPathsDepthLimit(t *testing.T) {
	b := newGraph()
	prev := "root"
	for i := range 10 {
		name := "N" + string(rune('a'+i))
		b.edge(name, prev, true)
		prev = name
	}
	_, err := b.g.AllPaths(b.id(prev), knowledge.PathLimits{MaxDepth: 5})
	assert.ErrorIs(t, err, knowledge.ErrIntegrityViolation, "no path within the depth limit")

	set, err := b.g.AllPaths(b.id(prev), knowledge.PathLimits{MaxDepth: 10})
	require.NoError(t, err)
	assert.Len(t, set.Paths[0], 11)
}

func TestAllPathsCycleProtection(t *testing.T) {
	// Corrupt data: A <-> B cycle, but A is also attached to root.
	b := newGraph().
		edge("A", "B", true).
		edge("B", "A", true).
		edge("A", "root", false)

	set, err := b.g.AllPaths(b.id("B"), knowledge.PathLimits{})
	require.NoError(t, err)
	require.Len(t, set.Paths, 1)
	assert.Equal(t, []string{"root", "A", "B"}, titles(set.Paths[0]))
}

func TestAllPathsDetectsOrphans(t *testing.T) {
	b := newGraph().edge("child", "orphan", true)
	_, err := b.g.AllPaths(b.id("child"), knowledge.PathLimits{})
	assert.ErrorIs(t, err, knowledge.ErrIntegrityViolation)

	_, err = b.g.AllPaths(uuid.New(), knowledge.PathLimits{})
	assert.ErrorIs(t, err, knowledge.ErrNotFound)
}

func TestValidatePath(t *testing.T) {
	doc := uuid.New()
	root := knowledge.PathElement{ID: knowledge.RootID, Title: "root"}
	leaf := knowledge.PathElement{ID: doc, Title: "doc"}

	assert.NoError(t, knowledge.ValidatePath(knowledge.Path{root, leaf}, doc))
	assert.ErrorIs(t, knowledge.ValidatePath(nil, doc), knowledge.ErrIntegrityViolation)
	assert.ErrorIs(t, knowledge.ValidatePath(knowledge.Path{leaf}, doc), knowledge.ErrIntegrityViolation)
	assert.ErrorIs(t, knowledge.ValidatePath(knowledge.Path{root}, doc), knowledge.ErrIntegrityViolation)
}

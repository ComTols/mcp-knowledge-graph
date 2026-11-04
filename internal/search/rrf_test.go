package search_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mcp-knowledge-graph/internal/search"
)

func TestFuseScoresAndDeduplicates(t *testing.T) {
	a, b, c, d := uuid.New(), uuid.New(), uuid.New(), uuid.New()

	got := search.Fuse(60,
		search.RankedList{Name: "doc", IDs: []uuid.UUID{a, b, c}},
		search.RankedList{Name: "block", IDs: []uuid.UUID{b, b, d}}, // duplicate b is ignored
		search.RankedList{Name: "lexical", IDs: []uuid.UUID{c}},
	)

	require.Len(t, got, 4, "each document appears exactly once")
	assert.Equal(t, b, got[0].ID)
	assert.InDelta(t, 1.0/62+1.0/61, got[0].Score, 1e-12)
	assert.Equal(t, c, got[1].ID)
	assert.InDelta(t, 1.0/63+1.0/61, got[1].Score, 1e-12)
	assert.Equal(t, a, got[2].ID)
	assert.Equal(t, d, got[3].ID)
	assert.InDelta(t, 1.0/62, got[3].Score, 1e-12, "d is at rank 2 after removing the duplicate")
}

func TestFuseWeightsAndDefaults(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	got := search.Fuse(0, // default k
		search.RankedList{IDs: []uuid.UUID{a, b}},
		search.RankedList{Weight: 3, IDs: []uuid.UUID{b}},
	)
	require.Len(t, got, 2)
	assert.Equal(t, b, got[0].ID)
	assert.InDelta(t, 1.0/62+3.0/61, got[0].Score, 1e-12)
}

func TestFuseIsDeterministicOnTies(t *testing.T) {
	ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	// Every ID is at rank 1 of its own list -> identical scores.
	lists := make([]search.RankedList, len(ids))
	for i, id := range ids {
		lists[i] = search.RankedList{IDs: []uuid.UUID{id}}
	}
	first := search.Fuse(60, lists...)
	for range 20 {
		assert.Equal(t, first, search.Fuse(60, lists...))
	}
	for i := 1; i < len(first); i++ {
		assert.Negative(t, compareUUID(first[i-1].ID, first[i].ID))
	}
}

func TestFuseEmpty(t *testing.T) {
	assert.Empty(t, search.Fuse(60))
	assert.Empty(t, search.Fuse(60, search.RankedList{}))
}

func TestTopIDs(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	scored := []search.Scored{{ID: a}, {ID: b}}
	assert.Equal(t, []uuid.UUID{a}, search.TopIDs(scored, 1))
	assert.Equal(t, []uuid.UUID{a, b}, search.TopIDs(scored, 10))
	assert.Empty(t, search.TopIDs(nil, 10))
}

func compareUUID(a, b uuid.UUID) int {
	for i := range a {
		if a[i] != b[i] {
			return int(a[i]) - int(b[i])
		}
	}
	return 0
}

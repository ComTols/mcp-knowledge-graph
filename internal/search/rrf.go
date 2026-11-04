// Package search contains the storage independent ranking logic of the
// hybrid search, most notably Reciprocal Rank Fusion (RRF).
package search

import (
	"bytes"
	"slices"

	"github.com/google/uuid"
)

// DefaultRRFK is the rank constant k of the RRF formula 1 / (k + rank).
const DefaultRRFK = 60

// RankedList is one candidate list, ordered by descending relevance. IDs must
// be unique within a list; duplicates after the first occurrence are ignored.
type RankedList struct {
	Name string
	// Weight scales the contribution of this list. Zero means 1.
	Weight float64
	IDs    []uuid.UUID
}

// Scored is a fused result. Score is internal and used only for ordering.
type Scored struct {
	ID    uuid.UUID
	Score float64
}

// Fuse merges the lists with Reciprocal Rank Fusion:
//
//	score(d) = Σ weight_i / (k + rank_i(d))
//
// with 1-based ranks. Every document appears exactly once in the output,
// ordered by descending score. Ties are broken by the number of lists that
// contain the document, then by ID, so the result is fully deterministic.
func Fuse(k int, lists ...RankedList) []Scored {
	if k <= 0 {
		k = DefaultRRFK
	}
	type acc struct {
		score float64
		hits  int
	}
	scores := make(map[uuid.UUID]*acc)
	order := make([]uuid.UUID, 0)

	for _, l := range lists {
		w := l.Weight
		if w == 0 {
			w = 1
		}
		seen := make(map[uuid.UUID]struct{}, len(l.IDs))
		rank := 0
		for _, id := range l.IDs {
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			rank++
			a, ok := scores[id]
			if !ok {
				a = &acc{}
				scores[id] = a
				order = append(order, id)
			}
			a.score += w / float64(k+rank)
			a.hits++
		}
	}

	out := make([]Scored, 0, len(order))
	for _, id := range order {
		out = append(out, Scored{ID: id, Score: scores[id].score})
	}
	slices.SortFunc(out, func(a, b Scored) int {
		switch {
		case a.Score > b.Score:
			return -1
		case a.Score < b.Score:
			return 1
		}
		if ha, hb := scores[a.ID].hits, scores[b.ID].hits; ha != hb {
			return hb - ha
		}
		return bytes.Compare(a.ID[:], b.ID[:])
	})
	return out
}

// TopIDs returns the IDs of the first n entries of scored.
func TopIDs(scored []Scored, n int) []uuid.UUID {
	if n > len(scored) {
		n = len(scored)
	}
	ids := make([]uuid.UUID, n)
	for i := range n {
		ids[i] = scored[i].ID
	}
	return ids
}

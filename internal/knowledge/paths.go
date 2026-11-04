package knowledge

import (
	"bytes"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
)

// Default limits for path resolution.
const (
	DefaultMaxPaths = 10
	DefaultMaxDepth = 64
)

// ParentEdge is a directed parent relation used for path enumeration.
type ParentEdge struct {
	ChildID   uuid.UUID
	ParentID  uuid.UUID
	IsPrimary bool
	CreatedAt time.Time
}

// AncestorGraph is the subgraph of all ancestors of a document, as loaded by
// a storage adapter. Titles must contain every node referenced by Edges.
type AncestorGraph struct {
	Titles map[uuid.UUID]string
	Edges  []ParentEdge
}

// PathLimits bounds the enumeration of alternative root paths.
type PathLimits struct {
	MaxPaths int // maximum number of returned paths
	MaxDepth int // maximum number of edges in a single path
}

func (l PathLimits) withDefaults() PathLimits {
	if l.MaxPaths <= 0 {
		l.MaxPaths = DefaultMaxPaths
	}
	if l.MaxDepth <= 0 {
		l.MaxDepth = DefaultMaxDepth
	}
	return l
}

// AllPaths enumerates root paths of id in a deterministic order: at every
// node the primary parent is visited first, the remaining parents by edge age
// and ID. The first path is therefore the canonical primary-parent path.
//
// Enumeration is bounded by limits; if paths were omitted the returned set is
// marked as truncated. Cycles in the data are never followed. A non-root node
// without parents is reported as ErrIntegrityViolation.
func (g AncestorGraph) AllPaths(id uuid.UUID, limits PathLimits) (PathSet, error) {
	limits = limits.withDefaults()
	if _, ok := g.Titles[id]; !ok {
		return PathSet{}, fmt.Errorf("%w: document %s", ErrNotFound, id)
	}

	parents := make(map[uuid.UUID][]ParentEdge)
	for _, e := range g.Edges {
		parents[e.ChildID] = append(parents[e.ChildID], e)
	}
	for child := range parents {
		slices.SortFunc(parents[child], compareEdges)
	}

	e := pathEnumerator{
		g:       g,
		parents: parents,
		limits:  limits,
		onPath:  make(map[uuid.UUID]bool),
	}
	if err := e.walk(id); err != nil {
		return PathSet{}, err
	}
	if len(e.found) == 0 {
		return PathSet{}, fmt.Errorf("%w: document %s is not reachable from root", ErrIntegrityViolation, id)
	}
	return PathSet{Paths: e.found, Truncated: e.truncated}, nil
}

// compareEdges orders the primary edge first, then by creation time and ID.
func compareEdges(a, b ParentEdge) int {
	if a.IsPrimary != b.IsPrimary {
		if a.IsPrimary {
			return -1
		}
		return 1
	}
	if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
		return c
	}
	return bytes.Compare(a.ParentID[:], b.ParentID[:])
}

type pathEnumerator struct {
	g         AncestorGraph
	parents   map[uuid.UUID][]ParentEdge
	limits    PathLimits
	onPath    map[uuid.UUID]bool
	stack     []uuid.UUID // from the document upwards
	found     []Path
	truncated bool
}

// walk performs a depth first search towards root. It stops as soon as more
// paths than allowed have been seen.
func (e *pathEnumerator) walk(id uuid.UUID) error {
	if e.truncated {
		return nil
	}
	e.stack = append(e.stack, id)
	e.onPath[id] = true
	defer func() {
		e.stack = e.stack[:len(e.stack)-1]
		delete(e.onPath, id)
	}()

	if id == RootID {
		if len(e.found) == e.limits.MaxPaths {
			e.truncated = true
			return nil
		}
		e.found = append(e.found, e.buildPath())
		return nil
	}

	edges := e.parents[id]
	if len(edges) == 0 {
		return fmt.Errorf("%w: document %s has no parent", ErrIntegrityViolation, id)
	}
	if len(e.stack) > e.limits.MaxDepth {
		e.truncated = true
		return nil
	}
	for _, edge := range edges {
		if e.onPath[edge.ParentID] {
			continue // cycle protection
		}
		if err := e.walk(edge.ParentID); err != nil {
			return err
		}
	}
	return nil
}

func (e *pathEnumerator) buildPath() Path {
	p := make(Path, 0, len(e.stack))
	for i := len(e.stack) - 1; i >= 0; i-- {
		id := e.stack[i]
		p = append(p, PathElement{ID: id, Title: e.g.Titles[id]})
	}
	return p
}

// ValidatePath checks that p starts at root and ends at documentID.
func ValidatePath(p Path, documentID uuid.UUID) error {
	if len(p) == 0 {
		return fmt.Errorf("%w: empty path for document %s", ErrIntegrityViolation, documentID)
	}
	if p[0].ID != RootID {
		return fmt.Errorf("%w: path of document %s does not start at root (starts at %s)",
			ErrIntegrityViolation, documentID, p[0].ID)
	}
	if p[len(p)-1].ID != documentID {
		return fmt.Errorf("%w: path does not end at document %s", ErrIntegrityViolation, documentID)
	}
	return nil
}

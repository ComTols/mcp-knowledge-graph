package database

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"mcp-knowledge-graph/internal/knowledge"
)

// GetPaths resolves the canonical root path (primary parents) or, with
// allPaths, a bounded set of root paths with the canonical one first.
func (s *Store) GetPaths(ctx context.Context, documentID uuid.UUID, allPaths bool) (knowledge.PathSet, error) {
	var out knowledge.PathSet
	err := s.inReadTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = s.resolvePaths(ctx, tx, documentID, allPaths)
		return err
	})
	return out, err
}

func (s *Store) resolvePaths(ctx context.Context, q querier, id uuid.UUID, allPaths bool) (knowledge.PathSet, error) {
	if !allPaths {
		p, err := canonicalPath(ctx, q, id, s.pathLimits.MaxDepth)
		if err != nil {
			return knowledge.PathSet{}, err
		}
		return knowledge.PathSet{Paths: []knowledge.Path{p}}, nil
	}
	g, err := loadAncestorGraph(ctx, q, id)
	if err != nil {
		return knowledge.PathSet{}, err
	}
	set, err := g.AllPaths(id, s.pathLimits)
	if err != nil {
		return knowledge.PathSet{}, err
	}
	for _, p := range set.Paths {
		if err := knowledge.ValidatePath(p, id); err != nil {
			return knowledge.PathSet{}, err
		}
	}
	return set, nil
}

// canonicalPath follows the primary parent chain up to root with a bounded
// recursive CTE and returns the path in root-to-document order.
func canonicalPath(ctx context.Context, q querier, id uuid.UUID, maxDepth int) (knowledge.Path, error) {
	if maxDepth <= 0 {
		maxDepth = knowledge.DefaultMaxDepth
	}
	rows, err := q.Query(ctx, `
		WITH RECURSIVE ancestry AS (
			SELECT d.id, d.title, 0 AS depth, ARRAY[d.id] AS visited
			FROM documents d
			WHERE d.id = $1
			UNION ALL
			SELECT p.id, p.title, a.depth + 1, a.visited || p.id
			FROM ancestry a
			JOIN document_parents dp ON dp.child_id = a.id AND dp.is_primary
			JOIN documents p ON p.id = dp.parent_id
			WHERE NOT p.id = ANY (a.visited)
			  AND a.depth < $2
		)
		SELECT id, title FROM ancestry ORDER BY depth DESC`, id, maxDepth)
	if err != nil {
		return nil, fmt.Errorf("resolve path of %s: %w", id, err)
	}
	path, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (knowledge.PathElement, error) {
		var e knowledge.PathElement
		err := r.Scan(&e.ID, &e.Title)
		return e, err
	})
	if err != nil {
		return nil, fmt.Errorf("resolve path of %s: %w", id, err)
	}
	if len(path) == 0 {
		return nil, fmt.Errorf("%w: document %s", knowledge.ErrNotFound, id)
	}
	if err := knowledge.ValidatePath(path, id); err != nil {
		return nil, err
	}
	return path, nil
}

// loadAncestorGraph loads all ancestors of id and the edges between them.
// UNION (instead of UNION ALL) makes the recursion terminate even if the
// stored data contained a cycle.
func loadAncestorGraph(ctx context.Context, q querier, id uuid.UUID) (knowledge.AncestorGraph, error) {
	rows, err := q.Query(ctx, `
		WITH RECURSIVE anc(id) AS (
			SELECT $1::uuid
			UNION
			SELECT dp.parent_id
			FROM document_parents dp
			JOIN anc ON dp.child_id = anc.id
		)
		SELECT d.id, d.title, dp.parent_id, dp.is_primary, dp.created_at
		FROM anc
		JOIN documents d ON d.id = anc.id
		LEFT JOIN document_parents dp ON dp.child_id = anc.id`, id)
	if err != nil {
		return knowledge.AncestorGraph{}, fmt.Errorf("load ancestors of %s: %w", id, err)
	}
	defer rows.Close()

	g := knowledge.AncestorGraph{Titles: make(map[uuid.UUID]string)}
	for rows.Next() {
		var (
			nodeID    uuid.UUID
			title     string
			parentID  *uuid.UUID
			isPrimary *bool
			createdAt *time.Time
		)
		if err := rows.Scan(&nodeID, &title, &parentID, &isPrimary, &createdAt); err != nil {
			return knowledge.AncestorGraph{}, fmt.Errorf("load ancestors of %s: %w", id, err)
		}
		g.Titles[nodeID] = title
		if parentID != nil {
			g.Edges = append(g.Edges, knowledge.ParentEdge{
				ChildID:   nodeID,
				ParentID:  *parentID,
				IsPrimary: *isPrimary,
				CreatedAt: *createdAt,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return knowledge.AncestorGraph{}, fmt.Errorf("load ancestors of %s: %w", id, err)
	}
	if len(g.Titles) == 0 {
		return knowledge.AncestorGraph{}, fmt.Errorf("%w: document %s", knowledge.ErrNotFound, id)
	}
	return g, nil
}

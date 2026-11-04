package database

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"mcp-knowledge-graph/internal/knowledge"
)

func requirePageLimit(limit int) error {
	if limit <= 0 {
		return fmt.Errorf("%w: page limit must be positive", knowledge.ErrInvalidInput)
	}
	return nil
}

// GetChildrenPage returns a page of direct children ordered by
// lower(title), id using keyset pagination.
func (s *Store) GetChildrenPage(ctx context.Context, documentID uuid.UUID, req knowledge.ChildrenPageRequest) (knowledge.ChildrenPage, error) {
	if err := requirePageLimit(req.Limit); err != nil {
		return knowledge.ChildrenPage{}, err
	}
	var out knowledge.ChildrenPage
	err := s.inReadTx(ctx, func(tx pgx.Tx) error {
		if err := requireDocument(ctx, tx, documentID); err != nil {
			return err
		}
		// Without a cursor, the keyset condition is skipped.
		var afterKey *string
		var afterID *uuid.UUID
		if req.After != nil {
			afterKey, afterID = &req.After.TitleKey, &req.After.ID
		}
		rows, err := tx.Query(ctx, `
			SELECT c.id, c.title, c.summary, dp.is_primary, lower(c.title)
			FROM document_parents dp
			JOIN documents c ON c.id = dp.child_id
			WHERE dp.parent_id = $1
			  AND ($2::text IS NULL OR (lower(c.title), c.id) > ($2::text, $3::uuid))
			ORDER BY lower(c.title), c.id
			LIMIT $4`, documentID, afterKey, afterID, req.Limit+1)
		if err != nil {
			return fmt.Errorf("get children of %s: %w", documentID, err)
		}
		type row struct {
			child knowledge.ChildRef
			key   string
		}
		page, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
			var x row
			err := r.Scan(&x.child.ID, &x.child.Title, &x.child.Summary, &x.child.IsPrimary, &x.key)
			return x, err
		})
		if err != nil {
			return fmt.Errorf("get children of %s: %w", documentID, err)
		}
		hasMore := len(page) > req.Limit
		if hasMore {
			page = page[:req.Limit]
		}
		out.Children = make([]knowledge.ChildRef, len(page))
		for i, x := range page {
			out.Children[i] = x.child
		}
		if hasMore {
			last := page[len(page)-1]
			out.Next = &knowledge.ChildrenCursor{TitleKey: last.key, ID: last.child.ID}
		}
		return nil
	})
	if err != nil {
		return knowledge.ChildrenPage{}, err
	}
	return out, nil
}

// GetSourcesPage returns a page of source references ordered by
// created_at, id using keyset pagination.
func (s *Store) GetSourcesPage(ctx context.Context, documentID uuid.UUID, req knowledge.SourcesPageRequest) (knowledge.SourcesPage, error) {
	if err := requirePageLimit(req.Limit); err != nil {
		return knowledge.SourcesPage{}, err
	}
	var out knowledge.SourcesPage
	err := s.inReadTx(ctx, func(tx pgx.Tx) error {
		if err := requireDocument(ctx, tx, documentID); err != nil {
			return err
		}
		var afterTime any
		var afterID *uuid.UUID
		if req.After != nil {
			afterTime, afterID = req.After.CreatedAt, &req.After.ID
		}
		rows, err := tx.Query(ctx, `
			SELECT id, kind, source_uri, title, created_at
			FROM document_sources
			WHERE document_id = $1
			  AND ($2::timestamptz IS NULL OR (created_at, id) > ($2::timestamptz, $3::uuid))
			ORDER BY created_at, id
			LIMIT $4`, documentID, afterTime, afterID, req.Limit+1)
		if err != nil {
			return fmt.Errorf("get sources of %s: %w", documentID, err)
		}
		page, err := pgx.CollectRows(rows, scanSource)
		if err != nil {
			return fmt.Errorf("get sources of %s: %w", documentID, err)
		}
		if len(page) > req.Limit {
			page = page[:req.Limit]
			last := page[len(page)-1]
			out.Next = &knowledge.SourcesCursor{CreatedAt: last.CreatedAt, ID: last.ID}
		}
		out.Sources = page
		return nil
	})
	if err != nil {
		return knowledge.SourcesPage{}, err
	}
	return out, nil
}

// GetFulltextPage returns a page of the ordered content blocks together
// with the document version, read from one snapshot. AfterBlockID must
// belong to the document, otherwise ErrInvalidInput is returned.
func (s *Store) GetFulltextPage(ctx context.Context, documentID uuid.UUID, req knowledge.BlockPageRequest) (knowledge.FulltextPage, error) {
	if err := requirePageLimit(req.Limit); err != nil {
		return knowledge.FulltextPage{}, err
	}
	var out knowledge.FulltextPage
	err := s.inReadTx(ctx, func(tx pgx.Tx) error {
		summary, err := getSummary(ctx, tx, documentID)
		if err != nil {
			return err
		}
		out.DocumentID, out.Version = summary.ID, summary.Version

		var afterPos *int64
		var afterID *uuid.UUID
		if req.AfterBlockID != nil {
			var pos int64
			err := tx.QueryRow(ctx,
				`SELECT position FROM content_blocks WHERE id = $1 AND document_id = $2`,
				*req.AfterBlockID, documentID).Scan(&pos)
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("%w: block %s does not belong to document %s",
					knowledge.ErrInvalidInput, *req.AfterBlockID, documentID)
			}
			if err != nil {
				return fmt.Errorf("load anchor block: %w", err)
			}
			afterPos, afterID = &pos, req.AfterBlockID
		}
		rows, err := tx.Query(ctx, `
			SELECT id, position, content, version
			FROM content_blocks
			WHERE document_id = $1
			  AND ($2::bigint IS NULL OR (position, id) > ($2::bigint, $3::uuid))
			ORDER BY position, id
			LIMIT $4`, documentID, afterPos, afterID, req.Limit+1)
		if err != nil {
			return fmt.Errorf("get blocks of %s: %w", documentID, err)
		}
		blocks, err := pgx.CollectRows(rows, scanBlock)
		if err != nil {
			return fmt.Errorf("get blocks of %s: %w", documentID, err)
		}
		if len(blocks) > req.Limit {
			blocks, out.HasMore = blocks[:req.Limit], true
		}
		out.Blocks = blocks
		return nil
	})
	if err != nil {
		return knowledge.FulltextPage{}, err
	}
	return out, nil
}

// Ping checks that the database is reachable.
func (s *Store) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

// GetParentList returns the direct parents (primary first) and the
// document version from one snapshot.
func (s *Store) GetParentList(ctx context.Context, documentID uuid.UUID) (knowledge.ParentList, error) {
	var out knowledge.ParentList
	err := s.inReadTx(ctx, func(tx pgx.Tx) error {
		summary, err := getSummary(ctx, tx, documentID)
		if err != nil {
			return err
		}
		out.DocumentID, out.Version = summary.ID, summary.Version
		out.Parents, err = getParents(ctx, tx, documentID)
		return err
	})
	if err != nil {
		return knowledge.ParentList{}, err
	}
	return out, nil
}

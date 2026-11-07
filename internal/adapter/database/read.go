package database

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"mcp-knowledge-graph/internal/knowledge"
)

// GetSummary returns id, title, summary and version of a document.
func (s *Store) GetSummary(ctx context.Context, documentID uuid.UUID) (knowledge.DocumentSummary, error) {
	return getSummary(ctx, s.pool, documentID)
}

// GetParents returns the direct parents, primary parent first.
func (s *Store) GetParents(ctx context.Context, documentID uuid.UUID) ([]knowledge.ParentRef, error) {
	var out []knowledge.ParentRef
	err := s.inReadTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = getParents(ctx, tx, documentID)
		return err
	})
	return out, err
}

// GetChildren returns the direct children with their current summaries,
// ordered by lower(title), id.
func (s *Store) GetChildren(ctx context.Context, documentID uuid.UUID) ([]knowledge.ChildRef, error) {
	var out []knowledge.ChildRef
	err := s.inReadTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = getChildren(ctx, tx, documentID)
		return err
	})
	return out, err
}

// GetFulltext returns the ordered content blocks of a document together
// with the document version (the ExpectedVersion for EditDocument). Child
// lists are never part of the stored full text.
func (s *Store) GetFulltext(ctx context.Context, documentID uuid.UUID) (knowledge.Fulltext, error) {
	var out knowledge.Fulltext
	err := s.inReadTx(ctx, func(tx pgx.Tx) error {
		summary, err := getSummary(ctx, tx, documentID)
		if err != nil {
			return err
		}
		out.DocumentID, out.Version = summary.ID, summary.Version
		out.Blocks, err = getBlocks(ctx, tx, documentID)
		return err
	})
	if err != nil {
		return knowledge.Fulltext{}, err
	}
	return out, nil
}

// GetSources returns the external source references of a document.
func (s *Store) GetSources(ctx context.Context, documentID uuid.UUID) ([]knowledge.Source, error) {
	var out []knowledge.Source
	err := s.inReadTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = getSources(ctx, tx, documentID)
		return err
	})
	return out, err
}

// GetDocument returns the aggregated view of a document from one snapshot.
func (s *Store) GetDocument(ctx context.Context, documentID uuid.UUID) (knowledge.Document, error) {
	var doc knowledge.Document
	err := s.inReadTx(ctx, func(tx pgx.Tx) error {
		var err error
		if doc.DocumentSummary, err = getSummary(ctx, tx, documentID); err != nil {
			return err
		}
		if doc.Parents, err = getParents(ctx, tx, documentID); err != nil {
			return err
		}
		if doc.Children, err = getChildren(ctx, tx, documentID); err != nil {
			return err
		}
		if doc.Blocks, err = getBlocks(ctx, tx, documentID); err != nil {
			return err
		}
		doc.Sources, err = getSources(ctx, tx, documentID)
		return err
	})
	if err != nil {
		return knowledge.Document{}, err
	}
	return doc, nil
}

// RenderDocument renders the deterministic Markdown view of a document (see
// knowledge.RenderViewerMarkdown). baseURL prefixes the parent and child
// links; empty yields root-relative links.
func (s *Store) RenderDocument(ctx context.Context, documentID uuid.UUID, baseURL string) (string, error) {
	doc, err := s.GetDocument(ctx, documentID)
	if err != nil {
		return "", err
	}
	return knowledge.RenderViewerMarkdown(doc, baseURL), nil
}

func getSummary(ctx context.Context, q querier, id uuid.UUID) (knowledge.DocumentSummary, error) {
	var d knowledge.DocumentSummary
	err := q.QueryRow(ctx,
		`SELECT id, title, summary, version FROM documents WHERE id = $1`, id,
	).Scan(&d.ID, &d.Title, &d.Summary, &d.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, fmt.Errorf("%w: document %s", knowledge.ErrNotFound, id)
	}
	if err != nil {
		return d, fmt.Errorf("get document %s: %w", id, err)
	}
	return d, nil
}

func getParents(ctx context.Context, q querier, id uuid.UUID) ([]knowledge.ParentRef, error) {
	if err := requireDocument(ctx, q, id); err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx, `
		SELECT p.id, p.title, p.summary, dp.is_primary
		FROM document_parents dp
		JOIN documents p ON p.id = dp.parent_id
		WHERE dp.child_id = $1
		ORDER BY dp.is_primary DESC, lower(p.title), p.id`, id)
	if err != nil {
		return nil, fmt.Errorf("get parents of %s: %w", id, err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (knowledge.ParentRef, error) {
		var p knowledge.ParentRef
		err := r.Scan(&p.ID, &p.Title, &p.Summary, &p.IsPrimary)
		return p, err
	})
	if err != nil {
		return nil, fmt.Errorf("get parents of %s: %w", id, err)
	}
	return out, nil
}

func getChildren(ctx context.Context, q querier, id uuid.UUID) ([]knowledge.ChildRef, error) {
	if err := requireDocument(ctx, q, id); err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx, `
		SELECT c.id, c.title, c.summary, dp.is_primary
		FROM document_parents dp
		JOIN documents c ON c.id = dp.child_id
		WHERE dp.parent_id = $1
		ORDER BY lower(c.title), c.id`, id)
	if err != nil {
		return nil, fmt.Errorf("get children of %s: %w", id, err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (knowledge.ChildRef, error) {
		var c knowledge.ChildRef
		err := r.Scan(&c.ID, &c.Title, &c.Summary, &c.IsPrimary)
		return c, err
	})
	if err != nil {
		return nil, fmt.Errorf("get children of %s: %w", id, err)
	}
	return out, nil
}

func getBlocks(ctx context.Context, q querier, id uuid.UUID) ([]knowledge.Block, error) {
	if err := requireDocument(ctx, q, id); err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx, `
		SELECT id, position, content, version
		FROM content_blocks
		WHERE document_id = $1
		ORDER BY position, id`, id)
	if err != nil {
		return nil, fmt.Errorf("get blocks of %s: %w", id, err)
	}
	out, err := pgx.CollectRows(rows, scanBlock)
	if err != nil {
		return nil, fmt.Errorf("get blocks of %s: %w", id, err)
	}
	return out, nil
}

func scanBlock(r pgx.CollectableRow) (knowledge.Block, error) {
	var b knowledge.Block
	err := r.Scan(&b.ID, &b.Position, &b.Content, &b.Version)
	return b, err
}

func getSources(ctx context.Context, q querier, id uuid.UUID) ([]knowledge.Source, error) {
	if err := requireDocument(ctx, q, id); err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx, `
		SELECT id, kind, source_uri, title, created_at
		FROM document_sources
		WHERE document_id = $1
		ORDER BY created_at, id`, id)
	if err != nil {
		return nil, fmt.Errorf("get sources of %s: %w", id, err)
	}
	out, err := pgx.CollectRows(rows, scanSource)
	if err != nil {
		return nil, fmt.Errorf("get sources of %s: %w", id, err)
	}
	return out, nil
}

func scanSource(r pgx.CollectableRow) (knowledge.Source, error) {
	var src knowledge.Source
	var kind string
	err := r.Scan(&src.ID, &kind, &src.URI, &src.Title, &src.CreatedAt)
	src.Kind = knowledge.SourceKind(kind)
	return src, err
}

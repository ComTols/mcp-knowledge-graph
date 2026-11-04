package database

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"mcp-knowledge-graph/internal/knowledge"
)

// AddSource attaches an external source reference. It is idempotent per
// document and URI: adding an existing URI returns the stored reference
// unchanged. Adding a new source increments the document version.
func (s *Store) AddSource(ctx context.Context, documentID uuid.UUID, in knowledge.SourceInput) (knowledge.Source, error) {
	in, err := knowledge.NormalizeSourceInput(in)
	if err != nil {
		return knowledge.Source{}, err
	}
	var out knowledge.Source
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := lockDocument(ctx, tx, documentID); err != nil {
			return err
		}
		src, created, err := insertSource(ctx, tx, documentID, in, 0)
		if err != nil {
			return err
		}
		out = src
		if !created {
			return nil
		}
		return bumpVersion(ctx, tx, documentID)
	})
	if err != nil {
		return knowledge.Source{}, err
	}
	return out, nil
}

// AddSourceChecked is AddSource with an optimistic version check. The
// version is checked under the document row lock before the source is
// looked up, so a stale version always fails, even for an already attached
// URI.
func (s *Store) AddSourceChecked(ctx context.Context, documentID uuid.UUID, expectedVersion int64, in knowledge.SourceInput) (knowledge.AddSourceResult, error) {
	if err := requireExpectedVersion(expectedVersion); err != nil {
		return knowledge.AddSourceResult{}, err
	}
	in, err := knowledge.NormalizeSourceInput(in)
	if err != nil {
		return knowledge.AddSourceResult{}, err
	}
	out := knowledge.AddSourceResult{DocumentID: documentID}
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		version, err := lockDocument(ctx, tx, documentID)
		if err != nil {
			return err
		}
		if version != expectedVersion {
			return knowledge.NewVersionConflict(documentID, expectedVersion, version)
		}
		out.Version = version
		src, created, err := insertSource(ctx, tx, documentID, in, 0)
		if err != nil {
			return err
		}
		out.Source, out.Changed = src, created
		if !created {
			return nil
		}
		out.Version, err = bumpVersionReturning(ctx, tx, documentID)
		return err
	})
	if err != nil {
		return knowledge.AddSourceResult{}, err
	}
	return out, nil
}

// insertSource inserts a source unless the document already references the
// URI; in that case the existing row is returned and created is false.
// ordinal offsets created_at by microseconds so that several sources
// inserted in one transaction keep their input order (now() is constant
// within a transaction).
func insertSource(ctx context.Context, tx pgx.Tx, documentID uuid.UUID, in knowledge.SourceInput, ordinal int) (knowledge.Source, bool, error) {
	rows, err := tx.Query(ctx, `
		INSERT INTO document_sources (document_id, kind, source_uri, title, created_at)
		VALUES ($1, $2, $3, $4, now() + $5 * interval '1 microsecond')
		ON CONFLICT (document_id, source_uri) DO NOTHING
		RETURNING id, kind, source_uri, title, created_at`,
		documentID, string(in.Kind), in.URI, in.Title, ordinal)
	if err != nil {
		return knowledge.Source{}, false, fmt.Errorf("insert source: %w", mapConstraintError(err))
	}
	inserted, err := pgx.CollectRows(rows, scanSource)
	if err != nil {
		return knowledge.Source{}, false, fmt.Errorf("insert source: %w", mapConstraintError(err))
	}
	if len(inserted) == 1 {
		return inserted[0], true, nil
	}

	rows, err = tx.Query(ctx, `
		SELECT id, kind, source_uri, title, created_at
		FROM document_sources WHERE document_id = $1 AND source_uri = $2`, documentID, in.URI)
	if err != nil {
		return knowledge.Source{}, false, fmt.Errorf("load source: %w", err)
	}
	existing, err := pgx.CollectExactlyOneRow(rows, scanSource)
	if err != nil {
		return knowledge.Source{}, false, fmt.Errorf("load source: %w", err)
	}
	return existing, false, nil
}

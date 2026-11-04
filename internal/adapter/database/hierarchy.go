package database

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"mcp-knowledge-graph/internal/knowledge"
)

// CreateDocument creates a non-root document with its parent edges, blocks,
// sources and embedding jobs in one transaction. The first parent ID becomes
// the primary parent. Missing parents are never created implicitly.
func (s *Store) CreateDocument(ctx context.Context, in knowledge.CreateDocumentInput) (knowledge.CreateDocumentResult, error) {
	in, err := knowledge.NormalizeCreateInput(in)
	if err != nil {
		return knowledge.CreateDocumentResult{}, err
	}

	var out knowledge.CreateDocumentResult
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		if err := lockHierarchy(ctx, tx); err != nil {
			return err
		}
		if err := requireReachable(ctx, tx, in.ParentIDs); err != nil {
			return err
		}

		err := tx.QueryRow(ctx, `
			INSERT INTO documents (title, summary)
			VALUES ($1, $2)
			RETURNING id, title, summary, version`, in.Title, in.Summary,
		).Scan(&out.ID, &out.Title, &out.Summary, &out.Version)
		if err != nil {
			return fmt.Errorf("insert document: %w", mapConstraintError(err))
		}
		out.PrimaryParentID = in.ParentIDs[0]

		// Offsetting created_at by the ordinal keeps the given parent order
		// visible, which drives the deterministic primary replacement.
		if _, err := tx.Exec(ctx, `
			INSERT INTO document_parents (child_id, parent_id, is_primary, created_at)
			SELECT $1, p.id, p.ord = 1, now() + p.ord * interval '1 microsecond'
			FROM unnest($2::uuid[]) WITH ORDINALITY AS p(id, ord)`, out.ID, in.ParentIDs); err != nil {
			return fmt.Errorf("insert parent edges: %w", mapConstraintError(err))
		}

		if out.BlockIDs, err = insertInitialBlocks(ctx, tx, out.ID, in.Blocks); err != nil {
			return err
		}
		out.SourceIDs = make([]uuid.UUID, 0, len(in.Sources))
		for i, src := range in.Sources {
			created, _, err := insertSource(ctx, tx, out.ID, src, i)
			if err != nil {
				return err
			}
			out.SourceIDs = append(out.SourceIDs, created.ID)
		}
		return enqueueJobs(ctx, tx, knowledge.TargetDocument,
			[]uuid.UUID{out.ID},
			[]string{knowledge.InputHash(knowledge.DocumentEmbeddingInput(out.Title, out.Summary))})
	})
	if err != nil {
		return knowledge.CreateDocumentResult{}, err
	}
	return out, nil
}

// UpdateDocumentMetadata changes title and/or summary using optimistic
// locking. A real change increments the version, invalidates the document
// vector and enqueues a reindexing job. A no-op update returns the current
// state unchanged.
func (s *Store) UpdateDocumentMetadata(ctx context.Context, id uuid.UUID, expectedVersion int64, upd knowledge.MetadataUpdate) (knowledge.DocumentSummary, error) {
	upd, err := knowledge.NormalizeMetadataUpdate(upd)
	if err != nil {
		return knowledge.DocumentSummary{}, err
	}

	var out knowledge.DocumentSummary
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx,
			`SELECT id, title, summary, version FROM documents WHERE id = $1 FOR NO KEY UPDATE`, id,
		).Scan(&out.ID, &out.Title, &out.Summary, &out.Version)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: document %s", knowledge.ErrNotFound, id)
		}
		if err != nil {
			return fmt.Errorf("load document %s: %w", id, err)
		}
		if out.Version != expectedVersion {
			return knowledge.NewVersionConflict(id, expectedVersion, out.Version)
		}

		title, summary := out.Title, out.Summary
		if upd.Title != nil {
			title = *upd.Title
		}
		if upd.Summary != nil {
			summary = *upd.Summary
		}
		if title == out.Title && summary == out.Summary {
			return nil
		}

		err = tx.QueryRow(ctx, `
			UPDATE documents
			SET title = $2, summary = $3,
			    version = version + 1, updated_at = now(),
			    embedding = NULL, embedding_model = NULL,
			    embedding_input_hash = NULL, embedding_updated_at = NULL
			WHERE id = $1
			RETURNING id, title, summary, version`, id, title, summary,
		).Scan(&out.ID, &out.Title, &out.Summary, &out.Version)
		if err != nil {
			return fmt.Errorf("update document %s: %w", id, mapConstraintError(err))
		}
		return enqueueJobs(ctx, tx, knowledge.TargetDocument,
			[]uuid.UUID{id},
			[]string{knowledge.InputHash(knowledge.DocumentEmbeddingInput(title, summary))})
	})
	if err != nil {
		return knowledge.DocumentSummary{}, err
	}
	return out, nil
}

// AddParent adds the edge parentID -> childID after verifying that it keeps
// the graph acyclic. Adding an existing edge is idempotent; with makePrimary
// an existing secondary edge becomes the primary one.
func (s *Store) AddParent(ctx context.Context, childID, parentID uuid.UUID, makePrimary bool) error {
	_, err := s.addParent(ctx, knowledge.ParentChange{DocumentID: childID, ParentID: parentID, MakePrimary: makePrimary})
	return err
}

// AddParentChecked is AddParent with an optimistic version check. It
// reports whether the hierarchy changed and the resulting version.
func (s *Store) AddParentChecked(ctx context.Context, in knowledge.ParentChange) (knowledge.ParentChangeResult, error) {
	if err := requireExpectedVersion(in.ExpectedVersion); err != nil {
		return knowledge.ParentChangeResult{}, err
	}
	return s.addParent(ctx, in)
}

// RemoveParent removes the edge parentID -> childID. The last parent can
// never be removed. If the primary edge is removed, the oldest remaining
// edge (then lowest parent ID) becomes primary. To choose a different new
// primary parent, call SetPrimaryParent before removing the edge.
func (s *Store) RemoveParent(ctx context.Context, childID, parentID uuid.UUID) error {
	_, err := s.removeParent(ctx, knowledge.ParentChange{DocumentID: childID, ParentID: parentID})
	return err
}

// RemoveParentChecked is RemoveParent with an optimistic version check.
func (s *Store) RemoveParentChecked(ctx context.Context, in knowledge.ParentChange) (knowledge.ParentChangeResult, error) {
	if err := requireExpectedVersion(in.ExpectedVersion); err != nil {
		return knowledge.ParentChangeResult{}, err
	}
	return s.removeParent(ctx, in)
}

// SetPrimaryParent makes an existing direct parent edge the primary one.
func (s *Store) SetPrimaryParent(ctx context.Context, childID, parentID uuid.UUID) error {
	_, err := s.setPrimaryParent(ctx, knowledge.ParentChange{DocumentID: childID, ParentID: parentID})
	return err
}

// SetPrimaryParentChecked is SetPrimaryParent with an optimistic version
// check.
func (s *Store) SetPrimaryParentChecked(ctx context.Context, in knowledge.ParentChange) (knowledge.ParentChangeResult, error) {
	if err := requireExpectedVersion(in.ExpectedVersion); err != nil {
		return knowledge.ParentChangeResult{}, err
	}
	return s.setPrimaryParent(ctx, in)
}

func requireExpectedVersion(v int64) error {
	if v <= 0 {
		return fmt.Errorf("%w: expected version must be positive", knowledge.ErrInvalidInput)
	}
	return nil
}

// lockChildForHierarchyChange acquires the hierarchy lock and then the row
// lock of the child (always in this order to avoid deadlocks) and checks
// the expected version if one is given (0 skips the check).
func lockChildForHierarchyChange(ctx context.Context, tx pgx.Tx, childID uuid.UUID, expectedVersion int64) (int64, error) {
	if err := lockHierarchy(ctx, tx); err != nil {
		return 0, err
	}
	version, err := lockDocument(ctx, tx, childID)
	if err != nil {
		return 0, err
	}
	if expectedVersion > 0 && version != expectedVersion {
		return 0, knowledge.NewVersionConflict(childID, expectedVersion, version)
	}
	return version, nil
}

func (s *Store) addParent(ctx context.Context, in knowledge.ParentChange) (knowledge.ParentChangeResult, error) {
	childID, parentID := in.DocumentID, in.ParentID
	if childID == knowledge.RootID {
		return knowledge.ParentChangeResult{}, fmt.Errorf("%w: root cannot have a parent", knowledge.ErrRootViolation)
	}
	if childID == parentID {
		return knowledge.ParentChangeResult{}, fmt.Errorf("%w: document %s cannot be its own parent", knowledge.ErrCycleDetected, childID)
	}

	res := knowledge.ParentChangeResult{DocumentID: childID, ParentID: parentID}
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		version, err := lockChildForHierarchyChange(ctx, tx, childID, in.ExpectedVersion)
		if err != nil {
			return err
		}
		res.Version = version
		if err := requireReachable(ctx, tx, []uuid.UUID{parentID}); err != nil {
			return err
		}

		var isPrimary bool
		err = tx.QueryRow(ctx,
			`SELECT is_primary FROM document_parents WHERE child_id = $1 AND parent_id = $2`,
			childID, parentID).Scan(&isPrimary)
		switch {
		case err == nil: // edge exists
			if !in.MakePrimary || isPrimary {
				res.IsPrimary = isPrimary
				res.PrimaryParentID, err = primaryParent(ctx, tx, childID)
				return err
			}
			if err := switchPrimary(ctx, tx, childID, parentID); err != nil {
				return err
			}
			res.Changed, res.IsPrimary, res.PrimaryParentID = true, true, parentID
			res.Version, err = bumpVersionReturning(ctx, tx, childID)
			return err
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("check edge: %w", err)
		}

		cycle, err := isDescendant(ctx, tx, childID, parentID)
		if err != nil {
			return err
		}
		if cycle {
			return fmt.Errorf("%w: %s is a descendant of %s", knowledge.ErrCycleDetected, parentID, childID)
		}

		if in.MakePrimary {
			if _, err := tx.Exec(ctx,
				`UPDATE document_parents SET is_primary = false WHERE child_id = $1 AND is_primary`,
				childID); err != nil {
				return fmt.Errorf("clear primary parent: %w", err)
			}
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO document_parents (child_id, parent_id, is_primary, created_at)
			VALUES ($1, $2, $3, clock_timestamp())`, childID, parentID, in.MakePrimary); err != nil {
			return fmt.Errorf("insert edge: %w", mapConstraintError(err))
		}
		res.Changed, res.IsPrimary = true, in.MakePrimary
		if res.PrimaryParentID, err = primaryParent(ctx, tx, childID); err != nil {
			return err
		}
		res.Version, err = bumpVersionReturning(ctx, tx, childID)
		return err
	})
	if err != nil {
		return knowledge.ParentChangeResult{}, err
	}
	return res, nil
}

func (s *Store) removeParent(ctx context.Context, in knowledge.ParentChange) (knowledge.ParentChangeResult, error) {
	childID, parentID := in.DocumentID, in.ParentID
	if childID == knowledge.RootID {
		return knowledge.ParentChangeResult{}, fmt.Errorf("%w: root has no parents", knowledge.ErrRootViolation)
	}
	res := knowledge.ParentChangeResult{DocumentID: childID, ParentID: parentID, Changed: true}
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := lockChildForHierarchyChange(ctx, tx, childID, in.ExpectedVersion); err != nil {
			return err
		}

		rows, err := tx.Query(ctx, `
			SELECT parent_id, is_primary
			FROM document_parents
			WHERE child_id = $1
			ORDER BY created_at, parent_id
			FOR UPDATE`, childID)
		if err != nil {
			return fmt.Errorf("load parents of %s: %w", childID, err)
		}
		type edge struct {
			parent  uuid.UUID
			primary bool
		}
		edges, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (edge, error) {
			var e edge
			err := r.Scan(&e.parent, &e.primary)
			return e, err
		})
		if err != nil {
			return fmt.Errorf("load parents of %s: %w", childID, err)
		}

		idx := -1
		for i, e := range edges {
			if e.parent == parentID {
				idx = i
			}
			if e.primary {
				res.PrimaryParentID = e.parent
			}
		}
		if idx < 0 {
			return fmt.Errorf("%w: %s is not a parent of %s", knowledge.ErrRelationshipNotFound, parentID, childID)
		}
		if len(edges) == 1 {
			return fmt.Errorf("%w: %s is the last parent of %s", knowledge.ErrLastParentViolation, parentID, childID)
		}

		if _, err := tx.Exec(ctx,
			`DELETE FROM document_parents WHERE child_id = $1 AND parent_id = $2`,
			childID, parentID); err != nil {
			return fmt.Errorf("delete edge: %w", err)
		}
		if edges[idx].primary {
			replacement := edges[0].parent
			if idx == 0 {
				replacement = edges[1].parent
			}
			if _, err := tx.Exec(ctx,
				`UPDATE document_parents SET is_primary = true WHERE child_id = $1 AND parent_id = $2`,
				childID, replacement); err != nil {
				return fmt.Errorf("set replacement primary parent: %w", err)
			}
			res.PrimaryParentID = replacement
		}
		// Defensive: in a DAG the remaining parents cannot depend on the
		// removed edge, but the invariant is cheap to assert.
		if err := requireReachable(ctx, tx, []uuid.UUID{childID}); err != nil {
			return err
		}
		res.Version, err = bumpVersionReturning(ctx, tx, childID)
		return err
	})
	if err != nil {
		return knowledge.ParentChangeResult{}, err
	}
	return res, nil
}

func (s *Store) setPrimaryParent(ctx context.Context, in knowledge.ParentChange) (knowledge.ParentChangeResult, error) {
	childID, parentID := in.DocumentID, in.ParentID
	if childID == knowledge.RootID {
		return knowledge.ParentChangeResult{}, fmt.Errorf("%w: root has no parents", knowledge.ErrRootViolation)
	}
	res := knowledge.ParentChangeResult{DocumentID: childID, ParentID: parentID, IsPrimary: true, PrimaryParentID: parentID}
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		version, err := lockChildForHierarchyChange(ctx, tx, childID, in.ExpectedVersion)
		if err != nil {
			return err
		}
		res.Version = version
		var isPrimary bool
		err = tx.QueryRow(ctx,
			`SELECT is_primary FROM document_parents WHERE child_id = $1 AND parent_id = $2`,
			childID, parentID).Scan(&isPrimary)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: %s is not a parent of %s", knowledge.ErrRelationshipNotFound, parentID, childID)
		}
		if err != nil {
			return fmt.Errorf("check edge: %w", err)
		}
		if isPrimary {
			return nil
		}
		if err := switchPrimary(ctx, tx, childID, parentID); err != nil {
			return err
		}
		res.Changed = true
		res.Version, err = bumpVersionReturning(ctx, tx, childID)
		return err
	})
	if err != nil {
		return knowledge.ParentChangeResult{}, err
	}
	return res, nil
}

// primaryParent returns the primary parent of a non-root document.
func primaryParent(ctx context.Context, q querier, childID uuid.UUID) (uuid.UUID, error) {
	var id uuid.UUID
	err := q.QueryRow(ctx,
		`SELECT parent_id FROM document_parents WHERE child_id = $1 AND is_primary`, childID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, fmt.Errorf("%w: document %s has no primary parent", knowledge.ErrIntegrityViolation, childID)
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("load primary parent of %s: %w", childID, err)
	}
	return id, nil
}

// switchPrimary atomically moves the primary flag of childID to parentID.
// The partial unique index is not deferrable, so the old flag is cleared
// first.
func switchPrimary(ctx context.Context, tx pgx.Tx, childID, parentID uuid.UUID) error {
	if _, err := tx.Exec(ctx,
		`UPDATE document_parents SET is_primary = false WHERE child_id = $1 AND is_primary`,
		childID); err != nil {
		return fmt.Errorf("clear primary parent: %w", err)
	}
	tag, err := tx.Exec(ctx,
		`UPDATE document_parents SET is_primary = true WHERE child_id = $1 AND parent_id = $2`,
		childID, parentID)
	if err != nil {
		return fmt.Errorf("set primary parent: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("%w: %s is not a parent of %s", knowledge.ErrRelationshipNotFound, parentID, childID)
	}
	return nil
}

// isDescendant reports whether candidate is reachable from ancestor along
// child edges (or equal to it).
func isDescendant(ctx context.Context, q querier, ancestor, candidate uuid.UUID) (bool, error) {
	var found bool
	err := q.QueryRow(ctx, `
		WITH RECURSIVE descendants(id) AS (
			SELECT $1::uuid
			UNION
			SELECT dp.child_id
			FROM document_parents dp
			JOIN descendants d ON dp.parent_id = d.id
		)
		SELECT EXISTS (SELECT 1 FROM descendants WHERE id = $2)`, ancestor, candidate).Scan(&found)
	if err != nil {
		return false, fmt.Errorf("cycle check: %w", err)
	}
	return found, nil
}

// requireReachable verifies that every id exists (ErrNotFound) and is
// reachable from root (ErrIntegrityViolation otherwise).
func requireReachable(ctx context.Context, q querier, ids []uuid.UUID) error {
	rows, err := q.Query(ctx, `
		WITH RECURSIVE anc(start_id, id) AS (
			SELECT s, s FROM unnest($1::uuid[]) AS s
			UNION
			SELECT anc.start_id, dp.parent_id
			FROM anc
			JOIN document_parents dp ON dp.child_id = anc.id
		)
		SELECT s.id,
		       EXISTS (SELECT 1 FROM documents d WHERE d.id = s.id),
		       EXISTS (SELECT 1 FROM anc WHERE anc.start_id = s.id AND anc.id = $2)
		FROM unnest($1::uuid[]) AS s(id)`, ids, knowledge.RootID)
	if err != nil {
		return fmt.Errorf("check reachability: %w", err)
	}
	type state struct {
		id                uuid.UUID
		exists, reachable bool
	}
	states, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (state, error) {
		var st state
		err := r.Scan(&st.id, &st.exists, &st.reachable)
		return st, err
	})
	if err != nil {
		return fmt.Errorf("check reachability: %w", err)
	}
	for _, st := range states {
		if !st.exists {
			return fmt.Errorf("%w: document %s", knowledge.ErrNotFound, st.id)
		}
		if !st.reachable {
			return fmt.Errorf("%w: document %s is not reachable from root", knowledge.ErrIntegrityViolation, st.id)
		}
	}
	return nil
}

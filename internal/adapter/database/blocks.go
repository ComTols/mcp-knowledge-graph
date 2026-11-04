package database

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"mcp-knowledge-graph/internal/knowledge"
)

// EditDocument applies a batch of block operations (replace, insert, append,
// prepend, delete, move) atomically. It is the only write path for content
// blocks, so all position logic lives in knowledge.PlanBlockEdit.
//
// The document row is locked and its version must equal
// in.ExpectedVersion. The batch is planned against the locked, current block
// state and only the necessary rows are written: new blocks, deleted blocks,
// changed contents and changed positions (moves, rebalancing). A batch with
// at least one effect increments the document version exactly once; a batch
// without effect returns Changed=false and keeps the version. Embedding jobs
// are created only for new blocks and for replaced contents whose embedding
// input actually changed. Any error rolls back the whole batch.
func (s *Store) EditDocument(ctx context.Context, in knowledge.EditDocumentInput) (knowledge.EditDocumentResult, error) {
	if err := in.Validate(); err != nil {
		return knowledge.EditDocumentResult{}, err
	}

	var res knowledge.EditDocumentResult
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		version, err := lockDocument(ctx, tx, in.DocumentID)
		if err != nil {
			return err
		}
		if version != in.ExpectedVersion {
			return knowledge.NewVersionConflict(in.DocumentID, in.ExpectedVersion, version)
		}

		current, err := loadBlockStates(ctx, tx, in.DocumentID)
		if err != nil {
			return err
		}
		plan, err := knowledge.PlanBlockEdit(current, in.Operations, uuid.New)
		if err != nil {
			return err
		}

		res = knowledge.EditDocumentResult{
			DocumentID:    in.DocumentID,
			NewVersion:    version,
			Changed:       plan.Changed(),
			CreatedBlocks: plan.CreatedBlocks(),
			DeletedBlocks: append([]uuid.UUID{}, plan.Deleted...),
			BlockOrder:    plan.Order(),
		}
		if !plan.Changed() {
			return nil
		}
		if err := applyBlockPlan(ctx, tx, in.DocumentID, current, plan); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
			UPDATE documents SET version = version + 1, updated_at = now()
			WHERE id = $1 RETURNING version`, in.DocumentID).Scan(&res.NewVersion)
	})
	if err != nil {
		// Deferred constraints are checked at commit; report them as
		// domain errors, never as success.
		return knowledge.EditDocumentResult{}, mapConstraintError(err)
	}
	return res, nil
}

// loadBlockStates reads the blocks of a locked document in their order.
func loadBlockStates(ctx context.Context, tx pgx.Tx, documentID uuid.UUID) ([]knowledge.BlockState, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, position, content
		FROM content_blocks
		WHERE document_id = $1
		ORDER BY position, id`, documentID)
	if err != nil {
		return nil, fmt.Errorf("load blocks: %w", err)
	}
	states, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (knowledge.BlockState, error) {
		var b knowledge.BlockState
		err := r.Scan(&b.ID, &b.Position, &b.Content)
		return b, err
	})
	if err != nil {
		return nil, fmt.Errorf("load blocks: %w", err)
	}
	return states, nil
}

// applyBlockPlan writes a plan. The unique (document_id, position)
// constraint is deferred, so the statement order does not matter for
// intermediate collisions.
func applyBlockPlan(ctx context.Context, tx pgx.Tx, documentID uuid.UUID, current []knowledge.BlockState, plan knowledge.BlockEditPlan) error {
	if len(plan.Deleted) > 0 {
		if _, err := tx.Exec(ctx,
			`DELETE FROM content_blocks WHERE document_id = $1 AND id = ANY($2::uuid[])`,
			documentID, plan.Deleted); err != nil {
			return fmt.Errorf("delete blocks: %w", err)
		}
		// embedding_jobs has polymorphic targets without FK.
		if _, err := tx.Exec(ctx,
			`DELETE FROM embedding_jobs WHERE target_kind = $1 AND target_id = ANY($2::uuid[])`,
			string(knowledge.TargetBlock), plan.Deleted); err != nil {
			return fmt.Errorf("delete embedding jobs: %w", err)
		}
	}

	if len(plan.Repositioned) > 0 {
		ids := make([]uuid.UUID, len(plan.Repositioned))
		positions := make([]int64, len(plan.Repositioned))
		for i, b := range plan.Repositioned {
			ids[i], positions[i] = b.ID, b.Position
		}
		// Position changes touch neither content, version, updated_at nor
		// embeddings of the block.
		if _, err := tx.Exec(ctx, `
			UPDATE content_blocks b
			SET position = u.position
			FROM unnest($2::uuid[], $3::bigint[]) AS u(id, position)
			WHERE b.id = u.id AND b.document_id = $1`, documentID, ids, positions); err != nil {
			return fmt.Errorf("update block positions: %w", err)
		}
	}

	if len(plan.Inserted) > 0 {
		ids := make([]uuid.UUID, len(plan.Inserted))
		positions := make([]int64, len(plan.Inserted))
		contents := make([]string, len(plan.Inserted))
		hashes := make([]string, len(plan.Inserted))
		for i, b := range plan.Inserted {
			ids[i], positions[i], contents[i] = b.ID, b.Position, b.Content
			hashes[i] = knowledge.InputHash(knowledge.BlockEmbeddingInput(b.Content))
		}
		if err := insertBlockRows(ctx, tx, documentID, ids, positions, contents); err != nil {
			return err
		}
		if err := enqueueJobs(ctx, tx, knowledge.TargetBlock, ids, hashes); err != nil {
			return err
		}
	}

	if len(plan.ContentChanged) > 0 {
		original := make(map[uuid.UUID]string, len(current))
		for _, b := range current {
			original[b.ID] = b.Content
		}
		for _, b := range plan.ContentChanged {
			if err := replaceBlockContent(ctx, tx, b.ID, original[b.ID], b.Content); err != nil {
				return err
			}
		}
	}
	return nil
}

// replaceBlockContent stores new content and increments the block version.
// The vector is invalidated and a job enqueued only if the normalized
// embedding input changed.
func replaceBlockContent(ctx context.Context, tx pgx.Tx, blockID uuid.UUID, oldContent, newContent string) error {
	newHash := knowledge.InputHash(knowledge.BlockEmbeddingInput(newContent))
	reindex := knowledge.InputHash(knowledge.BlockEmbeddingInput(oldContent)) != newHash

	if _, err := tx.Exec(ctx, `
		UPDATE content_blocks
		SET content = $2, version = version + 1, updated_at = now(),
		    embedding            = CASE WHEN $3 THEN NULL ELSE embedding END,
		    embedding_model      = CASE WHEN $3 THEN NULL ELSE embedding_model END,
		    embedding_input_hash = CASE WHEN $3 THEN NULL ELSE embedding_input_hash END,
		    embedding_updated_at = CASE WHEN $3 THEN NULL ELSE embedding_updated_at END
		WHERE id = $1`, blockID, newContent, reindex); err != nil {
		return fmt.Errorf("replace block %s: %w", blockID, mapConstraintError(err))
	}
	if !reindex {
		return nil
	}
	return enqueueJobs(ctx, tx, knowledge.TargetBlock, []uuid.UUID{blockID}, []string{newHash})
}

func insertBlockRows(ctx context.Context, tx pgx.Tx, documentID uuid.UUID, ids []uuid.UUID, positions []int64, contents []string) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO content_blocks (id, document_id, position, content)
		SELECT b.id, $1, b.position, b.content
		FROM unnest($2::uuid[], $3::bigint[], $4::text[]) AS b(id, position, content)`,
		documentID, ids, positions, contents); err != nil {
		return fmt.Errorf("insert blocks: %w", mapConstraintError(err))
	}
	return nil
}

// insertInitialBlocks numbers the blocks of a new document sequentially
// (PositionGap, 2*PositionGap, ...) and enqueues their embedding jobs.
func insertInitialBlocks(ctx context.Context, tx pgx.Tx, documentID uuid.UUID, contents []string) ([]uuid.UUID, error) {
	if len(contents) == 0 {
		return []uuid.UUID{}, nil
	}
	positions, err := knowledge.SequentialPositions(len(contents))
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, len(contents))
	hashes := make([]string, len(contents))
	for i, c := range contents {
		ids[i] = uuid.New()
		hashes[i] = knowledge.InputHash(knowledge.BlockEmbeddingInput(c))
	}
	if err := insertBlockRows(ctx, tx, documentID, ids, positions, contents); err != nil {
		return nil, err
	}
	return ids, enqueueJobs(ctx, tx, knowledge.TargetBlock, ids, hashes)
}

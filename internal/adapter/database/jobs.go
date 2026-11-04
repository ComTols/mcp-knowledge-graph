package database

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pgvector/pgvector-go"

	"mcp-knowledge-graph/internal/indexing"
	"mcp-knowledge-graph/internal/knowledge"
)

// JobState is the persisted state of an embedding job.
type JobState string

// Job states.
const (
	JobPending    JobState = "pending"
	JobProcessing JobState = "processing"
	JobFailed     JobState = "failed"
)

// JobStatus is the diagnostic view of an embedding job.
type JobStatus struct {
	Kind       knowledge.TargetKind
	TargetID   uuid.UUID
	InputHash  string
	State      JobState
	Attempts   int
	LeaseUntil *time.Time
	LastError  *string
	UpdatedAt  time.Time
}

// EmbeddingStatus is the indexing state of a document or block.
type EmbeddingStatus struct {
	HasEmbedding bool
	Model        *string
	InputHash    *string
	UpdatedAt    *time.Time
}

// enqueueJobs upserts one job per target: a new input hash resets the job to
// pending, so any older in-flight result for the target becomes stale.
func enqueueJobs(ctx context.Context, q querier, kind knowledge.TargetKind, ids []uuid.UUID, hashes []string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := q.Exec(ctx, `
		INSERT INTO embedding_jobs (target_kind, target_id, input_hash)
		SELECT $1, t.id, t.hash
		FROM unnest($2::uuid[], $3::text[]) AS t(id, hash)
		ON CONFLICT (target_kind, target_id) DO UPDATE
		SET input_hash  = EXCLUDED.input_hash,
		    state       = 'pending',
		    attempts    = 0,
		    lease_until = NULL,
		    last_error  = NULL,
		    updated_at  = now()`, string(kind), ids, hashes)
	if err != nil {
		return fmt.Errorf("enqueue %s embedding jobs: %w", kind, err)
	}
	return nil
}

// RetryEmbedding re-queues the embedding of a document or block for its
// current text (administrative repair, e.g. after a permanent failure).
func (s *Store) RetryEmbedding(ctx context.Context, kind knowledge.TargetKind, targetID uuid.UUID) error {
	if !kind.Valid() {
		return fmt.Errorf("%w: unknown target kind %q", knowledge.ErrInvalidInput, kind)
	}
	return s.inTx(ctx, func(tx pgx.Tx) error {
		input, err := currentInput(ctx, tx, kind, targetID, false)
		if err != nil {
			return err
		}
		return enqueueJobs(ctx, tx, kind, []uuid.UUID{targetID}, []string{knowledge.InputHash(input)})
	})
}

// currentInput loads the current text of a target and builds its embedding
// input. With lock the target row is locked (FOR NO KEY UPDATE).
func currentInput(ctx context.Context, q querier, kind knowledge.TargetKind, id uuid.UUID, lock bool) (string, error) {
	suffix := ""
	if lock {
		suffix = " FOR NO KEY UPDATE"
	}
	switch kind {
	case knowledge.TargetDocument:
		var title, summary string
		err := q.QueryRow(ctx, `SELECT title, summary FROM documents WHERE id = $1`+suffix, id).Scan(&title, &summary)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", fmt.Errorf("%w: document %s", knowledge.ErrNotFound, id)
		}
		if err != nil {
			return "", fmt.Errorf("load document %s: %w", id, err)
		}
		return knowledge.DocumentEmbeddingInput(title, summary), nil
	case knowledge.TargetBlock:
		var content string
		err := q.QueryRow(ctx, `SELECT content FROM content_blocks WHERE id = $1`+suffix, id).Scan(&content)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", fmt.Errorf("%w: block %s", knowledge.ErrNotFound, id)
		}
		if err != nil {
			return "", fmt.Errorf("load block %s: %w", id, err)
		}
		return knowledge.BlockEmbeddingInput(content), nil
	}
	return "", fmt.Errorf("%w: unknown target kind %q", knowledge.ErrInvalidInput, kind)
}

// ClaimEmbeddingJobs leases up to limit jobs that are pending (and not
// delayed by a retry backoff) or whose processing lease has expired. Rows are
// selected with FOR UPDATE SKIP LOCKED, so concurrent workers never claim the
// same job. The transaction commits before any embedding is computed.
// Jobs whose target no longer exists are deleted.
func (s *Store) ClaimEmbeddingJobs(ctx context.Context, limit int, lease time.Duration) ([]indexing.Job, error) {
	if limit <= 0 {
		return nil, nil
	}
	if lease <= 0 {
		return nil, fmt.Errorf("%w: lease must be positive", knowledge.ErrInvalidInput)
	}
	var jobs []indexing.Job
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			WITH candidates AS (
				SELECT target_kind, target_id
				FROM embedding_jobs
				WHERE (state = 'pending' AND (lease_until IS NULL OR lease_until <= now()))
				   OR (state = 'processing' AND lease_until <= now())
				ORDER BY updated_at, target_kind, target_id
				LIMIT $1
				FOR UPDATE SKIP LOCKED
			)
			UPDATE embedding_jobs j
			SET state = 'processing',
			    attempts = j.attempts + 1,
			    lease_until = now() + make_interval(secs => $2),
			    updated_at = now()
			FROM candidates c
			WHERE j.target_kind = c.target_kind AND j.target_id = c.target_id
			RETURNING j.target_kind, j.target_id, j.input_hash, j.attempts`,
			limit, lease.Seconds())
		if err != nil {
			return fmt.Errorf("claim jobs: %w", err)
		}
		claimed, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (indexing.Job, error) {
			var j indexing.Job
			var kind string
			err := r.Scan(&kind, &j.TargetID, &j.InputHash, &j.Attempts)
			j.Kind = knowledge.TargetKind(kind)
			return j, err
		})
		if err != nil {
			return fmt.Errorf("claim jobs: %w", err)
		}

		for _, j := range claimed {
			input, err := currentInput(ctx, tx, j.Kind, j.TargetID, false)
			if errors.Is(err, knowledge.ErrNotFound) {
				if _, err := tx.Exec(ctx,
					`DELETE FROM embedding_jobs WHERE target_kind = $1 AND target_id = $2`,
					string(j.Kind), j.TargetID); err != nil {
					return fmt.Errorf("delete orphaned job: %w", err)
				}
				continue
			}
			if err != nil {
				return err
			}
			j.Input = input
			jobs = append(jobs, j)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return jobs, nil
}

// CompleteEmbeddingJob stores the vector computed for job.Input, but only if
// that input is still the current text of the target. Otherwise the result
// is discarded and the (newer) job stays queued. The job row is deleted only
// if it still refers to the embedded input.
func (s *Store) CompleteEmbeddingJob(ctx context.Context, job indexing.Job, model string, vector []float32) (bool, error) {
	if len(vector) != knowledge.EmbeddingDimensions {
		return false, fmt.Errorf("%w: vector has %d dimensions, want %d",
			knowledge.ErrInvalidInput, len(vector), knowledge.EmbeddingDimensions)
	}
	if model == "" {
		return false, fmt.Errorf("%w: model must not be empty", knowledge.ErrInvalidInput)
	}
	embeddedHash := knowledge.InputHash(job.Input)

	var stored bool
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		input, err := currentInput(ctx, tx, job.Kind, job.TargetID, true)
		if errors.Is(err, knowledge.ErrNotFound) {
			_, err := tx.Exec(ctx,
				`DELETE FROM embedding_jobs WHERE target_kind = $1 AND target_id = $2`,
				string(job.Kind), job.TargetID)
			return err
		}
		if err != nil {
			return err
		}
		if knowledge.InputHash(input) != embeddedHash {
			return nil // stale result: the text changed meanwhile
		}

		table := "documents"
		if job.Kind == knowledge.TargetBlock {
			table = "content_blocks"
		}
		// table is one of two constants, never user input.
		if _, err := tx.Exec(ctx, `
			UPDATE `+table+`
			SET embedding = $2, embedding_model = $3,
			    embedding_input_hash = $4, embedding_updated_at = now()
			WHERE id = $1`, job.TargetID, pgvector.NewVector(vector), model, embeddedHash); err != nil {
			return fmt.Errorf("store embedding: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			DELETE FROM embedding_jobs
			WHERE target_kind = $1 AND target_id = $2 AND input_hash = $3`,
			string(job.Kind), job.TargetID, embeddedHash); err != nil {
			return fmt.Errorf("delete completed job: %w", err)
		}
		stored = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return stored, nil
}

// FailEmbeddingJob records a failed attempt. The job is retried after
// f.RetryAfter unless it failed permanently or reached f.MaxAttempts, in
// which case it is marked failed with the error message. A job that was
// re-queued for a newer input in the meantime is left untouched.
func (s *Store) FailEmbeddingJob(ctx context.Context, job indexing.Job, f indexing.Failure) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE embedding_jobs
		SET state       = CASE WHEN $4 OR attempts >= $5 THEN 'failed' ELSE 'pending' END,
		    lease_until = CASE WHEN $4 OR attempts >= $5 THEN NULL
		                       ELSE now() + make_interval(secs => $6) END,
		    last_error  = $7,
		    updated_at  = now()
		WHERE target_kind = $1 AND target_id = $2 AND input_hash = $3 AND state = 'processing'`,
		string(job.Kind), job.TargetID, job.InputHash,
		f.Permanent, f.MaxAttempts, f.RetryAfter.Seconds(), f.Message)
	if err != nil {
		return fmt.Errorf("record job failure: %w", err)
	}
	return nil
}

// GetEmbeddingJob returns the job of a target (diagnostics, tests).
func (s *Store) GetEmbeddingJob(ctx context.Context, kind knowledge.TargetKind, targetID uuid.UUID) (JobStatus, error) {
	var st JobStatus
	var k, state string
	err := s.pool.QueryRow(ctx, `
		SELECT target_kind, target_id, input_hash, state, attempts, lease_until, last_error, updated_at
		FROM embedding_jobs WHERE target_kind = $1 AND target_id = $2`, string(kind), targetID,
	).Scan(&k, &st.TargetID, &st.InputHash, &state, &st.Attempts, &st.LeaseUntil, &st.LastError, &st.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return st, fmt.Errorf("%w: embedding job %s/%s", knowledge.ErrNotFound, kind, targetID)
	}
	if err != nil {
		return st, fmt.Errorf("get embedding job: %w", err)
	}
	st.Kind, st.State = knowledge.TargetKind(k), JobState(state)
	return st, nil
}

// ListFailedEmbeddingJobs returns all permanently failed jobs.
func (s *Store) ListFailedEmbeddingJobs(ctx context.Context) ([]JobStatus, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT target_kind, target_id, input_hash, state, attempts, lease_until, last_error, updated_at
		FROM embedding_jobs WHERE state = 'failed'
		ORDER BY updated_at, target_kind, target_id`)
	if err != nil {
		return nil, fmt.Errorf("list failed jobs: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (JobStatus, error) {
		var st JobStatus
		var k, state string
		err := r.Scan(&k, &st.TargetID, &st.InputHash, &state, &st.Attempts, &st.LeaseUntil, &st.LastError, &st.UpdatedAt)
		st.Kind, st.State = knowledge.TargetKind(k), JobState(state)
		return st, err
	})
	if err != nil {
		return nil, fmt.Errorf("list failed jobs: %w", err)
	}
	return out, nil
}

// GetEmbeddingStatus returns the indexing state of a document or block.
func (s *Store) GetEmbeddingStatus(ctx context.Context, kind knowledge.TargetKind, targetID uuid.UUID) (EmbeddingStatus, error) {
	table := "documents"
	switch kind {
	case knowledge.TargetDocument:
	case knowledge.TargetBlock:
		table = "content_blocks"
	default:
		return EmbeddingStatus{}, fmt.Errorf("%w: unknown target kind %q", knowledge.ErrInvalidInput, kind)
	}
	var st EmbeddingStatus
	err := s.pool.QueryRow(ctx, `
		SELECT embedding IS NOT NULL, embedding_model, embedding_input_hash, embedding_updated_at
		FROM `+table+` WHERE id = $1`, targetID,
	).Scan(&st.HasEmbedding, &st.Model, &st.InputHash, &st.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return st, fmt.Errorf("%w: %s %s", knowledge.ErrNotFound, kind, targetID)
	}
	if err != nil {
		return st, fmt.Errorf("get embedding status: %w", err)
	}
	return st, nil
}

package database

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"mcp-knowledge-graph/internal/knowledge"
)

// hierarchyLockKey is the transaction scoped advisory lock that serializes
// all hierarchy mutations (create with parents, add/remove parent, change
// primary parent). Without it two concurrent, individually harmless edges
// could together form a cycle.
const hierarchyLockKey int64 = 7654321001

// querier is implemented by *pgxpool.Pool, *pgx.Conn and pgx.Tx.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// inTx runs fn in a read-write transaction (READ COMMITTED).
func (s *Store) inTx(ctx context.Context, fn func(pgx.Tx) error) error {
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, fn)
}

// inReadTx runs fn in a read-only REPEATABLE READ transaction so that
// aggregated reads see one consistent snapshot.
func (s *Store) inReadTx(ctx context.Context, fn func(pgx.Tx) error) error {
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	}, fn)
}

// lockHierarchy must be the first statement of every hierarchy mutation so
// that all later statements (READ COMMITTED) see the latest committed graph.
func lockHierarchy(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, hierarchyLockKey); err != nil {
		return fmt.Errorf("acquire hierarchy lock: %w", err)
	}
	return nil
}

// lockDocument takes a row lock on a document and returns its version.
func lockDocument(ctx context.Context, q querier, id uuid.UUID) (int64, error) {
	var version int64
	err := q.QueryRow(ctx, `SELECT version FROM documents WHERE id = $1 FOR NO KEY UPDATE`, id).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("%w: document %s", knowledge.ErrNotFound, id)
	}
	if err != nil {
		return 0, fmt.Errorf("lock document %s: %w", id, err)
	}
	return version, nil
}

// bumpVersion increments the version of a document to signal a change.
func bumpVersion(ctx context.Context, q querier, id uuid.UUID) error {
	if _, err := q.Exec(ctx,
		`UPDATE documents SET version = version + 1, updated_at = now() WHERE id = $1`, id); err != nil {
		return fmt.Errorf("bump version of %s: %w", id, err)
	}
	return nil
}

// bumpVersionReturning increments the version of a document and returns
// the new version.
func bumpVersionReturning(ctx context.Context, q querier, id uuid.UUID) (int64, error) {
	var v int64
	if err := q.QueryRow(ctx,
		`UPDATE documents SET version = version + 1, updated_at = now() WHERE id = $1 RETURNING version`,
		id).Scan(&v); err != nil {
		return 0, fmt.Errorf("bump version of %s: %w", id, err)
	}
	return v, nil
}

// documentExists reports whether a document exists.
func documentExists(ctx context.Context, q querier, id uuid.UUID) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM documents WHERE id = $1)`, id).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("check document %s: %w", id, err)
	}
	return ok, nil
}

func requireDocument(ctx context.Context, q querier, id uuid.UUID) error {
	ok, err := documentExists(ctx, q, id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: document %s", knowledge.ErrNotFound, id)
	}
	return nil
}

// PostgreSQL error codes used for mapping.
const (
	pgForeignKeyViolation = "23503"
	pgUniqueViolation     = "23505"
	pgCheckViolation      = "23514"
)

// mapConstraintError translates constraint violations that slipped past the
// Go validation into domain errors. Other errors are returned unchanged.
func mapConstraintError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch pgErr.Code {
	case pgForeignKeyViolation:
		return fmt.Errorf("%w: %s", knowledge.ErrNotFound, pgErr.Message)
	case pgCheckViolation:
		return fmt.Errorf("%w: %s", knowledge.ErrInvalidInput, pgErr.Message)
	case pgUniqueViolation:
		return fmt.Errorf("%w: %s", knowledge.ErrIntegrityViolation, pgErr.Message)
	}
	return err
}

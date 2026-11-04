package database

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"mcp-knowledge-graph/internal/knowledge"
)

// VerifyRoot checks the root invariants: exactly one root, with the fixed
// ID, without parents. It reports problems as ErrIntegrityViolation and
// never repairs anything.
func (s *Store) VerifyRoot(ctx context.Context) error {
	var (
		rootCount   int
		fixedIsRoot bool
		rootParents int
	)
	err := s.pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM documents WHERE is_root),
			COALESCE((SELECT is_root FROM documents WHERE id = $1), false),
			(SELECT count(*) FROM document_parents WHERE child_id = $1)`,
		knowledge.RootID,
	).Scan(&rootCount, &fixedIsRoot, &rootParents)
	if err != nil {
		return fmt.Errorf("verify root: %w", err)
	}
	switch {
	case rootCount != 1:
		return fmt.Errorf("%w: expected exactly one root document, found %d", knowledge.ErrIntegrityViolation, rootCount)
	case !fixedIsRoot:
		return fmt.Errorf("%w: root document %s is missing or not marked as root", knowledge.ErrIntegrityViolation, knowledge.RootID)
	case rootParents != 0:
		return fmt.Errorf("%w: root document has %d parents", knowledge.ErrIntegrityViolation, rootParents)
	}
	return nil
}

// CheckIntegrity performs a full scan of all graph and block invariants and
// returns a human readable description of every violation found. An empty
// result means the database is consistent. It is intended for tests,
// diagnostics and administrative checks; it is O(size of the database).
func (s *Store) CheckIntegrity(ctx context.Context) ([]string, error) {
	var violations []string
	if err := s.VerifyRoot(ctx); err != nil {
		violations = append(violations, err.Error())
	}

	checks := []struct {
		name string
		sql  string
	}{
		{"non-root document without parent", `
			SELECT d.id FROM documents d
			WHERE NOT d.is_root
			  AND NOT EXISTS (SELECT 1 FROM document_parents dp WHERE dp.child_id = d.id)`},
		{"non-root document without exactly one primary parent", `
			SELECT d.id FROM documents d
			WHERE NOT d.is_root
			  AND (SELECT count(*) FROM document_parents dp
			       WHERE dp.child_id = d.id AND dp.is_primary) <> 1`},
		{"document not reachable from root", `
			WITH RECURSIVE reach(id) AS (
				SELECT id FROM documents WHERE is_root
				UNION
				SELECT dp.child_id FROM document_parents dp JOIN reach ON dp.parent_id = reach.id
			)
			SELECT d.id FROM documents d WHERE d.id NOT IN (SELECT id FROM reach)`},
		{"document on a cycle", `
			WITH RECURSIVE walk(start_id, id) AS (
				SELECT child_id, parent_id FROM document_parents
				UNION
				SELECT w.start_id, dp.parent_id
				FROM walk w JOIN document_parents dp ON dp.child_id = w.id
			)
			SELECT DISTINCT start_id FROM walk WHERE start_id = id`},
		{"document with duplicate block positions", `
			SELECT document_id FROM content_blocks
			GROUP BY document_id
			HAVING count(*) <> count(DISTINCT position)`},
	}
	for _, c := range checks {
		rows, err := s.pool.Query(ctx, c.sql)
		if err != nil {
			return nil, fmt.Errorf("integrity check %q: %w", c.name, err)
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		if err != nil {
			return nil, fmt.Errorf("integrity check %q: %w", c.name, err)
		}
		for _, id := range ids {
			violations = append(violations, fmt.Sprintf("%s: %s", c.name, id))
		}
	}
	return violations, nil
}

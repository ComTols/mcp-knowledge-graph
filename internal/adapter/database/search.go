package database

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pgvector/pgvector-go"
	"golang.org/x/sync/errgroup"

	"mcp-knowledge-graph/internal/knowledge"
	"mcp-knowledge-graph/internal/search"
)

// Search runs the hybrid search:
//
//  1. nearest document vectors (title + summary),
//  2. nearest block vectors, merged per document,
//  3. lexical matches (full text over documents and blocks, exact, prefix,
//     substring and trigram title matches), deduplicated per document,
//
// fused with Reciprocal Rank Fusion. Exact title matches form an additional
// weighted list. Only vectors of the configured embedder's model are used.
// The root document is never a result.
//
// A query embedding is required: without an embedder, or if it fails,
// ErrEmbeddingUnavailable is returned instead of silently degrading to a
// keyword search, unless the caller explicitly allows a lexical fallback
// (SearchParams.LexicalFallback). A fallback is reported by
// SemanticSearchAvailable=false. Documents without a vector are still found
// lexically.
func (s *Store) Search(ctx context.Context, p knowledge.SearchParams) (knowledge.SearchResponse, error) {
	cfg := s.searchCfg
	query := strings.TrimSpace(p.Query)
	if query == "" {
		return knowledge.SearchResponse{}, fmt.Errorf("%w: query must not be empty", knowledge.ErrInvalidInput)
	}
	if len(query) > cfg.MaxQueryBytes {
		return knowledge.SearchResponse{}, fmt.Errorf("%w: query exceeds %d bytes", knowledge.ErrInvalidInput, cfg.MaxQueryBytes)
	}
	limit := p.Limit
	if limit <= 0 {
		limit = cfg.DefaultLimit
	}
	limit = min(limit, cfg.MaxLimit)

	semantic := true
	qvec, model, err := s.embedQuery(ctx, query)
	if err != nil {
		// A canceled request is not an embedding outage.
		if !p.LexicalFallback || !errors.Is(err, knowledge.ErrEmbeddingUnavailable) || ctx.Err() != nil {
			return knowledge.SearchResponse{}, err
		}
		semantic = false
	}
	semanticErr := err

	var docVec, blockVec, lexical, exact []uuid.UUID
	g, gctx := errgroup.WithContext(ctx)
	if semantic {
		g.Go(func() (err error) {
			docVec, err = s.documentVectorCandidates(gctx, qvec, model)
			return err
		})
		g.Go(func() (err error) {
			blockVec, err = s.blockVectorCandidates(gctx, qvec, model)
			return err
		})
	}
	g.Go(func() (err error) {
		lexical, exact, err = s.lexicalCandidates(gctx, query)
		return err
	})
	if err := g.Wait(); err != nil {
		return knowledge.SearchResponse{}, err
	}

	fused := search.Fuse(cfg.RRFK,
		search.RankedList{Name: "document_vector", IDs: docVec},
		search.RankedList{Name: "block_vector", IDs: blockVec},
		search.RankedList{Name: "lexical", IDs: lexical},
		search.RankedList{Name: "exact_title", Weight: cfg.ExactTitleWeight, IDs: exact},
	)
	ids := search.TopIDs(fused, limit)

	resp := knowledge.SearchResponse{
		Query:                   query,
		Results:                 make([]knowledge.SearchResult, 0, len(ids)),
		SemanticSearchAvailable: semantic,
		SemanticError:           semanticErr,
	}
	err = s.inReadTx(ctx, func(tx pgx.Tx) error {
		for _, id := range ids {
			set, err := s.resolvePaths(ctx, tx, id, p.AllPaths)
			if err != nil {
				return fmt.Errorf("resolve paths of result %s: %w", id, err)
			}
			resp.Results = append(resp.Results, knowledge.SearchResult{
				ID:             id,
				Paths:          set.Paths,
				PathsTruncated: set.Truncated,
			})
		}
		return nil
	})
	if err != nil {
		return knowledge.SearchResponse{}, err
	}
	return resp, nil
}

func (s *Store) embedQuery(ctx context.Context, query string) (pgvector.Vector, string, error) {
	if s.embedder == nil {
		return pgvector.Vector{}, "", fmt.Errorf("%w: no embedder configured", knowledge.ErrEmbeddingUnavailable)
	}
	vecs, err := s.embedder.Embed(ctx, []string{knowledge.NormalizeText(query)})
	if err != nil {
		return pgvector.Vector{}, "", fmt.Errorf("%w: embed query: %w", knowledge.ErrEmbeddingUnavailable, err)
	}
	if len(vecs) != 1 || len(vecs[0]) != knowledge.EmbeddingDimensions {
		return pgvector.Vector{}, "", fmt.Errorf("%w: query embedding has unexpected shape", knowledge.ErrEmbeddingUnavailable)
	}
	return pgvector.NewVector(vecs[0]), s.embedder.Model(), nil
}

// inVectorTx runs fn in a read-only transaction with a raised
// hnsw.ef_search, because the model filter is applied after the index scan.
func (s *Store) inVectorTx(ctx context.Context, fn func(pgx.Tx) error) error {
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('hnsw.ef_search', $1, true)`,
			strconv.Itoa(s.searchCfg.EFSearch)); err != nil {
			return fmt.Errorf("set hnsw.ef_search: %w", err)
		}
		return fn(tx)
	})
}

func (s *Store) documentVectorCandidates(ctx context.Context, qvec pgvector.Vector, model string) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	err := s.inVectorTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id
			FROM documents
			WHERE embedding IS NOT NULL
			  AND embedding_model = $2
			  AND id <> $4
			ORDER BY embedding <=> $1
			LIMIT $3`, qvec, model, s.searchCfg.CandidateLimit, knowledge.RootID)
		if err != nil {
			return err
		}
		ids, err = pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("document vector search: %w", err)
	}
	return ids, nil
}

func (s *Store) blockVectorCandidates(ctx context.Context, qvec pgvector.Vector, model string) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	err := s.inVectorTx(ctx, func(tx pgx.Tx) error {
		// Nearest blocks first, then one row per document ranked by its
		// best block.
		rows, err := tx.Query(ctx, `
			SELECT document_id
			FROM (
				SELECT document_id, embedding <=> $1 AS distance
				FROM content_blocks
				WHERE embedding IS NOT NULL
				  AND embedding_model = $2
				ORDER BY embedding <=> $1
				LIMIT $3
			) nearest
			WHERE document_id <> $4
			GROUP BY document_id
			ORDER BY min(distance), document_id`, qvec, model, s.searchCfg.CandidateLimit, knowledge.RootID)
		if err != nil {
			return err
		}
		ids, err = pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("block vector search: %w", err)
	}
	return ids, nil
}

// lexicalCandidates returns documents ranked by lexical signals and,
// separately, the documents whose title equals the query (case-insensitive).
func (s *Store) lexicalCandidates(ctx context.Context, query string) (ranked, exact []uuid.UUID, err error) {
	escaped := escapeLike(query)
	rows, err := s.pool.Query(ctx, `
		WITH q AS (
			SELECT websearch_to_tsquery('simple', $1) AS tsq
		),
		doc_fts AS (
			SELECT d.id AS document_id, ts_rank_cd(d.search_vector, q.tsq) AS rank
			FROM documents d CROSS JOIN q
			WHERE d.search_vector @@ q.tsq AND d.id <> $5
		),
		block_fts AS (
			SELECT b.document_id, max(ts_rank_cd(b.search_vector, q.tsq)) AS rank
			FROM content_blocks b CROSS JOIN q
			WHERE b.search_vector @@ q.tsq AND b.document_id <> $5
			GROUP BY b.document_id
		),
		title_hits AS (
			SELECT d.id AS document_id,
			       lower(d.title) = lower($1)        AS exact,
			       lower(d.title) LIKE lower($2)     AS prefix,
			       lower(d.title) LIKE lower($3)     AS substr,
			       similarity(d.title, $1)           AS sim
			FROM documents d
			WHERE d.id <> $5
			  AND (lower(d.title) LIKE lower($3) OR d.title % $1)
		),
		signals AS (
			SELECT document_id, rank::float8 AS doc_rank, 0::float8 AS block_rank,
			       false AS exact, false AS prefix, false AS substr, 0::float8 AS sim
			FROM doc_fts
			UNION ALL
			SELECT document_id, 0, rank, false, false, false, 0
			FROM block_fts
			UNION ALL
			SELECT document_id, 0, 0, exact, prefix, substr, sim
			FROM title_hits
		)
		SELECT document_id, bool_or(exact) AS exact
		FROM signals
		GROUP BY document_id
		ORDER BY bool_or(exact) DESC,
		         bool_or(prefix) DESC,
		         (0.5 * bool_or(substr)::int + max(doc_rank) + 0.5 * max(block_rank) + 0.5 * max(sim)) DESC,
		         document_id
		LIMIT $4`,
		query, escaped+"%", "%"+escaped+"%", s.searchCfg.CandidateLimit, knowledge.RootID)
	if err != nil {
		return nil, nil, fmt.Errorf("lexical search: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var isExact bool
		if err := rows.Scan(&id, &isExact); err != nil {
			return nil, nil, fmt.Errorf("lexical search: %w", err)
		}
		ranked = append(ranked, id)
		if isExact {
			exact = append(exact, id)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("lexical search: %w", err)
	}
	return ranked, exact, nil
}

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// escapeLike escapes LIKE wildcards (backslash is PostgreSQL's default
// escape character) so user input is matched literally.
func escapeLike(s string) string {
	return likeEscaper.Replace(s)
}

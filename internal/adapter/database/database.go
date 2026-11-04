// Package database is the PostgreSQL (pgx + pgvector) adapter of the
// knowledge graph. It implements knowledge.Repository and the
// indexing.JobStore port.
//
// All write operations run in explicit transactions and enforce the graph
// invariants (single root, at least one parent, exactly one primary parent,
// no cycles, reachability from root, gap-free block positions) that the
// schema alone cannot guarantee. Hierarchy mutations are serialized with a
// transaction scoped advisory lock.
//
// Typical setup (configuration is read from the environment, see Config):
//
//	store, err := database.Setup(ctx, database.WithEmbedder(embedder))
//
// or step by step on an existing pool:
//
//	if err := database.Migrate(ctx, url); err != nil { ... }
//	pool, err := database.OpenPool(ctx, url)
//	store, err := database.New(pool, database.WithEmbedder(embedder))
//	if err := store.VerifyRoot(ctx); err != nil { ... }
package database

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	pgxvec "github.com/pgvector/pgvector-go/pgx"

	env "mcp-knowledge-graph/internal/environment"
	"mcp-knowledge-graph/internal/indexing"
	"mcp-knowledge-graph/internal/knowledge"
)

// Compile time interface checks.
var (
	_ knowledge.Repository = (*Store)(nil)
	_ indexing.JobStore    = (*Store)(nil)
)

type options struct {
	cfg      *Config
	embedder knowledge.Embedder
}

// Option modifies the construction of a Store.
type Option func(*options)

// WithConfig sets an explicit configuration instead of loading it from the
// environment. A nil config keeps the environment based default.
func WithConfig(cfg *Config) Option {
	return func(o *options) { o.cfg = cfg }
}

// WithEmbedder sets the embedding provider used for search queries. It must
// be the same model that the indexing worker uses. Without an embedder,
// Search returns knowledge.ErrEmbeddingUnavailable.
func WithEmbedder(e knowledge.Embedder) Option {
	return func(o *options) { o.embedder = e }
}

// resolveOptions applies mods, loads the configuration from the environment
// if none was given and validates it. Loading panics if environment values
// cannot be parsed (see env.GetConf).
func resolveOptions(mods []Option) (options, error) {
	var o options
	for _, m := range mods {
		m(&o)
	}
	if o.cfg == nil {
		o.cfg = env.GetConf[Config]()
	}
	if err := o.cfg.Validate(); err != nil {
		return o, fmt.Errorf("invalid database config: %w", err)
	}
	return o, nil
}

// Store is the PostgreSQL implementation of knowledge.Repository.
// It is safe for concurrent use.
type Store struct {
	pool       *pgxpool.Pool
	embedder   knowledge.Embedder
	searchCfg  SearchConfig
	pathLimits knowledge.PathLimits
}

// New creates a Store on an existing pool. The pool must have the pgvector
// types registered (see OpenPool). The configuration is loaded from the
// environment unless WithConfig is given.
func New(pool *pgxpool.Pool, mods ...Option) (*Store, error) {
	if pool == nil {
		return nil, fmt.Errorf("database: pool must not be nil")
	}
	o, err := resolveOptions(mods)
	if err != nil {
		return nil, err
	}
	return newStore(pool, o), nil
}

func newStore(pool *pgxpool.Pool, o options) *Store {
	return &Store{
		pool:      pool,
		embedder:  o.embedder,
		searchCfg: o.cfg.Search,
		pathLimits: knowledge.PathLimits{
			MaxPaths: o.cfg.Paths.MaxPaths,
			MaxDepth: o.cfg.Paths.MaxDepth,
		},
	}
}

// OpenPool opens a pgx pool and registers the pgvector types on every new
// connection. The vector extension must exist, so run Migrate first.
func OpenPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		return pgxvec.RegisterTypes(ctx, conn)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}

// Setup migrates the schema of Config.URL, opens a pool, creates the Store
// and verifies the root structure. The configuration is loaded from the
// environment unless WithConfig is given. The caller must Close the store.
func Setup(ctx context.Context, mods ...Option) (*Store, error) {
	o, err := resolveOptions(mods)
	if err != nil {
		return nil, err
	}
	if err := Migrate(ctx, o.cfg.URL); err != nil {
		return nil, err
	}
	pool, err := OpenPool(ctx, o.cfg.URL)
	if err != nil {
		return nil, err
	}
	s := newStore(pool, o)
	if err := s.VerifyRoot(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the underlying pool.
func (s *Store) Close() {
	s.pool.Close()
}

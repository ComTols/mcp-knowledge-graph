package database

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Config is the configuration of the database adapter. It is loaded from
// the environment (github.com/caarlos0/env); the defaults match the local
// docker-compose setup.
type Config struct {
	// URL is the PostgreSQL connection URL. It is used by Setup; New works
	// on an existing pool and ignores it.
	URL    string       `env:"KG_DATABASE_URL" envDefault:"postgres://kg:kg@localhost:5433/kg?sslmode=disable"`
	Search SearchConfig `envPrefix:"KG_SEARCH_"`
	Paths  PathConfig   `envPrefix:"KG_PATHS_"`
}

// SearchConfig tunes the hybrid search.
type SearchConfig struct {
	// CandidateLimit is the number of candidates per ranked list.
	CandidateLimit int `env:"CANDIDATE_LIMIT" envDefault:"50"`
	// DefaultLimit is used if the caller passes a limit <= 0.
	DefaultLimit int `env:"DEFAULT_LIMIT" envDefault:"10"`
	// MaxLimit caps the number of results.
	MaxLimit int `env:"MAX_LIMIT" envDefault:"50"`
	// RRFK is the rank constant k of Reciprocal Rank Fusion.
	RRFK int `env:"RRF_K" envDefault:"60"`
	// ExactTitleWeight weights the extra list of exact title matches.
	ExactTitleWeight float64 `env:"EXACT_TITLE_WEIGHT" envDefault:"1"`
	// EFSearch is hnsw.ef_search for vector queries. It must not be smaller
	// than CandidateLimit, because the model filter is applied after the
	// index scan.
	EFSearch int `env:"HNSW_EF_SEARCH" envDefault:"100"`
	// MaxQueryBytes is the maximum query length in bytes. 4000 bytes hold
	// 1000 characters of any UTF-8 text.
	MaxQueryBytes int `env:"MAX_QUERY_BYTES" envDefault:"4000"`
}

// PathConfig bounds the enumeration of alternative root paths.
type PathConfig struct {
	MaxPaths int `env:"MAX_PATHS" envDefault:"10"`
	MaxDepth int `env:"MAX_DEPTH" envDefault:"64"`
}

// Validate checks the configuration and reports all problems at once.
func (c *Config) Validate() error {
	var errs []error
	if c.URL == "" {
		errs = append(errs, errors.New("KG_DATABASE_URL must not be empty"))
	} else if _, err := pgxpool.ParseConfig(c.URL); err != nil {
		errs = append(errs, fmt.Errorf("KG_DATABASE_URL is invalid: %w", err))
	}

	s := c.Search
	positive := []struct {
		name  string
		value int
	}{
		{"KG_SEARCH_CANDIDATE_LIMIT", s.CandidateLimit},
		{"KG_SEARCH_DEFAULT_LIMIT", s.DefaultLimit},
		{"KG_SEARCH_MAX_LIMIT", s.MaxLimit},
		{"KG_SEARCH_RRF_K", s.RRFK},
		{"KG_SEARCH_HNSW_EF_SEARCH", s.EFSearch},
		{"KG_SEARCH_MAX_QUERY_BYTES", s.MaxQueryBytes},
		{"KG_PATHS_MAX_PATHS", c.Paths.MaxPaths},
		{"KG_PATHS_MAX_DEPTH", c.Paths.MaxDepth},
	}
	for _, p := range positive {
		if p.value <= 0 {
			errs = append(errs, fmt.Errorf("%s must be positive, got %d", p.name, p.value))
		}
	}
	if s.ExactTitleWeight <= 0 {
		errs = append(errs, fmt.Errorf("KG_SEARCH_EXACT_TITLE_WEIGHT must be positive, got %g", s.ExactTitleWeight))
	}
	if s.DefaultLimit > s.MaxLimit {
		errs = append(errs, fmt.Errorf("KG_SEARCH_DEFAULT_LIMIT (%d) must not exceed KG_SEARCH_MAX_LIMIT (%d)", s.DefaultLimit, s.MaxLimit))
	}
	if s.EFSearch < s.CandidateLimit {
		errs = append(errs, fmt.Errorf("KG_SEARCH_HNSW_EF_SEARCH (%d) must be at least KG_SEARCH_CANDIDATE_LIMIT (%d)", s.EFSearch, s.CandidateLimit))
	}
	if s.EFSearch > 1000 {
		errs = append(errs, fmt.Errorf("KG_SEARCH_HNSW_EF_SEARCH must not exceed 1000 (pgvector limit), got %d", s.EFSearch))
	}
	return errors.Join(errs...)
}

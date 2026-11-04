package database

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	env "mcp-knowledge-graph/internal/environment"
)

func TestConfigDefaults(t *testing.T) {
	cfg := env.GetDefaultConf[Config]()
	require.NoError(t, cfg.Validate())
	assert.Equal(t, Config{
		URL: "postgres://kg:kg@localhost:5433/kg?sslmode=disable",
		Search: SearchConfig{
			CandidateLimit:   50,
			DefaultLimit:     10,
			MaxLimit:         50,
			RRFK:             60,
			ExactTitleWeight: 1,
			EFSearch:         100,
			MaxQueryBytes:    4000,
		},
		Paths: PathConfig{MaxPaths: 10, MaxDepth: 64},
	}, *cfg)
}

func TestConfigFromEnvironment(t *testing.T) {
	t.Setenv("KG_DATABASE_URL", "postgres://u:p@db:5432/x")
	t.Setenv("KG_SEARCH_DEFAULT_LIMIT", "5")
	t.Setenv("KG_SEARCH_EXACT_TITLE_WEIGHT", "2.5")
	t.Setenv("KG_PATHS_MAX_PATHS", "3")

	cfg := env.GetConf[Config]()
	require.NoError(t, cfg.Validate())
	assert.Equal(t, "postgres://u:p@db:5432/x", cfg.URL)
	assert.Equal(t, 5, cfg.Search.DefaultLimit)
	assert.InDelta(t, 2.5, cfg.Search.ExactTitleWeight, 0)
	assert.Equal(t, 3, cfg.Paths.MaxPaths)
	assert.Equal(t, 50, cfg.Search.CandidateLimit, "unset values keep their default")
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"empty url", func(c *Config) { c.URL = "" }, "KG_DATABASE_URL"},
		{"invalid url", func(c *Config) { c.URL = "postgres://host:notaport/db" }, "KG_DATABASE_URL"},
		{"candidate limit", func(c *Config) { c.Search.CandidateLimit = 0 }, "KG_SEARCH_CANDIDATE_LIMIT"},
		{"default limit", func(c *Config) { c.Search.DefaultLimit = -1 }, "KG_SEARCH_DEFAULT_LIMIT"},
		{"limit order", func(c *Config) { c.Search.DefaultLimit = 60 }, "must not exceed KG_SEARCH_MAX_LIMIT"},
		{"rrf k", func(c *Config) { c.Search.RRFK = 0 }, "KG_SEARCH_RRF_K"},
		{"exact weight", func(c *Config) { c.Search.ExactTitleWeight = 0 }, "KG_SEARCH_EXACT_TITLE_WEIGHT"},
		{"ef search below candidates", func(c *Config) { c.Search.EFSearch = 10 }, "at least KG_SEARCH_CANDIDATE_LIMIT"},
		{"ef search above pgvector max", func(c *Config) { c.Search.EFSearch = 1001 }, "must not exceed 1000"},
		{"query bytes", func(c *Config) { c.Search.MaxQueryBytes = 0 }, "KG_SEARCH_MAX_QUERY_BYTES"},
		{"max paths", func(c *Config) { c.Paths.MaxPaths = 0 }, "KG_PATHS_MAX_PATHS"},
		{"max depth", func(c *Config) { c.Paths.MaxDepth = 0 }, "KG_PATHS_MAX_DEPTH"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := env.GetDefaultConf[Config]()
			tc.mutate(cfg)
			assert.ErrorContains(t, cfg.Validate(), tc.want)

			_, err := resolveOptions([]Option{WithConfig(cfg)})
			assert.ErrorContains(t, err, tc.want, "constructors validate the config")
		})
	}
}

func TestResolveOptionsLoadsEnvironmentByDefault(t *testing.T) {
	t.Setenv("KG_SEARCH_MAX_LIMIT", "20")
	o, err := resolveOptions(nil)
	require.NoError(t, err)
	assert.Equal(t, 20, o.cfg.Search.MaxLimit)

	explicit := env.GetDefaultConf[Config]()
	o, err = resolveOptions([]Option{WithConfig(explicit)})
	require.NoError(t, err)
	assert.Same(t, explicit, o.cfg, "WithConfig replaces the environment")

	o, err = resolveOptions([]Option{WithConfig(nil)})
	require.NoError(t, err)
	assert.Equal(t, 20, o.cfg.Search.MaxLimit, "a nil config falls back to the environment")
}

func TestNewRejectsNilPool(t *testing.T) {
	_, err := New(nil)
	assert.Error(t, err)
}

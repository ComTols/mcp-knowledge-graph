package indexing_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	env "mcp-knowledge-graph/internal/environment"
	"mcp-knowledge-graph/internal/indexing"
)

func TestConfigDefaults(t *testing.T) {
	cfg := env.GetDefaultConf[indexing.Config]()
	require.NoError(t, cfg.Validate())
	assert.Equal(t, indexing.Config{
		BatchSize:     16,
		Lease:         5 * time.Minute,
		MaxAttempts:   5,
		BaseBackoff:   10 * time.Second,
		MaxBackoff:    10 * time.Minute,
		MaxInputBytes: 0,
		PollInterval:  2 * time.Second,
	}, *cfg)
}

func TestConfigFromEnvironment(t *testing.T) {
	t.Setenv("KG_INDEXING_BATCH_SIZE", "3")
	t.Setenv("KG_INDEXING_LEASE", "90s")
	t.Setenv("KG_INDEXING_MAX_INPUT_BYTES", "32768")

	cfg := env.GetConf[indexing.Config]()
	require.NoError(t, cfg.Validate())
	assert.Equal(t, 3, cfg.BatchSize)
	assert.Equal(t, 90*time.Second, cfg.Lease)
	assert.Equal(t, 32768, cfg.MaxInputBytes)
	assert.Equal(t, 5, cfg.MaxAttempts, "unset values keep their default")
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*indexing.Config)
		want   string
	}{
		{"batch size", func(c *indexing.Config) { c.BatchSize = 0 }, "KG_INDEXING_BATCH_SIZE"},
		{"lease", func(c *indexing.Config) { c.Lease = 0 }, "KG_INDEXING_LEASE"},
		{"attempts", func(c *indexing.Config) { c.MaxAttempts = -1 }, "KG_INDEXING_MAX_ATTEMPTS"},
		{"base backoff", func(c *indexing.Config) { c.BaseBackoff = 0 }, "KG_INDEXING_BASE_BACKOFF"},
		{"backoff order", func(c *indexing.Config) { c.MaxBackoff = time.Second }, "must not be smaller"},
		{"input bytes", func(c *indexing.Config) { c.MaxInputBytes = -1 }, "KG_INDEXING_MAX_INPUT_BYTES"},
		{"poll interval", func(c *indexing.Config) { c.PollInterval = 0 }, "KG_INDEXING_POLL_INTERVAL"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := env.GetDefaultConf[indexing.Config]()
			tc.mutate(cfg)
			assert.ErrorContains(t, cfg.Validate(), tc.want)
		})
	}

	// All problems are reported at once.
	err := (&indexing.Config{}).Validate()
	assert.ErrorContains(t, err, "KG_INDEXING_BATCH_SIZE")
	assert.ErrorContains(t, err, "KG_INDEXING_POLL_INTERVAL")
}

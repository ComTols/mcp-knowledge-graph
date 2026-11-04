package openai_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mcp-knowledge-graph/internal/adapter/embedding/openai"
	env "mcp-knowledge-graph/internal/environment"
)

func TestConfigDefaults(t *testing.T) {
	cfg := env.GetDefaultConf[openai.Config]()
	require.NoError(t, cfg.Validate())
	assert.Equal(t, openai.Config{
		BaseURL: "http://localhost:11434/v1",
		Model:   "bge-m3",
		Timeout: 60 * time.Second,
	}, *cfg)
}

func TestConfigFromEnvironment(t *testing.T) {
	t.Setenv("KG_EMBEDDING_BASE_URL", "https://api.openai.com/v1")
	t.Setenv("KG_EMBEDDING_MODEL", "text-embedding-3-large")
	t.Setenv("KG_EMBEDDING_API_KEY", "secret")
	t.Setenv("KG_EMBEDDING_DIMENSIONS", "1024")

	e, err := openai.New()
	require.NoError(t, err)
	assert.Equal(t, "text-embedding-3-large@1024", e.Model())
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*openai.Config)
		want   string
	}{
		{"empty url", func(c *openai.Config) { c.BaseURL = "" }, "KG_EMBEDDING_BASE_URL"},
		{"relative url", func(c *openai.Config) { c.BaseURL = "localhost:11434" }, "KG_EMBEDDING_BASE_URL"},
		{"no host", func(c *openai.Config) { c.BaseURL = "http://" }, "KG_EMBEDDING_BASE_URL"},
		{"model", func(c *openai.Config) { c.Model = "" }, "KG_EMBEDDING_MODEL"},
		{"dimensions", func(c *openai.Config) { c.Dimensions = 512 }, "KG_EMBEDDING_DIMENSIONS"},
		{"timeout", func(c *openai.Config) { c.Timeout = 0 }, "KG_EMBEDDING_TIMEOUT"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := env.GetDefaultConf[openai.Config]()
			tc.mutate(cfg)
			assert.ErrorContains(t, cfg.Validate(), tc.want)
			_, err := openai.New(openai.WithConfig(cfg))
			assert.ErrorContains(t, err, tc.want, "New validates the config")
		})
	}
}

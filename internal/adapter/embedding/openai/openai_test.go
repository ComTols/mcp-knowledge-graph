package openai_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mcp-knowledge-graph/internal/adapter/embedding/openai"
	env "mcp-knowledge-graph/internal/environment"
	"mcp-knowledge-graph/internal/knowledge"
)

// newEmbedder builds an embedder from the default configuration adjusted by
// modify, independent of the process environment.
func newEmbedder(t *testing.T, modify func(*openai.Config)) (*openai.Embedder, error) {
	t.Helper()
	cfg := env.GetDefaultConf[openai.Config]()
	modify(cfg)
	return openai.New(openai.WithConfig(cfg), openai.WithHTTPClient(http.DefaultClient))
}

func TestEmbed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/embeddings", r.URL.Path)
		assert.Equal(t, "Bearer secret", r.Header.Get("Authorization"))
		var req struct {
			Model      string   `json:"model"`
			Input      []string `json:"input"`
			Dimensions int      `json:"dimensions"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Equal(t, "m", req.Model)
		assert.Equal(t, 1024, req.Dimensions)

		// Answer in reverse order to verify index handling.
		type item struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		var data []item
		for i := len(req.Input) - 1; i >= 0; i-- {
			v := make([]float32, knowledge.EmbeddingDimensions)
			v[0] = float32(i)
			data = append(data, item{Index: i, Embedding: v})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer srv.Close()

	e, err := newEmbedder(t, func(c *openai.Config) {
		c.BaseURL, c.Model, c.APIKey, c.Dimensions = srv.URL+"/v1/", "m", "secret", 1024
	})
	require.NoError(t, err)
	assert.Equal(t, "m@1024", e.Model())

	out, err := e.Embed(t.Context(), []string{"a", "b", "c"})
	require.NoError(t, err)
	require.Len(t, out, 3)
	for i, v := range out {
		assert.InDelta(t, float32(i), v[0], 0)
	}
}

func TestEmbedErrors(t *testing.T) {
	t.Run("http error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "model not found", http.StatusNotFound)
		}))
		defer srv.Close()
		e, err := newEmbedder(t, func(c *openai.Config) { c.BaseURL, c.Model = srv.URL, "m" })
		require.NoError(t, err)
		_, err = e.Embed(t.Context(), []string{"a"})
		assert.ErrorContains(t, err, "model not found")
	})

	t.Run("wrong dimension", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[1,2,3]}]}`))
		}))
		defer srv.Close()
		e, err := newEmbedder(t, func(c *openai.Config) { c.BaseURL, c.Model = srv.URL, "m" })
		require.NoError(t, err)
		_, err = e.Embed(t.Context(), []string{"a"})
		assert.ErrorContains(t, err, "3 dimensions")
	})

	t.Run("invalid config", func(t *testing.T) {
		_, err := openai.New(openai.WithConfig(&openai.Config{}))
		assert.Error(t, err)
	})
}

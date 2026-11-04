package hashing_test

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mcp-knowledge-graph/internal/adapter/embedding/hashing"
	"mcp-knowledge-graph/internal/knowledge"
)

func cosine(a, b []float32) float64 {
	var dot float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
	}
	return dot // vectors are normalized
}

func TestVector(t *testing.T) {
	v := hashing.Vector("Go HTTP Server Hardening")
	require.Len(t, v, knowledge.EmbeddingDimensions)

	var norm float64
	for _, x := range v {
		norm += float64(x) * float64(x)
	}
	assert.InDelta(t, 1, math.Sqrt(norm), 1e-5)

	assert.Equal(t, v, hashing.Vector("Go HTTP Server Hardening"), "deterministic")
	assert.Greater(t,
		cosine(v, hashing.Vector("hardening an http server")),
		cosine(v, hashing.Vector("sourdough bread in the oven")))

	empty := hashing.Vector("")
	assert.InDelta(t, 1, cosine(empty, empty), 1e-6)
}

func TestEmbedder(t *testing.T) {
	var e hashing.Embedder
	out, err := e.Embed(t.Context(), []string{"a", "b"})
	require.NoError(t, err)
	assert.Len(t, out, 2)
	assert.Equal(t, hashing.ModelName, e.Model())
}

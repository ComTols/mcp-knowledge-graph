// Package hashing provides a deterministic, dependency free stub embedder
// based on feature hashing of word tokens and character trigrams.
//
// It is NOT a semantic model: texts are similar only if they share words or
// character sequences. It exists for tests and local development where the
// vector code paths must work without an external model.
package hashing

import (
	"context"
	"hash/fnv"
	"math"
	"strings"
	"unicode"

	"mcp-knowledge-graph/internal/knowledge"
)

// ModelName identifies vectors produced by this embedder.
const ModelName = "hashing-stub-v1"

// Embedder is the feature hashing stub. The zero value is ready to use.
type Embedder struct{}

// Model implements knowledge.Embedder.
func (Embedder) Model() string { return ModelName }

// Embed implements knowledge.Embedder.
func (Embedder) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	out := make([][]float32, len(inputs))
	for i, in := range inputs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		out[i] = Vector(in)
	}
	return out, nil
}

// Vector returns the L2 normalized hashing vector of s.
func Vector(s string) []float32 {
	v := make([]float64, knowledge.EmbeddingDimensions)
	words := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	for _, w := range words {
		add(v, "w:"+w, 1)
		padded := []rune(" " + w + " ")
		for i := 0; i+3 <= len(padded); i++ {
			add(v, "t:"+string(padded[i:i+3]), 0.5)
		}
	}

	var norm float64
	for _, x := range v {
		norm += x * x
	}
	out := make([]float32, len(v))
	if norm == 0 {
		// Empty input: a fixed unit vector keeps cosine distance defined.
		out[0] = 1
		return out
	}
	norm = math.Sqrt(norm)
	for i, x := range v {
		out[i] = float32(x / norm)
	}
	return out
}

func add(v []float64, feature string, weight float64) {
	h := fnv.New64a()
	_, _ = h.Write([]byte(feature))
	sum := h.Sum64()
	idx := sum % uint64(len(v))
	if sum&(1<<63) != 0 {
		weight = -weight
	}
	v[idx] += weight
}

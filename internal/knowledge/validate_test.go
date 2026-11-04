package knowledge_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mcp-knowledge-graph/internal/knowledge"
)

func ptr[T any](v T) *T { return &v }

func TestNormalizeCreateInput(t *testing.T) {
	p1, p2 := uuid.New(), uuid.New()
	valid := knowledge.CreateDocumentInput{
		Title:     "  Go HTTP Server  ",
		Summary:   " Hardening ",
		ParentIDs: []uuid.UUID{p1, p2, p1},
		Blocks:    []string{"  keep *as is*  \n"},
		Sources: []knowledge.SourceInput{
			{URI: " https://example.invalid/c/1 ", Title: ptr("  ")},
			{URI: "https://example.invalid/c/1", Kind: knowledge.SourceKindOther},
		},
	}

	t.Run("valid input is normalized", func(t *testing.T) {
		got, err := knowledge.NormalizeCreateInput(valid)
		require.NoError(t, err)
		assert.Equal(t, "Go HTTP Server", got.Title)
		assert.Equal(t, "Hardening", got.Summary)
		assert.Equal(t, []uuid.UUID{p1, p2}, got.ParentIDs, "dedupe must keep the first parent first")
		assert.Equal(t, []string{"  keep *as is*  \n"}, got.Blocks, "block content must not be rewritten")
		require.Len(t, got.Sources, 1)
		assert.Equal(t, knowledge.SourceKindConversation, got.Sources[0].Kind)
		assert.Equal(t, "https://example.invalid/c/1", got.Sources[0].URI)
		assert.Nil(t, got.Sources[0].Title, "blank titles become nil")
	})

	tests := []struct {
		name   string
		mutate func(*knowledge.CreateDocumentInput)
	}{
		{"empty title", func(in *knowledge.CreateDocumentInput) { in.Title = " \t" }},
		{"empty summary", func(in *knowledge.CreateDocumentInput) { in.Summary = "" }},
		{"no parents", func(in *knowledge.CreateDocumentInput) { in.ParentIDs = nil }},
		{"nil parent", func(in *knowledge.CreateDocumentInput) { in.ParentIDs = []uuid.UUID{uuid.Nil} }},
		{"blank block", func(in *knowledge.CreateDocumentInput) { in.Blocks = []string{"ok", "  \n"} }},
		{"blank source uri", func(in *knowledge.CreateDocumentInput) { in.Sources = []knowledge.SourceInput{{URI: " "}} }},
		{"unknown source kind", func(in *knowledge.CreateDocumentInput) {
			in.Sources = []knowledge.SourceInput{{URI: "x", Kind: "mail"}}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := valid
			tc.mutate(&in)
			_, err := knowledge.NormalizeCreateInput(in)
			assert.ErrorIs(t, err, knowledge.ErrInvalidInput)
		})
	}
}

func TestNormalizeMetadataUpdate(t *testing.T) {
	_, err := knowledge.NormalizeMetadataUpdate(knowledge.MetadataUpdate{})
	assert.ErrorIs(t, err, knowledge.ErrInvalidInput)

	_, err = knowledge.NormalizeMetadataUpdate(knowledge.MetadataUpdate{Title: ptr(" ")})
	assert.ErrorIs(t, err, knowledge.ErrInvalidInput)

	_, err = knowledge.NormalizeMetadataUpdate(knowledge.MetadataUpdate{Summary: ptr("")})
	assert.ErrorIs(t, err, knowledge.ErrInvalidInput)

	got, err := knowledge.NormalizeMetadataUpdate(knowledge.MetadataUpdate{Title: ptr(" T ")})
	require.NoError(t, err)
	assert.Equal(t, "T", *got.Title)
	assert.Nil(t, got.Summary)
}

func TestEmbeddingInput(t *testing.T) {
	assert.Equal(t, "Title: A\nSummary: B", knowledge.DocumentEmbeddingInput("A", "B"))

	// CRLF and decomposed accents normalize to the same input and hash.
	decomposed := "Cafe\u0301\r\nLine"
	composed := "Caf\u00e9\nLine"
	assert.Equal(t, composed, knowledge.BlockEmbeddingInput(decomposed))
	assert.Equal(t,
		knowledge.InputHash(knowledge.BlockEmbeddingInput(composed)),
		knowledge.InputHash(knowledge.BlockEmbeddingInput(decomposed)))

	assert.Len(t, knowledge.InputHash("x"), 64)
	assert.NotEqual(t, knowledge.InputHash("x"), knowledge.InputHash("y"))
}

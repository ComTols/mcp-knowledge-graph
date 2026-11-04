//go:build integration

package mcpserver

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mcp-knowledge-graph/internal/adapter/embedding/openai"
)

// Acceptance 10: semantic search finds related concepts without shared
// words. It calls the real embedding provider configured by KG_EMBEDDING_*
// and therefore only runs with KG_TEST_OPENAI=1. It embeds five short
// texts, so the cost is negligible.
func TestSemanticSearchWithRealEmbeddings(t *testing.T) {
	if os.Getenv("KG_TEST_OPENAI") != "1" {
		t.Skip("set KG_TEST_OPENAI=1 and KG_EMBEDDING_* to run against a real embedding provider")
	}
	embedder, err := openai.New()
	require.NoError(t, err)
	e := newEnv(t, withEmbedder(embedder))

	target := callOK[CreateDocumentOutput](t, e.CS, ToolCreateDocument, map[string]any{
		"title":      "Go HTTP Server Timeouts",
		"summary":    "Setting ReadHeaderTimeout and WriteTimeout in net/http.",
		"parent_ids": []string{rootID},
		"blocks":     []map[string]any{{"content": "Configure ReadHeaderTimeout so slow clients cannot exhaust connections."}},
	})
	e.createDoc(t, "Sourdough Baking", []string{rootID})
	e.createDoc(t, "Alpine Hiking Gear", []string{rootID})
	e.indexAll(t, embedder)

	// No word of the query occurs in the target document.
	out := callOK[SearchOutput](t, e.CS, ToolSearch, map[string]any{
		"query": "protecting a backend against sluggish callers that never finish sending",
	})
	assert.True(t, out.SemanticSearchAvailable)
	require.NotEmpty(t, out.Results)
	assert.Equal(t, target.DocumentID, out.Results[0].ID)
}

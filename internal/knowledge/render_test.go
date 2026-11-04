package knowledge_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"mcp-knowledge-graph/internal/knowledge"
)

func TestRenderMarkdownFull(t *testing.T) {
	c1 := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	c2 := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	doc := knowledge.Document{
		DocumentSummary: knowledge.DocumentSummary{
			Title:   "Go HTTP Server Hardening",
			Summary: "Security measures for HTTP servers in Go.",
		},
		Children: []knowledge.ChildRef{
			{ID: c1, Title: "HTTP Timeouts", Summary: "Protection against slow clients."},
			{ID: c2, Title: "Request [Validation]", Summary: "Validation\nof incoming requests."},
		},
		Blocks: []knowledge.Block{
			{Position: 0, Content: "HTTP servers should define safe timeouts ...\n"},
			{Position: 1, Content: "```go\nsrv := &http.Server{}\n```"},
		},
		Sources: []knowledge.Source{
			{URI: "https://example.invalid/permanent/conversation/123", Title: ptr("Original conversation")},
			{URI: "https://example.invalid/other"},
		},
	}

	want := "# Go HTTP Server Hardening\n" +
		"\n" +
		"Security measures for HTTP servers in Go.\n" +
		"\n" +
		"## Subtopics\n" +
		"\n" +
		"- [HTTP Timeouts](document://11111111-1111-1111-1111-111111111111) — Protection against slow clients.\n" +
		"- [Request \\[Validation\\]](document://22222222-2222-2222-2222-222222222222) — Validation of incoming requests.\n" +
		"\n" +
		"## Content\n" +
		"\n" +
		"HTTP servers should define safe timeouts ...\n" +
		"\n" +
		"```go\nsrv := &http.Server{}\n```\n" +
		"\n" +
		"## Sources\n" +
		"\n" +
		"- [Original conversation](https://example.invalid/permanent/conversation/123)\n" +
		"- [https://example.invalid/other](https://example.invalid/other)\n"

	assert.Equal(t, want, knowledge.RenderMarkdown(doc))
}

func TestRenderMarkdownOmitsEmptySections(t *testing.T) {
	doc := knowledge.Document{DocumentSummary: knowledge.DocumentSummary{Title: "Leer", Summary: "Nur Metadaten."}}
	assert.Equal(t, "# Leer\n\nNur Metadaten.\n", knowledge.RenderMarkdown(doc))
}

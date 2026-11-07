package knowledge_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"mcp-knowledge-graph/internal/knowledge"
)

func TestRenderViewerMarkdownFull(t *testing.T) {
	p1 := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	p2 := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	c1 := uuid.MustParse("33333333-3333-3333-3333-333333333333")
	c2 := uuid.MustParse("44444444-4444-4444-4444-444444444444")
	doc := knowledge.Document{
		DocumentSummary: knowledge.DocumentSummary{
			Title:   "Go HTTP Server Hardening",
			Summary: "Security measures for HTTP servers in Go.",
		},
		Parents: []knowledge.ParentRef{
			{ID: p1, Title: "Backend", Summary: "Reliable backend structures.", IsPrimary: true},
			{ID: p2, Title: "Go", Summary: "  "},
		},
		Children: []knowledge.ChildRef{
			{ID: c1, Title: "HTTP Timeouts", Summary: "Protection against slow clients."},
			{ID: c2, Title: "Request [Validation]", Summary: "Validation\n\nof incoming requests."},
		},
		Blocks: []knowledge.Block{
			{Position: 0, Content: "HTTP servers should define safe timeouts ...\n"},
			{Position: 1, Content: "```go\nsrv := &http.Server{}\n```"},
		},
		Sources: []knowledge.Source{
			{URI: "https://example.invalid/permanent/conversation/123", Title: ptr("Original conversation")},
			{URI: "https://example.invalid/a (b)"},
		},
	}

	want := "# Go HTTP Server Hardening\n" +
		"\n" +
		"Security measures for HTTP servers in Go.\n" +
		"\n" +
		"---\n" +
		"\n" +
		"## Parents\n" +
		"\n" +
		"- [Backend](https://kg.example.com/ui/viewer?id=11111111-1111-1111-1111-111111111111)<br>Reliable backend structures.\n" +
		"- [Go](https://kg.example.com/ui/viewer?id=22222222-2222-2222-2222-222222222222)\n" +
		"\n" +
		"## Children\n" +
		"\n" +
		"- [HTTP Timeouts](https://kg.example.com/ui/viewer?id=33333333-3333-3333-3333-333333333333)<br>Protection against slow clients.\n" +
		"- [Request \\[Validation\\]](https://kg.example.com/ui/viewer?id=44444444-4444-4444-4444-444444444444)<br>Validation of incoming requests.\n" +
		"\n" +
		"---\n" +
		"\n" +
		"HTTP servers should define safe timeouts ...\n" +
		"\n" +
		"```go\nsrv := &http.Server{}\n```\n" +
		"\n" +
		"## Sources\n" +
		"\n" +
		"- [Original conversation](https://example.invalid/permanent/conversation/123)\n" +
		"- [https://example.invalid/a (b)](https://example.invalid/a%20%28b%29)\n"

	assert.Equal(t, want, knowledge.RenderViewerMarkdown(doc, "https://kg.example.com"))
}

func TestRenderViewerMarkdownOmitsEmptySections(t *testing.T) {
	doc := knowledge.Document{DocumentSummary: knowledge.DocumentSummary{Title: "Leer", Summary: "Nur Metadaten."}}
	assert.Equal(t, "# Leer\n\nNur Metadaten.\n\n---\n", knowledge.RenderViewerMarkdown(doc, ""))
}

//go:build integration

package database_test

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mcp-knowledge-graph/internal/knowledge"
)

func blockContents(blocks []knowledge.Block) []string {
	out := make([]string, len(blocks))
	for i, b := range blocks {
		out[i] = b.Content
	}
	return out
}

func TestSources(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := t.Context()
	a := db.create(t, "A", "a", root)
	b := db.create(t, "B", "b", root)
	const uri = "https://example.invalid/permanent/conversation/123"

	s1, err := db.Store.AddSource(ctx, a.ID, knowledge.SourceInput{URI: uri, Title: ptr("Chat")})
	require.NoError(t, err)
	s2, err := db.Store.AddSource(ctx, a.ID, knowledge.SourceInput{URI: " " + uri + " ", Kind: knowledge.SourceKindOther})
	require.NoError(t, err)
	assert.Equal(t, s1, s2, "idempotent per document and uri")

	_, err = db.Store.AddSource(ctx, b.ID, knowledge.SourceInput{URI: uri})
	require.NoError(t, err, "the same conversation may feed several documents")

	_, err = db.Store.AddSource(ctx, a.ID, knowledge.SourceInput{URI: ""})
	assert.ErrorIs(t, err, knowledge.ErrInvalidInput)
	_, err = db.Store.AddSource(ctx, uuid.New(), knowledge.SourceInput{URI: uri})
	assert.ErrorIs(t, err, knowledge.ErrNotFound)

	sources, err := db.Store.GetSources(ctx, a.ID)
	require.NoError(t, err)
	require.Len(t, sources, 1)
	assert.Equal(t, knowledge.SourceKindConversation, sources[0].Kind)
	assert.Equal(t, "Chat", *sources[0].Title)

	s, err := db.Store.GetSummary(ctx, a.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), s.Version, "only the first insert changes the document")
}

// Acceptance 12: rendering.
func TestRenderDocument(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := t.Context()

	doc, err := db.Store.CreateDocument(ctx, knowledge.CreateDocumentInput{
		Title:     "Go HTTP Server Hardening",
		Summary:   "Security measures for HTTP servers in Go.",
		ParentIDs: []uuid.UUID{root},
		Blocks:    []string{"HTTP servers should define safe timeouts ...", "Middleware can validate incoming requests ..."},
		Sources:   []knowledge.SourceInput{{URI: "https://example.invalid/permanent/conversation/123", Title: ptr("Original conversation")}},
	})
	require.NoError(t, err)
	timeouts := db.create(t, "HTTP Timeouts", "Protection against slow clients.", doc.ID)
	validation := db.create(t, "Request Validation", "Validation of incoming requests.", doc.ID)

	md, err := db.Store.RenderDocument(ctx, doc.ID, "https://kg.example.com")
	require.NoError(t, err)
	want := "# Go HTTP Server Hardening\n\n" +
		"Security measures for HTTP servers in Go.\n\n" +
		"---\n\n" +
		"## Parents\n\n" +
		"- [root](https://kg.example.com/ui/viewer?id=" + root.String() + ")<br>Top-level entry point of the knowledge base.\n\n" +
		"## Children\n\n" +
		"- [HTTP Timeouts](https://kg.example.com/ui/viewer?id=" + timeouts.ID.String() + ")<br>Protection against slow clients.\n" +
		"- [Request Validation](https://kg.example.com/ui/viewer?id=" + validation.ID.String() + ")<br>Validation of incoming requests.\n\n" +
		"---\n\n" +
		"HTTP servers should define safe timeouts ...\n\n" +
		"Middleware can validate incoming requests ...\n\n" +
		"## Sources\n\n" +
		"- [Original conversation](https://example.invalid/permanent/conversation/123)\n"
	assert.Equal(t, want, md)

	// Child summaries are rendered from the children, never stored in the
	// parent: the full text of the parent does not contain them.
	fulltext, err := db.Store.GetFulltext(ctx, doc.ID)
	require.NoError(t, err)
	for _, b := range fulltext.Blocks {
		assert.NotContains(t, b.Content, "langsamen Clients")
	}

	// Updating a child summary is reflected in the next rendering.
	_, err = db.Store.UpdateDocumentMetadata(ctx, timeouts.ID, timeouts.Version,
		knowledge.MetadataUpdate{Summary: ptr("Read/Write/Idle-Timeouts.")})
	require.NoError(t, err)
	md, err = db.Store.RenderDocument(ctx, doc.ID, "")
	require.NoError(t, err)
	assert.True(t, strings.Contains(md, "<br>Read/Write/Idle-Timeouts.\n"))
	assert.NotContains(t, md, "Protection against slow clients.")

	// Likewise, the parent summary is rendered from the parent.
	md, err = db.Store.RenderDocument(ctx, timeouts.ID, "")
	require.NoError(t, err)
	assert.Contains(t, md, "## Parents\n\n- [Go HTTP Server Hardening](/ui/viewer?id="+doc.ID.String()+")<br>Security measures for HTTP servers in Go.\n")
	_, err = db.Store.UpdateDocumentMetadata(ctx, doc.ID, doc.Version,
		knowledge.MetadataUpdate{Summary: ptr("Hardened HTTP servers.")})
	require.NoError(t, err)
	md, err = db.Store.RenderDocument(ctx, timeouts.ID, "")
	require.NoError(t, err)
	assert.Contains(t, md, "<br>Hardened HTTP servers.\n")
	assert.NotContains(t, md, "Security measures for HTTP servers in Go.")
}

// Acceptance 3 + 12: aggregated view.
func TestGetDocument(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := t.Context()
	parent := db.create(t, "Parent", "p", root)
	doc, err := db.Store.CreateDocument(ctx, knowledge.CreateDocumentInput{
		Title: "Doc", Summary: "d", ParentIDs: []uuid.UUID{parent.ID, root},
		Blocks: []string{"b0"}, Sources: []knowledge.SourceInput{{URI: "u"}},
	})
	require.NoError(t, err)
	child := db.create(t, "Child", "c", doc.ID)

	got, err := db.Store.GetDocument(ctx, doc.ID)
	require.NoError(t, err)
	assert.Equal(t, doc.DocumentSummary, got.DocumentSummary)
	assert.Equal(t, []knowledge.ParentRef{
		{ID: parent.ID, Title: "Parent", Summary: "p", IsPrimary: true},
		{ID: root, Title: "root", Summary: "Top-level entry point of the knowledge base.", IsPrimary: false},
	}, got.Parents)
	assert.Equal(t, []knowledge.ChildRef{{ID: child.ID, Title: "Child", Summary: "c", IsPrimary: true}}, got.Children)
	assert.Equal(t, []string{"b0"}, blockContents(got.Blocks))
	require.Len(t, got.Sources, 1)
	assert.Equal(t, "u", got.Sources[0].URI)
}

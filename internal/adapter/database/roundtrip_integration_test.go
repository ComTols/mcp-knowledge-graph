//go:build integration

package database_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"

	"mcp-knowledge-graph/internal/adapter/database"
	"mcp-knowledge-graph/internal/adapter/embedding/hashing"
	"mcp-knowledge-graph/internal/knowledge"
)

// Acceptance 15: a topic with several parents and sources is fully
// reconstructable after a database restart.
//
// With testcontainers a dedicated PostgreSQL container is stopped and
// started again. With KG_TEST_DATABASE_URL the server cannot be restarted
// by the test, so only all client connections are re-established.
func TestRoundtripAfterRestart(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	dbURL, restart := roundtripDatabase(t)
	store, err := database.Setup(ctx, database.WithConfig(testConfig(dbURL, nil)), database.WithEmbedder(hashing.Embedder{}))
	require.NoError(t, err)

	minkter, err := store.CreateDocument(ctx, knowledge.CreateDocumentInput{Title: "Minkter", Summary: "m", ParentIDs: []uuid.UUID{root}})
	require.NoError(t, err)
	ai, err := store.CreateDocument(ctx, knowledge.CreateDocumentInput{Title: "AI", Summary: "k", ParentIDs: []uuid.UUID{root}})
	require.NoError(t, err)
	doc, err := store.CreateDocument(ctx, knowledge.CreateDocumentInput{
		Title: "Analytics with Go", Summary: "Statistics in Go.", ParentIDs: []uuid.UUID{ai.ID, minkter.ID},
		Blocks:  []string{"# Part 1\n\n```go\nfmt.Println(42)\n```", "Part 2"},
		Sources: []knowledge.SourceInput{{URI: "s3://bucket/conversations/1.md", Title: ptr("Chat 1")}, {URI: "https://example.invalid/2", Kind: knowledge.SourceKindOther}},
	})
	require.NoError(t, err)
	indexWith(t, store, hashing.Embedder{})

	before, err := store.GetDocument(ctx, doc.ID)
	require.NoError(t, err)
	pathsBefore, err := store.GetPaths(ctx, doc.ID, true)
	require.NoError(t, err)
	renderedBefore, err := store.RenderDocument(ctx, doc.ID)
	require.NoError(t, err)
	store.Close()

	dbURL = restart()

	var reopened *database.Store
	require.NoError(t, eventually(30*time.Second, func() error {
		var err error
		reopened, err = database.Setup(ctx, database.WithConfig(testConfig(dbURL, nil)), database.WithEmbedder(hashing.Embedder{}))
		return err
	}))
	defer reopened.Close()

	after, err := reopened.GetDocument(ctx, doc.ID)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	pathsAfter, err := reopened.GetPaths(ctx, doc.ID, true)
	require.NoError(t, err)
	assert.Equal(t, pathsBefore, pathsAfter)
	require.Len(t, pathsAfter.Paths, 2)
	renderedAfter, err := reopened.RenderDocument(ctx, doc.ID)
	require.NoError(t, err)
	assert.Equal(t, renderedBefore, renderedAfter)

	status, err := reopened.GetEmbeddingStatus(ctx, knowledge.TargetDocument, doc.ID)
	require.NoError(t, err)
	assert.True(t, status.HasEmbedding)

	res, err := reopened.Search(ctx, knowledge.SearchParams{Query: "Analytics", AllPaths: true})
	require.NoError(t, err)
	require.NotEmpty(t, res.Results)
	assert.Equal(t, doc.ID, res.Results[0].ID)

	violations, err := reopened.CheckIntegrity(ctx)
	require.NoError(t, err)
	assert.Empty(t, violations)
}

// roundtripDatabase returns the URL of an empty database and a function that
// restarts it and returns the (possibly changed) URL.
func roundtripDatabase(t *testing.T) (string, func() string) {
	t.Helper()
	ctx := context.Background()

	if suite.container == nil {
		db := newTestDB(t)
		return db.URL, func() string { return db.URL }
	}

	ctr, err := startContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(ctr) })
	dbURL, err := ctr.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	return dbURL, func() string {
		timeout := 30 * time.Second
		require.NoError(t, ctr.Stop(ctx, &timeout))
		require.NoError(t, ctr.Start(ctx))
		newURL, err := ctr.ConnectionString(ctx, "sslmode=disable")
		require.NoError(t, err)
		return newURL
	}
}

//go:build integration

package database_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mcp-knowledge-graph/internal/adapter/database"
	"mcp-knowledge-graph/internal/adapter/embedding/hashing"
	"mcp-knowledge-graph/internal/adapter/embedding/openai"
	"mcp-knowledge-graph/internal/knowledge"
)

func resultIDs(r knowledge.SearchResponse) []uuid.UUID {
	out := make([]uuid.UUID, len(r.Results))
	for i, res := range r.Results {
		out[i] = res.ID
	}
	return out
}

func assertValidResultPaths(t *testing.T, r knowledge.SearchResponse) {
	t.Helper()
	for _, res := range r.Results {
		require.NotEmpty(t, res.Paths)
		for _, p := range res.Paths {
			assert.Equal(t, root, p[0].ID, "paths start at root")
			assert.Equal(t, "root", p[0].Title)
			assert.Equal(t, res.ID, p[len(p)-1].ID, "paths end at the hit")
		}
	}
}

// Acceptance 8: one result per document, even with several matching blocks.
func TestSearchDeduplicatesBlocks(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := t.Context()

	doc, err := db.Store.CreateDocument(ctx, knowledge.CreateDocumentInput{
		Title: "Database Access", Summary: "Managing connections.", ParentIDs: []uuid.UUID{root},
		Blocks: []string{
			"pgxpool manages connections.",
			"Configuring pgxpool via ParseConfig.",
			"AfterConnect in pgxpool registers types.",
		},
	})
	require.NoError(t, err)
	db.create(t, "Unrelated", "Something completely different.", root)

	// Lexical only (no vectors yet) and with vectors.
	for _, indexed := range []bool{false, true} {
		if indexed {
			db.indexAll(t)
		}
		res, err := db.Store.Search(ctx, knowledge.SearchParams{Query: "pgxpool"})
		require.NoError(t, err)
		require.NotEmpty(t, res.Results)
		assert.Equal(t, doc.ID, res.Results[0].ID)
		seen := map[uuid.UUID]int{}
		for _, id := range resultIDs(res) {
			seen[id]++
		}
		assert.Equal(t, 1, seen[doc.ID], "exactly one result for the document (indexed=%v)", indexed)
		assertValidResultPaths(t, res)
	}
}

// Acceptance 9: exact technical terms without vectors.
func TestLexicalSearchWithoutEmbeddings(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := t.Context()

	backend := db.create(t, "Reliable Backend Structures", "Architecture.", root)
	auto := db.create(t, "GORM AutoMigrate", "Schema migrations with GORM.", backend.ID)
	cve, err := db.Store.CreateDocument(ctx, knowledge.CreateDocumentInput{
		Title: "Security Vulnerabilities", Summary: "Known weaknesses.", ParentIDs: []uuid.UUID{root},
		Blocks: []string{"Affected by CVE-2024-23674 is ..."},
	})
	require.NoError(t, err)
	goDoc := db.create(t, "Go", "The programming language.", root)
	db.create(t, "Gotham", "A city.", root)

	assert.Equal(t, 0, db.count(t, `SELECT count(*) FROM documents WHERE embedding IS NOT NULL`))

	cases := []struct {
		query string
		want  uuid.UUID
	}{
		{"AutoMigrate", auto.ID},
		{"automigrate", auto.ID},
		{"GORM AutoMigrate", auto.ID},
		{"Migrate", auto.ID},       // title substring
		{"CVE-2024-23674", cve.ID}, // hyphenated identifier in a block
		{"Go", goDoc.ID},           // very short token: exact title wins
		{"Backend Structures", backend.ID},
	}
	for _, tc := range cases {
		res, err := db.Store.Search(ctx, knowledge.SearchParams{Query: tc.query})
		require.NoError(t, err, tc.query)
		require.NotEmpty(t, res.Results, tc.query)
		assert.Equal(t, tc.want, res.Results[0].ID, "query %q", tc.query)
		assert.Equal(t, tc.query, res.Query)
		assertValidResultPaths(t, res)
	}

	// The path of the AutoMigrate hit goes through its parent.
	res, err := db.Store.Search(ctx, knowledge.SearchParams{Query: "AutoMigrate"})
	require.NoError(t, err)
	assert.Equal(t, []string{"root", "Reliable Backend Structures", "GORM AutoMigrate"}, pathTitles(res.Results[0].Paths[0]))

	// LIKE wildcards in the query are matched literally.
	res, err = db.Store.Search(ctx, knowledge.SearchParams{Query: "%"})
	require.NoError(t, err)
	assert.Empty(t, res.Results)
}

func TestSearchParameters(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	cfg := testConfig(db.URL, func(c *database.Config) { c.Search.DefaultLimit, c.Search.MaxLimit = 2, 3 })
	pool, err := database.OpenPool(t.Context(), db.URL)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	limited, err := database.New(pool, database.WithConfig(cfg), database.WithEmbedder(hashing.Embedder{}))
	require.NoError(t, err)
	ctx := t.Context()

	a := db.create(t, "Alpha Topic", "x", root)
	b := db.create(t, "Beta Topic", "x", root)
	require.NoError(t, db.Store.AddParent(ctx, b.ID, a.ID, false))
	for _, title := range []string{"Gamma Topic", "Delta Topic", "Epsilon Topic"} {
		db.create(t, title, "x", root)
	}
	db.indexAll(t)

	_, err = limited.Search(ctx, knowledge.SearchParams{Query: "   "})
	assert.ErrorIs(t, err, knowledge.ErrInvalidInput)

	res, err := limited.Search(ctx, knowledge.SearchParams{Query: "Topic"})
	require.NoError(t, err)
	assert.Len(t, res.Results, 2, "default limit")

	res, err = limited.Search(ctx, knowledge.SearchParams{Query: "Topic", Limit: 100})
	require.NoError(t, err)
	assert.Len(t, res.Results, 3, "limit is clamped to the server maximum")

	res, err = limited.Search(ctx, knowledge.SearchParams{Query: "Beta Topic", AllPaths: true})
	require.NoError(t, err)
	require.Equal(t, b.ID, res.Results[0].ID)
	assert.Len(t, res.Results[0].Paths, 2, "allPaths returns alternative paths")
	assertValidResultPaths(t, res)

	for _, id := range resultIDs(res) {
		assert.NotEqual(t, root, id, "root is never a search result")
	}
}

func TestSearchRequiresEmbedder(t *testing.T) {
	t.Parallel()
	db := newTestDB(t, database.WithEmbedder(nil))
	db.create(t, "AutoMigrate", "x", root)
	_, err := db.Store.Search(t.Context(), knowledge.SearchParams{Query: "AutoMigrate"})
	assert.ErrorIs(t, err, knowledge.ErrEmbeddingUnavailable, "no silent fallback to keyword-only search")

	db2 := newTestDB(t, database.WithEmbedder(failingEmbedder{}))
	_, err = db2.Store.Search(t.Context(), knowledge.SearchParams{Query: "x"})
	assert.ErrorIs(t, err, knowledge.ErrEmbeddingUnavailable)
}

type failingEmbedder struct{}

func (failingEmbedder) Model() string { return "failing" }
func (failingEmbedder) Embed(context.Context, []string) ([][]float32, error) {
	return nil, errors.New("provider down")
}

// Vector candidates from other models are never mixed into the ranking.
func TestSearchIgnoresVectorsOfOtherModels(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := t.Context()
	doc := db.create(t, "Zebra", "Stripes", root)
	db.indexAll(t)

	_, err := db.Raw.Exec(ctx, `UPDATE documents SET embedding_model = 'other-model' WHERE id = $1`, doc.ID)
	require.NoError(t, err)
	res, err := db.Store.Search(ctx, knowledge.SearchParams{Query: "completely unrelated"})
	require.NoError(t, err)
	assert.Empty(t, res.Results)
}

// Acceptance 10: qualitative semantic search with a real embedding model.
//
// Enabled with KG_TEST_SEMANTIC=1. The embedder is configured from the
// regular KG_EMBEDDING_* environment (see openai.Config); the defaults target
// a local Ollama with bge-m3 (1024 dimensions):
//
//	docker compose --profile semantic up -d ollama
//	docker compose exec ollama ollama pull bge-m3
//	KG_TEST_SEMANTIC=1 go test -tags=integration -run TestSemanticSearch ./internal/adapter/database/
//
// The test data never contains the word "Backend".
func TestSemanticSearch(t *testing.T) {
	if os.Getenv("KG_TEST_SEMANTIC") == "" {
		t.Skip("KG_TEST_SEMANTIC not set; semantic search needs a real embedding model")
	}
	emb, err := openai.New()
	require.NoError(t, err)
	model := emb.Model()

	t.Parallel()
	db := newTestDB(t, database.WithEmbedder(emb))
	ctx := t.Context()

	target, err := db.Store.CreateDocument(ctx, knowledge.CreateDocumentInput{
		Title:     "Go HTTP Server Hardening",
		Summary:   "Timeouts, middleware and routing for net/http servers in Go.",
		ParentIDs: []uuid.UUID{root},
		Blocks:    []string{"An http.Server should set ReadHeaderTimeout. Middleware validates requests, the router dispatches them to handlers."},
	})
	require.NoError(t, err)
	for _, d := range [][2]string{
		{"Sourdough Bread", "Recipe and proofing times for rye bread."},
		{"Garden Care in Autumn", "Leaves, pruning and winter protection for perennials."},
		{"Roman History", "From the Republic to the Empire."},
	} {
		db.create(t, d[0], d[1], root)
	}
	st := indexWith(t, db.Store, emb)
	require.Zero(t, st.Failed, "all documents must be embedded")

	res, err := db.Store.Search(ctx, knowledge.SearchParams{Query: "Backend"})
	require.NoError(t, err)
	require.NotEmpty(t, res.Results)
	assert.Equal(t, target.ID, res.Results[0].ID, "model %s should rank the HTTP server topic first for 'Backend'", model)
}

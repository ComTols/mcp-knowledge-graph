//go:build integration

package database_test

import (
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mcp-knowledge-graph/internal/adapter/database"
	"mcp-knowledge-graph/internal/knowledge"
)

var root = knowledge.RootID

// Acceptance 1: bootstrap.
func TestBootstrap(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := t.Context()

	assert.Equal(t, 1, db.count(t, `SELECT count(*) FROM documents WHERE is_root`))
	assert.Equal(t, 0, db.count(t, `SELECT count(*) FROM document_parents WHERE child_id = $1`, root))

	// Re-running migrations is a no-op and never creates a second root.
	require.NoError(t, database.Migrate(ctx, db.URL))
	require.NoError(t, database.Migrate(ctx, db.URL))
	assert.Equal(t, 1, db.count(t, `SELECT count(*) FROM documents`))

	s, err := db.Store.GetSummary(ctx, root)
	require.NoError(t, err)
	assert.Equal(t, "root", s.Title)
	assert.Equal(t, int64(1), s.Version)
}

func TestMigrateDownAndUp(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := t.Context()
	db.Store.Close() // the schema is dropped below

	require.NoError(t, database.MigrateDown(ctx, db.URL, 3))
	assert.Equal(t, 0, db.count(t, `SELECT count(*) FROM pg_tables WHERE tablename = 'documents'`))

	require.NoError(t, database.Migrate(ctx, db.URL))
	assert.Equal(t, 1, db.count(t, `SELECT count(*) FROM documents WHERE is_root`))

	// The test's store pool was closed; use a fresh one for the final
	// integrity check done by the cleanup.
	pool, err := database.OpenPool(ctx, db.URL)
	require.NoError(t, err)
	db.Store, err = database.New(pool, database.WithConfig(testConfig(db.URL, nil)))
	require.NoError(t, err)
}

func TestVerifyRootDetectsBrokenRoot(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := t.Context()

	_, err := db.Raw.Exec(ctx, `UPDATE documents SET is_root = false WHERE id = $1`, root)
	require.NoError(t, err)
	assert.ErrorIs(t, db.Store.VerifyRoot(ctx), knowledge.ErrIntegrityViolation)

	// Repair is not done silently; restore for the cleanup integrity check.
	_, err = db.Raw.Exec(ctx, `UPDATE documents SET is_root = true WHERE id = $1`, root)
	require.NoError(t, err)
	require.NoError(t, db.Store.VerifyRoot(ctx))
}

func TestSchemaRejectsRootAsChild(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	a := db.create(t, "A", "a", root)
	_, err := db.Raw.Exec(t.Context(),
		`INSERT INTO document_parents (child_id, parent_id) VALUES ($1, $2)`, root, a.ID)
	assert.Error(t, err, "defense in depth: the schema forbids the root as child")
}

// Acceptance 2: create.
func TestCreateDocument(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := t.Context()

	invalid := []knowledge.CreateDocumentInput{
		{Title: "", Summary: "s", ParentIDs: []uuid.UUID{root}},
		{Title: "t", Summary: "  ", ParentIDs: []uuid.UUID{root}},
		{Title: "t", Summary: "s"},
	}
	for _, in := range invalid {
		_, err := db.Store.CreateDocument(ctx, in)
		assert.ErrorIs(t, err, knowledge.ErrInvalidInput)
	}

	// A missing second parent aborts the whole transaction.
	_, err := db.Store.CreateDocument(ctx, knowledge.CreateDocumentInput{
		Title: "t", Summary: "s", ParentIDs: []uuid.UUID{root, uuid.New()},
		Blocks: []string{"block"}, Sources: []knowledge.SourceInput{{URI: "https://example.invalid/1"}},
	})
	assert.ErrorIs(t, err, knowledge.ErrNotFound)
	assert.Equal(t, 1, db.count(t, `SELECT count(*) FROM documents`))
	assert.Equal(t, 0, db.count(t, `SELECT count(*) FROM content_blocks`))
	assert.Equal(t, 0, db.count(t, `SELECT count(*) FROM embedding_jobs`))

	minkter := db.create(t, "Minkter", "Projekt Minkter", root)
	doc, err := db.Store.CreateDocument(ctx, knowledge.CreateDocumentInput{
		Title:     "  GORM AutoMigrate ",
		Summary:   "Schema migrations with GORM.",
		ParentIDs: []uuid.UUID{minkter.ID, minkter.ID},
		Blocks:    []string{"First block", "Second block"},
		Sources:   []knowledge.SourceInput{{URI: "https://example.invalid/c/1", Title: ptr("Chat")}},
	})
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, doc.ID)
	assert.Equal(t, "GORM AutoMigrate", doc.Title)
	assert.Equal(t, int64(1), doc.Version)

	parents, err := db.Store.GetParents(ctx, doc.ID)
	require.NoError(t, err)
	assert.Equal(t, []knowledge.ParentRef{{ID: minkter.ID, Title: "Minkter", IsPrimary: true}}, parents)

	paths, err := db.Store.GetPaths(ctx, doc.ID, false)
	require.NoError(t, err)
	require.Len(t, paths.Paths, 1)
	assert.Equal(t, []string{"root", "Minkter", "GORM AutoMigrate"}, pathTitles(paths.Paths[0]))

	// One job for the document and one per block.
	assert.Equal(t, 1, db.count(t, `SELECT count(*) FROM embedding_jobs WHERE target_kind = 'document' AND target_id = $1`, doc.ID))
	assert.Equal(t, 2, db.count(t, `SELECT count(*) FROM embedding_jobs j JOIN content_blocks b ON b.id = j.target_id WHERE b.document_id = $1`, doc.ID))
}

// Acceptance 3: multiple parents.
func TestMultipleParents(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := t.Context()

	minkter := db.create(t, "Minkter", "m", root)
	backend := db.create(t, "Reliable Backend Structures", "b", minkter.ID)
	ai := db.create(t, "AI", "k", root)
	doc, err := db.Store.CreateDocument(ctx, knowledge.CreateDocumentInput{
		Title: "Go HTTP Server Hardening", Summary: "h", ParentIDs: []uuid.UUID{backend.ID, ai.ID},
	})
	require.NoError(t, err)

	assert.Equal(t, 1, db.count(t, `SELECT count(*) FROM documents WHERE title = 'Go HTTP Server Hardening'`))

	parents, err := db.Store.GetParents(ctx, doc.ID)
	require.NoError(t, err)
	assert.Equal(t, []knowledge.ParentRef{
		{ID: backend.ID, Title: "Reliable Backend Structures", IsPrimary: true},
		{ID: ai.ID, Title: "AI", IsPrimary: false},
	}, parents)

	for _, p := range []uuid.UUID{backend.ID, ai.ID} {
		children, err := db.Store.GetChildren(ctx, p)
		require.NoError(t, err)
		require.Len(t, children, 1)
		assert.Equal(t, doc.ID, children[0].ID)
	}

	canonical, err := db.Store.GetPaths(ctx, doc.ID, false)
	require.NoError(t, err)
	require.Len(t, canonical.Paths, 1)
	assert.Equal(t, []string{"root", "Minkter", "Reliable Backend Structures", "Go HTTP Server Hardening"},
		pathTitles(canonical.Paths[0]))

	all, err := db.Store.GetPaths(ctx, doc.ID, true)
	require.NoError(t, err)
	require.Len(t, all.Paths, 2)
	assert.False(t, all.Truncated)
	assert.Equal(t, canonical.Paths[0], all.Paths[0], "canonical path comes first")
	assert.Equal(t, []string{"root", "AI", "Go HTTP Server Hardening"}, pathTitles(all.Paths[1]))
}

func TestChildrenOrderingAndNotFound(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := t.Context()

	db.create(t, "beta", "b", root)
	db.create(t, "Alpha", "a", root)
	db.create(t, "gamma", "g", root)
	children, err := db.Store.GetChildren(ctx, root)
	require.NoError(t, err)
	var got []string
	for _, c := range children {
		got = append(got, c.Title)
		assert.True(t, c.IsPrimary)
	}
	assert.Equal(t, []string{"Alpha", "beta", "gamma"}, got)

	missing := uuid.New()
	_, err = db.Store.GetChildren(ctx, missing)
	assert.ErrorIs(t, err, knowledge.ErrNotFound)
	_, err = db.Store.GetParents(ctx, missing)
	assert.ErrorIs(t, err, knowledge.ErrNotFound)
	_, err = db.Store.GetSummary(ctx, missing)
	assert.ErrorIs(t, err, knowledge.ErrNotFound)
	_, err = db.Store.GetDocument(ctx, missing)
	assert.ErrorIs(t, err, knowledge.ErrNotFound)
	_, err = db.Store.GetPaths(ctx, missing, false)
	assert.ErrorIs(t, err, knowledge.ErrNotFound)
	_, err = db.Store.GetPaths(ctx, missing, true)
	assert.ErrorIs(t, err, knowledge.ErrNotFound)
}

// Acceptance 4: cycles.
func TestCyclesAreRejected(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := t.Context()

	a := db.create(t, "A", "a", root)
	b := db.create(t, "B", "b", a.ID)
	c := db.create(t, "C", "c", b.ID)

	assert.ErrorIs(t, db.Store.AddParent(ctx, a.ID, a.ID, false), knowledge.ErrCycleDetected)
	assert.ErrorIs(t, db.Store.AddParent(ctx, a.ID, c.ID, false), knowledge.ErrCycleDetected)
	assert.ErrorIs(t, db.Store.AddParent(ctx, b.ID, c.ID, true), knowledge.ErrCycleDetected)
	assert.ErrorIs(t, db.Store.AddParent(ctx, root, a.ID, false), knowledge.ErrRootViolation)
	assert.ErrorIs(t, db.Store.AddParent(ctx, a.ID, uuid.New(), false), knowledge.ErrNotFound)
	assert.ErrorIs(t, db.Store.AddParent(ctx, uuid.New(), a.ID, false), knowledge.ErrNotFound)

	// A legal shortcut edge is accepted and idempotent.
	require.NoError(t, db.Store.AddParent(ctx, c.ID, a.ID, false))
	require.NoError(t, db.Store.AddParent(ctx, c.ID, a.ID, false))
	parents, err := db.Store.GetParents(ctx, c.ID)
	require.NoError(t, err)
	assert.Len(t, parents, 2)
}

// Acceptance 4 + 14: concurrent edges that would only together form a cycle.
func TestConcurrentCycleCreationIsPrevented(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := t.Context()

	for range 10 {
		x := db.create(t, "X", "x", root)
		y := db.create(t, "Y", "y", root)

		var wg sync.WaitGroup
		errs := make([]error, 2)
		start := make(chan struct{})
		wg.Add(2)
		go func() { defer wg.Done(); <-start; errs[0] = db.Store.AddParent(ctx, x.ID, y.ID, false) }()
		go func() { defer wg.Done(); <-start; errs[1] = db.Store.AddParent(ctx, y.ID, x.ID, false) }()
		close(start)
		wg.Wait()

		succeeded := 0
		for _, err := range errs {
			if err == nil {
				succeeded++
			} else {
				assert.ErrorIs(t, err, knowledge.ErrCycleDetected)
			}
		}
		assert.Equal(t, 1, succeeded, "exactly one of the two edges may win")
	}
}

// Acceptance 5: remove parent.
func TestRemoveParent(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := t.Context()

	p1 := db.create(t, "P1", "p", root)
	p2 := db.create(t, "P2", "p", root)
	p3 := db.create(t, "P3", "p", root)
	child, err := db.Store.CreateDocument(ctx, knowledge.CreateDocumentInput{
		Title: "Child", Summary: "c", ParentIDs: []uuid.UUID{p1.ID, p3.ID},
	})
	require.NoError(t, err)
	require.NoError(t, db.Store.AddParent(ctx, child.ID, p2.ID, false))

	assert.ErrorIs(t, db.Store.RemoveParent(ctx, child.ID, uuid.New()), knowledge.ErrNotFound)
	assert.ErrorIs(t, db.Store.RemoveParent(ctx, root, p1.ID), knowledge.ErrRootViolation)

	// Removing the primary picks the oldest remaining edge (P3 was created
	// together with the document, P2 later).
	require.NoError(t, db.Store.RemoveParent(ctx, child.ID, p1.ID))
	parents, err := db.Store.GetParents(ctx, child.ID)
	require.NoError(t, err)
	require.Len(t, parents, 2)
	assert.Equal(t, knowledge.ParentRef{ID: p3.ID, Title: "P3", IsPrimary: true}, parents[0])

	require.NoError(t, db.Store.RemoveParent(ctx, child.ID, p2.ID))
	assert.ErrorIs(t, db.Store.RemoveParent(ctx, child.ID, p3.ID), knowledge.ErrLastParentViolation)

	s, err := db.Store.GetSummary(ctx, child.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(4), s.Version, "add + two removals bump the version")
}

func TestSetPrimaryParent(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := t.Context()

	p1 := db.create(t, "P1", "p", root)
	p2 := db.create(t, "P2", "p", root)
	child := db.create(t, "Child", "c", p1.ID)

	assert.ErrorIs(t, db.Store.SetPrimaryParent(ctx, child.ID, p2.ID), knowledge.ErrNotFound)
	require.NoError(t, db.Store.AddParent(ctx, child.ID, p2.ID, false))
	require.NoError(t, db.Store.SetPrimaryParent(ctx, child.ID, p2.ID))

	paths, err := db.Store.GetPaths(ctx, child.ID, false)
	require.NoError(t, err)
	assert.Equal(t, []string{"root", "P2", "Child"}, pathTitles(paths.Paths[0]))

	// AddParent with makePrimary on a new edge switches atomically.
	p3 := db.create(t, "P3", "p", root)
	require.NoError(t, db.Store.AddParent(ctx, child.ID, p3.ID, true))
	parents, err := db.Store.GetParents(ctx, child.ID)
	require.NoError(t, err)
	assert.Equal(t, p3.ID, parents[0].ID)
	assert.True(t, parents[0].IsPrimary)
	assert.Equal(t, 1, db.count(t, `SELECT count(*) FROM document_parents WHERE child_id = $1 AND is_primary`, child.ID))

	// Parent changes do not touch the document embedding.
	assert.Equal(t, 1, db.count(t, `SELECT count(*) FROM embedding_jobs WHERE target_id = $1`, child.ID))
	db.indexAll(t)
	require.NoError(t, db.Store.AddParent(ctx, child.ID, p1.ID, true))
	assert.Equal(t, 0, db.count(t, `SELECT count(*) FROM embedding_jobs`))
	assert.Equal(t, 1, db.count(t, `SELECT count(*) FROM documents WHERE id = $1 AND embedding IS NOT NULL`, child.ID))
}

// Acceptance 14: concurrent removal can never orphan a document.
func TestConcurrentRemoveParentKeepsOneParent(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := t.Context()

	for range 10 {
		p1 := db.create(t, "P1", "p", root)
		p2 := db.create(t, "P2", "p", root)
		child, err := db.Store.CreateDocument(ctx, knowledge.CreateDocumentInput{
			Title: "C", Summary: "c", ParentIDs: []uuid.UUID{p1.ID, p2.ID},
		})
		require.NoError(t, err)

		var wg sync.WaitGroup
		errs := make([]error, 2)
		start := make(chan struct{})
		for i, p := range []uuid.UUID{p1.ID, p2.ID} {
			wg.Add(1)
			go func() { defer wg.Done(); <-start; errs[i] = db.Store.RemoveParent(ctx, child.ID, p) }()
		}
		close(start)
		wg.Wait()

		failures := 0
		for _, err := range errs {
			if err != nil {
				failures++
				assert.ErrorIs(t, err, knowledge.ErrLastParentViolation)
			}
		}
		assert.Equal(t, 1, failures)
		assert.Equal(t, 1, db.count(t, `SELECT count(*) FROM document_parents WHERE child_id = $1 AND is_primary`, child.ID))
	}
}

// Acceptance 6: metadata update.
func TestUpdateDocumentMetadata(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := t.Context()

	parent, err := db.Store.CreateDocument(ctx, knowledge.CreateDocumentInput{
		Title: "Parent", Summary: "p", ParentIDs: []uuid.UUID{root}, Blocks: []string{"Parent text"},
	})
	require.NoError(t, err)
	child := db.create(t, "Old title", "Old summary", parent.ID)
	db.indexAll(t)
	parentBefore, err := db.Store.GetDocument(ctx, parent.ID)
	require.NoError(t, err)

	_, err = db.Store.UpdateDocumentMetadata(ctx, child.ID, child.Version+1, knowledge.MetadataUpdate{Title: ptr("x")})
	assert.ErrorIs(t, err, knowledge.ErrVersionConflict)
	_, err = db.Store.UpdateDocumentMetadata(ctx, uuid.New(), 1, knowledge.MetadataUpdate{Title: ptr("x")})
	assert.ErrorIs(t, err, knowledge.ErrNotFound)
	_, err = db.Store.UpdateDocumentMetadata(ctx, child.ID, child.Version, knowledge.MetadataUpdate{})
	assert.ErrorIs(t, err, knowledge.ErrInvalidInput)

	updated, err := db.Store.UpdateDocumentMetadata(ctx, child.ID, child.Version, knowledge.MetadataUpdate{
		Title: ptr("New title"), Summary: ptr(" New summary "),
	})
	require.NoError(t, err)
	assert.Equal(t, child.Version+1, updated.Version)
	assert.Equal(t, "New summary", updated.Summary)

	// The document vector is invalidated and a job for the new text exists.
	st, err := db.Store.GetEmbeddingStatus(ctx, knowledge.TargetDocument, child.ID)
	require.NoError(t, err)
	assert.False(t, st.HasEmbedding)
	job, err := db.Store.GetEmbeddingJob(ctx, knowledge.TargetDocument, child.ID)
	require.NoError(t, err)
	assert.Equal(t, database.JobPending, job.State)
	assert.Equal(t, knowledge.InputHash(knowledge.DocumentEmbeddingInput("New title", "New summary")), job.InputHash)

	// Children lists immediately show the new data; the parent is unchanged.
	children, err := db.Store.GetChildren(ctx, parent.ID)
	require.NoError(t, err)
	assert.Equal(t, []knowledge.ChildRef{{ID: child.ID, Title: "New title", Summary: "New summary", IsPrimary: true}}, children)
	parentAfter, err := db.Store.GetDocument(ctx, parent.ID)
	require.NoError(t, err)
	assert.Equal(t, parentBefore.Version, parentAfter.Version)
	assert.Equal(t, parentBefore.Blocks, parentAfter.Blocks)

	// A no-op update keeps version and embedding state.
	same, err := db.Store.UpdateDocumentMetadata(ctx, child.ID, updated.Version, knowledge.MetadataUpdate{Title: ptr("New title")})
	require.NoError(t, err)
	assert.Equal(t, updated.Version, same.Version)
}

// Acceptance 14: optimistic locking under concurrency.
func TestConcurrentMetadataUpdatesConflict(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := t.Context()
	doc := db.create(t, "Doc", "d", root)

	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = db.Store.UpdateDocumentMetadata(ctx, doc.ID, doc.Version,
				knowledge.MetadataUpdate{Summary: ptr("writer " + string(rune('A'+i)))})
		}()
	}
	close(start)
	wg.Wait()

	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
		} else {
			assert.ErrorIs(t, err, knowledge.ErrVersionConflict)
		}
	}
	assert.Equal(t, 1, ok)
	s, err := db.Store.GetSummary(ctx, doc.ID)
	require.NoError(t, err)
	assert.Equal(t, doc.Version+1, s.Version)
}

func pathTitles(p knowledge.Path) []string {
	out := make([]string, len(p))
	for i, e := range p {
		out[i] = e.Title
	}
	return out
}

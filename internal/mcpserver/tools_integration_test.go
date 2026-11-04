//go:build integration

package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mcp-knowledge-graph/internal/adapter/embedding/hashing"
	"mcp-knowledge-graph/internal/knowledge"
)

// Acceptance 12, 13, 16, 17, 18: create with two parents and browse.
func TestCreateAndBrowse(t *testing.T) {
	t.Parallel()
	e := newEnv(t)

	root := callOK[SummaryOutput](t, e.CS, ToolGetRoot, map[string]any{})
	assert.Equal(t, rootID, root.ID)
	assert.Equal(t, "root", root.Title)
	rootParents := callOK[GetParentsOutput](t, e.CS, ToolGetParents, map[string]any{"document_id": rootID})
	assert.Equal(t, []ParentOutput{}, rootParents.Parents)

	minkter := e.createDoc(t, "Minkter", []string{rootID})
	golang := e.createDoc(t, "Go", []string{rootID})
	jobsBefore := e.jobs(t)

	created := callOK[CreateDocumentOutput](t, e.CS, ToolCreateDocument, map[string]any{
		"title":      "Go HTTP Server Hardening",
		"summary":    "Timeouts, middleware, and request validation for Go HTTP servers.",
		"parent_ids": []string{minkter.DocumentID, golang.DocumentID},
		"blocks": []map[string]any{
			{"content": "## Timeouts\nUse ReadHeaderTimeout to protect the server."},
			{"content": "## Middleware\nApply request validation consistently."},
		},
		"sources": []map[string]any{
			{"kind": "conversation", "source_uri": "https://example.invalid/archive/conversation-123", "title": "Go server discussion"},
			{"kind": "other", "source_uri": "urn:note:1"},
		},
	})
	assert.Equal(t, int64(1), created.Version)
	assert.Equal(t, minkter.DocumentID, created.PrimaryParentID, "first parent becomes primary")
	require.Len(t, created.BlockIDs, 2)
	require.Len(t, created.SourceIDs, 2)
	assert.Equal(t, jobsBefore+3, e.jobs(t), "one document job and one job per block")

	summary := callOK[SummaryOutput](t, e.CS, ToolGetSummary, map[string]any{"document_id": created.DocumentID})
	assert.Equal(t, SummaryOutput{ID: created.DocumentID, Title: "Go HTTP Server Hardening",
		Summary: "Timeouts, middleware, and request validation for Go HTTP servers.", Version: 1}, summary)

	parents := callOK[GetParentsOutput](t, e.CS, ToolGetParents, map[string]any{"document_id": created.DocumentID})
	assert.Equal(t, int64(1), parents.Version)
	assert.Equal(t, []ParentOutput{
		{ID: minkter.DocumentID, Title: "Minkter", IsPrimary: true},
		{ID: golang.DocumentID, Title: "Go", IsPrimary: false},
	}, parents.Parents)

	children := callOK[GetChildrenOutput](t, e.CS, ToolGetChildren, map[string]any{"document_id": golang.DocumentID})
	assert.Equal(t, []ChildOutput{{ID: created.DocumentID, Title: "Go HTTP Server Hardening",
		Summary: "Timeouts, middleware, and request validation for Go HTTP servers.", IsPrimary: false}}, children.Children)
	assert.False(t, children.HasMore)
	assert.Nil(t, children.NextCursor)

	full := callOK[GetFulltextOutput](t, e.CS, ToolGetFulltext, map[string]any{"document_id": created.DocumentID})
	assert.Equal(t, int64(1), full.Version)
	require.Len(t, full.Blocks, 2)
	assert.Equal(t, created.BlockIDs[0], full.Blocks[0].ID)
	assert.Equal(t, "## Timeouts\nUse ReadHeaderTimeout to protect the server.", full.Blocks[0].Content)
	assert.Equal(t, created.BlockIDs[1], full.Blocks[1].ID)

	paths := callOK[GetPathsOutput](t, e.CS, ToolGetPaths, map[string]any{"document_id": created.DocumentID})
	assert.Equal(t, []DocumentPath{{{rootID, "root"}, {minkter.DocumentID, "Minkter"}, {created.DocumentID, "Go HTTP Server Hardening"}}}, paths.Paths)
	assert.False(t, paths.PathsTruncated)

	all := callOK[GetPathsOutput](t, e.CS, ToolGetPaths, map[string]any{"document_id": created.DocumentID, "all_paths": true})
	require.Len(t, all.Paths, 2)
	assert.Equal(t, paths.Paths[0], all.Paths[0], "primary path first")
	assert.Equal(t, golang.DocumentID, all.Paths[1][1].ID)
	one := callOK[GetPathsOutput](t, e.CS, ToolGetPaths, map[string]any{"document_id": created.DocumentID, "all_paths": true, "max_paths": 1})
	assert.Len(t, one.Paths, 1)
	assert.True(t, one.PathsTruncated)

	rootPath := callOK[GetPathsOutput](t, e.CS, ToolGetPaths, map[string]any{"document_id": rootID})
	assert.Equal(t, []DocumentPath{{{rootID, "root"}}}, rootPath.Paths)

	sources := callOK[GetSourcesOutput](t, e.CS, ToolGetSources, map[string]any{"document_id": created.DocumentID})
	require.Len(t, sources.Sources, 2)
	title := "Go server discussion"
	assert.Equal(t, SourceOutput{ID: created.SourceIDs[0], Kind: "conversation",
		SourceURI: "https://example.invalid/archive/conversation-123", Title: &title}, sources.Sources[0])
	assert.Nil(t, sources.Sources[1].Title, "absent title is null")

	pub := callErr(t, e.CS, ToolGetSummary, map[string]any{"document_id": uuid.NewString()})
	assert.Equal(t, CodeNotFound, pub.Code, "unknown IDs are errors, not empty results")
}

// Acceptance 19: rejected creates leave no trace.
func TestCreateRejectionsAreAtomic(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	cat := e.createDoc(t, "Category", []string{rootID})
	docs, jobs := e.count(t, `SELECT count(*) FROM documents`), e.jobs(t)

	pub := callErr(t, e.CS, ToolCreateDocument, map[string]any{
		"title": "Orphan", "summary": "s", "parent_ids": []string{cat.DocumentID, uuid.NewString()},
		"blocks": []map[string]any{{"content": "x"}},
	})
	assert.Equal(t, CodeNotFound, pub.Code)

	pub = callErr(t, e.CS, ToolCreateDocument, map[string]any{"title": "T", "summary": "  ", "parent_ids": []string{rootID}})
	assert.Equal(t, CodeInvalidInput, pub.Code)

	assert.Equal(t, docs, e.count(t, `SELECT count(*) FROM documents`))
	assert.Equal(t, jobs, e.jobs(t))
	assert.Equal(t, 1, e.count(t, `SELECT count(*) FROM documents WHERE is_root`))
}

// Acceptance 8, 9: one hit per document with root-to-hit paths only.
func TestSearch(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.createDoc(t, "Backend", []string{rootID})
	b := e.createDoc(t, "Security", []string{rootID})
	doc := e.createDoc(t, "Go HTTP Server Hardening", []string{a.DocumentID, b.DocumentID},
		"Use ReadHeaderTimeout for every server.", "ReadHeaderTimeout also limits slowloris attacks.")
	e.indexAll(t, hashing.Embedder{})

	res := call(t, e.CS, ToolSearch, map[string]any{"query": "ReadHeaderTimeout"})
	require.False(t, res.IsError, resultText(res))
	text := resultText(res)
	assert.NotContains(t, text, "score")
	assert.NotContains(t, text, "slowloris", "no block content in search results")
	out := callOK[SearchOutput](t, e.CS, ToolSearch, map[string]any{"query": "ReadHeaderTimeout"})
	assert.True(t, out.SemanticSearchAvailable)
	assert.Equal(t, []string{}, out.Warnings)
	hits := 0
	for _, r := range out.Results {
		assert.NotEqual(t, rootID, r.ID, "root is never a result")
		if r.ID == doc.DocumentID {
			hits++
			require.Len(t, r.Paths, 1)
			path := r.Paths[0]
			assert.Equal(t, rootID, path[0].ID)
			assert.Equal(t, doc.DocumentID, path[len(path)-1].ID)
		}
	}
	assert.Equal(t, 1, hits, "one entry per document even with several matching blocks")

	all := callOK[SearchOutput](t, e.CS, ToolSearch, map[string]any{"query": "Go HTTP Server Hardening", "all_paths": true, "max_paths": 1})
	require.NotEmpty(t, all.Results)
	assert.Equal(t, doc.DocumentID, all.Results[0].ID, "exact title match ranks first")
	assert.Len(t, all.Results[0].Paths, 1)
	assert.True(t, all.Results[0].PathsTruncated)

	empty := callOK[SearchOutput](t, e.CS, ToolSearch, map[string]any{"query": "zzqxjv nonexistent"})
	assert.NotNil(t, empty.Results)
}

type failingEmbedder struct{}

func (failingEmbedder) Model() string { return hashing.ModelName }
func (failingEmbedder) Embed(context.Context, []string) ([][]float32, error) {
	return nil, errors.New("provider unavailable: api key sk-secret rejected")
}

// Acceptance 11: lexical fallback with an explicit warning.
func TestSearchLexicalFallback(t *testing.T) {
	t.Parallel()
	e := newEnv(t, withEmbedder(failingEmbedder{}))
	doc := e.createDoc(t, "Kubernetes Ingress", []string{rootID})

	res := call(t, e.CS, ToolSearch, map[string]any{"query": "Kubernetes"})
	require.False(t, res.IsError, resultText(res))
	assert.NotContains(t, resultText(res), "sk-secret")
	out := callOK[SearchOutput](t, e.CS, ToolSearch, map[string]any{"query": "Kubernetes"})
	assert.False(t, out.SemanticSearchAvailable)
	assert.Equal(t, []string{"semantic_search_unavailable"}, out.Warnings)
	require.Len(t, out.Results, 1)
	assert.Equal(t, doc.DocumentID, out.Results[0].ID)
}

// Acceptance 20: metadata updates.
func TestUpdateDocument(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	cat := e.createDoc(t, "Category", []string{rootID})
	doc := e.createDoc(t, "Draft", []string{cat.DocumentID}, "Body")
	e.indexAll(t, hashing.Embedder{})

	out := callOK[ChangeOutput](t, e.CS, ToolUpdateDocument, map[string]any{
		"document_id": doc.DocumentID, "expected_version": 1, "summary": "Final summary",
	})
	assert.Equal(t, ChangeOutput{DocumentID: doc.DocumentID, Version: 2, Changed: true}, out)
	s := callOK[SummaryOutput](t, e.CS, ToolGetSummary, map[string]any{"document_id": doc.DocumentID})
	assert.Equal(t, "Draft", s.Title, "omitted fields stay unchanged")
	assert.Equal(t, "Final summary", s.Summary)
	assert.Equal(t, 1, e.jobs(t), "only the document embedding is requeued")
	assert.Equal(t, 1, e.count(t, `SELECT count(*) FROM embedding_jobs WHERE target_kind = 'document' AND target_id = $1`, doc.DocumentID))
	assert.Equal(t, 0, e.count(t, `SELECT count(*) FROM content_blocks WHERE document_id = $1 AND embedding IS NULL`, doc.DocumentID),
		"block embeddings are untouched")

	children := callOK[GetChildrenOutput](t, e.CS, ToolGetChildren, map[string]any{"document_id": cat.DocumentID})
	assert.Equal(t, "Final summary", children.Children[0].Summary, "summary changes are visible immediately")

	e.indexAll(t, hashing.Embedder{})
	noop := callOK[ChangeOutput](t, e.CS, ToolUpdateDocument, map[string]any{
		"document_id": doc.DocumentID, "expected_version": 2, "title": "Draft", "summary": "Final summary",
	})
	assert.Equal(t, ChangeOutput{DocumentID: doc.DocumentID, Version: 2, Changed: false}, noop)
	assert.Equal(t, 0, e.jobs(t), "no-op updates enqueue nothing")

	pub := callErr(t, e.CS, ToolUpdateDocument, map[string]any{"document_id": doc.DocumentID, "expected_version": 1, "title": "Other"})
	assert.Equal(t, CodeVersionConflict, pub.Code)
	assert.EqualValues(t, 2, pub.Details["current_version"])
}

func (e *testEnv) blockOrder(t *testing.T, documentID string) []string {
	t.Helper()
	full := callOK[GetFulltextOutput](t, e.CS, ToolGetFulltext, map[string]any{"document_id": documentID, "limit": 100})
	ids := make([]string, len(full.Blocks))
	for i, b := range full.Blocks {
		ids[i] = b.ID
	}
	return ids
}

func op(kv ...any) map[string]any {
	m := map[string]any{}
	for i := 0; i < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	return m
}

func (e *testEnv) edit(t *testing.T, documentID string, version int64, ops ...map[string]any) EditDocumentOutput {
	t.Helper()
	return callOK[EditDocumentOutput](t, e.CS, ToolEditDocument, map[string]any{
		"document_id": documentID, "expected_version": version, "operations": ops,
	})
}

func (e *testEnv) editErr(t *testing.T, documentID string, version int64, ops ...map[string]any) PublicToolError {
	t.Helper()
	return callErr(t, e.CS, ToolEditDocument, map[string]any{
		"document_id": documentID, "expected_version": version, "operations": ops,
	})
}

// Acceptance 21, 23, 24: all six operations, atomic batches, compact output.
func TestEditDocument(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	doc := e.createDoc(t, "Doc", []string{rootID}, "A", "B")
	a, b := doc.BlockIDs[0], doc.BlockIDs[1]

	// replace + insert between A and B + move B before A.
	out := e.edit(t, doc.DocumentID, 1,
		op("type", "replace", "block_id", a, "content", "A2"),
		op("type", "insert", "after_block_id", a, "content", "C"),
		op("type", "move", "block_id", b, "before_block_id", a),
	)
	require.Len(t, out.CreatedBlocks, 1)
	assert.Equal(t, 1, out.CreatedBlocks[0].OperationIndex, "zero-based operation index")
	c := out.CreatedBlocks[0].BlockID
	assert.Equal(t, int64(2), out.Version, "one version increment per batch")
	assert.True(t, out.Changed)
	assert.Equal(t, []string{b, a, c}, out.BlockIDsInOrder)
	assert.Equal(t, []string{}, out.DeletedBlockIDs)
	assert.Equal(t, out.BlockIDsInOrder, e.blockOrder(t, doc.DocumentID))
	full := callOK[GetFulltextOutput](t, e.CS, ToolGetFulltext, map[string]any{"document_id": doc.DocumentID})
	assert.Equal(t, "A2", full.Blocks[1].Content)
	assert.Equal(t, int64(2), full.Blocks[1].Version, "replace bumps the block version")
	assert.Equal(t, int64(1), full.Blocks[0].Version, "move keeps the block version")

	// prepend + append + delete.
	out = e.edit(t, doc.DocumentID, 2,
		op("type", "prepend", "content", "First"),
		op("type", "append", "content", "Last"),
		op("type", "delete", "block_id", c),
	)
	assert.Equal(t, []string{c}, out.DeletedBlockIDs)
	require.Len(t, out.CreatedBlocks, 2)
	first, last := out.CreatedBlocks[0].BlockID, out.CreatedBlocks[1].BlockID
	assert.Equal(t, []string{first, b, a, last}, out.BlockIDsInOrder)

	// Moving a block to where it already is is a semantic no-op.
	out = e.edit(t, doc.DocumentID, 3, op("type", "move", "block_id", a, "after_block_id", b))
	assert.False(t, out.Changed)
	assert.Equal(t, int64(3), out.Version)

	// A failing operation rolls back the whole batch.
	jobs := e.jobs(t)
	pub := e.editErr(t, doc.DocumentID, 3,
		op("type", "append", "content", "never stored"),
		op("type", "delete", "block_id", a),
		op("type", "replace", "block_id", a, "content", "deleted earlier in the batch"),
	)
	assert.Equal(t, CodeNotFound, pub.Code)
	assert.Equal(t, []string{first, b, a, last}, e.blockOrder(t, doc.DocumentID))
	assert.Equal(t, jobs, e.jobs(t))
	s := callOK[SummaryOutput](t, e.CS, ToolGetSummary, map[string]any{"document_id": doc.DocumentID})
	assert.Equal(t, int64(3), s.Version)

	// Anchors from another document are rejected.
	other := e.createDoc(t, "Other", []string{rootID}, "X")
	pub = e.editErr(t, doc.DocumentID, 3, op("type", "insert", "after_block_id", other.BlockIDs[0], "content", "y"))
	assert.Equal(t, CodeNotFound, pub.Code)

	// Stale version.
	pub = e.editErr(t, doc.DocumentID, 2, op("type", "append", "content", "late"))
	assert.Equal(t, CodeVersionConflict, pub.Code)
	assert.True(t, pub.Retryable)

	// Append to an empty document; prepend to a one-block document.
	empty := e.createDoc(t, "Empty", []string{rootID})
	assert.Equal(t, []string{}, e.blockOrder(t, empty.DocumentID))
	out = e.edit(t, empty.DocumentID, 1, op("type", "append", "content", "only"))
	only := out.CreatedBlocks[0].BlockID
	out = e.edit(t, empty.DocumentID, 2, op("type", "prepend", "content", "before"))
	assert.Equal(t, []string{out.CreatedBlocks[0].BlockID, only}, out.BlockIDsInOrder)
}

// Acceptance 22: rebalancing keeps the logical order and creates no
// unnecessary embedding jobs.
func TestEditDocumentRebalance(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	doc := e.createDoc(t, "Dense", []string{rootID}, "left", "right")
	e.indexAll(t, hashing.Embedder{})
	left := doc.BlockIDs[0]

	// Inserting repeatedly directly after the same block halves the gap
	// each time; 1,000,000 is exhausted after ~20 inserts.
	version := int64(1)
	var inserted []string
	for i := 0; i < 25; i++ {
		out := e.edit(t, doc.DocumentID, version, op("type", "insert", "after_block_id", left, "content", fmt.Sprintf("n%d", i)))
		version = out.Version
		inserted = append(inserted, out.CreatedBlocks[0].BlockID)
	}
	want := []string{left}
	for i := len(inserted) - 1; i >= 0; i-- {
		want = append(want, inserted[i])
	}
	want = append(want, doc.BlockIDs[1])
	assert.Equal(t, want, e.blockOrder(t, doc.DocumentID))
	assert.Equal(t, int64(26), version)

	assert.Equal(t, len(want), e.count(t, `SELECT count(DISTINCT position) FROM content_blocks WHERE document_id = $1`, doc.DocumentID))
	assert.Equal(t, 25, e.jobs(t), "only inserted blocks are embedded; rebalancing creates no jobs")
	assert.Equal(t, 0, e.count(t, `SELECT count(*) FROM content_blocks WHERE id = ANY($1::uuid[]) AND embedding IS NULL`,
		[]string{left, doc.BlockIDs[1]}), "moved blocks keep their embeddings")
}

// Acceptance 25: concurrent edits with the same version.
func TestConcurrentEdits(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	doc := e.createDoc(t, "Contended", []string{rootID}, "A")

	var wg sync.WaitGroup
	results := make([]bool, 4)
	codes := make([]string, 4)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res := call(t, e.CS, ToolEditDocument, map[string]any{
				"document_id": doc.DocumentID, "expected_version": 1,
				"operations": []any{op("type", "append", "content", fmt.Sprintf("writer %d", i))},
			})
			results[i] = !res.IsError
			if res.IsError {
				codes[i] = resultText(res)
			}
		}(i)
	}
	wg.Wait()
	successes := 0
	for i, ok := range results {
		if ok {
			successes++
		} else {
			assert.Contains(t, codes[i], CodeVersionConflict)
		}
	}
	assert.Equal(t, 1, successes, "exactly one writer wins")
	assert.Len(t, e.blockOrder(t, doc.DocumentID), 2, "no lost or duplicated update")
}

// Acceptance 26, 27, 28, 30: graph tools.
func TestParentTools(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.createDoc(t, "A", []string{rootID})
	b := e.createDoc(t, "B", []string{rootID})
	doc := e.createDoc(t, "Doc", []string{a.DocumentID})
	child := e.createDoc(t, "Child", []string{doc.DocumentID})
	e.indexAll(t, hashing.Embedder{})

	edge := func(tool, parent string, version int64, extra ...any) map[string]any {
		m := map[string]any{"document_id": doc.DocumentID, "parent_id": parent, "expected_version": version}
		for i := 0; i < len(extra); i += 2 {
			m[extra[i].(string)] = extra[i+1]
		}
		return m
	}

	// Cycle and root violations.
	pub := callErr(t, e.CS, ToolAddParent, edge(ToolAddParent, child.DocumentID, 1))
	assert.Equal(t, CodeCycleDetected, pub.Code)
	pub = callErr(t, e.CS, ToolAddParent, map[string]any{"document_id": rootID, "parent_id": a.DocumentID, "expected_version": 1})
	assert.Equal(t, CodeRootViolation, pub.Code)

	// Add a secondary parent.
	add := callOK[AddParentOutput](t, e.CS, ToolAddParent, edge(ToolAddParent, b.DocumentID, 1))
	assert.Equal(t, AddParentOutput{DocumentID: doc.DocumentID, ParentID: b.DocumentID, Version: 2, Changed: true, IsPrimary: false}, add)

	// Duplicate edge is a no-op ...
	add = callOK[AddParentOutput](t, e.CS, ToolAddParent, edge(ToolAddParent, b.DocumentID, 2))
	assert.False(t, add.Changed)
	assert.Equal(t, int64(2), add.Version)
	// ... unless it is promoted.
	add = callOK[AddParentOutput](t, e.CS, ToolAddParent, edge(ToolAddParent, b.DocumentID, 2, "make_primary", true))
	assert.True(t, add.Changed)
	assert.True(t, add.IsPrimary)
	assert.Equal(t, int64(3), add.Version)

	// set_primary_parent back to A; repeating it is a no-op.
	set := callOK[ParentChangeOutput](t, e.CS, ToolSetPrimaryParent, edge(ToolSetPrimaryParent, a.DocumentID, 3))
	assert.Equal(t, ParentChangeOutput{DocumentID: doc.DocumentID, Version: 4, Changed: true, PrimaryParentID: a.DocumentID}, set)
	set = callOK[ParentChangeOutput](t, e.CS, ToolSetPrimaryParent, edge(ToolSetPrimaryParent, a.DocumentID, 4))
	assert.False(t, set.Changed)
	assert.Equal(t, int64(4), set.Version)
	assert.Equal(t, 1, e.count(t, `SELECT count(*) FROM document_parents WHERE child_id = $1 AND is_primary`, doc.DocumentID))
	pub = callErr(t, e.CS, ToolSetPrimaryParent, edge(ToolSetPrimaryParent, rootID, 4))
	assert.Equal(t, CodeRelationshipNotFound, pub.Code, "no edge is created implicitly")

	// Removing the primary parent selects the oldest remaining edge.
	rm := callOK[ParentChangeOutput](t, e.CS, ToolRemoveParent, edge(ToolRemoveParent, a.DocumentID, 4))
	assert.Equal(t, ParentChangeOutput{DocumentID: doc.DocumentID, Version: 5, Changed: true, PrimaryParentID: b.DocumentID}, rm)
	paths := callOK[GetPathsOutput](t, e.CS, ToolGetPaths, map[string]any{"document_id": doc.DocumentID})
	assert.Equal(t, b.DocumentID, paths.Paths[0][1].ID)

	pub = callErr(t, e.CS, ToolRemoveParent, edge(ToolRemoveParent, a.DocumentID, 5))
	assert.Equal(t, CodeRelationshipNotFound, pub.Code)
	pub = callErr(t, e.CS, ToolRemoveParent, edge(ToolRemoveParent, b.DocumentID, 5))
	assert.Equal(t, CodeLastParentViolation, pub.Code)
	pub = callErr(t, e.CS, ToolRemoveParent, map[string]any{"document_id": rootID, "parent_id": a.DocumentID, "expected_version": 1})
	assert.Equal(t, CodeRootViolation, pub.Code)

	// Stale versions fail on every graph tool without changes.
	for _, tool := range []string{ToolAddParent, ToolRemoveParent, ToolSetPrimaryParent} {
		pub = callErr(t, e.CS, tool, edge(tool, b.DocumentID, 4))
		assert.Equal(t, CodeVersionConflict, pub.Code, tool)
	}
	s := callOK[SummaryOutput](t, e.CS, ToolGetSummary, map[string]any{"document_id": doc.DocumentID})
	assert.Equal(t, int64(5), s.Version)

	assert.Equal(t, 0, e.jobs(t), "structural changes never touch embeddings")
}

// Acceptance 29: add_source is idempotent per URI and not embedded.
func TestAddSource(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	doc := e.createDoc(t, "Doc", []string{rootID})
	other := e.createDoc(t, "Other", []string{rootID})
	e.indexAll(t, hashing.Embedder{})

	args := map[string]any{"document_id": doc.DocumentID, "expected_version": 1, "kind": "conversation",
		"source_uri": "https://example.invalid/archive/conversation-456", "title": "Follow-up"}
	first := callOK[AddSourceOutput](t, e.CS, ToolAddSource, args)
	assert.True(t, first.Changed)
	assert.Equal(t, int64(2), first.Version)

	args["expected_version"], args["title"] = 2, "Different title"
	again := callOK[AddSourceOutput](t, e.CS, ToolAddSource, args)
	assert.Equal(t, AddSourceOutput{DocumentID: doc.DocumentID, SourceID: first.SourceID, Version: 2, Changed: false}, again)
	sources := callOK[GetSourcesOutput](t, e.CS, ToolGetSources, map[string]any{"document_id": doc.DocumentID})
	require.Len(t, sources.Sources, 1)
	assert.Equal(t, "Follow-up", *sources.Sources[0].Title, "existing title is not overwritten")

	// The same URI may be attached to another document.
	args["document_id"], args["expected_version"] = other.DocumentID, 1
	shared := callOK[AddSourceOutput](t, e.CS, ToolAddSource, args)
	assert.True(t, shared.Changed)
	assert.NotEqual(t, first.SourceID, shared.SourceID)

	args["document_id"], args["source_uri"] = doc.DocumentID, "urn:conversation:new"
	pub := callErr(t, e.CS, ToolAddSource, args)
	assert.Equal(t, CodeVersionConflict, pub.Code)

	assert.Equal(t, 0, e.jobs(t), "sources are not embedded")
}

// Acceptance 13, 14, 15: deterministic pagination.
func TestPagination(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	parent := e.createDoc(t, "Parent", []string{rootID})
	titles := []string{"delta", "Alpha", "charlie", "Bravo", "echo"}
	for _, title := range titles {
		e.createDoc(t, title, []string{parent.DocumentID})
	}

	var got []string
	cursor := ""
	pages := 0
	for {
		args := map[string]any{"document_id": parent.DocumentID, "limit": 2}
		if cursor != "" {
			args["cursor"] = cursor
		}
		page := callOK[GetChildrenOutput](t, e.CS, ToolGetChildren, args)
		pages++
		for _, c := range page.Children {
			got = append(got, c.Title)
		}
		if !page.HasMore {
			assert.Nil(t, page.NextCursor)
			break
		}
		require.NotNil(t, page.NextCursor)
		cursor = *page.NextCursor
	}
	assert.Equal(t, 3, pages)
	want := append([]string(nil), titles...)
	sort.Slice(want, func(i, j int) bool { return strings.ToLower(want[i]) < strings.ToLower(want[j]) })
	assert.Equal(t, want, got, "ordered by lower(title)")

	// A cursor is bound to its document.
	page := callOK[GetChildrenOutput](t, e.CS, ToolGetChildren, map[string]any{"document_id": parent.DocumentID, "limit": 1})
	pub := callErr(t, e.CS, ToolGetChildren, map[string]any{"document_id": rootID, "cursor": *page.NextCursor})
	assert.Equal(t, CodeInvalidInput, pub.Code)

	// Sources.
	doc := e.createDoc(t, "Sources", []string{rootID})
	version := int64(1)
	for i := 0; i < 3; i++ {
		out := callOK[AddSourceOutput](t, e.CS, ToolAddSource, map[string]any{"document_id": doc.DocumentID,
			"expected_version": version, "kind": "other", "source_uri": fmt.Sprintf("urn:source:%d", i)})
		version = out.Version
	}
	srcPage := callOK[GetSourcesOutput](t, e.CS, ToolGetSources, map[string]any{"document_id": doc.DocumentID, "limit": 2})
	require.True(t, srcPage.HasMore)
	rest := callOK[GetSourcesOutput](t, e.CS, ToolGetSources, map[string]any{"document_id": doc.DocumentID, "cursor": *srcPage.NextCursor})
	assert.False(t, rest.HasMore)
	require.Len(t, rest.Sources, 1)
	assert.Equal(t, "urn:source:2", rest.Sources[0].SourceURI)

	// Full text pages with a consistent version.
	long := e.createDoc(t, "Long", []string{rootID}, "b0", "b1", "b2", "b3", "b4")
	p1 := callOK[GetFulltextOutput](t, e.CS, ToolGetFulltext, map[string]any{"document_id": long.DocumentID, "limit": 2})
	require.True(t, p1.HasMore)
	require.NotNil(t, p1.NextAfterBlockID)
	assert.Equal(t, long.BlockIDs[1], *p1.NextAfterBlockID)
	p2 := callOK[GetFulltextOutput](t, e.CS, ToolGetFulltext, map[string]any{"document_id": long.DocumentID, "limit": 2,
		"after_block_id": *p1.NextAfterBlockID, "expected_version": p1.Version})
	assert.Equal(t, []string{"b2", "b3"}, []string{p2.Blocks[0].Content, p2.Blocks[1].Content})

	// A change between pages is reported instead of mixing versions.
	e.edit(t, long.DocumentID, p1.Version, op("type", "delete", "block_id", long.BlockIDs[4]))
	pub = callErr(t, e.CS, ToolGetFulltext, map[string]any{"document_id": long.DocumentID,
		"after_block_id": *p2.NextAfterBlockID, "expected_version": p1.Version})
	assert.Equal(t, CodeVersionConflict, pub.Code)

	// after_block_id of another document.
	pub = callErr(t, e.CS, ToolGetFulltext, map[string]any{"document_id": long.DocumentID, "after_block_id": uuid.NewString()})
	assert.Equal(t, CodeInvalidInput, pub.Code)
}

// Acceptance 34: a canceled context reaches the service and the database.
func TestCanceledContextIsPropagated(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	h := newHandler(e.Store, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := h.getSummary(ctx, DocumentIDInput{DocumentID: rootID})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	_, err = h.createDocument(ctx, CreateDocumentInput{Title: "T", Summary: "S", ParentIDs: []string{rootID}})
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, e.count(t, `SELECT count(*) FROM documents`))
	_ = knowledge.RootID
}

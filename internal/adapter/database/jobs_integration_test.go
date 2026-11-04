//go:build integration

package database_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mcp-knowledge-graph/internal/adapter/database"
	"mcp-knowledge-graph/internal/adapter/embedding/hashing"
	"mcp-knowledge-graph/internal/indexing"
	"mcp-knowledge-graph/internal/knowledge"
)

// Acceptance 13: a crashed worker's job is picked up again after the lease.
func TestEmbeddingJobSurvivesCrash(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := t.Context()
	doc := db.create(t, "Doc", "d", root)

	jobs, err := db.Store.ClaimEmbeddingJobs(ctx, 10, time.Minute)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	assert.Equal(t, knowledge.DocumentEmbeddingInput("Doc", "d"), jobs[0].Input)
	assert.Equal(t, 1, jobs[0].Attempts)

	// "Crash": the job is never completed. While leased it is invisible.
	again, err := db.Store.ClaimEmbeddingJobs(ctx, 10, time.Minute)
	require.NoError(t, err)
	assert.Empty(t, again)

	// Lease expires.
	_, err = db.Raw.Exec(ctx, `UPDATE embedding_jobs SET lease_until = now() - interval '1 second'`)
	require.NoError(t, err)

	st := db.indexAll(t)
	assert.Equal(t, 1, st.Stored)
	status, err := db.Store.GetEmbeddingStatus(ctx, knowledge.TargetDocument, doc.ID)
	require.NoError(t, err)
	assert.True(t, status.HasEmbedding)
	assert.Equal(t, hashing.ModelName, *status.Model)
	assert.Equal(t, knowledge.InputHash(knowledge.DocumentEmbeddingInput("Doc", "d")), *status.InputHash)
	assert.Equal(t, 0, db.count(t, `SELECT count(*) FROM embedding_jobs`))
}

// Acceptance 13: a slow, outdated embedding must not overwrite newer state.
func TestStaleEmbeddingIsDiscarded(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := t.Context()
	doc, err := db.Store.CreateDocument(ctx, knowledge.CreateDocumentInput{
		Title: "Doc", Summary: "old", ParentIDs: []uuid.UUID{root}, Blocks: []string{"old block"},
	})
	require.NoError(t, err)

	jobs, err := db.Store.ClaimEmbeddingJobs(ctx, 10, time.Minute)
	require.NoError(t, err)
	require.Len(t, jobs, 2)

	// While the worker is "computing", the text changes.
	_, err = db.Store.UpdateDocumentMetadata(ctx, doc.ID, doc.Version, knowledge.MetadataUpdate{Summary: ptr("new")})
	require.NoError(t, err)
	fulltext, err := db.Store.GetFulltext(ctx, doc.ID)
	require.NoError(t, err)
	blocks := fulltext.Blocks
	_, err = db.Store.EditDocument(ctx, knowledge.EditDocumentInput{
		DocumentID: doc.ID, ExpectedVersion: fulltext.Version,
		Operations: []knowledge.BlockOperation{knowledge.ReplaceBlock(blocks[0].ID, "new block")},
	})
	require.NoError(t, err)

	for _, j := range jobs {
		stored, err := db.Store.CompleteEmbeddingJob(ctx, j, hashing.ModelName, hashing.Vector(j.Input))
		require.NoError(t, err)
		assert.False(t, stored, "outdated result for %s must be discarded", j.Kind)
	}
	for _, kind := range []knowledge.TargetKind{knowledge.TargetDocument, knowledge.TargetBlock} {
		id := doc.ID
		if kind == knowledge.TargetBlock {
			id = blocks[0].ID
		}
		st, err := db.Store.GetEmbeddingStatus(ctx, kind, id)
		require.NoError(t, err)
		assert.False(t, st.HasEmbedding)
		job, err := db.Store.GetEmbeddingJob(ctx, kind, id)
		require.NoError(t, err, "the newer job is kept")
		assert.Equal(t, database.JobPending, job.State)
	}

	// The next run indexes the current text.
	st := db.indexAll(t)
	assert.Equal(t, 2, st.Stored)
	status, err := db.Store.GetEmbeddingStatus(ctx, knowledge.TargetDocument, doc.ID)
	require.NoError(t, err)
	assert.Equal(t, knowledge.InputHash(knowledge.DocumentEmbeddingInput("Doc", "new")), *status.InputHash)
}

func TestEmbeddingRetryAndPermanentFailure(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := t.Context()
	doc := db.create(t, "Doc", "d", root)

	w := newWorker(t, db.Store, failingEmbedder{}, func(c *indexing.Config) {
		c.MaxAttempts, c.BaseBackoff, c.MaxBackoff = 2, time.Hour, 2*time.Hour
	})
	st, err := w.RunOnce(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, st.Failed)

	job, err := db.Store.GetEmbeddingJob(ctx, knowledge.TargetDocument, doc.ID)
	require.NoError(t, err)
	assert.Equal(t, database.JobPending, job.State)
	assert.Equal(t, 1, job.Attempts)
	require.NotNil(t, job.LeaseUntil)
	assert.True(t, job.LeaseUntil.After(time.Now().Add(50*time.Minute)), "retry is delayed by the backoff")
	assert.Contains(t, *job.LastError, "provider down")

	// Not claimable during backoff.
	st, err = w.RunOnce(ctx)
	require.NoError(t, err)
	assert.Zero(t, st.Claimed)

	// After the backoff the second attempt fails permanently.
	_, err = db.Raw.Exec(ctx, `UPDATE embedding_jobs SET lease_until = now()`)
	require.NoError(t, err)
	st, err = w.RunOnce(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, st.Failed)
	failed, err := db.Store.ListFailedEmbeddingJobs(ctx)
	require.NoError(t, err)
	require.Len(t, failed, 1)
	assert.Equal(t, doc.ID, failed[0].TargetID)
	assert.Equal(t, 2, failed[0].Attempts)

	// Failed jobs are not retried automatically ...
	assert.Zero(t, db.indexAll(t).Claimed)

	// ... but administratively.
	require.NoError(t, db.Store.RetryEmbedding(ctx, knowledge.TargetDocument, doc.ID))
	job, err = db.Store.GetEmbeddingJob(ctx, knowledge.TargetDocument, doc.ID)
	require.NoError(t, err)
	assert.Equal(t, database.JobPending, job.State)
	assert.Zero(t, job.Attempts)
	assert.Equal(t, 1, db.indexAll(t).Stored)

	assert.ErrorIs(t, db.Store.RetryEmbedding(ctx, knowledge.TargetBlock, uuid.New()), knowledge.ErrNotFound)
	assert.ErrorIs(t, db.Store.RetryEmbedding(ctx, "nope", doc.ID), knowledge.ErrInvalidInput)
}

func TestTooLongBlockIsMarkedFailedNotTruncated(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := t.Context()
	_, err := db.Store.CreateDocument(ctx, knowledge.CreateDocumentInput{
		Title: "Doc", Summary: "d", ParentIDs: []uuid.UUID{root}, Blocks: []string{"this block is definitely much too long for the configured model limit"},
	})
	require.NoError(t, err)

	w := newWorker(t, db.Store, hashing.Embedder{}, func(c *indexing.Config) { c.MaxInputBytes = 40 })
	_, err = w.RunOnce(ctx)
	require.NoError(t, err)

	failed, err := db.Store.ListFailedEmbeddingJobs(ctx)
	require.NoError(t, err)
	require.Len(t, failed, 1)
	assert.Equal(t, knowledge.TargetBlock, failed[0].Kind)
	assert.Contains(t, *failed[0].LastError, "exceeds model limit")
}

func TestConcurrentWorkersNeverClaimTheSameJob(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := t.Context()
	blocks := make([]string, 40)
	for i := range blocks {
		blocks[i] = "block " + uuid.NewString()
	}
	_, err := db.Store.CreateDocument(ctx, knowledge.CreateDocumentInput{
		Title: "Doc", Summary: "d", ParentIDs: []uuid.UUID{root}, Blocks: blocks,
	})
	require.NoError(t, err)

	results := make(chan []indexing.Job, 4)
	for range 4 {
		go func() {
			jobs, err := db.Store.ClaimEmbeddingJobs(ctx, 15, time.Minute)
			assert.NoError(t, err)
			results <- jobs
		}()
	}
	seen := map[uuid.UUID]bool{}
	for range 4 {
		for _, j := range <-results {
			assert.False(t, seen[j.TargetID], "job claimed twice")
			seen[j.TargetID] = true
		}
	}
	assert.Len(t, seen, 41)
}

func TestCompleteRejectsInvalidVectors(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	job := indexing.Job{Kind: knowledge.TargetDocument, TargetID: root, Input: "x"}
	_, err := db.Store.CompleteEmbeddingJob(t.Context(), job, "m", []float32{1, 2})
	assert.ErrorIs(t, err, knowledge.ErrInvalidInput)
	_, err = db.Store.CompleteEmbeddingJob(t.Context(), job, "", hashing.Vector("x"))
	assert.ErrorIs(t, err, knowledge.ErrInvalidInput)
}

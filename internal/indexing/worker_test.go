package indexing_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	env "mcp-knowledge-graph/internal/environment"
	"mcp-knowledge-graph/internal/indexing"
	"mcp-knowledge-graph/internal/knowledge"
)

type fakeStore struct {
	jobs      []indexing.Job
	completed map[uuid.UUID][]float32
	stale     map[uuid.UUID]bool // targets whose text changed after claim
	failures  map[uuid.UUID]indexing.Failure
}

func newFakeStore(jobs ...indexing.Job) *fakeStore {
	return &fakeStore{
		jobs:      jobs,
		completed: map[uuid.UUID][]float32{},
		stale:     map[uuid.UUID]bool{},
		failures:  map[uuid.UUID]indexing.Failure{},
	}
}

func (s *fakeStore) ClaimEmbeddingJobs(_ context.Context, limit int, _ time.Duration) ([]indexing.Job, error) {
	n := min(limit, len(s.jobs))
	out := s.jobs[:n]
	s.jobs = s.jobs[n:]
	return out, nil
}

func (s *fakeStore) CompleteEmbeddingJob(_ context.Context, j indexing.Job, _ string, v []float32) (bool, error) {
	if s.stale[j.TargetID] {
		return false, nil
	}
	s.completed[j.TargetID] = v
	return true, nil
}

func (s *fakeStore) FailEmbeddingJob(_ context.Context, j indexing.Job, f indexing.Failure) error {
	s.failures[j.TargetID] = f
	return nil
}

type fakeEmbedder struct {
	dims  int
	err   error
	calls int
}

func (e *fakeEmbedder) Model() string { return "fake" }

func (e *fakeEmbedder) Embed(_ context.Context, inputs []string) ([][]float32, error) {
	e.calls++
	if e.err != nil {
		return nil, e.err
	}
	out := make([][]float32, len(inputs))
	for i := range out {
		out[i] = make([]float32, e.dims)
	}
	return out, nil
}

// newWorker builds a worker from the default configuration, optionally
// adjusted by modify, independent of the process environment.
func newWorker(t *testing.T, store indexing.JobStore, e knowledge.Embedder, modify func(*indexing.Config)) *indexing.Worker {
	t.Helper()
	cfg := env.GetDefaultConf[indexing.Config]()
	if modify != nil {
		modify(cfg)
	}
	w, err := indexing.NewWorker(store, e, indexing.WithConfig(cfg))
	require.NoError(t, err)
	return w
}

func job(input string, attempts int) indexing.Job {
	return indexing.Job{Kind: knowledge.TargetBlock, TargetID: uuid.New(), Input: input, Attempts: attempts}
}

func TestRunOnceStoresVectors(t *testing.T) {
	j1, j2 := job("a", 1), job("b", 1)
	store := newFakeStore(j1, j2)
	store.stale[j2.TargetID] = true
	w := newWorker(t, store, &fakeEmbedder{dims: knowledge.EmbeddingDimensions}, nil)

	st, err := w.RunOnce(t.Context())
	require.NoError(t, err)
	assert.Equal(t, indexing.Stats{Claimed: 2, Stored: 1, Stale: 1}, st)
	assert.Contains(t, store.completed, j1.TargetID)
	assert.NotContains(t, store.completed, j2.TargetID)
}

func TestRunOnceRetryableFailure(t *testing.T) {
	j := job("a", 3)
	store := newFakeStore(j)
	w := newWorker(t, store, &fakeEmbedder{err: errors.New("provider down")}, func(c *indexing.Config) {
		c.MaxAttempts, c.BaseBackoff, c.MaxBackoff = 4, time.Second, time.Minute
	})

	st, err := w.RunOnce(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 1, st.Failed)
	f := store.failures[j.TargetID]
	assert.False(t, f.Permanent)
	assert.Equal(t, 4, f.MaxAttempts)
	assert.Equal(t, 4*time.Second, f.RetryAfter)
	assert.Contains(t, f.Message, "provider down")
}

func TestRunOnceInputTooLongIsPermanent(t *testing.T) {
	long, short := job("0123456789", 1), job("ok", 1)
	store := newFakeStore(long, short)
	emb := &fakeEmbedder{dims: knowledge.EmbeddingDimensions}
	w := newWorker(t, store, emb, func(c *indexing.Config) { c.MaxInputBytes = 5 })

	st, err := w.RunOnce(t.Context())
	require.NoError(t, err)
	assert.Equal(t, indexing.Stats{Claimed: 2, Stored: 1, Failed: 1}, st)
	assert.True(t, store.failures[long.TargetID].Permanent, "too long input must not be truncated silently")
	assert.Contains(t, store.completed, short.TargetID)
}

func TestRunOnceWrongDimensionsIsPermanent(t *testing.T) {
	j := job("a", 1)
	store := newFakeStore(j)
	w := newWorker(t, store, &fakeEmbedder{dims: 3}, nil)

	st, err := w.RunOnce(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 1, st.Failed)
	assert.True(t, store.failures[j.TargetID].Permanent)
}

func TestRunOnceNoJobs(t *testing.T) {
	emb := &fakeEmbedder{dims: knowledge.EmbeddingDimensions}
	w := newWorker(t, newFakeStore(), emb, nil)
	st, err := w.RunOnce(t.Context())
	require.NoError(t, err)
	assert.Zero(t, st)
	assert.Zero(t, emb.calls)
}

func TestRunStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	emb := &fakeEmbedder{dims: knowledge.EmbeddingDimensions}
	w := newWorker(t, newFakeStore(job("a", 1)), emb, func(c *indexing.Config) { c.PollInterval = time.Millisecond })

	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
	}
}

func TestBackoff(t *testing.T) {
	base, maxDelay := time.Second, 10*time.Second
	assert.Equal(t, time.Second, indexing.Backoff(0, base, maxDelay))
	assert.Equal(t, time.Second, indexing.Backoff(1, base, maxDelay))
	assert.Equal(t, 2*time.Second, indexing.Backoff(2, base, maxDelay))
	assert.Equal(t, 8*time.Second, indexing.Backoff(4, base, maxDelay))
	assert.Equal(t, maxDelay, indexing.Backoff(5, base, maxDelay))
	assert.Equal(t, maxDelay, indexing.Backoff(500, base, maxDelay))
}

func TestNewWorkerOptions(t *testing.T) {
	emb := &fakeEmbedder{dims: knowledge.EmbeddingDimensions}

	_, err := indexing.NewWorker(nil, emb)
	assert.Error(t, err)
	_, err = indexing.NewWorker(newFakeStore(), nil)
	assert.Error(t, err)

	_, err = indexing.NewWorker(newFakeStore(), emb, indexing.WithConfig(&indexing.Config{}))
	assert.ErrorContains(t, err, "KG_INDEXING_BATCH_SIZE", "an explicit config is validated, not defaulted")

	t.Setenv("KG_INDEXING_BATCH_SIZE", "7")
	w, err := indexing.NewWorker(newFakeStore(), emb, indexing.WithLogger(nil))
	require.NoError(t, err, "without WithConfig the environment is used")
	assert.NotNil(t, w)

	t.Setenv("KG_INDEXING_BATCH_SIZE", "-1")
	_, err = indexing.NewWorker(newFakeStore(), emb)
	assert.ErrorContains(t, err, "KG_INDEXING_BATCH_SIZE")
}

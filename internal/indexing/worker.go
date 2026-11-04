// Package indexing computes embeddings for documents and content blocks
// asynchronously, driven by the persistent embedding job queue.
//
// The worker claims jobs with a lease, releases the database transaction,
// calls the (slow, external) embedding provider and finally hands the vector
// back to the store, which writes it only if the indexed text is still
// current. Crashed workers are recovered through expired leases.
package indexing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	env "mcp-knowledge-graph/internal/environment"
	"mcp-knowledge-graph/internal/knowledge"
)

// Job is a claimed embedding job together with the exact input text that
// has to be embedded.
type Job struct {
	Kind      knowledge.TargetKind
	TargetID  uuid.UUID
	InputHash string // hash stored in the job row at claim time
	Attempts  int    // attempts including the current one
	Input     string // embedding input built from the current text
}

// Failure describes a failed attempt.
type Failure struct {
	Message string
	// Permanent marks the job as failed immediately, e.g. for inputs that
	// exceed the model limit. Otherwise the job is retried after RetryAfter
	// until MaxAttempts is reached.
	Permanent   bool
	MaxAttempts int
	RetryAfter  time.Duration
}

// JobStore is the persistence port used by the worker.
type JobStore interface {
	// ClaimEmbeddingJobs leases up to limit pending (or expired) jobs.
	ClaimEmbeddingJobs(ctx context.Context, limit int, lease time.Duration) ([]Job, error)
	// CompleteEmbeddingJob stores vector if job.Input still matches the
	// current text of the target. It reports whether the vector was stored.
	CompleteEmbeddingJob(ctx context.Context, job Job, model string, vector []float32) (bool, error)
	// FailEmbeddingJob records a failed attempt.
	FailEmbeddingJob(ctx context.Context, job Job, f Failure) error
}

// Stats summarizes a single RunOnce call.
type Stats struct {
	Claimed int
	Stored  int // vectors written
	Stale   int // results discarded because the text changed meanwhile
	Failed  int // failed attempts (retryable or permanent)
}

// Worker processes embedding jobs.
type Worker struct {
	store    JobStore
	embedder knowledge.Embedder
	cfg      Config
	log      *slog.Logger
}

type options struct {
	cfg *Config
	log *slog.Logger
}

// Option modifies the construction of a Worker.
type Option func(*options)

// WithConfig sets an explicit configuration instead of loading it from the
// environment. A nil config keeps the environment based default.
func WithConfig(cfg *Config) Option {
	return func(o *options) { o.cfg = cfg }
}

// WithLogger sets the logger. By default log output is discarded.
func WithLogger(log *slog.Logger) Option {
	return func(o *options) { o.log = log }
}

// NewWorker creates a worker. The configuration is loaded from the
// environment unless WithConfig is given; loading panics if environment
// values cannot be parsed (see env.GetConf).
func NewWorker(store JobStore, embedder knowledge.Embedder, mods ...Option) (*Worker, error) {
	if store == nil || embedder == nil {
		return nil, errors.New("indexing: store and embedder are required")
	}
	var o options
	for _, m := range mods {
		m(&o)
	}
	if o.cfg == nil {
		o.cfg = env.GetConf[Config]()
	}
	if err := o.cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid indexing config: %w", err)
	}
	if o.log == nil {
		o.log = slog.New(slog.DiscardHandler)
	}
	return &Worker{store: store, embedder: embedder, cfg: *o.cfg, log: o.log}, nil
}

// Run processes jobs until ctx is cancelled. Transient errors are logged and
// retried after the poll interval.
func (w *Worker) Run(ctx context.Context) error {
	for {
		st, err := w.RunOnce(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			w.log.ErrorContext(ctx, "embedding worker iteration failed", "error", err)
		}
		if err == nil && st.Claimed > 0 {
			continue // there may be more work
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(w.cfg.PollInterval):
		}
	}
}

// RunOnce claims one batch of jobs and processes it.
func (w *Worker) RunOnce(ctx context.Context) (Stats, error) {
	jobs, err := w.store.ClaimEmbeddingJobs(ctx, w.cfg.BatchSize, w.cfg.Lease)
	if err != nil {
		return Stats{}, fmt.Errorf("claim embedding jobs: %w", err)
	}
	st := Stats{Claimed: len(jobs)}
	if len(jobs) == 0 {
		return st, nil
	}

	embeddable := make([]Job, 0, len(jobs))
	for _, j := range jobs {
		if w.cfg.MaxInputBytes > 0 && len(j.Input) > w.cfg.MaxInputBytes {
			msg := fmt.Sprintf("input of %d bytes exceeds model limit of %d bytes; split the content", len(j.Input), w.cfg.MaxInputBytes)
			if err := w.fail(ctx, j, msg, true); err != nil {
				return st, err
			}
			st.Failed++
			continue
		}
		embeddable = append(embeddable, j)
	}
	if len(embeddable) == 0 {
		return st, nil
	}

	inputs := make([]string, len(embeddable))
	for i, j := range embeddable {
		inputs[i] = j.Input
	}
	vectors, embedErr := w.embedder.Embed(ctx, inputs)
	if embedErr == nil && len(vectors) != len(inputs) {
		embedErr = fmt.Errorf("provider returned %d vectors for %d inputs", len(vectors), len(inputs))
	}
	if embedErr != nil {
		if ctx.Err() != nil {
			// Shutdown: leave the jobs leased; they are picked up again
			// once the lease expires.
			return st, ctx.Err()
		}
		for _, j := range embeddable {
			if err := w.fail(ctx, j, embedErr.Error(), false); err != nil {
				return st, err
			}
			st.Failed++
		}
		return st, nil
	}

	model := w.embedder.Model()
	for i, j := range embeddable {
		if len(vectors[i]) != knowledge.EmbeddingDimensions {
			msg := fmt.Sprintf("model %q returned %d dimensions, schema requires %d", model, len(vectors[i]), knowledge.EmbeddingDimensions)
			if err := w.fail(ctx, j, msg, true); err != nil {
				return st, err
			}
			st.Failed++
			continue
		}
		stored, err := w.store.CompleteEmbeddingJob(ctx, j, model, vectors[i])
		if err != nil {
			return st, fmt.Errorf("complete embedding job %s/%s: %w", j.Kind, j.TargetID, err)
		}
		if stored {
			st.Stored++
		} else {
			st.Stale++
		}
	}
	return st, nil
}

func (w *Worker) fail(ctx context.Context, j Job, msg string, permanent bool) error {
	f := Failure{
		Message:     msg,
		Permanent:   permanent,
		MaxAttempts: w.cfg.MaxAttempts,
		RetryAfter:  Backoff(j.Attempts, w.cfg.BaseBackoff, w.cfg.MaxBackoff),
	}
	w.log.WarnContext(ctx, "embedding job failed",
		"kind", j.Kind, "target", j.TargetID, "attempt", j.Attempts, "permanent", permanent, "error", msg)
	if err := w.store.FailEmbeddingJob(ctx, j, f); err != nil {
		return fmt.Errorf("record embedding failure %s/%s: %w", j.Kind, j.TargetID, err)
	}
	return nil
}

// Backoff returns the exponential retry delay for the given attempt
// (1-based), capped at maxDelay.
func Backoff(attempt int, base, maxDelay time.Duration) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := base
	for i := 1; i < attempt; i++ {
		d *= 2
		if d >= maxDelay || d <= 0 {
			return maxDelay
		}
	}
	return min(d, maxDelay)
}

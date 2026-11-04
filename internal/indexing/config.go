package indexing

import (
	"errors"
	"fmt"
	"time"
)

// Config tunes the embedding worker. It is loaded from the environment
// (github.com/caarlos0/env).
type Config struct {
	// BatchSize is the number of jobs claimed and embedded per iteration.
	BatchSize int `env:"KG_INDEXING_BATCH_SIZE" envDefault:"16"`
	// Lease is how long a claimed job stays reserved. A crashed worker's
	// jobs become claimable again once the lease expires.
	Lease time.Duration `env:"KG_INDEXING_LEASE" envDefault:"5m"`
	// MaxAttempts is the number of attempts before a job is marked failed.
	MaxAttempts int `env:"KG_INDEXING_MAX_ATTEMPTS" envDefault:"5"`
	// BaseBackoff is the delay before the first retry; it doubles per
	// attempt up to MaxBackoff.
	BaseBackoff time.Duration `env:"KG_INDEXING_BASE_BACKOFF" envDefault:"10s"`
	MaxBackoff  time.Duration `env:"KG_INDEXING_MAX_BACKOFF" envDefault:"10m"`
	// MaxInputBytes is the model input limit; longer inputs fail
	// permanently instead of being truncated. 0 disables the check.
	MaxInputBytes int `env:"KG_INDEXING_MAX_INPUT_BYTES" envDefault:"0"`
	// PollInterval is the idle wait of Run when no jobs are pending.
	PollInterval time.Duration `env:"KG_INDEXING_POLL_INTERVAL" envDefault:"2s"`
}

// Validate checks the configuration and reports all problems at once.
func (c *Config) Validate() error {
	var errs []error
	if c.BatchSize <= 0 {
		errs = append(errs, fmt.Errorf("KG_INDEXING_BATCH_SIZE must be positive, got %d", c.BatchSize))
	}
	if c.MaxAttempts <= 0 {
		errs = append(errs, fmt.Errorf("KG_INDEXING_MAX_ATTEMPTS must be positive, got %d", c.MaxAttempts))
	}
	if c.MaxInputBytes < 0 {
		errs = append(errs, fmt.Errorf("KG_INDEXING_MAX_INPUT_BYTES must not be negative, got %d", c.MaxInputBytes))
	}
	durations := []struct {
		name  string
		value time.Duration
	}{
		{"KG_INDEXING_LEASE", c.Lease},
		{"KG_INDEXING_BASE_BACKOFF", c.BaseBackoff},
		{"KG_INDEXING_MAX_BACKOFF", c.MaxBackoff},
		{"KG_INDEXING_POLL_INTERVAL", c.PollInterval},
	}
	for _, d := range durations {
		if d.value <= 0 {
			errs = append(errs, fmt.Errorf("%s must be positive, got %s", d.name, d.value))
		}
	}
	if c.MaxBackoff < c.BaseBackoff {
		errs = append(errs, fmt.Errorf("KG_INDEXING_MAX_BACKOFF (%s) must not be smaller than KG_INDEXING_BASE_BACKOFF (%s)", c.MaxBackoff, c.BaseBackoff))
	}
	return errors.Join(errs...)
}

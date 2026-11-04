package openai

import (
	"errors"
	"fmt"
	"net/url"
	"time"

	"mcp-knowledge-graph/internal/knowledge"
)

// Config configures the client. It is loaded from the environment
// (github.com/caarlos0/env); the defaults target a local Ollama
// (docker compose --profile semantic up -d ollama).
type Config struct {
	// BaseURL of the API, e.g. https://api.openai.com/v1.
	BaseURL string `env:"KG_EMBEDDING_BASE_URL" envDefault:"http://localhost:11434/v1"`
	// Model is the embedding model name. It must produce
	// knowledge.EmbeddingDimensions values (natively or via Dimensions).
	Model string `env:"KG_EMBEDDING_MODEL" envDefault:"bge-m3"`
	// APIKey is sent as bearer token if set.
	APIKey string `env:"KG_EMBEDDING_API_KEY"`
	// Dimensions is sent as the "dimensions" request parameter for models
	// that support shortening (e.g. text-embedding-3-large). 0 omits it;
	// otherwise it must equal knowledge.EmbeddingDimensions.
	Dimensions int `env:"KG_EMBEDDING_DIMENSIONS" envDefault:"0"`
	// Timeout per HTTP request.
	Timeout time.Duration `env:"KG_EMBEDDING_TIMEOUT" envDefault:"60s"`
}

// Validate checks the configuration and reports all problems at once.
func (c *Config) Validate() error {
	var errs []error
	if c.BaseURL == "" {
		errs = append(errs, errors.New("KG_EMBEDDING_BASE_URL must not be empty"))
	} else if u, err := url.Parse(c.BaseURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		errs = append(errs, fmt.Errorf("KG_EMBEDDING_BASE_URL must be an absolute http(s) URL, got %q", c.BaseURL))
	}
	if c.Model == "" {
		errs = append(errs, errors.New("KG_EMBEDDING_MODEL must not be empty"))
	}
	if c.Dimensions != 0 && c.Dimensions != knowledge.EmbeddingDimensions {
		errs = append(errs, fmt.Errorf("KG_EMBEDDING_DIMENSIONS must be 0 or %d (schema dimension), got %d",
			knowledge.EmbeddingDimensions, c.Dimensions))
	}
	if c.Timeout <= 0 {
		errs = append(errs, fmt.Errorf("KG_EMBEDDING_TIMEOUT must be positive, got %s", c.Timeout))
	}
	return errors.Join(errs...)
}

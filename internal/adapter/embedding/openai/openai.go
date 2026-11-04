// Package openai implements knowledge.Embedder against any OpenAI compatible
// /embeddings endpoint (OpenAI, Ollama's /v1 API, vLLM, LocalAI, ...).
//
// By default the configuration is read from the environment (see Config);
// the defaults target a local Ollama with the multilingual bge-m3 model,
// which natively produces 1024 dimensional vectors:
//
//	e, err := openai.New()
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	env "mcp-knowledge-graph/internal/environment"
	"mcp-knowledge-graph/internal/knowledge"
)

// Embedder is the HTTP client.
type Embedder struct {
	cfg      Config
	endpoint string
	client   *http.Client
}

type options struct {
	cfg    *Config
	client *http.Client
}

// Option modifies the construction of an Embedder.
type Option func(*options)

// WithConfig sets an explicit configuration instead of loading it from the
// environment. A nil config keeps the environment based default.
func WithConfig(cfg *Config) Option {
	return func(o *options) { o.cfg = cfg }
}

// WithHTTPClient sets the HTTP client. By default a client with
// Config.Timeout is used.
func WithHTTPClient(c *http.Client) Option {
	return func(o *options) { o.client = c }
}

// New creates an Embedder. The configuration is loaded from the environment
// unless WithConfig is given; loading panics if environment values cannot be
// parsed (see env.GetConf).
func New(mods ...Option) (*Embedder, error) {
	var o options
	for _, m := range mods {
		m(&o)
	}
	if o.cfg == nil {
		o.cfg = env.GetConf[Config]()
	}
	if err := o.cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid embedding config: %w", err)
	}
	if o.client == nil {
		o.client = &http.Client{Timeout: o.cfg.Timeout}
	}
	return &Embedder{
		cfg:      *o.cfg,
		endpoint: strings.TrimRight(o.cfg.BaseURL, "/") + "/embeddings",
		client:   o.client,
	}, nil
}

// Model implements knowledge.Embedder. The configured dimension is part of
// the identifier because shortened vectors are not comparable to full ones.
func (e *Embedder) Model() string {
	if e.cfg.Dimensions > 0 {
		return fmt.Sprintf("%s@%d", e.cfg.Model, e.cfg.Dimensions)
	}
	return e.cfg.Model
}

type request struct {
	Model      string   `json:"model"`
	Input      []string `json:"input"`
	Dimensions int      `json:"dimensions,omitempty"`
}

type response struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// Embed implements knowledge.Embedder.
func (e *Embedder) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	body, err := json.Marshal(request{Model: e.cfg.Model, Input: inputs, Dimensions: e.cfg.Dimensions})
	if err != nil {
		return nil, fmt.Errorf("encode embedding request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create embedding request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if e.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.cfg.APIKey)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embedding request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("embedding request: status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	var r response
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("decode embedding response: %w", err)
	}
	if len(r.Data) != len(inputs) {
		return nil, fmt.Errorf("embedding response has %d vectors for %d inputs", len(r.Data), len(inputs))
	}
	out := make([][]float32, len(inputs))
	for _, d := range r.Data {
		if d.Index < 0 || d.Index >= len(out) || out[d.Index] != nil {
			return nil, fmt.Errorf("embedding response has invalid index %d", d.Index)
		}
		if len(d.Embedding) != knowledge.EmbeddingDimensions {
			return nil, fmt.Errorf("model %q returned %d dimensions, schema requires %d",
				e.Model(), len(d.Embedding), knowledge.EmbeddingDimensions)
		}
		out[d.Index] = d.Embedding
	}
	return out, nil
}

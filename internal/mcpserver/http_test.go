package mcpserver

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	env "mcp-knowledge-graph/internal/environment"
	"mcp-knowledge-graph/internal/knowledge"
)

const testToken = "test-token-0123456789-abcdefghijklmnop"

type pingerFunc func(context.Context) error

func (f pingerFunc) Ping(ctx context.Context) error { return f(ctx) }

func httpConfig() *Config {
	cfg := env.GetDefaultConf[Config]()
	cfg.BearerToken = testToken
	return cfg
}

// logBuffer collects log output to assert that secrets are never logged.
func logBuffer() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})), &buf
}

func newTestHTTPServer(t *testing.T, svc Service, cfg *Config, pinger Pinger) (*httptest.Server, *bytes.Buffer) {
	t.Helper()
	logger, buf := logBuffer()
	server := NewServer(svc, Options{Logger: logger})
	handler, err := NewHTTPHandler(server, cfg, pinger, logger)
	require.NoError(t, err)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts, buf
}

func TestMCPEndpointRequiresBearerTokenForAllMethods(t *testing.T) {
	ts, logs := newTestHTTPServer(t, &fakeService{}, httpConfig(), nil)

	for _, method := range []string{http.MethodPost, http.MethodGet, http.MethodDelete, http.MethodPut, http.MethodOptions} {
		for name, header := range map[string]string{
			"missing":      "",
			"wrong scheme": "Basic " + testToken,
			"wrong token":  "Bearer wrong-token-0123456789-abcdefghijklmnop",
			"prefix token": "Bearer " + testToken[:10],
		} {
			req, err := http.NewRequest(method, ts.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
			require.NoError(t, err)
			req.Header.Set("Content-Type", "application/json")
			if header != "" {
				req.Header.Set("Authorization", header)
			}
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			_ = resp.Body.Close()
			assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "%s %s", method, name)
			assert.True(t, strings.HasPrefix(resp.Header.Get("WWW-Authenticate"), "Bearer"), "%s %s", method, name)
		}
	}
	assert.NotContains(t, logs.String(), testToken)
	assert.NotContains(t, logs.String(), "wrong-token")
}

// authTransport adds the bearer token to every request.
type authTransport struct {
	token string
	base  http.RoundTripper
}

func (a authTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+a.token)
	return a.base.RoundTrip(r)
}

func TestStreamableHTTPWithToken(t *testing.T) {
	id := uuid.MustParse(docID)
	svc := &fakeService{getSummary: func(uuid.UUID) (knowledge.DocumentSummary, error) {
		return knowledge.DocumentSummary{ID: id, Title: "T", Summary: "S", Version: 4}, nil
	}}
	ts, logs := newTestHTTPServer(t, svc, httpConfig(), nil)

	client := mcp.NewClient(&mcp.Implementation{Name: "http-test", Version: "v0"}, nil)
	cs, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:   ts.URL + "/mcp",
		HTTPClient: &http.Client{Transport: authTransport{token: testToken, base: http.DefaultTransport}},
	}, nil)
	require.NoError(t, err)
	defer func() { _ = cs.Close() }()

	tools, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err)
	assert.Len(t, tools.Tools, 15)

	out := callOK[SummaryOutput](t, cs, ToolGetSummary, map[string]any{"document_id": docID})
	assert.Equal(t, SummaryOutput{ID: docID, Title: "T", Summary: "S", Version: 4}, out)
	assert.NotContains(t, logs.String(), testToken)
}

func TestRequestBodyLimit(t *testing.T) {
	cfg := httpConfig()
	cfg.MaxRequestBytes = 1024
	ts, _ := newTestHTTPServer(t, &fakeService{}, cfg, nil)

	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"padding":"` + strings.Repeat("x", 4096) + `"}}`
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/mcp", strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode)
}

func TestHealthAndReadiness(t *testing.T) {
	var fail bool
	pinger := pingerFunc(func(context.Context) error {
		if fail {
			return errors.New("dial tcp: connection refused (postgres://kg:secret@db)")
		}
		return nil
	})
	ts, _ := newTestHTTPServer(t, &fakeService{}, httpConfig(), pinger)

	get := func(path string) (int, string) {
		resp, err := http.Get(ts.URL + path)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		return resp.StatusCode, buf.String()
	}

	code, _ := get("/healthz")
	assert.Equal(t, http.StatusOK, code)
	code, _ = get("/readyz")
	assert.Equal(t, http.StatusOK, code)

	fail = true
	code, body := get("/readyz")
	assert.Equal(t, http.StatusServiceUnavailable, code)
	assert.NotContains(t, body, "secret", "readiness must not expose diagnostics")
	code, _ = get("/healthz")
	assert.Equal(t, http.StatusOK, code, "liveness does not depend on the backend")
}

func TestUnauthenticatedModeForLocalDevelopment(t *testing.T) {
	cfg := httpConfig()
	cfg.RequireAuth, cfg.BearerToken = false, ""
	ts, _ := newTestHTTPServer(t, &fakeService{}, cfg, nil)

	client := mcp.NewClient(&mcp.Implementation{Name: "http-test", Version: "v0"}, nil)
	cs, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: ts.URL + "/mcp"}, nil)
	require.NoError(t, err)
	_ = cs.Close()
}

func TestGracefulShutdown(t *testing.T) {
	cfg := httpConfig()
	cfg.ShutdownTimeout = 5 * time.Second
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	started := make(chan struct{})
	release := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/slow", func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_, _ = w.Write([]byte("done"))
	})

	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- serveListener(ctx, cfg, ln, mux, nil) }()

	type result struct {
		body string
		err  error
	}
	inflight := make(chan result, 1)
	go func() {
		resp, err := http.Get("http://" + ln.Addr().String() + "/slow")
		if err != nil {
			inflight <- result{err: err}
			return
		}
		defer func() { _ = resp.Body.Close() }()
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		inflight <- result{body: buf.String()}
	}()
	<-started
	cancel()

	// New connections are refused once shutdown started.
	require.Eventually(t, func() bool {
		conn, err := net.DialTimeout("tcp", ln.Addr().String(), 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
		}
		return err != nil
	}, 2*time.Second, 20*time.Millisecond)

	close(release)
	r := <-inflight
	require.NoError(t, r.err)
	assert.Equal(t, "done", r.body, "in-flight requests complete during graceful shutdown")
	assert.NoError(t, <-served)
}

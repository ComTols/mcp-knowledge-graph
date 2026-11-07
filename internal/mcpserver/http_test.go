package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
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
	handler, err := NewHTTPHandler(server, cfg, pinger, fakeGraph{}, logger)
	require.NoError(t, err)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts, buf
}

type fakeGraph struct{}

func (fakeGraph) GetGraph(context.Context) (knowledge.Graph, error) {
	root, child := uuid.New(), uuid.New()
	return knowledge.Graph{Elements: knowledge.GraphElements{
		Nodes: []knowledge.GraphNode{
			{Data: knowledge.GraphNodeData{ID: root, Name: "root"}},
			{Data: knowledge.GraphNodeData{ID: child, Name: "child", Parent: &root}},
		},
		Edges: []knowledge.GraphEdge{},
	}}, nil
}

var (
	fakeDocID    = uuid.MustParse("11111111-1111-1111-1111-111111111111")
	fakeParentID = uuid.MustParse("22222222-2222-2222-2222-222222222222")
	fakeChildID  = uuid.MustParse("33333333-3333-3333-3333-333333333333")
)

func (fakeGraph) GetDocument(_ context.Context, id uuid.UUID) (knowledge.Document, error) {
	if id != fakeDocID {
		return knowledge.Document{}, knowledge.ErrNotFound
	}
	return knowledge.Document{
		DocumentSummary: knowledge.DocumentSummary{ID: id, Title: "Doc", Summary: "Sum"},
		Parents:         []knowledge.ParentRef{{ID: fakeParentID, Title: "Par [x]", Summary: "Parent sum", IsPrimary: true}},
		Children:        []knowledge.ChildRef{{ID: fakeChildID, Title: "Kid", Summary: "ignored"}},
		Blocks:          []knowledge.Block{{Content: "First"}, {Content: "Second"}},
	}, nil
}

func TestFulltextParentLinksUsePublicURL(t *testing.T) {
	cfg := httpConfig()
	cfg.PublicURL = "https://kg.example.com/prefix/"
	ts, _ := newTestHTTPServer(t, &fakeService{}, cfg, nil)

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/graph/fulltext?id="+fakeDocID.String(), nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	assert.Contains(t, buf.String(), "](https://kg.example.com/prefix/ui/viewer?id="+fakeParentID.String()+")")
}

func TestFulltextEndpoint(t *testing.T) {
	ts, _ := newTestHTTPServer(t, &fakeService{}, httpConfig(), nil)
	get := func(path string, auth bool) (*http.Response, string) {
		req, err := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		require.NoError(t, err)
		if auth {
			req.Header.Set("Authorization", "Bearer "+testToken)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		return resp, buf.String()
	}

	resp, _ := get("/graph/fulltext?id="+fakeDocID.String(), false)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	resp, body := get("/graph/fulltext?id="+fakeDocID.String(), true)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Type"), "text/markdown")
	want := strings.Join([]string{
		"# Doc",
		"",
		"Sum",
		"",
		"---",
		"",
		"## Parents",
		"",
		`- [Par \[x\]](/ui/viewer?id=` + fakeParentID.String() + `)<br>Parent sum`,
		"",
		"## Children",
		"",
		`- [Kid](/ui/viewer?id=` + fakeChildID.String() + `)<br>ignored`,
		"",
		"---",
		"",
		"First",
		"",
		"Second",
		"",
	}, "\n")
	assert.Equal(t, want, body)

	resp, _ = get("/graph/fulltext?id=nope", true)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	resp, _ = get("/graph/fulltext", true)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	resp, _ = get("/graph/fulltext?id="+uuid.NewString(), true)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	// The viewer page is served behind auth, too.
	resp, _ = get("/ui/viewer", false)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	resp, body = get("/ui/viewer?id="+fakeDocID.String(), true)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, body, "viewer.js")
}

func TestGraphEndpointRequiresBearerToken(t *testing.T) {
	ts, _ := newTestHTTPServer(t, &fakeService{}, httpConfig(), nil)

	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodHead, http.MethodOptions} {
		for name, header := range map[string]string{
			"missing":     "",
			"wrong token": "Bearer wrong-token-0123456789-abcdefghijklmnop",
		} {
			req, err := http.NewRequest(method, ts.URL+"/graph", nil)
			require.NoError(t, err)
			if header != "" {
				req.Header.Set("Authorization", header)
			}
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			_ = resp.Body.Close()
			assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "%s %s", method, name)
		}
	}
}

func TestUIRequiresAuthAndServesFrontend(t *testing.T) {
	ts, _ := newTestHTTPServer(t, &fakeService{}, httpConfig(), nil)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	for _, path := range []string{"/ui", "/ui/", "/ui/app.js", "/ui/cytoscape.min.js"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			req, err := http.NewRequest(method, ts.URL+path, nil)
			require.NoError(t, err)
			resp, err := client.Do(req)
			require.NoError(t, err)
			_ = resp.Body.Close()
			assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "%s %s", method, path)
		}
	}
	// A wrong Basic password is rejected, too.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/ui/", nil)
	req.SetBasicAuth("u", "wrong-token-0123456789-abcdefghijklmnop")
	resp, err := client.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	// Basic auth (browser login) works for the UI and the graph.
	for path, wantType := range map[string]string{"/ui/": "text/html", "/ui/app.js": "javascript", "/graph": "application/json"} {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		req.SetBasicAuth("anyone", testToken)
		resp, err := client.Do(req)
		require.NoError(t, err)
		_ = resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode, path)
		assert.Contains(t, resp.Header.Get("Content-Type"), wantType, path)
	}

	// /ui redirects to /ui/ after authentication.
	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/ui", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err = client.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusMovedPermanently, resp.StatusCode)
	assert.Equal(t, "/ui/", resp.Header.Get("Location"))

	// /mcp does not accept Basic auth.
	req, _ = http.NewRequest(http.MethodPost, ts.URL+"/mcp", strings.NewReader("{}"))
	req.SetBasicAuth("anyone", testToken)
	resp, err = client.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestGraphEndpointReturnsCytoscapeJSON(t *testing.T) {
	ts, _ := newTestHTTPServer(t, &fakeService{}, httpConfig(), nil)

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/graph", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Type"), "application/json")

	var got struct {
		Elements struct {
			Nodes []struct {
				Data map[string]any `json:"data"`
			} `json:"nodes"`
			Edges []any `json:"edges"`
		} `json:"elements"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	require.Len(t, got.Elements.Nodes, 2)
	assert.Equal(t, "root", got.Elements.Nodes[0].Data["name"])
	assert.NotContains(t, got.Elements.Nodes[0].Data, "parent")
	assert.Equal(t, got.Elements.Nodes[0].Data["id"], got.Elements.Nodes[1].Data["parent"])
	assert.NotNil(t, got.Elements.Edges)

	req.Method = http.MethodPost
	resp2, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp2.Body.Close()
	assert.Equal(t, http.StatusMethodNotAllowed, resp2.StatusCode)
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

package mcpserver

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	env "mcp-knowledge-graph/internal/environment"
	"mcp-knowledge-graph/internal/knowledge"
)

func TestParseID(t *testing.T) {
	id, err := parseID("document_id", "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA")
	require.NoError(t, err)
	assert.Equal(t, docID, id.String(), "IDs are returned in lowercase canonical form")

	for _, bad := range []string{"", "x", "urn:uuid:" + docID, "{" + docID + "}", strings.ReplaceAll(docID, "-", ""), uuid.Nil.String()} {
		_, err := parseID("document_id", bad)
		assert.Error(t, err, bad)
	}
}

func TestCheckSourceURI(t *testing.T) {
	valid := []string{
		"https://example.invalid/archive/conversation-123",
		"http://localhost:3000/c/1",
		"urn:conversation:42",
		"claude://conversation/abc",
		"https://example.com/search?q=go&page=2",
	}
	for _, uri := range valid {
		assert.NoError(t, checkSourceURI(uri, 2048), uri)
	}
	invalid := []string{
		"",
		"   ",
		"conversation-123",
		" https://example.com/x",
		"https://example.com/a b",
		"javascript:alert(1)",
		"data:text/plain,hi",
		"file:///etc/passwd",
		"https://user:pass@example.com/x",
		"https://bucket.s3.amazonaws.com/x?X-Amz-Signature=abc&X-Amz-Credential=y",
		"https://storage.googleapis.com/x?X-Goog-Signature=abc",
		"https://example.com/x?access_token=abc",
		"https://example.com/x?sig=abc",
		"https:///no-host",
		"https://example.com/" + strings.Repeat("a", 2048),
	}
	for _, uri := range invalid {
		assert.Error(t, checkSourceURI(uri, 2048), uri)
	}
}

func TestBlockOperationShapes(t *testing.T) {
	h := newHandler(nil, Options{})
	a, b := "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"
	s := func(v string) *string { return &v }

	ok := []EditOperationInput{
		{Type: "replace", BlockID: &a, Content: s("x")},
		{Type: "insert", AfterBlockID: &a, Content: s("x")},
		{Type: "insert", BeforeBlockID: &a, Content: s("x")},
		{Type: "append", Content: s("x")},
		{Type: "prepend", Content: s("x")},
		{Type: "delete", BlockID: &a},
		{Type: "move", BlockID: &a, BeforeBlockID: &b},
	}
	for _, op := range ok {
		_, err := h.blockOperation(op)
		assert.NoError(t, err, "%+v", op)
	}

	bad := []EditOperationInput{
		{Type: "replace", BlockID: &a},     // missing content
		{Type: "replace", Content: s("x")}, // missing block
		{Type: "replace", BlockID: &a, Content: s("x"), AfterBlockID: &b},
		{Type: "insert", Content: s("x")}, // missing anchor
		{Type: "insert", BlockID: &a, AfterBlockID: &b, Content: s("x")},
		{Type: "append", Content: s("x"), AfterBlockID: &a},
		{Type: "append", Content: s("")},              // blank content
		{Type: "prepend"},                             // missing content
		{Type: "delete", BlockID: &a, Content: s("")}, // forbidden even if empty
		{Type: "delete", BlockID: &a, BeforeBlockID: &b},
		{Type: "move", BlockID: &a}, // missing anchor
		{Type: "move", BlockID: &a, AfterBlockID: &b, BeforeBlockID: &b},
		{Type: "move", BlockID: &a, AfterBlockID: &a}, // relative to itself
		{Type: "move", BlockID: &a, AfterBlockID: &b, Content: s("x")},
		{Type: "delete", BlockID: s("nope")},
		{Type: "Replace", BlockID: &a, Content: s("x")},
	}
	for _, op := range bad {
		_, err := h.blockOperation(op)
		assert.Error(t, err, "%+v", op)
	}

	big := strings.Repeat("x", DefaultLimits().MaxBlockBytes+1)
	_, err := h.blockOperation(EditOperationInput{Type: "append", Content: &big})
	assert.ErrorContains(t, err, "exceeds the limit")
}

func TestCursorRoundTripAndScope(t *testing.T) {
	doc := uuid.MustParse(docID)
	other := uuid.New()
	child := &knowledge.ChildrenCursor{TitleKey: "go http server", ID: uuid.New()}

	raw := encodeChildrenCursor(doc, child)
	require.NotNil(t, raw)
	got, err := decodeChildrenCursor(*raw, doc)
	require.NoError(t, err)
	assert.Equal(t, child, got)

	_, err = decodeChildrenCursor(*raw, other)
	assert.Error(t, err, "cursor of another document")
	_, err = decodeSourcesCursor(*raw, doc)
	assert.Error(t, err, "cursor of another list kind")

	src := &knowledge.SourcesCursor{CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 123456000, time.UTC), ID: uuid.New()}
	raw = encodeSourcesCursor(doc, src)
	gotSrc, err := decodeSourcesCursor(*raw, doc)
	require.NoError(t, err)
	assert.True(t, src.CreatedAt.Equal(gotSrc.CreatedAt))
	assert.Equal(t, src.ID, gotSrc.ID)

	for _, bad := range []string{"%%%", "e30", "eyJ2IjoxfQ", strings.Repeat("A", 5000)} {
		_, err := decodeChildrenCursor(bad, doc)
		assert.Error(t, err, bad)
	}
	none, err := decodeChildrenCursor("", doc)
	require.NoError(t, err)
	assert.Nil(t, none)
}

func TestConfigValidate(t *testing.T) {
	token := strings.Repeat("t", 40)
	valid := func() *Config {
		c := env.GetDefaultConf[Config]()
		c.BearerToken = token
		return c
	}
	require.NoError(t, valid().Validate())

	def := env.GetDefaultConf[Config]()
	assert.Equal(t, "http", def.Transport)
	assert.Equal(t, "127.0.0.1:8080", def.ListenAddress)
	assert.True(t, def.RequireAuth)
	assert.Equal(t, int64(2<<20), def.MaxRequestBytes)
	assert.Equal(t, DefaultLimits(), def.Limits)
	assert.ErrorContains(t, def.Validate(), "KG_MCP_BEARER_TOKEN is required")

	cases := []struct {
		name   string
		modify func(*Config)
		want   string
	}{
		{"transport", func(c *Config) { c.Transport = "sse" }, "KG_MCP_TRANSPORT"},
		{"short token", func(c *Config) { c.BearerToken = "short" }, "at least 32"},
		{"address", func(c *Config) { c.ListenAddress = "8080" }, "KG_MCP_LISTEN_ADDRESS"},
		{"no auth public", func(c *Config) { c.RequireAuth = false; c.ListenAddress = "0.0.0.0:8080" }, "loopback"},
		{"no auth all interfaces", func(c *Config) { c.RequireAuth = false; c.ListenAddress = ":8080" }, "loopback"},
		{"public url no scheme", func(c *Config) { c.PublicURL = "kg.example.com" }, "KG_MCP_PUBLIC_URL"},
		{"public url scheme", func(c *Config) { c.PublicURL = "ftp://kg.example.com" }, "KG_MCP_PUBLIC_URL"},
		{"public url query", func(c *Config) { c.PublicURL = "https://kg.example.com/?a=b" }, "KG_MCP_PUBLIC_URL"},
		{"timeout", func(c *Config) { c.RequestTimeout = 0 }, "KG_MCP_REQUEST_TIMEOUT"},
		{"limits", func(c *Config) { c.Limits.MaxBlockBytes = 0 }, "KG_MCP_MAX_BLOCK_BYTES"},
		{"operations", func(c *Config) { c.Limits.MaxEditOperations = 5000 }, "KG_MCP_MAX_EDIT_OPERATIONS"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := valid()
			c.modify(cfg)
			assert.ErrorContains(t, cfg.Validate(), c.want)
		})
	}

	// Allowed combinations.
	cfg := valid()
	cfg.RequireAuth, cfg.BearerToken, cfg.ListenAddress = false, "", "localhost:9000"
	assert.NoError(t, cfg.Validate(), "unauthenticated loopback is allowed for development")
	cfg.ListenAddress, cfg.AllowUnauthenticatedNonLoopback = "0.0.0.0:9000", true
	assert.NoError(t, cfg.Validate(), "explicit development override")
	cfg = valid()
	cfg.Transport, cfg.BearerToken = "stdio", ""
	assert.NoError(t, cfg.Validate(), "stdio needs no HTTP token")
}

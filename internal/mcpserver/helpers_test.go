package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

// connect starts server on an in-memory transport and returns a connected
// client session.
func connect(t *testing.T, server *mcp.Server) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	ct, st := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	require.NoError(t, err)
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = cs.Close()
		_ = ss.Wait()
	})
	return cs
}

// call invokes a tool and fails the test on protocol errors.
func call(t *testing.T, cs *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err, "protocol error calling %s", name)
	return res
}

// callOK invokes a tool, requires success and decodes the structured
// output into Out.
func callOK[Out any](t *testing.T, cs *mcp.ClientSession, name string, args any) Out {
	t.Helper()
	res := call(t, cs, name, args)
	require.False(t, res.IsError, "%s failed: %s", name, resultText(res))
	require.NotNil(t, res.StructuredContent, "%s returned no structured content", name)
	b, err := json.Marshal(res.StructuredContent)
	require.NoError(t, err)
	var out Out
	require.NoError(t, json.Unmarshal(b, &out))

	// The text fallback carries the same JSON object.
	var fromText Out
	require.NoError(t, json.Unmarshal([]byte(resultText(res)), &fromText), "text fallback must be JSON")
	require.Equal(t, out, fromText)
	return out
}

// callErr invokes a tool, requires isError=true with a JSON error body and
// returns the parsed error.
func callErr(t *testing.T, cs *mcp.ClientSession, name string, args any) PublicToolError {
	t.Helper()
	res := call(t, cs, name, args)
	require.True(t, res.IsError, "%s unexpectedly succeeded: %s", name, resultText(res))
	var pub PublicToolError
	require.NoError(t, json.Unmarshal([]byte(resultText(res)), &pub), "error content must be JSON: %s", resultText(res))
	require.NotEmpty(t, pub.Code)
	require.NotEmpty(t, pub.Message)
	return pub
}

func resultText(res *mcp.CallToolResult) string {
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			return tc.Text
		}
	}
	return ""
}

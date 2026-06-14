package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testServer() *Server {
	s := New("test", "1.0.0")
	s.AddTool(Tool{
		Name:        "echo",
		Description: "echoes back",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"msg": map[string]any{"type": "string"}}},
		Handler: func(_ context.Context, args json.RawMessage) (string, error) {
			var p struct {
				Msg string `json:"msg"`
			}
			_ = json.Unmarshal(args, &p)
			return "echo: " + p.Msg, nil
		},
	})
	return s
}

func TestDispatch_Initialize(t *testing.T) {
	s := testServer()
	res, rpcErr := s.dispatch(context.Background(), request{Method: "initialize", Params: json.RawMessage(`{"protocolVersion":"2025-06-18"}`)})
	require.Nil(t, rpcErr)
	m := res.(map[string]any)
	assert.Equal(t, "2025-06-18", m["protocolVersion"], "echoes the client protocol version")
	assert.Equal(t, "test", m["serverInfo"].(map[string]any)["name"])
}

func TestDispatch_ToolsList(t *testing.T) {
	s := testServer()
	res, rpcErr := s.dispatch(context.Background(), request{Method: "tools/list"})
	require.Nil(t, rpcErr)
	tools := res.(map[string]any)["tools"].([]map[string]any)
	require.Len(t, tools, 1)
	assert.Equal(t, "echo", tools[0]["name"])
}

func TestDispatch_ToolsCall(t *testing.T) {
	s := testServer()
	res, rpcErr := s.dispatch(context.Background(), request{
		Method: "tools/call",
		Params: json.RawMessage(`{"name":"echo","arguments":{"msg":"hi"}}`),
	})
	require.Nil(t, rpcErr)
	m := res.(map[string]any)
	assert.Equal(t, false, m["isError"])
	assert.Equal(t, "echo: hi", m["content"].([]map[string]any)[0]["text"])
}

func TestDispatch_UnknownMethod(t *testing.T) {
	s := testServer()
	_, rpcErr := s.dispatch(context.Background(), request{Method: "nope"})
	require.NotNil(t, rpcErr)
	assert.Equal(t, -32601, rpcErr.Code)
}

func TestDispatch_UnknownTool(t *testing.T) {
	s := testServer()
	res, rpcErr := s.dispatch(context.Background(), request{
		Method: "tools/call",
		Params: json.RawMessage(`{"name":"ghost","arguments":{}}`),
	})
	require.NotNil(t, rpcErr) // unknown tool is a protocol error (-32602)
	assert.Equal(t, -32602, rpcErr.Code)
	assert.Nil(t, res)
}

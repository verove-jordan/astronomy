// Package mcpserver is a tiny, dependency-free Model Context Protocol server over stdio
// (line-delimited JSON-RPC 2.0). It implements initialize / tools/list / tools/call / ping —
// enough to expose Go functions as MCP tools to Claude. Logs go to stderr; protocol I/O is stdin/stdout.
package mcpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

const defaultProtocolVersion = "2024-11-05"

// Tool is one callable MCP tool. InputSchema is a JSON Schema object for the arguments.
type Tool struct {
	Name        string
	Description string
	InputSchema map[string]any
	Handler     func(ctx context.Context, args json.RawMessage) (string, error)
}

// Server hosts a set of tools over stdio.
type Server struct {
	name, version   string
	protocolVersion string
	tools           []Tool
	index           map[string]Tool
	out             *bufio.Writer
}

// New creates a server with the given implementation name/version.
func New(name, version string) *Server {
	return &Server{
		name:            name,
		version:         version,
		protocolVersion: defaultProtocolVersion,
		index:           map[string]Tool{},
		out:             bufio.NewWriter(os.Stdout),
	}
}

// AddTool registers a tool.
func (s *Server) AddTool(t Tool) {
	s.tools = append(s.tools, t)
	s.index[t.Name] = t
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Run reads requests until stdin closes.
func (s *Server) Run(ctx context.Context) error {
	r := bufio.NewReaderSize(os.Stdin, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			s.handleLine(ctx, line)
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

func (s *Server) handleLine(ctx context.Context, line []byte) {
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		return // not valid JSON-RPC; ignore
	}
	if req.Method == "" {
		return
	}
	// Notifications (no id) get no response.
	if len(req.ID) == 0 {
		return
	}
	result, rpcErr := s.dispatch(ctx, req)
	s.write(response{JSONRPC: "2.0", ID: req.ID, Result: result, Error: rpcErr})
}

func (s *Server) dispatch(ctx context.Context, req request) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		return s.initialize(req.Params), nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": s.toolList()}, nil
	case "tools/call":
		return s.callTool(ctx, req.Params)
	default:
		return nil, &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}
}

func (s *Server) initialize(params json.RawMessage) any {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(params, &p)
	if p.ProtocolVersion != "" {
		s.protocolVersion = p.ProtocolVersion
	}
	return map[string]any{
		"protocolVersion": s.protocolVersion,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": s.name, "version": s.version},
	}
}

func (s *Server) toolList() []map[string]any {
	out := make([]map[string]any, 0, len(s.tools))
	for _, t := range s.tools {
		schema := t.InputSchema
		if schema == nil {
			schema = map[string]any{"type": "object"}
		}
		out = append(out, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"inputSchema": schema,
		})
	}
	return out
}

func (s *Server) callTool(ctx context.Context, params json.RawMessage) (any, *rpcError) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpcError{Code: -32602, Message: "invalid params: " + err.Error()}
	}
	tool, ok := s.index[p.Name]
	if !ok {
		return nil, &rpcError{Code: -32602, Message: "unknown tool: " + p.Name}
	}
	text, err := tool.Handler(ctx, p.Arguments)
	if err != nil {
		return toolResult("error: "+err.Error(), true), nil
	}
	return toolResult(text, false), nil
}

func toolResult(text string, isError bool) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isError,
	}
}

func (s *Server) write(resp response) {
	b, err := json.Marshal(resp)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mcp: marshal response:", err)
		return
	}
	s.out.Write(b)
	s.out.WriteByte('\n')
	s.out.Flush()
}

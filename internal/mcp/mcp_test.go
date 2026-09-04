package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"go.uber.org/zap"
)

// rpcReq is the subset of a JSON-RPC request the fake server cares about.
type rpcReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// fakeServer is an in-process MCP server over HTTP. It records the methods it
// has seen and lets a test override how individual methods respond.
type fakeServer struct {
	t *testing.T

	mu      sync.Mutex
	methods []string

	// handlers maps a JSON-RPC method to a function returning the raw "result"
	// JSON (already encoded). If a handler returns a non-nil *rpcError, an error
	// response is written instead.
	handlers map[string]func(params json.RawMessage) (json.RawMessage, *rpcError)

	// sse, when true, frames the response as a text/event-stream body.
	sse bool

	// sessionID, when set, is echoed back via the Mcp-Session-Id header.
	sessionID string
}

func newFakeServer(t *testing.T) *fakeServer {
	return &fakeServer{
		t:        t,
		handlers: map[string]func(json.RawMessage) (json.RawMessage, *rpcError){},
	}
}

func (f *fakeServer) handle(method string, fn func(json.RawMessage) (json.RawMessage, *rpcError)) {
	f.handlers[method] = fn
}

func (f *fakeServer) sawMethod(method string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.methods {
		if m == method {
			return true
		}
	}
	return false
}

func (f *fakeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req rpcReq
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}

	f.mu.Lock()
	f.methods = append(f.methods, req.Method)
	f.mu.Unlock()

	if f.sessionID != "" {
		w.Header().Set("Mcp-Session-Id", f.sessionID)
	}

	// Notifications carry no id and expect no response body.
	if len(req.ID) == 0 || string(req.ID) == "null" {
		w.WriteHeader(http.StatusOK)
		return
	}

	var result json.RawMessage
	var rerr *rpcError
	if fn, ok := f.handlers[req.Method]; ok {
		result, rerr = fn(req.Params)
	} else {
		result = json.RawMessage(`{}`)
	}

	env := map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(req.ID)}
	if rerr != nil {
		env["error"] = rerr
	} else {
		env["result"] = result
	}
	enc, _ := json.Marshal(env)

	if f.sse {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "event: message\ndata: %s\n\n", string(enc))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(enc)
}

// standardTools wires initialize + tools/list with one "echo" tool.
func (f *fakeServer) standardTools() {
	f.handle("initialize", func(json.RawMessage) (json.RawMessage, *rpcError) {
		return json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{},"serverInfo":{"name":"fake","version":"1.0"}}`), nil
	})
	f.handle("tools/list", func(json.RawMessage) (json.RawMessage, *rpcError) {
		return json.RawMessage(`{"tools":[
			{"name":"echo","description":"echo back text","inputSchema":{"type":"object","properties":{"text":{"type":"string"}}}}
		]}`), nil
	})
}

func newTestClient(t *testing.T, id string, srv *httptest.Server) *Client {
	t.Helper()
	cfg := Config{Servers: map[string]ServerConfig{
		id: {Transport: "http", URL: srv.URL},
	}}
	c := New(cfg, zap.NewNop())
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestNew_HandshakeAndToolList(t *testing.T) {
	fake := newFakeServer(t)
	fake.standardTools()
	srv := httptest.NewServer(fake)
	defer srv.Close()

	c := newTestClient(t, "files", srv)

	if !fake.sawMethod("initialize") {
		t.Fatal("server never received initialize handshake")
	}
	if !fake.sawMethod("notifications/initialized") {
		t.Fatal("server never received initialized notification")
	}
	if !fake.sawMethod("tools/list") {
		t.Fatal("server never received tools/list")
	}

	tools := c.AllTools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	tl := tools[0]
	if tl.Name != "echo" {
		t.Errorf("tool name = %q, want echo", tl.Name)
	}
	if tl.ServerID != "files" {
		t.Errorf("tool ServerID = %q, want files", tl.ServerID)
	}
	if tl.Description != "echo back text" {
		t.Errorf("tool description = %q", tl.Description)
	}
	if want := "mcp__files__echo"; tl.FullName() != want {
		t.Errorf("FullName = %q, want %q", tl.FullName(), want)
	}
	if tl.InputSchema == nil {
		t.Error("InputSchema should not be nil")
	}
}

func TestCall_Success(t *testing.T) {
	fake := newFakeServer(t)
	fake.standardTools()
	fake.handle("tools/call", func(params json.RawMessage) (json.RawMessage, *rpcError) {
		// Confirm the client forwarded the tool name and arguments.
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		_ = json.Unmarshal(params, &p)
		if p.Name != "echo" {
			t.Errorf("tools/call name = %q, want echo", p.Name)
		}
		if got, _ := p.Arguments["text"].(string); got != "hello" {
			t.Errorf("argument text = %q, want hello", got)
		}
		return json.RawMessage(`{"content":[
			{"type":"text","text":"line one"},
			{"type":"text","text":"line two"},
			{"type":"image","data":"ignored"}
		],"isError":false}`), nil
	})
	srv := httptest.NewServer(fake)
	defer srv.Close()

	c := newTestClient(t, "files", srv)

	out, err := c.Call(context.Background(), "mcp__files__echo", map[string]any{"text": "hello"})
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if out != "line one\nline two" {
		t.Errorf("Call result = %q, want %q", out, "line one\nline two")
	}
}

func TestCall_NilArgsAndEmptyContent(t *testing.T) {
	fake := newFakeServer(t)
	fake.standardTools()
	fake.handle("tools/call", func(params json.RawMessage) (json.RawMessage, *rpcError) {
		// With nil args the client must still send an arguments object.
		var p struct {
			Arguments map[string]any `json:"arguments"`
		}
		_ = json.Unmarshal(params, &p)
		if p.Arguments == nil {
			t.Error("expected non-nil arguments object even when args are nil")
		}
		return json.RawMessage(`{"content":[],"isError":false}`), nil
	})
	srv := httptest.NewServer(fake)
	defer srv.Close()

	c := newTestClient(t, "files", srv)

	out, err := c.Call(context.Background(), "mcp__files__echo", nil)
	if err != nil {
		t.Fatalf("Call error: %v", err)
	}
	if out != "(no text content)" {
		t.Errorf("empty content result = %q, want placeholder", out)
	}
}

func TestCall_ServerToolError(t *testing.T) {
	// A tool result with isError:true should surface as a Go error.
	fake := newFakeServer(t)
	fake.standardTools()
	fake.handle("tools/call", func(json.RawMessage) (json.RawMessage, *rpcError) {
		return json.RawMessage(`{"content":[{"type":"text","text":"boom went wrong"}],"isError":true}`), nil
	})
	srv := httptest.NewServer(fake)
	defer srv.Close()

	c := newTestClient(t, "files", srv)

	_, err := c.Call(context.Background(), "mcp__files__echo", nil)
	if err == nil {
		t.Fatal("expected error from isError tool result")
	}
	if !strings.Contains(err.Error(), "boom went wrong") {
		t.Errorf("error = %v, want it to contain tool error text", err)
	}
}

func TestCall_JSONRPCError(t *testing.T) {
	// The server returns a JSON-RPC error envelope for tools/call.
	fake := newFakeServer(t)
	fake.standardTools()
	fake.handle("tools/call", func(json.RawMessage) (json.RawMessage, *rpcError) {
		return nil, &rpcError{Code: -32601, Message: "method not found"}
	})
	srv := httptest.NewServer(fake)
	defer srv.Close()

	c := newTestClient(t, "files", srv)

	_, err := c.Call(context.Background(), "mcp__files__echo", nil)
	if err == nil {
		t.Fatal("expected JSON-RPC error")
	}
	if !strings.Contains(err.Error(), "method not found") {
		t.Errorf("error = %v, want JSON-RPC message", err)
	}
}

func TestCall_BadNames(t *testing.T) {
	fake := newFakeServer(t)
	fake.standardTools()
	srv := httptest.NewServer(fake)
	defer srv.Close()

	c := newTestClient(t, "files", srv)

	cases := []struct {
		name    string
		full    string
		wantSub string
	}{
		{"not-mcp-prefix", "regular_tool", "not an MCP tool"},
		{"missing-tool-part", "mcp__files", "malformed MCP tool name"},
		{"unknown-server", "mcp__nope__echo", "not available"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := c.Call(context.Background(), tc.full, nil)
			if err == nil {
				t.Fatalf("expected error for %q", tc.full)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error = %v, want substring %q", err, tc.wantSub)
			}
		})
	}
}

func TestNew_InitializeFailureNotFatal(t *testing.T) {
	// initialize returns a JSON-RPC error → server is recorded as not connected
	// but the client is still usable.
	fake := newFakeServer(t)
	fake.handle("initialize", func(json.RawMessage) (json.RawMessage, *rpcError) {
		return nil, &rpcError{Code: -32000, Message: "unauthorized"}
	})
	srv := httptest.NewServer(fake)
	defer srv.Close()

	c := newTestClient(t, "files", srv)

	if got := c.AllTools(); len(got) != 0 {
		t.Errorf("AllTools = %d, want 0 for failed server", len(got))
	}
	snap := c.ServersSnapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot len = %d, want 1", len(snap))
	}
	if snap[0].Connected {
		t.Error("server should be marked not connected")
	}
	if !strings.Contains(snap[0].Detail, "unauthorized") {
		t.Errorf("detail = %q, want initialize error", snap[0].Detail)
	}
}

func TestServersSnapshot(t *testing.T) {
	fake := newFakeServer(t)
	fake.standardTools()
	srv := httptest.NewServer(fake)
	defer srv.Close()

	c := newTestClient(t, "Files", srv)

	snap := c.ServersSnapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot len = %d, want 1", len(snap))
	}
	s := snap[0]
	if s.ID != "Files" {
		t.Errorf("ID = %q", s.ID)
	}
	if s.Transport != "http" {
		t.Errorf("Transport = %q, want http", s.Transport)
	}
	if !s.Connected {
		t.Error("expected connected")
	}
	if len(s.Tools) != 1 {
		t.Fatalf("tools len = %d, want 1", len(s.Tools))
	}
	// ServerID "Files" is sanitized to lowercase in the full name.
	if want := "mcp__files__echo"; s.Tools[0].FullName != want {
		t.Errorf("tool full name = %q, want %q", s.Tools[0].FullName, want)
	}
}

func TestSSETransport(t *testing.T) {
	// Exercise the text/event-stream response path end to end.
	fake := newFakeServer(t)
	fake.sse = true
	fake.sessionID = "sess-123"
	fake.standardTools()
	fake.handle("tools/call", func(json.RawMessage) (json.RawMessage, *rpcError) {
		return json.RawMessage(`{"content":[{"type":"text","text":"via sse"}]}`), nil
	})
	srv := httptest.NewServer(fake)
	defer srv.Close()

	c := newTestClient(t, "files", srv)

	if len(c.AllTools()) != 1 {
		t.Fatalf("expected 1 tool over SSE, got %d", len(c.AllTools()))
	}
	out, err := c.Call(context.Background(), "mcp__files__echo", nil)
	if err != nil {
		t.Fatalf("SSE Call error: %v", err)
	}
	if out != "via sse" {
		t.Errorf("SSE result = %q, want %q", out, "via sse")
	}
}

func TestAddAndRemoveServer(t *testing.T) {
	// Start with an empty client, then hot-add a server.
	c := New(Config{}, zap.NewNop())
	t.Cleanup(func() { _ = c.Close() })

	if len(c.AllTools()) != 0 {
		t.Fatalf("fresh client should have no tools")
	}

	fake := newFakeServer(t)
	fake.standardTools()
	srv := httptest.NewServer(fake)
	defer srv.Close()

	if err := c.AddServer("hot", ServerConfig{Transport: "http", URL: srv.URL}); err != nil {
		t.Fatalf("AddServer error: %v", err)
	}
	if len(c.AllTools()) != 1 {
		t.Fatalf("after AddServer expected 1 tool, got %d", len(c.AllTools()))
	}

	out, err := c.Call(context.Background(), "mcp__hot__echo", nil)
	if err == nil && out == "" {
		t.Error("expected some result or error from hot-added server")
	}

	if err := c.RemoveServer("hot"); err != nil {
		t.Fatalf("RemoveServer error: %v", err)
	}
	if len(c.AllTools()) != 0 {
		t.Errorf("after RemoveServer expected 0 tools, got %d", len(c.AllTools()))
	}
	// Removing a non-existent server is a no-op.
	if err := c.RemoveServer("ghost"); err != nil {
		t.Errorf("RemoveServer(ghost) = %v, want nil", err)
	}
}

func TestAddServer_FailureRecorded(t *testing.T) {
	// Hot-add a server whose initialize fails: AddServer returns an error and the
	// server is still recorded (disconnected) in the snapshot.
	fake := newFakeServer(t)
	fake.handle("initialize", func(json.RawMessage) (json.RawMessage, *rpcError) {
		return nil, &rpcError{Code: -32000, Message: "nope"}
	})
	srv := httptest.NewServer(fake)
	defer srv.Close()

	c := New(Config{}, zap.NewNop())
	t.Cleanup(func() { _ = c.Close() })

	err := c.AddServer("bad", ServerConfig{Transport: "http", URL: srv.URL})
	if err == nil {
		t.Fatal("expected AddServer to return the start error")
	}
	snap := c.ServersSnapshot()
	if len(snap) != 1 || snap[0].Connected {
		t.Errorf("expected one disconnected server, got %+v", snap)
	}
}

func TestUnknownTransport(t *testing.T) {
	// An unknown transport string is recorded as a startup failure.
	cfg := Config{Servers: map[string]ServerConfig{
		"weird": {Transport: "carrierpigeon"},
	}}
	c := New(cfg, zap.NewNop())
	t.Cleanup(func() { _ = c.Close() })

	snap := c.ServersSnapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot len = %d, want 1", len(snap))
	}
	if snap[0].Connected {
		t.Error("unknown-transport server should not be connected")
	}
	if !strings.Contains(snap[0].Detail, "unknown transport") {
		t.Errorf("detail = %q, want unknown transport error", snap[0].Detail)
	}
}

func TestSanitizeIDAndFullName(t *testing.T) {
	tool := Tool{ServerID: "My Server!", Name: "do-thing"}
	if want := "mcp__my_server___do-thing"; tool.FullName() != want {
		t.Errorf("FullName = %q, want %q", tool.FullName(), want)
	}
}

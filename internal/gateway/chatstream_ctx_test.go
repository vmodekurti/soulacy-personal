package gateway

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/llm"
)

// slowLLMProvider takes long enough that a cancel racing the run is visible.
// The real provider call is where the bug shows up: the engine's first LLM
// request returns "context canceled" instead of content.
type slowLLMProvider struct {
	id      string
	delay   time.Duration
	content string
}

func (p *slowLLMProvider) ID() string { return p.id }

func (p *slowLLMProvider) Complete(ctx context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	select {
	case <-time.After(p.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if req.Stream {
		ch := make(chan string, 1)
		ch <- p.content
		close(ch)
		return &llm.CompletionResponse{Stream: ch}, nil
	}
	return &llm.CompletionResponse{Content: p.content}, nil
}

func (p *slowLLMProvider) Models(context.Context) ([]string, error) { return []string{"slow"}, nil }

// POST /api/v1/chat/stream cancelled the run it had just started.
//
// The handler built runCtx and then `defer cancel()` at HANDLER scope. Fiber
// hands the connection to a body stream writer and the handler RETURNS
// IMMEDIATELY, so that defer fired before the writer — and before the engine
// goroutine's first LLM call — had got anywhere. Every streamed chat died with
// "context canceled" a few hundred microseconds in.
//
// The identical bug was found and fixed on /studio/generate/stream (see
// studio.go and studio_stream_ctx_test.go); handleChatStream never got the fix.
//
// This test goes over a REAL listener, and that is the whole point: app.Test()
// drives the body stream writer INLINE, so the handler's defers run after the
// stream has drained — the opposite ordering from a real server. Every existing
// /chat/stream test uses app.Test() and therefore passed against the broken
// handler.
func TestChatStreamDoesNotCancelItsOwnRunContext(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")
	s.llmRouter.Register(&slowLLMProvider{id: "slow", delay: 400 * time.Millisecond, content: "hello there"})

	create := `{
		"id": "slow-stream-agent",
		"name": "Slow Stream Agent",
		"trigger": "channel",
		"channels": ["http"],
		"llm": {"provider": "slow", "model": "slow"},
		"builtins": [],
		"system_prompt": "Reply.",
		"stream_reply": true,
		"enabled": true
	}`
	if status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents", "secret", create); status != http.StatusCreated {
		t.Fatalf("create agent: %d %v", status, body)
	}

	raw := readSSEOverRealSocket(t, s, "/api/v1/chat/stream", `{
		"agent_id": "slow-stream-agent",
		"user_id": "user-1",
		"text": "hello"
	}`)

	if strings.Contains(raw, "context canceled") || strings.Contains(raw, "caller cancel") {
		t.Fatalf("the run context was cancelled out from under the engine:\n%s", raw)
	}
	if !strings.Contains(raw, "[DONE]") {
		t.Fatalf("the run never completed; stream was:\n%s", raw)
	}
}

// The run id is registered so POST /chat/cancel can stop a slow run. That
// registration was also deferred at handler scope, so by the time a client
// could read the run id off the stream and act on it, the entry was gone.
func TestChatStreamRunStaysCancellableWhileItIsStillRunning(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")
	s.llmRouter.Register(&slowLLMProvider{id: "slow", delay: 2 * time.Second, content: "hello"})

	create := `{
		"id": "cancellable-agent", "name": "Cancellable", "trigger": "channel",
		"channels": ["http"], "llm": {"provider": "slow", "model": "slow"},
		"builtins": [], "system_prompt": "Reply.", "stream_reply": true, "enabled": true
	}`
	if status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents", "secret", create); status != http.StatusCreated {
		t.Fatalf("create agent: %d %v", status, body)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close() //nolint:errcheck
	go func() { _ = s.app.Listener(ln) }()
	time.Sleep(150 * time.Millisecond)

	req, _ := http.NewRequest(http.MethodPost,
		fmt.Sprintf("http://%s/api/v1/chat/stream", ln.Addr().String()),
		strings.NewReader(`{"agent_id":"cancellable-agent","user_id":"u","text":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	// Read just the first frame — the run id — while the run is still in its
	// (2s) LLM call.
	sc := bufio.NewScanner(resp.Body)
	var runID string
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "data: {\"run_id\"") {
			runID = sc.Text()
			break
		}
	}
	if runID == "" {
		t.Fatal("no run event on the stream")
	}
	if s.runReg == nil {
		t.Skip("no run registry on this server")
	}
	if !s.runReg.Cancel(strings.TrimSuffix(strings.TrimPrefix(runID, `data: {"run_id":"`), `"}`)) {
		t.Fatal("the run was already deregistered while it was still running — POST /chat/cancel cannot stop it")
	}
}

// readSSEOverRealSocket serves the app on a real TCP listener and drains one SSE
// response to completion. A real socket is required: app.Test() runs the body
// stream writer inline and hides handler-return ordering bugs.
func readSSEOverRealSocket(t *testing.T, s *Server, path, body string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close() //nolint:errcheck
	go func() { _ = s.app.Listener(ln) }()
	time.Sleep(150 * time.Millisecond)

	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://%s%s", ln.Addr().String(), path), strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer secret")

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	var out strings.Builder
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		out.WriteString(sc.Text())
		out.WriteString("\n")
	}
	return out.String()
}

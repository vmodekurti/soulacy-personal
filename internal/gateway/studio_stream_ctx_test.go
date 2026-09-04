package gateway

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The streamed generate path died on its first LLM call with
// "context canceled", in ~6ms, for every request.
//
// Cause: the handler built a cancellable run context and deferred cancelRun()
// at HANDLER scope. Fiber hands the connection to a stream writer and the
// handler returns immediately, so that defer fired before the writer — and
// before the pipeline — had started.
//
// This test goes over a REAL listener rather than app.Test(), and that is the
// whole point: app.Test() drives the body stream writer inline, so the handler's
// defers run after the stream has already drained — the opposite ordering from a
// real server. The pre-existing SSE test passed against the broken handler for
// exactly that reason.
func TestGenerateStreamDoesNotCancelItsOwnRunContext(t *testing.T) {
	s, fake := studioFake(t)
	fake.content = `{"refined_intent":"summarize the news","summary":"news digest","assumptions":[],"questions":[]}`

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() { _ = s.app.Listener(ln) }() //nolint:errcheck
	// Give the server a moment to start accepting.
	time.Sleep(150 * time.Millisecond)

	url := fmt.Sprintf("http://%s/api/v1/studio/generate/stream", ln.Addr().String())
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(`{"intent":"summarize world news on demand"}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer k")

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	var body strings.Builder
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		body.WriteString(sc.Text())
		body.WriteString("\n")
	}
	raw := body.String()
	if strings.Contains(raw, "context canceled") {
		t.Fatalf("the run context was cancelled out from under the pipeline:\n%s", raw)
	}
	if !strings.Contains(raw, "clarify_intent") {
		t.Fatalf("pipeline never started; stream was:\n%s", raw)
	}
}

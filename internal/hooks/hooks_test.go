package hooks

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/events"
	queuememory "github.com/soulacy/soulacy/internal/queue/memory"
	"github.com/soulacy/soulacy/pkg/message"
)

// fakeRT records every request and answers with a programmable status.
type fakeRT struct {
	mu       sync.Mutex
	requests []capturedReq
	status   func(attempt int) int // attempt is 1-based per URL
	calls    int
}

type capturedReq struct {
	url    string
	body   string
	header http.Header
}

func (f *fakeRT) RoundTrip(req *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(req.Body)
	f.mu.Lock()
	f.calls++
	n := f.calls
	f.requests = append(f.requests, capturedReq{url: req.URL.String(), body: string(body), header: req.Header.Clone()})
	f.mu.Unlock()
	code := http.StatusOK
	if f.status != nil {
		code = f.status(n)
	}
	return &http.Response{
		StatusCode: code,
		Body:       io.NopCloser(strings.NewReader("")),
		Header:     http.Header{},
	}, nil
}

func (f *fakeRT) snapshot() []capturedReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]capturedReq, len(f.requests))
	copy(out, f.requests)
	return out
}

func TestMatches(t *testing.T) {
	cases := []struct {
		hook  config.HookConfig
		typ   string
		agent string
		want  bool
	}{
		{config.HookConfig{On: []string{"run.failed"}}, "run.failed", "any", true},
		{config.HookConfig{On: []string{"run.failed"}}, "run.finished", "any", false},
		{config.HookConfig{On: []string{"*"}}, "tool.call", "any", true},
		{config.HookConfig{On: []string{"run.*"}}, "run.started", "any", true},
		{config.HookConfig{On: []string{"run.*"}}, "message.in", "any", false},
		{config.HookConfig{On: []string{"*"}, Agents: []string{"bot-1"}}, "error", "bot-1", true},
		{config.HookConfig{On: []string{"*"}, Agents: []string{"bot-1"}}, "error", "bot-2", false},
		{config.HookConfig{}, "anything", "any", false}, // no filter = no match
	}
	for i, c := range cases {
		if got := matches(c.hook, c.typ, c.agent); got != c.want {
			t.Errorf("case %d: matches(%v, %q, %q) = %v, want %v", i, c.hook.On, c.typ, c.agent, got, c.want)
		}
	}
}

func TestSignAndVerify(t *testing.T) {
	body := []byte(`{"schema":1,"type":"run.failed"}`)
	header := Sign("topsecret", 1765000000, body)
	if !strings.HasPrefix(header, "t=1765000000,v1=") {
		t.Fatalf("header = %q", header)
	}
	if !VerifySignature("topsecret", header, body, 1765000000+10) {
		t.Error("valid signature should verify")
	}
	if VerifySignature("wrong", header, body, 1765000000+10) {
		t.Error("wrong secret should fail")
	}
	if VerifySignature("topsecret", header, []byte("tampered"), 1765000000+10) {
		t.Error("tampered body should fail")
	}
	// Stale timestamp (> 5 min skew) is rejected.
	if VerifySignature("topsecret", header, body, 1765000000+600) {
		t.Error("stale signature should fail")
	}
}

func newTestDispatcher(t *testing.T, rt *fakeRT, hooks []config.HookConfig) (*Dispatcher, *queuememory.Backend, *deadRecorder) {
	t.Helper()
	q := queuememory.New()
	t.Cleanup(func() { q.Close() })

	dead := &deadRecorder{}
	d := NewDispatcher(q, hooks, zap.NewNop())
	d.client = &http.Client{Transport: rt}
	d.maxAttempts = 3
	d.backoff = func(attempt int) time.Duration { return time.Millisecond }
	d.onDead = dead.record

	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d, q, dead
}

type deadRecorder struct {
	mu    sync.Mutex
	hooks []string
}

func (r *deadRecorder) record(hookURL string, env events.Envelope, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hooks = append(r.hooks, hookURL+"|"+env.Type+"|"+reason)
}

func (r *deadRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.hooks)
}

func publishEvent(t *testing.T, q *queuememory.Backend, ev message.Event) {
	t.Helper()
	p := events.NewPublisher(q, zap.NewNop())
	p.PublishEvent(ev)
	t.Cleanup(func() { p.Close() })
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestDispatcher_DeliversSignedEnvelope(t *testing.T) {
	t.Setenv("TEST_HOOK_SECRET", "topsecret")
	rt := &fakeRT{}
	_, q, dead := newTestDispatcher(t, rt, []config.HookConfig{
		{On: []string{"run.failed"}, URL: "https://ops.example.com/hook", SecretEnv: "TEST_HOOK_SECRET"},
	})

	publishEvent(t, q, message.Event{
		Type: "run.failed", AgentID: "bot", SessionID: "s1",
		Payload: map[string]any{"failure_reason": "boom"},
	})

	waitFor(t, "delivery", func() bool { return len(rt.snapshot()) >= 1 })
	req := rt.snapshot()[0]

	if req.url != "https://ops.example.com/hook" {
		t.Errorf("url = %q", req.url)
	}
	if ct := req.header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q", ct)
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(req.body), &env); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if env["schema"] != float64(1) || env["type"] != "run.failed" {
		t.Errorf("envelope = %v", env)
	}
	sig := req.header.Get("X-Soulacy-Signature")
	if sig == "" {
		t.Fatal("missing X-Soulacy-Signature")
	}
	if !VerifySignature("topsecret", sig, []byte(req.body), time.Now().Unix()) {
		t.Errorf("signature does not verify: %q", sig)
	}
	if dead.count() != 0 {
		t.Errorf("dead = %d, want 0", dead.count())
	}
}

func TestDispatcher_FiltersEvents(t *testing.T) {
	rt := &fakeRT{}
	_, q, _ := newTestDispatcher(t, rt, []config.HookConfig{
		{On: []string{"run.*"}, Agents: []string{"bot-1"}, URL: "https://x.example.com/h"},
	})

	publishEvent(t, q, message.Event{Type: "tool.call", AgentID: "bot-1"})  // wrong type
	publishEvent(t, q, message.Event{Type: "run.failed", AgentID: "bot-2"}) // wrong agent
	publishEvent(t, q, message.Event{Type: "run.failed", AgentID: "bot-1"}) // match

	waitFor(t, "matching delivery", func() bool { return len(rt.snapshot()) >= 1 })
	time.Sleep(150 * time.Millisecond) // allow any incorrect extra deliveries to surface
	reqs := rt.snapshot()
	if len(reqs) != 1 {
		t.Fatalf("deliveries = %d, want 1", len(reqs))
	}
	var env map[string]any
	_ = json.Unmarshal([]byte(reqs[0].body), &env)
	if env["agent_id"] != "bot-1" || env["type"] != "run.failed" {
		t.Errorf("delivered wrong event: %v", env)
	}
}

func TestDispatcher_RetriesThenDeadLetters(t *testing.T) {
	rt := &fakeRT{status: func(int) int { return http.StatusInternalServerError }}
	_, q, dead := newTestDispatcher(t, rt, []config.HookConfig{
		{On: []string{"*"}, URL: "https://down.example.com/h"},
	})

	publishEvent(t, q, message.Event{Type: "error", AgentID: "bot"})

	waitFor(t, "dead letter", func() bool { return dead.count() >= 1 })
	if got := len(rt.snapshot()); got != 3 { // maxAttempts = 3 in test harness
		t.Errorf("attempts = %d, want 3", got)
	}
	r := func() string { dead.mu.Lock(); defer dead.mu.Unlock(); return dead.hooks[0] }()
	if !strings.Contains(r, "https://down.example.com/h") || !strings.Contains(r, "error") {
		t.Errorf("dead record = %q", r)
	}
}

func TestDispatcher_RecoversAfterTransientFailure(t *testing.T) {
	rt := &fakeRT{status: func(attempt int) int {
		if attempt == 1 {
			return http.StatusBadGateway
		}
		return http.StatusOK
	}}
	_, q, dead := newTestDispatcher(t, rt, []config.HookConfig{
		{On: []string{"*"}, URL: "https://flaky.example.com/h"},
	})

	publishEvent(t, q, message.Event{Type: "message.out", AgentID: "bot"})

	waitFor(t, "retry success", func() bool { return len(rt.snapshot()) >= 2 })
	if dead.count() != 0 {
		t.Errorf("dead = %d, want 0 (second attempt succeeded)", dead.count())
	}
}

func TestDispatcher_NoHooksIsNoop(t *testing.T) {
	q := queuememory.New()
	t.Cleanup(func() { q.Close() })
	d := NewDispatcher(q, nil, zap.NewNop())
	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("Start with no hooks: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

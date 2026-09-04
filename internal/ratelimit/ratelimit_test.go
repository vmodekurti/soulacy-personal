// ratelimit_test.go — unit tests for the MemoryCounter, tokenBucket,
// Manager token recording, and all four Fiber middleware functions.
// Pure Go — no network or external process dependencies.
package ratelimit

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newManager(t *testing.T, cfg Config) *Manager {
	t.Helper()
	m, err := New(cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

// fiberReq sends a request through a Fiber app and returns the status code.
// bearer sets Authorization: Bearer <token>; body is the raw JSON body.
func fiberReq(t *testing.T, app *fiber.App, method, path, body string) int {
	t.Helper()
	var bodyReader io.Reader
	if body != "" {
		bodyReader = bytes.NewBufferString(body)
	}
	req, err := http.NewRequest(method, path, bodyReader)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := app.Test(req, 5000)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// ---------------------------------------------------------------------------
// DefaultConfig
// ---------------------------------------------------------------------------

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.Enabled {
		t.Error("DefaultConfig: Enabled should be true")
	}
	if cfg.PerUserRPM != 60 {
		t.Errorf("DefaultConfig: PerUserRPM = %d, want 60", cfg.PerUserRPM)
	}
	if cfg.Backend != "memory" {
		t.Errorf("DefaultConfig: Backend = %q, want memory", cfg.Backend)
	}
}

// ---------------------------------------------------------------------------
// MemoryCounter
// ---------------------------------------------------------------------------

// TestMemoryCounterIncrement verifies sequential increments return 1, 2, 3…
func TestMemoryCounterIncrement(t *testing.T) {
	c := NewMemoryCounter()
	defer c.Close()

	ctx := context.Background()
	for i := int64(1); i <= 5; i++ {
		got, err := c.Increment(ctx, "key", time.Minute)
		if err != nil {
			t.Fatalf("Increment: %v", err)
		}
		if got != i {
			t.Errorf("call %d: got %d, want %d", i, got, i)
		}
	}
}

// TestMemoryCounterWindowReset verifies the counter resets to 1 after the window expires.
func TestMemoryCounterWindowReset(t *testing.T) {
	c := NewMemoryCounter()
	defer c.Close()

	ctx := context.Background()
	// First call in a very short window.
	n, _ := c.Increment(ctx, "reset-key", time.Millisecond)
	if n != 1 {
		t.Fatalf("first increment = %d, want 1", n)
	}
	// Wait for the window to expire.
	time.Sleep(5 * time.Millisecond)
	// Should reset to 1.
	n, _ = c.Increment(ctx, "reset-key", time.Millisecond)
	if n != 1 {
		t.Fatalf("after window reset = %d, want 1", n)
	}
}

// TestMemoryCounterDifferentKeys verifies that different keys have independent counters.
func TestMemoryCounterDifferentKeys(t *testing.T) {
	c := NewMemoryCounter()
	defer c.Close()

	ctx := context.Background()
	c.Increment(ctx, "a", time.Minute)
	c.Increment(ctx, "a", time.Minute)
	c.Increment(ctx, "b", time.Minute)

	na, _ := c.Increment(ctx, "a", time.Minute)
	nb, _ := c.Increment(ctx, "b", time.Minute)
	if na != 3 {
		t.Errorf("key a = %d, want 3", na)
	}
	if nb != 2 {
		t.Errorf("key b = %d, want 2", nb)
	}
}

// TestMemoryCounterConcurrent verifies no races under parallel increments.
func TestMemoryCounterConcurrent(t *testing.T) {
	c := NewMemoryCounter()
	defer c.Close()

	ctx := context.Background()
	const goroutines = 20
	const perGoroutine = 50
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				if _, err := c.Increment(ctx, "shared", time.Minute); err != nil {
					t.Errorf("Increment: %v", err)
				}
			}
		}()
	}
	wg.Wait()

	final, _ := c.Increment(ctx, "shared", time.Minute)
	want := int64(goroutines*perGoroutine) + 1
	if final != want {
		t.Errorf("final count = %d, want %d", final, want)
	}
}

// TestMemoryCounterClose verifies Close is safe to call and idempotent (no panic).
func TestMemoryCounterClose(t *testing.T) {
	c := NewMemoryCounter()
	if err := c.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// ---------------------------------------------------------------------------
// tokenBucket
// ---------------------------------------------------------------------------

// TestTokenBucketAddAndGet verifies add accumulates and get returns current total.
func TestTokenBucketAddAndGet(t *testing.T) {
	b := &tokenBucket{windowStart: time.Now()}
	b.add(100)
	b.add(50)
	got := b.get()
	if got != 150 {
		t.Errorf("get = %d, want 150", got)
	}
}

// TestTokenBucketGetReturnsZeroForExpiredWindow verifies that get() returns 0
// when the 24h window has expired, without needing an add() call first.
// (add() resets and then appends; get() only checks without mutating.)
func TestTokenBucketGetReturnsZeroForExpiredWindow(t *testing.T) {
	b := &tokenBucket{
		total:       500,
		windowStart: time.Now().Add(-25 * time.Hour),
	}
	if got := b.get(); got != 0 {
		t.Errorf("get after expired window = %d, want 0", got)
	}
}

// ---------------------------------------------------------------------------
// Manager — token recording
// ---------------------------------------------------------------------------

// TestRecordTokensAccumulates verifies RecordTokens sums tokens per user.
func TestRecordTokensAccumulates(t *testing.T) {
	cfg := Config{Enabled: true, PerUserTokensDay: 10000, Backend: "memory"}
	m := newManager(t, cfg)

	m.RecordTokens("alice", 500)
	m.RecordTokens("alice", 300)

	m.tokenMu.RLock()
	b := m.tokenBuckets["alice"]
	m.tokenMu.RUnlock()
	if b == nil {
		t.Fatal("no bucket for alice")
	}
	if got := b.get(); got != 800 {
		t.Errorf("alice tokens = %d, want 800", got)
	}
}

// TestRecordTokensNoopWhenDisabled verifies RecordTokens is a no-op when limit is 0.
func TestRecordTokensNoopWhenDisabled(t *testing.T) {
	cfg := Config{Enabled: true, PerUserTokensDay: 0, Backend: "memory"}
	m := newManager(t, cfg)

	m.RecordTokens("bob", 9999)

	m.tokenMu.RLock()
	b := m.tokenBuckets["bob"]
	m.tokenMu.RUnlock()
	if b != nil {
		t.Error("expected no bucket when PerUserTokensDay=0")
	}
}

// TestRecordAgentTokensAccumulates verifies per-agent token accumulation.
func TestRecordAgentTokensAccumulates(t *testing.T) {
	cfg := Config{Enabled: true, PerAgentTokensDay: 50000, Backend: "memory"}
	m := newManager(t, cfg)

	m.RecordAgentTokens("research-agent", 1000)
	m.RecordAgentTokens("research-agent", 2000)

	m.agentTokenMu.RLock()
	b := m.agentTokenBuckets["research-agent"]
	m.agentTokenMu.RUnlock()
	if b == nil {
		t.Fatal("no bucket for research-agent")
	}
	if got := b.get(); got != 3000 {
		t.Errorf("agent tokens = %d, want 3000", got)
	}
}

// ---------------------------------------------------------------------------
// Manager — middleware (via Fiber app.Test)
// ---------------------------------------------------------------------------

// newRPMApp builds a minimal Fiber app with one middleware and one ping route.
func newRPMApp(mw fiber.Handler) *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(mw)
	app.Get("/ping", func(c *fiber.Ctx) error {
		return c.SendStatus(fiber.StatusOK)
	})
	app.Post("/chat", func(c *fiber.Ctx) error {
		return c.SendStatus(fiber.StatusOK)
	})
	return app
}

// TestUserRPMMiddlewarePassesUnderLimit verifies requests below the limit get 200.
func TestUserRPMMiddlewarePassesUnderLimit(t *testing.T) {
	cfg := Config{Enabled: true, PerUserRPM: 5, Backend: "memory"}
	m := newManager(t, cfg)
	app := newRPMApp(m.UserRPMMiddleware())

	for i := 0; i < 5; i++ {
		status := fiberReq(t, app, http.MethodGet, "/ping", "")
		if status != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i+1, status)
		}
	}
}

// TestUserRPMMiddlewareBlocks verifies the 6th request in a 5-RPM window gets 429.
func TestUserRPMMiddlewareBlocks(t *testing.T) {
	cfg := Config{Enabled: true, PerUserRPM: 3, Backend: "memory"}
	m := newManager(t, cfg)
	app := newRPMApp(m.UserRPMMiddleware())

	for i := 0; i < 3; i++ {
		fiberReq(t, app, http.MethodGet, "/ping", "")
	}
	status := fiberReq(t, app, http.MethodGet, "/ping", "")
	if status != http.StatusTooManyRequests {
		t.Fatalf("4th request: status = %d, want 429", status)
	}
}

// TestUserRPMMiddlewareDisabledWhenLimitZero verifies that PerUserRPM=0 is a no-op.
func TestUserRPMMiddlewareDisabledWhenLimitZero(t *testing.T) {
	cfg := Config{Enabled: true, PerUserRPM: 0, Backend: "memory"}
	m := newManager(t, cfg)
	app := newRPMApp(m.UserRPMMiddleware())

	for i := 0; i < 100; i++ {
		status := fiberReq(t, app, http.MethodGet, "/ping", "")
		if status != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200 (limit disabled)", i+1, status)
		}
	}
}

// TestUserRPMMiddlewareDisabledWhenManagerDisabled verifies Enabled=false is a no-op.
func TestUserRPMMiddlewareDisabledWhenManagerDisabled(t *testing.T) {
	cfg := Config{Enabled: false, PerUserRPM: 1, Backend: "memory"}
	m := newManager(t, cfg)
	app := newRPMApp(m.UserRPMMiddleware())

	for i := 0; i < 10; i++ {
		status := fiberReq(t, app, http.MethodGet, "/ping", "")
		if status != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200 (manager disabled)", i+1, status)
		}
	}
}

// TestAgentRPMMiddlewareBlocksByPathParam verifies the path-param agent ID is rate-limited.
// The middleware is chained at the route level (not via app.Use) because Fiber only
// populates c.Params("id") after the router matches, which is after global Use middleware.
func TestAgentRPMMiddlewareBlocksByPathParam(t *testing.T) {
	cfg := Config{Enabled: true, PerAgentRPM: 2, Backend: "memory"}
	m := newManager(t, cfg)

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Get("/agents/:id", m.AgentRPMMiddleware(), func(c *fiber.Ctx) error { return c.SendStatus(200) })

	fiberReq(t, app, http.MethodGet, "/agents/my-bot", "")
	fiberReq(t, app, http.MethodGet, "/agents/my-bot", "")
	status := fiberReq(t, app, http.MethodGet, "/agents/my-bot", "")
	if status != http.StatusTooManyRequests {
		t.Fatalf("3rd request on limit-2 agent: status = %d, want 429", status)
	}
}

// TestAgentRPMMiddlewarePassesWithoutAgentID verifies requests with no agent ID pass through.
func TestAgentRPMMiddlewarePassesWithoutAgentID(t *testing.T) {
	cfg := Config{Enabled: true, PerAgentRPM: 1, Backend: "memory"}
	m := newManager(t, cfg)
	app := newRPMApp(m.AgentRPMMiddleware())

	// /ping has no :id param and no agent_id body — should always pass.
	for i := 0; i < 10; i++ {
		status := fiberReq(t, app, http.MethodGet, "/ping", "")
		if status != http.StatusOK {
			t.Fatalf("request %d without agent_id: status = %d, want 200", i+1, status)
		}
	}
}

// TestTokenQuotaMiddlewareNoopWhenLimitZero verifies PerUserTokensDay=0 is a pass-through.
func TestTokenQuotaMiddlewareNoopWhenLimitZero(t *testing.T) {
	cfg := Config{Enabled: true, PerUserTokensDay: 0, Backend: "memory"}
	m := newManager(t, cfg)
	app := newRPMApp(m.TokenQuotaMiddleware())

	status := fiberReq(t, app, http.MethodGet, "/ping", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (limit disabled)", status)
	}
}

// TestTokenQuotaMiddlewareBlocksWhenQuotaExceeded verifies the middleware returns 429
// once the user's 24h token bucket is full.
func TestTokenQuotaMiddlewareBlocksWhenQuotaExceeded(t *testing.T) {
	cfg := Config{Enabled: true, PerUserTokensDay: 1000, Backend: "memory"}
	m := newManager(t, cfg)

	// Manually pre-fill the "anon" bucket to the limit.
	m.tokenMu.Lock()
	m.tokenBuckets["anon"] = &tokenBucket{
		total:       1000,
		windowStart: time.Now(),
	}
	m.tokenMu.Unlock()

	app := newRPMApp(m.TokenQuotaMiddleware())
	status := fiberReq(t, app, http.MethodGet, "/ping", "")
	if status != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 (quota exhausted)", status)
	}
}

// TestAgentTokenQuotaMiddlewareBlocksWhenQuotaExceeded verifies per-agent token quota.
// Chained at the route level for the same reason as the RPM test above.
func TestAgentTokenQuotaMiddlewareBlocksWhenQuotaExceeded(t *testing.T) {
	cfg := Config{Enabled: true, PerAgentTokensDay: 5000, Backend: "memory"}
	m := newManager(t, cfg)

	m.agentTokenMu.Lock()
	m.agentTokenBuckets["heavy-agent"] = &tokenBucket{
		total:       5000,
		windowStart: time.Now(),
	}
	m.agentTokenMu.Unlock()

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Get("/agents/:id/chat", m.AgentTokenQuotaMiddleware(), func(c *fiber.Ctx) error { return c.SendStatus(200) })

	status := fiberReq(t, app, http.MethodGet, "/agents/heavy-agent/chat", "")
	if status != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 (agent quota exhausted)", status)
	}
}

// TestManagerClose verifies Close does not panic and stops goroutines cleanly.
func TestManagerClose(t *testing.T) {
	cfg := Config{Enabled: true, PerUserRPM: 60, Backend: "memory"}
	m, err := New(cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// ---------------------------------------------------------------------------
// NewRedisCounter stub
// ---------------------------------------------------------------------------

// TestNewRedisCounterFallsBackToMemory verifies the Redis stub returns a
// working Counter (the MemoryCounter fallback) without requiring Redis.
func TestNewRedisCounterFallsBackToMemory(t *testing.T) {
	c, err := NewRedisCounter("redis://localhost:6379")
	if err != nil {
		t.Fatalf("NewRedisCounter: %v", err)
	}
	defer c.Close()

	n, err := c.Increment(context.Background(), "redis-stub-key", time.Minute)
	if err != nil {
		t.Fatalf("Increment on fallback counter: %v", err)
	}
	if n != 1 {
		t.Errorf("first increment = %d, want 1", n)
	}
}

// ---------------------------------------------------------------------------
// Manager — redis backend selection path
// ---------------------------------------------------------------------------

// TestNewManagerRedisBackendFallsBack verifies that specifying backend=redis
// with a URL that cannot connect still produces a usable Manager (memory fallback).
func TestNewManagerRedisBackendFallsBack(t *testing.T) {
	cfg := Config{
		Enabled:    true,
		PerUserRPM: 10,
		Backend:    "redis",
		RedisURL:   "redis://localhost:6379",
	}
	m, err := New(cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("New with redis backend: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
}

// TestNewManagerRedisBackendEmptyURLErrors verifies an error is returned when
// backend=redis but redis_url is empty.
func TestNewManagerRedisBackendEmptyURLErrors(t *testing.T) {
	cfg := Config{
		Enabled:  true,
		Backend:  "redis",
		RedisURL: "",
	}
	_, err := New(cfg, zap.NewNop())
	if err == nil {
		t.Fatal("expected error for redis backend with empty url, got nil")
	}
}

// ---------------------------------------------------------------------------
// tokenBucket — add resets window
// ---------------------------------------------------------------------------

// TestTokenBucketAddResetsExpiredWindow verifies that add() resets the window
// when it has expired and starts the new total from the added value.
func TestTokenBucketAddResetsExpiredWindow(t *testing.T) {
	b := &tokenBucket{
		total:       999,
		windowStart: time.Now().Add(-25 * time.Hour),
	}
	got := b.add(42)
	if got != 42 {
		t.Errorf("add after expired window = %d, want 42", got)
	}
}

// ---------------------------------------------------------------------------
// RecordAgentTokens — disabled quota
// ---------------------------------------------------------------------------

// TestRecordAgentTokensNoopWhenDisabled verifies RecordAgentTokens is a no-op
// when PerAgentTokensDay == 0.
func TestRecordAgentTokensNoopWhenDisabled(t *testing.T) {
	cfg := Config{Enabled: true, PerAgentTokensDay: 0, Backend: "memory"}
	m := newManager(t, cfg)

	m.RecordAgentTokens("my-agent", 5000)

	m.agentTokenMu.RLock()
	b := m.agentTokenBuckets["my-agent"]
	m.agentTokenMu.RUnlock()
	if b != nil {
		t.Error("expected no bucket when PerAgentTokensDay=0")
	}
}

// ---------------------------------------------------------------------------
// AgentRPMMiddleware — body agent_id path
// ---------------------------------------------------------------------------

// TestAgentRPMMiddlewareBlocksByBodyAgentID verifies that the middleware reads
// agent_id from the JSON body (for /chat-style endpoints with no path param).
func TestAgentRPMMiddlewareBlocksByBodyAgentID(t *testing.T) {
	cfg := Config{Enabled: true, PerAgentRPM: 2, Backend: "memory"}
	m := newManager(t, cfg)
	app := newRPMApp(m.AgentRPMMiddleware())

	body := `{"agent_id":"body-bot"}`
	fiberReq(t, app, http.MethodPost, "/chat", body)
	fiberReq(t, app, http.MethodPost, "/chat", body)
	status := fiberReq(t, app, http.MethodPost, "/chat", body)
	if status != http.StatusTooManyRequests {
		t.Fatalf("3rd request via body agent_id on limit-2: status = %d, want 429", status)
	}
}

// TestAgentRPMMiddlewareDisabledWhenLimitZero verifies PerAgentRPM=0 is a no-op.
func TestAgentRPMMiddlewareDisabledWhenLimitZero(t *testing.T) {
	cfg := Config{Enabled: true, PerAgentRPM: 0, Backend: "memory"}
	m := newManager(t, cfg)
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Get("/agents/:id", m.AgentRPMMiddleware(), func(c *fiber.Ctx) error { return c.SendStatus(200) })

	for i := 0; i < 10; i++ {
		status := fiberReq(t, app, http.MethodGet, "/agents/my-bot", "")
		if status != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200 (limit disabled)", i+1, status)
		}
	}
}

// ---------------------------------------------------------------------------
// AgentTokenQuotaMiddleware — no-op and body-agent-id paths
// ---------------------------------------------------------------------------

// TestAgentTokenQuotaMiddlewareNoopWhenLimitZero verifies PerAgentTokensDay=0 is
// a pass-through.
func TestAgentTokenQuotaMiddlewareNoopWhenLimitZero(t *testing.T) {
	cfg := Config{Enabled: true, PerAgentTokensDay: 0, Backend: "memory"}
	m := newManager(t, cfg)
	app := newRPMApp(m.AgentTokenQuotaMiddleware())

	status := fiberReq(t, app, http.MethodGet, "/ping", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (limit disabled)", status)
	}
}

// TestAgentTokenQuotaMiddlewarePassesWithNoAgentID verifies that requests
// without an agent ID (no path param, no body field) pass through even when
// the middleware is active.
func TestAgentTokenQuotaMiddlewarePassesWithNoAgentID(t *testing.T) {
	cfg := Config{Enabled: true, PerAgentTokensDay: 1, Backend: "memory"}
	m := newManager(t, cfg)
	app := newRPMApp(m.AgentTokenQuotaMiddleware())

	status := fiberReq(t, app, http.MethodGet, "/ping", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (no agent id)", status)
	}
}

// TestAgentTokenQuotaMiddlewareBlocksByBodyAgentID verifies that the middleware
// reads agent_id from the JSON body when there is no path param.
func TestAgentTokenQuotaMiddlewareBlocksByBodyAgentID(t *testing.T) {
	cfg := Config{Enabled: true, PerAgentTokensDay: 5000, Backend: "memory"}
	m := newManager(t, cfg)

	m.agentTokenMu.Lock()
	m.agentTokenBuckets["body-agent"] = &tokenBucket{
		total:       5000,
		windowStart: time.Now(),
	}
	m.agentTokenMu.Unlock()

	app := newRPMApp(m.AgentTokenQuotaMiddleware())
	status := fiberReq(t, app, http.MethodPost, "/chat", `{"agent_id":"body-agent"}`)
	if status != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 (agent quota exhausted via body)", status)
	}
}

// ---------------------------------------------------------------------------
// HandleStatus
// ---------------------------------------------------------------------------

// TestHandleStatusReturnsOK verifies the status handler returns HTTP 200 with
// the expected JSON fields present for an anonymous (no JWT claims) request.
func TestHandleStatusReturnsOK(t *testing.T) {
	cfg := Config{
		Enabled:          true,
		PerUserRPM:       30,
		PerAgentRPM:      0,
		PerUserTokensDay: 2000,
		Backend:          "memory",
	}
	m := newManager(t, cfg)

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Get("/status", m.HandleStatus)

	resp, err := app.Test(newGETRequest(t, "/status"), 5000)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HandleStatus: status = %d, want 200", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	s := string(body)
	// The response must contain the config fields.
	for _, want := range []string{`"enabled"`, `"per_user_rpm"`, `"per_user_tokens_day"`, `"backend"`, `"user"`} {
		if !strings.Contains(s, want) {
			t.Errorf("HandleStatus response missing %q; body=%s", want, s)
		}
	}
}

// TestHandleStatusReflectsTokenUsage verifies that HandleStatus returns the
// calling user's current token usage (keyed as "anon" for unauthenticated).
func TestHandleStatusReflectsTokenUsage(t *testing.T) {
	cfg := Config{Enabled: true, PerUserTokensDay: 10000, Backend: "memory"}
	m := newManager(t, cfg)

	// Pre-fill the anon bucket.
	m.tokenMu.Lock()
	m.tokenBuckets["anon"] = &tokenBucket{total: 750, windowStart: time.Now()}
	m.tokenMu.Unlock()

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Get("/status", m.HandleStatus)

	resp, err := app.Test(newGETRequest(t, "/status"), 5000)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	s := string(body)
	if !strings.Contains(s, "750") {
		t.Errorf("HandleStatus: expected token usage 750 in body; got: %s", s)
	}
}

// newGETRequest is a small helper to build a plain GET request for app.Test.
func newGETRequest(t *testing.T, path string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, path, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	return req
}


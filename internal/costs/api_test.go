// api_test.go — HTTP handler tests for the costs API.
//
// Each test spins up a minimal Fiber app with the costs.API handlers wired in.
// No httptest.NewServer — all requests go through app.Test().
//
// Run with:
//
//	GOCACHE=$PWD/.gocache go test ./internal/costs/... -v
package costs

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// newAPI creates an API backed by a fresh in-memory-style Store.
func newAPIWithStore(t *testing.T) (*API, *Store) {
	t.Helper()
	s := newStore(t)
	api := NewAPI(s, zap.NewNop())
	return api, s
}

// newAPINoStore creates an API with a nil store (simulates cost tracking disabled).
func newAPINoStore(t *testing.T) *API {
	t.Helper()
	return NewAPI(nil, zap.NewNop())
}

// newApp wires a costs API into a Fiber app.  apiKey is the Bearer token
// required by the auth middleware; pass "" to disable auth.
func newApp(t *testing.T, api *API, apiKey string) *fiber.App {
	t.Helper()
	app := fiber.New(fiber.Config{DisableStartupMessage: true})

	if apiKey != "" {
		app.Use(bearerAuthMW(apiKey))
	}

	app.Get("/costs", api.HandleGetCosts)
	app.Get("/costs/:agent_id", api.HandleGetAgentCosts)
	return app
}

// bearerAuthMW returns a Fiber middleware that checks for
// "Authorization: Bearer <key>".  Unauthorized requests get 401.
func bearerAuthMW(expectedKey string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		auth := c.Get("Authorization")
		token := strings.TrimPrefix(auth, "Bearer ")
		if token != expectedKey {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
		}
		return c.Next()
	}
}

// doJSON fires a request at the Fiber app and decodes the JSON body.
func doJSON(t *testing.T, app *fiber.App, method, path, apiKey string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, path, nil)
	if err != nil {
		t.Fatalf("http.NewRequest: %v", err)
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if len(raw) == 0 {
		return resp.StatusCode, nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode JSON status=%d body=%s: %v", resp.StatusCode, string(raw), err)
	}
	return resp.StatusCode, out
}

// ---------------------------------------------------------------------------
// Auth middleware
// ---------------------------------------------------------------------------

func TestHandleGetCosts_MissingAuth_Returns401(t *testing.T) {
	api, _ := newAPIWithStore(t)
	app := newApp(t, api, "secret")

	status, body := doJSON(t, app, http.MethodGet, "/costs", "") // no key
	if status != http.StatusUnauthorized {
		t.Fatalf("missing auth status = %d body=%v", status, body)
	}
	if body["error"] == nil {
		t.Fatalf("expected error field, body=%v", body)
	}
}

func TestHandleGetCosts_WrongAuth_Returns401(t *testing.T) {
	api, _ := newAPIWithStore(t)
	app := newApp(t, api, "secret")

	status, body := doJSON(t, app, http.MethodGet, "/costs", "wrong-key")
	if status != http.StatusUnauthorized {
		t.Fatalf("wrong auth status = %d body=%v", status, body)
	}
}

func TestHandleGetAgentCosts_MissingAuth_Returns401(t *testing.T) {
	api, _ := newAPIWithStore(t)
	app := newApp(t, api, "secret")

	status, body := doJSON(t, app, http.MethodGet, "/costs/my-agent", "") // no key
	if status != http.StatusUnauthorized {
		t.Fatalf("missing auth status = %d body=%v", status, body)
	}
	if body["error"] == nil {
		t.Fatalf("expected error field, body=%v", body)
	}
}

// ---------------------------------------------------------------------------
// GET /costs — nil store
// ---------------------------------------------------------------------------

func TestHandleGetCosts_NilStore_Returns503(t *testing.T) {
	api := newAPINoStore(t)
	app := newApp(t, api, "") // no auth for simplicity

	status, body := doJSON(t, app, http.MethodGet, "/costs", "")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("nil store status = %d body=%v", status, body)
	}
	if body["error"] == nil {
		t.Fatalf("expected error field, body=%v", body)
	}
}

// ---------------------------------------------------------------------------
// GET /costs — real store
// ---------------------------------------------------------------------------

func TestHandleGetCosts_EmptyStore_Returns200WithEmptyArray(t *testing.T) {
	api, _ := newAPIWithStore(t)
	app := newApp(t, api, "")

	status, body := doJSON(t, app, http.MethodGet, "/costs", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d body=%v", status, body)
	}
	byAgent, ok := body["by_agent"].([]any)
	if !ok {
		t.Fatalf("by_agent is not a JSON array: %v", body)
	}
	if len(byAgent) != 0 {
		t.Fatalf("expected empty by_agent, got %d items", len(byAgent))
	}
	if body["generated_at"] == nil {
		t.Fatalf("expected generated_at field, body=%v", body)
	}
}

func TestHandleGetCosts_WithData_Returns200WithTotals(t *testing.T) {
	api, store := newAPIWithStore(t)
	app := newApp(t, api, "")

	ctx := context.Background()
	records := []UsageRecord{
		{AgentID: "alpha", SessionID: "s1", Provider: "test", Model: "m",
			PromptTokens: 100, CompTokens: 50, TotalTokens: 150, CostUSD: 0.01},
		{AgentID: "beta", SessionID: "s2", Provider: "test", Model: "m",
			PromptTokens: 200, CompTokens: 100, TotalTokens: 300, CostUSD: 0.02},
		{AgentID: "alpha", SessionID: "s3", Provider: "test", Model: "m",
			PromptTokens: 50, CompTokens: 25, TotalTokens: 75, CostUSD: 0.005},
	}
	for _, r := range records {
		if err := store.Record(ctx, r); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	status, body := doJSON(t, app, http.MethodGet, "/costs", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d body=%v", status, body)
	}
	byAgent, ok := body["by_agent"].([]any)
	if !ok {
		t.Fatalf("by_agent is not an array: %v", body)
	}
	// alpha + beta = 2 agents
	if len(byAgent) != 2 {
		t.Fatalf("expected 2 agents, got %d: %v", len(byAgent), byAgent)
	}

	// Verify alpha's total_tokens = 150 + 75 = 225
	var alphaRow map[string]any
	for _, row := range byAgent {
		r, _ := row.(map[string]any)
		if r["agent_id"] == "alpha" {
			alphaRow = r
		}
	}
	if alphaRow == nil {
		t.Fatalf("alpha agent not found in response")
	}
	totalTokens, ok := alphaRow["total_tokens"].(float64)
	if !ok {
		t.Fatalf("total_tokens is not a number: %v", alphaRow)
	}
	if int(totalTokens) != 225 {
		t.Fatalf("alpha total_tokens = %v, want 225", totalTokens)
	}
}

func TestHandleGetCosts_AgentIDFilter_Returns200Filtered(t *testing.T) {
	api, store := newAPIWithStore(t)
	app := newApp(t, api, "")

	ctx := context.Background()
	_ = store.Record(ctx, UsageRecord{AgentID: "alpha", SessionID: "s1", Provider: "test", Model: "m", TotalTokens: 100})
	_ = store.Record(ctx, UsageRecord{AgentID: "beta", SessionID: "s2", Provider: "test", Model: "m", TotalTokens: 200})

	status, body := doJSON(t, app, http.MethodGet, "/costs?agent_id=alpha", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d body=%v", status, body)
	}
	byAgent, ok := body["by_agent"].([]any)
	if !ok {
		t.Fatalf("by_agent is not an array: %v", body)
	}
	if len(byAgent) != 1 {
		t.Fatalf("expected 1 agent after filter, got %d: %v", len(byAgent), byAgent)
	}
	row, _ := byAgent[0].(map[string]any)
	if row["agent_id"] != "alpha" {
		t.Fatalf("expected agent_id=alpha, got %v", row["agent_id"])
	}
}

func TestHandleGetCosts_UnknownAgentFilter_Returns200EmptyArray(t *testing.T) {
	api, store := newAPIWithStore(t)
	app := newApp(t, api, "")

	ctx := context.Background()
	_ = store.Record(ctx, UsageRecord{AgentID: "alpha", SessionID: "s1", Provider: "test", Model: "m", TotalTokens: 100})

	status, body := doJSON(t, app, http.MethodGet, "/costs?agent_id=nobody", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d body=%v", status, body)
	}
	byAgent, ok := body["by_agent"].([]any)
	if !ok {
		t.Fatalf("by_agent is not an array: %v", body)
	}
	if len(byAgent) != 0 {
		t.Fatalf("expected 0 agents after filter, got %d", len(byAgent))
	}
}

func TestHandleGetCosts_SinceDuration_Returns200(t *testing.T) {
	api, store := newAPIWithStore(t)
	app := newApp(t, api, "")

	ctx := context.Background()
	_ = store.Record(ctx, UsageRecord{
		AgentID: "alpha", SessionID: "s1", Provider: "test", Model: "m",
		TotalTokens: 100, CreatedAt: time.Now().UTC(),
	})

	// ?since=24h should include records from the last 24 hours.
	status, body := doJSON(t, app, http.MethodGet, "/costs?since=24h", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d body=%v", status, body)
	}
	if body["period"] != "24h" {
		t.Fatalf("expected period=24h, got %v", body["period"])
	}
	byAgent, ok := body["by_agent"].([]any)
	if !ok {
		t.Fatalf("by_agent is not an array: %v", body)
	}
	if len(byAgent) == 0 {
		t.Fatalf("expected at least one agent with since=24h, got none")
	}
}

func TestHandleGetCosts_SinceDateString_Returns200(t *testing.T) {
	api, store := newAPIWithStore(t)
	app := newApp(t, api, "")

	ctx := context.Background()
	_ = store.Record(ctx, UsageRecord{
		AgentID: "alpha", SessionID: "s1", Provider: "test", Model: "m",
		TotalTokens: 100, CreatedAt: time.Now().UTC(),
	})

	// Use a past date so all records are included.
	status, body := doJSON(t, app, http.MethodGet, "/costs?since=2024-01-01", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d body=%v", status, body)
	}
	byAgent, ok := body["by_agent"].([]any)
	if !ok {
		t.Fatalf("by_agent is not an array: %v", body)
	}
	if len(byAgent) == 0 {
		t.Fatalf("expected records since 2024-01-01, got none")
	}
}

func TestHandleGetCosts_InvalidSince_Returns400(t *testing.T) {
	api, _ := newAPIWithStore(t)
	app := newApp(t, api, "")

	status, body := doJSON(t, app, http.MethodGet, "/costs?since=not-a-date", "")
	if status != http.StatusBadRequest {
		t.Fatalf("invalid since status = %d body=%v", status, body)
	}
	if body["error"] == nil {
		t.Fatalf("expected error field, body=%v", body)
	}
}

func TestHandleGetCosts_ResponseShape(t *testing.T) {
	api, _ := newAPIWithStore(t)
	app := newApp(t, api, "")

	status, body := doJSON(t, app, http.MethodGet, "/costs", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d body=%v", status, body)
	}
	// Mandatory fields in the response envelope.
	for _, field := range []string{"by_agent", "period", "generated_at"} {
		if _, ok := body[field]; !ok {
			t.Errorf("missing required field %q in response: %v", field, body)
		}
	}
}

// ---------------------------------------------------------------------------
// GET /costs/:agent_id — nil store
// ---------------------------------------------------------------------------

func TestHandleGetAgentCosts_NilStore_Returns503(t *testing.T) {
	api := newAPINoStore(t)
	app := newApp(t, api, "")

	status, body := doJSON(t, app, http.MethodGet, "/costs/my-agent", "")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("nil store status = %d body=%v", status, body)
	}
	if body["error"] == nil {
		t.Fatalf("expected error field, body=%v", body)
	}
}

// ---------------------------------------------------------------------------
// GET /costs/:agent_id — real store
// ---------------------------------------------------------------------------

func TestHandleGetAgentCosts_ValidAgent_Returns200WithSessions(t *testing.T) {
	api, store := newAPIWithStore(t)
	app := newApp(t, api, "")

	ctx := context.Background()
	for _, sess := range []string{"s1", "s2", "s1"} {
		_ = store.Record(ctx, UsageRecord{
			AgentID: "research", SessionID: sess,
			Provider: "test", Model: "m",
			TotalTokens: 100, CostUSD: 0.01,
		})
	}

	status, body := doJSON(t, app, http.MethodGet, "/costs/research", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d body=%v", status, body)
	}
	if body["agent_id"] != "research" {
		t.Fatalf("expected agent_id=research, got %v", body["agent_id"])
	}

	bySession, ok := body["by_session"].([]any)
	if !ok {
		t.Fatalf("by_session is not an array: %v", body)
	}
	// s1 and s2 → 2 distinct sessions
	if len(bySession) != 2 {
		t.Fatalf("expected 2 sessions, got %d: %v", len(bySession), bySession)
	}

	// total.total_tokens = 100+100+100 = 300
	total, ok := body["total"].(map[string]any)
	if !ok {
		t.Fatalf("total is not an object: %v", body)
	}
	totalTokens, ok := total["total_tokens"].(float64)
	if !ok {
		t.Fatalf("total.total_tokens is not a number: %v", total)
	}
	if int(totalTokens) != 300 {
		t.Fatalf("total.total_tokens = %v, want 300", totalTokens)
	}
}

func TestHandleGetAgentCosts_UnknownAgent_Returns200ZeroTotals(t *testing.T) {
	api, store := newAPIWithStore(t)
	app := newApp(t, api, "")

	ctx := context.Background()
	_ = store.Record(ctx, UsageRecord{
		AgentID: "other", SessionID: "s1",
		Provider: "test", Model: "m", TotalTokens: 50,
	})

	// Request for an agent that has no records.
	status, body := doJSON(t, app, http.MethodGet, "/costs/nobody", "")
	if status != http.StatusOK {
		t.Fatalf("unknown agent status = %d body=%v", status, body)
	}
	if body["agent_id"] != "nobody" {
		t.Fatalf("expected agent_id=nobody, got %v", body["agent_id"])
	}
	bySession, ok := body["by_session"].([]any)
	if !ok {
		t.Fatalf("by_session is not an array: %v", body)
	}
	if len(bySession) != 0 {
		t.Fatalf("expected 0 sessions for unknown agent, got %d", len(bySession))
	}
	total, ok := body["total"].(map[string]any)
	if !ok {
		t.Fatalf("total is not an object: %v", body)
	}
	totalTokens, _ := total["total_tokens"].(float64)
	if totalTokens != 0 {
		t.Fatalf("expected total_tokens=0 for unknown agent, got %v", totalTokens)
	}
}

func TestHandleGetAgentCosts_SinceParam_Returns200(t *testing.T) {
	api, store := newAPIWithStore(t)
	app := newApp(t, api, "")

	ctx := context.Background()
	_ = store.Record(ctx, UsageRecord{
		AgentID: "research", SessionID: "s1",
		Provider: "test", Model: "m",
		TotalTokens: 100, CreatedAt: time.Now().UTC(),
	})

	status, body := doJSON(t, app, http.MethodGet, "/costs/research?since=24h", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d body=%v", status, body)
	}
	if body["agent_id"] != "research" {
		t.Fatalf("expected agent_id=research, got %v", body["agent_id"])
	}
}

func TestHandleGetAgentCosts_InvalidSince_Returns400(t *testing.T) {
	api, _ := newAPIWithStore(t)
	app := newApp(t, api, "")

	status, body := doJSON(t, app, http.MethodGet, "/costs/research?since=bad-value", "")
	if status != http.StatusBadRequest {
		t.Fatalf("invalid since status = %d body=%v", status, body)
	}
	if body["error"] == nil {
		t.Fatalf("expected error field, body=%v", body)
	}
}

func TestHandleGetAgentCosts_ResponseShape(t *testing.T) {
	api, _ := newAPIWithStore(t)
	app := newApp(t, api, "")

	status, body := doJSON(t, app, http.MethodGet, "/costs/any-agent", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d body=%v", status, body)
	}
	for _, field := range []string{"agent_id", "by_session", "total"} {
		if _, ok := body[field]; !ok {
			t.Errorf("missing required field %q in response: %v", field, body)
		}
	}
	total, ok := body["total"].(map[string]any)
	if !ok {
		t.Fatalf("total is not an object: %v", body)
	}
	for _, subField := range []string{"total_tokens", "cost_usd"} {
		if _, ok := total[subField]; !ok {
			t.Errorf("missing field total.%q: %v", subField, total)
		}
	}
}

// ---------------------------------------------------------------------------
// parseSince unit tests (package-level function)
// ---------------------------------------------------------------------------

func TestParseSince_Empty(t *testing.T) {
	ts, label, err := parseSince("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ts.IsZero() {
		t.Fatalf("expected zero time for empty string, got %v", ts)
	}
	if label != "" {
		t.Fatalf("expected empty label, got %q", label)
	}
}

func TestParseSince_DayShorthand(t *testing.T) {
	ts, label, err := parseSince("7d")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ts.IsZero() {
		t.Fatalf("expected non-zero time for 7d")
	}
	if label != "7d" {
		t.Fatalf("expected label=7d, got %q", label)
	}
	// Should be approximately 7 days ago.
	delta := time.Since(ts)
	if delta < 6*24*time.Hour || delta > 8*24*time.Hour {
		t.Fatalf("7d parsed to %v, expected ~7 days ago", ts)
	}
}

func TestParseSince_GoDuration(t *testing.T) {
	ts, label, err := parseSince("24h")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ts.IsZero() {
		t.Fatalf("expected non-zero time for 24h")
	}
	if label != "24h" {
		t.Fatalf("expected label=24h, got %q", label)
	}
}

func TestParseSince_NegativeDuration(t *testing.T) {
	// Negative durations should be treated as their absolute value.
	ts, _, err := parseSince("-24h")
	if err != nil {
		t.Fatalf("unexpected error for -24h: %v", err)
	}
	if ts.IsZero() {
		t.Fatalf("expected non-zero time for -24h")
	}
}

func TestParseSince_RFC3339(t *testing.T) {
	ts, _, err := parseSince("2024-06-01T00:00:00Z")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ts.IsZero() {
		t.Fatalf("expected non-zero time for RFC3339")
	}
	if ts.Year() != 2024 || ts.Month() != 6 || ts.Day() != 1 {
		t.Fatalf("RFC3339 parsed to wrong date: %v", ts)
	}
}

func TestParseSince_DateOnly(t *testing.T) {
	ts, _, err := parseSince("2024-01-15")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ts.IsZero() {
		t.Fatalf("expected non-zero time for date-only")
	}
	if ts.Year() != 2024 || ts.Month() != 1 || ts.Day() != 15 {
		t.Fatalf("date-only parsed to wrong date: %v", ts)
	}
}

func TestParseSince_Invalid(t *testing.T) {
	_, _, err := parseSince("not-a-duration")
	if err == nil {
		t.Fatal("expected error for invalid input, got nil")
	}
}

func TestParseSince_GarbageString(t *testing.T) {
	_, _, err := parseSince("!@#$%^")
	if err == nil {
		t.Fatal("expected error for garbage string, got nil")
	}
}

// ---------------------------------------------------------------------------
// NewAPI
// ---------------------------------------------------------------------------

func TestNewAPI_NilStore_NotNil(t *testing.T) {
	api := NewAPI(nil, zap.NewNop())
	if api == nil {
		t.Fatal("NewAPI with nil store returned nil")
	}
}

func TestNewAPI_WithStore_NotNil(t *testing.T) {
	s := newStore(t)
	api := NewAPI(s, zap.NewNop())
	if api == nil {
		t.Fatal("NewAPI with real store returned nil")
	}
}

// ---------------------------------------------------------------------------
// Integration: auth + real store
// ---------------------------------------------------------------------------

func TestHandleGetCosts_AuthWithValidKey_Returns200(t *testing.T) {
	api, store := newAPIWithStore(t)
	app := newApp(t, api, "my-secret")

	ctx := context.Background()
	_ = store.Record(ctx, UsageRecord{
		AgentID: "agent1", SessionID: "s1",
		Provider: "test", Model: "m", TotalTokens: 50,
	})

	status, body := doJSON(t, app, http.MethodGet, "/costs", "my-secret")
	if status != http.StatusOK {
		t.Fatalf("valid auth status = %d body=%v", status, body)
	}
	byAgent, _ := body["by_agent"].([]any)
	if len(byAgent) == 0 {
		t.Fatalf("expected data in response, body=%v", body)
	}
}

func TestHandleGetAgentCosts_AuthWithValidKey_Returns200(t *testing.T) {
	api, store := newAPIWithStore(t)
	app := newApp(t, api, "my-secret")

	ctx := context.Background()
	_ = store.Record(ctx, UsageRecord{
		AgentID: "agent1", SessionID: "s1",
		Provider: "test", Model: "m", TotalTokens: 50,
	})

	status, body := doJSON(t, app, http.MethodGet, "/costs/agent1", "my-secret")
	if status != http.StatusOK {
		t.Fatalf("valid auth status = %d body=%v", status, body)
	}
	if body["agent_id"] != "agent1" {
		t.Fatalf("expected agent_id=agent1, body=%v", body)
	}
}

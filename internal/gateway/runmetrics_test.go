package gateway

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/actionlog"
	"github.com/soulacy/soulacy/internal/costs"
	storagesqlite "github.com/soulacy/soulacy/internal/storage/sqlite"
	"github.com/soulacy/soulacy/pkg/message"
)

// newTestGatewayWithMetrics wires a costs store and a real actionlog backend
// (both SQLite in temp dirs) into a test gateway, seeded for one session.
func newTestGatewayWithMetrics(t *testing.T) *Server {
	t.Helper()
	s := newTestGateway(t, "secret")

	cs, err := costs.NewStore(filepath.Join(t.TempDir(), "costs.db"))
	if err != nil {
		t.Fatalf("costs.NewStore: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	s.SetCostStore(cs)

	dir := t.TempDir()
	al, err := actionlog.New(filepath.Join(dir, "logs"), filepath.Join(dir, "actions.db"), zap.NewNop())
	if err != nil {
		t.Fatalf("actionlog.New: %v", err)
	}
	t.Cleanup(func() { al.Close() })
	s.actions = storagesqlite.NewActionLog(al)

	base := time.Date(2026, 6, 6, 14, 0, 0, 0, time.UTC)
	ctx := context.Background()

	// Two LLM calls for the session.
	if err := cs.Record(ctx, costs.UsageRecord{
		AgentID: "bot", SessionID: "sess-run-1", Provider: "openai", Model: "gpt-4o-mini",
		PromptTokens: 100, CompTokens: 40, TotalTokens: 140, CostUSD: 0.002, CreatedAt: base,
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := cs.Record(ctx, costs.UsageRecord{
		AgentID: "bot", SessionID: "sess-run-1", Provider: "openai", Model: "gpt-4o-mini",
		PromptTokens: 200, CompTokens: 60, TotalTokens: 260, CostUSD: 0.003, CreatedAt: base.Add(8 * time.Second),
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	// Event trail: run spans 10s with one tool call and a final error.
	for _, ev := range []message.Event{
		{Type: "message.in", AgentID: "bot", SessionID: "sess-run-1", Timestamp: base},
		{Type: "tool.call", AgentID: "bot", SessionID: "sess-run-1", Timestamp: base.Add(2 * time.Second)},
		{Type: "tool.result", AgentID: "bot", SessionID: "sess-run-1", Timestamp: base.Add(3 * time.Second)},
		{Type: "error", AgentID: "bot", SessionID: "sess-run-1", Timestamp: base.Add(10 * time.Second),
			Payload: map[string]any{"error": "tool exploded"}},
	} {
		al.Append(ev)
	}
	// Wait for the async writer to land all 4 events.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		st, err := al.SessionStats("bot", "sess-run-1")
		if err == nil && st.Events >= 4 {
			return s
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("actionlog events never flushed")
	return nil
}

func TestRunMetrics_CombinedSources(t *testing.T) {
	s := newTestGatewayWithMetrics(t)

	status, body := gatewayJSON(t, s, http.MethodGet,
		"/api/v1/runs/sess-run-1/metrics?agent_id=bot", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d body=%v", status, body)
	}

	if body["session_id"] != "sess-run-1" {
		t.Errorf("session_id = %v", body["session_id"])
	}
	if body["provider"] != "openai" || body["model"] != "gpt-4o-mini" {
		t.Errorf("provider/model = %v/%v", body["provider"], body["model"])
	}
	if body["llm_calls"] != float64(2) {
		t.Errorf("llm_calls = %v, want 2", body["llm_calls"])
	}
	if body["total_tokens"] != float64(400) {
		t.Errorf("total_tokens = %v, want 400", body["total_tokens"])
	}
	if body["prompt_tokens"] != float64(300) || body["comp_tokens"] != float64(100) {
		t.Errorf("prompt/comp = %v/%v, want 300/100", body["prompt_tokens"], body["comp_tokens"])
	}
	cost, _ := body["cost_usd"].(float64)
	if cost < 0.0049 || cost > 0.0051 {
		t.Errorf("cost_usd = %v, want ~0.005", body["cost_usd"])
	}
	if body["tool_calls"] != float64(1) {
		t.Errorf("tool_calls = %v, want 1", body["tool_calls"])
	}
	// Duration from event trail: 10s.
	if body["duration_ms"] != float64(10000) {
		t.Errorf("duration_ms = %v, want 10000", body["duration_ms"])
	}
	failure, _ := body["failure"].(string)
	if failure != "tool exploded" {
		t.Errorf("failure = %q, want %q", failure, "tool exploded")
	}
	if body["started_at"] == nil || body["ended_at"] == nil {
		t.Errorf("started_at/ended_at missing: %v", body)
	}
}

func TestRunMetrics_CostsOnlySession(t *testing.T) {
	s := newTestGatewayWithMetrics(t)
	// Session present in costs but with no action events.
	if err := s.costStore.Record(context.Background(), costs.UsageRecord{
		AgentID: "bot", SessionID: "costs-only", Provider: "ollama", Model: "llama3",
		PromptTokens: 10, CompTokens: 5, TotalTokens: 15, CostUSD: 0,
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	status, body := gatewayJSON(t, s, http.MethodGet,
		"/api/v1/runs/costs-only/metrics", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d body=%v", status, body)
	}
	if body["model"] != "llama3" || body["total_tokens"] != float64(15) {
		t.Errorf("metrics = %v", body)
	}
	if body["tool_calls"] != float64(0) {
		t.Errorf("tool_calls = %v, want 0", body["tool_calls"])
	}
}

func TestRunMetrics_UnknownSession404(t *testing.T) {
	s := newTestGatewayWithMetrics(t)
	status, _ := gatewayJSON(t, s, http.MethodGet,
		"/api/v1/runs/never-existed/metrics", "secret", "")
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
}

func TestRunMetrics_NoStores503(t *testing.T) {
	s := newTestGateway(t, "secret") // no cost store, no actions backend
	status, _ := gatewayJSON(t, s, http.MethodGet,
		"/api/v1/runs/whatever/metrics", "secret", "")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", status)
	}
}

func TestRunMetrics_RequiresAuth(t *testing.T) {
	s := newTestGatewayWithMetrics(t)
	status, _ := gatewayJSON(t, s, http.MethodGet,
		"/api/v1/runs/sess-run-1/metrics", "", "")
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", status)
	}
}

func TestOpsSummary_CombinedActionLogAndCosts(t *testing.T) {
	s := newTestGatewayWithMetrics(t)

	status, body := gatewayJSON(t, s, http.MethodGet,
		"/api/v1/runs/ops-summary?window=2026-01-01", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d body=%v", status, body)
	}
	if body["total_runs"] != float64(1) || body["failed_runs"] != float64(1) {
		t.Fatalf("run totals = %v", body)
	}
	if body["tool_calls"] != float64(1) {
		t.Fatalf("tool_calls = %v, want 1", body["tool_calls"])
	}
	if body["incomplete_rate"] != float64(0) {
		t.Fatalf("incomplete_rate = %v, want 0", body["incomplete_rate"])
	}
	if body["avg_duration_ms"] != float64(10000) || body["p95_duration_ms"] != float64(10000) || body["max_duration_ms"] != float64(10000) {
		t.Fatalf("duration rollup = avg %v p95 %v max %v", body["avg_duration_ms"], body["p95_duration_ms"], body["max_duration_ms"])
	}
	if body["total_tokens"] != float64(400) {
		t.Fatalf("total_tokens = %v, want 400", body["total_tokens"])
	}
	if cost, _ := body["cost_usd"].(float64); cost < 0.0049 || cost > 0.0051 {
		t.Fatalf("cost_usd = %v, want ~0.005", body["cost_usd"])
	}
	failures, _ := body["recent_failures"].([]any)
	if len(failures) != 1 {
		t.Fatalf("recent_failures = %#v", body["recent_failures"])
	}
}

func TestSLOStatusReportsRunHealth(t *testing.T) {
	s := newTestGatewayWithMetrics(t)
	s.cfg.Ops.SLOWindow = "2026-01-01"
	s.cfg.Ops.MaxFailureRate = 0.25
	s.cfg.Ops.MaxIncompleteRate = 0.1
	s.cfg.Ops.MaxP95RunDuration = "1s"
	s.cfg.Ops.MinRunsForSignal = 1

	status, body := gatewayJSON(t, s, http.MethodGet,
		"/api/v1/runs/slo-status?window=2026-01-01", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d body=%v", status, body)
	}
	if body["status"] != "fail" {
		t.Fatalf("slo status = %v, want fail: %v", body["status"], body)
	}
	if body["window"] != "2026-01-01" {
		t.Fatalf("window = %v", body["window"])
	}
	summary, _ := body["summary"].(map[string]any)
	if summary["total_runs"] != float64(1) || summary["failure_rate"] != float64(1) {
		t.Fatalf("summary = %#v", summary)
	}
	checks, _ := body["checks"].([]any)
	if len(checks) < 5 {
		t.Fatalf("checks = %#v", body["checks"])
	}
	actions, _ := body["next_actions"].([]any)
	if len(actions) == 0 {
		t.Fatalf("next_actions missing: %v", body)
	}
}

func TestReadinessIncludesSLOPosture(t *testing.T) {
	s := newTestGatewayWithMetrics(t)
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/readiness", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d body=%v", status, body)
	}
	if body["slo"] == nil {
		t.Fatalf("readiness missing slo: %v", body)
	}
	parity, _ := body["parity"].(map[string]any)
	areas, _ := parity["areas"].([]any)
	foundOps := false
	for _, item := range areas {
		area, _ := item.(map[string]any)
		if area["key"] == "ops" {
			foundOps = true
			break
		}
	}
	if !foundOps {
		t.Fatalf("parity areas missing ops: %v", parity["areas"])
	}
}

func TestOpsSummary_NoActionLog503(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, _ := gatewayJSON(t, s, http.MethodGet,
		"/api/v1/runs/ops-summary", "secret", "")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", status)
	}
}

func TestRunEvents_DurableAllAgentsAndFilters(t *testing.T) {
	s := newTestGatewayWithMetrics(t)

	status, body := gatewayJSON(t, s, http.MethodGet,
		"/api/v1/runs/events?limit=10", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d body=%v", status, body)
	}
	if body["durable"] != true || body["count"] != float64(4) {
		t.Fatalf("events response = %v", body)
	}

	status, body = gatewayJSON(t, s, http.MethodGet,
		"/api/v1/runs/events?session_id=sess-run-1&types=message.in,error", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("filtered status = %d body=%v", status, body)
	}
	events, _ := body["events"].([]any)
	if len(events) != 2 {
		t.Fatalf("filtered events = %#v", body["events"])
	}
	last, _ := events[1].(map[string]any)
	if last["type"] != "error" {
		t.Fatalf("last filtered event = %#v", last)
	}
}

func TestRunEvents_NoActionLog503(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, _ := gatewayJSON(t, s, http.MethodGet,
		"/api/v1/runs/events", "secret", "")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", status)
	}
}

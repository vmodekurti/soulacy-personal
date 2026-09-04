package costs

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func newMetricsTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(filepath.Join(t.TempDir(), "costs.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSessionMetrics_AggregatesOneSession(t *testing.T) {
	s := newMetricsTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 6, 10, 0, 0, 0, time.UTC)

	records := []UsageRecord{
		{AgentID: "bot", SessionID: "sess-1", Provider: "openai", Model: "gpt-4o-mini",
			PromptTokens: 100, CompTokens: 50, TotalTokens: 150, CostUSD: 0.001, CreatedAt: base},
		{AgentID: "bot", SessionID: "sess-1", Provider: "openai", Model: "gpt-4o",
			PromptTokens: 200, CompTokens: 80, TotalTokens: 280, CostUSD: 0.004, CreatedAt: base.Add(30 * time.Second)},
		// Different session — must be excluded.
		{AgentID: "bot", SessionID: "sess-2", Provider: "anthropic", Model: "claude",
			PromptTokens: 999, CompTokens: 999, TotalTokens: 1998, CostUSD: 9.99, CreatedAt: base},
	}
	for _, r := range records {
		if err := s.Record(ctx, r); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	m, found, err := s.SessionMetrics(ctx, "sess-1")
	if err != nil {
		t.Fatalf("SessionMetrics: %v", err)
	}
	if !found {
		t.Fatal("SessionMetrics should find sess-1")
	}
	if m.SessionID != "sess-1" {
		t.Errorf("SessionID = %q", m.SessionID)
	}
	if m.LLMCalls != 2 {
		t.Errorf("LLMCalls = %d, want 2", m.LLMCalls)
	}
	if m.PromptTokens != 300 || m.CompTokens != 130 || m.TotalTokens != 430 {
		t.Errorf("tokens = %d/%d/%d, want 300/130/430", m.PromptTokens, m.CompTokens, m.TotalTokens)
	}
	if m.CostUSD < 0.0049 || m.CostUSD > 0.0051 {
		t.Errorf("CostUSD = %f, want ~0.005", m.CostUSD)
	}
	// Provider/model from the most recent call.
	if m.Provider != "openai" || m.Model != "gpt-4o" {
		t.Errorf("provider/model = %s/%s, want openai/gpt-4o", m.Provider, m.Model)
	}
	if !m.FirstCallAt.Equal(base) {
		t.Errorf("FirstCallAt = %v, want %v", m.FirstCallAt, base)
	}
	if !m.LastCallAt.Equal(base.Add(30 * time.Second)) {
		t.Errorf("LastCallAt = %v, want %v", m.LastCallAt, base.Add(30*time.Second))
	}
}

func TestSessionMetrics_NotFound(t *testing.T) {
	s := newMetricsTestStore(t)
	_, found, err := s.SessionMetrics(context.Background(), "no-such-session")
	if err != nil {
		t.Fatalf("SessionMetrics: %v", err)
	}
	if found {
		t.Fatal("found should be false for unknown session")
	}
}

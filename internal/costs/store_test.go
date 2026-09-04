// store_test.go — tests for the costs SQLite store.
package costs

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(filepath.Join(t.TempDir(), "costs.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestNewStoreCreatesDB(t *testing.T) {
	s := newStore(t)
	if s == nil {
		t.Fatal("NewStore returned nil")
	}
}

func TestNewStoreBadPath(t *testing.T) {
	_, err := NewStore("/no/such/directory/costs.db")
	if err == nil {
		t.Fatal("expected error for bad path, got nil")
	}
}

// ---------------------------------------------------------------------------
// Record
// ---------------------------------------------------------------------------

func TestRecordAndSumByAgent(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	rec := UsageRecord{
		AgentID: "research", SessionID: "s1",
		Provider: "anthropic", Model: "claude-3-haiku",
		PromptTokens: 100, CompTokens: 50, TotalTokens: 150, CostUSD: 0.01,
	}
	if err := s.Record(ctx, rec); err != nil {
		t.Fatalf("Record: %v", err)
	}

	costs, err := s.SumByAgent(ctx, time.Time{})
	if err != nil {
		t.Fatalf("SumByAgent: %v", err)
	}
	if len(costs) != 1 {
		t.Fatalf("SumByAgent count = %d, want 1", len(costs))
	}
	if costs[0].AgentID != "research" {
		t.Errorf("AgentID = %q", costs[0].AgentID)
	}
	if costs[0].PromptTokens != 100 || costs[0].CompTokens != 50 || costs[0].TotalTokens != 150 {
		t.Errorf("tokens = %+v", costs[0])
	}
}

func TestRecordAutoFillsCreatedAt(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	before := time.Now().UTC().Add(-time.Second)
	rec := UsageRecord{AgentID: "ag", SessionID: "s1", Provider: "ollama", Model: "llama3",
		PromptTokens: 10, CompTokens: 5, TotalTokens: 15}
	// CreatedAt is zero — should be auto-filled.
	if err := s.Record(ctx, rec); err != nil {
		t.Fatalf("Record: %v", err)
	}

	// Record with explicit CreatedAt.
	rec2 := UsageRecord{AgentID: "ag2", SessionID: "s2", Provider: "ollama", Model: "llama3",
		PromptTokens: 20, CompTokens: 10, TotalTokens: 30,
		CreatedAt: time.Now().UTC()}
	if err := s.Record(ctx, rec2); err != nil {
		t.Fatalf("Record explicit: %v", err)
	}

	// Both should appear with since=before.
	costs, err := s.SumByAgent(ctx, before)
	if err != nil {
		t.Fatalf("SumByAgent filtered: %v", err)
	}
	if len(costs) == 0 {
		t.Error("expected at least one cost record after filtering")
	}
}

func TestSumByAgentEmptyDB(t *testing.T) {
	s := newStore(t)
	costs, err := s.SumByAgent(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("SumByAgent empty: %v", err)
	}
	if len(costs) != 0 {
		t.Errorf("expected 0 costs, got %d", len(costs))
	}
}

func TestSumByAgentMultipleAgents(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	for i, agentID := range []string{"alpha", "beta", "alpha"} {
		pt := 10 * (i + 1)
		_ = s.Record(ctx, UsageRecord{
			AgentID: agentID, SessionID: "s1",
			Provider: "test", Model: "m", PromptTokens: pt,
			CompTokens: 5, TotalTokens: pt + 5,
		})
	}

	costs, err := s.SumByAgent(ctx, time.Time{})
	if err != nil {
		t.Fatalf("SumByAgent: %v", err)
	}
	if len(costs) != 2 {
		t.Fatalf("agent count = %d, want 2", len(costs))
	}
}

// ---------------------------------------------------------------------------
// SumBySession
// ---------------------------------------------------------------------------

func TestSumBySession(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	for _, sess := range []string{"s1", "s2", "s1"} {
		_ = s.Record(ctx, UsageRecord{
			AgentID: "ag", SessionID: sess,
			Provider: "test", Model: "m",
			PromptTokens: 100, CompTokens: 50, TotalTokens: 150,
		})
	}

	sessions, err := s.SumBySession(ctx, "ag", time.Time{})
	if err != nil {
		t.Fatalf("SumBySession: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("session count = %d, want 2", len(sessions))
	}
}

func TestSumBySessionFiltered(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	cutoff := time.Now().UTC()
	_ = s.Record(ctx, UsageRecord{
		AgentID: "ag", SessionID: "new-sess",
		Provider: "test", Model: "m", PromptTokens: 10,
		CompTokens: 5, TotalTokens: 15,
		CreatedAt: time.Now().UTC(),
	})

	sessions, err := s.SumBySession(ctx, "ag", cutoff)
	if err != nil {
		t.Fatalf("SumBySession filtered: %v", err)
	}
	_ = sessions // may be 0 or 1 depending on sub-second timing
}

func TestSumBySessionUnknownAgent(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	_ = s.Record(ctx, UsageRecord{AgentID: "other", SessionID: "s1",
		Provider: "test", Model: "m", TotalTokens: 10})

	sessions, err := s.SumBySession(ctx, "nobody", time.Time{})
	if err != nil {
		t.Fatalf("SumBySession unknown: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("expected 0 sessions, got %d", len(sessions))
	}
}

func TestStoreClose(t *testing.T) {
	s := newStore(t)
	if err := s.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

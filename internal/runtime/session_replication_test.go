package runtime

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/session"
)

type replicatedHistory struct {
	mu      sync.Mutex
	entries map[string][]session.ConversationEntry
	loads   map[string]int
}

func (h *replicatedHistory) Append(_ context.Context, entry session.ConversationEntry) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.entries[entry.WorkspaceID] = append(h.entries[entry.WorkspaceID], entry)
	return nil
}
func (h *replicatedHistory) Load(_ context.Context, workspaceID, _ string, _ int) ([]session.ConversationEntry, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.loads[workspaceID]++
	return append([]session.ConversationEntry(nil), h.entries[workspaceID]...), nil
}
func (*replicatedHistory) LoadForAgent(context.Context, string, string, string, int) ([]session.ConversationEntry, error) {
	return nil, nil
}
func (*replicatedHistory) Prune(context.Context, time.Duration) (int64, error) { return 0, nil }
func (*replicatedHistory) Close() error                                        { return nil }

func TestNewReplicaHydratesConversationBeforeUse(t *testing.T) {
	store := &replicatedHistory{
		entries: map[string][]session.ConversationEntry{
			"ws_a": {{WorkspaceID: "ws_a", Role: "user", Content: "remember alpha"}, {WorkspaceID: "ws_a", Role: "assistant", Content: "alpha remembered"}},
		},
		loads: map[string]int{},
	}
	e := newMinimalEngine(t)
	e.SetHistoryStore(store)

	sess, err := e.getOrCreateSessionContext(context.Background(), "ws_a", "same-session", "assistant")
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.History) != 2 || sess.History[0].Content != "remember alpha" {
		t.Fatalf("hydrated history = %+v", sess.History)
	}
	if _, err = e.getOrCreateSessionContext(context.Background(), "ws_a", "same-session", "assistant"); err != nil {
		t.Fatal(err)
	}
	if store.loads["ws_a"] != 1 {
		t.Fatalf("durable history loaded %d times, want once", store.loads["ws_a"])
	}
}

func TestIdenticalAgentAndSessionIDsDoNotShareTenantCache(t *testing.T) {
	store := &replicatedHistory{
		entries: map[string][]session.ConversationEntry{
			"ws_a": {{WorkspaceID: "ws_a", Role: "user", Content: "tenant A"}},
			"ws_b": {{WorkspaceID: "ws_b", Role: "user", Content: "tenant B"}},
		},
		loads: map[string]int{},
	}
	e := newMinimalEngine(t)
	e.SetHistoryStore(store)
	a, err := e.getOrCreateSessionContext(context.Background(), "ws_a", "same-session", "same-agent")
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.getOrCreateSessionContext(context.Background(), "ws_b", "same-session", "same-agent")
	if err != nil {
		t.Fatal(err)
	}
	if a == b || a.History[0].Content != "tenant A" || b.History[0].Content != "tenant B" {
		t.Fatalf("tenant session collision: A=%p %+v B=%p %+v", a, a.History, b, b.History)
	}
}

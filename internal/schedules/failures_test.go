package schedules

import (
	"context"
	"path/filepath"
	"testing"
)

func failureStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "schedules.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, context.Background()
}

func seedSchedule(t *testing.T, store *Store, ctx context.Context, workspaceID, agentID string) {
	t.Helper()
	if _, err := store.Upsert(ctx, Schedule{
		ID: agentID, WorkspaceID: workspaceID, AgentID: agentID, Cron: "0 * * * *",
		Timezone: "UTC", Enabled: true,
	}); err != nil {
		t.Fatalf("seed %s/%s: %v", workspaceID, agentID, err)
	}
}

// The counter has to accumulate across processes, or a replica that never fires
// the same agent twice in a row can never reach the limit.
func TestFailuresAccumulateAcrossProcesses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schedules.db")
	ctx := context.Background()

	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	seedSchedule(t, first, ctx, "ws_a", "bot")
	if n, err := first.RecordFailure(ctx, "ws_a", "bot"); err != nil || n != 1 {
		t.Fatalf("first failure: n=%d err=%v", n, err)
	}
	_ = first.Close()

	// A different process — which in scale mode is a different replica.
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	n, err := second.RecordFailure(ctx, "ws_a", "bot")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("count = %d, want 2; each replica counts its own failures and a chronically "+
			"broken agent is never auto-disabled", n)
	}
}

// A success resets it, or an agent that fails intermittently forever is
// eventually disabled for no good reason.
func TestASuccessClearsTheCount(t *testing.T) {
	store, ctx := failureStore(t)
	seedSchedule(t, store, ctx, "ws_a", "bot")
	_, _ = store.RecordFailure(ctx, "ws_a", "bot")
	_, _ = store.RecordFailure(ctx, "ws_a", "bot")

	if err := store.ClearFailures(ctx, "ws_a", "bot"); err != nil {
		t.Fatal(err)
	}
	if n, _ := store.ConsecutiveFailures(ctx, "ws_a", "bot"); n != 0 {
		t.Fatalf("count = %d after a success", n)
	}
}

// Two workspaces' agents share an id all the time. One tenant's broken agent
// must not disable another's.
func TestOneWorkspacesFailuresDoNotCountAgainstAnother(t *testing.T) {
	store, ctx := failureStore(t)
	seedSchedule(t, store, ctx, "ws_a", "bot")
	seedSchedule(t, store, ctx, "ws_b", "bot")

	for i := 0; i < 3; i++ {
		if _, err := store.RecordFailure(ctx, "ws_a", "bot"); err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := store.ConsecutiveFailures(ctx, "ws_b", "bot"); n != 0 {
		t.Fatalf("ws_b count = %d; another tenant's failures counted against it", n)
	}
	if n, _ := store.ConsecutiveFailures(ctx, "ws_a", "bot"); n != 3 {
		t.Fatalf("ws_a count = %d, want 3", n)
	}
}

// An agent fired without a durable schedule row must not error and must not be
// silently treated as having zero failures forever.
func TestAnAgentWithNoScheduleRowReportsNothingRatherThanFailing(t *testing.T) {
	store, ctx := failureStore(t)
	n, err := store.RecordFailure(ctx, "ws_a", "unscheduled")
	if err != nil {
		t.Fatalf("err = %v; a missing row turned into a failed run", err)
	}
	if n != 0 {
		t.Fatalf("n = %d, want 0 so the caller falls back to its own counter", n)
	}
}

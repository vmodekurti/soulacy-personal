// tenancy_test.go — MU-023 parts 1 and 2 meeting: schedules are keyed by
// workspace, and an occurrence fires exactly once across gateway instances.
// Criterion 6's failover cases kill an instance at the claim and enqueue
// boundaries.
package scheduler

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/internal/schedules"
	"github.com/soulacy/soulacy/pkg/agent"
)

func newScheduleStore(t *testing.T) *schedules.Store {
	t.Helper()
	store, err := schedules.Open(filepath.Join(t.TempDir(), "schedules.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func seedSchedule(t *testing.T, store *schedules.Store, workspaceID, agentID string) schedules.Schedule {
	t.Helper()
	sched, err := store.Upsert(context.Background(), schedules.Schedule{
		WorkspaceID: workspaceID, AgentID: agentID, Cron: "0 7 * * *",
		Timezone: "UTC", Enabled: true, CreatedBy: "usr_a",
	})
	if err != nil {
		t.Fatal(err)
	}
	return sched
}

// The bug this story is about, at the level it actually bit: two tenants with
// the same agent ID must get two cron entries, not one that replaced the other.
func TestTwoWorkspacesGetTheirOwnCronEntryForTheSameAgentID(t *testing.T) {
	s := New(nil, nil, zap.NewNop(), context.Background())
	def := &agent.Definition{
		ID: "daily-report", Enabled: true, Trigger: agent.TriggerCron,
		Schedule: &agent.Schedule{Cron: "0 7 * * *"},
	}
	for _, ws := range []string{"ws-a", "ws-b"} {
		if err := s.RegisterAgentInWorkspace(ws, def); err != nil {
			t.Fatalf("%s: %v", ws, err)
		}
	}
	s.mu.Lock()
	entries := len(s.entries)
	_, hasA := s.entries[keyFor("ws-a", "daily-report")]
	_, hasB := s.entries[keyFor("ws-b", "daily-report")]
	s.mu.Unlock()
	if entries != 2 || !hasA || !hasB {
		t.Fatalf("entries = %d (a=%v b=%v); one workspace's schedule replaced the other's", entries, hasA, hasB)
	}

	// Deregistering one leaves the other running. Keyed by agent ID, removing
	// a schedule in one tenant silently stopped the other's.
	s.DeregisterAgentInWorkspace("ws-a", "daily-report")
	s.mu.Lock()
	_, stillA := s.entries[keyFor("ws-a", "daily-report")]
	_, stillB := s.entries[keyFor("ws-b", "daily-report")]
	s.mu.Unlock()
	if stillA || !stillB {
		t.Fatalf("deregister crossed the workspace boundary: a=%v b=%v", stillA, stillB)
	}
}

// The run lock is per (workspace, agent). Keyed by agent alone, one tenant's
// long-running agent silently suppressed every other tenant's.
func TestOneWorkspacesRunningAgentDoesNotBlockAnothers(t *testing.T) {
	s := New(nil, nil, zap.NewNop(), context.Background())
	if !s.tryStartRun(keyFor("ws-a", "bot")) {
		t.Fatal("first claim failed")
	}
	if s.tryStartRun(keyFor("ws-a", "bot")) {
		t.Fatal("the same workspace's agent started twice concurrently")
	}
	if !s.tryStartRun(keyFor("ws-b", "bot")) {
		t.Fatal("another workspace's agent was blocked by an unrelated tenant's run")
	}
}

// Criterion 2, through the scheduler rather than the store: however many
// instances tick at once, one occurrence is claimed once.
func TestOnlyOneSchedulerInstanceClaimsAnOccurrence(t *testing.T) {
	store := newScheduleStore(t)
	seedSchedule(t, store, "ws-a", "bot")
	key := keyFor("ws-a", "bot")
	due := time.Date(2026, 8, 16, 7, 0, 0, 0, time.UTC)

	const instances = 6
	claimed := make([]bool, instances)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < instances; i++ {
		s := New(nil, nil, zap.NewNop(), context.Background())
		s.SetScheduleStore(store, "gw-"+string(rune('a'+i)))
		wg.Add(1)
		go func(i int, s *Scheduler) {
			defer wg.Done()
			<-start
			_, ok := s.claimOccurrence(key, due)
			claimed[i] = ok
		}(i, s)
	}
	close(start)
	wg.Wait()

	winners := 0
	for _, ok := range claimed {
		if ok {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("%d instances claimed the same occurrence, want 1", winners)
	}
}

// A personal deployment has no store and one process. It must keep firing
// exactly as it always did (invariant 7).
func TestWithNoStoreEveryOccurrenceIsClaimedLocally(t *testing.T) {
	s := New(nil, nil, zap.NewNop(), context.Background())
	if _, ok := s.claimOccurrence(keyFor("", "bot"), time.Now().UTC()); !ok {
		t.Fatal("a single personal gateway refused to fire its own schedule")
	}
}

// A store wired without an instance identity cannot claim safely. Firing
// anyway would restore duplicate execution on every instance, so it refuses.
func TestAStoreWithNoInstanceIdentityRefusesToFire(t *testing.T) {
	store := newScheduleStore(t)
	s := New(nil, nil, zap.NewNop(), context.Background())
	s.SetScheduleStore(store, "   ")
	if _, ok := s.claimOccurrence(keyFor("ws-a", "bot"), time.Now().UTC()); ok {
		t.Fatal("an anonymous instance claimed an occurrence")
	}
}

// ── Failover (criterion 6) ─────────────────────────────────────────────────

// Killed AT THE CLAIM BOUNDARY: the instance won the claim and died before
// running anything. Another instance must be able to take it once the lease
// expires — and must NOT be able to before.
func TestFailoverAtTheClaimBoundary(t *testing.T) {
	store := newScheduleStore(t)
	sched := seedSchedule(t, store, "ws-a", "bot")
	ctx := context.Background()
	due := time.Date(2026, 8, 16, 7, 0, 0, 0, time.UTC)

	// gw-1 claims with a long lease, then "dies" — nothing completes it.
	if _, err := store.Claim(ctx, "ws-a", sched.ID, due, "gw-1", time.Hour); err != nil {
		t.Fatal(err)
	}
	// While the lease is live the work is NOT retried. This is the direction
	// that matters: a slow run must not be executed twice just because the
	// process holding it stopped answering for a while.
	survivor := New(nil, nil, zap.NewNop(), ctx)
	survivor.SetScheduleStore(store, "gw-2")
	if _, ok := survivor.claimOccurrence(keyFor("ws-a", "bot"), due); ok {
		t.Fatal("a live lease was stolen; the occurrence would run twice")
	}

	// With an expired lease the occurrence becomes available, so the work is
	// not lost either.
	expired := time.Date(2026, 8, 17, 7, 0, 0, 0, time.UTC)
	if _, err := store.Claim(ctx, "ws-a", sched.ID, expired, "gw-1", time.Nanosecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	if _, ok := survivor.claimOccurrence(keyFor("ws-a", "bot"), expired); !ok {
		t.Fatal("an occurrence orphaned by a dead instance was never recovered")
	}
}

// Killed AT THE ENQUEUE BOUNDARY: the run happened and was recorded. It must
// never run again, however long the lease has been expired — a completed
// occurrence is completed forever.
func TestFailoverAtTheEnqueueBoundary(t *testing.T) {
	store := newScheduleStore(t)
	sched := seedSchedule(t, store, "ws-a", "bot")
	ctx := context.Background()
	due := time.Date(2026, 8, 16, 7, 0, 0, 0, time.UTC)

	claim, err := store.Claim(ctx, "ws-a", sched.ID, due, "gw-1", time.Nanosecond)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(ctx, claim, nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond) // the lease is long gone

	survivor := New(nil, nil, zap.NewNop(), ctx)
	survivor.SetScheduleStore(store, "gw-2")
	if _, ok := survivor.claimOccurrence(keyFor("ws-a", "bot"), due); ok {
		t.Fatal("a completed occurrence was re-run after failover")
	}
	// And the enqueued run carried a deterministic key, so even a duplicate
	// enqueue would be refused by the run store's own uniqueness.
	if claim.IdempotencyKey != schedules.IdempotencyKey(sched.ID, due) {
		t.Fatalf("idempotency key = %q", claim.IdempotencyKey)
	}
}

// An unreachable store must not become "fire anyway". Two instances that both
// fail to claim would both fire — the duplicate execution this exists to
// prevent — and a missed occurrence is recoverable where a duplicated side
// effect is not.
func TestAnUnreachableStoreRefusesRatherThanFiring(t *testing.T) {
	store := newScheduleStore(t)
	s := New(nil, nil, zap.NewNop(), context.Background())
	s.SetScheduleStore(store, "gw-1")
	_ = store.Close() // the database is gone under us

	if _, ok := s.claimOccurrence(keyFor("ws-a", "bot"), time.Now().UTC()); ok {
		t.Fatal("an unreachable store was treated as permission to fire")
	}
}

// The service principal a fire runs under is derived per workspace. One
// process-wide principal is why a multi-user deployment could only be correct
// by refusing to fire at all.
func TestEachWorkspacesFireGetsItsOwnPrincipal(t *testing.T) {
	s := New(nil, nil, zap.NewNop(), context.Background())
	s.SetPrincipal(runtimePrincipal("ws-configured", "mem_configured", "org_configured"))

	own := s.principalFor("ws-configured")
	if own.MembershipID != "mem_configured" {
		t.Fatalf("the configured workspace lost its own principal: %+v", own)
	}
	other := s.principalFor("ws-other")
	if other.WorkspaceID != "ws-other" {
		t.Fatalf("a fire in ws-other would act in %q", other.WorkspaceID)
	}
	// A membership id is a grant. Carrying the configured tenant's across the
	// boundary would hand ws-other authority that belongs to somebody else.
	if other.MembershipID == "mem_configured" || other.OrganizationID == "org_configured" {
		t.Fatalf("authority crossed the tenant boundary: %+v", other)
	}
}

func runtimePrincipal(workspaceID, membershipID, organizationID string) runtime.Principal {
	return runtime.Principal{
		Subject: "scheduler", WorkspaceID: workspaceID, MembershipID: membershipID,
		OrganizationID: organizationID, Role: "admin", CredentialID: "service:scheduler", Kind: "service",
	}
}

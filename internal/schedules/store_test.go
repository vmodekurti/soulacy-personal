// store_test.go — MU-023: schedules are workspace-scoped durable records, an
// occurrence fires exactly once across instances, and catch-up is bounded.
package schedules

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/robfig/cron/v3"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "schedules.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func upsert(t *testing.T, store *Store, workspaceID, agentID string, mutate func(*Schedule)) Schedule {
	t.Helper()
	sched := Schedule{
		WorkspaceID: workspaceID, AgentID: agentID, Cron: "0 7 * * *",
		Timezone: "UTC", Enabled: true, CreatedBy: "usr_creator",
		AgentVersion: "v1", VersionPolicy: "pinned",
	}
	if mutate != nil {
		mutate(&sched)
	}
	stored, err := store.Upsert(context.Background(), sched)
	if err != nil {
		t.Fatalf("upsert %s/%s: %v", workspaceID, agentID, err)
	}
	return stored
}

// Criterion 1. Agent IDs are unique per workspace, not per deployment: the old
// in-memory map keyed by agent ID alone meant registering the second tenant's
// "daily-report" silently replaced the first's.
func TestTwoWorkspacesMayScheduleTheSameAgentID(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	a := upsert(t, store, "ws-a", "daily-report", func(s *Schedule) { s.Cron = "0 7 * * *" })
	b := upsert(t, store, "ws-b", "daily-report", func(s *Schedule) { s.Cron = "0 19 * * *" })

	if a.Cron == b.Cron {
		t.Fatal("one workspace's schedule overwrote the other's")
	}
	for _, want := range []Schedule{a, b} {
		got, err := store.GetByAgent(ctx, want.WorkspaceID, "daily-report")
		if err != nil {
			t.Fatal(err)
		}
		if got.Cron != want.Cron || got.WorkspaceID != want.WorkspaceID {
			t.Fatalf("%s got %+v", want.WorkspaceID, got)
		}
	}
	// And neither workspace lists the other's.
	for _, ws := range []string{"ws-a", "ws-b"} {
		list, err := store.ListEnabled(ctx, ws)
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != 1 || list[0].WorkspaceID != ws {
			t.Fatalf("%s lists %+v", ws, list)
		}
	}
}

// Criterion 1's other half: everything a fire needs to be reproducible later.
func TestAScheduleRecordsWhatAFireNeedsToBeExplainable(t *testing.T) {
	store := newStore(t)
	sched := upsert(t, store, "ws-a", "bot", func(s *Schedule) {
		s.Timezone = "America/New_York"
		s.MisfirePolicy = MisfireCatchUp
	})
	if sched.Timezone != "America/New_York" {
		t.Fatal("timezone was not stored; '07:00' without a zone is not a time")
	}
	if sched.CreatedBy == "" {
		t.Fatal("nobody owns this schedule")
	}
	if sched.AgentVersion == "" || sched.VersionPolicy != "pinned" {
		t.Fatalf("version policy lost: %+v", sched)
	}
	if sched.CatchUpLimit <= 0 {
		t.Fatal("catch-up is unbounded")
	}
}

// A zone the host cannot resolve must not silently become UTC: the schedule
// would fire at a different hour than the creator chose, with no error
// anywhere.
func TestAnUnknownTimezoneIsRefusedRatherThanCoercedToUTC(t *testing.T) {
	store := newStore(t)
	_, err := store.Upsert(context.Background(), Schedule{
		WorkspaceID: "ws-a", AgentID: "bot", Cron: "0 7 * * *", Timezone: "Mars/Olympus",
	})
	if err == nil || !strings.Contains(err.Error(), "timezone") {
		t.Fatalf("err = %v, want a timezone rejection", err)
	}
}

// Criterion 2. This is the property the whole package exists for.
func TestOnlyOneInstanceClaimsAnOccurrence(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	sched := upsert(t, store, "ws-a", "bot", nil)
	due := time.Date(2026, 8, 16, 7, 0, 0, 0, time.UTC)

	const instances = 8
	var wg sync.WaitGroup
	results := make([]error, instances)
	claims := make([]Occurrence, instances)
	start := make(chan struct{})
	for i := 0; i < instances; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			claims[i], results[i] = store.Claim(ctx, "ws-a", sched.ID, due, fmt.Sprintf("gw-%d", i), 0)
		}(i)
	}
	close(start)
	wg.Wait()

	winners := 0
	var winner Occurrence
	for i, err := range results {
		switch {
		case err == nil:
			winners++
			winner = claims[i]
		case errors.Is(err, ErrAlreadyClaimed):
		default:
			t.Fatalf("instance %d: unexpected error %v", i, err)
		}
	}
	if winners != 1 {
		t.Fatalf("%d instances fired the same occurrence, want 1", winners)
	}
	// Criterion 3: the key is deterministic, so every instance computed the
	// same one — which is why they collided rather than all winning.
	if winner.Key != OccurrenceKey(due) {
		t.Fatalf("key = %q", winner.Key)
	}
	if winner.IdempotencyKey != IdempotencyKey(sched.ID, due) {
		t.Fatalf("idempotency key = %q", winner.IdempotencyKey)
	}
}

// Two instances must derive the same key without talking to each other. Any
// key involving time.Now(), a counter or a UUID breaks this silently.
func TestTheOccurrenceKeyIsAgreedWithoutCoordination(t *testing.T) {
	due := time.Date(2026, 8, 16, 7, 0, 0, 0, time.UTC)
	// Same instant, different clock skew and different zones on each host.
	skewed := due.Add(400 * time.Millisecond).In(time.FixedZone("elsewhere", 5*3600))
	if OccurrenceKey(due) != OccurrenceKey(skewed) {
		t.Fatalf("sub-second skew or a different zone changes the key: %q vs %q",
			OccurrenceKey(due), OccurrenceKey(skewed))
	}
	// And a genuinely different occurrence is a different key.
	if OccurrenceKey(due) == OccurrenceKey(due.Add(time.Minute)) {
		t.Fatal("two occurrences share a key")
	}
}

// A completed occurrence must never fire again, however long ago it ran and
// however long the lease has been expired.
func TestACompletedOccurrenceIsNeverReclaimed(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	sched := upsert(t, store, "ws-a", "bot", nil)
	due := time.Date(2026, 8, 16, 7, 0, 0, 0, time.UTC)

	first, err := store.Claim(ctx, "ws-a", sched.ID, due, "gw-1", time.Nanosecond)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(ctx, first, nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond) // the lease is long expired

	if _, err := store.Claim(ctx, "ws-a", sched.ID, due, "gw-2", 0); !errors.Is(err, ErrAlreadyClaimed) {
		t.Fatalf("a completed occurrence was re-claimed: %v", err)
	}
}

// Criterion 6's shape: an instance that dies mid-run holds a claim nobody will
// complete. The lease is what lets the work resume — and it must be the
// EXPIRY that permits it, not merely a different claimant.
func TestAnExpiredLeaseIsTakenOverButALiveOneIsNot(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	sched := upsert(t, store, "ws-a", "bot", nil)
	due := time.Date(2026, 8, 16, 7, 0, 0, 0, time.UTC)

	// A live claim is not stealable, which is what stops a slow run being
	// executed twice.
	if _, err := store.Claim(ctx, "ws-a", sched.ID, due, "gw-1", time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Claim(ctx, "ws-a", sched.ID, due, "gw-2", 0); !errors.Is(err, ErrAlreadyClaimed) {
		t.Fatalf("a live claim was stolen: %v", err)
	}

	// An expired one is.
	other := time.Date(2026, 8, 17, 7, 0, 0, 0, time.UTC)
	dead, err := store.Claim(ctx, "ws-a", sched.ID, other, "gw-dead", time.Nanosecond)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	taken, err := store.Claim(ctx, "ws-a", sched.ID, other, "gw-live", time.Hour)
	if err != nil {
		t.Fatalf("an expired lease was not recoverable: %v", err)
	}
	if taken.ClaimedBy != "gw-live" {
		t.Fatalf("claimed_by = %q", taken.ClaimedBy)
	}
	// The superseded instance must not be able to record its outcome over the
	// new holder's: the run it is reporting on is the one that was replaced.
	if err := store.Complete(ctx, dead, nil); !errors.Is(err, ErrAlreadyClaimed) {
		t.Fatalf("a superseded instance recorded its outcome: %v", err)
	}
}

// One workspace's claim must not block or satisfy another's.
func TestClaimsAreScopedToTheirWorkspace(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	a := upsert(t, store, "ws-a", "bot", nil)
	b := upsert(t, store, "ws-b", "bot", nil)
	due := time.Date(2026, 8, 16, 7, 0, 0, 0, time.UTC)

	if _, err := store.Claim(ctx, "ws-a", a.ID, due, "gw-1", time.Hour); err != nil {
		t.Fatal(err)
	}
	// Same agent ID, same instant, different workspace: a separate occurrence.
	if _, err := store.Claim(ctx, "ws-b", b.ID, due, "gw-1", time.Hour); err != nil {
		t.Fatalf("one workspace's claim blocked another's: %v", err)
	}
	// And the record stays on its own side of the boundary.
	occurrences, err := store.Occurrences(ctx, "ws-a", a.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(occurrences) != 1 || occurrences[0].WorkspaceID != "ws-a" {
		t.Fatalf("ws-a occurrences = %+v", occurrences)
	}
}

// Criterion 5. "It stopped running and nobody knows why" is the failure this
// field exists to prevent.
func TestDisablingRecordsAnAuditableReason(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	upsert(t, store, "ws-a", "bot", nil)

	if err := store.Disable(ctx, "ws-a", "bot", "workspace budget exhausted"); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetByAgent(ctx, "ws-a", "bot")
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled {
		t.Fatal("a disabled schedule is still enabled")
	}
	if !strings.Contains(got.DisabledReason, "budget") {
		t.Fatalf("reason = %q", got.DisabledReason)
	}
	list, err := store.ListEnabled(ctx, "ws-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatal("a disabled schedule is still listed as active")
	}
	// Another workspace's schedule cannot be disabled by naming its agent.
	upsert(t, store, "ws-b", "bot", nil)
	if err := store.Disable(ctx, "ws-attacker", "bot", "malice"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-workspace disable: %v", err)
	}
	other, err := store.GetByAgent(ctx, "ws-b", "bot")
	if err != nil {
		t.Fatal(err)
	}
	if !other.Enabled {
		t.Fatal("another workspace's schedule was disabled")
	}
}

// The deployment-wide sweep must decide per schedule, against that schedule's
// own workspace — never inherit one workspace for all of them, which is what
// a single scheduler.principal made structural.
func TestTheDeploymentWideListCarriesEachSchedulesOwnWorkspace(t *testing.T) {
	store := newStore(t)
	upsert(t, store, "ws-a", "bot", nil)
	upsert(t, store, "ws-b", "bot", nil)
	upsert(t, store, "ws-c", "other", func(s *Schedule) { s.Enabled = false })

	all, err := store.ListAllEnabledAcrossWorkspaces(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("got %d schedules, want the 2 enabled ones", len(all))
	}
	seen := map[string]bool{}
	for _, sched := range all {
		if sched.WorkspaceID == "" {
			t.Fatal("a schedule came back with no workspace to fire in")
		}
		seen[sched.WorkspaceID] = true
	}
	if !seen["ws-a"] || !seen["ws-b"] || seen["ws-c"] {
		t.Fatalf("workspaces = %v", seen)
	}
}

// Criterion 4.
func TestCatchUpIsBoundedAndSaysWhatItDropped(t *testing.T) {
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	since := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC) // a week of downtime

	t.Run("skip is the default and replays nothing", func(t *testing.T) {
		sched := Schedule{Cron: "*/5 * * * *", Timezone: "UTC", MisfirePolicy: MisfireSkip, CatchUpLimit: 3}
		missed, err := sched.MissedSince(parser, since, now)
		if err != nil {
			t.Fatal(err)
		}
		if len(missed.Occurrences) != 0 {
			t.Fatalf("skip replayed %d occurrences", len(missed.Occurrences))
		}
	})

	t.Run("catch-up is capped", func(t *testing.T) {
		sched := Schedule{Cron: "*/5 * * * *", Timezone: "UTC", MisfirePolicy: MisfireCatchUp, CatchUpLimit: 3}
		missed, err := sched.MissedSince(parser, since, now)
		if err != nil {
			t.Fatal(err)
		}
		if len(missed.Occurrences) != 3 {
			t.Fatalf("replayed %d, want the cap of 3", len(missed.Occurrences))
		}
		// A silent cap reads as "we caught up" when it means "we caught up a bit".
		if missed.Dropped == 0 {
			t.Fatal("a week of missed 5-minute occurrences reported nothing dropped")
		}
		// The NEWEST are kept: replaying the oldest and discarding the current
		// is the opposite of what catch-up is for.
		last := missed.Occurrences[len(missed.Occurrences)-1]
		if now.Sub(last) > 10*time.Minute {
			t.Fatalf("the newest replayed occurrence is %v old; the stale end was kept", now.Sub(last))
		}
		// Oldest-first within the window, because catch-up replays a sequence.
		for i := 1; i < len(missed.Occurrences); i++ {
			if !missed.Occurrences[i-1].Before(missed.Occurrences[i]) {
				t.Fatal("catch-up occurrences are not in chronological order")
			}
		}
	})

	t.Run("a schedule with few misses replays them all", func(t *testing.T) {
		sched := Schedule{Cron: "0 7 * * *", Timezone: "UTC", MisfirePolicy: MisfireCatchUp, CatchUpLimit: 10}
		missed, err := sched.MissedSince(parser, since, since.Add(50*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if len(missed.Occurrences) != 2 || missed.Dropped != 0 {
			t.Fatalf("got %d occurrences, %d dropped", len(missed.Occurrences), missed.Dropped)
		}
	})
}

// The timezone is not decoration: the same expression fires at a different
// instant depending on it, and DST is where that becomes visible.
func TestTheStoredTimezoneDecidesWhenAScheduleIsDue(t *testing.T) {
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	after := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)

	utc := Schedule{Cron: "0 7 * * *", Timezone: "UTC"}
	newYork := Schedule{Cron: "0 7 * * *", Timezone: "America/New_York"}
	utcNext, err := utc.NextFire(parser, after)
	if err != nil {
		t.Fatal(err)
	}
	nyNext, err := newYork.NextFire(parser, after)
	if err != nil {
		t.Fatal(err)
	}
	if utcNext.Equal(nyNext) {
		t.Fatal("the timezone had no effect; '07:00' is being read as UTC everywhere")
	}
	if nyNext.Sub(utcNext) != 5*time.Hour {
		t.Fatalf("New York 07:00 in January should be 12:00 UTC: got %s vs %s", nyNext, utcNext)
	}
}

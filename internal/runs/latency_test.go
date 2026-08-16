// latency_test.go — MU-027 criterion 6: provider/tool time is visible
// separately from Soulacy's own queue and processing time.
package runs

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func timedRun(created time.Time, queue, execution time.Duration, externalMicros int64) Run {
	started := created.Add(queue)
	ended := started.Add(execution)
	return Run{
		CreatedAt: created, StartedAt: &started, EndedAt: &ended,
		ExternalMicros: externalMicros,
	}
}

// Without the decomposition a slow run is one number and every explanation
// fits it — switch model, add workers, profile the engine — which have
// opposite remediations.
func TestTheBreakdownSeparatesWaitingFromWorking(t *testing.T) {
	run := timedRun(time.Now(), 3*time.Second, 40*time.Second, (39 * time.Second).Microseconds())

	if got := run.QueueLatency(); got != 3*time.Second {
		t.Fatalf("queue = %v, want 3s", got)
	}
	if got := run.ExecutionLatency(); got != 40*time.Second {
		t.Fatalf("execution = %v, want 40s", got)
	}
	// The interesting number: 40 seconds of run, 39 of which was somebody
	// else's system.
	if got := run.ProcessingLatency(); got != time.Second {
		t.Fatalf("processing = %v, want 1s — provider time was not excluded", got)
	}
	if got := run.TotalLatency(); got != 43*time.Second {
		t.Fatalf("total = %v, want 43s", got)
	}
}

// External time accumulates from concurrent calls, so parallel tool execution
// can legitimately sum to more than the wall clock. A negative "processing
// time" is arithmetically explicable and operationally meaningless.
func TestConcurrentExternalTimeDoesNotProduceNegativeProcessing(t *testing.T) {
	run := timedRun(time.Now(), time.Second, 10*time.Second, (25 * time.Second).Microseconds())
	if got := run.ProcessingLatency(); got != 0 {
		t.Fatalf("processing = %v, want 0 — concurrency produced a negative duration", got)
	}
	if got := run.Latency().ProcessingMS; got != 0 {
		t.Fatalf("breakdown processing = %d ms", got)
	}
}

// A run that has not started or not finished reports zero rather than a
// duration measured against a zero timestamp — which would be decades.
func TestAnUnfinishedRunReportsZeroRatherThanNonsense(t *testing.T) {
	queued := Run{CreatedAt: time.Now()}
	if queued.QueueLatency() != 0 || queued.ExecutionLatency() != 0 || queued.TotalLatency() != 0 {
		t.Fatalf("a queued run reported latency: %+v", queued.Latency())
	}
	started := time.Now()
	running := Run{CreatedAt: started.Add(-time.Second), StartedAt: &started}
	if running.QueueLatency() != time.Second {
		t.Fatalf("queue = %v", running.QueueLatency())
	}
	if running.ExecutionLatency() != 0 || running.TotalLatency() != 0 {
		t.Fatal("a running run reported an execution or total time")
	}
}

// Provider and tool calls happen concurrently from different goroutines.
// Read-modify-write would lose increments under exactly the concurrency this
// is measuring.
func TestExternalTimeAccumulatesUnderConcurrency(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	submit(t, store, "run_1", "ws_a", "bot")

	const calls = 40
	done := make(chan struct{})
	for i := 0; i < calls; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			if err := store.RecordExternal(ctx, "ws_a", "run_1", 10*time.Millisecond); err != nil {
				t.Error(err)
			}
		}()
	}
	for i := 0; i < calls; i++ {
		<-done
	}

	stored, err := store.Get(ctx, "ws_a", "run_1")
	if err != nil {
		t.Fatal(err)
	}
	want := (calls * 10 * time.Millisecond).Microseconds()
	if stored.ExternalMicros != want {
		t.Fatalf("external = %d micros, want %d — increments were lost", stored.ExternalMicros, want)
	}
}

// One run's external time must not be charged to another workspace's run of
// the same id.
func TestExternalTimeIsScopedToItsWorkspace(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	submit(t, store, "shared", "ws_a", "bot")
	submit(t, store, "shared", "ws_b", "bot")

	if err := store.RecordExternal(ctx, "ws_a", "shared", time.Second); err != nil {
		t.Fatal(err)
	}
	other, err := store.Get(ctx, "ws_b", "shared")
	if err != nil {
		t.Fatal(err)
	}
	if other.ExternalMicros != 0 {
		t.Fatalf("ws_b was charged %d micros of another workspace's provider time", other.ExternalMicros)
	}
}

// Microseconds, because a fast tool call rounds to zero milliseconds and a run
// makes many of them — a run of a hundred 400µs calls would report zero.
func TestSubMillisecondCallsAreNotLost(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	submit(t, store, "run_1", "ws_a", "bot")
	for i := 0; i < 100; i++ {
		if err := store.RecordExternal(ctx, "ws_a", "run_1", 400*time.Microsecond); err != nil {
			t.Fatal(err)
		}
	}
	stored, err := store.Get(ctx, "ws_a", "run_1")
	if err != nil {
		t.Fatal(err)
	}
	if stored.ExternalMicros != 40_000 {
		t.Fatalf("external = %d micros, want 40000", stored.ExternalMicros)
	}
}

// The ownership catalog has declared agent_runs CompositeUniqueness since
// MU-020 and the schema did not implement it: a bare id primary key made run
// IDs globally unique, so submitting an ID another workspace already used
// returned a constraint violation — a weak enumeration oracle over other
// tenants' run IDs.
func TestTwoWorkspacesMayHoldRunsWithTheSameID(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	submit(t, store, "shared", "ws_a", "bot")
	submit(t, store, "shared", "ws_b", "bot")

	for _, ws := range []string{"ws_a", "ws_b"} {
		got, err := store.Get(ctx, ws, "shared")
		if err != nil {
			t.Fatalf("%s: %v", ws, err)
		}
		if got.WorkspaceID != ws {
			t.Fatalf("%s got %s's run", ws, got.WorkspaceID)
		}
	}
	// And a second submission within ONE workspace is still refused.
	if _, _, err := store.Submit(ctx, Run{ID: "shared", WorkspaceID: "ws_a", AgentID: "bot"}); err == nil {
		t.Fatal("a duplicate run id within one workspace was accepted")
	}
}

// A database created before the composite key must migrate rather than fail
// to open.
func TestAPreCompositeDatabaseMigrates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.db")
	legacy, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	legacySchema := strings.Replace(schema,
		"    id               TEXT NOT NULL,", "    id               TEXT PRIMARY KEY,", 1)
	// The old table had exactly one primary key; strip the composite clause
	// the current schema adds, or SQLite refuses two.
	clause := "    PRIMARY KEY (workspace_id, id)\n"
	if from := strings.Index(legacySchema, "    -- Composite, not a bare id"); from >= 0 {
		to := strings.Index(legacySchema, clause) + len(clause)
		legacySchema = legacySchema[:from] + legacySchema[to:]
		legacySchema = strings.Replace(legacySchema,
			"    side_effect_tool TEXT NOT NULL DEFAULT '',", "    side_effect_tool TEXT NOT NULL DEFAULT ''", 1)
	}
	if _, err := legacy.Exec(legacySchema); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(
		`INSERT INTO agent_runs (id, workspace_id, agent_id, status, created_at, updated_at, max_attempts)
         VALUES ('legacy_run','ws_a','bot','queued',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,3)`); err != nil {
		t.Fatal(err)
	}
	_ = legacy.Close()

	store, err := Open(path)
	if err != nil {
		t.Fatalf("a pre-composite database failed to open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// The existing row survived the rebuild — a migration that loses runs is
	// worse than the schema it fixes.
	if _, err := store.Get(context.Background(), "ws_a", "legacy_run"); err != nil {
		t.Fatalf("the migration lost an existing run: %v", err)
	}
	// And the new constraint is in force.
	submit(t, store, "legacy_run", "ws_b", "bot")
}

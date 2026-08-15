// isolation_test.go — the cross-tenant isolation contract for the dead-letter
// queue. internal/ownership/catalog.go names this file as the isolation
// evidence for the dead_letters table.
//
// The dead-letter queue is a single deployment-wide database, and a dead letter
// carries the original job payload — for an agent run, the user's own prompt.
// So the workspace column is the only thing standing between one team's parked
// jobs and another's, and these cases assert that boundary rather than
// incidental behaviour.
package dlq

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/sqlitex"
	"github.com/soulacy/soulacy/internal/wsroot"
)

func seedDeadLetter(t *testing.T, s *SQLiteStore, id, workspaceID, queue string) {
	t.Helper()
	if err := s.Push(context.Background(), DeadLetter{
		ID: id, WorkspaceID: workspaceID, Queue: queue,
		Payload:  []byte(`{"content":"secret prompt for ` + id + `"}`),
		ErrorMsg: "provider refused", Attempts: 3,
		CreatedAt: time.Now().UTC(), LastAttemptAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

// A listing is the workspace's own backlog and nothing else — with and without
// a queue filter, since the filtered path is a separate statement.
func TestDeadLetterListingIsScopedToTheFailingWorkspace(t *testing.T) {
	s := newDLQStore(t)
	seedDeadLetter(t, s, "a1", "ws_a", "agent-one")
	seedDeadLetter(t, s, "a2", "ws_a", "agent-two")
	seedDeadLetter(t, s, "b1", "ws_b", "agent-one")

	for _, tc := range []struct {
		workspace, queue string
		want             []string
	}{
		{"ws_a", "", []string{"a1", "a2"}},
		{"ws_a", "agent-one", []string{"a1"}},
		{"ws_b", "", []string{"b1"}},
		{"ws_b", "agent-two", nil},
		{"ws_c", "", nil},
	} {
		entries, err := s.List(context.Background(), tc.workspace, tc.queue)
		if err != nil {
			t.Fatalf("list %s/%s: %v", tc.workspace, tc.queue, err)
		}
		seen := map[string]bool{}
		for _, entry := range entries {
			seen[entry.ID] = true
		}
		if len(entries) != len(tc.want) {
			t.Errorf("list %s/%q returned %d entries, want %d: %v", tc.workspace, tc.queue, len(entries), len(tc.want), seen)
		}
		for _, id := range tc.want {
			if !seen[id] {
				t.Errorf("list %s/%q missing own entry %s", tc.workspace, tc.queue, id)
			}
		}
	}
}

// Reads and writes by ID carry the tenant in the predicate. A neighbour's ID is
// reported exactly as a nonexistent one, and a delete that misses must leave
// the row intact — a refusal that still destroyed the evidence would be worse
// than the leak it was preventing.
func TestDeadLetterByIDCannotCrossTheWorkspaceBoundary(t *testing.T) {
	s := newDLQStore(t)
	ctx := context.Background()
	seedDeadLetter(t, s, "a1", "ws_a", "agent-one")

	if _, err := s.Get(ctx, "ws_b", "a1"); err != ErrNotFound {
		t.Errorf("Get across workspaces returned %v, want ErrNotFound", err)
	}
	if _, err := s.Get(ctx, "ws_b", "no-such-id"); err != ErrNotFound {
		t.Errorf("Get of a missing id returned %v, want ErrNotFound", err)
	}
	if err := s.Delete(ctx, "ws_b", "a1"); err != ErrNotFound {
		t.Errorf("Delete across workspaces returned %v, want ErrNotFound", err)
	}
	got, err := s.Get(ctx, "ws_a", "a1")
	if err != nil {
		t.Fatalf("the owner lost its own dead letter to a neighbour's delete: %v", err)
	}
	if string(got.Payload) == "" {
		t.Fatal("payload was dropped")
	}
	if err := s.Delete(ctx, "ws_a", "a1"); err != nil {
		t.Fatalf("the owner could not delete its own entry: %v", err)
	}
}

// A push that could not name a tenant is recorded as personal rather than
// refused. Push runs after the job has already exhausted its retries, so
// rejecting the insert would turn "we could not attribute this" into "this
// never happened" — and personal is nobody else's workspace, so the entry is
// visible to the operator without being visible to a stranger.
func TestAnUnattributedPushLandsInPersonalRatherThanNowhere(t *testing.T) {
	s := newDLQStore(t)
	ctx := context.Background()
	seedDeadLetter(t, s, "orphan", "", "agent-one")

	personal, err := s.List(ctx, wsroot.PersonalWorkspaceID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(personal) != 1 || personal[0].ID != "orphan" {
		t.Fatalf("an unattributed dead letter did not land in personal: %+v", personal)
	}
	if personal[0].WorkspaceID != wsroot.PersonalWorkspaceID {
		t.Errorf("stored workspace = %q, want %q", personal[0].WorkspaceID, wsroot.PersonalWorkspaceID)
	}
	other, err := s.List(ctx, "ws_a", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != 0 {
		t.Fatalf("an unattributed dead letter was visible to a tenant: %+v", other)
	}
}

// A row left with an empty workspace by an interrupted upgrade matches no
// scoped read at all, so it does not leak — it disappears, and the operator
// sees an empty backlog and concludes nothing failed. The backfill runs on
// every open, not only inside the versioned migration, so reopening repairs it.
func TestPreTenantDeadLettersAreRecoveredOnOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dlq.db")
	first, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	seedDeadLetter(t, first, "legacy", "ws_a", "agent-one")
	// Simulate the migration having added the column but died before the
	// backfill: the row is present and unattributed.
	if _, err := first.db.Exec(`UPDATE dead_letters SET workspace_id = '' WHERE id = 'legacy'`); err != nil {
		t.Fatal(err)
	}
	stranded, err := first.List(context.Background(), wsroot.PersonalWorkspaceID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(stranded) != 0 {
		t.Fatal("test setup did not actually strand the row")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	recovered, err := second.List(context.Background(), wsroot.PersonalWorkspaceID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 1 || recovered[0].ID != "legacy" {
		t.Fatalf("a stranded dead letter was not recovered on open: %+v", recovered)
	}
}

// The schema step is additive. A dead-letter database written by an older
// binary must open, keep its rows, and gain the column — the version table is
// what stops the ALTER from running twice.
func TestTheTenantColumnIsAnAdditiveMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dlq.db")
	s, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	seedDeadLetter(t, s, "kept", "ws_a", "agent-one")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("reopening a migrated database failed: %v", err)
	}
	defer reopened.Close()
	version, err := sqlitex.SchemaVersion(reopened.db, "dlq")
	if err != nil {
		t.Fatal(err)
	}
	if version != 2 {
		t.Fatalf("schema version = %d, want 2", version)
	}
	entries, err := reopened.List(context.Background(), "ws_a", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].ID != "kept" {
		t.Fatalf("migration lost rows: %+v", entries)
	}
}

// Every read is scoped, but the primary key is the ID alone, so the write path
// needs its own guard: an INSERT OR REPLACE whose ID matched a neighbour's row
// would overwrite that row through a store whose reads can never see it. The
// conflict clause is conditional on the workspace, so the collision updates
// nothing and says so.
func TestAPushCannotOverwriteAnotherWorkspacesDeadLetter(t *testing.T) {
	s := newDLQStore(t)
	ctx := context.Background()
	seedDeadLetter(t, s, "shared-id", "ws_a", "agent-one")

	err := s.Push(ctx, DeadLetter{
		ID: "shared-id", WorkspaceID: "ws_b", Queue: "agent-evil",
		Payload: []byte(`{"content":"overwritten"}`), ErrorMsg: "planted", Attempts: 1,
	})
	if err == nil {
		t.Fatal("a cross-workspace push was accepted silently")
	}
	original, getErr := s.Get(ctx, "ws_a", "shared-id")
	if getErr != nil {
		t.Fatalf("the owner lost its dead letter to a colliding push: %v", getErr)
	}
	if original.Queue != "agent-one" || original.ErrorMsg != "provider refused" {
		t.Fatalf("the row was overwritten across the tenant boundary: %+v", original)
	}
	if _, err := s.Get(ctx, "ws_b", "shared-id"); err != ErrNotFound {
		t.Errorf("the colliding push became visible to the pusher: %v", err)
	}

	// A same-workspace re-push keeps its overwrite semantics: the engine
	// re-pushes an entry as attempts accumulate.
	if err := s.Push(ctx, DeadLetter{
		ID: "shared-id", WorkspaceID: "ws_a", Queue: "agent-one",
		Payload: []byte(`{"content":"retry"}`), ErrorMsg: "provider refused again", Attempts: 4,
	}); err != nil {
		t.Fatalf("a same-workspace re-push was rejected: %v", err)
	}
	updated, err := s.Get(ctx, "ws_a", "shared-id")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Attempts != 4 || updated.ErrorMsg != "provider refused again" {
		t.Fatalf("the owner's re-push did not update the row: %+v", updated)
	}
}

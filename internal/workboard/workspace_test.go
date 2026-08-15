// workspace_test.go — the cross-tenant isolation contract for the workboard:
// tasks, runs, comments, and artifacts.
//
// Every ID in this package is a global SQLite autoincrement, so another
// tenant's task, run, comment, or artifact ID is always a plausible one. That
// makes the workboard the clearest case in the codebase for "a missing row and
// someone else's row must be the same answer".
package workboard

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/soulacy/soulacy/internal/wsroot"
)

func newWorkspaceStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "workboard.db")
	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, path
}

func makeTask(t *testing.T, s *Store, workspaceID, title string) Task {
	t.Helper()
	task, err := s.Create(context.Background(), Task{
		WorkspaceID: workspaceID, Title: title, AgentID: "assistant",
	})
	if err != nil {
		t.Fatalf("create %s/%s: %v", workspaceID, title, err)
	}
	return task
}

// A task with no owner is a task every tenant can read and edit, so an absent
// workspace is refused rather than defaulted.
func TestOperationsWithoutAWorkspaceAreRefused(t *testing.T) {
	store, _ := newWorkspaceStore(t)
	ctx := context.Background()
	if _, err := store.Create(ctx, Task{Title: "orphan"}); !errors.Is(err, ErrWorkspaceRequired) {
		t.Errorf("create without a workspace = %v", err)
	}
	if _, err := store.List(ctx, "", Filter{}); !errors.Is(err, ErrWorkspaceRequired) {
		t.Errorf("list without a workspace = %v", err)
	}
	if _, err := store.Get(ctx, "", 1); !errors.Is(err, ErrWorkspaceRequired) {
		t.Errorf("get without a workspace = %v", err)
	}
	if err := store.Delete(ctx, "", 1); !errors.Is(err, ErrWorkspaceRequired) {
		t.Errorf("delete without a workspace = %v", err)
	}
	if _, err := store.GetRun(ctx, "", 1); !errors.Is(err, ErrWorkspaceRequired) {
		t.Errorf("get run without a workspace = %v", err)
	}
	if _, err := store.GetArtifact(ctx, "", 1); !errors.Is(err, ErrWorkspaceRequired) {
		t.Errorf("get artifact without a workspace = %v", err)
	}
	if err := store.DeleteComment(ctx, "", 1); !errors.Is(err, ErrWorkspaceRequired) {
		t.Errorf("delete comment without a workspace = %v", err)
	}
}

// A task another workspace owns must be indistinguishable from one that never
// existed, and must not be reachable for edit, delete, or run.
func TestATaskIsUnreachableFromAnotherWorkspace(t *testing.T) {
	store, _ := newWorkspaceStore(t)
	ctx := context.Background()
	owned := makeTask(t, store, "ws_a", "reconcile the alpha ledger")

	if _, err := store.Get(ctx, "ws_b", owned.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("another workspace read the task: %v", err)
	}
	done := StatusDone
	if _, err := store.Update(ctx, "ws_b", owned.ID, Update{Status: &done}); !errors.Is(err, ErrNotFound) {
		t.Errorf("another workspace updated the task: %v", err)
	}
	if _, err := store.StartRun(ctx, "ws_b", owned.ID, "assistant", "sess", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("another workspace started a run on the task: %v", err)
	}
	if _, err := store.AddComment(ctx, "ws_b", owned.ID, Comment{Body: "not yours"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("another workspace commented on the task: %v", err)
	}
	if err := store.Delete(ctx, "ws_b", owned.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("another workspace deleted the task: %v", err)
	}
	// A nonexistent ID gives exactly the same answer, so IDs cannot be probed.
	if _, err := store.Get(ctx, "ws_b", 999999); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown ID gave a different answer: %v", err)
	}
	// And after all of that the owner's task is untouched.
	still, err := store.Get(ctx, "ws_a", owned.ID)
	if err != nil {
		t.Fatalf("the owner lost its task: %v", err)
	}
	if still.Status != StatusTodo {
		t.Fatalf("the task was modified from another workspace: %+v", still)
	}
}

// A listing carries the tenant predicate in the query, not applied to results.
func TestListingIsScopedToOneWorkspace(t *testing.T) {
	store, _ := newWorkspaceStore(t)
	makeTask(t, store, "ws_a", "reconcile the alpha ledger")
	makeTask(t, store, "ws_a", "reconcile it again")
	makeTask(t, store, "ws_b", "publish the beta changelog")

	listed, err := store.List(context.Background(), "ws_a", Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 {
		t.Fatalf("ws_a listed %d tasks, want its own 2: %+v", len(listed), listed)
	}
	for _, task := range listed {
		if task.WorkspaceID != "ws_a" {
			t.Errorf("a listing crossed the boundary: %+v", task)
		}
	}
	// The agent filter is the one a caller supplies, and it must not widen the
	// tenant predicate: both workspaces use the same agent ID here.
	filtered, err := store.List(context.Background(), "ws_b", Filter{AgentID: "assistant"})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 1 {
		t.Fatalf("ws_b listed %d tasks, want its own 1: %+v", len(filtered), filtered)
	}
}

// Runs, comments, and artifacts carry no workspace of their own — they derive
// it from the task. That is the point: the two can never disagree.
func TestChildRecordsDeriveOwnershipFromTheirTask(t *testing.T) {
	store, _ := newWorkspaceStore(t)
	ctx := context.Background()
	owned := makeTask(t, store, "ws_a", "reconcile the alpha ledger")

	run, err := store.StartRun(ctx, "ws_a", owned.ID, "assistant", "sess-a", "/var/log/assistant.log")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddArtifacts(ctx, "ws_a", owned.ID, run.ID, []Artifact{{Path: "/tmp/alpha-report.csv", Tool: "write_file"}}); err != nil {
		t.Fatal(err)
	}
	comment, err := store.AddComment(ctx, "ws_a", owned.ID, Comment{Body: "looks right"})
	if err != nil {
		t.Fatal(err)
	}

	// A run record names the agent, the session, and the action-log path.
	if _, err := store.GetRun(ctx, "ws_b", run.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("another workspace read the run: %v", err)
	}
	if got, err := store.ListRuns(ctx, "ws_b", owned.ID); err != nil || len(got) != 0 {
		t.Errorf("another workspace listed the runs: %+v %v", got, err)
	}
	if _, err := store.FinishRun(ctx, "ws_b", run.ID, RunStatusFailed, "", "sabotage"); !errors.Is(err, ErrNotFound) {
		t.Errorf("another workspace finished the run: %v", err)
	}

	// An artifact row is an on-disk path the gateway will stream.
	if _, err := store.GetArtifact(ctx, "ws_b", 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("another workspace read the artifact: %v", err)
	}
	if got, err := store.ListArtifacts(ctx, "ws_b", owned.ID); err != nil || len(got) != 0 {
		t.Errorf("another workspace listed the artifacts: %+v %v", got, err)
	}
	if got, err := store.ListRunArtifacts(ctx, "ws_b", run.ID); err != nil || len(got) != 0 {
		t.Errorf("another workspace listed the run's artifacts: %+v %v", got, err)
	}
	if err := store.AddArtifacts(ctx, "ws_b", owned.ID, run.ID, []Artifact{{Path: "/tmp/injected"}}); !errors.Is(err, ErrNotFound) {
		t.Errorf("another workspace attached an artifact: %v", err)
	}

	if got, err := store.ListComments(ctx, "ws_b", owned.ID); err != nil || len(got) != 0 {
		t.Errorf("another workspace listed the comments: %+v %v", got, err)
	}
	if err := store.DeleteComment(ctx, "ws_b", comment.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("another workspace deleted the comment: %v", err)
	}

	// The owner still sees everything it created.
	if got, err := store.ListRuns(ctx, "ws_a", owned.ID); err != nil || len(got) != 1 {
		t.Fatalf("the owner lost its run: %+v %v", got, err)
	}
	if got, err := store.ListArtifacts(ctx, "ws_a", owned.ID); err != nil || len(got) != 1 {
		t.Fatalf("the owner lost its artifact: %+v %v", got, err)
	}
	if got, err := store.ListComments(ctx, "ws_a", owned.ID); err != nil || len(got) != 1 {
		t.Fatalf("the owner lost its comment: %+v %v", got, err)
	}
}

// The run-active guard is per task, so one tenant's in-flight run must not
// block another tenant from starting one — and must not be visible as a
// conflict either.
func TestARunInOneWorkspaceDoesNotBlockAnother(t *testing.T) {
	store, _ := newWorkspaceStore(t)
	ctx := context.Background()
	a := makeTask(t, store, "ws_a", "shared title")
	b := makeTask(t, store, "ws_b", "shared title")

	if _, err := store.StartRun(ctx, "ws_a", a.ID, "assistant", "sess-a", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StartRun(ctx, "ws_b", b.ID, "assistant", "sess-b", ""); err != nil {
		t.Fatalf("another workspace's active run blocked this one: %v", err)
	}
}

// A delete must not reach across the boundary, and the owner's delete must
// still take its children with it.
func TestDeleteIsScopedAndStillCascades(t *testing.T) {
	store, _ := newWorkspaceStore(t)
	ctx := context.Background()
	owned := makeTask(t, store, "ws_a", "reconcile the alpha ledger")
	run, err := store.StartRun(ctx, "ws_a", owned.ID, "assistant", "sess-a", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddArtifacts(ctx, "ws_a", owned.ID, run.ID, []Artifact{{Path: "/tmp/report.csv"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddComment(ctx, "ws_a", owned.ID, Comment{Body: "note"}); err != nil {
		t.Fatal(err)
	}

	if err := store.Delete(ctx, "ws_b", owned.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another workspace's delete was accepted: %v", err)
	}
	// The refused delete must not have removed the children either — the
	// parent row is deleted first precisely so the cascade is never reached.
	if got, err := store.ListRuns(ctx, "ws_a", owned.ID); err != nil || len(got) != 1 {
		t.Fatalf("another workspace's delete removed the owner's runs: %+v %v", got, err)
	}

	if err := store.Delete(ctx, "ws_a", owned.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, "ws_a", owned.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the owner's delete did not take effect: %v", err)
	}
	if got, err := store.ListRuns(ctx, "ws_a", owned.ID); err != nil || len(got) != 0 {
		t.Fatalf("the owner's delete left orphaned runs: %+v %v", got, err)
	}
}

// A workboard written before tenants existed keeps working: the column is
// added in place and its tasks are assigned to the personal workspace, which
// is what a single-user installation's board was.
func TestExistingBoardMigratesToPersonal(t *testing.T) {
	store, path := newWorkspaceStore(t)
	ctx := context.Background()
	makeTask(t, store, wsroot.PersonalWorkspaceID, "written before tenants")
	// Simulate rows from before the column existed.
	if _, err := store.db.Exec(`UPDATE workboard_tasks SET workspace_id = ''`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()

	listed, err := reopened.List(ctx, wsroot.PersonalWorkspaceID, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Title != "written before tenants" {
		t.Fatalf("a pre-existing task was lost by the migration: %+v", listed)
	}
}

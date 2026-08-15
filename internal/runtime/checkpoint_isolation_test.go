// checkpoint_isolation_test.go — the cross-tenant isolation contract for
// workflow checkpoints. internal/ownership/catalog.go names this file as the
// isolation evidence for the workflow_checkpoints table.
//
// Checkpoints are unusual among the tenant stores: their contents are not only
// read back, they are *replayed*. The Restore hook feeds a completed
// checkpoint's State into a resuming run as that step's own output. So a
// cross-tenant match here is not a disclosure bug, it is an injection bug —
// another workspace's data becomes this workspace's computation.
package runtime

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/sqlitex"
	"github.com/soulacy/soulacy/internal/wsroot"
	"go.uber.org/zap"
)

func workspaceCtx(workspaceID string) context.Context {
	return WithPrincipal(context.Background(), Principal{
		Subject: "usr_" + workspaceID, WorkspaceID: workspaceID,
		OrganizationID: "org_a", Role: "developer", Kind: "user",
	})
}

func seedCheckpoint(t *testing.T, s *CheckpointStore, workspaceID, agentID, runID, stepID, status, state string) {
	t.Helper()
	var raw json.RawMessage
	if state != "" {
		raw = json.RawMessage(state)
	}
	if err := s.Upsert(context.Background(), Checkpoint{
		WorkspaceID: workspaceID, AgentID: agentID, RunID: runID, StepID: stepID,
		Status: status, State: raw, UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed %s/%s/%s/%s: %v", workspaceID, agentID, runID, stepID, err)
	}
}

// Agent IDs are unique within a workspace, not across the deployment: two
// tenants can each run an agent called "researcher". Under the v1 key the
// second tenant's write did not create a row, it took over the first's.
func TestTheSameAgentRunAndStepInTwoWorkspacesAreTwoRows(t *testing.T) {
	s := newTestCheckpointStore(t)
	ctx := context.Background()
	seedCheckpoint(t, s, "ws_a", "researcher", "run-1", "step-1", CheckpointCompleted, `{"answer":"alpha"}`)
	seedCheckpoint(t, s, "ws_b", "researcher", "run-1", "step-1", CheckpointCompleted, `{"answer":"beta"}`)

	mine, err := s.Get(ctx, "ws_a", "researcher", "run-1", "step-1")
	if err != nil {
		t.Fatalf("the first tenant's checkpoint was taken over by the second: %v", err)
	}
	if string(mine.State) != `{"answer":"alpha"}` {
		t.Fatalf("state crossed the tenant boundary: %s", mine.State)
	}
	theirs, err := s.Get(ctx, "ws_b", "researcher", "run-1", "step-1")
	if err != nil {
		t.Fatal(err)
	}
	if string(theirs.State) != `{"answer":"beta"}` {
		t.Fatalf("second tenant's state = %s", theirs.State)
	}
	if mine.WorkspaceID != "ws_a" || theirs.WorkspaceID != "ws_b" {
		t.Fatalf("rows do not carry their own tenant: %q / %q", mine.WorkspaceID, theirs.WorkspaceID)
	}
}

// The restore path: a workspace that never wrote this checkpoint must miss,
// because a hit would be replayed into its run as a real step output.
func TestARestoreCannotReplayAnotherWorkspacesStepOutput(t *testing.T) {
	s := newTestCheckpointStore(t)
	seedCheckpoint(t, s, "ws_a", "researcher", "run-1", "step-1", CheckpointCompleted, `{"answer":"alpha"}`)

	if _, err := s.Get(context.Background(), "ws_b", "researcher", "run-1", "step-1"); err == nil {
		t.Fatal("a neighbour's completed step was restorable")
	}
}

// Resuming is per tenant. The deployment-wide sweep still exists — a
// process-restart recovery has no request and therefore no tenant — but it is
// named so it cannot be reached by accident, and every row it returns carries
// the workspace the resumer has to act in.
func TestInProgressListingIsScopedAndTheDeploymentSweepIsExplicit(t *testing.T) {
	s := newTestCheckpointStore(t)
	ctx := context.Background()
	seedCheckpoint(t, s, "ws_a", "researcher", "run-1", "step-1", CheckpointInProgress, "")
	seedCheckpoint(t, s, "ws_b", "researcher", "run-1", "step-1", CheckpointInProgress, "")
	seedCheckpoint(t, s, "ws_a", "researcher", "run-1", "step-2", CheckpointCompleted, `{"done":true}`)

	scoped, err := s.ListInProgress(ctx, "ws_a")
	if err != nil {
		t.Fatal(err)
	}
	if len(scoped) != 1 || scoped[0].WorkspaceID != "ws_a" {
		t.Fatalf("scoped in-progress listing crossed the boundary: %+v", scoped)
	}

	all, err := s.ListInProgressAcrossWorkspaces(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("the deployment sweep returned %d rows, want 2", len(all))
	}
	seen := map[string]bool{}
	for _, cp := range all {
		if cp.WorkspaceID == "" {
			t.Error("a swept row does not name its workspace, so a resumer cannot act on it correctly")
		}
		seen[cp.WorkspaceID] = true
	}
	if !seen["ws_a"] || !seen["ws_b"] {
		t.Fatalf("the sweep missed a tenant: %v", seen)
	}
}

// The v2 migration rebuilds the table because SQLite cannot widen a primary
// key in place. A database written by an older binary must keep its rows, and
// they must land in the personal workspace — whose runs they were.
func TestPreTenantCheckpointsSurviveTheKeyWidening(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoints.db")

	// Build a v1 database by hand: the old shape, the old key, version 1.
	legacy, err := sqlitex.Open(path, sqlitex.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`
CREATE TABLE workflow_checkpoints (
    agent_id   TEXT NOT NULL,
    run_id     TEXT NOT NULL,
    step_id    TEXT NOT NULL,
    state      TEXT,
    status     TEXT NOT NULL DEFAULT 'pending',
    updated_at DATETIME NOT NULL,
    PRIMARY KEY (agent_id, run_id, step_id)
);`); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(
		`INSERT INTO workflow_checkpoints (agent_id, run_id, step_id, state, status, updated_at)
		 VALUES ('researcher','run-1','step-1','{"answer":"legacy"}','completed',?)`,
		time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err := sqlitex.RecordSchemaVersion(legacy, "checkpoints", 1); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := NewCheckpointStore(path)
	if err != nil {
		t.Fatalf("upgrading a v1 checkpoint database failed: %v", err)
	}
	defer store.Close()

	version, err := sqlitex.SchemaVersion(store.db, "checkpoints")
	if err != nil {
		t.Fatal(err)
	}
	if version != 2 {
		t.Fatalf("schema version = %d, want 2", version)
	}
	cp, err := store.Get(context.Background(), wsroot.PersonalWorkspaceID, "researcher", "run-1", "step-1")
	if err != nil {
		t.Fatalf("a pre-tenant checkpoint was lost by the rebuild: %v", err)
	}
	if string(cp.State) != `{"answer":"legacy"}` || cp.Status != CheckpointCompleted {
		t.Fatalf("the rebuild changed the row: %+v", cp)
	}

	// The widened key is real: the same triple in another workspace is now its
	// own row rather than an overwrite of the migrated one.
	seedCheckpoint(t, store, "ws_b", "researcher", "run-1", "step-1", CheckpointCompleted, `{"answer":"beta"}`)
	migrated, err := store.Get(context.Background(), wsroot.PersonalWorkspaceID, "researcher", "run-1", "step-1")
	if err != nil || string(migrated.State) != `{"answer":"legacy"}` {
		t.Fatalf("a post-migration write from another tenant overwrote the migrated row: %+v err=%v", migrated, err)
	}
}

// The executor stamps the tenant, so callers construct Checkpoint literals
// without a workspace and still get the right row.
func TestTheExecutorStampsTheRunsWorkspace(t *testing.T) {
	store := newTestCheckpointStore(t)
	w := &WorkflowExecutor{store: store, log: zap.NewNop()}

	if err := w.saveCheckpoint(workspaceCtx("ws_a"), Checkpoint{
		AgentID: "researcher", RunID: "run-1", StepID: "step-1",
		Status: CheckpointCompleted, State: json.RawMessage(`{"answer":"alpha"}`),
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	got, err := w.loadCheckpoint(workspaceCtx("ws_a"), "researcher", "run-1", "step-1")
	if err != nil {
		t.Fatalf("the executor could not read back its own checkpoint: %v", err)
	}
	if got.WorkspaceID != "ws_a" {
		t.Fatalf("stamped workspace = %q, want ws_a", got.WorkspaceID)
	}
	if _, err := w.loadCheckpoint(workspaceCtx("ws_b"), "researcher", "run-1", "step-1"); err == nil {
		t.Fatal("another tenant's executor restored this run's step")
	}
	// A run with no principal is the personal workspace's, which is what a
	// single-user installation's rows already are.
	if _, err := w.loadCheckpoint(context.Background(), "researcher", "run-1", "step-1"); err == nil {
		t.Fatal("an unattributed run restored a tenant's step")
	}
}

// TestExecutorCheckpointsGoThroughTheStampingHelpers is the structural half.
//
// The isolation tests above prove the writes that exist today carry the
// tenant. They cannot prove the next one will: a Checkpoint literal that omits
// WorkspaceID compiles, writes a row, and lands it in the personal workspace,
// where it can collide with a genuinely personal run. This fails the build when
// the executor reaches the store directly instead of through saveCheckpoint or
// loadCheckpoint, which are the only two places the tenant is applied.
func TestExecutorCheckpointsGoThroughTheStampingHelpers(t *testing.T) {
	stampingHelpers := map[string]bool{"saveCheckpoint": true, "loadCheckpoint": true}
	fileSet := token.NewFileSet()
	for _, name := range []string{"workflow.go", "flow.go"} {
		parsed, err := parser.ParseFile(fileSet, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || stampingHelpers[fn.Name.Name] {
				continue
			}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				method, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				field, ok := method.X.(*ast.SelectorExpr)
				if !ok || field.Sel.Name != "store" {
					return true
				}
				receiver, ok := field.X.(*ast.Ident)
				if !ok || receiver.Name != "w" {
					return true
				}
				if method.Sel.Name == "Upsert" || method.Sel.Name == "Get" {
					t.Errorf("%s: %s calls w.store.%s directly — use w.saveCheckpoint/w.loadCheckpoint so the run's workspace cannot be omitted",
						fileSet.Position(call.Pos()), fn.Name.Name, method.Sel.Name)
				}
				return true
			})
		}
	}
	// Sanity: the guard is looking at real files, not an empty directory.
	if _, err := os.Stat("workflow.go"); err != nil {
		t.Fatalf("guard ran against the wrong directory: %v", err)
	}
}

// workspace_purge_test.go — the deletion coverage gap is a written list, not a
// silence.
//
// MU-032 criterion 4 covers twenty-seven resource classes and this build purges
// a few of them. That is a normal state for work in progress; what is NOT
// acceptable is the gap being discoverable only by reading a deletion report
// from a customer's workspace. Every uncovered class is named here with the
// reason it is uncovered, so adding a resource to the ownership catalog
// without either a purger or an entry below fails the build.
package gateway

import (
	"context"
	"sort"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/actionlog"
	"github.com/soulacy/soulacy/internal/approvals"
	"github.com/soulacy/soulacy/internal/knowledge"
	"github.com/soulacy/soulacy/internal/mcpstore"
	"github.com/soulacy/soulacy/internal/memory"
	"github.com/soulacy/soulacy/internal/queue/dlq"
	"github.com/soulacy/soulacy/internal/rbac"
	"github.com/soulacy/soulacy/internal/runs"
	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/internal/schedules"
	"github.com/soulacy/soulacy/internal/session"
	storagesqlite "github.com/soulacy/soulacy/internal/storage/sqlite"
	"github.com/soulacy/soulacy/internal/workboard"
	"github.com/soulacy/soulacy/internal/workspacepolicy"
	"github.com/soulacy/soulacy/internal/workspacepurge"
)

// notYetPurged is the deletion gap, one line per class, each with what is
// missing. An entry is a claim a reviewer can check; the absence of one is the
// build failing.
var notYetPurged = map[string]string{
	"studio-drafts":   "per-workspace, per-user directory under <root>/studio/drafts",
	"studio-traces":   "trace directory, which is env-configurable",
	"studio-learning": "four tables plus the lesson files",
	"skills":          "per-workspace skill inventory under its own base",
	"channels":        "same as mcp: configuration, not a store",
	"webhooks":        "same as mcp: configuration, not a store",
	"shares":          "share records are file-backed under their own root",
	"artifacts":       "workboard_artifacts is purged with workboard; object storage is not",
	"secrets":         "cryptographic erasure, not a DELETE — the catalog says so, and doing it as a row delete would leave the ciphertext recoverable from a backup",
	"credentials":     "revoke-then-purge across three tables, and revocation has to happen first so a credential cannot be used between the two",
	"api-keys":        "same shape as credentials",
	"workspace-files": "there is no single workspace file tree; each store applies wsroot to its own base, so this needs one purger per class rather than one for all",
}

func TestTheWorkspacePurgeCoverageGapIsAKnownList(t *testing.T) {
	actions, err := actionlog.New(t.TempDir()+"/events", t.TempDir()+"/events.db", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = actions.Close() })
	hotMemory, err := memory.NewFileStore(t.TempDir() + "/memory")
	if err != nil {
		t.Fatal(err)
	}
	archiveMemory, err := memory.NewSQLiteArchive(t.TempDir() + "/memory.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = archiveMemory.Close() })
	vectorMemory, err := memory.NewVectorStore(archiveMemory.DB(), nil, 3)
	if err != nil {
		t.Fatal(err)
	}
	// A server with the stores PRESENT, because this guard is about what the
	// build can purge, not about what one instance happens to have wired. A
	// zero-valued store is enough: the coverage question is which resources
	// are registered, and no purger runs here.
	server := &Server{
		actions:           storagesqlite.NewActionLog(actions),
		memoryStore:       hotMemory,
		memoryArchive:     storagesqlite.NewMemoryArchive(archiveMemory),
		vectorMemory:      vectorMemory,
		loader:            &runtime.Loader{},
		rbacManager:       rbac.NewManager(rbac.NoopStore{}, zap.NewNop()),
		runStore:          &runs.Store{},
		workboardStore:    &workboard.Store{},
		workspacePolicies: &workspacepolicy.Store{},
		mcpServers:        &mcpstore.Store{},
		dlqStore:          &dlq.SQLiteStore{},
		approvalStore:     &approvals.Store{},
		scheduleStore:     &schedules.Store{},
		knowledgeStore:    &knowledge.Store{},
		historyStore:      &session.SQLiteHistoryStore{},
		sessionOwnership:  &session.SQLiteOwnershipStore{},
		resourceStore:     &session.SQLiteStore{},
		checkpointStore:   &runtime.CheckpointStore{},
	}
	purgers := server.workspacePurgers()
	if err := workspacepurge.ValidatePurgers(purgers); err != nil {
		t.Fatalf("the registered purgers disagree with the ownership catalog: %v", err)
	}

	uncovered := workspacepurge.Coverage(purgers)
	var undeclared []string
	for _, resource := range uncovered {
		reason, declared := notYetPurged[resource]
		if !declared {
			undeclared = append(undeclared, resource)
			continue
		}
		if strings.TrimSpace(reason) == "" {
			t.Errorf("%q is declared uncovered with no reason", resource)
		}
	}
	sort.Strings(undeclared)
	for _, resource := range undeclared {
		t.Errorf("resource %q survives a workspace deletion and is not in notYetPurged — "+
			"register a purger in workspacePurgers, or add it here with what is missing", resource)
	}

	// The other direction. An entry naming a class that IS now purged is a
	// stale claim, and leaving it makes the list read as a bigger gap than
	// exists — which is how a list stops being maintained.
	covered := map[string]bool{}
	for _, resource := range uncovered {
		covered[resource] = true
	}
	for resource := range notYetPurged {
		if !covered[resource] {
			t.Errorf("%q is listed as not-yet-purged but is now covered — remove the entry", resource)
		}
	}
}

func TestWorkspacePurgeRemovesOnlyTheDeletingWorkspacesDeadLetters(t *testing.T) {
	store, err := dlq.NewSQLiteStore(t.TempDir() + "/dlq.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	for _, workspaceID := range []string{"ws_delete", "ws_keep"} {
		if err := store.Push(ctx, dlq.DeadLetter{
			ID: dlq.NewID(), WorkspaceID: workspaceID, Queue: "runs", Payload: []byte(`{"run":"one"}`), ErrorMsg: "failed",
		}); err != nil {
			t.Fatal(err)
		}
	}

	server := &Server{dlqStore: store}
	var purge workspacepurge.Purger
	for _, candidate := range server.workspacePurgers() {
		if candidate.Resource == "queue-dlq" {
			purge = candidate
			break
		}
	}
	if purge.Purge == nil {
		t.Fatal("the durable DLQ registered no workspace purger")
	}
	removed, err := purge.Purge(ctx, "ws_delete")
	if err != nil {
		t.Fatal(err)
	}
	if removed.Rows != 1 {
		t.Fatalf("removed %d rows, want 1", removed.Rows)
	}
	deleted, err := store.List(ctx, "ws_delete", "")
	if err != nil {
		t.Fatal(err)
	}
	kept, err := store.List(ctx, "ws_keep", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 0 || len(kept) != 1 {
		t.Fatalf("after purge: deleting workspace=%d, other workspace=%d", len(deleted), len(kept))
	}
}

// A server with nothing wired must still produce a valid purger set, because
// that is the state a directly constructed or partially wired gateway is in,
// and a purge that panics is worse than one that reports no coverage.
func TestPurgersFromAnUnwiredServerAreStillValid(t *testing.T) {
	purgers := (&Server{}).workspacePurgers()
	if err := workspacepurge.ValidatePurgers(purgers); err != nil {
		t.Fatalf("an unwired server produced invalid purgers: %v", err)
	}
	report, err := workspacepurge.Run(workspacepurge.Options{
		WorkspaceID: "ws_a", RequestedBy: "usr_alice", Purgers: purgers,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Complete {
		t.Fatal("an unwired server reported a complete deletion")
	}
}

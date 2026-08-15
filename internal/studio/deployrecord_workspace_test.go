// deployrecord_workspace_test.go — the cross-tenant isolation contract for the
// deployment history. internal/ownership/catalog.go names this file as the
// isolation evidence for internal/studio/deployrecord.go.
//
// The isolation here is structural in the strongest available sense: each
// workspace's histories live in a different directory, so a tenant asking for
// another tenant's agent does not get a filtered-out result, it gets "no such
// file". Nothing is filtered, so nothing can forget to filter.
package studio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/wsroot"
	"github.com/soulacy/soulacy/pkg/agent"
)

func recordDeployment(t *testing.T, store *DeploymentStore, workspaceID string, def agent.Definition, cert *CertificationRecord, note string) DeploymentRecord {
	t.Helper()
	rec, err := NewDeploymentRecord(def, DefaultSOULRules, cert, "alice", note)
	if err != nil {
		t.Fatalf("build record: %v", err)
	}
	stored, err := store.Record(workspaceID, rec)
	if err != nil {
		t.Fatalf("record in %s: %v", workspaceID, err)
	}
	return stored
}

// Agent IDs are unique within a workspace, not across the deployment. Under a
// single shared directory two tenants deploying an agent with the same ID
// would append to one history file, so each tenant's version numbers would
// count the other's deploys.
func TestTwoWorkspacesDeployingTheSameAgentIDKeepSeparateHistories(t *testing.T) {
	store := newTestDeploymentStore(t)

	defA := deployableAgent()
	defA.SystemPrompt = "workspace A's confidential prompt"
	defB := deployableAgent()
	defB.SystemPrompt = "workspace B's confidential prompt"

	first := recordDeployment(t, store, "ws_a", defA, nil, "A first")
	second := recordDeployment(t, store, "ws_b", defB, nil, "B first")

	if first.Version != 1 || second.Version != 1 {
		t.Fatalf("version numbering leaked across workspaces: %d / %d", first.Version, second.Version)
	}

	histA, err := store.History("ws_a", defA.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(histA) != 1 {
		t.Fatalf("workspace A sees %d records, want 1", len(histA))
	}
	restored, err := histA[0].AgentDefinition()
	if err != nil {
		t.Fatal(err)
	}
	if restored.SystemPrompt != "workspace A's confidential prompt" {
		t.Fatalf("a deployed definition crossed the tenant boundary: %q", restored.SystemPrompt)
	}

	// A third workspace that never deployed sees nothing at all — not an
	// empty-after-filtering list, but an absent file.
	histC, err := store.History("ws_c", defA.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(histC) != 0 {
		t.Fatalf("a workspace that never deployed sees %d records", len(histC))
	}
	if _, err := store.Latest("ws_c", defA.ID); err != ErrNoDeployment {
		t.Fatalf("Latest in an undeployed workspace returned %v, want ErrNoDeployment", err)
	}
}

// Product invariant 7: a personal deployment's files stay exactly where they
// were before the store became tenant-aware.
func TestPersonalDeploymentFilesStayAtTheRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "deployments")
	store := NewDeploymentStore(root)
	def := deployableAgent()

	recordDeployment(t, store, "", def, nil, "personal")

	personalPath := filepath.Join(root, def.ID+deploymentFileExt)
	if _, err := os.Stat(personalPath); err != nil {
		t.Fatalf("the personal history is not at its historical path: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, wsroot.NamespaceDir)); !os.IsNotExist(err) {
		t.Fatalf("a personal deployment created a tenant namespace directory: %v", err)
	}

	// An explicit personal workspace ID resolves to the same file, so the two
	// ways of saying "personal" cannot diverge into two histories.
	recordDeployment(t, store, wsroot.PersonalWorkspaceID, def, nil, "personal again")
	hist, err := store.History("", def.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 2 {
		t.Fatalf("the empty and explicit personal IDs wrote different histories: %d records", len(hist))
	}
}

// A tenant's history lives under its own directory, and the shared root holds
// nothing but those directories and personal's own files.
func TestATenantsHistoryIsWrittenUnderItsOwnDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "deployments")
	store := NewDeploymentStore(root)
	def := deployableAgent()

	recordDeployment(t, store, "ws_a", def, nil, "A")

	tenantPath := filepath.Join(root, wsroot.NamespaceDir, "ws_a", def.ID+deploymentFileExt)
	if _, err := os.Stat(tenantPath); err != nil {
		t.Fatalf("the tenant's history is not under its own directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, def.ID+deploymentFileExt)); !os.IsNotExist(err) {
		t.Fatal("a tenant's history was written into the shared root")
	}
	// The shared root holds the namespace directory and nothing else. Stated as
	// "only this entry" rather than "no .tmp files" on purpose: the temp file
	// is renamed away on success and removed on every error path, so checking
	// for leftovers would pass whether or not the write was namespaced. What is
	// actually observable — and what matters — is that a tenant's write leaves
	// no artifact at all in a directory other tenants read.
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != wsroot.NamespaceDir {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("a tenant write left artifacts in the shared root: %s", strings.Join(names, ", "))
	}
}

// Rollback is the sharpest edge in this store: it reads the previous record and
// re-applies its Definition. Under a shared history a tenant's rollback would
// restore another tenant's system prompt into its own agent — not a leak, an
// adoption.
func TestARollbackCannotRestoreAnotherWorkspacesDefinition(t *testing.T) {
	store := newTestDeploymentStore(t)

	defA := deployableAgent()
	defA.SystemPrompt = "workspace A version one"
	recordDeployment(t, store, "ws_a", defA, nil, "A v1")
	defA2 := defA
	defA2.SystemPrompt = "workspace A version two"
	recordDeployment(t, store, "ws_a", defA2, nil, "A v2")

	defB := deployableAgent()
	defB.SystemPrompt = "workspace B version one"
	recordDeployment(t, store, "ws_b", defB, nil, "B v1")

	// B has exactly one deployment, so it has nothing to roll back to — even
	// though the same agent ID has two versions in another workspace.
	if _, err := store.Rollback("ws_b", defB.ID, "bob"); err != ErrNoPreviousDeployment {
		t.Fatalf("rollback in B returned %v, want ErrNoPreviousDeployment", err)
	}

	restored, err := store.Rollback("ws_a", defA.ID, "alice")
	if err != nil {
		t.Fatalf("rollback in A: %v", err)
	}
	def, err := restored.AgentDefinition()
	if err != nil {
		t.Fatal(err)
	}
	if def.SystemPrompt != "workspace A version one" {
		t.Fatalf("rollback restored the wrong definition: %q", def.SystemPrompt)
	}
	if restored.Version != 3 || restored.RolledBackTo != 1 {
		t.Fatalf("rollback did not append as a new version: %+v", restored)
	}
	// B's single record is untouched by A's rollback.
	histB, err := store.History("ws_b", defB.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(histB) != 1 {
		t.Fatalf("another workspace's rollback changed B's history: %d records", len(histB))
	}
}

// The scheduler's readiness gate must read its verdict from the workspace the
// run will execute in. One tenant certifying an agent must not clear another
// tenant's identically-named agent to fire on a schedule.
func TestScheduleReadinessDoesNotBorrowAnotherWorkspacesCertification(t *testing.T) {
	store := newTestDeploymentStore(t)
	def := deployableAgent()
	cert := Certify(certifiableInput(), certAt)

	recordDeployment(t, store, "ws_a", def, &cert, "A certified")

	ready := store.ScheduleReadiness("ws_a", def.ID)
	if !ready.Deployed || ready.Blocked {
		t.Fatalf("the certifying workspace was not cleared: %+v", ready)
	}

	// B never deployed this agent. The gate must have *no opinion* rather than
	// inheriting A's verdict — reporting Deployed here would be the leak, and
	// reporting Blocked would stop B's hand-written agents from ever firing.
	other := store.ScheduleReadiness("ws_b", def.ID)
	if other.Deployed {
		t.Fatalf("a neighbour's certification was visible to another workspace: %+v", other)
	}
	if other.Blocked {
		t.Fatalf("an undeployed agent was blocked rather than left alone: %+v", other)
	}

	// And an uncertified deployment in B stays blocked despite A's passing one.
	recordDeployment(t, store, "ws_b", def, nil, "B uncertified")
	blocked := store.ScheduleReadiness("ws_b", def.ID)
	if !blocked.Deployed || !blocked.Blocked {
		t.Fatalf("B's uncertified deployment was cleared by A's certification: %+v", blocked)
	}
}

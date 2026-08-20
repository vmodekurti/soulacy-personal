// workspace_test.go — the cross-tenant contract for per-agent RBAC grants,
// and a regression test for the divergence that made a missed grant *widen*
// access instead of denying it.
package rbac

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/soulacy/soulacy/internal/wsroot"
)

func newGrantStore(t *testing.T) (*SQLiteStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rbac.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, path
}

func TestPurgeWorkspaceRemovesOnlyThatWorkspacesAgentGrants(t *testing.T) {
	store, _ := newGrantStore(t)
	for _, workspaceID := range []string{"ws_a", "ws_b"} {
		if err := store.SetAgentGrantInWorkspace(AgentGrant{
			WorkspaceID: workspaceID, Role: RoleDeveloper, AgentID: "support",
			Actions: []string{ActionRead}, GrantedByRole: RoleOwner,
		}); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := store.PurgeWorkspace(context.Background(), "ws_a")
	if err != nil || removed.Rows != 1 {
		t.Fatalf("purge=%+v err=%v", removed, err)
	}
	a, _ := store.ListAgentGrantsInWorkspace("ws_a")
	b, _ := store.ListAgentGrantsInWorkspace("ws_b")
	if len(a) != 0 || len(b) != 1 {
		t.Fatalf("after purge: ws_a=%+v ws_b=%+v", a, b)
	}
}

// The failure mode that makes this package's key consistency load-bearing.
//
// A grant is an allow-list that NARROWS a role. When a lookup misses,
// CanAccessAgentInWorkspace falls through to the static role baseline, which
// is broader. So a grant stored under one workspace key and read under another
// does not fail closed — it silently restores the wider permission the grant
// was written to remove.
//
// This test states that property directly, so anyone changing the key scheme
// sees what it costs.
func TestAMissedGrantWidensAccessRatherThanDenying(t *testing.T) {
	store, _ := newGrantStore(t)
	// developer may write agents by default.
	if !HasPermission(RoleDeveloper, ResourceAgents, ActionWrite) {
		t.Skip("baseline changed; this test encodes the narrowing case")
	}
	// A restrictive grant: in ws_a, developer may only read this agent.
	if err := store.SetAgentGrantInWorkspace(AgentGrant{
		WorkspaceID: "ws_a", Role: RoleDeveloper, AgentID: "billing-bot",
		Actions: []string{ActionRead}, GrantedByRole: RoleOwner,
	}); err != nil {
		t.Fatal(err)
	}

	allowed, err := store.CanAccessAgentInWorkspace("ws_a", RoleDeveloper, "billing-bot", ResourceAgents, ActionWrite)
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("the restrictive grant did not narrow the role in its own workspace")
	}

	// Reading the same grant under a different workspace key misses, and the
	// baseline comes back — which is exactly the escalation the "personal" vs
	// "ws_personal" split produced in practice.
	widened, err := store.CanAccessAgentInWorkspace("ws_b", RoleDeveloper, "billing-bot", ResourceAgents, ActionWrite)
	if err != nil {
		t.Fatal(err)
	}
	if !widened {
		t.Skip("baseline denies this action; the widening property cannot be shown")
	}
}

// The concrete regression: the legacy key and the current one must be the same
// string, so a grant written by one request path is seen by the other.
func TestThePersonalKeyMatchesTheRestOfTheCodebase(t *testing.T) {
	if personalWorkspace != wsroot.PersonalWorkspaceID {
		t.Fatalf("rbac personal key = %q, want %q — a grant written under one is invisible under the other, and a missed grant widens access",
			personalWorkspace, wsroot.PersonalWorkspaceID)
	}
}

// A grant written before the keys were unified must be migrated, not
// stranded. Leaving it under the old key would mean the restriction it
// encodes silently stops applying.
func TestLegacyPersonalGrantsAreMigratedOnOpen(t *testing.T) {
	store, path := newGrantStore(t)
	if _, err := store.db.Exec(
		`INSERT INTO rbac_agent_grants (workspace_id,role,agent_id,actions,elevated,granted_by_role)
		 VALUES ('personal', ?, 'billing-bot', ?, 0, ?)`,
		RoleDeveloper, ActionRead, RoleOwner,
	); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()

	grants, err := reopened.ListAgentGrantsInWorkspace(wsroot.PersonalWorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(grants) != 1 || grants[0].AgentID != "billing-bot" {
		t.Fatalf("a legacy grant was stranded under the old key: %+v", grants)
	}
	// And nothing is left behind under the old key.
	var stragglers int
	if err := reopened.db.QueryRow(`SELECT COUNT(*) FROM rbac_agent_grants WHERE workspace_id = 'personal'`).Scan(&stragglers); err != nil {
		t.Fatal(err)
	}
	if stragglers != 0 {
		t.Fatalf("%d grants remain under the legacy key", stragglers)
	}
}

// Grants are per workspace: one tenant's restriction must not apply to
// another's agent of the same name, and one tenant's elevation must not.
func TestGrantsDoNotCrossWorkspaces(t *testing.T) {
	store, _ := newGrantStore(t)
	if err := store.SetAgentGrantInWorkspace(AgentGrant{
		WorkspaceID: "ws_a", Role: RoleViewer, AgentID: "shared-name",
		Actions: []string{ActionRead, ActionWrite}, Elevated: true, GrantedByRole: RoleOwner,
	}); err != nil {
		t.Fatal(err)
	}

	// The elevation applies in its own workspace.
	elevated, err := store.CanAccessAgentInWorkspace("ws_a", RoleViewer, "shared-name", ResourceAgents, ActionWrite)
	if err != nil {
		t.Fatal(err)
	}
	if !elevated {
		t.Fatal("an owner-granted elevation did not apply in its own workspace")
	}
	// It must not leak to another workspace's agent of the same name.
	leaked, err := store.CanAccessAgentInWorkspace("ws_b", RoleViewer, "shared-name", ResourceAgents, ActionWrite)
	if err != nil {
		t.Fatal(err)
	}
	if leaked {
		t.Fatal("an elevation granted in one workspace applied in another")
	}

	// And a listing shows only the owning workspace's grants.
	listed, err := store.ListAgentGrantsInWorkspace("ws_b")
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Fatalf("another workspace's grants were listed: %+v", listed)
	}
}

// A delete must not reach across the boundary.
func TestGrantDeleteCannotReachAnotherWorkspace(t *testing.T) {
	store, _ := newGrantStore(t)
	if err := store.SetAgentGrantInWorkspace(AgentGrant{
		WorkspaceID: "ws_a", Role: RoleDeveloper, AgentID: "billing-bot",
		Actions: []string{ActionRead}, GrantedByRole: RoleOwner,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteAgentGrantInWorkspace("ws_b", RoleDeveloper, "billing-bot"); err != nil {
		t.Fatal(err)
	}
	grants, err := store.ListAgentGrantsInWorkspace("ws_a")
	if err != nil {
		t.Fatal(err)
	}
	if len(grants) != 1 {
		t.Fatal("another workspace's delete removed the owner's grant — which would silently widen access")
	}
}

// rbac_test.go — tests for RBAC policy logic, SQLiteStore, and NoopStore.
package rbac

import (
	"path/filepath"
	"testing"
)

// ---------------------------------------------------------------------------
// IsKnownRole
// ---------------------------------------------------------------------------

func TestIsKnownRoleKnown(t *testing.T) {
	for _, r := range KnownRoles {
		if !IsKnownRole(r) {
			t.Errorf("IsKnownRole(%q) = false, want true", r)
		}
	}
}

func TestDefaultPolicyMatrix(t *testing.T) {
	resources := []string{
		ResourceAgents, ResourceChat, ResourceMemory, ResourceChannels, ResourceProviders,
		ResourceSkills, ResourceMCP, ResourceKnowledge, ResourceBuilder, ResourceTemplates,
		ResourceConfig, ResourceLogs, ResourceMetrics, ResourceSchedule, ResourceRBAC,
		ResourceSecrets, ResourceCredentials, ResourceTour, ResourceStudio, ResourcePlugins,
	}
	actions := []string{ActionRead, ActionWrite, ActionDelete, ActionChat, ActionEnable, ActionList, ActionSet, ActionRotate, ActionReveal}
	for _, role := range KnownRoles {
		rolePolicy, ok := defaultPolicy[role]
		if !ok {
			t.Fatalf("role %q has no policy", role)
		}
		for _, resource := range resources {
			resourcePolicy, ok := rolePolicy[resource]
			if !ok {
				t.Errorf("role %q does not explicitly define resource %q", role, resource)
				continue
			}
			for _, action := range actions {
				if got, want := HasPermission(role, resource, action), resourcePolicy[action]; got != want {
					t.Errorf("%s %s:%s = %v, want %v", role, resource, action, got, want)
				}
			}
		}
	}
}

func TestEveryWorkspaceRoleCanReadProductToursWithoutConfigAccess(t *testing.T) {
	for _, role := range KnownRoles {
		if !HasPermission(role, ResourceTour, ActionRead) {
			t.Errorf("%s cannot read its workspace product tour", role)
		}
	}
	if HasPermission(RoleDeveloper, ResourceConfig, ActionRead) {
		t.Fatal("tour access accidentally widened developer deployment-config access")
	}
	if HasPermission(RoleViewer, ResourceConfig, ActionRead) {
		t.Fatal("tour access accidentally widened viewer deployment-config access")
	}
}

func TestIsKnownRoleUnknown(t *testing.T) {
	for _, r := range []string{"superuser", "", "ADMIN"} {
		if IsKnownRole(r) {
			t.Errorf("IsKnownRole(%q) = true, want false", r)
		}
	}
}

// ---------------------------------------------------------------------------
// HasPermission — default policy matrix
// ---------------------------------------------------------------------------

func TestHasPermissionAdminHasFullAgentAccess(t *testing.T) {
	for _, action := range []string{ActionRead, ActionWrite, ActionDelete, ActionEnable} {
		if !HasPermission(RoleAdmin, ResourceAgents, action) {
			t.Errorf("admin agents %q: got false, want true", action)
		}
	}
}

func TestHasPermissionOperatorCannotDeleteAgents(t *testing.T) {
	if HasPermission(RoleOperator, ResourceAgents, ActionDelete) {
		t.Error("operator should not be allowed to delete agents")
	}
}

func TestHasPermissionViewerCanOnlyRead(t *testing.T) {
	if !HasPermission(RoleViewer, ResourceAgents, ActionRead) {
		t.Error("viewer should be allowed to read agents")
	}
	if HasPermission(RoleViewer, ResourceAgents, ActionWrite) {
		t.Error("viewer should not be allowed to write agents")
	}
	if HasPermission(RoleViewer, ResourceConfig, ActionRead) {
		t.Error("viewer should not be allowed to read config")
	}
}

func TestDemoDeveloperCanAuthorButCannotPublishOrAdminister(t *testing.T) {
	for _, permission := range [][2]string{
		{ResourceStudio, ActionRead}, {ResourceStudio, ActionWrite},
		{ResourceAgents, ActionRead}, {ResourceChat, ActionRead}, {ResourceChat, ActionChat},
		{ResourceChannels, ActionRead}, {ResourceProviders, ActionRead}, {ResourceTemplates, ActionRead},
		{ResourceSkills, ActionRead}, {ResourceMCP, ActionRead}, {ResourcePlugins, ActionRead},
		{ResourceKnowledge, ActionRead}, {ResourceSchedule, ActionRead},
	} {
		if !HasPermission(RoleDemoDeveloper, permission[0], permission[1]) {
			t.Errorf("demo developer missing %s:%s", permission[0], permission[1])
		}
	}
	for _, permission := range [][2]string{
		{ResourceAgents, ActionWrite}, {ResourceAgents, ActionEnable}, {ResourceAgents, ActionDelete},
		{ResourceBuilder, ActionWrite}, {ResourceChannels, ActionWrite}, {ResourceMCP, ActionWrite},
		{ResourceSkills, ActionWrite}, {ResourceKnowledge, ActionWrite}, {ResourceSecrets, ActionSet},
		{ResourceSchedule, ActionWrite}, {ResourceMemory, ActionRead}, {ResourceMetrics, ActionRead},
		{ResourceConfig, ActionRead}, {ResourceRBAC, ActionRead},
	} {
		if HasPermission(RoleDemoDeveloper, permission[0], permission[1]) {
			t.Errorf("demo developer unexpectedly has %s:%s", permission[0], permission[1])
		}
	}
}

func TestHasPermissionOperatorNoRBACAccess(t *testing.T) {
	if HasPermission(RoleOperator, ResourceRBAC, ActionRead) {
		t.Error("operator should have no RBAC access")
	}
}

func TestHasPermissionOperatorNoMetricsAccess(t *testing.T) {
	if HasPermission(RoleOperator, ResourceMetrics, ActionRead) {
		t.Error("operator should have no metrics access")
	}
}

func TestHasPermissionUnknownRole(t *testing.T) {
	if HasPermission("hacker", ResourceAgents, ActionRead) {
		t.Error("unknown role should have no permissions")
	}
}

func TestHasPermissionUnknownResource(t *testing.T) {
	if HasPermission(RoleAdmin, "unknown-resource", ActionRead) {
		t.Error("unknown resource should return false")
	}
}

func TestHasPermissionUnknownAction(t *testing.T) {
	if HasPermission(RoleAdmin, ResourceAgents, "destroy") {
		t.Error("unknown action should return false")
	}
}

func TestHasPermissionAdminCanChat(t *testing.T) {
	if !HasPermission(RoleAdmin, ResourceChat, ActionChat) {
		t.Error("admin should be able to chat")
	}
}

func TestHasPermissionChatRolesCanReadChatState(t *testing.T) {
	for _, role := range []string{RoleAdmin, RoleOperator, RoleViewer} {
		if !HasPermission(role, ResourceChat, ActionRead) {
			t.Errorf("%s should be able to read chat state", role)
		}
	}
}

// ---------------------------------------------------------------------------
// SQLiteStore helpers
// ---------------------------------------------------------------------------

func newRBACStore(t *testing.T) *SQLiteStore {
	t.Helper()
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "rbac.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// ---------------------------------------------------------------------------
// NewSQLiteStore
// ---------------------------------------------------------------------------

func TestNewSQLiteStoreCreates(t *testing.T) {
	s := newRBACStore(t)
	if s == nil {
		t.Fatal("NewSQLiteStore returned nil")
	}
}

func TestDedicatedSecretAndCredentialPolicy(t *testing.T) {
	for _, action := range []string{ActionList, ActionSet, ActionDelete} {
		if !HasPermission(RoleAdmin, ResourceSecrets, action) {
			t.Fatalf("admin missing secrets:%s", action)
		}
		if HasPermission(RoleOperator, ResourceSecrets, action) || HasPermission(RoleViewer, ResourceSecrets, action) {
			t.Fatalf("non-admin received secrets:%s", action)
		}
	}
	for _, action := range []string{ActionList, ActionSet, ActionDelete, ActionRotate} {
		if !HasPermission(RoleOperator, ResourceCredentials, action) {
			t.Fatalf("operator missing credentials:%s", action)
		}
	}
	if HasPermission(RoleOperator, ResourceCredentials, ActionReveal) || !HasPermission(RoleAdmin, ResourceCredentials, ActionReveal) {
		t.Fatal("plaintext credential reveal is not admin-only")
	}
}

func TestNewSQLiteStoreBadPath(t *testing.T) {
	_, err := NewSQLiteStore("/no/such/dir/rbac.db")
	if err == nil {
		t.Fatal("expected error for bad path, got nil")
	}
}

// ---------------------------------------------------------------------------
// SetAgentGrant / CanAccessAgent / DeleteAgentGrant
// ---------------------------------------------------------------------------

func TestSetAndCanAccessExactGrant(t *testing.T) {
	s := newRBACStore(t)

	if err := s.SetAgentGrant(AgentGrant{
		Role: RoleOperator, AgentID: "secret-agent",
		Actions: []string{ActionRead, ActionChat},
	}); err != nil {
		t.Fatalf("SetAgentGrant: %v", err)
	}

	allowed, err := s.CanAccessAgent(RoleOperator, "secret-agent", ActionRead)
	if err != nil || !allowed {
		t.Fatalf("CanAccessAgent read: allowed=%v err=%v", allowed, err)
	}
	allowed, err = s.CanAccessAgent(RoleOperator, "secret-agent", ActionDelete)
	if err != nil || allowed {
		t.Fatalf("CanAccessAgent delete (not granted): allowed=%v err=%v", allowed, err)
	}
}

func TestCanAccessAgentWildcardFallsThrough(t *testing.T) {
	s := newRBACStore(t)

	// Grant operator read on all agents via wildcard.
	if err := s.SetAgentGrant(AgentGrant{
		Role: RoleOperator, AgentID: "*",
		Actions: []string{ActionRead},
	}); err != nil {
		t.Fatalf("SetAgentGrant wildcard: %v", err)
	}

	// Any specific agent should match the wildcard.
	allowed, err := s.CanAccessAgent(RoleOperator, "any-agent", ActionRead)
	if err != nil || !allowed {
		t.Fatalf("wildcard match: allowed=%v err=%v", allowed, err)
	}
}

func TestCanAccessAgentFallsToDefaultPolicy(t *testing.T) {
	s := newRBACStore(t)
	// No grant stored — should fall through to HasPermission.
	allowed, err := s.CanAccessAgent(RoleAdmin, "some-agent", ActionDelete)
	if err != nil || !allowed {
		t.Fatalf("fallback to default policy (admin delete): allowed=%v err=%v", allowed, err)
	}
}

func TestSetAgentGrantRequiresRoleAndAgentID(t *testing.T) {
	s := newRBACStore(t)
	if err := s.SetAgentGrant(AgentGrant{Role: "", AgentID: "ag", Actions: []string{"read"}}); err == nil {
		t.Error("expected error for empty role")
	}
	if err := s.SetAgentGrant(AgentGrant{Role: RoleOperator, AgentID: "", Actions: []string{"read"}}); err == nil {
		t.Error("expected error for empty agentID")
	}
}

func TestSetAgentGrantUnknownRoleErrors(t *testing.T) {
	s := newRBACStore(t)
	if err := s.SetAgentGrant(AgentGrant{Role: "hacker", AgentID: "ag", Actions: []string{"read"}}); err == nil {
		t.Error("expected error for unknown role")
	}
}

func TestWorkspaceGrantIsolationAndElevation(t *testing.T) {
	s := newRBACStore(t)
	if err := s.SetAgentGrantInWorkspace(AgentGrant{
		WorkspaceID: "workspace-a", Role: RoleViewer, AgentID: "agent-1",
		Actions: []string{ActionDelete}, Elevated: true, GrantedByRole: RoleOwner,
	}); err != nil {
		t.Fatal(err)
	}
	allowed, err := s.CanAccessAgentInWorkspace("workspace-a", RoleViewer, "agent-1", ResourceAgents, ActionDelete)
	if err != nil || !allowed {
		t.Fatalf("owner elevation in workspace-a: allowed=%v err=%v", allowed, err)
	}
	allowed, err = s.CanAccessAgentInWorkspace("workspace-b", RoleViewer, "agent-1", ResourceAgents, ActionDelete)
	if err != nil || allowed {
		t.Fatalf("grant leaked into workspace-b: allowed=%v err=%v", allowed, err)
	}
	if err := s.SetAgentGrantInWorkspace(AgentGrant{
		WorkspaceID: "workspace-a", Role: RoleViewer, AgentID: "agent-2",
		Actions: []string{ActionDelete}, GrantedByRole: RoleAdmin,
	}); err != nil {
		t.Fatal(err)
	}
	allowed, err = s.CanAccessAgentInWorkspace("workspace-a", RoleViewer, "agent-2", ResourceAgents, ActionDelete)
	if err != nil || allowed {
		t.Fatalf("ordinary grant widened membership role: allowed=%v err=%v", allowed, err)
	}
	if err := s.SetAgentGrantInWorkspace(AgentGrant{
		WorkspaceID: "workspace-a", Role: RoleViewer, AgentID: "agent-3",
		Actions: []string{ActionDelete}, Elevated: true, GrantedByRole: RoleAdmin,
	}); err == nil {
		t.Fatal("non-owner elevation was accepted")
	}
}

func TestSetAgentGrantUpserts(t *testing.T) {
	s := newRBACStore(t)
	g := AgentGrant{Role: RoleDeveloper, AgentID: "bot", Actions: []string{ActionRead}}
	_ = s.SetAgentGrant(g)
	// Update to also allow writes already permitted by the role. Object rules
	// narrow unless an owner explicitly marks an elevation.
	g.Actions = []string{ActionRead, ActionWrite}
	if err := s.SetAgentGrant(g); err != nil {
		t.Fatalf("second SetAgentGrant: %v", err)
	}
	allowed, _ := s.CanAccessAgent(RoleDeveloper, "bot", ActionWrite)
	if !allowed {
		t.Error("after upsert, role-permitted write should be allowed")
	}
}

func TestDeleteAgentGrant(t *testing.T) {
	s := newRBACStore(t)
	_ = s.SetAgentGrant(AgentGrant{Role: RoleOperator, AgentID: "ag", Actions: []string{ActionRead}})
	if err := s.DeleteAgentGrant(RoleOperator, "ag"); err != nil {
		t.Fatalf("DeleteAgentGrant: %v", err)
	}
	// After deletion, should fall back to default policy.
	allowed, _ := s.CanAccessAgent(RoleOperator, "ag", ActionRead)
	if !allowed {
		t.Error("after delete, default policy (operator read) should apply")
	}
}

func TestDeleteAgentGrantIdempotent(t *testing.T) {
	s := newRBACStore(t)
	// Deleting a non-existent grant should not error.
	if err := s.DeleteAgentGrant(RoleAdmin, "ghost"); err != nil {
		t.Errorf("DeleteAgentGrant non-existent: %v", err)
	}
}

// ---------------------------------------------------------------------------
// ListAgentGrants / ListAgentGrantsForRole
// ---------------------------------------------------------------------------

func TestListAgentGrantsEmpty(t *testing.T) {
	s := newRBACStore(t)
	grants, err := s.ListAgentGrants()
	if err != nil {
		t.Fatalf("ListAgentGrants: %v", err)
	}
	if len(grants) != 0 {
		t.Errorf("expected 0 grants, got %d", len(grants))
	}
}

func TestListAgentGrantsReturnsBoth(t *testing.T) {
	s := newRBACStore(t)
	_ = s.SetAgentGrant(AgentGrant{Role: RoleOperator, AgentID: "ag1", Actions: []string{ActionRead}})
	_ = s.SetAgentGrant(AgentGrant{Role: RoleViewer, AgentID: "ag2", Actions: []string{ActionChat}})

	grants, err := s.ListAgentGrants()
	if err != nil {
		t.Fatalf("ListAgentGrants: %v", err)
	}
	if len(grants) != 2 {
		t.Fatalf("grant count = %d, want 2", len(grants))
	}
}

func TestListAgentGrantsForRoleFilters(t *testing.T) {
	s := newRBACStore(t)
	_ = s.SetAgentGrant(AgentGrant{Role: RoleOperator, AgentID: "ag1", Actions: []string{ActionRead}})
	_ = s.SetAgentGrant(AgentGrant{Role: RoleViewer, AgentID: "ag2", Actions: []string{ActionRead}})

	grants, err := s.ListAgentGrantsForRole(RoleOperator)
	if err != nil {
		t.Fatalf("ListAgentGrantsForRole: %v", err)
	}
	if len(grants) != 1 || grants[0].AgentID != "ag1" {
		t.Errorf("expected 1 operator grant, got %+v", grants)
	}
}

func TestListActionsCSVWithSpaces(t *testing.T) {
	s := newRBACStore(t)
	// Insert a raw row with spaces in the actions CSV to test trimming.
	_, _ = s.db.Exec(
		`INSERT INTO rbac_agent_grants (role, agent_id, actions) VALUES (?, ?, ?)`,
		RoleViewer, "spaced", "read, chat, ",
	)
	grants, err := s.ListAgentGrants()
	if err != nil {
		t.Fatalf("ListAgentGrants: %v", err)
	}
	if len(grants) != 1 {
		t.Fatalf("grant count = %d, want 1", len(grants))
	}
	// "read", "chat" should be present; trailing empty token dropped.
	if len(grants[0].Actions) != 2 {
		t.Errorf("actions = %v, want [read chat]", grants[0].Actions)
	}
}

// ---------------------------------------------------------------------------
// NoopStore
// ---------------------------------------------------------------------------

func TestNoopStoreDelegatesToDefaultPolicy(t *testing.T) {
	var s NoopStore
	// Admin can read agents via default policy.
	allowed, err := s.CanAccessAgent(RoleAdmin, "any-agent", ActionRead)
	if err != nil || !allowed {
		t.Errorf("NoopStore admin read: allowed=%v err=%v", allowed, err)
	}
	// Viewer cannot delete.
	allowed, err = s.CanAccessAgent(RoleViewer, "any-agent", ActionDelete)
	if err != nil || allowed {
		t.Errorf("NoopStore viewer delete: allowed=%v err=%v", allowed, err)
	}
}

func TestNoopStoreMutationsAreNoops(t *testing.T) {
	var s NoopStore
	if err := s.SetAgentGrant(AgentGrant{Role: RoleAdmin, AgentID: "ag"}); err != nil {
		t.Errorf("SetAgentGrant: %v", err)
	}
	if err := s.DeleteAgentGrant(RoleAdmin, "ag"); err != nil {
		t.Errorf("DeleteAgentGrant: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	grants, _ := s.ListAgentGrants()
	if len(grants) != 0 {
		t.Errorf("ListAgentGrants NoopStore: got %v", grants)
	}
}

// ---------------------------------------------------------------------------
// HasPermission — broader matrix coverage
// ---------------------------------------------------------------------------

func TestHasPermissionOperatorCannotWriteAgents(t *testing.T) {
	if HasPermission(RoleOperator, ResourceAgents, ActionWrite) {
		t.Error("operator should not author or change agent definitions")
	}
}

func TestHasPermissionOperatorCanEnableAgents(t *testing.T) {
	if !HasPermission(RoleOperator, ResourceAgents, ActionEnable) {
		t.Error("operator should be allowed to enable agents")
	}
}

func TestHasPermissionOperatorCannotWriteChannels(t *testing.T) {
	if HasPermission(RoleOperator, ResourceChannels, ActionWrite) {
		t.Error("operator should not be allowed to write channels")
	}
}

func TestHasPermissionOperatorCanEnableChannels(t *testing.T) {
	if !HasPermission(RoleOperator, ResourceChannels, ActionEnable) {
		t.Error("operator should be allowed to enable channels")
	}
}

func TestHasPermissionAdminCanDeleteRBAC(t *testing.T) {
	if !HasPermission(RoleAdmin, ResourceRBAC, ActionDelete) {
		t.Error("admin should be allowed to delete RBAC grants")
	}
}

func TestHasPermissionViewerCannotWriteKnowledge(t *testing.T) {
	if HasPermission(RoleViewer, ResourceKnowledge, ActionWrite) {
		t.Error("viewer should not be allowed to write knowledge")
	}
}

func TestHasPermissionViewerCannotUseBuilder(t *testing.T) {
	if HasPermission(RoleViewer, ResourceBuilder, ActionWrite) {
		t.Error("viewer should not be allowed to use builder")
	}
}

func TestHasPermissionAdminCanWriteConfig(t *testing.T) {
	if !HasPermission(RoleAdmin, ResourceConfig, ActionWrite) {
		t.Error("admin should be allowed to write config")
	}
}

func TestHasPermissionOperatorCanReadConfig(t *testing.T) {
	if !HasPermission(RoleOperator, ResourceConfig, ActionRead) {
		t.Error("operator should be allowed to read config")
	}
}

func TestHasPermissionOperatorCannotWriteConfig(t *testing.T) {
	if HasPermission(RoleOperator, ResourceConfig, ActionWrite) {
		t.Error("operator should not be allowed to write config")
	}
}

func TestHasPermissionOperatorCannotWriteProviders(t *testing.T) {
	if HasPermission(RoleOperator, ResourceProviders, ActionWrite) {
		t.Error("operator should not be allowed to write providers")
	}
}

func TestHasPermissionAdminCanWriteProviders(t *testing.T) {
	if !HasPermission(RoleAdmin, ResourceProviders, ActionWrite) {
		t.Error("admin should be allowed to write providers")
	}
}

func TestHasPermissionAdminCanWriteSchedule(t *testing.T) {
	if !HasPermission(RoleAdmin, ResourceSchedule, ActionWrite) {
		t.Error("admin should be allowed to write schedule")
	}
}

func TestHasPermissionViewerCanReadSchedule(t *testing.T) {
	if !HasPermission(RoleViewer, ResourceSchedule, ActionRead) {
		t.Error("viewer should be allowed to read schedule")
	}
}

func TestHasPermissionViewerCannotWriteSchedule(t *testing.T) {
	if HasPermission(RoleViewer, ResourceSchedule, ActionWrite) {
		t.Error("viewer should not be allowed to write schedule")
	}
}

// ---------------------------------------------------------------------------
// CanAccessAgent — explicit deny (row exists but action not listed)
// ---------------------------------------------------------------------------

func TestCanAccessAgentExplicitDeny(t *testing.T) {
	s := newRBACStore(t)
	// Grant only read; chat is explicitly absent.
	if err := s.SetAgentGrant(AgentGrant{
		Role: RoleViewer, AgentID: "bot", Actions: []string{ActionRead},
	}); err != nil {
		t.Fatalf("SetAgentGrant: %v", err)
	}
	// chat should be explicitly denied because a row exists but chat is not listed.
	allowed, err := s.CanAccessAgent(RoleViewer, "bot", ActionChat)
	if err != nil {
		t.Fatalf("CanAccessAgent: %v", err)
	}
	if allowed {
		t.Error("chat should be explicitly denied when row exists but action not listed")
	}
}

// ---------------------------------------------------------------------------
// CanAccessAgent — wildcard agentID queried directly
// ---------------------------------------------------------------------------

func TestCanAccessAgentWildcardSkipsSecondLookup(t *testing.T) {
	s := newRBACStore(t)
	// When querying with agentID=="*" directly, the wildcard branch is skipped.
	// No row stored → falls back to default policy.
	allowed, err := s.CanAccessAgent(RoleAdmin, "*", ActionDelete)
	if err != nil {
		t.Fatalf("CanAccessAgent: %v", err)
	}
	if !allowed {
		t.Error("admin delete via default policy should be allowed when agentID is '*'")
	}
}

// ---------------------------------------------------------------------------
// ListAgentGrantsForRole — empty result for unknown role
// ---------------------------------------------------------------------------

func TestListAgentGrantsForRoleEmpty(t *testing.T) {
	s := newRBACStore(t)
	// No grants stored at all — should return empty slice, not nil error.
	grants, err := s.ListAgentGrantsForRole(RoleViewer)
	if err != nil {
		t.Fatalf("ListAgentGrantsForRole: %v", err)
	}
	if len(grants) != 0 {
		t.Errorf("expected 0 grants, got %d", len(grants))
	}
}

func TestListAgentGrantsForRoleMultipleAgents(t *testing.T) {
	s := newRBACStore(t)
	_ = s.SetAgentGrant(AgentGrant{Role: RoleOperator, AgentID: "ag1", Actions: []string{ActionRead}})
	_ = s.SetAgentGrant(AgentGrant{Role: RoleOperator, AgentID: "ag2", Actions: []string{ActionRead, ActionChat}})
	_ = s.SetAgentGrant(AgentGrant{Role: RoleViewer, AgentID: "ag3", Actions: []string{ActionRead}})

	grants, err := s.ListAgentGrantsForRole(RoleOperator)
	if err != nil {
		t.Fatalf("ListAgentGrantsForRole: %v", err)
	}
	if len(grants) != 2 {
		t.Fatalf("operator grant count = %d, want 2", len(grants))
	}
	// Order is by agent_id; ag1 < ag2.
	if grants[0].AgentID != "ag1" || grants[1].AgentID != "ag2" {
		t.Errorf("unexpected order: %v", grants)
	}
	if len(grants[1].Actions) != 2 {
		t.Errorf("ag2 actions = %v, want [read chat]", grants[1].Actions)
	}
}

// ---------------------------------------------------------------------------
// SQLiteStore — Close
// ---------------------------------------------------------------------------

func TestSQLiteStoreClose(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "close_test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// ---------------------------------------------------------------------------
// NoopStore — ListAgentGrantsForRole
// ---------------------------------------------------------------------------

func TestNoopStoreListAgentGrantsForRole(t *testing.T) {
	var s NoopStore
	grants, err := s.ListAgentGrantsForRole(RoleAdmin)
	if err != nil {
		t.Errorf("ListAgentGrantsForRole: %v", err)
	}
	if len(grants) != 0 {
		t.Errorf("NoopStore ListAgentGrantsForRole: want 0, got %d", len(grants))
	}
}

// ---------------------------------------------------------------------------
// ListAgentGrants — multiple actions, round-trip integrity
// ---------------------------------------------------------------------------

func TestListAgentGrantsActionsRoundTrip(t *testing.T) {
	s := newRBACStore(t)
	wantActions := []string{ActionRead, ActionChat, ActionWrite}
	if err := s.SetAgentGrant(AgentGrant{
		Role: RoleAdmin, AgentID: "trip-agent", Actions: wantActions,
	}); err != nil {
		t.Fatalf("SetAgentGrant: %v", err)
	}
	grants, err := s.ListAgentGrants()
	if err != nil {
		t.Fatalf("ListAgentGrants: %v", err)
	}
	if len(grants) != 1 {
		t.Fatalf("grant count = %d, want 1", len(grants))
	}
	got := grants[0].Actions
	if len(got) != len(wantActions) {
		t.Fatalf("actions = %v, want %v", got, wantActions)
	}
	for i, a := range wantActions {
		if got[i] != a {
			t.Errorf("actions[%d] = %q, want %q", i, got[i], a)
		}
	}
}

// ---------------------------------------------------------------------------
// CanAccessAgent — default deny for viewer on agent write
// ---------------------------------------------------------------------------

func TestCanAccessAgentDefaultDenyViewerWrite(t *testing.T) {
	s := newRBACStore(t)
	// No grant row; falls back to default policy which denies viewer write.
	allowed, err := s.CanAccessAgent(RoleViewer, "some-agent", ActionWrite)
	if err != nil {
		t.Fatalf("CanAccessAgent: %v", err)
	}
	if allowed {
		t.Error("viewer should not be able to write an agent by default")
	}
}

// ---------------------------------------------------------------------------
// HasPermission — exhaustive matrix for untested resource/action combos
// ---------------------------------------------------------------------------

func TestHasPermissionAdminMemoryReadAndDelete(t *testing.T) {
	if !HasPermission(RoleAdmin, ResourceMemory, ActionRead) {
		t.Error("admin should read memory")
	}
	if !HasPermission(RoleAdmin, ResourceMemory, ActionDelete) {
		t.Error("admin should delete memory")
	}
}

func TestHasPermissionOperatorMemoryReadAndDelete(t *testing.T) {
	if !HasPermission(RoleOperator, ResourceMemory, ActionRead) {
		t.Error("operator should read memory")
	}
	if !HasPermission(RoleOperator, ResourceMemory, ActionDelete) {
		t.Error("operator should delete memory")
	}
}

func TestHasPermissionViewerMemoryRead(t *testing.T) {
	if !HasPermission(RoleViewer, ResourceMemory, ActionRead) {
		t.Error("viewer should read memory")
	}
	if HasPermission(RoleViewer, ResourceMemory, ActionDelete) {
		t.Error("viewer should not delete memory")
	}
}

func TestHasPermissionAdminMCPFullAccess(t *testing.T) {
	for _, action := range []string{ActionRead, ActionWrite, ActionDelete} {
		if !HasPermission(RoleAdmin, ResourceMCP, action) {
			t.Errorf("admin mcp %q: want true", action)
		}
	}
}

func TestOnlyWorkspaceAdminsMayWriteMCPDefinitions(t *testing.T) {
	if !HasPermission(RoleOperator, ResourceMCP, ActionRead) {
		t.Error("operator should read mcp")
	}
	if HasPermission(RoleOperator, ResourceMCP, ActionWrite) {
		t.Error("operator should not write executable MCP definitions")
	}
	if HasPermission(RoleOperator, ResourceMCP, ActionDelete) {
		t.Error("operator should not delete mcp")
	}
	if HasPermission(RoleDeveloper, ResourceMCP, ActionWrite) || HasPermission(RoleDeveloper, ResourceMCP, ActionDelete) {
		t.Error("developer should not administer executable MCP definitions")
	}
}

func TestHasPermissionViewerMCPRead(t *testing.T) {
	if !HasPermission(RoleViewer, ResourceMCP, ActionRead) {
		t.Error("viewer should read mcp")
	}
	if HasPermission(RoleViewer, ResourceMCP, ActionWrite) {
		t.Error("viewer should not write mcp")
	}
}

func TestHasPermissionAdminKnowledgeFullAccess(t *testing.T) {
	for _, action := range []string{ActionRead, ActionWrite, ActionDelete} {
		if !HasPermission(RoleAdmin, ResourceKnowledge, action) {
			t.Errorf("admin knowledge %q: want true", action)
		}
	}
}

func TestHasPermissionOperatorKnowledgeReadAndWrite(t *testing.T) {
	if !HasPermission(RoleOperator, ResourceKnowledge, ActionRead) {
		t.Error("operator should read knowledge")
	}
	if !HasPermission(RoleOperator, ResourceKnowledge, ActionWrite) {
		t.Error("operator should write knowledge")
	}
	if HasPermission(RoleOperator, ResourceKnowledge, ActionDelete) {
		t.Error("operator should not delete knowledge")
	}
}

func TestHasPermissionViewerKnowledgeRead(t *testing.T) {
	if !HasPermission(RoleViewer, ResourceKnowledge, ActionRead) {
		t.Error("viewer should read knowledge")
	}
}

func TestHasPermissionAdminBuilderWrite(t *testing.T) {
	if !HasPermission(RoleAdmin, ResourceBuilder, ActionWrite) {
		t.Error("admin should use builder")
	}
}

func TestHasPermissionOperatorCannotUseBuilder(t *testing.T) {
	if HasPermission(RoleOperator, ResourceBuilder, ActionWrite) {
		t.Error("operator should not use Studio builder")
	}
}

func TestHasPermissionAdminTemplatesReadAndWrite(t *testing.T) {
	if !HasPermission(RoleAdmin, ResourceTemplates, ActionRead) {
		t.Error("admin should read templates")
	}
	if !HasPermission(RoleAdmin, ResourceTemplates, ActionWrite) {
		t.Error("admin should write templates")
	}
}

func TestHasPermissionOperatorTemplatesReadOnly(t *testing.T) {
	if !HasPermission(RoleOperator, ResourceTemplates, ActionRead) {
		t.Error("operator should read templates")
	}
	if HasPermission(RoleOperator, ResourceTemplates, ActionWrite) {
		t.Error("operator should not author or change templates")
	}
}

func TestDeveloperAndOperatorResponsibilitiesStaySeparated(t *testing.T) {
	tests := []struct {
		name     string
		role     string
		resource string
		action   string
		want     bool
	}{
		{"developer authors agents", RoleDeveloper, ResourceAgents, ActionWrite, true},
		{"developer uses Studio", RoleDeveloper, ResourceBuilder, ActionWrite, true},
		{"developer selects approved provider models", RoleDeveloper, ResourceProviders, ActionSet, true},
		{"developer cannot administer providers", RoleDeveloper, ResourceProviders, ActionWrite, false},
		{"developer cannot approve production actions", RoleDeveloper, ResourceApprovals, ActionWrite, false},
		{"developer cannot enable delivery channels", RoleDeveloper, ResourceChannels, ActionEnable, false},
		{"operator cannot author agents", RoleOperator, ResourceAgents, ActionWrite, false},
		{"operator cannot use Studio", RoleOperator, ResourceBuilder, ActionWrite, false},
		{"operator cannot author templates", RoleOperator, ResourceTemplates, ActionWrite, false},
		{"operator can chat with agents", RoleOperator, ResourceChat, ActionChat, true},
		{"operator can enable deployed agents", RoleOperator, ResourceAgents, ActionEnable, true},
		{"operator can approve production actions", RoleOperator, ResourceApprovals, ActionWrite, true},
		{"operator can enable delivery channels", RoleOperator, ResourceChannels, ActionEnable, true},
		{"operator can manage schedules", RoleOperator, ResourceSchedule, ActionWrite, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HasPermission(tt.role, tt.resource, tt.action); got != tt.want {
				t.Fatalf("HasPermission(%q, %q, %q) = %v, want %v", tt.role, tt.resource, tt.action, got, tt.want)
			}
		})
	}
}

func TestHasPermissionViewerTemplatesRead(t *testing.T) {
	if !HasPermission(RoleViewer, ResourceTemplates, ActionRead) {
		t.Error("viewer should read templates")
	}
	if HasPermission(RoleViewer, ResourceTemplates, ActionWrite) {
		t.Error("viewer should not write templates")
	}
}

func TestHasPermissionAdminLogsAndMetricsRead(t *testing.T) {
	if !HasPermission(RoleAdmin, ResourceLogs, ActionRead) {
		t.Error("admin should read logs")
	}
	if !HasPermission(RoleAdmin, ResourceMetrics, ActionRead) {
		t.Error("admin should read metrics")
	}
}

func TestHasPermissionOperatorLogsRead(t *testing.T) {
	if !HasPermission(RoleOperator, ResourceLogs, ActionRead) {
		t.Error("operator should read logs")
	}
}

func TestHasPermissionViewerLogsRead(t *testing.T) {
	if !HasPermission(RoleViewer, ResourceLogs, ActionRead) {
		t.Error("viewer should read logs")
	}
}

func TestHasPermissionViewerCannotRunAgents(t *testing.T) {
	if HasPermission(RoleViewer, ResourceChat, ActionChat) {
		t.Error("viewer should not be able to submit prompts or run agents")
	}
	if !HasPermission(RoleViewer, ResourceChat, ActionRead) {
		t.Error("viewer should retain read access to existing conversations")
	}
}

func TestHasPermissionOperatorCanChat(t *testing.T) {
	if !HasPermission(RoleOperator, ResourceChat, ActionChat) {
		t.Error("operator should be able to chat")
	}
}

func TestHasPermissionAdminChannelsEnableAndWrite(t *testing.T) {
	if !HasPermission(RoleAdmin, ResourceChannels, ActionEnable) {
		t.Error("admin should enable channels")
	}
	if !HasPermission(RoleAdmin, ResourceChannels, ActionWrite) {
		t.Error("admin should write channels")
	}
}

func TestHasPermissionViewerChannelsRead(t *testing.T) {
	if !HasPermission(RoleViewer, ResourceChannels, ActionRead) {
		t.Error("viewer should read channels")
	}
	if HasPermission(RoleViewer, ResourceChannels, ActionEnable) {
		t.Error("viewer should not enable channels")
	}
}

func TestHasPermissionAdminSkillsRead(t *testing.T) {
	if !HasPermission(RoleAdmin, ResourceSkills, ActionRead) {
		t.Error("admin should read skills")
	}
}

func TestHasPermissionViewerSkillsRead(t *testing.T) {
	if !HasPermission(RoleViewer, ResourceSkills, ActionRead) {
		t.Error("viewer should read skills")
	}
	if HasPermission(RoleViewer, ResourceSkills, ActionWrite) {
		t.Error("viewer should not write skills")
	}
}

func TestHasPermissionAdminProvidersReadAndWrite(t *testing.T) {
	if !HasPermission(RoleAdmin, ResourceProviders, ActionRead) {
		t.Error("admin should read providers")
	}
}

func TestHasPermissionViewerProvidersRead(t *testing.T) {
	if !HasPermission(RoleViewer, ResourceProviders, ActionRead) {
		t.Error("viewer should read providers")
	}
	if HasPermission(RoleViewer, ResourceProviders, ActionWrite) {
		t.Error("viewer should not write providers")
	}
}

func TestHasPermissionAdminScheduleReadAndWrite(t *testing.T) {
	if !HasPermission(RoleAdmin, ResourceSchedule, ActionRead) {
		t.Error("admin should read schedule")
	}
}

func TestHasPermissionOperatorScheduleReadAndWrite(t *testing.T) {
	if !HasPermission(RoleOperator, ResourceSchedule, ActionRead) {
		t.Error("operator should read schedule")
	}
	if !HasPermission(RoleOperator, ResourceSchedule, ActionWrite) {
		t.Error("operator should write schedule")
	}
}

func TestHasPermissionAdminRBACReadWriteDelete(t *testing.T) {
	if !HasPermission(RoleAdmin, ResourceRBAC, ActionRead) {
		t.Error("admin should read rbac")
	}
	if !HasPermission(RoleAdmin, ResourceRBAC, ActionWrite) {
		t.Error("admin should write rbac")
	}
}

func TestHasPermissionViewerNoRBACAccess(t *testing.T) {
	for _, action := range []string{ActionRead, ActionWrite, ActionDelete} {
		if HasPermission(RoleViewer, ResourceRBAC, action) {
			t.Errorf("viewer should have no rbac/%s access", action)
		}
	}
}

func TestHasPermissionViewerNoMetricsAccess(t *testing.T) {
	if HasPermission(RoleViewer, ResourceMetrics, ActionRead) {
		t.Error("viewer should have no metrics access")
	}
}

func TestHasPermissionViewerNoConfigWrite(t *testing.T) {
	if HasPermission(RoleViewer, ResourceConfig, ActionWrite) {
		t.Error("viewer should not write config")
	}
}

// ---------------------------------------------------------------------------
// CanAccessAgent — wildcard overridden by exact deny
// ---------------------------------------------------------------------------

// TestCanAccessAgentExactGrantOverridesWildcard confirms that when both a
// wildcard row and an exact row exist, the exact row is consulted first.
// If the exact row does not list the action, access is denied even if the
// wildcard would permit it.
func TestCanAccessAgentExactGrantOverridesWildcard(t *testing.T) {
	s := newRBACStore(t)

	// Wildcard grants read to all agents.
	_ = s.SetAgentGrant(AgentGrant{Role: RoleOperator, AgentID: "*", Actions: []string{ActionRead}})
	// Exact row for "restricted-agent" grants only enable — no read.
	_ = s.SetAgentGrant(AgentGrant{Role: RoleOperator, AgentID: "restricted-agent", Actions: []string{ActionEnable}})

	// Exact row found first → read is denied because it is not in the row.
	allowed, err := s.CanAccessAgent(RoleOperator, "restricted-agent", ActionRead)
	if err != nil {
		t.Fatalf("CanAccessAgent: %v", err)
	}
	if allowed {
		t.Error("exact deny should override wildcard allow")
	}

	// Enable is in the exact row and in the role policy → allowed.
	allowed, err = s.CanAccessAgent(RoleOperator, "restricted-agent", ActionEnable)
	if err != nil {
		t.Fatalf("CanAccessAgent enable: %v", err)
	}
	if !allowed {
		t.Error("enable should be allowed via exact grant")
	}
}

// ---------------------------------------------------------------------------
// CanAccessAgent — wildcard row consulted when no exact row
// ---------------------------------------------------------------------------

func TestCanAccessAgentWildcardActionDeny(t *testing.T) {
	s := newRBACStore(t)
	// Wildcard grants only read — delete is not listed.
	_ = s.SetAgentGrant(AgentGrant{Role: RoleOperator, AgentID: "*", Actions: []string{ActionRead}})

	allowed, err := s.CanAccessAgent(RoleOperator, "any-agent", ActionDelete)
	if err != nil {
		t.Fatalf("CanAccessAgent: %v", err)
	}
	if allowed {
		t.Error("wildcard row exists but does not list delete — should be denied")
	}
}

// ---------------------------------------------------------------------------
// SetAgentGrant — update (upsert) changes actions stored in DB
// ---------------------------------------------------------------------------

func TestSetAgentGrantUpdateReflectedInList(t *testing.T) {
	s := newRBACStore(t)
	_ = s.SetAgentGrant(AgentGrant{Role: RoleAdmin, AgentID: "ag", Actions: []string{ActionRead}})
	// Upsert: replace with write-only.
	if err := s.SetAgentGrant(AgentGrant{Role: RoleAdmin, AgentID: "ag", Actions: []string{ActionWrite}}); err != nil {
		t.Fatalf("SetAgentGrant update: %v", err)
	}

	grants, err := s.ListAgentGrantsForRole(RoleAdmin)
	if err != nil {
		t.Fatalf("ListAgentGrantsForRole: %v", err)
	}
	if len(grants) != 1 {
		t.Fatalf("grant count = %d, want 1", len(grants))
	}
	if len(grants[0].Actions) != 1 || grants[0].Actions[0] != ActionWrite {
		t.Errorf("actions after update = %v, want [write]", grants[0].Actions)
	}
}

// ---------------------------------------------------------------------------
// SQLiteStore — empty actions string round-trips cleanly
// ---------------------------------------------------------------------------

func TestListAgentGrantsEmptyActionsRow(t *testing.T) {
	s := newRBACStore(t)
	// Insert a row where actions is "" (edge case from manual DB edits).
	_, _ = s.db.Exec(
		`INSERT INTO rbac_agent_grants (role, agent_id, actions) VALUES (?, ?, ?)`,
		RoleViewer, "empty-act", "",
	)
	grants, err := s.ListAgentGrants()
	if err != nil {
		t.Fatalf("ListAgentGrants: %v", err)
	}
	// Row should exist but actions slice should be empty (no empty string token).
	if len(grants) != 1 {
		t.Fatalf("grant count = %d, want 1", len(grants))
	}
	if len(grants[0].Actions) != 0 {
		t.Errorf("actions = %v, want []", grants[0].Actions)
	}
}

// ---------------------------------------------------------------------------
// IsKnownRole — all three roles individually
// ---------------------------------------------------------------------------

func TestIsKnownRoleAdmin(t *testing.T) {
	if !IsKnownRole(RoleAdmin) {
		t.Error("RoleAdmin should be known")
	}
}

func TestIsKnownRoleOperator(t *testing.T) {
	if !IsKnownRole(RoleOperator) {
		t.Error("RoleOperator should be known")
	}
}

func TestIsKnownRoleViewer(t *testing.T) {
	if !IsKnownRole(RoleViewer) {
		t.Error("RoleViewer should be known")
	}
}

// ---------------------------------------------------------------------------
// KnownRoles slice correctness
// ---------------------------------------------------------------------------

func TestKnownRolesContainsEveryWorkspaceRole(t *testing.T) {
	want := map[string]bool{RoleOwner: false, RoleAdmin: false, RoleDeveloper: false, RoleOperator: false, RoleViewer: false, RoleDemoDeveloper: false}
	for _, r := range KnownRoles {
		if _, ok := want[r]; !ok {
			t.Errorf("unexpected role in KnownRoles: %q", r)
		}
		want[r] = true
	}
	for r, seen := range want {
		if !seen {
			t.Errorf("KnownRoles missing %q", r)
		}
	}
}

// MU-017 criterion 3: installation permission is separate from extension-use
// permission.
//
// Using an extension runs code somebody already vetted. Installing one chooses
// whose code runs — and a developer who may author a local skill has not
// thereby been trusted to pull an arbitrary package off the internet into
// everyone else's runtime. Before ActionInstall existed, both were
// ActionWrite, so the two were the same permission.
func TestInstallingAnExtensionIsASeparatePermissionFromUsingOne(t *testing.T) {
	for _, resource := range []string{ResourceSkills, ResourceMCP} {
		for _, role := range []string{RoleOwner, RoleAdmin} {
			if !HasPermission(role, resource, ActionInstall) {
				t.Errorf("%s cannot install %s", role, resource)
			}
		}
		for _, role := range []string{RoleDeveloper, RoleOperator, RoleViewer} {
			if HasPermission(role, resource, ActionInstall) {
				t.Errorf("%s may install %s — installation must not follow from write or read", role, resource)
			}
		}
		// The separation is only meaningful if the lesser roles still have the
		// permissions they had. A developer keeps authoring; everyone keeps
		// reading. Otherwise this is a permission removal wearing a split's
		// clothes.
		if !HasPermission(RoleDeveloper, resource, ActionRead) {
			t.Errorf("developer lost read on %s", resource)
		}
		if !HasPermission(RoleViewer, resource, ActionRead) {
			t.Errorf("viewer lost read on %s", resource)
		}
	}
	if !HasPermission(RoleDeveloper, ResourceSkills, ActionWrite) {
		t.Error("developer lost the ability to author a local skill")
	}
	if HasPermission(RoleDeveloper, ResourceMCP, ActionWrite) {
		t.Error("developer gained the ability to configure an executable MCP server")
	}
}

func TestDeveloperCanRequestButNotInstallMCP(t *testing.T) {
	if !HasPermission(RoleDeveloper, ResourceMCP, ActionRequest) {
		t.Fatal("developer cannot request an MCP server")
	}
	if HasPermission(RoleDeveloper, ResourceMCP, ActionInstall) {
		t.Fatal("developer request permission escalated to install permission")
	}
	for _, role := range []string{RoleOwner, RoleAdmin} {
		if !HasPermission(role, ResourceMCP, ActionRequest) || !HasPermission(role, ResourceMCP, ActionInstall) {
			t.Fatalf("%s must be able to request and install MCP servers", role)
		}
	}
	for _, role := range []string{RoleOperator, RoleViewer} {
		if HasPermission(role, ResourceMCP, ActionRequest) {
			t.Fatalf("%s unexpectedly may request MCP installation", role)
		}
	}
}

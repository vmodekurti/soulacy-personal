// middleware_test.go — tests for Manager, Require, RequireAgent, and
// the admin HTTP handlers in middleware.go.
package rbac

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/auth"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// newManager builds a Manager backed by NoopStore and a no-op logger.
func newManager(t *testing.T) *Manager {
	t.Helper()
	return NewManager(NoopStore{}, zap.NewNop())
}

// newManagerWithStore builds a Manager backed by the provided store.
func newManagerWithStore(t *testing.T, s Store) *Manager {
	t.Helper()
	return NewManager(s, zap.NewNop())
}

// newSQLiteManager creates a Manager backed by a fresh SQLite store in a temp dir.
func newSQLiteManager(t *testing.T) (*Manager, *SQLiteStore) {
	t.Helper()
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "rbac.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return newManagerWithStore(t, s), s
}

// claimsMiddleware returns a Fiber handler that injects claims before the
// next handler runs, simulating what the auth middleware does in production.
func claimsMiddleware(cl *auth.Claims) fiber.Handler {
	return func(c *fiber.Ctx) error {
		auth.SetClaims(c, cl)
		return c.Next()
	}
}

// adminClaims returns Claims for a user with admin role.
func adminClaims() *auth.Claims {
	return &auth.Claims{Role: RoleAdmin, Kind: "access"}
}

// operatorClaims returns Claims for a user with operator role.
func operatorClaims() *auth.Claims {
	return &auth.Claims{Role: RoleOperator, Kind: "access"}
}

// viewerClaims returns Claims for a user with viewer role.
func viewerClaims() *auth.Claims {
	return &auth.Claims{Role: RoleViewer, Kind: "access"}
}

// doRequest sends a request to a Fiber app and returns the response.
func doRequest(t *testing.T, app *fiber.App, method, path, body string) *http.Response {
	t.Helper()
	var reqBody io.Reader
	if body != "" {
		reqBody = bytes.NewBufferString(body)
	}
	req, err := http.NewRequest(method, path, reqBody)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := app.Test(req, 2000)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	return resp
}

// readBody reads and returns the full response body as a string.
func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	return string(b)
}

// decodeJSON parses the response body as a JSON map.
func decodeJSON(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("json.Unmarshal(%q): %v", string(b), err)
	}
	return m
}

// ---------------------------------------------------------------------------
// errStore — a Store that returns an error from CanAccessAgent for testing
// the 500 error path in RequireAgent.
// ---------------------------------------------------------------------------

type errStore struct {
	NoopStore
}

func (errStore) CanAccessAgent(_, _, _ string) (bool, error) {
	return false, fmt.Errorf("simulated store failure")
}

// ---------------------------------------------------------------------------
// NewManager
// ---------------------------------------------------------------------------

func TestNewManagerReturnsNonNil(t *testing.T) {
	m := NewManager(NoopStore{}, zap.NewNop())
	if m == nil {
		t.Fatal("NewManager returned nil")
	}
}

func TestNewManagerStoresFields(t *testing.T) {
	store := NoopStore{}
	log := zap.NewNop()
	m := NewManager(store, log)
	if m.store == nil {
		t.Error("Manager.store is nil after NewManager")
	}
	if m.log == nil {
		t.Error("Manager.log is nil after NewManager")
	}
}

// ---------------------------------------------------------------------------
// Require middleware
// ---------------------------------------------------------------------------

// setupRequireApp builds a Fiber app with an optional claims injector upstream
// of the Require middleware, followed by a terminal 200 handler.
func setupRequireApp(m *Manager, cl *auth.Claims, resource, action string) *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	if cl != nil {
		app.Use(claimsMiddleware(cl))
	}
	app.Use(m.Require(resource, action))
	app.Get("/test", func(c *fiber.Ctx) error {
		return c.SendStatus(fiber.StatusOK)
	})
	return app
}

func TestRequireNoClaims_Allows(t *testing.T) {
	m := newManager(t)
	app := setupRequireApp(m, nil, ResourceAgents, ActionRead)

	resp := doRequest(t, app, http.MethodGet, "/test", "")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestRequireClaimsAllowed_Allows(t *testing.T) {
	m := newManager(t)
	// admin has read on agents
	app := setupRequireApp(m, adminClaims(), ResourceAgents, ActionRead)

	resp := doRequest(t, app, http.MethodGet, "/test", "")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestRequireClaimsDenied_Returns403(t *testing.T) {
	m := newManager(t)
	// viewer does not have delete on agents
	app := setupRequireApp(m, viewerClaims(), ResourceAgents, ActionDelete)

	resp := doRequest(t, app, http.MethodGet, "/test", "")
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
	body := decodeJSON(t, resp)
	if body["error"] != "insufficient permissions" {
		t.Errorf("error field = %q, want 'insufficient permissions'", body["error"])
	}
}

func TestRequireClaimsDenied_BodyContainsRole(t *testing.T) {
	m := newManager(t)
	app := setupRequireApp(m, viewerClaims(), ResourceRBAC, ActionWrite)

	resp := doRequest(t, app, http.MethodGet, "/test", "")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	body := decodeJSON(t, resp)
	if body["role"] != RoleViewer {
		t.Errorf("role field = %q, want %q", body["role"], RoleViewer)
	}
	want := ResourceRBAC + ":" + ActionWrite
	if body["required"] != want {
		t.Errorf("required field = %q, want %q", body["required"], want)
	}
}

func TestRequireOperatorAllowed_Write(t *testing.T) {
	m := newManager(t)
	app := setupRequireApp(m, operatorClaims(), ResourceAgents, ActionWrite)

	resp := doRequest(t, app, http.MethodGet, "/test", "")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("operator write agents: status = %d, want 200", resp.StatusCode)
	}
}

func TestRequireOperatorDenied_Delete(t *testing.T) {
	m := newManager(t)
	app := setupRequireApp(m, operatorClaims(), ResourceAgents, ActionDelete)

	resp := doRequest(t, app, http.MethodGet, "/test", "")
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("operator delete agents: status = %d, want 403", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// RequireAgent middleware
// ---------------------------------------------------------------------------

// setupRequireAgentApp sets up a Fiber app with a route param ":id" so that
// RequireAgent can extract it.
func setupRequireAgentApp(m *Manager, cl *auth.Claims, action string) *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	if cl != nil {
		app.Use(claimsMiddleware(cl))
	}
	app.Use(m.RequireAgent("id", action))
	// Both a parameterised and a param-less route:
	app.Get("/agents/:id", func(c *fiber.Ctx) error {
		return c.SendStatus(fiber.StatusOK)
	})
	app.Get("/agents", func(c *fiber.Ctx) error {
		return c.SendStatus(fiber.StatusOK)
	})
	return app
}

func TestRequireAgentNoClaims_Allows(t *testing.T) {
	m := newManager(t)
	app := setupRequireAgentApp(m, nil, ActionRead)

	resp := doRequest(t, app, http.MethodGet, "/agents/abc-123", "")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("no claims: status = %d, want 200", resp.StatusCode)
	}
}

func TestRequireAgentNoParam_FallsBackToResourceCheck_Allow(t *testing.T) {
	m := newManager(t)
	// admin can read agents → the resource-level fallback should allow
	app := setupRequireAgentApp(m, adminClaims(), ActionRead)

	resp := doRequest(t, app, http.MethodGet, "/agents", "")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("no param fallback allow: status = %d, want 200", resp.StatusCode)
	}
}

func TestRequireAgentNoParam_FallsBackToResourceCheck_Deny(t *testing.T) {
	m := newManager(t)
	// viewer cannot delete agents → resource-level fallback should deny
	app := setupRequireAgentApp(m, viewerClaims(), ActionDelete)

	resp := doRequest(t, app, http.MethodGet, "/agents", "")
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("no param fallback deny: status = %d, want 403", resp.StatusCode)
	}
}

func TestRequireAgentWithParam_StoreAllows(t *testing.T) {
	m, store := newSQLiteManager(t)
	// Grant the operator read on agent "ag-1".
	if err := store.SetAgentGrant(AgentGrant{
		Role: RoleOperator, AgentID: "ag-1", Actions: []string{ActionRead},
	}); err != nil {
		t.Fatalf("SetAgentGrant: %v", err)
	}
	app := setupRequireAgentApp(m, operatorClaims(), ActionRead)

	resp := doRequest(t, app, http.MethodGet, "/agents/ag-1", "")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("store allows: status = %d, want 200", resp.StatusCode)
	}
}

func TestRequireAgentWithParam_StoreDenies(t *testing.T) {
	m, store := newSQLiteManager(t)
	// Grant the operator only read — delete is denied.
	if err := store.SetAgentGrant(AgentGrant{
		Role: RoleOperator, AgentID: "ag-1", Actions: []string{ActionRead},
	}); err != nil {
		t.Fatalf("SetAgentGrant: %v", err)
	}
	app := setupRequireAgentApp(m, operatorClaims(), ActionDelete)

	resp := doRequest(t, app, http.MethodGet, "/agents/ag-1", "")
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("store denies: status = %d, want 403", resp.StatusCode)
	}
}

func TestRequireAgentWithParam_StoreError_Returns500(t *testing.T) {
	// Must register the middleware at the route level (not app.Use) so that
	// c.Params("id") is populated when RequireAgent runs.
	m := newManagerWithStore(t, errStore{})
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(claimsMiddleware(adminClaims()))
	app.Get("/agents/:id", m.RequireAgent("id", ActionRead), func(c *fiber.Ctx) error {
		return c.SendStatus(fiber.StatusOK)
	})

	resp := doRequest(t, app, http.MethodGet, "/agents/any-id", "")
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("store error: status = %d, want 500", resp.StatusCode)
	}
	body := decodeJSON(t, resp)
	if body["error"] != "rbac store error" {
		t.Errorf("error field = %q, want 'rbac store error'", body["error"])
	}
}

// ---------------------------------------------------------------------------
// HandleListGrants
// ---------------------------------------------------------------------------

func setupListGrantsApp(m *Manager) *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Get("/rbac/grants", m.HandleListGrants)
	return app
}

func TestHandleListGrants_EmptyStore(t *testing.T) {
	m := newManager(t) // NoopStore returns nil → handler normalises to []
	app := setupListGrantsApp(m)

	resp := doRequest(t, app, http.MethodGet, "/rbac/grants", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body := decodeJSON(t, resp)

	// grants should be an empty array, not null
	grants, ok := body["grants"]
	if !ok {
		t.Fatal("response missing 'grants' key")
	}
	arr, ok := grants.([]any)
	if !ok {
		t.Fatalf("grants is %T, want []any", grants)
	}
	if len(arr) != 0 {
		t.Errorf("grants len = %d, want 0", len(arr))
	}
	if body["count"] != float64(0) {
		t.Errorf("count = %v, want 0", body["count"])
	}
}

func TestHandleListGrants_WithData(t *testing.T) {
	m, store := newSQLiteManager(t)
	_ = store.SetAgentGrant(AgentGrant{Role: RoleAdmin, AgentID: "agent-a", Actions: []string{ActionRead}})
	_ = store.SetAgentGrant(AgentGrant{Role: RoleOperator, AgentID: "agent-b", Actions: []string{ActionRead, ActionChat}})

	app := setupListGrantsApp(m)
	resp := doRequest(t, app, http.MethodGet, "/rbac/grants", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body := decodeJSON(t, resp)
	arr, ok := body["grants"].([]any)
	if !ok {
		t.Fatalf("grants is %T, want []any", body["grants"])
	}
	if len(arr) != 2 {
		t.Errorf("grants len = %d, want 2", len(arr))
	}
	if body["count"] != float64(2) {
		t.Errorf("count = %v, want 2", body["count"])
	}
}

// ---------------------------------------------------------------------------
// HandleListGrantsForRole
// ---------------------------------------------------------------------------

func setupListGrantsForRoleApp(m *Manager) *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Get("/rbac/grants/:role", m.HandleListGrantsForRole)
	return app
}

func TestHandleListGrantsForRole_UnknownRole_Returns400(t *testing.T) {
	m := newManager(t)
	app := setupListGrantsForRoleApp(m)

	resp := doRequest(t, app, http.MethodGet, "/rbac/grants/superuser", "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	body := decodeJSON(t, resp)
	if body["error"] == nil || body["error"] == "" {
		t.Errorf("expected non-empty error field, got: %v", body["error"])
	}
}

func TestHandleListGrantsForRole_ValidRole_NoGrants(t *testing.T) {
	m := newManager(t) // NoopStore returns nil → handler normalises to []
	app := setupListGrantsForRoleApp(m)

	resp := doRequest(t, app, http.MethodGet, "/rbac/grants/admin", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body := decodeJSON(t, resp)
	arr, ok := body["grants"].([]any)
	if !ok {
		t.Fatalf("grants is %T, want []any", body["grants"])
	}
	if len(arr) != 0 {
		t.Errorf("grants len = %d, want 0", len(arr))
	}
}

func TestHandleListGrantsForRole_ValidRole_WithGrants(t *testing.T) {
	m, store := newSQLiteManager(t)
	_ = store.SetAgentGrant(AgentGrant{Role: RoleOperator, AgentID: "ag1", Actions: []string{ActionRead}})
	_ = store.SetAgentGrant(AgentGrant{Role: RoleViewer, AgentID: "ag2", Actions: []string{ActionRead}})

	app := setupListGrantsForRoleApp(m)
	resp := doRequest(t, app, http.MethodGet, "/rbac/grants/operator", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body := decodeJSON(t, resp)
	arr, ok := body["grants"].([]any)
	if !ok {
		t.Fatalf("grants is %T, want []any", body["grants"])
	}
	if len(arr) != 1 {
		t.Errorf("grants len = %d, want 1 (only operator grants)", len(arr))
	}
	if body["count"] != float64(1) {
		t.Errorf("count = %v, want 1", body["count"])
	}
}

func TestHandleListGrantsForRole_AllThreeRoles(t *testing.T) {
	m := newManager(t)
	app := setupListGrantsForRoleApp(m)

	for _, role := range []string{RoleAdmin, RoleOperator, RoleViewer} {
		resp := doRequest(t, app, http.MethodGet, "/rbac/grants/"+role, "")
		if resp.StatusCode != http.StatusOK {
			t.Errorf("role %q: status = %d, want 200", role, resp.StatusCode)
		}
		_ = resp.Body.Close()
	}
}

// ---------------------------------------------------------------------------
// HandleSetAgentGrant
// ---------------------------------------------------------------------------

func setupSetAgentGrantApp(m *Manager) *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Put("/rbac/grants/:role/:agent_id", m.HandleSetAgentGrant)
	return app
}

func TestHandleSetAgentGrant_UnknownRole_Returns400(t *testing.T) {
	m := newManager(t)
	app := setupSetAgentGrantApp(m)

	body := `{"actions":["read"]}`
	resp := doRequest(t, app, http.MethodPut, "/rbac/grants/hacker/agent-x", body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestHandleSetAgentGrant_MissingActions_Returns400(t *testing.T) {
	m, _ := newSQLiteManager(t)
	app := setupSetAgentGrantApp(m)

	body := `{"actions":[]}`
	resp := doRequest(t, app, http.MethodPut, "/rbac/grants/admin/agent-x", body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	respBody := decodeJSON(t, resp)
	if respBody["error"] != "actions must not be empty" {
		t.Errorf("error = %q, want 'actions must not be empty'", respBody["error"])
	}
}

func TestHandleSetAgentGrant_InvalidJSON_Returns400(t *testing.T) {
	m, _ := newSQLiteManager(t)
	app := setupSetAgentGrantApp(m)

	resp := doRequest(t, app, http.MethodPut, "/rbac/grants/admin/agent-x", `{not-json}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestHandleSetAgentGrant_HappyPath(t *testing.T) {
	m, _ := newSQLiteManager(t)
	app := setupSetAgentGrantApp(m)

	body := `{"actions":["read","chat"]}`
	resp := doRequest(t, app, http.MethodPut, "/rbac/grants/operator/ag-42", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, readBody(t, resp))
	}
	respBody := decodeJSON(t, resp)
	if respBody["role"] != RoleOperator {
		t.Errorf("role = %q, want %q", respBody["role"], RoleOperator)
	}
	if respBody["agent_id"] != "ag-42" {
		t.Errorf("agent_id = %q, want 'ag-42'", respBody["agent_id"])
	}
	actions, ok := respBody["actions"].([]any)
	if !ok {
		t.Fatalf("actions is %T, want []any", respBody["actions"])
	}
	if len(actions) != 2 {
		t.Errorf("actions len = %d, want 2", len(actions))
	}
}

func TestHandleSetAgentGrant_NoBody_Returns400(t *testing.T) {
	m, _ := newSQLiteManager(t)
	app := setupSetAgentGrantApp(m)

	// Send no body at all — BodyParser receives empty input.
	req, _ := http.NewRequest(http.MethodPut, "/rbac/grants/admin/agent-x", nil)
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req, 2000)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	// Empty body parses to empty Actions slice → 400
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// HandleDeleteAgentGrant
// ---------------------------------------------------------------------------

func setupDeleteAgentGrantApp(m *Manager) *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Delete("/rbac/grants/:role/:agent_id", m.HandleDeleteAgentGrant)
	return app
}

func TestHandleDeleteAgentGrant_HappyPath(t *testing.T) {
	m, store := newSQLiteManager(t)
	_ = store.SetAgentGrant(AgentGrant{Role: RoleAdmin, AgentID: "tgt", Actions: []string{ActionRead}})

	app := setupDeleteAgentGrantApp(m)
	resp := doRequest(t, app, http.MethodDelete, "/rbac/grants/admin/tgt", "")
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want 204", resp.StatusCode)
	}
}

func TestHandleDeleteAgentGrant_NonExistent_StillReturns204(t *testing.T) {
	m, _ := newSQLiteManager(t)
	app := setupDeleteAgentGrantApp(m)

	resp := doRequest(t, app, http.MethodDelete, "/rbac/grants/admin/ghost", "")
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want 204 (idempotent delete)", resp.StatusCode)
	}
}

func TestHandleDeleteAgentGrant_RemovesFromStore(t *testing.T) {
	m, store := newSQLiteManager(t)
	_ = store.SetAgentGrant(AgentGrant{Role: RoleOperator, AgentID: "del-me", Actions: []string{ActionRead}})

	app := setupDeleteAgentGrantApp(m)
	resp := doRequest(t, app, http.MethodDelete, "/rbac/grants/operator/del-me", "")
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d", resp.StatusCode)
	}

	grants, err := store.ListAgentGrantsForRole(RoleOperator)
	if err != nil {
		t.Fatalf("ListAgentGrantsForRole: %v", err)
	}
	if len(grants) != 0 {
		t.Errorf("expected 0 grants after delete, got %d", len(grants))
	}
}

// ---------------------------------------------------------------------------
// HandleListPolicy
// ---------------------------------------------------------------------------

func setupListPolicyApp(m *Manager) *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Get("/rbac/policy", m.HandleListPolicy)
	return app
}

func TestHandleListPolicy_Returns200(t *testing.T) {
	m := newManager(t)
	app := setupListPolicyApp(m)

	resp := doRequest(t, app, http.MethodGet, "/rbac/policy", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestHandleListPolicy_BodyContainsPolicyKey(t *testing.T) {
	m := newManager(t)
	app := setupListPolicyApp(m)

	resp := doRequest(t, app, http.MethodGet, "/rbac/policy", "")
	body := decodeJSON(t, resp)
	policy, ok := body["policy"]
	if !ok {
		t.Fatal("response missing 'policy' key")
	}
	if policy == nil {
		t.Fatal("'policy' value is nil")
	}
}

func TestHandleListPolicy_ContainsAllThreeRoles(t *testing.T) {
	m := newManager(t)
	app := setupListPolicyApp(m)

	resp := doRequest(t, app, http.MethodGet, "/rbac/policy", "")
	body := decodeJSON(t, resp)
	policy, ok := body["policy"].(map[string]any)
	if !ok {
		t.Fatalf("policy is %T, want map[string]any", body["policy"])
	}
	for _, role := range []string{RoleAdmin, RoleOperator, RoleViewer} {
		if _, exists := policy[role]; !exists {
			t.Errorf("policy missing role %q", role)
		}
	}
}

func TestHandleListPolicy_AdminAgentsIncludesDelete(t *testing.T) {
	m := newManager(t)
	app := setupListPolicyApp(m)

	resp := doRequest(t, app, http.MethodGet, "/rbac/policy", "")
	body := decodeJSON(t, resp)

	policy := body["policy"].(map[string]any)
	adminPolicy := policy[RoleAdmin].(map[string]any)
	agentsPolicy := adminPolicy[ResourceAgents].(map[string]any)
	if agentsPolicy[ActionDelete] != true {
		t.Error("policy: admin agents delete should be true")
	}
}

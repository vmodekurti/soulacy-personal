// api_test.go — HTTP handler tests for the apikeys API.
// Uses fiber.App.Test so no external server is needed. Each test wires a
// fresh SQLiteStore into a minimal Fiber app and exercises the handlers.
package apikeys

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/soulacy/soulacy/internal/requestctx"
	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// helper — build a Fiber app backed by a fresh store
// ---------------------------------------------------------------------------

func newAPIApp(t *testing.T) (*fiber.App, *SQLiteStore) {
	t.Helper()
	s := newStore(t)
	log := zap.NewNop()
	api := NewAPI(s, log)

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Post("/apikeys", api.HandleCreate)
	app.Get("/apikeys", api.HandleList)
	app.Post("/apikeys/:id/revoke", api.HandleRevoke)
	app.Post("/apikeys/validate", api.HandleValidate)
	return app, s
}

func tenantAPIApp(t *testing.T, identity requestctx.Identity) (*fiber.App, *SQLiteStore) {
	t.Helper()
	store := newStore(t)
	api := NewAPI(store, zap.NewNop())
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(func(c *fiber.Ctx) error {
		c.SetUserContext(requestctx.With(c.UserContext(), identity))
		return c.Next()
	})
	app.Post("/apikeys", api.HandleCreate)
	app.Get("/apikeys", api.HandleList)
	app.Delete("/apikeys/:id", api.HandleRevoke)
	app.Post("/apikeys/:id/rotate", api.HandleRotate)
	app.Patch("/apikeys/:id/status", api.HandleStatus)
	return app, store
}

func testIdentity(t *testing.T, subject, org, workspace, role string) requestctx.Identity {
	t.Helper()
	identity, err := requestctx.New(requestctx.Input{Subject: subject, OrganizationID: org, WorkspaceID: workspace, MembershipID: "mem_test", Role: role, CredentialID: "cred_actor", RequestID: "req_test", PrincipalKind: "user"})
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

// ---------------------------------------------------------------------------
// NewAPI
// ---------------------------------------------------------------------------

func TestNewAPI_NotNil(t *testing.T) {
	s := newStore(t)
	api := NewAPI(s, zap.NewNop())
	if api == nil {
		t.Fatal("NewAPI returned nil")
	}
}

// ---------------------------------------------------------------------------
// HandleCreate
// ---------------------------------------------------------------------------

func TestHandleCreate_HappyPath(t *testing.T) {
	app, _ := newAPIApp(t)

	body := `{"name":"ci-bot","scopes":["read","write"]}`
	req, _ := http.NewRequest(http.MethodPost, "/apikeys", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("POST /apikeys: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("Create status = %d, want 201", resp.StatusCode)
	}

	var got map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got["id"] == "" {
		t.Error("Create response missing 'id'")
	}
	if got["key"] == "" {
		t.Error("Create response missing 'key' (plaintext)")
	}
	if got["name"] != "ci-bot" {
		t.Errorf("name = %v, want ci-bot", got["name"])
	}
}

func TestHandleCreate_MissingName(t *testing.T) {
	app, _ := newAPIApp(t)

	body := `{"name":"  ","scopes":[]}`
	req, _ := http.NewRequest(http.MethodPost, "/apikeys", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("Create empty name: status = %d, want 400", resp.StatusCode)
	}
}

func TestHandleCreate_EmptyNameWhitespace(t *testing.T) {
	app, _ := newAPIApp(t)

	body := `{"name":"\t  \n"}`
	req, _ := http.NewRequest(http.MethodPost, "/apikeys", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("Create whitespace-only name: status = %d, want 400", resp.StatusCode)
	}
}

func TestHandleCreate_InvalidBody(t *testing.T) {
	app, _ := newAPIApp(t)

	req, _ := http.NewRequest(http.MethodPost, "/apikeys", strings.NewReader("not-json{"))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("Create invalid body: status = %d, want 400", resp.StatusCode)
	}
}

func TestHandleCreate_NilScopes(t *testing.T) {
	// Scopes omitted from body — should default to nil/empty and succeed.
	app, _ := newAPIApp(t)

	body := `{"name":"no-scopes-key"}`
	req, _ := http.NewRequest(http.MethodPost, "/apikeys", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("Create nil scopes: status = %d, want 201", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// HandleList
// ---------------------------------------------------------------------------

func TestHandleList_EmptyStore(t *testing.T) {
	app, _ := newAPIApp(t)

	req, _ := http.NewRequest(http.MethodGet, "/apikeys", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("GET /apikeys: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("List empty: status = %d, want 200", resp.StatusCode)
	}

	var got map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&got)
	if got["keys"] == nil {
		t.Error("List empty: 'keys' field is null, want []")
	}
}

func TestHandleList_WithKeys(t *testing.T) {
	app, s := newAPIApp(t)
	ctx := context.Background()
	s.Create(ctx, "alpha", nil)
	s.Create(ctx, "beta", nil)

	req, _ := http.NewRequest(http.MethodGet, "/apikeys", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("GET /apikeys: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("List: status = %d, want 200", resp.StatusCode)
	}

	var got map[string][]interface{}
	json.NewDecoder(resp.Body).Decode(&got)
	if len(got["keys"]) != 2 {
		t.Errorf("List: got %d keys, want 2", len(got["keys"]))
	}
}

func TestHandleList_IncludeRevokedQuery(t *testing.T) {
	app, s := newAPIApp(t)
	ctx := context.Background()
	_, k, _ := s.Create(ctx, "revokable", nil)
	s.Revoke(ctx, k.ID)

	// Without include_revoked — revoked key excluded.
	req, _ := http.NewRequest(http.MethodGet, "/apikeys", nil)
	resp, _ := app.Test(req)
	var got1 map[string][]interface{}
	json.NewDecoder(resp.Body).Decode(&got1)
	if len(got1["keys"]) != 0 {
		t.Errorf("List without include_revoked: got %d, want 0", len(got1["keys"]))
	}

	// With include_revoked=true — revoked key included.
	req2, _ := http.NewRequest(http.MethodGet, "/apikeys?include_revoked=true", nil)
	resp2, _ := app.Test(req2)
	var got2 map[string][]interface{}
	json.NewDecoder(resp2.Body).Decode(&got2)
	if len(got2["keys"]) != 1 {
		t.Errorf("List with include_revoked=true: got %d, want 1", len(got2["keys"]))
	}
}

// ---------------------------------------------------------------------------
// HandleRevoke
// ---------------------------------------------------------------------------

func TestHandleRevoke_HappyPath(t *testing.T) {
	app, s := newAPIApp(t)
	ctx := context.Background()
	_, key, _ := s.Create(ctx, "revoke-me", nil)

	req, _ := http.NewRequest(http.MethodPost, "/apikeys/"+key.ID+"/revoke", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("POST revoke: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Revoke status = %d, want 200", resp.StatusCode)
	}

	var got map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&got)
	if got["status"] != "revoked" {
		t.Errorf("Revoke response status = %v, want 'revoked'", got["status"])
	}
	if got["id"] != key.ID {
		t.Errorf("Revoke response id = %v, want %q", got["id"], key.ID)
	}
}

func TestHandleRevoke_NotFound(t *testing.T) {
	app, _ := newAPIApp(t)

	req, _ := http.NewRequest(http.MethodPost, "/apikeys/nonexistent-id/revoke", nil)
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("Revoke missing ID: status = %d, want 404", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// HandleValidate
// ---------------------------------------------------------------------------

func TestHandleValidate_HappyPath(t *testing.T) {
	app, s := newAPIApp(t)
	ctx := context.Background()
	plaintext, key, _ := s.Create(ctx, "validator", []string{"read"})

	body := `{"key":"` + plaintext + `"}`
	req, _ := http.NewRequest(http.MethodPost, "/apikeys/validate", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("POST validate: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Validate status = %d, want 200", resp.StatusCode)
	}

	var got map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&got)
	if got["id"] != key.ID {
		t.Errorf("Validate response id = %v, want %q", got["id"], key.ID)
	}
}

func TestHandleValidate_InvalidKey(t *testing.T) {
	app, _ := newAPIApp(t)

	body := `{"key":"sk_totally_wrong_key"}`
	req, _ := http.NewRequest(http.MethodPost, "/apikeys/validate", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("Validate bad key: status = %d, want 401", resp.StatusCode)
	}
}

func TestHandleValidate_RevokedKey(t *testing.T) {
	app, s := newAPIApp(t)
	ctx := context.Background()
	plaintext, key, _ := s.Create(ctx, "to-revoke", nil)
	s.Revoke(ctx, key.ID)

	body := `{"key":"` + plaintext + `"}`
	req, _ := http.NewRequest(http.MethodPost, "/apikeys/validate", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("Validate revoked key: status = %d, want 401", resp.StatusCode)
	}
}

func TestHandleValidate_MissingKey(t *testing.T) {
	app, _ := newAPIApp(t)

	body := `{"key":""}`
	req, _ := http.NewRequest(http.MethodPost, "/apikeys/validate", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("Validate empty key: status = %d, want 400", resp.StatusCode)
	}
}

func TestHandleValidate_InvalidBody(t *testing.T) {
	app, _ := newAPIApp(t)

	req, _ := http.NewRequest(http.MethodPost, "/apikeys/validate", strings.NewReader("bad-json"))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("Validate invalid body: status = %d, want 400", resp.StatusCode)
	}
}

func TestHandleCreate_ResponseContainsPrefix(t *testing.T) {
	app, _ := newAPIApp(t)

	body := `{"name":"prefix-test"}`
	req, _ := http.NewRequest(http.MethodPost, "/apikeys", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("Create: status = %d", resp.StatusCode)
	}

	var got map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&got)

	prefix, _ := got["prefix"].(string)
	key, _ := got["key"].(string)
	if prefix == "" {
		t.Error("Create response missing 'prefix'")
	}
	if key == "" {
		t.Error("Create response missing 'key'")
	}
	if len(key) >= 8 && prefix != key[:8] {
		t.Errorf("prefix %q != key[:8] %q", prefix, key[:8])
	}
}

func TestHandleCreatePersonalCredentialUsesCallerAuthority(t *testing.T) {
	app, _ := tenantAPIApp(t, testIdentity(t, "usr_alice", "org_a", "ws_a", "developer"))
	body := `{"name":"alice-cli","kind":"personal_access_token","subject_id":"usr_mallory","role":"owner","scopes":["agents:read"]}`
	req, _ := http.NewRequest(http.MethodPost, "/apikeys", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var got map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if got["subject_id"] != "usr_alice" || got["role"] != "developer" {
		t.Fatalf("caller authority was not enforced: %#v", got)
	}
	if got["expires_at"] == nil {
		t.Fatal("default expiry was not assigned")
	}
}

func TestHandleCreateServiceCredentialRejectsNonAdmin(t *testing.T) {
	app, _ := tenantAPIApp(t, testIdentity(t, "usr_dev", "org_a", "ws_a", "developer"))
	req, _ := http.NewRequest(http.MethodPost, "/apikeys", strings.NewReader(`{"name":"bot","kind":"service_account","subject_id":"svc_bot","role":"developer","scopes":["agents:read"]}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status=%d, want 403", resp.StatusCode)
	}
}

func TestHandleListAndMutationHideOtherWorkspaceCredentials(t *testing.T) {
	identity := testIdentity(t, "usr_owner", "org_a", "ws_a", "owner")
	app, store := tenantAPIApp(t, identity)
	_, visible, err := store.CreateScoped(context.Background(), CreateRequest{Name: "visible", Kind: KindPersonal, SubjectID: "usr_owner", OrganizationID: "org_a", WorkspaceIDs: []string{"ws_a"}, Role: "owner", Scopes: []string{"credentials:list"}, Issuer: "soulacy"})
	if err != nil {
		t.Fatal(err)
	}
	_, hidden, err := store.CreateScoped(context.Background(), CreateRequest{Name: "hidden", Kind: KindPersonal, SubjectID: "usr_other", OrganizationID: "org_a", WorkspaceIDs: []string{"ws_b"}, Role: "owner", Scopes: []string{"credentials:list"}, Issuer: "soulacy"})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := app.Test(mustRequest(t, http.MethodGet, "/apikeys", ""))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var listed struct {
		Keys []APIKey `json:"keys"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&listed)
	if len(listed.Keys) != 1 || listed.Keys[0].ID != visible.ID {
		t.Fatalf("cross-workspace list leak: %+v", listed.Keys)
	}
	resp, err = app.Test(mustRequest(t, http.MethodDelete, "/apikeys/"+hidden.ID, ""))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-workspace mutation status=%d", resp.StatusCode)
	}
}

func mustRequest(t *testing.T, method, path, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

// The audit record must name the credential that was issued — and, for
// automation, the service account behind it — while never carrying the
// plaintext secret out of the handler.
func TestCredentialAuditIdentifiesTheServiceAccountWithoutTheSecret(t *testing.T) {
	identity := testIdentity(t, "usr_owner", "org_a", "ws_a", "owner")
	store := newStore(t)
	api := NewAPI(store, zap.NewNop())
	app := fiber.New(fiber.Config{DisableStartupMessage: true})

	var auditTarget string
	var auditDetails map[string]any
	var responseBody string
	app.Post("/apikeys", func(c *fiber.Ctx) error {
		c.SetUserContext(requestctx.With(c.UserContext(), identity))
		err := api.HandleCreate(c)
		auditTarget, auditDetails = AuditSubject(c)
		responseBody = string(c.Response().Body())
		return err
	})

	body := `{"name":"ci","kind":"service_account","subject_id":"svc_ci","role":"operator","scopes":["agents:run"]}`
	req, _ := http.NewRequest(http.MethodPost, "/apikeys", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create returned %d: %s", resp.StatusCode, responseBody)
	}
	if auditTarget == "" || auditDetails == nil {
		t.Fatal("no audit subject was recorded for a credential issuance")
	}
	if got := auditDetails["service_account_id"]; got != "svc_ci" {
		t.Fatalf("audit does not name the service account: %v", auditDetails)
	}
	if got := auditDetails["credential_kind"]; got != KindService {
		t.Fatalf("audit reports a generic principal kind: %v", got)
	}
	if auditDetails["credential_id"] != auditTarget {
		t.Fatalf("audit target %q does not match the credential ID %v", auditTarget, auditDetails["credential_id"])
	}

	var created struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal([]byte(responseBody), &created); err != nil || created.Key == "" {
		t.Fatalf("create response did not carry a one-time secret: %v %s", err, responseBody)
	}
	encoded, err := json.Marshal(auditDetails)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), created.Key) {
		t.Fatal("the audit details leak the plaintext credential secret")
	}
	for _, forbidden := range []string{"key", "secret", "plaintext", "secret_hash", "key_hash"} {
		if _, present := auditDetails[forbidden]; present {
			t.Errorf("audit details carry a secret-bearing field %q", forbidden)
		}
	}
}

// A workspace-bound caller manages only the credentials bound to its own
// workspace; another workspace's credential is reported as absent rather than
// as forbidden, so the ID is not confirmed to exist.
func TestCredentialManagementIsWorkspaceScoped(t *testing.T) {
	app, store := tenantAPIApp(t, testIdentity(t, "usr_owner", "org_a", "ws_a", "owner"))
	foreign := seedCredential(t, store, "foreign", "org_b", []string{"ws_b"})

	for _, call := range []struct {
		method string
		path   string
	}{
		{http.MethodDelete, "/apikeys/" + foreign.ID},
		{http.MethodPost, "/apikeys/" + foreign.ID + "/rotate"},
	} {
		req, _ := http.NewRequest(call.method, call.path, strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s %s returned %d, want 404", call.method, call.path, resp.StatusCode)
		}
	}

	req, _ := http.NewRequest(http.MethodGet, "/apikeys?include_revoked=true", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	var listed struct {
		Keys []APIKey `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	for _, key := range listed.Keys {
		if key.ID == foreign.ID {
			t.Fatal("a credential from another workspace was listed")
		}
	}
}

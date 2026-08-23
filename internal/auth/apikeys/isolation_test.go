// isolation_test.go — the cross-tenant isolation contract for the credential
// catalog. internal/ownership/catalog.go names this package's tests as the
// isolation evidence for the api_keys and access_credentials tables, so these
// cases assert the boundary itself rather than incidental behaviour.
package apikeys

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/soulacy/soulacy/internal/requestctx"
	"go.uber.org/zap"
)

func seedCredential(t *testing.T, s *SQLiteStore, name, org string, workspaces []string) APIKey {
	t.Helper()
	future := time.Now().UTC().Add(time.Hour)
	_, key, err := s.CreateScoped(context.Background(), CreateRequest{
		Name: name, Kind: KindPersonal, SubjectID: "usr_" + name,
		OrganizationID: org, WorkspaceIDs: workspaces, Role: "developer",
		Scopes: []string{"agents:read"}, Issuer: "soulacy", ExpiresAt: &future,
	})
	if err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
	return key
}

// A workspace-scoped listing is the only listing a workspace-bound caller ever
// sees. A credential in a sibling workspace, or in a different organization
// that happens to reuse the same workspace ID, must not appear.
func TestListForWorkspaceExcludesOtherTenants(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	mine := seedCredential(t, s, "mine", "org_a", []string{"ws_a"})
	shared := seedCredential(t, s, "shared", "org_a", []string{"ws_a", "ws_b"})
	sibling := seedCredential(t, s, "sibling", "org_a", []string{"ws_b"})
	// Same workspace ID, different organization: IDs are not a tenant boundary.
	collision := seedCredential(t, s, "collision", "org_b", []string{"ws_a"})

	keys, err := s.ListForWorkspace(ctx, "org_a", "ws_a", false)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, key := range keys {
		seen[key.ID] = true
	}
	if !seen[mine.ID] || !seen[shared.ID] {
		t.Fatalf("workspace-bound credentials missing from scoped listing: %v", seen)
	}
	if seen[sibling.ID] {
		t.Error("a credential bound only to another workspace was listed")
	}
	if seen[collision.ID] {
		t.Error("a credential from another organization with a colliding workspace ID was listed")
	}
	if len(keys) != 2 {
		t.Fatalf("scoped listing returned %d credentials, want 2", len(keys))
	}
}

func TestListForWorkspaceRequiresBothScopeComponents(t *testing.T) {
	s := newStore(t)
	seedCredential(t, s, "mine", "org_a", []string{"ws_a"})
	for _, scope := range [][2]string{{"", "ws_a"}, {"org_a", ""}, {" ", " "}} {
		if _, err := s.ListForWorkspace(context.Background(), scope[0], scope[1], true); err == nil {
			t.Fatalf("listing with an incomplete scope %v was allowed", scope)
		}
	}
}

func TestPurgeWorkspaceRevokesThenRemovesCredentialHashes(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	deletedPlaintext, deleted, err := s.CreateScoped(ctx, CreateRequest{
		Name: "delete", Kind: KindPersonal, SubjectID: "usr_delete",
		OrganizationID: "org_a", WorkspaceIDs: []string{"ws_delete", "ws_shared"},
		Role: "developer", Scopes: []string{"agents:read"}, Issuer: "soulacy",
	})
	if err != nil {
		t.Fatal(err)
	}
	kept := seedCredential(t, s, "keep", "org_a", []string{"ws_keep"})

	removed, err := s.PurgeWorkspace(ctx, "ws_delete")
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("purged %d credential hashes, want 1", removed)
	}
	if _, err := s.Validate(ctx, deletedPlaintext); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("deleted workspace credential still validates: %v", err)
	}
	all, err := s.List(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, key := range all {
		seen[key.ID] = true
	}
	if seen[deleted.ID] || !seen[kept.ID] {
		t.Fatalf("post-purge credential set = %v", seen)
	}
}

// Revoked credentials stay invisible by default but remain auditable, and the
// revoked view is scoped exactly like the active one.
func TestListForWorkspaceHonoursRevocationFilterWithinTheScope(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mine := seedCredential(t, s, "mine", "org_a", []string{"ws_a"})
	other := seedCredential(t, s, "other", "org_b", []string{"ws_a"})
	if err := s.Revoke(ctx, mine.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Revoke(ctx, other.ID); err != nil {
		t.Fatal(err)
	}
	active, err := s.ListForWorkspace(ctx, "org_a", "ws_a", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 0 {
		t.Fatalf("revoked credential listed as active: %+v", active)
	}
	all, err := s.ListForWorkspace(ctx, "org_a", "ws_a", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].ID != mine.ID {
		t.Fatalf("revoked listing crossed the tenant boundary: %+v", all)
	}
}

// A revoked, suspended, deleted, or expired credential is rejected on the very
// next request. No cache is consulted and no restart is involved.
func TestValidateRejectsWithdrawnCredentialsImmediately(t *testing.T) {
	ctx := context.Background()
	for _, status := range []string{StatusRevoked, StatusSuspended, StatusDeleted} {
		s := newStore(t)
		future := time.Now().UTC().Add(time.Hour)
		plaintext, key, err := s.CreateScoped(ctx, CreateRequest{
			Name: "ci", Kind: KindService, SubjectID: "svc_ci", OrganizationID: "org_a",
			WorkspaceIDs: []string{"ws_a"}, Role: "operator", Scopes: []string{"agents:run"},
			Issuer: "soulacy", ExpiresAt: &future,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Validate(ctx, plaintext); err != nil {
			t.Fatalf("freshly issued credential rejected: %v", err)
		}
		if err := s.SetStatus(ctx, key.ID, status); err != nil {
			t.Fatalf("set %s: %v", status, err)
		}
		if _, err := s.Validate(ctx, plaintext); err == nil {
			t.Fatalf("a %s credential was still accepted", status)
		}
	}
}

// Rotation must not leave a window in which both secrets work.
func TestRotateInvalidatesThePreviousSecretAtomically(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	future := time.Now().UTC().Add(time.Hour)
	oldPlaintext, key, err := s.CreateScoped(ctx, CreateRequest{
		Name: "ci", Kind: KindService, SubjectID: "svc_ci", OrganizationID: "org_a",
		WorkspaceIDs: []string{"ws_a"}, Role: "operator", Scopes: []string{"agents:run"},
		Issuer: "soulacy", ExpiresAt: &future,
	})
	if err != nil {
		t.Fatal(err)
	}
	newPlaintext, replacement, err := s.Rotate(ctx, key.ID)
	if err != nil {
		t.Fatal(err)
	}
	if newPlaintext == oldPlaintext {
		t.Fatal("rotation reissued the same secret")
	}
	if replacement.RotatedFromID != key.ID {
		t.Fatalf("replacement does not reference its predecessor: %+v", replacement)
	}
	if _, err := s.Validate(ctx, oldPlaintext); err == nil {
		t.Fatal("the rotated-out secret is still accepted")
	}
	if got, err := s.Validate(ctx, newPlaintext); err != nil {
		t.Fatalf("replacement secret rejected: %v", err)
	} else if got.OrganizationID != key.OrganizationID || len(got.WorkspaceIDs) != 1 || got.WorkspaceIDs[0] != "ws_a" || got.Role != key.Role {
		t.Fatalf("rotation changed the credential's authority: %+v", got)
	}
}

// The plaintext secret is shown once and is never recoverable from storage.
func TestStoredCredentialNeverRetainsPlaintext(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	future := time.Now().UTC().Add(time.Hour)
	plaintext, key, err := s.CreateScoped(ctx, CreateRequest{
		Name: "ci", Kind: KindPersonal, SubjectID: "usr_a", OrganizationID: "org_a",
		WorkspaceIDs: []string{"ws_a"}, Role: "viewer", Scopes: []string{"agents:read"},
		Issuer: "soulacy", ExpiresAt: &future,
	})
	if err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := s.db.QueryRowContext(ctx, `SELECT key_hash FROM api_keys WHERE id=?`, key.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == plaintext || strings.Contains(stored, strings.TrimPrefix(plaintext, "sk_")) {
		t.Fatal("the plaintext secret is recoverable from storage")
	}
	encoded, err := json.Marshal(key)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), plaintext) {
		t.Fatal("the credential record serializes its plaintext secret")
	}
}

// ---------------------------------------------------------------------------
// The unverified-request fallback
//
// visibleKeys, mayManage and credentialVisibleTo all scope to the caller's
// workspace when requestctx yields an identity. The interesting case is the
// other branch. Personal deployments reach the handlers with no identity as a
// matter of course and must keep working, so the fallback there grants the
// caller everything — correctly, because there is exactly one tenant. A
// multi-user deployment that kept that fallback would hand an unattributed
// request management of every credential in the installation, and for the
// credential store specifically it would also let that request mint new ones
// bound to a tenant it names itself. NewScopedAPI(..., true) turns the
// fallback into a denial; these cases pin that shape down.
// ---------------------------------------------------------------------------

func unverifiedApp(t *testing.T, requireIdentity bool) (*fiber.App, *SQLiteStore) {
	t.Helper()
	store := newStore(t)
	api := NewScopedAPI(store, zap.NewNop(), requireIdentity)
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Post("/apikeys", api.HandleCreate)
	app.Get("/apikeys", api.HandleList)
	app.Delete("/apikeys/:id", api.HandleRevoke)
	app.Post("/apikeys/:id/rotate", api.HandleRotate)
	app.Patch("/apikeys/:id/status", api.HandleStatus)
	app.Post("/apikeys/validate", api.HandleValidate)
	return app, store
}

func do(t *testing.T, app *fiber.App, method, path, body string) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

func TestUnverifiedRequestCarriesNoCredentialAuthority(t *testing.T) {
	app, store := unverifiedApp(t, true)
	ctx := context.Background()
	future := time.Now().UTC().Add(time.Hour)
	plaintext, key, err := store.CreateScoped(ctx, CreateRequest{
		Name: "victim", Kind: KindService, SubjectID: "svc_victim", OrganizationID: "org_a",
		WorkspaceIDs: []string{"ws_a"}, Role: "operator", Scopes: []string{"agents:run"},
		Issuer: "soulacy", ExpiresAt: &future,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Reading: the listing is empty and says so as a refusal, not as a fault.
	resp := do(t, app, http.MethodGet, "/apikeys?include_revoked=true", "")
	if resp.StatusCode != fiber.StatusForbidden {
		t.Fatalf("unverified list returned %d, want 403", resp.StatusCode)
	}
	payload, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(payload), key.ID) || strings.Contains(string(payload), key.Prefix) {
		t.Fatalf("the refusal leaked credential material: %s", payload)
	}

	// Introspection: holding no identity, the caller cannot even confirm that
	// a secret it already possesses is a real credential.
	resp = do(t, app, http.MethodPost, "/apikeys/validate", `{"key":"`+plaintext+`"}`)
	if resp.StatusCode != fiber.StatusNotFound {
		t.Fatalf("unverified validate returned %d, want 404", resp.StatusCode)
	}

	// Writing: revoke, rotate and status must all miss, and the credential
	// must still work afterwards. A denial that still mutated would be worse
	// than no denial at all.
	for _, call := range []struct {
		method, path, body string
	}{
		{http.MethodDelete, "/apikeys/" + key.ID, ""},
		{http.MethodPost, "/apikeys/" + key.ID + "/rotate", ""},
		{http.MethodPatch, "/apikeys/" + key.ID + "/status", `{"status":"revoked"}`},
	} {
		resp := do(t, app, call.method, call.path, call.body)
		if resp.StatusCode != fiber.StatusNotFound {
			t.Errorf("unverified %s %s returned %d, want 404", call.method, call.path, resp.StatusCode)
		}
	}
	if _, err := store.Validate(ctx, plaintext); err != nil {
		t.Fatalf("an unverified caller withdrew a credential it could not see: %v", err)
	}

	// Issuance: the unverified create path reads the tenant binding out of the
	// request body, so leaving it open would let this caller write itself into
	// org_b. Nothing may be created.
	before, err := store.List(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	// The body is deliberately complete — a future expiry included — so that
	// the only thing standing between it and a stored credential is the
	// identity guard, not incidental request validation.
	forged := `{"name":"forged","kind":"` + KindService + `","subject_id":"svc_forged",` +
		`"organization_id":"org_b","workspace_ids":["ws_b"],"role":"owner",` +
		`"scopes":["agents:run"],"issuer":"soulacy","expires_at":"` +
		time.Now().UTC().Add(24*time.Hour).Format(time.RFC3339) + `"}`
	resp = do(t, app, http.MethodPost, "/apikeys", forged)
	if resp.StatusCode != fiber.StatusForbidden {
		t.Fatalf("unverified create returned %d, want 403", resp.StatusCode)
	}
	after, err := store.List(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("a forbidden create still wrote a credential: %d -> %d", len(before), len(after))
	}
}

// Product invariant 7: a personal deployment must not notice that the handlers
// became tenant-aware. With RequireIdentity unset the unverified caller keeps
// the authority it has always had.
func TestPersonalDeploymentKeepsTheUnverifiedFallback(t *testing.T) {
	app, store := unverifiedApp(t, false)
	ctx := context.Background()
	_, key, err := store.Create(ctx, "local", []string{"agents:read"})
	if err != nil {
		t.Fatal(err)
	}
	resp := do(t, app, http.MethodGet, "/apikeys", "")
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("personal list returned %d, want 200", resp.StatusCode)
	}
	var listed struct {
		Keys []APIKey `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Keys) != 1 || listed.Keys[0].ID != key.ID {
		t.Fatalf("personal listing changed shape: %+v", listed.Keys)
	}
	if resp := do(t, app, http.MethodDelete, "/apikeys/"+key.ID, ""); resp.StatusCode != fiber.StatusOK {
		t.Fatalf("personal revoke returned %d, want 200", resp.StatusCode)
	}
}

// A verified identity is scoped to its own workspace at the handler layer too,
// not only in the store query. The sibling credential is neither visible nor
// manageable, and the 404 is deliberately indistinguishable from a credential
// that does not exist.
func TestVerifiedCallerCannotReachASiblingWorkspacesCredential(t *testing.T) {
	store := newStore(t)
	api := NewScopedAPI(store, zap.NewNop(), true)
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	identity, err := requestctx.New(requestctx.Input{
		Subject: "usr_b", OrganizationID: "org_a", WorkspaceID: "ws_b",
		MembershipID: "mem_b", Role: "owner", CredentialID: "cred_b",
		RequestID: "req_b", PrincipalKind: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	app.Use(func(c *fiber.Ctx) error {
		c.SetUserContext(requestctx.With(c.UserContext(), identity))
		return c.Next()
	})
	app.Get("/apikeys", api.HandleList)
	app.Delete("/apikeys/:id", api.HandleRevoke)
	app.Post("/apikeys/:id/rotate", api.HandleRotate)

	victim := seedCredential(t, store, "victim", "org_a", []string{"ws_a"})

	resp := do(t, app, http.MethodGet, "/apikeys?include_revoked=true", "")
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("scoped list returned %d, want 200", resp.StatusCode)
	}
	payload, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(payload), victim.ID) {
		t.Fatalf("a sibling workspace's credential was listed: %s", payload)
	}
	for _, call := range [][2]string{
		{http.MethodDelete, "/apikeys/" + victim.ID},
		{http.MethodPost, "/apikeys/" + victim.ID + "/rotate"},
	} {
		if resp := do(t, app, call[0], call[1], ""); resp.StatusCode != fiber.StatusNotFound {
			t.Errorf("%s %s returned %d, want 404", call[0], call[1], resp.StatusCode)
		}
	}
	stored, err := store.byID(context.Background(), victim.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != StatusActive {
		t.Fatalf("a sibling workspace's credential was withdrawn: status=%s", stored.Status)
	}
}

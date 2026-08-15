// isolation_test.go — the cross-tenant isolation contract for the credential
// catalog. internal/ownership/catalog.go names this package's tests as the
// isolation evidence for the api_keys and access_credentials tables, so these
// cases assert the boundary itself rather than incidental behaviour.
package apikeys

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
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

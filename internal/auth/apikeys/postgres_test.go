package apikeys

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPostgresCredentialSchemaCarriesTenantAuthority(t *testing.T) {
	joined := strings.Join(postgresSchema, "\n")
	for _, required := range []string{"secret_hash BYTEA", "organization_id TEXT", "workspace_ids JSONB", "scopes JSONB", "issuer TEXT", "expires_at TIMESTAMPTZ", "rotated_from_id TEXT", "suspended", "deleted"} {
		if !strings.Contains(joined, required) {
			t.Errorf("postgres credential schema missing %q", required)
		}
	}
}

func TestNormalizeCreateRequestRequiresExplicitAuthorityAndFutureExpiry(t *testing.T) {
	future := time.Now().UTC().Add(time.Hour)
	req, err := normalizeCreateRequest(CreateRequest{Name: " ci ", Kind: KindService, SubjectID: " svc_ci ", OrganizationID: " org_a ", WorkspaceIDs: []string{"ws_a", "ws_a"}, Role: " Operator ", Scopes: []string{"agents:read", "agents:read"}, Issuer: " soulacy ", ExpiresAt: &future})
	if err != nil {
		t.Fatal(err)
	}
	if req.Name != "ci" || req.SubjectID != "svc_ci" || req.Role != "operator" || len(req.WorkspaceIDs) != 1 || len(req.Scopes) != 1 {
		t.Fatalf("not normalized: %+v", req)
	}
	past := time.Now().UTC().Add(-time.Second)
	if _, err := normalizeCreateRequest(CreateRequest{Name: "bad", Kind: KindPersonal, SubjectID: "usr", OrganizationID: "org", WorkspaceIDs: []string{"ws"}, Role: "viewer", Issuer: "soulacy", ExpiresAt: &past}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("past expiry accepted: %v", err)
	}
}

// The shared Team/Scale catalog is the multi-tenant store, so every listing it
// performs on behalf of a workspace caller must carry the tenant predicate in
// SQL. internal/ownership/catalog.go cites this test as the isolation evidence
// for access_credentials and for the postgres repository.
func TestPostgresScopedListQueryAlwaysCarriesTheTenantPredicate(t *testing.T) {
	for _, includeRevoked := range []bool{false, true} {
		query := postgresScopedListQuery(includeRevoked)
		for _, required := range []string{"organization_id=$1", "workspace_ids @> to_jsonb($2::text)"} {
			if !strings.Contains(query, required) {
				t.Errorf("includeRevoked=%v: scoped listing is missing %q: %s", includeRevoked, required, query)
			}
		}
		if includeRevoked == strings.Contains(query, "status='active'") {
			t.Errorf("includeRevoked=%v: revocation filter is wrong: %s", includeRevoked, query)
		}
	}
}

// A Team credential cannot be minted without explicit tenant bindings, so the
// legacy unbound Create path must fail closed rather than inherit ambient
// authority from whatever pool happens to be wired.
func TestPostgresRejectsUnboundCredentialCreation(t *testing.T) {
	store := &PostgresStore{}
	if _, _, err := store.Create(context.Background(), "ci", []string{"agents:read"}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("unbound Team credential creation was allowed: %v", err)
	}
}

func TestNewPostgresStoreRequiresAPool(t *testing.T) {
	if _, err := NewPostgresStore(context.Background(), nil); err == nil {
		t.Fatal("a nil pool was accepted")
	}
}

// Validation revalidates the live status of the credential's subject, so a
// suspended membership or a disabled service account stops working without a
// restart or a cache flush.
func TestPostgresValidationRevalidatesSubjectStatus(t *testing.T) {
	source, err := os.ReadFile("postgres.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(source)
	for _, required := range []string{
		"FROM service_accounts WHERE id=$1 AND organization_id=$2 AND status='active'",
		"FROM memberships WHERE user_id=$1 AND organization_id=$2 AND status='active'",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("credential validation no longer revalidates the subject with %q", required)
		}
	}
}

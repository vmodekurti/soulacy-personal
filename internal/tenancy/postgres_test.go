package tenancy

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresSchemaContainsTenantIntegrityAndAuditConstraints(t *testing.T) {
	joined := strings.Join(postgresSchema, "\n")
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS organizations", "CREATE TABLE IF NOT EXISTS workspaces",
		"CREATE TABLE IF NOT EXISTS users", "CREATE TABLE IF NOT EXISTS identities",
		"CREATE TABLE IF NOT EXISTS memberships", "CREATE TABLE IF NOT EXISTS invitations",
		"CREATE TABLE IF NOT EXISTS service_accounts", "CREATE TABLE IF NOT EXISTS credentials",
		"CREATE TABLE IF NOT EXISTS service_account_workspaces",
		"CREATE TABLE IF NOT EXISTS workspace_identity_providers",
		"CREATE TABLE IF NOT EXISTS workspace_setup_tokens",
		"CREATE TABLE IF NOT EXISTS tenant_mutation_audit",
		"tenant_mutation_audit_resource_created", "tenant_mutation_audit_action_created", "tenant_mutation_audit_actor_created",
		"FOREIGN KEY(workspace_id,organization_id) REFERENCES workspaces(id,organization_id)",
		"UNIQUE(provider, external_subject)", "users_normalized_email_unique",
		"provider=LOWER(BTRIM(provider))",
		"CHECK(NUM_NONNULLS(user_id,service_account_id)=1)",
	} {
		if !strings.Contains(joined, required) {
			t.Errorf("schema missing %q", required)
		}
	}
}

func TestWorkspaceProviderSecretEncryptionAndSetupToken(t *testing.T) {
	store := &PostgresStore{}
	store.ConfigureProviderEncryptionSecret("deployment-secret")
	ciphertext, err := store.encryptProviderSecret("provider-client-secret")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(ciphertext), "provider-client-secret") {
		t.Fatal("provider secret was stored in plaintext")
	}
	plaintext, err := store.decryptProviderSecret(ciphertext)
	if err != nil || plaintext != "provider-client-secret" {
		t.Fatalf("decrypted secret = %q, %v", plaintext, err)
	}

	token, err := newWorkspaceSetupToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(token) != 64 {
		t.Fatalf("setup token length = %d, want 64 hex characters", len(token))
	}
	if tokenAgain, _ := newWorkspaceSetupToken(); tokenAgain == token {
		t.Fatal("two setup tokens were identical")
	}
}

func TestWorkspaceLogoValidation(t *testing.T) {
	for _, valid := range []string{"", "data:image/png;base64,aGVsbG8=", "data:image/jpeg;base64,aGVsbG8=", "data:image/webp;base64,aGVsbG8="} {
		if err := validateLogoDataURL(valid); err != nil {
			t.Errorf("valid logo %q rejected: %v", valid, err)
		}
	}
	for _, invalid := range []string{"https://example.test/logo.png", "data:image/svg+xml;base64,PHN2Zz4="} {
		if err := validateLogoDataURL(invalid); err == nil {
			t.Errorf("unsafe logo %q accepted", invalid)
		}
	}
}

func TestOIDCSubjectLockKeyIsPostgresTextSafeAndTupleBound(t *testing.T) {
	key := oidcSubjectLockKey("https://accounts.google.com", "google-subject")
	if strings.ContainsRune(key, '\x00') {
		t.Fatal("advisory lock key contains a PostgreSQL-invalid NUL byte")
	}
	if len(key) != 64 {
		t.Fatalf("lock key length = %d, want 64", len(key))
	}
	if oidcSubjectLockKey("ab", "c") == oidcSubjectLockKey("a", "bc") {
		t.Fatal("different provider/subject tuples produced the same lock key")
	}
}

func TestWorkspaceSlugIsFriendlyStableAndUnique(t *testing.T) {
	const id = "ws_0123456789abcdef0123456789abcdef"
	if got := workspaceSlug("Customer Support", id); got != "customer-support-abcdef" {
		t.Fatalf("workspace slug = %q", got)
	}
	if workspaceSlug("Customer Support", id) == workspaceSlug("Customer Support", "ws_0123456789abcdef0123456789abcdee") {
		t.Fatal("workspace slug did not disambiguate duplicate names")
	}
	if got := workspaceSlug("研究 チーム", id); got != "workspace-abcdef" {
		t.Fatalf("fallback workspace slug = %q", got)
	}
}

func TestWorkspaceSlugValidation(t *testing.T) {
	for _, valid := range []string{"acme", "acme-support", "team-42"} {
		if err := validateWorkspaceSlug(valid); err != nil {
			t.Errorf("valid slug %q: %v", valid, err)
		}
	}
	for _, invalid := range []string{"ab", "Acme", "-acme", "acme-", "acme support", strings.Repeat("a", 49)} {
		if err := validateWorkspaceSlug(invalid); err == nil {
			t.Errorf("invalid slug %q accepted", invalid)
		}
	}
}

func TestPostgresMutationRequiresActorAndRequest(t *testing.T) {
	s := &PostgresStore{}
	if _, err := s.CreateOrganization(context.Background(), Mutation{}, "Acme"); err == nil || !strings.Contains(err.Error(), "actor") {
		t.Fatalf("missing mutation identity error = %v", err)
	}
}

func TestPostgresCredentialRequiresExactlyOneSubject(t *testing.T) {
	s := &PostgresStore{}
	m := Mutation{ActorSubject: "admin", RequestID: "req"}
	if _, err := s.CreateCredential(context.Background(), m, "user", "service", []byte("hash"), nil, nil); err == nil {
		t.Fatal("expected two credential subjects to be rejected")
	}
	if _, err := s.CreateCredential(context.Background(), m, "", "", []byte("hash"), nil, nil); err == nil {
		t.Fatal("expected missing credential subject to be rejected")
	}
}

func TestPostgresTenantLifecycleIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("SOULACY_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("POSTGRES INTEGRATION SKIPPED: set SOULACY_TEST_POSTGRES_DSN")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store, err := OpenPostgres(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"tenant_mutation_audit", "workspace_setup_tokens", "workspace_identity_providers", "credentials", "service_account_workspaces", "service_accounts", "invitations", "memberships", "identities", "users", "workspaces", "organizations"} {
		if _, err := pool.Exec(ctx, "DELETE FROM "+table); err != nil {
			t.Fatal(err)
		}
	}

	mutation := Mutation{ActorSubject: "bootstrap-admin", RequestID: "req-bootstrap", At: time.Now().UTC()}
	orgA, err := store.CreateOrganization(ctx, mutation, "Acme")
	if err != nil {
		t.Fatal(err)
	}
	orgB, err := store.CreateOrganization(ctx, mutation, "Other")
	if err != nil {
		t.Fatal(err)
	}
	wsA, err := store.CreateWorkspace(ctx, mutation, orgA.ID, "Engineering")
	if err != nil {
		t.Fatal(err)
	}
	wsB, err := store.CreateWorkspace(ctx, mutation, orgA.ID, "Operations")
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.CreateUser(ctx, mutation, " Alice@Example.COM ", "Alice")
	if err != nil {
		t.Fatal(err)
	}
	if user.Email != "alice@example.com" {
		t.Fatalf("normalized email = %q", user.Email)
	}
	identity, err := store.CreateIdentity(ctx, mutation, user.ID, "OIDC", "external-alice")
	if err != nil {
		t.Fatal(err)
	}
	linked, err := store.LinkOIDCIdentity(ctx, "https://issuer.example", "verified-subject", "ALICE@example.com", true, "Alice")
	if err != nil || linked != user.ID {
		t.Fatalf("verified email link = %q, %v; want %q", linked, err, user.ID)
	}
	linkedAgain, err := store.LinkOIDCIdentity(ctx, "https://issuer.example", "verified-subject", "changed@example.com", true, "Changed")
	if err != nil || linkedAgain != user.ID {
		t.Fatalf("provider subject was not stable: %q, %v", linkedAgain, err)
	}
	unverified, err := store.LinkOIDCIdentity(ctx, "https://issuer.example", "unverified-subject", "alice@example.com", false, "Unverified")
	if err != nil || unverified == user.ID {
		t.Fatalf("unverified email attached to existing account: %q, %v", unverified, err)
	}
	signupUser, err := store.LinkOIDCIdentity(ctx, "https://issuer.example", "new-customer", "founder@example.com", true, "Founder")
	if err != nil {
		t.Fatal(err)
	}
	signupResult, err := store.ProvisionSelfServiceOrganization(ctx, Mutation{ActorSubject: signupUser, RequestID: "req-self-service"}, signupUser, BootstrapRequest{
		OrganizationName: "Founder Co", WorkspaceName: "AI Operations", OwnerEmail: "founder@example.com", OwnerDisplayName: "Founder",
	})
	if err != nil || signupResult.User.ID != signupUser || signupResult.Membership.Role != RoleOwner || signupResult.SetupToken == "" {
		t.Fatalf("self-service provision = %#v, %v", signupResult, err)
	}
	if _, err = store.ProvisionSelfServiceOrganization(ctx, Mutation{ActorSubject: signupUser, RequestID: "req-self-service-retry"}, signupUser, BootstrapRequest{
		OrganizationName: "Second Co", WorkspaceName: "Second", OwnerEmail: "founder@example.com", OwnerDisplayName: "Founder",
	}); err != ErrSelfServiceAlreadyProvisioned {
		t.Fatalf("duplicate self-service provision error = %v", err)
	}
	memberA, err := store.CreateMembership(ctx, mutation, orgA.ID, wsA.ID, user.ID, RoleOwner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateMembership(ctx, mutation, orgA.ID, wsB.ID, user.ID, "viewer"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateMembership(ctx, mutation, orgB.ID, wsA.ID, user.ID, "viewer"); err == nil {
		t.Fatal("cross-organization membership should violate composite foreign key")
	}
	if _, err := store.SetMembershipStatusInWorkspace(ctx, Mutation{ActorSubject: user.ID, RequestID: "req-last-owner"}, wsA.ID, memberA.ID, RoleOwner, MembershipSuspended); err != ErrLastOwner {
		t.Fatalf("last owner suspension error = %v, want %v", err, ErrLastOwner)
	}
	bob, err := store.CreateUser(ctx, mutation, "bob@example.com", "Bob")
	if err != nil {
		t.Fatal(err)
	}
	invitation, err := store.CreateInvitation(ctx, mutation, orgA.ID, wsA.ID, RoleOwner, bob.Email, RoleOwner, time.Now().UTC().Add(time.Hour))
	if err != nil || invitation.Token == "" {
		t.Fatalf("create invitation = %#v, %v", invitation, err)
	}
	bobMembership, err := store.AcceptInvitation(ctx, Mutation{ActorSubject: bob.ID, RequestID: "req-accept"}, invitation.Token, bob.ID)
	if err != nil || bobMembership.Role != RoleOwner {
		t.Fatalf("accept invitation = %#v, %v", bobMembership, err)
	}
	acceptedAgain, err := store.AcceptInvitation(ctx, Mutation{ActorSubject: bob.ID, RequestID: "req-accept-retry"}, invitation.Token, bob.ID)
	if err != nil || acceptedAgain.ID != bobMembership.ID {
		t.Fatalf("idempotent invitation acceptance = %#v, %v", acceptedAgain, err)
	}
	if _, err := store.AcceptInvitation(ctx, Mutation{ActorSubject: user.ID, RequestID: "req-token-reuse"}, invitation.Token, user.ID); err != ErrInvitationInvalid {
		t.Fatalf("cross-user token reuse error = %v, want %v", err, ErrInvitationInvalid)
	}
	resolved, err := store.ResolveMembership(ctx, identity.ExternalSubject, wsB.ID)
	if err != nil || resolved.Role != "viewer" {
		t.Fatalf("resolved membership = %#v, %v", resolved, err)
	}
	if _, err := store.SetMembershipStatusInWorkspace(ctx, Mutation{ActorSubject: bob.ID, RequestID: "req-suspend"}, wsA.ID, memberA.ID, RoleOwner, MembershipSuspended); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResolveMembership(ctx, user.ID, wsA.ID); err != ErrMembershipNotFound {
		t.Fatalf("suspended membership resolution = %v", err)
	}
	var auditCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM tenant_mutation_audit WHERE actor_subject='bootstrap-admin' AND request_id='req-bootstrap'`).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount < 8 {
		t.Fatalf("audit rows = %d, want at least 8", auditCount)
	}
}

// The credentials table has no workspace column, and the catalog records its
// scope as "workspace_id via principal memberships". These cases hold that
// claim to account without needing a live database: the join is the boundary,
// so the join is what is asserted.
func TestCredentialScopeIsDerivedFromThePrincipalNotStoredOnTheRow(t *testing.T) {
	// A credential belongs to exactly one principal. Without this, a row could
	// name both a user and a service account and inherit two different
	// workspaces at once — the join would return it for both.
	if !strings.Contains(strings.Join(postgresSchema, "\n"), "CHECK(NUM_NONNULLS(user_id,service_account_id)=1)") {
		t.Fatal("a credential must have exactly one principal for the tenant join to be single-valued")
	}
	if strings.Contains(strings.Join(postgresSchema, "\n"), "credentials(\n\t\tid TEXT PRIMARY KEY CHECK (id ~ '^cred_[a-f0-9]{32}$'), workspace_id") {
		t.Fatal("credentials must not carry their own workspace_id: it would be a second source of truth that outlives a revoked membership")
	}

	for _, required := range []string{
		// Both principal kinds are covered. A query that joined only
		// memberships would silently omit every service-account credential,
		// and one that joined only service accounts would omit every human's.
		"m.user_id = c.user_id AND m.workspace_id = $1",
		"saw.service_account_id = c.service_account_id AND saw.workspace_id = $1",
		// Revocation has to take effect through the join, not by a separate
		// sweep somebody has to remember to run.
		"m.status = 'active'",
		"saw.status = 'active'",
	} {
		if !strings.Contains(credentialsForWorkspaceQuery, required) {
			t.Errorf("the workspace credential query is missing %q", required)
		}
	}
}

func TestCredentialListingRequiresAWorkspace(t *testing.T) {
	s := &PostgresStore{}
	for _, workspaceID := range []string{"", "   "} {
		if _, err := s.CredentialsForWorkspace(context.Background(), workspaceID); err == nil {
			t.Fatalf("listing credentials with workspace %q was allowed", workspaceID)
		}
	}
}

package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/tenancy"
)

// listingResolver resolves membership and can also enumerate workspaces, which
// is the pairing a real Team deployment provides.
type listingResolver struct {
	recordingMembershipResolver
	workspaces                                                 []tenancy.SubjectWorkspace
	listErr                                                    error
	askedFor                                                   string
	createdName, createOrganization, createSource, createOwner string
	createErr                                                  error
	// denyRequested names a workspace the subject may not select, so a test
	// can establish a valid active context and still be refused a different
	// workspace — which is the only shape in which the selection endpoint is
	// ever reached.
	denyRequested string
}

func (r *listingResolver) CreateWorkspaceForOwner(_ context.Context, _ tenancy.Mutation, organizationID, sourceWorkspaceID, name, ownerUserID string) (tenancy.Workspace, tenancy.StoredMembership, error) {
	r.createdName, r.createOrganization, r.createSource, r.createOwner = name, organizationID, sourceWorkspaceID, ownerUserID
	if r.createErr != nil {
		return tenancy.Workspace{}, tenancy.StoredMembership{}, r.createErr
	}
	workspace := tenancy.Workspace{ID: "ws_created", OrganizationID: organizationID, Name: name}
	membership := tenancy.StoredMembership{ID: "mem_created", OrganizationID: organizationID, WorkspaceID: workspace.ID, UserID: ownerUserID, Role: tenancy.RoleOwner, Status: tenancy.MembershipActive}
	return workspace, membership, nil
}

func (r *listingResolver) ResolveMembership(ctx context.Context, subject, requested string) (tenancy.Membership, error) {
	if r.denyRequested != "" && requested == r.denyRequested {
		return tenancy.Membership{}, tenancy.ErrMembershipNotFound
	}
	return r.recordingMembershipResolver.ResolveMembership(ctx, subject, requested)
}

func (r *listingResolver) ListSubjectWorkspaces(_ context.Context, subject string) ([]tenancy.SubjectWorkspace, error) {
	r.askedFor = subject
	return r.workspaces, r.listErr
}

func identityApp(t *testing.T, resolver tenancy.Resolver, mode string) *fiber.App {
	t.Helper()
	s := withCfg(&Server{log: zap.NewNop()}, &config.Config{Deployment: config.DeploymentConfig{Mode: mode}})
	s.SetTenantResolver(resolver)
	app := fiber.New(fiber.Config{DisableStartupMessage: true, Immutable: true})
	app.Use(func(c *fiber.Ctx) error {
		auth.SetClaims(c, &auth.Claims{
			RegisteredClaims: jwt.RegisteredClaims{Subject: "usr_alice"},
			Role:             "admin", Kind: "access", PrincipalKind: "user",
			CredentialID: "cred_session", Scopes: []string{"agents:read"},
		})
		c.Locals("request_id", "req-identity")
		return c.Next()
	})
	app.Use(s.workspaceContextMW())
	app.Get("/identity", s.handleWorkspaceIdentity)
	app.Get("/workspaces", s.handleListSelectableWorkspaces)
	app.Post("/workspaces", s.handleCreateWorkspace)
	app.Post("/select", s.handleSelectWorkspace)
	return app
}

func TestWorkspaceOwnerCanCreateAnotherWorkspace(t *testing.T) {
	resolver := &listingResolver{recordingMembershipResolver: recordingMembershipResolver{
		membership: tenancy.Membership{OrganizationID: "org_a", WorkspaceID: "ws_a", MembershipID: "mem_a", UserID: "usr_alice", Role: tenancy.RoleOwner},
	}}
	app := identityApp(t, resolver, config.DeploymentModeTeam)
	req := httptest.NewRequest(http.MethodPost, "/workspaces", strings.NewReader(`{"name":"Operations"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status=%d, want 201", resp.StatusCode)
	}
	if resolver.createdName != "Operations" || resolver.createOrganization != "org_a" || resolver.createSource != "ws_a" || resolver.createOwner != "usr_alice" {
		t.Fatalf("creator received wrong verified context: %+v", resolver)
	}
}

func TestWorkspaceOIDCRedirectURLUsesTheBrowserLocalPort(t *testing.T) {
	app := fiber.New(fiber.Config{DisableStartupMessage: true, Immutable: true})
	app.Get("/redirect", func(c *fiber.Ctx) error {
		return c.SendString(workspaceOIDCRedirectURL(c, "http://localhost:18789/api/v1/auth/oidc/callback"))
	})

	req := httptest.NewRequest(http.MethodGet, "http://localhost:1947/redirect", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body := make([]byte, 256)
	n, err := resp.Body.Read(body)
	if err != nil && n == 0 {
		t.Fatal(err)
	}
	if got, want := string(body[:n]), "http://localhost:1947/api/v1/auth/oidc/callback"; got != want {
		t.Fatalf("redirect URL = %q, want %q", got, want)
	}
}

func TestWorkspaceOIDCRedirectURLPreservesPublicHTTPSOverride(t *testing.T) {
	app := fiber.New(fiber.Config{DisableStartupMessage: true, Immutable: true})
	app.Get("/redirect", func(c *fiber.Ctx) error {
		return c.SendString(workspaceOIDCRedirectURL(c, "https://agents.example/api/v1/auth/oidc/callback"))
	})

	req := httptest.NewRequest(http.MethodGet, "http://localhost:1947/redirect", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body := make([]byte, 256)
	n, err := resp.Body.Read(body)
	if err != nil && n == 0 {
		t.Fatal(err)
	}
	if got, want := string(body[:n]), "https://agents.example/api/v1/auth/oidc/callback"; got != want {
		t.Fatalf("redirect URL = %q, want %q", got, want)
	}
}

func TestWorkspaceAdminCannotCreateAnOwnerWorkspace(t *testing.T) {
	resolver := &listingResolver{recordingMembershipResolver: recordingMembershipResolver{
		membership: tenancy.Membership{OrganizationID: "org_a", WorkspaceID: "ws_a", MembershipID: "mem_a", UserID: "usr_alice", Role: tenancy.RoleAdmin},
	}}
	app := identityApp(t, resolver, config.DeploymentModeTeam)
	req := httptest.NewRequest(http.MethodPost, "/workspaces", strings.NewReader(`{"name":"Escalation"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status=%d, want 403", resp.StatusCode)
	}
	if resolver.createdName != "" {
		t.Fatal("admin reached the workspace creator")
	}
}

// The identity endpoint must report the role the server resolved from stored
// membership, not the broader role the caller's token asserts. A CLI that
// showed the token's role would tell an operator they are an admin in a
// workspace where every write is about to be refused.
func TestIdentityEndpointReportsTheVerifiedRoleNotTheTokenRole(t *testing.T) {
	app := identityApp(t, &listingResolver{recordingMembershipResolver: recordingMembershipResolver{
		membership: tenancy.Membership{OrganizationID: "org_a", WorkspaceID: "ws_a", MembershipID: "mem_a", Role: "viewer"},
	}}, config.DeploymentModeTeam)

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/identity", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var identity identityResponse
	if err := json.NewDecoder(resp.Body).Decode(&identity); err != nil {
		t.Fatal(err)
	}
	if identity.Role != "viewer" {
		t.Fatalf("identity reported role %q, want the verified membership role viewer", identity.Role)
	}
	if identity.WorkspaceID != "ws_a" || identity.OrganizationID != "org_a" || identity.MembershipID != "mem_a" {
		t.Fatalf("identity does not describe the verified membership: %+v", identity)
	}
	if identity.DeploymentMode != config.DeploymentModeTeam {
		t.Fatalf("identity reported deployment mode %q", identity.DeploymentMode)
	}
}

func TestIdentityEndpointProjectsPersonalCapabilityPermissionsForWorkspaceOwner(t *testing.T) {
	app := identityApp(t, &listingResolver{recordingMembershipResolver: recordingMembershipResolver{
		membership: tenancy.Membership{OrganizationID: "org_a", WorkspaceID: "ws_a", MembershipID: "mem_a", Role: tenancy.RoleOwner},
	}}, config.DeploymentModeTeam)

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/identity", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var identity identityResponse
	if err := json.NewDecoder(resp.Body).Decode(&identity); err != nil {
		t.Fatal(err)
	}

	// Team/Scale wrap the Personal product in a tenant boundary; they do not
	// replace it with a smaller product. Keep every tenant-safe capability in
	// the identity projection so the GUI cannot silently hide working pages.
	for _, resource := range []string{
		"agents", "chat", "memory", "knowledge", "channels", "schedule",
		"skills", "mcp", "plugins", "providers", "secrets", "config",
	} {
		actions := identity.Permissions[resource]
		foundRead := false
		for _, action := range actions {
			if action == "read" || (resource == "secrets" && action == "list") {
				foundRead = true
				break
			}
		}
		if !foundRead {
			t.Errorf("owner identity is missing usable %s permission: %v", resource, actions)
		}
	}
}

// Scopes must serialize as an empty array rather than null: `--json` consumers
// index into it, and a stable schema is an explicit acceptance criterion.
func TestIdentityEndpointEmitsAStableSchema(t *testing.T) {
	app := identityApp(t, &listingResolver{recordingMembershipResolver: recordingMembershipResolver{
		membership: tenancy.Membership{OrganizationID: "org_a", WorkspaceID: "ws_a", MembershipID: "mem_a", Role: "owner"},
	}}, config.DeploymentModePersonal)
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/identity", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"subject", "principal_kind", "organization_id", "workspace_id", "membership_id", "role", "scopes", "deployment_mode"} {
		if _, ok := raw[required]; !ok {
			t.Errorf("identity response is missing the %q field", required)
		}
	}
	if string(raw["scopes"]) == "null" {
		t.Error("scopes serialized as null; it must always be an array")
	}
}

func TestWorkspaceListingComesFromStoredMembershipsForThisSubject(t *testing.T) {
	resolver := &listingResolver{
		recordingMembershipResolver: recordingMembershipResolver{
			membership: tenancy.Membership{OrganizationID: "org_a", WorkspaceID: "ws_a", MembershipID: "mem_a", Role: "developer"},
		},
		workspaces: []tenancy.SubjectWorkspace{
			{OrganizationID: "org_a", OrganizationName: "Acme", WorkspaceID: "ws_a", WorkspaceName: "Engineering", MembershipID: "mem_a", Role: "developer", PrincipalKind: "user"},
			{OrganizationID: "org_a", OrganizationName: "Acme", WorkspaceID: "ws_b", WorkspaceName: "Operations", MembershipID: "mem_b", Role: "viewer", PrincipalKind: "user"},
		},
	}
	app := identityApp(t, resolver, config.DeploymentModeTeam)
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/workspaces", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var listing workspacesResponse
	if err := json.NewDecoder(resp.Body).Decode(&listing); err != nil {
		t.Fatal(err)
	}
	if resolver.askedFor != "usr_alice" {
		t.Fatalf("workspaces were listed for %q, want the authenticated subject", resolver.askedFor)
	}
	if len(listing.Workspaces) != 2 || listing.ActiveWorkspaceID != "ws_a" {
		t.Fatalf("unexpected listing: %+v", listing)
	}
}

// A deployment that cannot enumerate workspaces must say so. Returning just
// the active workspace would be indistinguishable from "you belong to exactly
// one", which is a different and misleading fact.
func TestWorkspaceListingFailsClosedWhenEnumerationIsUnsupported(t *testing.T) {
	app := identityApp(t, &recordingMembershipResolver{
		membership: tenancy.Membership{OrganizationID: "org_a", WorkspaceID: "ws_a", MembershipID: "mem_a", Role: "owner"},
	}, config.DeploymentModeTeam)
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/workspaces", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want 503", resp.StatusCode)
	}
}

// Selecting a workspace is a server decision. An unauthorized workspace is
// reported as not found so the response cannot be used to discover which
// workspace IDs exist.
func TestWorkspaceSelectionIsServerVerifiedAndDoesNotLeakExistence(t *testing.T) {
	app := identityApp(t, &listingResolver{
		recordingMembershipResolver: recordingMembershipResolver{
			membership: tenancy.Membership{OrganizationID: "org_a", WorkspaceID: "ws_a", MembershipID: "mem_a", Role: "owner"},
		},
		denyRequested: "ws_someone_else",
	}, config.DeploymentModeTeam)
	req := httptest.NewRequest(http.MethodPost, "/select", strings.NewReader(`{"workspace_id":"ws_someone_else"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d, want 404 for a workspace the subject cannot use", resp.StatusCode)
	}
	body := make(map[string]any)
	_ = json.NewDecoder(resp.Body).Decode(&body)
	encoded, _ := json.Marshal(body)
	if strings.Contains(strings.ToLower(string(encoded)), "forbidden") || strings.Contains(string(encoded), "ws_someone_else") {
		t.Fatalf("selection failure discloses target metadata: %s", encoded)
	}
}

func TestWorkspaceSelectionEchoesTheVerifiedMembership(t *testing.T) {
	resolver := &listingResolver{recordingMembershipResolver: recordingMembershipResolver{
		membership: tenancy.Membership{OrganizationID: "org_a", WorkspaceID: "ws_b", MembershipID: "mem_b", Role: "viewer"},
	}}
	app := identityApp(t, resolver, config.DeploymentModeTeam)
	req := httptest.NewRequest(http.MethodPost, "/select", strings.NewReader(`{"workspace_id":"ws_b"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var selected tenancy.SubjectWorkspace
	if err := json.NewDecoder(resp.Body).Decode(&selected); err != nil {
		t.Fatal(err)
	}
	if selected.WorkspaceID != "ws_b" || selected.Role != "viewer" || selected.OrganizationID != "org_a" {
		t.Fatalf("selection did not echo the verified membership: %+v", selected)
	}
}

func TestPersonalResolverEnumeratesItsSingleWorkspace(t *testing.T) {
	resolver := tenancy.NewPersonalResolver(tenancy.PersonalTenant{
		OrganizationID: "org_personal", WorkspaceID: "ws_personal",
		UserID: "usr_local_owner", MembershipID: "mem_personal_owner",
	})
	workspaces, err := resolver.ListSubjectWorkspaces(context.Background(), "local-owner")
	if err != nil {
		t.Fatal(err)
	}
	if len(workspaces) != 1 || workspaces[0].WorkspaceID != "ws_personal" || workspaces[0].Role != "owner" {
		t.Fatalf("personal enumeration returned %+v", workspaces)
	}
	if _, err := resolver.ListSubjectWorkspaces(context.Background(), " "); !errors.Is(err, tenancy.ErrMembershipNotFound) {
		t.Fatalf("an empty subject enumerated workspaces: %v", err)
	}
}

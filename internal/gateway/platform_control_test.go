package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/tenancy"
)

type fakePlatformCatalog struct{}

func (fakePlatformCatalog) ResolveMembership(context.Context, string, string) (tenancy.Membership, error) {
	return tenancy.Membership{}, tenancy.ErrMembershipNotFound
}
func (fakePlatformCatalog) PlatformOverview(context.Context) (tenancy.PlatformOverview, error) {
	return tenancy.PlatformOverview{Organizations: 2, Workspaces: 3, ActiveWorkspaces: 3, Users: 4, ActiveMemberships: 5}, nil
}
func (fakePlatformCatalog) ListPlatformOrganizations(context.Context) ([]tenancy.PlatformOrganization, error) {
	return []tenancy.PlatformOrganization{{ID: "org_safe", Name: "Acme", CreatedAt: time.Unix(1, 0).UTC(), Workspaces: []tenancy.PlatformWorkspace{{ID: "ws_safe", Name: "Production", Status: "active", ActiveMembers: 2}}}}, nil
}
func (fakePlatformCatalog) ProvisionOrganization(_ context.Context, mutation tenancy.Mutation, request tenancy.BootstrapRequest) (tenancy.BootstrapResult, error) {
	return tenancy.BootstrapResult{Organization: tenancy.Organization{ID: "org_new", Name: request.OrganizationName}, Workspace: tenancy.Workspace{ID: "ws_new", OrganizationID: "org_new", Name: request.WorkspaceName}, User: tenancy.User{ID: "usr_owner", Email: request.OwnerEmail, DisplayName: request.OwnerDisplayName}, Membership: tenancy.StoredMembership{ID: "mem_owner", OrganizationID: "org_new", WorkspaceID: "ws_new", UserID: "usr_owner", Role: "owner", Status: "active"}}, nil
}
func (fakePlatformCatalog) ProvisionWorkspace(_ context.Context, mutation tenancy.Mutation, organizationID string, request tenancy.WorkspaceProvisionRequest) (tenancy.BootstrapResult, error) {
	return tenancy.BootstrapResult{Organization: tenancy.Organization{ID: organizationID, Name: "Acme"}, Workspace: tenancy.Workspace{ID: "ws_more", OrganizationID: organizationID, Name: request.WorkspaceName}, User: tenancy.User{ID: "usr_owner", Email: request.OwnerEmail, DisplayName: request.OwnerDisplayName}, Membership: tenancy.StoredMembership{ID: "mem_more", OrganizationID: organizationID, WorkspaceID: "ws_more", UserID: "usr_owner", Role: "owner", Status: "active"}}, nil
}

func TestPlatformControlPlaneUsesDeploymentCredentialAndReturnsMetadataOnly(t *testing.T) {
	s := newTestGateway(t, "deployment-key")
	s.mutateConfig(func(cfg *config.Config) { cfg.Deployment.Mode = config.DeploymentModeTeam })
	s.SetTenantResolver(fakePlatformCatalog{})

	status, overview := gatewayJSON(t, s, http.MethodGet, "/api/v1/admin/platform/overview", "deployment-key", "")
	if status != http.StatusOK {
		t.Fatalf("overview status=%d body=%#v", status, overview)
	}
	if overview["summary"].(map[string]any)["workspaces"] != float64(3) {
		t.Fatalf("overview summary=%#v", overview["summary"])
	}

	status, organizations := gatewayJSON(t, s, http.MethodGet, "/api/v1/admin/platform/organizations", "deployment-key", "")
	if status != http.StatusOK {
		t.Fatalf("organizations status=%d body=%#v", status, organizations)
	}
	raw, _ := json.Marshal(organizations)
	for _, forbidden := range []string{"email", "conversation", "secret", "agent"} {
		if strings.Contains(strings.ToLower(string(raw)), forbidden) {
			t.Fatalf("platform response exposed tenant field %q: %s", forbidden, raw)
		}
	}

	status, _ = gatewayJSON(t, s, http.MethodGet, "/api/v1/admin/platform/overview", "wrong-key", "")
	if status != http.StatusUnauthorized {
		t.Fatalf("non-platform credential status=%d, want 401", status)
	}
}

func TestPlatformAdministratorProvisionsTenantsWithoutBecomingAMember(t *testing.T) {
	s := newTestGateway(t, "deployment-key")
	s.mutateConfig(func(cfg *config.Config) { cfg.Deployment.Mode = config.DeploymentModeTeam })
	s.SetTenantResolver(fakePlatformCatalog{})
	payload := `{"organization_name":"New Co","workspace_name":"Primary","owner_email":"owner@example.com","owner_display_name":"Owner"}`
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/admin/platform/organizations", "deployment-key", payload)
	if status != http.StatusCreated {
		t.Fatalf("provision status=%d body=%#v", status, body)
	}
	result := body["result"].(map[string]any)
	membership := result["membership"].(map[string]any)
	if membership["user_id"] != "usr_owner" || membership["role"] != "owner" {
		t.Fatalf("membership=%#v", membership)
	}
	raw, _ := json.Marshal(body)
	if strings.Contains(string(raw), "platform:administrator") {
		t.Fatalf("platform actor became tenant data: %s", raw)
	}

	workspacePayload := `{"workspace_name":"Operations","owner_email":"owner@example.com","owner_display_name":"Owner"}`
	status, body = gatewayJSON(t, s, http.MethodPost, "/api/v1/admin/platform/organizations/org_safe/workspaces", "deployment-key", workspacePayload)
	if status != http.StatusCreated {
		t.Fatalf("workspace provision status=%d body=%#v", status, body)
	}
}

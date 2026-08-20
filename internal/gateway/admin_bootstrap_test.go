package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/tenancy"
)

type fakeBootstrapResolver struct{ done bool }

func (f *fakeBootstrapResolver) ResolveMembership(context.Context, string, string) (tenancy.Membership, error) {
	return tenancy.Membership{}, tenancy.ErrMembershipNotFound
}
func (f *fakeBootstrapResolver) BootstrapState(context.Context) (tenancy.BootstrapState, error) {
	return tenancy.BootstrapState{Required: !f.done, Organizations: boolCount(f.done), Workspaces: boolCount(f.done), Memberships: boolCount(f.done)}, nil
}
func (f *fakeBootstrapResolver) BootstrapFirstOwner(_ context.Context, _ tenancy.Mutation, req tenancy.BootstrapRequest) (tenancy.BootstrapResult, error) {
	if f.done {
		return tenancy.BootstrapResult{}, tenancy.ErrAlreadyBootstrapped
	}
	if req.OwnerEmail == "" {
		return tenancy.BootstrapResult{}, errors.New("owner email is required")
	}
	f.done = true
	return tenancy.BootstrapResult{Organization: tenancy.Organization{ID: "org_test", Name: req.OrganizationName}, Workspace: tenancy.Workspace{ID: "ws_test", OrganizationID: "org_test", Name: req.WorkspaceName}, User: tenancy.User{ID: "usr_test", Email: req.OwnerEmail, DisplayName: req.OwnerDisplayName}}, nil
}
func boolCount(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

func TestAdminBootstrapIsPlatformOnlyAndOneTime(t *testing.T) {
	s := newTestGateway(t, "deployment-key")
	s.mutateConfig(func(cfg *config.Config) { cfg.Deployment.Mode = config.DeploymentModeTeam })
	store := &fakeBootstrapResolver{}
	s.SetTenantResolver(store)

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/admin/bootstrap", "deployment-key", "")
	if status != http.StatusOK || body["state"].(map[string]any)["required"] != true {
		t.Fatalf("initial state = %d %#v", status, body)
	}
	payload := `{"organization_name":"Acme","workspace_name":"Main","owner_email":"owner@example.com","owner_display_name":"Owner"}`
	status, _ = gatewayJSON(t, s, http.MethodPost, "/api/v1/admin/bootstrap", "deployment-key", payload)
	if status != http.StatusCreated {
		t.Fatalf("bootstrap status = %d", status)
	}
	status, _ = gatewayJSON(t, s, http.MethodPost, "/api/v1/admin/bootstrap", "deployment-key", payload)
	if status != http.StatusConflict {
		t.Fatalf("second bootstrap status = %d", status)
	}
}

func TestAuthConfigPatchAndRedaction(t *testing.T) {
	var patch PatchableConfig
	if err := json.Unmarshal([]byte(`{"auth":{"mode":"jwt","oidc_issuer":"https://accounts.google.com","oidc_client_id":"client","oidc_client_secret":"secret","oidc_redirect_url":"https://agents.example/api/v1/auth/oidc/callback","oidc_scopes":["openid","email"]}}`), &patch); err != nil {
		t.Fatal(err)
	}
	raw := map[string]any{}
	applyPatch(raw, patch)
	auth := raw["auth"].(map[string]any)
	if auth["oidc_client_secret"] != "secret" || auth["mode"] != "jwt" {
		t.Fatalf("auth patch = %#v", auth)
	}
	if sections := restartRequiredSections(patch); len(sections) != 1 || sections[0] != "auth" {
		t.Fatalf("restart sections = %#v", sections)
	}

	s := newTestGateway(t, "secret")
	s.mutateConfig(func(cfg *config.Config) { cfg.Auth.OIDCClientSecret = "do-not-return" })
	view := s.safeConfigView()["auth"].(fiber.Map)
	if view["oidc_client_secret"] != "***" {
		t.Fatalf("secret was not redacted: %#v", view)
	}
}

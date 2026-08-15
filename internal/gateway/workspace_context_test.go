package gateway

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/requestctx"
	"github.com/soulacy/soulacy/internal/tenancy"
)

type recordingMembershipResolver struct {
	requested  string
	membership tenancy.Membership
	err        error
}

func (r *recordingMembershipResolver) ResolveMembership(_ context.Context, _ string, requested string) (tenancy.Membership, error) {
	r.requested = requested
	return r.membership, r.err
}

func TestWorkspaceContextUsesOnlyVerifiedMembershipAuthority(t *testing.T) {
	s := &Server{cfg: &config.Config{Deployment: config.DeploymentConfig{Mode: config.DeploymentModePersonal}}, log: zap.NewNop()}
	r := &recordingMembershipResolver{membership: tenancy.Membership{
		OrganizationID: "org_verified", WorkspaceID: "ws_verified", MembershipID: "mem_verified", Role: "owner",
	}}
	s.SetTenantResolver(r)
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		auth.SetClaims(c, &auth.Claims{
			RegisteredClaims: jwt.RegisteredClaims{Subject: "user-1", ID: "jwt-1"},
			Role:             "viewer", Kind: "access", Scopes: []string{"chat"},
		})
		c.Locals("request_id", "req-1")
		return c.Next()
	})
	app.Use(s.workspaceContextMW())
	app.Post("/protected", func(c *fiber.Ctx) error {
		id, ok := requestctx.From(c.UserContext())
		if !ok {
			t.Fatal("verified identity missing from request context")
		}
		return c.JSON(fiber.Map{"workspace": id.WorkspaceID(), "role": id.Role(), "credential": id.CredentialID()})
	})

	req := httptest.NewRequest(http.MethodPost, "/protected?workspace_id=ws_query_spoof", bytes.NewBufferString(`{"workspace_id":"ws_body_spoof","role":"admin"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Soulacy-Workspace", "ws_selector")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != `{"credential":"jwt-1","role":"viewer","workspace":"ws_verified"}` {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	if r.requested != "ws_selector" {
		t.Fatalf("resolver selector = %q", r.requested)
	}
}

func TestWorkspaceContextMapsLegacyPersonalCredentialAliases(t *testing.T) {
	s := &Server{cfg: &config.Config{Deployment: config.DeploymentConfig{Mode: config.DeploymentModePersonal}}, log: zap.NewNop()}
	r := &recordingMembershipResolver{membership: tenancy.Membership{OrganizationID: "org_real", WorkspaceID: "ws_real", MembershipID: "mem_real", Role: "owner"}}
	s.SetTenantResolver(r)
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		auth.SetClaims(c, &auth.Claims{RegisteredClaims: jwt.RegisteredClaims{Subject: "local-owner"}, Role: "operator", Kind: "access", OrganizationID: "org_personal", WorkspaceID: "ws_personal", WorkspaceIDs: []string{"ws_personal"}})
		c.Locals("request_id", "req-legacy-personal")
		return c.Next()
	})
	app.Use(s.workspaceContextMW())
	app.Get("/protected", func(c *fiber.Ctx) error {
		identity, _ := requestctx.From(c.UserContext())
		return c.JSON(fiber.Map{"organization": identity.OrganizationID(), "workspace": identity.WorkspaceID()})
	})
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/protected", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != `{"organization":"org_real","workspace":"ws_real"}` || r.requested != "" {
		t.Fatalf("status=%d body=%s requested=%q", resp.StatusCode, body, r.requested)
	}
}

func TestWorkspaceContextUsesWorkspaceRoleInMultiUserMode(t *testing.T) {
	s := &Server{cfg: &config.Config{Deployment: config.DeploymentConfig{Mode: config.DeploymentModeTeam}}, log: zap.NewNop()}
	s.SetTenantResolver(&recordingMembershipResolver{membership: tenancy.Membership{
		OrganizationID: "org_verified", WorkspaceID: "ws_verified", MembershipID: "mem_verified", Role: "viewer",
	}})
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		auth.SetClaims(c, &auth.Claims{
			RegisteredClaims: jwt.RegisteredClaims{Subject: "user-1"},
			Role:             "admin", Kind: "access",
		})
		c.Locals("request_id", "req-team-role")
		return c.Next()
	})
	app.Use(s.workspaceContextMW())
	app.Get("/protected", func(c *fiber.Ctx) error {
		identity, _ := requestctx.From(c.UserContext())
		return c.SendString(identity.Role())
	})

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/protected", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "viewer" {
		t.Fatalf("status=%d role=%q, want verified workspace role viewer", resp.StatusCode, body)
	}
}

func TestWorkspaceContextRejectsStaticServerKeyInTeamMode(t *testing.T) {
	s := &Server{cfg: &config.Config{Deployment: config.DeploymentConfig{Mode: config.DeploymentModeTeam}}, log: zap.NewNop()}
	s.SetTenantResolver(&recordingMembershipResolver{membership: tenancy.Membership{OrganizationID: "org_a", WorkspaceID: "ws_a", MembershipID: "mem_a", Role: "owner"}})
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		auth.SetClaims(c, &auth.Claims{RegisteredClaims: jwt.RegisteredClaims{Subject: "api-key"}, Role: "admin", Kind: "access", PrincipalKind: "static_api_key", CredentialID: "static-api-key"})
		c.Locals("request_id", "req-static")
		return c.Next()
	})
	app.Use(s.workspaceContextMW())
	app.Get("/protected", func(c *fiber.Ctx) error { return c.SendStatus(http.StatusNoContent) })
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/protected", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status=%d, want 403", resp.StatusCode)
	}
}

func TestWorkspaceContextEnforcesCredentialWorkspaceAndOrganizationBindings(t *testing.T) {
	for _, tc := range []struct{ name, header, org string }{
		{name: "workspace", header: "ws_other", org: "org_a"},
		{name: "organization", header: "ws_allowed", org: "org_other"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{cfg: &config.Config{Deployment: config.DeploymentConfig{Mode: config.DeploymentModeTeam}}, log: zap.NewNop()}
			s.SetTenantResolver(&recordingMembershipResolver{membership: tenancy.Membership{OrganizationID: "org_a", WorkspaceID: tc.header, MembershipID: "svc_bind", Role: "operator"}})
			app := fiber.New()
			app.Use(func(c *fiber.Ctx) error {
				auth.SetClaims(c, &auth.Claims{RegisteredClaims: jwt.RegisteredClaims{Subject: "svc_bot"}, Role: "operator", Kind: "access", PrincipalKind: "service_account", CredentialID: "cred_1", OrganizationID: tc.org, WorkspaceIDs: []string{"ws_allowed"}})
				c.Locals("request_id", "req-bound")
				return c.Next()
			})
			app.Use(s.workspaceContextMW())
			app.Get("/protected", func(c *fiber.Ctx) error { return c.SendStatus(http.StatusNoContent) })
			req := httptest.NewRequest(http.MethodGet, "/protected", nil)
			req.Header.Set("X-Soulacy-Workspace", tc.header)
			resp, err := app.Test(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("status=%d, want 403", resp.StatusCode)
			}
		})
	}
}

func TestAllProtectedAPIRoutesFailClosedWithoutWorkspaceResolverInTeamMode(t *testing.T) {
	s := newTestGateway(t, "secret")
	s.cfg.Deployment.Mode = config.DeploymentModeTeam
	s.tenantResolver = nil

	for _, target := range []string{"/api/v1/health", "/api/v1/agents", "/api/v1/config"} {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.Header.Set("Authorization", "Bearer secret")
		resp, err := s.app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s status=%d, want %d", target, resp.StatusCode, http.StatusForbidden)
		}
	}

	resp, err := s.app.Test(httptest.NewRequest(http.MethodGet, "/ping", nil))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("public ping status=%d", resp.StatusCode)
	}
}

func TestProtectedRouteArchitectureIncludesAuthWorkspaceAndAuthorizationGates(t *testing.T) {
	s := newTestGateway(t, "secret")
	s.cfg.Deployment.Mode = config.DeploymentModeTeam
	s.tenantResolver = nil
	public := map[string]bool{
		"/api/v1/auth/token": true, "/api/v1/auth/refresh": true, "/api/v1/auth/logout": true,
		"/api/v1/auth/oidc/config": true, "/api/v1/auth/oidc/start": true, "/api/v1/auth/oidc/callback": true, "/api/v1/auth/oidc/complete": true,
		"/api/v1/auth/oidc/device/start": true, "/api/v1/auth/oidc/device/poll": true,
		// Authenticated but intentionally pre-workspace: invitees do not have a
		// membership until this endpoint succeeds.
		"/api/v1/invitations/accept": true,
		"/api/v1/shared/:token":      true,
	}
	for _, route := range s.app.GetRoutes(true) {
		if !strings.HasPrefix(route.Path, "/api/v1/") || public[route.Path] || route.Method == fiber.MethodHead {
			continue
		}
		target := route.Path
		for _, param := range route.Params {
			target = strings.ReplaceAll(target, ":"+param, "route-test")
		}
		target = strings.ReplaceAll(target, "*", "route-test")
		unauthenticated := httptest.NewRequest(route.Method, target, nil)
		resp, err := s.app.Test(unauthenticated)
		if err != nil {
			t.Fatalf("%s %s: %v", route.Method, route.Path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s bypassed authentication: status=%d", route.Method, route.Path, resp.StatusCode)
		}

		authenticated := httptest.NewRequest(route.Method, target, nil)
		authenticated.Header.Set("Authorization", "Bearer secret")
		resp, err = s.app.Test(authenticated)
		if err != nil {
			t.Fatalf("%s %s: %v", route.Method, route.Path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s accepted the Team static key or bypassed workspace authorization: status=%d", route.Method, route.Path, resp.StatusCode)
		}
	}
}

func TestInvitationAcceptanceRequiresAuthenticationBeforeWorkspaceMembership(t *testing.T) {
	s := newTestGateway(t, "secret")
	s.cfg.Deployment.Mode = config.DeploymentModeTeam
	req := httptest.NewRequest(http.MethodPost, "/api/v1/invitations/accept", strings.NewReader(`{"token":"not-a-real-token"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d, want authentication rejection before invitation handling", resp.StatusCode)
	}
}

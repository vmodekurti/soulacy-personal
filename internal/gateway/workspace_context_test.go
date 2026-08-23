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
	s := withCfg(&Server{log: zap.NewNop()}, &config.Config{Deployment: config.DeploymentConfig{Mode: config.DeploymentModePersonal}})
	r := &recordingMembershipResolver{membership: tenancy.Membership{
		OrganizationID: "org_verified", WorkspaceID: "ws_verified", MembershipID: "mem_verified", Role: "owner",
	}}
	s.SetTenantResolver(r)
	app := fiber.New(fiber.Config{Immutable: true})
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

func TestWebSocketWorkspaceSelectorUsesVerifiedMembership(t *testing.T) {
	s := withCfg(&Server{log: zap.NewNop()}, &config.Config{Deployment: config.DeploymentConfig{Mode: config.DeploymentModeTeam}})
	r := &recordingMembershipResolver{membership: tenancy.Membership{
		OrganizationID: "org_verified", WorkspaceID: "ws_verified", MembershipID: "mem_verified", Role: "owner",
	}}
	s.SetTenantResolver(r)
	app := fiber.New(fiber.Config{Immutable: true})
	app.Use(func(c *fiber.Ctx) error {
		auth.SetClaims(c, &auth.Claims{
			RegisteredClaims: jwt.RegisteredClaims{Subject: "user-1"},
			Role:             "owner", Kind: "access", PrincipalKind: "user", WorkspaceID: "ws_login",
		})
		c.Locals("request_id", "req-ws-events")
		return c.Next()
	})
	app.Use(s.websocketWorkspaceContextMW())
	app.Get("/ws/events", func(c *fiber.Ctx) error {
		identity, ok := requestctx.From(c.UserContext())
		if !ok {
			t.Fatal("verified WebSocket identity missing")
		}
		return c.SendString(identity.WorkspaceID())
	})

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/ws/events?workspace_id=ws_selected", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "ws_verified" {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	if r.requested != "ws_selected" {
		t.Fatalf("resolver selector = %q, want ws_selected", r.requested)
	}
}

func TestWorkspaceContextMapsLegacyPersonalCredentialAliases(t *testing.T) {
	s := withCfg(&Server{log: zap.NewNop()}, &config.Config{Deployment: config.DeploymentConfig{Mode: config.DeploymentModePersonal}})
	r := &recordingMembershipResolver{membership: tenancy.Membership{OrganizationID: "org_real", WorkspaceID: "ws_real", MembershipID: "mem_real", Role: "owner"}}
	s.SetTenantResolver(r)
	app := fiber.New(fiber.Config{Immutable: true})
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
	s := withCfg(&Server{log: zap.NewNop()}, &config.Config{Deployment: config.DeploymentConfig{Mode: config.DeploymentModeTeam}})
	s.SetTenantResolver(&recordingMembershipResolver{membership: tenancy.Membership{
		OrganizationID: "org_verified", WorkspaceID: "ws_verified", MembershipID: "mem_verified", Role: "viewer",
	}})
	app := fiber.New(fiber.Config{Immutable: true})
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

func TestHumanSessionWorkspaceSelectorOverridesItsMintedWorkspace(t *testing.T) {
	s := withCfg(&Server{log: zap.NewNop()}, &config.Config{Deployment: config.DeploymentConfig{Mode: config.DeploymentModeTeam}})
	r := &recordingMembershipResolver{membership: tenancy.Membership{
		OrganizationID: "org_verified", WorkspaceID: "ws_new", MembershipID: "mem_new", Role: "owner",
	}}
	s.SetTenantResolver(r)
	app := fiber.New(fiber.Config{Immutable: true})
	app.Use(func(c *fiber.Ctx) error {
		auth.SetClaims(c, &auth.Claims{
			RegisteredClaims: jwt.RegisteredClaims{Subject: "usr_owner"},
			Kind:             "access", PrincipalKind: "user", OrganizationID: "org_minted", WorkspaceID: "ws_minted",
		})
		c.Locals("request_id", "req-switch")
		return c.Next()
	})
	app.Use(s.workspaceContextMW())
	app.Get("/protected", func(c *fiber.Ctx) error {
		identity, _ := requestctx.From(c.UserContext())
		return c.SendString(identity.WorkspaceID())
	})
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("X-Soulacy-Workspace", "ws_new")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "ws_new" || r.requested != "ws_new" {
		t.Fatalf("status=%d body=%q selector=%q", resp.StatusCode, body, r.requested)
	}
}

func TestLegacyOIDCAccessTokenDefaultsToHumanPrincipal(t *testing.T) {
	claims := &auth.Claims{Kind: "access"}
	if got := principalKind(claims); got != "user" {
		t.Fatalf("legacy OIDC principal kind = %q, want user", got)
	}
}

func TestWorkspaceContextRejectsStaticServerKeyInTeamMode(t *testing.T) {
	s := withCfg(&Server{log: zap.NewNop()}, &config.Config{Deployment: config.DeploymentConfig{Mode: config.DeploymentModeTeam}})
	s.SetTenantResolver(&recordingMembershipResolver{membership: tenancy.Membership{OrganizationID: "org_a", WorkspaceID: "ws_a", MembershipID: "mem_a", Role: "owner"}})
	app := fiber.New(fiber.Config{Immutable: true})
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
			s := withCfg(&Server{log: zap.NewNop()}, &config.Config{Deployment: config.DeploymentConfig{Mode: config.DeploymentModeTeam}})
			s.SetTenantResolver(&recordingMembershipResolver{membership: tenancy.Membership{OrganizationID: "org_a", WorkspaceID: tc.header, MembershipID: "svc_bind", Role: "operator"}})
			app := fiber.New(fiber.Config{Immutable: true})
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
	s.config().Deployment.Mode = config.DeploymentModeTeam
	s.tenantResolver = nil

	// WORKSPACE routes fail closed: without a resolver there is no membership
	// to verify, and a workspace route with no verified membership must refuse.
	for _, target := range []string{"/api/v1/health", "/api/v1/agents"} {
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

	// /api/v1/config is a PLATFORM route, and the assertion inverts with it.
	// It changes the deployment rather than a workspace, so there is no
	// membership it could ever need and the bootstrap credential is exactly
	// the right authority — including, in fact especially, when the tenancy
	// catalog is unreachable and the operator is trying to fix it. A route
	// that failed closed here would lock the operator out of the settings at
	// the moment the settings are what is broken.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/config", nil)
	req.Header.Set("Authorization", "Bearer secret")
	configResp, err := s.app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = configResp.Body.Close()
	if configResp.StatusCode == http.StatusForbidden || configResp.StatusCode == http.StatusUnauthorized {
		t.Fatalf("the deployment's own credential was refused its own config: status=%d",
			configResp.StatusCode)
	}
	if !isPlatformRoute(http.MethodGet, "/api/v1/config") {
		t.Fatal("this test assumes /api/v1/config is a platform route and it no longer is")
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

// routesPerGateway bounds how many routes one gateway is asked about.
//
// The gateway limits each client IP to 600 requests a minute and this test
// makes two per route, so once the API grew past 300 routes the later ones
// began answering 429 — which this test reads as "bypassed authentication".
// That is a failure whose cause is the SIZE of the route table rather than
// anything about a route's middleware, and whose message points at a security
// regression that is not there.
//
// The budget is refreshed by BUILDING A NEW GATEWAY, and the two obvious
// alternatives are both worse:
//
//   - Disabling the limiter for the test would stop it exercising the
//     middleware stack a real request traverses, which is the entire subject.
//   - Varying the client address does not work at all. app.Test synthesises
//     the connection, and Fiber's c.IP() reads that rather than the
//     http.Request's RemoteAddr, so every synthetic request shares one key.
//
// Raising the limit would work today and re-break on the batch of routes after
// next, producing the same misleading failure for the next person.
const routesPerGateway = 200

func TestProtectedRouteArchitectureIncludesAuthWorkspaceAndAuthorizationGates(t *testing.T) {
	s := newTestGateway(t, "secret")
	s.config().Deployment.Mode = config.DeploymentModeTeam
	s.tenantResolver = nil
	public := map[string]bool{
		"/api/v1/auth/token": true, "/api/v1/auth/refresh": true, "/api/v1/auth/logout": true,
		"/api/v1/auth/oidc/config": true, "/api/v1/auth/oidc/start": true, "/api/v1/auth/oidc/callback": true, "/api/v1/auth/oidc/complete": true,
		"/api/v1/auth/oidc/device/start": true, "/api/v1/auth/oidc/device/poll": true,
		// Workspace discovery and one-time identity activation happen before a
		// workspace can authenticate. The setup mutation is authorized by its
		// expiring, single-use high-entropy token.
		"/api/v1/auth/workspaces/:id/config": true, "/api/v1/auth/workspaces/:id/setup": true,
		// Authenticated but intentionally pre-workspace: invitees do not have a
		// membership until this endpoint succeeds.
		"/api/v1/invitations/accept": true,
		// Signup configuration is public. Session inspection and creation are
		// authenticated but intentionally pre-workspace: the verified onboarding
		// principal has no membership until creation succeeds.
		"/api/v1/signup/config": true, "/api/v1/signup": true,
		"/api/v1/shared/:token": true,
	}
	var protected []fiber.Route
	for _, route := range s.app.GetRoutes(true) {
		if !strings.HasPrefix(route.Path, "/api/v1/") || public[route.Path] || route.Method == fiber.MethodHead {
			continue
		}
		protected = append(protected, route)
	}
	if len(protected) == 0 {
		t.Fatal("no protected routes found; this test would pass for any code")
	}

	probed := 0
	for start := 0; start < len(protected); start += routesPerGateway {
		if start > 0 {
			// A fresh gateway is a fresh limiter store. Same config, same
			// middleware chain — only the request budget is new.
			s = newTestGateway(t, "secret")
			s.config().Deployment.Mode = config.DeploymentModeTeam
			s.tenantResolver = nil
		}
		end := min(start+routesPerGateway, len(protected))
		for _, route := range protected[start:end] {
			probed++
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
			if isPlatformRoute(route.Method, route.Path) {
				// The bearer here IS the deployment's bootstrap key, and these
				// routes are the ones it exists to open. Refusing it would
				// mean nothing can administer the deployment at all.
				//
				// The other direction — a workspace member refused — is
				// asserted in platformroutes_test.go, because it cannot be
				// expressed here: this loop authenticates with the static key,
				// and any other bearer fails authentication at 401 before
				// authorization is reached.
				if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
					t.Errorf("%s %s refused the deployment's own credential: status=%d",
						route.Method, route.Path, resp.StatusCode)
				}
				continue
			}
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("%s %s accepted the Team static key or bypassed workspace authorization: status=%d", route.Method, route.Path, resp.StatusCode)
			}
		}
	}
	if probed != len(protected) {
		t.Fatalf("probed %d of %d protected routes; the batching dropped some", probed, len(protected))
	}
}

func TestInvitationAcceptanceRequiresAuthenticationBeforeWorkspaceMembership(t *testing.T) {
	s := newTestGateway(t, "secret")
	s.config().Deployment.Mode = config.DeploymentModeTeam
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

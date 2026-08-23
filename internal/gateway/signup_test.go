package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/tenancy"
)

type signupProvisioner struct {
	subject string
	request tenancy.BootstrapRequest
	err     error
}

func (*signupProvisioner) ResolveMembership(context.Context, string, string) (tenancy.Membership, error) {
	return tenancy.Membership{}, tenancy.ErrMembershipNotFound
}
func (p *signupProvisioner) ProvisionSelfServiceOrganization(_ context.Context, _ tenancy.Mutation, subject string, request tenancy.BootstrapRequest) (tenancy.BootstrapResult, error) {
	p.subject, p.request = subject, request
	if p.err != nil {
		return tenancy.BootstrapResult{}, p.err
	}
	return tenancy.BootstrapResult{
		Organization: tenancy.Organization{ID: "org_new", Name: request.OrganizationName},
		Workspace:    tenancy.Workspace{ID: "ws_new", Slug: "acme-ai", OrganizationID: "org_new", Name: request.WorkspaceName},
		User:         tenancy.User{ID: subject, Email: request.OwnerEmail}, SetupToken: "one time/token",
	}, nil
}

func signupApp(t *testing.T, kind string, provisioner *signupProvisioner) *fiber.App {
	t.Helper()
	srv := newTestGateway(t, "secret")
	srv.mutateConfig(func(cfg *config.Config) {
		cfg.Deployment.Mode = config.DeploymentModeTeam
		cfg.Signup.Enabled = true
	})
	srv.SetTenantResolver(provisioner)
	app := fiber.New(fiber.Config{DisableStartupMessage: true, Immutable: true})
	app.Use(func(c *fiber.Ctx) error {
		auth.SetClaims(c, &auth.Claims{RegisteredClaims: jwt.RegisteredClaims{Subject: "usr_verified", Issuer: "soulacy"}, Email: "owner@example.test", Kind: "access", PrincipalKind: kind})
		return c.Next()
	})
	app.Get("/api/v1/signup", srv.handleSignupSession)
	app.Post("/api/v1/signup", srv.handleSelfServiceSignup)
	return app
}

func TestSelfServiceSignupBindsVerifiedIdentityAsOwner(t *testing.T) {
	provisioner := &signupProvisioner{}
	app := signupApp(t, "onboarding", provisioner)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/signup", strings.NewReader(`{"organization_name":"Acme","workspace_name":"Agents","workspace_slug":"acme-ai","display_name":"Alice","owner_email":"attacker@example.test"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("signup status=%d body=%#v", resp.StatusCode, body)
	}
	if provisioner.subject != "usr_verified" || provisioner.request.OwnerEmail != "owner@example.test" {
		t.Fatalf("signup authority escaped verified identity: subject=%q request=%#v", provisioner.subject, provisioner.request)
	}
	next := body["next"].(map[string]any)
	if next["setup_path"] != "/w/acme-ai/setup?token=one+time%2Ftoken" || next["refresh_session"] != true {
		t.Fatalf("next=%#v", next)
	}
}

func TestSelfServiceSignupRejectsNormalWorkspacePrincipal(t *testing.T) {
	app := signupApp(t, "user", &signupProvisioner{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/signup", strings.NewReader(`{"organization_name":"Acme","workspace_name":"Agents"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("workspace user signup status=%d, want 403", resp.StatusCode)
	}
}

func TestSelfServiceSignupRejectsProviderBearerClaimingOnboardingKind(t *testing.T) {
	provisioner := &signupProvisioner{}
	app := fiber.New(fiber.Config{DisableStartupMessage: true, Immutable: true})
	app.Use(func(c *fiber.Ctx) error {
		auth.SetClaims(c, &auth.Claims{RegisteredClaims: jwt.RegisteredClaims{Subject: "usr_verified", Issuer: "https://issuer.example"}, Email: "owner@example.test", Kind: "access", PrincipalKind: "onboarding"})
		return c.Next()
	})
	srv := newTestGateway(t, "secret")
	srv.mutateConfig(func(cfg *config.Config) { cfg.Deployment.Mode, cfg.Signup.Enabled = config.DeploymentModeTeam, true })
	srv.SetTenantResolver(provisioner)
	app.Post("/api/v1/signup", srv.handleSelfServiceSignup)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/signup", strings.NewReader(`{"organization_name":"Acme","workspace_name":"Agents"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || provisioner.subject != "" {
		t.Fatalf("provider bearer signup status=%d subject=%q", resp.StatusCode, provisioner.subject)
	}
}

func TestSelfServiceSignupConflictIsStable(t *testing.T) {
	app := signupApp(t, "onboarding", &signupProvisioner{err: tenancy.ErrSelfServiceAlreadyProvisioned})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/signup", strings.NewReader(`{"organization_name":"Acme","workspace_name":"Agents"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate signup status=%d, want 409", resp.StatusCode)
	}
}

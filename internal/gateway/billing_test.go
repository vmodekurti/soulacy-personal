package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/entitlements"
	"github.com/soulacy/soulacy/internal/tenancy"
)

type billingStore struct{ value *entitlements.Entitlement }

func (s *billingStore) Get(context.Context, string) (entitlements.Entitlement, error) {
	if s.value == nil {
		return entitlements.Entitlement{}, entitlements.ErrNotFound
	}
	return *s.value, nil
}
func (*billingStore) ApplyEvent(context.Context, string, entitlements.Entitlement, string) (bool, error) {
	return true, nil
}

type billingSessions struct {
	checkout entitlements.CheckoutRequest
	portal   entitlements.PortalRequest
}

func (s *billingSessions) CreateCheckout(_ context.Context, req entitlements.CheckoutRequest) (entitlements.BillingSession, error) {
	s.checkout = req
	return entitlements.BillingSession{ID: "cs_1", URL: "https://checkout.test/cs_1"}, nil
}
func (s *billingSessions) CreatePortal(_ context.Context, req entitlements.PortalRequest) (entitlements.BillingSession, error) {
	s.portal = req
	return entitlements.BillingSession{ID: "bps_1", URL: "https://billing.test/bps_1"}, nil
}

func billingApp(t *testing.T, role *string, store *billingStore, sessions *billingSessions) *fiber.App {
	t.Helper()
	srv := newTestGateway(t, "secret")
	srv.config().Deployment.Mode = config.DeploymentModeTeam
	srv.config().Billing = config.BillingConfig{
		Provider: "stripe", Enforcement: "strict", DefaultPlan: "team", StripePrices: map[string]string{"team": "price_team"},
		CheckoutSuccessURL: "https://app.test/settings?checkout=success", CheckoutCancelURL: "https://app.test/settings", PortalReturnURL: "https://app.test/settings",
	}
	srv.SetEntitlements(entitlements.NewWithOptions(store, entitlements.ServiceOptions{Missing: entitlements.MissingDenied}), store)
	srv.SetBillingSessions(sessions)
	app := fiber.New(fiber.Config{DisableStartupMessage: true, Immutable: true})
	app.Use(func(c *fiber.Ctx) error {
		srv.SetTenantResolver(&recordingMembershipResolver{membership: tenancy.Membership{
			OrganizationID: "org_team", WorkspaceID: "ws_team", MembershipID: "mem_team", Role: *role,
		}})
		auth.SetClaims(c, &auth.Claims{RegisteredClaims: jwt.RegisteredClaims{Subject: "usr_owner"}, Role: *role, Kind: "access", PrincipalKind: "user", AuthTime: time.Now().Unix()})
		c.Locals("request_id", "req-billing")
		return c.Next()
	})
	app.Use(srv.workspaceContextMW())
	app.Use(srv.entitlementMW())
	app.Get("/api/v1/billing", srv.handleBillingStatus)
	app.Post("/api/v1/billing/checkout", srv.handleBillingCheckout)
	app.Post("/api/v1/billing/portal", srv.handleBillingPortal)
	return app
}

func billingRequest(t *testing.T, app *fiber.App, method, path, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	decoded := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&decoded)
	return resp.StatusCode, decoded
}

func TestStrictBillingStillAllowsOwnerToCreateCheckout(t *testing.T) {
	role := tenancy.RoleOwner
	store, sessions := &billingStore{}, &billingSessions{}
	app := billingApp(t, &role, store, sessions)
	status, body := billingRequest(t, app, http.MethodPost, "/api/v1/billing/checkout", `{"plan":"team"}`)
	if status != http.StatusOK || body["url"] == "" {
		t.Fatalf("checkout = %d %v", status, body)
	}
	if sessions.checkout.WorkspaceID != "ws_team" || sessions.checkout.PriceID != "price_team" || sessions.checkout.IdempotencyKey == "" {
		t.Fatalf("checkout request = %#v", sessions.checkout)
	}
}

func TestBillingMutationRequiresWorkspaceOwner(t *testing.T) {
	role := tenancy.RoleAdmin
	app := billingApp(t, &role, &billingStore{}, &billingSessions{})
	if status, _ := billingRequest(t, app, http.MethodPost, "/api/v1/billing/checkout", `{"plan":"team"}`); status != http.StatusForbidden {
		t.Fatalf("admin checkout status = %d, want 403", status)
	}
}

func TestBillingPortalUsesWorkspaceCustomer(t *testing.T) {
	role := tenancy.RoleOwner
	store := &billingStore{value: &entitlements.Entitlement{WorkspaceID: "ws_team", Status: entitlements.StatusPastDue, CustomerID: "cus_team"}}
	sessions := &billingSessions{}
	app := billingApp(t, &role, store, sessions)
	if status, body := billingRequest(t, app, http.MethodPost, "/api/v1/billing/portal", `{}`); status != http.StatusOK {
		t.Fatalf("portal = %d %v", status, body)
	}
	if sessions.portal.CustomerID != "cus_team" || sessions.portal.ReturnURL != "https://app.test/settings" {
		t.Fatalf("portal request = %#v", sessions.portal)
	}
}

func TestActiveSubscriptionCannotCreateDuplicateCheckout(t *testing.T) {
	role := tenancy.RoleOwner
	store := &billingStore{value: &entitlements.Entitlement{WorkspaceID: "ws_team", Status: entitlements.StatusActive, CustomerID: "cus_team", SubscriptionID: "sub_team"}}
	app := billingApp(t, &role, store, &billingSessions{})
	if status, _ := billingRequest(t, app, http.MethodPost, "/api/v1/billing/checkout", `{"plan":"team"}`); status != http.StatusConflict {
		t.Fatalf("duplicate checkout status = %d, want 409", status)
	}
}

func TestEntitlementMiddlewareDoesNotRequireWorkspaceForPlatformMutation(t *testing.T) {
	srv := newTestGateway(t, "secret")
	srv.config().Deployment.Mode = config.DeploymentModeTeam
	srv.SetEntitlements(entitlements.NewWithOptions(&billingStore{}, entitlements.ServiceOptions{Missing: entitlements.MissingDenied}), &billingStore{})

	app := fiber.New(fiber.Config{DisableStartupMessage: true, Immutable: true})
	app.Use(func(c *fiber.Ctx) error {
		auth.SetClaims(c, &auth.Claims{CredentialID: staticAPIKeyCredentialID})
		return c.Next()
	})
	app.Use(srv.entitlementMW())
	app.Post("/api/v1/admin/bootstrap", func(c *fiber.Ctx) error {
		return c.SendStatus(fiber.StatusNoContent)
	})

	status, _ := billingRequest(t, app, http.MethodPost, "/api/v1/admin/bootstrap", `{}`)
	if status != http.StatusNoContent {
		t.Fatalf("platform bootstrap status = %d, want 204", status)
	}
}

func TestEntitlementMiddlewareStillRequiresWorkspaceForTenantMutation(t *testing.T) {
	srv := newTestGateway(t, "secret")
	srv.config().Deployment.Mode = config.DeploymentModeTeam
	srv.SetEntitlements(entitlements.NewWithOptions(&billingStore{}, entitlements.ServiceOptions{Missing: entitlements.MissingDenied}), &billingStore{})

	app := fiber.New(fiber.Config{DisableStartupMessage: true, Immutable: true})
	app.Use(srv.entitlementMW())
	app.Post("/api/v1/agents", func(c *fiber.Ctx) error {
		return c.SendStatus(fiber.StatusNoContent)
	})

	status, body := billingRequest(t, app, http.MethodPost, "/api/v1/agents", `{}`)
	if status != http.StatusForbidden || body["error"] != "verified workspace membership is required" {
		t.Fatalf("tenant mutation = %d %v, want workspace-membership 403", status, body)
	}
}

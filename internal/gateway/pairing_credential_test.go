// pairing_credential_test.go — the credential a paired device walks away with.
//
// POST /pairing/redeem is the one mutating route on the authenticated group
// with no authorization middleware, and that is deliberate: the single-use
// pairing code IS the capability, and minting one already requires
// config:write. What was not deliberate is what it issued. The unscoped
// store.Create(ctx, name, scopes) hard-codes org_personal / ws_personal /
// role operator, so in a Team deployment redeeming a code produced an operator
// credential in the DEPLOYMENT's own workspace — bypassing the scoped
// credential API that MU-015 and MU-017 built, including its refusal to issue
// anything without a verified workspace identity.
package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"

	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/auth/apikeys"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/tenancy"
)

func pairingApp(t *testing.T, mode string, withIdentity bool) (*Server, *fiber.App, *apikeys.SQLiteStore) {
	t.Helper()
	store, err := apikeys.NewSQLiteStore(filepath.Join(t.TempDir(), "keys.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	srv := newTestGateway(t, "secret")
	srv.config().Deployment.Mode = mode
	srv.SetAPIKeyStore(store)
	srv.SetTenantResolver(&recordingMembershipResolver{membership: tenancy.Membership{
		OrganizationID: "org_acme", WorkspaceID: "ws_acme", MembershipID: "mem_a", Role: "operator",
	}})

	app := fiber.New(fiber.Config{DisableStartupMessage: true, Immutable: true})
	if withIdentity {
		app.Use(func(c *fiber.Ctx) error {
			auth.SetClaims(c, &auth.Claims{
				RegisteredClaims: jwt.RegisteredClaims{Subject: "usr_alice"},
				Role:             "operator", Kind: "access", PrincipalKind: "user",
				CredentialID: "cred_alice",
			})
			c.Locals("request_id", "req-pair")
			return c.Next()
		})
		app.Use(srv.workspaceContextMW())
	}
	app.Post("/pairing/tokens", srv.handleCreatePairingToken)
	app.Post("/pairing/redeem", srv.handleRedeemPairingToken)
	return srv, app, store
}

func redeem(t *testing.T, app *fiber.App) (int, map[string]any) {
	t.Helper()
	mint, err := app.Test(httptest.NewRequest(http.MethodPost, "/pairing/tokens", nil))
	if err != nil {
		t.Fatal(err)
	}
	minted := map[string]any{}
	_ = json.NewDecoder(mint.Body).Decode(&minted)
	mint.Body.Close()
	code, _ := minted["code"].(string)
	if code == "" {
		t.Fatalf("no pairing code minted: %v", minted)
	}

	req := httptest.NewRequest(http.MethodPost, "/pairing/redeem", strings.NewReader(`{"code":"`+code+`"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestAPairedDeviceGetsACredentialInTheRedeemersWorkspace(t *testing.T) {
	_, app, store := pairingApp(t, config.DeploymentModeTeam, true)

	status, body := redeem(t, app)
	if status != http.StatusOK || body["paired"] != true {
		t.Fatalf("redeem = %d %v", status, body)
	}

	keys, err := store.List(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 {
		t.Fatalf("expected exactly one credential, got %d", len(keys))
	}
	key := keys[0]
	// Asserted on the workspace binding itself rather than on the response
	// body, which does not carry it — and the response looked identical
	// before and after the fix.
	if len(key.WorkspaceIDs) != 1 || key.WorkspaceIDs[0] != "ws_acme" {
		t.Fatalf("paired credential is bound to %v, want the redeemer's workspace ws_acme", key.WorkspaceIDs)
	}
	if key.OrganizationID != "org_acme" {
		t.Fatalf("paired credential organization = %q, want org_acme", key.OrganizationID)
	}
	if key.SubjectID != "usr_alice" {
		t.Fatalf("paired credential subject = %q — an unattributed credential is one nobody's revocation reaches", key.SubjectID)
	}
	// A phone paired once and lost keeps working until somebody thinks to
	// revoke a credential they never knew was created.
	if key.ExpiresAt == nil {
		t.Fatal("the paired credential never expires")
	}
}

// Fail closed. Falling back to the unscoped mint would be the escalation this
// exists to prevent, done silently on the deployments least able to notice.
func TestPairingWithoutAVerifiedIdentityIsRefusedInMultiUser(t *testing.T) {
	_, app, store := pairingApp(t, config.DeploymentModeTeam, false)

	status, _ := redeem(t, app)
	// Asserted on 403 specifically, not merely "not 200". Deleting the
	// fail-closed branch still produces a failure — CreateScoped rejects the
	// empty subject and organization and the handler answers 500 — so a
	// looser assertion passes with the branch gone, and the property would
	// then rest on the store's validation rather than on this decision.
	// Mutation testing found exactly that.
	if status != http.StatusForbidden {
		t.Fatalf("an unattributed pairing returned %d, want 403 from the fail-closed branch", status)
	}
	keys, err := store.List(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 0 {
		t.Fatalf("a refused pairing still minted %d credential(s)", len(keys))
	}
}

// Product invariant 7: the single-user path is the call it has always made.
func TestPersonalPairingIsUnchanged(t *testing.T) {
	_, app, store := pairingApp(t, config.DeploymentModePersonal, false)

	status, body := redeem(t, app)
	if status != http.StatusOK || body["paired"] != true {
		t.Fatalf("personal redeem = %d %v", status, body)
	}
	if token, _ := body["token"].(string); token == "" {
		t.Fatalf("personal pairing issued no token: %v", body)
	}
	keys, err := store.List(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || len(keys[0].WorkspaceIDs) != 1 || keys[0].WorkspaceIDs[0] != "ws_personal" {
		t.Fatalf("personal pairing changed shape: %+v", keys)
	}
}

// workspace_policy_test.go — MU-030 criterion 1 at the route.
//
// The composition property (a stored entry can only tighten) is proven in
// internal/workspacepolicy, where the rule lives. What only the route can get
// wrong is: who may write, whose workspace is written, and whether an owner is
// TOLD when the number they set is not the number in force.
package gateway

import (
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
	"github.com/soulacy/soulacy/internal/costs"
	"github.com/soulacy/soulacy/internal/quota"
	"github.com/soulacy/soulacy/internal/tenancy"
	"github.com/soulacy/soulacy/internal/workspacepolicy"
)

func policyApp(t *testing.T, role *string, workspaceID string) (*Server, *fiber.App) {
	t.Helper()
	srv := newTestGateway(t, "secret")
	srv.config().Deployment.Mode = config.DeploymentModeTeam
	store, err := workspacepolicy.NewStore(t.TempDir() + "/policies.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	srv.SetWorkspacePolicyStore(store)

	app := fiber.New(fiber.Config{DisableStartupMessage: true, Immutable: true})
	app.Use(func(c *fiber.Ctx) error {
		srv.SetTenantResolver(&recordingMembershipResolver{membership: tenancy.Membership{
			OrganizationID: "org_team", WorkspaceID: workspaceID, MembershipID: "mem_team", Role: *role,
		}})
		auth.SetClaims(c, &auth.Claims{
			RegisteredClaims: jwt.RegisteredClaims{Subject: "usr_alice"},
			Role:             *role, Kind: "access", PrincipalKind: "user",
			CredentialID: "cred_alice", AuthTime: time.Now().Unix(),
		})
		c.Locals("request_id", "req-policy")
		return c.Next()
	})
	app.Use(srv.workspaceContextMW())
	srv.registerWorkspacePolicyRoutes(app)
	return srv, app
}

func policyRequest(t *testing.T, app *fiber.App, method, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, "/workspace/policy", strings.NewReader(body))
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

func TestOnlyWorkspaceOwnersAndAdminsReadOrWriteTheWorkspacePolicy(t *testing.T) {
	role := tenancy.RoleAdmin
	_, app := policyApp(t, &role, "ws_team")

	for _, denied := range []string{"member", "viewer", tenancy.RoleDemoDeveloper} {
		role = denied
		if status, _ := policyRequest(t, app, http.MethodPut, `{"daily_usd":5}`); status != http.StatusForbidden {
			t.Errorf("role %q wrote the workspace policy: %d", denied, status)
		}
		if status, _ := policyRequest(t, app, http.MethodGet, ""); status != http.StatusForbidden {
			t.Errorf("role %q read the workspace policy: %d", denied, status)
		}
	}
	role = tenancy.RoleAdmin
	if status, body := policyRequest(t, app, http.MethodPut, `{"per_user_daily_tokens":500000}`); status != http.StatusOK {
		t.Fatalf("the admin was refused: %d %v", status, body)
	}
	role = tenancy.RoleOwner
	if status, body := policyRequest(t, app, http.MethodPut, `{"daily_usd":5}`); status != http.StatusOK {
		t.Fatalf("the owner was refused: %d %v", status, body)
	}
}

// THE ANSWER THAT PREVENTS A SURPRISE. An owner who sets a number above the
// deployment's ceiling is told immediately what is actually in force, rather
// than discovering it the first time a run is refused with nothing connecting
// the two events.
func TestTheResponseReportsWhatIsInForceNotOnlyWhatWasStored(t *testing.T) {
	role := tenancy.RoleOwner
	srv, app := policyApp(t, &role, "ws_team")
	// The operator's ceiling.
	srv.config().Costs.Quotas.Workspaces = map[string]config.QuotaLimit{"ws_team": {DailyUSD: 25}}

	status, body := policyRequest(t, app, http.MethodPut, `{"daily_usd":1000000}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d: %v", status, body)
	}
	stored, _ := body["policy"].(map[string]any)
	if got, _ := stored["daily_usd"].(float64); got != 1000000 {
		t.Fatalf("the stored policy = %v, want what the owner wrote", stored["daily_usd"])
	}
	effective, _ := body["effective"].(map[string]any)
	if got, _ := effective["daily_usd"].(float64); got != 25 {
		t.Fatalf("effective daily = %v, want the operator's 25 — the response would have let "+
			"the owner believe their number took", effective["daily_usd"])
	}
}

func TestPublicDemoTokenCeilingDoesNotApplyToAnOrdinaryWorkspace(t *testing.T) {
	srv := newTestGateway(t, "secret")
	srv.config().RateLimit.PerUserTokensDay = 50_000
	srv.config().PublicDemo.Enabled = true
	srv.config().PublicDemo.WorkspaceID = "ws_demo"

	if got := srv.effectivePerUserDailyTokens(workspacepolicy.Policy{WorkspaceID: "ws_demo", PerUserDailyTokens: 500_000}); got != 50_000 {
		t.Fatalf("demo effective quota = %d, want operator ceiling 50000", got)
	}
	if got := srv.effectivePerUserDailyTokens(workspacepolicy.Policy{WorkspaceID: "ws_otg", PerUserDailyTokens: 500_000}); got != 500_000 {
		t.Fatalf("ordinary workspace effective quota = %d, want workspace setting 500000", got)
	}
}

// The workspace comes from the verified identity, never from the body. A
// body-supplied id would let an owner of one workspace set another's budget —
// and the write would look entirely ordinary in the audit trail, because it is
// an owner writing a policy.
func TestTheBodyCannotChooseWhichWorkspaceIsWritten(t *testing.T) {
	role := tenancy.RoleOwner
	srv, app := policyApp(t, &role, "ws_team")

	if status, _ := policyRequest(t, app, http.MethodPut,
		`{"workspace_id":"ws_victim","daily_usd":1}`); status != http.StatusOK {
		t.Fatal("the write failed")
	}
	victim, err := srv.workspacePolicies.Get(t.Context(), "ws_victim")
	if err != nil {
		t.Fatal(err)
	}
	if victim.DailyUSD != 0 {
		t.Fatalf("a body-supplied workspace id wrote another tenant's policy: %+v", victim)
	}
	mine, err := srv.workspacePolicies.Get(t.Context(), "ws_team")
	if err != nil {
		t.Fatal(err)
	}
	if mine.DailyUSD != 1 {
		t.Fatalf("the caller's own policy was not written: %+v", mine)
	}
	// And the echo reports what was WRITTEN, not what was sent — a response
	// that named ws_victim would tell the caller their write landed somewhere
	// it did not.
	status, body := policyRequest(t, app, http.MethodGet, "")
	if status != http.StatusOK {
		t.Fatalf("read status = %d", status)
	}
	stored, _ := body["policy"].(map[string]any)
	if got, _ := stored["workspace_id"].(string); got != "ws_team" {
		t.Fatalf("the echoed workspace = %q, want the verified ws_team", got)
	}
}

// A negative retention parses as a valid duration and reads downstream as
// "pruning disabled". Refused at the edge with the field named, so an owner is
// not left clearing the whole form to find out which value was wrong.
func TestAWindowThatWouldDisablePruningIsRefusedByName(t *testing.T) {
	role := tenancy.RoleOwner
	_, app := policyApp(t, &role, "ws_team")

	status, body := policyRequest(t, app, http.MethodPut, `{"action_events":"-1h"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("a negative retention = %d, want 400", status)
	}
	if message, _ := body["error"].(string); !strings.Contains(message, "action_events") {
		t.Fatalf("the refusal does not name the field: %v", body)
	}
}

// Retention is reported as EFFECTIVE too, and a workspace cannot lengthen it.
func TestRetentionIsReportedAsEffectiveAndCannotBeLengthened(t *testing.T) {
	role := tenancy.RoleOwner
	srv, app := policyApp(t, &role, "ws_team")
	srv.config().Runtime.Retention.ActionEvents = "720h"

	status, body := policyRequest(t, app, http.MethodPut, `{"action_events":"8760h"}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d: %v", status, body)
	}
	retention, _ := body["retention"].(map[string]any)
	if got, _ := retention["action_events"].(string); got != "720h0m0s" {
		t.Fatalf("effective action_events = %q, want the deployment's 720h — a workspace lengthened retention", got)
	}
}

func TestThePolicyRoutesRefuseWithNoStore(t *testing.T) {
	role := tenancy.RoleOwner
	srv, app := policyApp(t, &role, "ws_team")
	srv.SetWorkspacePolicyStore(nil)

	for _, method := range []string{http.MethodGet, http.MethodPut} {
		if status, _ := policyRequest(t, app, method, `{"daily_usd":5}`); status != http.StatusServiceUnavailable {
			t.Errorf("%s with no store = %d, want 503", method, status)
		}
	}
}

// A stored limit must take effect without a restart, and must survive one.
// Both halves matter: an edit that needs a restart is not a limit anybody can
// rely on, and a limit lost on restart is worse than one never set, because
// nobody is watching for its absence.
func TestAStoredLimitReachesTheGovernorOnEditAndOnStartup(t *testing.T) {
	role := tenancy.RoleOwner
	srv, app := policyApp(t, &role, "ws_team")
	governor := costs.NewGovernor(nil, nil, costs.GovernanceConfig{})
	srv.SetCostGovernor(governor)

	if status, _ := policyRequest(t, app, http.MethodPut, `{"daily_usd":5}`); status != http.StatusOK {
		t.Fatal("the write failed")
	}
	if got := governorDailyMicros(governor, "ws_team"); got != 5_000_000 {
		t.Fatalf("after an edit the governor enforces %d, want 5_000_000", got)
	}

	// A fresh server over the same store, as a restart would produce.
	restarted := newTestGateway(t, "secret")
	restarted.SetWorkspacePolicyStore(srv.workspacePolicies)
	restartedGovernor := costs.NewGovernor(nil, nil, costs.GovernanceConfig{})
	restarted.SetCostGovernor(restartedGovernor)
	restarted.ReloadWorkspaceQuotaPolicy(t.Context())

	if got := governorDailyMicros(restartedGovernor, "ws_team"); got != 5_000_000 {
		t.Fatalf("after a restart the governor enforces %d — the stored limit was lost", got)
	}
}

func governorDailyMicros(governor *costs.Governor, workspaceID string) int64 {
	policy := governor.QuotaPolicy()
	if policy == nil {
		return 0
	}
	return policy.Tightest(quota.Subject{WorkspaceID: workspaceID}).DailyMicros
}

// A reload that cannot read the store must leave the previous policy in place.
//
// The tempting alternative — install whatever was read — is catastrophic on an
// empty result: every workspace's self-imposed ceiling disappears at once, from
// a transient database error, with nothing in the API to show it happened.
func TestAFailedReloadKeepsThePreviousPolicyRatherThanClearingIt(t *testing.T) {
	role := tenancy.RoleOwner
	srv, app := policyApp(t, &role, "ws_team")
	governor := costs.NewGovernor(nil, nil, costs.GovernanceConfig{})
	srv.SetCostGovernor(governor)

	if status, _ := policyRequest(t, app, http.MethodPut, `{"daily_usd":5}`); status != http.StatusOK {
		t.Fatal("the write failed")
	}
	if got := governorDailyMicros(governor, "ws_team"); got != 5_000_000 {
		t.Fatalf("setup: governor enforces %d", got)
	}

	// Break the store the way an outage would, then reload.
	if err := srv.workspacePolicies.Close(); err != nil {
		t.Fatal(err)
	}
	srv.ReloadWorkspaceQuotaPolicy(t.Context())

	if got := governorDailyMicros(governor, "ws_team"); got != 5_000_000 {
		t.Fatalf("a failed reload cleared the ceiling: governor now enforces %d", got)
	}
}

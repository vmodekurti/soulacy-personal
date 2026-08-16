// tenancy_test.go — MU-024 criterion 5: rate limits are keyed by authenticated
// credential AND workspace, so a body field cannot reach another tenant's
// budget.
package ratelimit

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/requestctx"
)

// asWorkspace serves requests under a verified identity.
func asWorkspace(t *testing.T, workspaceID, subject string, handlers ...fiber.Handler) *fiber.App {
	t.Helper()
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(func(c *fiber.Ctx) error {
		identity, err := requestctx.New(requestctx.Input{
			Subject: subject, OrganizationID: "org_a", WorkspaceID: workspaceID,
			MembershipID: "mem_" + workspaceID, Role: "developer",
			RequestID: "req", PrincipalKind: "user", CredentialID: "cred_" + subject,
		})
		if err != nil {
			t.Fatal(err)
		}
		c.SetUserContext(requestctx.With(c.UserContext(), identity))
		return c.Next()
	})
	all := append(handlers, func(c *fiber.Ctx) error { return c.SendStatus(200) })
	app.Post("/chat", all...)
	return app
}

func post(t *testing.T, app *fiber.App, body string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/chat", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// The attack the old key allowed. `"agent:" + agentID` took the agent ID from
// the REQUEST BODY, so a member of one workspace could name another tenant's
// agent and burn its rate-limit budget — a cross-tenant denial of service
// needing no credential beyond a valid session of one's own.
func TestABodyAgentIDCannotReachAnotherWorkspacesBudget(t *testing.T) {
	m := newManager(t, Config{Enabled: true, PerAgentRPM: 2, Backend: "memory"})

	attacker := asWorkspace(t, "ws_attacker", "usr_attacker", m.AgentRPMMiddleware())
	// The attacker exhausts what they believe is the victim's budget.
	for i := 0; i < 5; i++ {
		post(t, attacker, `{"agent_id":"support-bot"}`)
	}

	// The victim's own agent is untouched.
	victim := asWorkspace(t, "ws_victim", "usr_victim", m.AgentRPMMiddleware())
	if status := post(t, victim, `{"agent_id":"support-bot"}`); status != http.StatusOK {
		t.Fatalf("the victim's first request returned %d — another tenant spent their budget", status)
	}
}

// Two tenants with an agent of the same ID do not share a token bucket.
// Agent IDs are unique per workspace, not per deployment.
func TestTwoWorkspacesDoNotShareAnAgentTokenBucket(t *testing.T) {
	m := newManager(t, Config{Enabled: true, PerAgentTokensDay: 100, Backend: "memory"})
	m.RecordAgentTokensInWorkspace("ws_a", "support-bot", 100)

	if status := post(t, asWorkspace(t, "ws_a", "usr_a", m.AgentTokenQuotaMiddleware()), `{"agent_id":"support-bot"}`); status != http.StatusTooManyRequests {
		t.Fatalf("ws_a status = %d, want 429 — its own quota is spent", status)
	}
	if status := post(t, asWorkspace(t, "ws_b", "usr_b", m.AgentTokenQuotaMiddleware()), `{"agent_id":"support-bot"}`); status != http.StatusOK {
		t.Fatalf("ws_b status = %d — another tenant's usage exhausted its quota", status)
	}
}

// The same subject acting in two workspaces has two budgets: a limit that is
// "per user" across tenants would let a member of many workspaces starve
// themselves in one by working in another.
func TestOneSubjectInTwoWorkspacesHasTwoBudgets(t *testing.T) {
	m := newManager(t, Config{Enabled: true, PerUserRPM: 2, Backend: "memory"})
	for i := 0; i < 5; i++ {
		post(t, asWorkspace(t, "ws_a", "usr_shared", m.UserRPMMiddleware()), `{}`)
	}
	if status := post(t, asWorkspace(t, "ws_b", "usr_shared", m.UserRPMMiddleware()), `{}`); status != http.StatusOK {
		t.Fatalf("status = %d — the same person's work in another workspace spent this budget", status)
	}
}

// The recorder, the middleware and the status endpoint must all name the same
// bucket. Three hand-rolled key expressions is how a limiter ends up checking
// a bucket nothing fills.
func TestTheRecorderAndTheMiddlewareAgreeOnTheKey(t *testing.T) {
	m := newManager(t, Config{Enabled: true, PerUserTokensDay: 100, Backend: "memory"})
	m.RecordTokensInWorkspace("ws_a", "cred_usr_a", 100)

	if status := post(t, asWorkspace(t, "ws_a", "usr_a", m.TokenQuotaMiddleware()), `{}`); status != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 — the middleware read a bucket the recorder did not fill", status)
	}
}

// A deployment with no verified identity keeps the shape it always had, so a
// personal install's buckets keep their meaning (invariant 7).
func TestWithNoIdentityTheKeyIsThePersonalWorkspace(t *testing.T) {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	var captured string
	app.Post("/chat", func(c *fiber.Ctx) error {
		captured = userKey(c)
		return c.SendStatus(200)
	})
	post(t, app, `{}`)
	if want := bucketUserKey("", "anon"); captured != want {
		t.Fatalf("key = %q, want %q", captured, want)
	}
}

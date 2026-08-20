// recentauth_test.go — MU-030 criterion 5.
//
// Two halves. The behavioural half pins the decision. The structural half
// fails the build when a high-impact route loses its gate.
package gateway

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"

	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/config"
)

// The issuer-level half of this story lives in internal/auth/recentauth_test.go,
// where the unexported constructor is reachable. It pins the property this
// middleware depends on: that a silent token rotation does not move auth_time.

func TestTheWindowIsAWindow(t *testing.T) {
	now := time.Now().UTC()
	fresh := &auth.Claims{AuthTime: now.Add(-time.Minute).Unix()}
	stale := &auth.Claims{AuthTime: now.Add(-recentAuthWindow - time.Second).Unix()}
	edge := &auth.Claims{AuthTime: now.Add(-recentAuthWindow).Unix()}
	if !recentlyAuthenticated(fresh, now) {
		t.Fatal("a minute-old authentication is not recent")
	}
	if recentlyAuthenticated(stale, now) {
		t.Fatal("an authentication older than the window is still accepted")
	}
	if !recentlyAuthenticated(edge, now) {
		t.Fatal("the boundary should be inclusive; a client that re-authenticated exactly at the limit did the right thing")
	}
	// Zero is "no interactive authentication ever happened", not "the epoch".
	if recentlyAuthenticated(&auth.Claims{}, now) {
		t.Fatal("a credential that never involved an interactive authentication passed the check")
	}
	if recentlyAuthenticated(nil, now) {
		t.Fatal("absent claims passed the check")
	}
}

func recentAuthApp(t *testing.T, mode string, claims *auth.Claims) *fiber.App {
	t.Helper()
	srv := withCfg(&Server{}, &config.Config{Deployment: config.DeploymentConfig{Mode: mode}})
	app := fiber.New(fiber.Config{DisableStartupMessage: true, Immutable: true})
	app.Use(func(c *fiber.Ctx) error {
		if claims != nil {
			auth.SetClaims(c, claims)
		}
		return c.Next()
	})
	app.Post("/high-impact", srv.requireRecentAuth(), func(c *fiber.Ctx) error {
		return c.SendStatus(fiber.StatusNoContent)
	})
	return app
}

func postTo(t *testing.T, app *fiber.App) int {
	t.Helper()
	resp, err := app.Test(httptest.NewRequest(http.MethodPost, "/high-impact", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func TestTheGateRefusesAStaleSessionAndAdmitsAFreshOne(t *testing.T) {
	stale := &auth.Claims{
		RegisteredClaims: jwt.RegisteredClaims{Subject: "usr_alice"},
		PrincipalKind:    "user", AuthTime: time.Now().Add(-time.Hour).Unix(),
	}
	if got := postTo(t, recentAuthApp(t, config.DeploymentModeTeam, stale)); got != http.StatusUnauthorized {
		t.Fatalf("stale session = %d, want 401", got)
	}
	fresh := &auth.Claims{
		RegisteredClaims: jwt.RegisteredClaims{Subject: "usr_alice"},
		PrincipalKind:    "user", AuthTime: time.Now().Unix(),
	}
	if got := postTo(t, recentAuthApp(t, config.DeploymentModeTeam, fresh)); got != http.StatusNoContent {
		t.Fatalf("fresh session = %d, want 204", got)
	}
}

// Product invariant 7: a single-user installation must not notice.
func TestAPersonalDeploymentIsNeverAskedToReauthenticate(t *testing.T) {
	stale := &auth.Claims{
		RegisteredClaims: jwt.RegisteredClaims{Subject: "admin"},
		PrincipalKind:    "user", AuthTime: time.Now().Add(-24 * time.Hour).Unix(),
	}
	if got := postTo(t, recentAuthApp(t, config.DeploymentModePersonal, stale)); got != http.StatusNoContent {
		t.Fatalf("personal deployment = %d, want 204", got)
	}
}

// A machine has no browser to bounce to. Demanding step-up of a service
// account breaks automation at 03:00 and buys nothing: a stolen machine
// credential is a rotation-and-revocation problem, not a step-up one. The
// exemption is stated rather than hidden.
func TestANonHumanPrincipalIsNotPrompted(t *testing.T) {
	machine := &auth.Claims{
		RegisteredClaims: jwt.RegisteredClaims{Subject: "svc_deployer"},
		PrincipalKind:    "service_account", AuthTime: 0,
	}
	if got := postTo(t, recentAuthApp(t, config.DeploymentModeTeam, machine)); got != http.StatusNoContent {
		t.Fatalf("service account = %d, want 204", got)
	}
	// But an older token with no principal_kind at all IS treated as human:
	// wrongly prompting a machine is a visible, reported failure, and wrongly
	// exempting a person is a silent hole that looks like working software.
	legacy := &auth.Claims{
		RegisteredClaims: jwt.RegisteredClaims{Subject: "usr_alice"},
		AuthTime:         time.Now().Add(-time.Hour).Unix(),
	}
	if got := postTo(t, recentAuthApp(t, config.DeploymentModeTeam, legacy)); got != http.StatusUnauthorized {
		t.Fatalf("a token with no principal_kind = %d, want 401", got)
	}
}

// 401 with a distinct code, not 403. The caller HAS permission and has stale
// proof, and a client told 403 will report "you do not have access" to
// somebody who does.
func TestTheRefusalTellsTheClientWhatToDo(t *testing.T) {
	stale := &auth.Claims{
		RegisteredClaims: jwt.RegisteredClaims{Subject: "usr_alice"},
		PrincipalKind:    "user", AuthTime: time.Now().Add(-time.Hour).Unix(),
	}
	app := recentAuthApp(t, config.DeploymentModeTeam, stale)
	resp, err := app.Test(httptest.NewRequest(http.MethodPost, "/high-impact", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body := make([]byte, 1024)
	n, _ := resp.Body.Read(body)
	text := string(body[:n])
	if !strings.Contains(text, "reauthentication_required") {
		t.Fatalf("the refusal carries no machine-readable code: %s", text)
	}
	if !strings.Contains(text, "/auth/reauthenticate") {
		t.Fatalf("the refusal does not say how to recover: %s", text)
	}
}

// ── the structural half ─────────────────────────────────────────────────────

// highImpactRoutes are the routes whose effect outlives the session that
// performed them, or which change who may do things. Each must carry
// requireRecentAuth.
//
// The list is here rather than only in server.go so that removing the
// middleware fails a test rather than passing review as a cleanup.
var highImpactRoutes = map[string]string{
	`api.Patch("/workspace/members/:id/role"`:   "granting yourself owner is the change that makes every other change possible",
	`api.Patch("/workspace/members/:id/status"`: "suspending a colleague is quiet and persists after the session ends",
	`api.Delete("/workspace/members/:id"`:       "removing a member is quiet and persists after the session ends",
	`api.Post("/workspace/invitations"`:         "an invitation is a durable way back in that outlives this session",
	`api.Post("/admin/api-keys"`:                "a minted credential is an authority that outlives the session that created it",
	`api.Delete("/admin/api-keys/:id"`:          "revoking a colleague's credential is a denial of service they cannot undo",
	`api.Post("/admin/api-keys/:id/rotate"`:     "rotation mints a new plaintext and invalidates the old one",
	`api.Patch("/admin/api-keys/:id/status"`:    "suspending a credential is revocation by another name",
	`api.Post("/admin/api-keys/validate"`:       "its whole purpose is to confirm a secret",
}

func TestEveryHighImpactRouteRequiresRecentAuth(t *testing.T) {
	source, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for prefix, reason := range highImpactRoutes {
		index := strings.Index(text, prefix)
		if index < 0 {
			t.Errorf("%s is no longer registered — if it was renamed, rename it here too", prefix)
			continue
		}
		// The registration is one call; look only as far as its closing of the
		// middleware list, so a requireRecentAuth on the NEXT route cannot
		// satisfy this one.
		end := strings.Index(text[index:], "\n")
		if end < 0 {
			end = len(text) - index
		}
		if !strings.Contains(text[index:index+end], "s.requireRecentAuth()") {
			t.Errorf("%s has no recent-auth gate — %s", prefix, reason)
		}
	}
}

// The other direction: a gate that is present but never fires is the shape of
// the iat mistake at the routing layer. This confirms the middleware the
// routes name is the one that actually decides, rather than a same-named
// helper that returns c.Next() unconditionally.
func TestTheRecentAuthMiddlewareCanActuallyRefuse(t *testing.T) {
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, "recentauth.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, decl := range parsed.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "requireRecentAuth" || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if ok && sel.Sel.Name == "Status" {
				found = true
			}
			return true
		})
	}
	if !found {
		t.Fatal("requireRecentAuth has no refusal path — it is a gate that cannot close")
	}
}

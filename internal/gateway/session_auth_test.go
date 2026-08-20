package gateway

import (
	"net/http"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/requestctx"
	"github.com/soulacy/soulacy/internal/session"
	"github.com/soulacy/soulacy/pkg/message"
	"go.uber.org/zap"
)

func TestSessionOwnershipIsPrincipalAndAgentBound(t *testing.T) {
	s := &Server{sessionOwners: make(map[string]sessionOwner)}
	app := fiber.New(fiber.Config{Immutable: true})
	var seen []string
	app.Use(func(c *fiber.Ctx) error {
		subject := c.Query("subject")
		seen = append(seen, subject)
		auth.SetClaims(c, &auth.Claims{Role: "viewer", RegisteredClaims: jwt.RegisteredClaims{Subject: subject}})
		return c.Next()
	})
	app.Post("/claim/:agent/:session", func(c *fiber.Ctx) error {
		if err := s.claimSession(c, c.Params("agent"), c.Params("session")); err != nil {
			return err
		}
		return c.SendStatus(http.StatusNoContent)
	})
	app.Get("/read/:agent/:session", func(c *fiber.Ctx) error {
		if err := s.requireSession(c, c.Params("agent"), c.Params("session")); err != nil {
			return err
		}
		return c.SendStatus(http.StatusNoContent)
	})

	request := func(method, path, subject string) int {
		req, _ := http.NewRequest(method, path+"?subject="+url.QueryEscape(subject), nil)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode
	}
	if got := request(http.MethodPost, "/claim/weather/s-opaque", "alice"); got != http.StatusNoContent {
		t.Fatalf("claim=%d", got)
	}
	if got := s.sessionOwners["s-opaque"]; got.Principal != "viewer:alice" {
		t.Fatalf("stored owner=%+v", got)
	}
	if got := request(http.MethodGet, "/read/weather/s-opaque", "alice"); got != http.StatusNoContent {
		t.Fatalf("owner read=%d", got)
	}
	if got := request(http.MethodGet, "/read/weather/s-opaque", "bob"); got != http.StatusNotFound {
		t.Fatalf("cross-principal read=%d seen=%v owner=%+v", got, seen, s.sessionOwners["s-opaque"])
	}
	if got := request(http.MethodGet, "/read/finance/s-opaque", "alice"); got != http.StatusNotFound {
		t.Fatalf("cross-agent read=%d", got)
	}
}

func TestEventAuthorizationUsesSessionOwner(t *testing.T) {
	s := &Server{sessionOwners: map[string]sessionOwner{
		"run-1": {Principal: "viewer:alice", AgentID: "weather"},
	}}
	event := message.Event{Type: "tool.result", AgentID: "weather", SessionID: "run-1"}
	if !s.authorizeEvent(eventPrincipal{Principal: "viewer:alice", Role: "viewer", Authenticated: true}, event) {
		t.Fatal("session owner was denied their event")
	}
	if s.authorizeEvent(eventPrincipal{Principal: "viewer:bob", Role: "viewer", Authenticated: true}, event) {
		t.Fatal("event was visible across principals")
	}
	if !s.authorizeEvent(eventPrincipal{Principal: "admin", Role: "admin", Authenticated: true, Admin: true}, event) {
		t.Fatal("admin audit access was denied")
	}
}

func TestTeamEventAuthorizationIsWorkspaceScopedForAdmins(t *testing.T) {
	s := withCfg(&Server{
		sessionOwners: map[string]sessionOwner{
			"run-1": {Principal: "viewer:alice", WorkspaceID: "workspace-a", AgentID: "weather"},
		},
	}, &config.Config{Deployment: config.DeploymentConfig{Mode: config.DeploymentModeTeam}})
	event := message.Event{Type: "tool.result", AgentID: "weather", SessionID: "run-1"}
	if !s.authorizeEvent(eventPrincipal{Principal: "admin:auditor", WorkspaceID: "workspace-a", Role: "admin", Authenticated: true, Admin: true}, event) {
		t.Fatal("same-workspace admin audit access was denied")
	}
	if s.authorizeEvent(eventPrincipal{Principal: "admin:auditor", WorkspaceID: "workspace-b", Role: "admin", Authenticated: true, Admin: true}, event) {
		t.Fatal("admin observed another workspace event")
	}
}

// A gateway with a durable ownership store must answer identically after a
// restart, because the in-process cache is empty on a fresh process and a
// second replica never had one. This is the failure the map could not avoid:
// not an error, just "session not found" for a conversation the user is
// looking at.
func TestSessionAuthorizationSurvivesRestartAndReplicas(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owners.db")
	store, err := session.NewSQLiteOwnershipStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	newGateway := func(shared session.OwnershipStore) *Server {
		gateway := &Server{sessionOwners: make(map[string]sessionOwner), log: zap.NewNop()}
		gateway.SetSessionOwnershipStore(shared)
		return gateway
	}

	app := func(gateway *Server) *fiber.App {
		application := fiber.New(fiber.Config{Immutable: true})
		application.Use(func(c *fiber.Ctx) error {
			identity, ierr := requestctx.New(requestctx.Input{
				Subject: c.Query("subject"), OrganizationID: "org_a", WorkspaceID: c.Query("ws"),
				MembershipID: "mem", Role: "viewer", RequestID: "req",
			})
			if ierr != nil {
				return ierr
			}
			c.Locals(workspaceIdentityLocal, identity)
			c.SetUserContext(requestctx.With(c.UserContext(), identity))
			auth.SetClaims(c, &auth.Claims{Role: "viewer", RegisteredClaims: jwt.RegisteredClaims{Subject: c.Query("subject")}})
			return c.Next()
		})
		application.Post("/claim/:agent/:session", func(c *fiber.Ctx) error {
			if cerr := gateway.claimSession(c, c.Params("agent"), c.Params("session")); cerr != nil {
				return cerr
			}
			return c.SendStatus(http.StatusNoContent)
		})
		application.Get("/read/:agent/:session", func(c *fiber.Ctx) error {
			if rerr := gateway.requireSession(c, c.Params("agent"), c.Params("session")); rerr != nil {
				return rerr
			}
			return c.SendStatus(http.StatusNoContent)
		})
		return application
	}

	call := func(application *fiber.App, method, path, subject, workspace string) int {
		req, _ := http.NewRequest(method, path+"?subject="+url.QueryEscape(subject)+"&ws="+url.QueryEscape(workspace), nil)
		resp, rerr := application.Test(req)
		if rerr != nil {
			t.Fatal(rerr)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	first := app(newGateway(store))
	if got := call(first, http.MethodPost, "/claim/weather/s-1", "alice", "ws_a"); got != http.StatusNoContent {
		t.Fatalf("claim=%d", got)
	}

	// A fresh process: empty cache, same database.
	restarted := app(newGateway(store))
	if got := call(restarted, http.MethodGet, "/read/weather/s-1", "alice", "ws_a"); got != http.StatusNoContent {
		t.Fatalf("the owner lost access after a restart: %d", got)
	}
	if got := call(restarted, http.MethodGet, "/read/weather/s-1", "bob", "ws_a"); got != http.StatusNotFound {
		t.Fatalf("a peer gained access after a restart: %d", got)
	}
	// Another workspace cannot claim or read the same session ID.
	if got := call(restarted, http.MethodPost, "/claim/weather/s-1", "alice", "ws_b"); got != http.StatusNotFound {
		t.Fatalf("another workspace claimed an existing session ID: %d", got)
	}
	if got := call(restarted, http.MethodGet, "/read/weather/s-1", "alice", "ws_b"); got != http.StatusNotFound {
		t.Fatalf("another workspace read the session: %d", got)
	}
}

// Without a durable store the gateway still works, but only for one process —
// which is exactly what the Team-mode readiness gate is for.
func TestInProcessOwnershipStillWorksWithoutADurableStore(t *testing.T) {
	gateway := &Server{sessionOwners: make(map[string]sessionOwner), log: zap.NewNop()}
	if gateway.ownershipStore() != nil {
		t.Fatal("an unwired gateway reported a durable store")
	}
	application := fiber.New(fiber.Config{Immutable: true})
	application.Use(func(c *fiber.Ctx) error {
		auth.SetClaims(c, &auth.Claims{Role: "viewer", RegisteredClaims: jwt.RegisteredClaims{Subject: c.Query("subject")}})
		return c.Next()
	})
	application.Post("/claim/:agent/:session", func(c *fiber.Ctx) error {
		if err := gateway.claimSession(c, c.Params("agent"), c.Params("session")); err != nil {
			return err
		}
		return c.SendStatus(http.StatusNoContent)
	})
	req, _ := http.NewRequest(http.MethodPost, "/claim/weather/s-1?subject=alice", nil)
	resp, err := application.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("claim=%d", resp.StatusCode)
	}
	if got := gateway.sessionOwners["s-1"]; got.Principal != "viewer:alice" || got.Visibility != session.VisibilityPrivate {
		t.Fatalf("in-process owner = %+v", got)
	}
}

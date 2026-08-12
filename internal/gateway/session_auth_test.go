package gateway

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/pkg/message"
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

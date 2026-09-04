package gateway

// Story 19c: scoped plugin tokens on the WebSocket event stream, gated by
// the events.subscribe capability. wsPluginTokenAuth is exercised through a
// probe route (a real WS upgrade can't complete inside app.Test).

import (
	"io"
	"net/http"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/soulacy/soulacy/pkg/plugin"
)

func wsProbeApp(s *Server) *fiber.App {
	app := fiber.New()
	app.Use("/probe", func(c *fiber.Ctx) error {
		if handled, err := s.wsPluginTokenAuth(c); handled {
			return err
		}
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "not a plugin token"})
	})
	app.Get("/probe", func(c *fiber.Ctx) error { return c.SendString("stream ok") })
	return app
}

func probe(t *testing.T, app *fiber.App, target, bearer string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, target, nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestWSPluginToken_EventsSubscribeGrantAdmits(t *testing.T) {
	s, _ := pluginGateway(t, []plugin.Permission{{Cap: "events.subscribe"}})
	tok := issueToken(t, s)
	app := wsProbeApp(s)

	status, body := probe(t, app, "/probe", tok)
	if status != http.StatusOK || body != "stream ok" {
		t.Fatalf("granted token: status=%d body=%q", status, body)
	}

	// Same token via ?api_key= (WS clients that can't set headers).
	status, body = probe(t, app, "/probe?api_key="+tok, "")
	if status != http.StatusOK || body != "stream ok" {
		t.Fatalf("query-param token: status=%d body=%q", status, body)
	}
}

func TestWSPluginToken_WithoutGrantIs403(t *testing.T) {
	// Valid plugin, valid token — but no events.subscribe in the manifest.
	s, _ := pluginGateway(t, []plugin.Permission{{Cap: "vector.search"}})
	tok := issueToken(t, s)
	status, body := probe(t, wsProbeApp(s), "/probe", tok)
	if status != http.StatusForbidden {
		t.Fatalf("ungranted token: status=%d body=%q, want 403", status, body)
	}
}

func TestWSPluginToken_NonPluginCredentialFallsThrough(t *testing.T) {
	s, _ := pluginGateway(t, nil)
	status, _ := probe(t, wsProbeApp(s), "/probe", "some-user-key")
	if status != http.StatusUnauthorized {
		t.Fatalf("non-plugin credential must fall through to user auth: %d", status)
	}
}

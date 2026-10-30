package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/httptestutil"
)

func TestAgensiCallbackURLAllowsLocalhostAndRequiresHTTPSElsewhere(t *testing.T) {
	tests := []struct {
		name, publicURL, host, forwardedProto, want string
		status                                      int
	}{
		{name: "local development", host: "localhost:18789", forwardedProto: "http", want: "http://localhost:18789/oauth/agensi/callback", status: 200},
		{name: "public configured URL", publicURL: "https://soulacy.example/", host: "internal", forwardedProto: "http", want: "https://soulacy.example/oauth/agensi/callback", status: 200},
		{name: "public plain HTTP refused", host: "soulacy.example", forwardedProto: "http", status: 400},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := &Server{cfg: &config.Config{Server: config.ServerConfig{PublicURL: test.publicURL}}}
			app := fiber.New()
			app.Get("/test", func(c *fiber.Ctx) error {
				value, err := s.agensiCallbackURL(c)
				if err != nil {
					return c.Status(400).SendString(err.Error())
				}
				return c.SendString(value)
			})
			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			req.Host = test.host
			req.Header.Set("X-Forwarded-Proto", test.forwardedProto)
			resp, err := app.Test(httptestutil.WithHost(req))
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != test.status {
				t.Fatalf("status = %d, want %d", resp.StatusCode, test.status)
			}
			if test.want != "" {
				body, _ := io.ReadAll(resp.Body)
				if string(body) != test.want {
					t.Fatalf("callback = %q, want %q", body, test.want)
				}
			}
		})
	}
}

func TestPersistAgensiMCPWritesOnlyEncryptedSecretReference(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server:\n  port: 18789\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Server{cfgPath: path}
	body := mcpServerBody{
		ID: agensiMCPID, Transport: "http", URL: agensiMCPURL, PublicOnly: true, Timeout: "60s",
		Auth: mcpAuthBody{Type: "oauth_authorization_code", SecretRef: agensiOAuthSecretRef, TokenURL: "https://mcp.agensi.io/oauth/token", ClientID: "registered-client", Scopes: agensiScopes, Resource: agensiMCPURL},
	}
	if err := s.persistAgensiMCP(body); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, required := range []string{"agensi:", agensiMCPURL, "oauth_authorization_code", agensiOAuthSecretRef, "public_only: true"} {
		if !strings.Contains(text, required) {
			t.Fatalf("saved config missing %q:\n%s", required, text)
		}
	}
	if strings.Contains(text, "access_token") || strings.Contains(text, "refresh_token") {
		t.Fatalf("OAuth tokens leaked into config:\n%s", text)
	}
}

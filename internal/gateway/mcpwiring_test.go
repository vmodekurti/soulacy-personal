package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/httptestutil"
	"github.com/soulacy/soulacy/internal/mcp"
)

// The redaction helper being correct is not the guarantee anyone cares about.
// The guarantee is that the RESPONSE does not contain the token — which needs
// the handler to actually call the helper.
//
// This was found by mutation testing: deleting the redactMCPServers call from
// handleListMCP left every test in disclosure_test.go passing, because they all
// call the helper directly. A unit test on a helper says nothing about whether
// the helper is installed on the path the data actually takes.
func TestHandleListMCP_ResponseCarriesNoCredentials(t *testing.T) {
	// A real *mcp.Client: `false` is not an executable, so the server fails to
	// start and is recorded as disconnected — but its configured Env/Headers
	// are kept, which is exactly what the status snapshot exposes. No process
	// is left running and no network is touched.
	client := mcp.New(mcp.Config{Servers: map[string]mcp.ServerConfig{
		"github": {
			Command: "/nonexistent/soulacy-test-binary",
			Env:     map[string]string{"GITHUB_TOKEN": "ghp_realtokenvalue"},
			Headers: map[string]string{"Authorization": "Bearer sk-realkeyvalue"},
		},
	}}, zap.NewNop())

	s := &Server{mcp: client}
	app := fiber.New()
	app.Get("/api/v1/mcp", s.handleListMCP)

	resp, err := app.Test(httptestutil.WithHost(httptest.NewRequest(http.MethodGet, "/api/v1/mcp", nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	// Sanity: the snapshot really did reach the response, otherwise this test
	// would pass against a handler that returns nothing at all.
	if !strings.Contains(string(body), "github") {
		t.Fatalf("the server list never reached the response, so this test proves nothing: %s", body)
	}
	for _, secret := range []string{"ghp_realtokenvalue", "sk-realkeyvalue"} {
		if strings.Contains(string(body), secret) {
			t.Errorf("GET /api/v1/mcp returned the credential %q verbatim:\n%s", secret, body)
		}
	}
}

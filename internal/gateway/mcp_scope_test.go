// mcp_scope_test.go — no handler may reach the deployment-wide MCP client.
//
// This is a SOURCE guard rather than a behavioural one because the mistake it
// catches is invisible at runtime: `s.mcp` and `s.mcpFor(c)` return the same
// type, both compile, both work, and on a single-tenant deployment they return
// the same client. The difference only shows up with two tenants in the
// process, which is exactly the configuration nobody runs while writing a
// handler. A test that exercised a handler would have to arrange a second
// workspace with live subprocesses to notice; reading the source notices for
// free, in every handler, including ones written next year.
package gateway

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/mcp"
	"github.com/soulacy/soulacy/internal/requestctx"
	"github.com/soulacy/soulacy/internal/sandbox"
)

// unscopedMCP matches a read of the Server's shared client. The accessor file
// is where the fallback legitimately lives, so it is the one exemption.
var unscopedMCP = regexp.MustCompile(`\bs\.mcp\b`)

func TestNoHandlerReachesPastTheScopedMCPAccessor(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	// The guard must be seen to match something, or a rename of the field
	// turns it into a test that passes for every possible source file.
	sawAccessor := false
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		source, readErr := os.ReadFile(filepath.Join(".", name))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if name == "mcp_scope.go" {
			if unscopedMCP.Match(source) {
				sawAccessor = true
			}
			continue
		}
		for i, line := range strings.Split(string(source), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") || !unscopedMCP.MatchString(line) {
				continue
			}
			// server.go declares the field; declaring it is not using it.
			if name == "server.go" && strings.Contains(line, "*mcp.Client") {
				continue
			}
			t.Errorf("%s:%d uses the deployment-wide MCP client directly — use s.mcpFor(c) or "+
				"s.mcpForWorkspace(id). One client means one set of subprocesses in one working "+
				"directory for every tenant, and the two spellings are indistinguishable until "+
				"a second workspace exists:\n\t%s", name, i+1, trimmed)
		}
	}
	if !sawAccessor {
		t.Fatal("the guard's pattern matched nothing even in mcp_scope.go; the field was renamed and this test now vouches for nothing")
	}
}

// The source guard above says no handler bypasses the accessor. This says the
// accessor itself does what its name claims — the two failures are
// independent, and the second is invisible to the first: an mcpFor that
// ignored the pool entirely would satisfy every call site in the package.
func TestTheAccessorGivesTwoWorkspacesTwoClients(t *testing.T) {
	pool := mcp.NewPool(mcp.Config{}, poolConfinementStub{}, zap.NewNop())
	defer pool.Close()
	shared := &mcp.Client{}
	s := &Server{mcp: shared}
	s.SetMCPPool(pool)

	app := fiber.New(fiber.Config{DisableStartupMessage: true, Immutable: true})
	seen := map[string]*mcp.Client{}
	app.Get("/:ws", func(c *fiber.Ctx) error {
		ws := c.Params("ws")
		identity, err := requestctx.New(requestctx.Input{Subject: "u", OrganizationID: "org", WorkspaceID: ws, MembershipID: "m", Role: "owner", RequestID: "r"})
		if err != nil {
			t.Fatal(err)
		}
		c.Locals(workspaceIdentityLocal, identity)
		seen[ws] = s.mcpFor(c)
		return c.SendString("ok")
	})
	for _, ws := range []string{"ws-a", "ws-b"} {
		req := httptest.NewRequest("GET", "/"+ws, nil)
		if _, err := app.Test(req); err != nil {
			t.Fatal(err)
		}
	}

	if seen["ws-a"] == nil || seen["ws-b"] == nil {
		t.Fatal("the accessor returned nil; call sites would panic")
	}
	if seen["ws-a"] == seen["ws-b"] {
		t.Error("two workspaces got the SAME MCP client, so they share one set of subprocesses")
	}
	// The shared client must be unreachable once a pool is installed. Returning
	// it would compile, work on one tenant, and hand every workspace the
	// deployment-wide servers.
	for ws, client := range seen {
		if client == shared {
			t.Errorf("%s got the deployment-wide client instead of its own", ws)
		}
	}
}

// poolConfinementStub gives every workspace its own directory so the pool
// builds a distinct client per workspace without spawning anything: the
// template is empty, so there are no servers to start.
type poolConfinementStub struct{}

func (poolConfinementStub) WorkspaceConfinement(workspaceID string) (string, sandbox.Limits, string, error) {
	return "/tmp/" + workspaceID, sandbox.Limits{}, "", nil
}

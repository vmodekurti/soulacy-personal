package gateway

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/requestctx"
)

func TestCanonicalGitHubRepositoryRejectsAmbiguousAndUnsafeSources(t *testing.T) {
	good, err := canonicalGitHubRepository("https://github.com/wshobson/maverick-mcp.git/")
	if err != nil || good != "https://github.com/wshobson/maverick-mcp" {
		t.Fatalf("canonical source = %q, %v", good, err)
	}
	for _, raw := range []string{
		"http://github.com/a/b", "https://gitlab.com/a/b", "https://github.com/a/b/tree/main",
		"https://github.com/a/b?ref=main", "https://github.com/a%2fb/c", "https://user@github.com/a/b",
	} {
		if _, err := canonicalGitHubRepository(raw); err == nil {
			t.Errorf("unsafe source accepted: %s", raw)
		}
	}
}

func TestInspectMCPRepositoryBuildsClosedContainerPlan(t *testing.T) {
	root := t.TempDir()
	manifest := `{
      "name":"io.github.wshobson/maverick-mcp",
      "description":"Stocks",
      "repository":{"url":"https://github.com/wshobson/maverick-mcp"},
      "packages":[
        {"registryType":"pypi","identifier":"maverick-mcp-server","transport":{"type":"stdio"},"environmentVariables":[{"name":"API_KEY","isRequired":false,"isSecret":true}]},
        {"registryType":"oci","identifier":"ghcr.io/wshobson/maverick-mcp:1.0.0","transport":{"type":"stdio"}}
      ]}`
	mustWriteInstallFixture(t, filepath.Join(root, "server.json"), manifest)
	mustWriteInstallFixture(t, filepath.Join(root, "Dockerfile"), "FROM python:3.12\nUSER 1000\nCMD [\"app\", \"--transport\", \"http\"]\n")
	mustWriteInstallFixture(t, filepath.Join(root, "pyproject.toml"), "[project.scripts]\nmaverick-mcp-server = \"maverick.server:main\"\n")

	stage, err := inspectMCPRepository(root, "https://github.com/wshobson/maverick-mcp", strings.Repeat("a", 40))
	if err != nil {
		t.Fatal(err)
	}
	if stage.ServerID != "maverick" || stage.Image != "ghcr.io/wshobson/maverick-mcp:1.0.0" {
		t.Fatalf("unexpected plan: %+v", stage)
	}
	want := []string{"uv", "run", "maverick-mcp-server", "--transport", "stdio"}
	if strings.Join(stage.Args, " ") != strings.Join(want, " ") {
		t.Fatalf("args = %q", stage.Args)
	}
	if len(stage.Env) != 1 || !stage.Env[0].Secret {
		t.Fatalf("environment report = %+v", stage.Env)
	}
}

func TestInspectMCPRepositoryRefusesHostBuildFallback(t *testing.T) {
	root := t.TempDir()
	mustWriteInstallFixture(t, filepath.Join(root, "server.json"), `{
      "name":"unsafe","repository":{"url":"https://github.com/acme/unsafe"},
      "packages":[{"registryType":"pypi","identifier":"unsafe","transport":{"type":"stdio"}}]}`)
	if _, err := inspectMCPRepository(root, "https://github.com/acme/unsafe", "abc"); err == nil || !strings.Contains(err.Error(), "OCI") {
		t.Fatalf("expected OCI refusal, got %v", err)
	}
}

func TestReadInstallFileRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	mustWriteInstallFixture(t, target, `{}`)
	link := filepath.Join(root, "server.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readInstallFile(link, 1024); err == nil {
		t.Fatal("symlink was accepted")
	}
}

func TestWorkspaceMCPInstallIsAdminOnly(t *testing.T) {
	for _, role := range []string{"viewer", "developer", "operator", "admin", "owner"} {
		t.Run(role, func(t *testing.T) {
			s := platformServer(t, config.DeploymentModeTeam)
			app := fiber.New(fiber.Config{DisableStartupMessage: true})
			app.Use(func(c *fiber.Ctx) error {
				identity, err := requestctx.New(requestctx.Input{Subject: "usr_one", OrganizationID: "org_one", WorkspaceID: "ws_one", MembershipID: "mem_one", Role: role, RequestID: "req_one", PrincipalKind: "user"})
				if err != nil {
					t.Fatal(err)
				}
				c.SetUserContext(requestctx.With(c.UserContext(), identity))
				return c.Next()
			})
			app.Post("/install", s.workspaceMCPAdmin, func(c *fiber.Ctx) error { return c.SendStatus(fiber.StatusNoContent) })
			resp, err := app.Test(httptest.NewRequest(http.MethodPost, "/install", nil))
			if err != nil {
				t.Fatal(err)
			}
			want := fiber.StatusForbidden
			if role == "admin" || role == "owner" {
				want = fiber.StatusNoContent
			}
			if resp.StatusCode != want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, want)
			}
		})
	}
}

func mustWriteInstallFixture(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

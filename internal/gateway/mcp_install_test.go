package gateway

import (
	"context"
	"encoding/json"
	"fmt"
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

func TestInspectMCPRepositoryAcceptsCurrentSchemaHomepageIdentity(t *testing.T) {
	root := t.TempDir()
	mustWriteInstallFixture(t, filepath.Join(root, "server.json"), `{
      "name":"io.github.dgahagan/weather-mcp","version":"1.23.0",
      "homepage":"https://github.com/weather-mcp/weather-mcp",
      "packages":[{"registryType":"npm","identifier":"@dangahagan/weather-mcp","version":"1.23.0","transport":{"type":"stdio"}}]}`)
	stage, err := inspectMCPRepository(root, "https://github.com/weather-mcp/weather-mcp", "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if stage.ServerID != "weather" || stage.Runtime != "npm" {
		t.Fatalf("current-schema plan = %+v", stage)
	}
}

func TestManifestRepositoryIdentityRejectsMissingMismatchAndConflict(t *testing.T) {
	source := "https://github.com/weather-mcp/weather-mcp"
	for name, manifest := range map[string]mcpInstallManifest{
		"missing":  {},
		"mismatch": {Homepage: "https://github.com/attacker/weather-mcp"},
		"conflict": func() mcpInstallManifest {
			var value mcpInstallManifest
			value.Homepage = source
			value.Repository.URL = "https://github.com/attacker/weather-mcp"
			return value
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			if manifestRepositoryMatches(manifest, source) {
				t.Fatal("unsafe manifest identity accepted")
			}
		})
	}
}

func TestInspectMCPRepositoryRefusesUnversionedPackageFallback(t *testing.T) {
	root := t.TempDir()
	mustWriteInstallFixture(t, filepath.Join(root, "server.json"), `{
      "name":"unsafe","repository":{"url":"https://github.com/acme/unsafe"},
      "packages":[{"registryType":"pypi","identifier":"unsafe","transport":{"type":"stdio"}}]}`)
	if _, err := inspectMCPRepository(root, "https://github.com/acme/unsafe", "abc"); err == nil || !strings.Contains(err.Error(), "exact published version") {
		t.Fatalf("expected exact-version refusal, got %v", err)
	}
}

func TestInspectMCPRepositoryUsesDisposableRunnerForExactPackages(t *testing.T) {
	for _, tc := range []struct {
		registry, identifier, version, image, command string
	}{
		{"npm", "@acme/weather-mcp", "2.3.1", mcpNodeRunnerImage, "npm exec --yes --ignore-scripts"},
		{"pypi", "weather-mcp", "1.4.0", mcpPythonRunnerImage, "uvx weather-mcp==1.4.0"},
	} {
		t.Run(tc.registry, func(t *testing.T) {
			root := t.TempDir()
			manifest := fmt.Sprintf(`{"name":"weather-mcp","version":"%s","repository":{"url":"https://github.com/acme/weather"},"packages":[{"registryType":"%s","identifier":"%s","transport":{"type":"stdio"}}]}`, tc.version, tc.registry, tc.identifier)
			mustWriteInstallFixture(t, filepath.Join(root, "server.json"), manifest)
			stage, err := inspectMCPRepository(root, "https://github.com/acme/weather", "abc")
			if err != nil {
				t.Fatal(err)
			}
			if stage.Image != tc.image || !strings.Contains(strings.Join(stage.Args, " "), tc.command) || stage.Runtime != tc.registry {
				t.Fatalf("runner plan = %+v", stage)
			}
		})
	}
}

func TestApprovalFingerprintBindsPermissions(t *testing.T) {
	stage := stagedMCPInstall{SourceURL: "https://github.com/acme/weather", Revision: "abc", ServerID: "weather", Image: "node:22-alpine"}
	stage.Permissions = mcpInstallPermissions{Network: "none", Workspace: "none"}
	a := mcpInstallFingerprint(stage)
	stage.Permissions.Network = "public"
	b := mcpInstallFingerprint(stage)
	if a == b {
		t.Fatal("changing network permission did not change the approval fingerprint")
	}
}

func TestApprovalFingerprintBindsInstallRequest(t *testing.T) {
	stage := stagedMCPInstall{SourceURL: "https://github.com/acme/weather", Revision: "abc", ServerID: "weather", Image: "node:22-alpine"}
	a := mcpInstallFingerprint(stage)
	stage.RequestID = "mcp_req_123"
	b := mcpInstallFingerprint(stage)
	if a == b {
		t.Fatal("adding an install request did not change the approval fingerprint")
	}
}

func TestPackageRunnerRequiresInternetPermission(t *testing.T) {
	permissions := mcpInstallPermissions{Network: "none", Workspace: "none"}
	for _, runtime := range []string{"npm", "pypi"} {
		if err := validateMCPRuntimePermissions(runtime, permissions); err == nil || !strings.Contains(err.Error(), "require Internet access") {
			t.Fatalf("%s permission validation = %v", runtime, err)
		}
	}
	if err := validateMCPRuntimePermissions("oci", permissions); err != nil {
		t.Fatalf("offline OCI validation = %v", err)
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

func TestInspectMCPRepositoryIgnoresUnrelatedSymlink(t *testing.T) {
	root := t.TempDir()
	mustWriteInstallFixture(t, filepath.Join(root, "server.json"), `{
      "name":"io.github.acme.weather-mcp",
      "version":"1.2.3",
      "repository":{"url":"https://github.com/acme/weather-mcp"},
      "packages":[{"registryType":"pypi","identifier":"weather-mcp","version":"1.2.3","transport":{"type":"stdio"}}]}`)
	mustWriteInstallFixture(t, filepath.Join(root, "AGENTS.md"), "project guidance")
	if err := os.Symlink("AGENTS.md", filepath.Join(root, "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectMCPRepository(root, "https://github.com/acme/weather-mcp", "abc123"); err != nil {
		t.Fatalf("unrelated repository symlink rejected: %v", err)
	}
}

func TestInspectMCPRepositoryInfersLockedNodeSource(t *testing.T) {
	root := t.TempDir()
	mustWriteInstallFixture(t, filepath.Join(root, "package.json"), `{
      "name":"trading-mcp","version":"1.0.0","description":"Trading",
      "main":"dist/server.js","scripts":{"build":"tsc"},
      "dependencies":{"@modelcontextprotocol/sdk":"^0.5.0"}}`)
	mustWriteInstallFixture(t, filepath.Join(root, "package-lock.json"), `{"lockfileVersion":3}`)
	mustWriteInstallFixture(t, filepath.Join(root, "env.example"), "OPENAI_API_KEY=replace-me\nLOG_LEVEL=info\n")
	stage, err := inspectMCPRepository(root, "https://github.com/acme/trading-mcp", "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if stage.Runtime != "source-npm" || stage.ServerID != "trading" || strings.Join(stage.Args, " ") != "node /app/dist/server.js" {
		t.Fatalf("source plan = %+v", stage)
	}
	if len(stage.Env) != 2 || !stage.Env[1].Secret {
		t.Fatalf("inferred environment = %+v", stage.Env)
	}
}

func TestNPMSourceDockerfileSeparatesNetworkedInstallFromOfflineBuild(t *testing.T) {
	base := "node@sha256:" + strings.Repeat("a", 64)
	dockerfile := npmSourceDockerfile(base, "dist/server.js")
	for _, required := range []string{
		"FROM " + base, "npm ci --ignore-scripts", "RUN --network=none npm run build",
		"COPY --chown=node:node", "npm prune --omit=dev --ignore-scripts", "USER node", `CMD ["node", "dist/server.js"]`,
	} {
		if !strings.Contains(dockerfile, required) {
			t.Errorf("generated Dockerfile missing %q:\n%s", required, dockerfile)
		}
	}
}

func TestInspectMCPRepositoryInfersPrimaryNodeBinAndIsolatesCustomBuild(t *testing.T) {
	root := t.TempDir()
	mustWriteInstallFixture(t, filepath.Join(root, "package.json"), `{
      "name":"stock-scanner-mcp","version":"1.18.0","description":"Stocks",
      "type":"module","bin":{"stock-scanner-mcp":"./dist/index.js","stock-scanner-install-skills":"./dist/install-skills.js"},
      "scripts":{"build":"npm run generate-openapi && tsup src/index.ts --format esm --dts --clean"},
      "dependencies":{"@modelcontextprotocol/sdk":"^1.27.1"}}`)
	mustWriteInstallFixture(t, filepath.Join(root, "package-lock.json"), `{"lockfileVersion":3}`)
	stage, err := inspectMCPRepository(root, "https://github.com/acme/stock-scanner-mcp", "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(stage.Args, " "); got != "node /app/dist/index.js" {
		t.Fatalf("source plan command = %q", got)
	}
}

func TestInspectPythonSourceRepositoryRequiresFrozenLock(t *testing.T) {
	root := t.TempDir()
	mustWriteInstallFixture(t, filepath.Join(root, "pyproject.toml"), `[project]
name = "weather-mcp"
version = "1.2.3"
description = "Weather"
[project.scripts]
weather-mcp = "weather.server:main"
`)
	if _, err := inspectPythonSourceRepository(root, "https://github.com/acme/weather", "abc"); err == nil || !strings.Contains(err.Error(), "uv.lock") {
		t.Fatalf("missing lock validation = %v", err)
	}
	mustWriteInstallFixture(t, filepath.Join(root, "uv.lock"), "version = 1\nrevision = 3\n")
	stage, err := inspectPythonSourceRepository(root, "https://github.com/acme/weather", "abc")
	if err != nil {
		t.Fatal(err)
	}
	if stage.Runtime != "source-python" || strings.Join(stage.Args, " ") != "uv run --offline --no-sync weather-mcp" {
		t.Fatalf("python plan = %+v", stage)
	}
}

func TestPythonSourceDockerfileInstallsProjectOffline(t *testing.T) {
	base := "uv@sha256:" + strings.Repeat("b", 64)
	dockerfile := pythonSourceDockerfile(base, []string{"uv", "run", "--offline", "--no-sync", "weather-mcp"})
	for _, required := range []string{
		"uv pip install --target /tmp/build-tools hatchling==1.27.0 editables==0.5",
		"uv sync --frozen --no-dev --no-install-project",
		"RUN --network=none uv sync --offline --frozen --no-dev --no-build-isolation",
		"USER 65532:65532", `CMD ["uv","run","--offline","--no-sync","weather-mcp"]`,
	} {
		if !strings.Contains(dockerfile, required) {
			t.Errorf("generated Python Dockerfile missing %q:\n%s", required, dockerfile)
		}
	}
}

func TestOptionalEmptyInstallSettingIsOmitted(t *testing.T) {
	stage := stagedMCPInstall{Env: []mcpInstallEnv{{Name: "REDIS_PORT"}, {Name: "API_KEY", Secret: true}}}
	env, _, err := (&Server{}).storeMCPInstallSettings(context.Background(), stage, map[string]string{"REDIS_PORT": "", "API_KEY": ""})
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := env["REDIS_PORT"]; exists {
		t.Fatalf("optional empty scalar was injected: %v", env)
	}
	if _, exists := env["API_KEY"]; !exists {
		t.Fatalf("secret placeholder was lost: %v", env)
	}
}

func TestPythonSourceGetsPrivateDurableDatabaseDefault(t *testing.T) {
	stage := stagedMCPInstall{Runtime: "source-python", ServerID: "maverick", Env: []mcpInstallEnv{{Name: "DATABASE_URL"}}}
	env, _, err := (&Server{}).storeMCPInstallSettings(context.Background(), stage, map[string]string{"DATABASE_URL": ""})
	if err != nil {
		t.Fatal(err)
	}
	if env["DATABASE_URL"] != "sqlite:////data/maverick.db" {
		t.Fatalf("database default = %q", env["DATABASE_URL"])
	}
}

func TestGeneratedPythonDatabaseDoesNotRequireCredentialVault(t *testing.T) {
	stage := stagedMCPInstall{Runtime: "source-python", ServerID: "maverick", Env: []mcpInstallEnv{{Name: "DATABASE_URL", Secret: true}}}
	env, stored, err := (&Server{}).storeMCPInstallSettings(context.Background(), stage, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if stored != 0 || env["DATABASE_URL"] != "sqlite:////data/maverick.db" {
		t.Fatalf("generated database = %#v; stored secrets = %d", env, stored)
	}
}

func TestWorkspaceMCPInstallIsAdminOnly(t *testing.T) {
	for _, role := range []string{"viewer", "developer", "operator", "admin", "owner"} {
		t.Run(role, func(t *testing.T) {
			s := platformServer(t, config.DeploymentModeTeam)
			app := fiber.New(fiber.Config{DisableStartupMessage: true, Immutable: true})
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

func TestRunBudgetOverrideUsesVerifiedWorkspaceRole(t *testing.T) {
	for _, role := range []string{"viewer", "developer", "operator", "admin", "owner"} {
		t.Run(role, func(t *testing.T) {
			s := platformServer(t, config.DeploymentModeTeam)
			app := fiber.New(fiber.Config{DisableStartupMessage: true, Immutable: true})
			app.Use(func(c *fiber.Ctx) error {
				identity, err := requestctx.New(requestctx.Input{Subject: "usr_one", OrganizationID: "org_one", WorkspaceID: "ws_one", MembershipID: "mem_one", Role: role, RequestID: "req_one", PrincipalKind: "user"})
				if err != nil {
					t.Fatal(err)
				}
				c.SetUserContext(requestctx.With(c.UserContext(), identity))
				return c.Next()
			})
			app.Get("/allowed", func(c *fiber.Ctx) error { return c.JSON(fiber.Map{"allowed": s.canOverrideRunBudget(c)}) })
			resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/allowed", nil))
			if err != nil {
				t.Fatal(err)
			}
			var body struct {
				Allowed bool `json:"allowed"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			want := role == "admin" || role == "owner"
			if body.Allowed != want {
				t.Fatalf("allowed = %v, want %v", body.Allowed, want)
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

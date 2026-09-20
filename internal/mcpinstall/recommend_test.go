package mcpinstall

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func writeFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestAnalyzeDirectoryPrefersPublishedRemote(t *testing.T) {
	root := writeFixture(t, map[string]string{
		"server.json":  `{"name":"weather","packages":[{"registryType":"npm","identifier":"weather-mcp"}],"remotes":[{"type":"streamable-http","url":"https://api.example.com/mcp"}]}`,
		"package.json": `{"name":"weather-mcp","bin":{"weather":"index.js"}}`,
	})
	got, err := AnalyzeDirectory(root, "https://github.com/acme/weather")
	if err != nil {
		t.Fatal(err)
	}
	if got.Method != MethodRemote || got.Endpoint != "https://api.example.com/mcp" || got.CanInstallHere {
		t.Fatalf("unexpected recommendation: %+v", got)
	}
}

func TestAnalyzeDirectoryDoesNotRecommendInsecureHostedEndpoint(t *testing.T) {
	root := writeFixture(t, map[string]string{
		"server.json":    `{"name":"weather","remotes":[{"type":"streamable-http","url":"http://api.example.com/mcp"}]}`,
		"pyproject.toml": "[project]\nname='weather-mcp'\n[project.scripts]\nweather-mcp='weather:main'",
	})
	got, err := AnalyzeDirectory(root, "https://github.com/acme/weather")
	if err != nil {
		t.Fatal(err)
	}
	if got.Method == MethodRemote {
		t.Fatalf("insecure public endpoint must not be recommended: %+v", got)
	}
}

func TestAnalyzeDirectoryUsesRunnerForDeviceLocalServer(t *testing.T) {
	root := writeFixture(t, map[string]string{
		"README.md":    "This MCP server provides access to your local filesystem.",
		"package.json": `{"name":"filesystem-mcp","bin":{"filesystem-mcp":"index.js"}}`,
	})
	got, err := AnalyzeDirectory(root, "https://github.com/acme/filesystem-mcp")
	if err != nil {
		t.Fatal(err)
	}
	if got.Method != MethodRunner {
		t.Fatalf("method = %q, want %q", got.Method, MethodRunner)
	}
}

func TestAnalyzeDirectoryUsesCompanionForStatefulContainer(t *testing.T) {
	root := writeFixture(t, map[string]string{
		"README.md":      "Run the Streamable HTTP server at /mcp. Supports SQLite, PostgreSQL and Redis.",
		"Dockerfile":     "FROM python:3.12\nEXPOSE 8000",
		"pyproject.toml": "[project]\nname='maverick-mcp'\n[project.scripts]\nmaverick-mcp='maverick:main'",
	})
	got, err := AnalyzeDirectory(root, "https://github.com/acme/maverick-mcp")
	if err != nil {
		t.Fatal(err)
	}
	if got.Method != MethodCompanion {
		t.Fatalf("method = %q, want %q; reasons=%v", got.Method, MethodCompanion, got.Reasons)
	}
}

func TestAnalyzeDirectoryUsesGatewayForSelfContainedPackage(t *testing.T) {
	root := writeFixture(t, map[string]string{
		"README.md":      "A small stdio MCP weather server.",
		"pyproject.toml": "[project]\nname='weather-mcp'\nrequires-python='>=3.12'\n[project.scripts]\nweather-mcp='weather:main'",
		".env.example":   "WEATHER_API_KEY=\nLOG_LEVEL=info\n",
	})
	got, err := AnalyzeDirectory(root, "https://github.com/acme/weather-mcp")
	if err != nil {
		t.Fatal(err)
	}
	if got.Method != MethodGateway || !got.CanInstallHere {
		t.Fatalf("unexpected recommendation: %+v", got)
	}
	if strings.Join(got.Evidence.Requirements, ",") != "python >=3.12" {
		t.Fatalf("requirements = %v", got.Evidence.Requirements)
	}
	if strings.Join(got.Evidence.EnvironmentVariables, ",") != "LOG_LEVEL,WEATHER_API_KEY" {
		t.Fatalf("environment variables = %v", got.Evidence.EnvironmentVariables)
	}
}

func TestPlanningTextIncludesBoundedUntrustedREADMEAndFailureDetail(t *testing.T) {
	root := writeFixture(t, map[string]string{
		"README.md": "Custom native runtime required. No package metadata is published.",
	})
	got, err := AnalyzeDirectory(root, "https://github.com/acme/unknown-mcp")
	if err != nil {
		t.Fatal(err)
	}
	if got.Method != MethodReview {
		t.Fatalf("method = %q, want %q", got.Method, MethodReview)
	}
	text := got.PlanningText()
	for _, want := range []string{"<untrusted_repository_readme>", "Custom native runtime required", "missing requirement", "machine-readable installation metadata"} {
		if !strings.Contains(text, want) {
			t.Fatalf("planning text missing %q:\n%s", want, text)
		}
	}
}

func TestREADMEEvidenceIsBoundedAndValidUTF8(t *testing.T) {
	root := writeFixture(t, map[string]string{
		"README.md": strings.Repeat("é", 5000),
	})
	got, err := AnalyzeDirectory(root, "https://github.com/acme/unknown-mcp")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Evidence.READMEExcerpt) > 6300 || !strings.Contains(got.Evidence.READMEExcerpt, "README truncated by Soulacy") || !utf8.ValidString(got.Evidence.READMEExcerpt) {
		t.Fatalf("README excerpt was not bounded: %d bytes", len(got.Evidence.READMEExcerpt))
	}
}

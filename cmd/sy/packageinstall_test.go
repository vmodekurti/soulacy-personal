package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectURLPackageKind(t *testing.T) {
	t.Run("skill", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("# Test"), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := detectURLPackageKind(dir)
		if err != nil || got != urlPackageSkill {
			t.Fatalf("got %q, %v", got, err)
		}
	})
	t.Run("mcp server manifest", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "server.json"), []byte(`{"name":"io.example.demo"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := detectURLPackageKind(dir)
		if err != nil || got != urlPackageMCP {
			t.Fatalf("got %q, %v", got, err)
		}
	})
	t.Run("hybrid repository defaults to skill but supports explicit mcp", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("# Test"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "mcp_server.py"), []byte("print('mcp')"), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := detectURLPackageKind(dir)
		if err != nil || got != urlPackageSkill {
			t.Fatalf("auto detection got %q, %v", got, err)
		}
		if !repositorySupportsPackageKind(dir, urlPackageMCP) {
			t.Fatal("hybrid repository did not support explicit MCP selection")
		}
	})
	t.Run("unknown", func(t *testing.T) {
		if _, err := detectURLPackageKind(t.TempDir()); err == nil {
			t.Fatal("expected an unsupported-package error")
		}
	})
	t.Run("conventional MCP monorepo", func(t *testing.T) {
		root := t.TempDir()
		writePythonMCPFixture(t, filepath.Join(root, "servers", "flight_server"), "flight-server", "flight_server.py")
		writePythonMCPFixture(t, filepath.Join(root, "servers", "weather_server"), "weather-server", "weather_server.py")
		got, err := detectURLPackageKind(root)
		if err != nil || got != urlPackageMCP {
			t.Fatalf("got %q, %v", got, err)
		}
		packages, err := discoverMCPPackages(root)
		if err != nil {
			t.Fatal(err)
		}
		if len(packages) != 2 || packages[0].Name != "flight-server" || packages[0].Entrypoint != "flight_server.py" {
			t.Fatalf("packages = %#v", packages)
		}
	})
	t.Run("workspace manifest falls through to servers", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"name":"mcp-workspace","workspaces":["servers/*"]}`), 0o644); err != nil {
			t.Fatal(err)
		}
		writePythonMCPFixture(t, filepath.Join(root, "servers", "flight_server"), "flight-server", "flight_server.py")
		packages, err := discoverMCPPackages(root)
		if err != nil || len(packages) != 1 || packages[0].Name != "flight-server" {
			t.Fatalf("packages = %#v, %v", packages, err)
		}
	})
}

func TestDiscoverPythonMCPEntrypoint(t *testing.T) {
	t.Run("prefers directory convention", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "weather_server")
		writePythonMCPFixture(t, dir, "weather-server", "weather_server.py")
		if err := os.WriteFile(filepath.Join(dir, "weatherstack_server.py"), []byte("from fastmcp import FastMCP\nmcp=FastMCP('alternate')\nmcp.run()\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := discoverPythonMCPEntrypoint(dir)
		if err != nil || got != "weather_server.py" {
			t.Fatalf("entrypoint = %q, %v", got, err)
		}
	})

	t.Run("rejects ambiguity", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "odd")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		body := []byte("from fastmcp import FastMCP\nmcp=FastMCP('test')\nmcp.run()\n")
		for _, name := range []string{"one.py", "two.py"} {
			if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := discoverPythonMCPEntrypoint(dir); err == nil {
			t.Fatal("expected ambiguous entrypoints to be rejected")
		}
	})
}

func TestDiscoverMCPEnvironmentReferences(t *testing.T) {
	dir := t.TempDir()
	body := []byte("import os\na=os.getenv('SERPAPI_KEY')\nb=os.environ[\"DIRECT_KEY\"]\nc=os.environ.get('OPTIONAL_KEY')\n")
	if err := os.WriteFile(filepath.Join(dir, "server.py"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	got := discoverMCPEnvironmentReferences(dir)
	want := []string{"DIRECT_KEY", "OPTIONAL_KEY", "SERPAPI_KEY"}
	if len(got) != len(want) {
		t.Fatalf("environment references = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("environment references = %v", got)
		}
	}
}

func TestCopyPackageDirRejectsSymlink(t *testing.T) {
	src := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("do not copy"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(src, "linked-secret")); err != nil {
		t.Fatal(err)
	}
	if err := copyPackageDir(src, filepath.Join(t.TempDir(), "dest")); err == nil {
		t.Fatal("expected package symlink to be rejected")
	}
}

func TestWriteManagedMCPLauncherDoesNotResolveOutsideRoot(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "mcp-servers", "weather-server")
	launcher, err := writeManagedMCPLauncher(dest, "python", `exec "${0%/*}/../venv/bin/python" "$@"`)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(launcher)
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(filepath.Dir(dest))
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || len(rel) >= 3 && rel[:3] == "../" {
		t.Fatalf("launcher resolved outside managed root: %q (%v)", resolved, err)
	}
	info, err := os.Stat(launcher)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("launcher is not executable: %v", info.Mode())
	}
}

func TestLegacyManagedLauncherKind(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "mcp-servers", "weather-server")
	if got := legacyManagedLauncherKind(dest, filepath.Join(dest, "venv", "bin", "python")); got != "python" {
		t.Fatalf("python launcher kind = %q", got)
	}
	if got := legacyManagedLauncherKind(dest, "node"); got != "node" {
		t.Fatalf("node launcher kind = %q", got)
	}
	if got := legacyManagedLauncherKind(dest, "/tmp/unmanaged/python"); got != "" {
		t.Fatalf("unmanaged command was eligible for repair: %q", got)
	}
}

func TestInstallNodeMCPBuildsBeforePruningDevDependencies(t *testing.T) {
	binDir := t.TempDir()
	callsFile := filepath.Join(t.TempDir(), "npm-calls")
	npm := filepath.Join(binDir, "npm")
	if err := os.WriteFile(npm, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$NPM_CALLS\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	t.Setenv("NPM_CALLS", callsFile)

	sourceDir := t.TempDir()
	manifest := `{"name":"typescript-mcp","bin":{"typescript-mcp":"dist/index.js"},"scripts":{"prepare":"npm run build"},"devDependencies":{"typescript":"^5.3.3"}}`
	if err := os.WriteFile(filepath.Join(sourceDir, "package.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "typescript-mcp")
	command, args, err := installMCPRuntime(context.Background(), dest, sourceDir, "")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(command) != filepath.Join(dest, "bin") || len(args) != 1 || args[0] != filepath.Join(sourceDir, "dist/index.js") {
		t.Fatalf("launcher = %q %v", command, args)
	}

	b, err := os.ReadFile(callsFile)
	if err != nil {
		t.Fatal(err)
	}
	calls := strings.Split(strings.TrimSpace(string(b)), "\n")
	want := []string{
		"install --include=dev --prefix " + sourceDir,
		"prune --omit=dev --ignore-scripts --prefix " + sourceDir,
	}
	if len(calls) != len(want) {
		t.Fatalf("npm calls = %q", calls)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("npm call %d = %q, want %q", i+1, calls[i], want[i])
		}
	}
}

func TestEnsureMCPBrowserRuntimeInstallsNodePatchrightChromium(t *testing.T) {
	sourceDir := t.TempDir()
	manifest := `{"name":"notebooklm-mcp","dependencies":{"patchright":"^1.56.0"}}`
	if err := os.WriteFile(filepath.Join(sourceDir, "package.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(sourceDir, "node_modules", ".bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	callsFile := filepath.Join(t.TempDir(), "browser-calls")
	installer := "#!/bin/sh\nprintf '%s|%s\\n' \"$*\" \"$PLAYWRIGHT_BROWSERS_PATH\" >> \"$BROWSER_CALLS\"\n"
	if err := os.WriteFile(filepath.Join(binDir, "patchright"), []byte(installer), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BROWSER_CALLS", callsFile)
	t.Setenv("PLAYWRIGHT_BROWSERS_PATH", filepath.Join(t.TempDir(), "browsers"))

	got, err := ensureMCPBrowserRuntime(context.Background(), sourceDir, "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "patchright Chromium" {
		t.Fatalf("browser runtime = %q", got)
	}
	b, err := os.ReadFile(callsFile)
	if err != nil {
		t.Fatal(err)
	}
	if want := "install chromium|" + os.Getenv("PLAYWRIGHT_BROWSERS_PATH"); strings.TrimSpace(string(b)) != want {
		t.Fatalf("installer call = %q, want %q", strings.TrimSpace(string(b)), want)
	}
}

func TestEnsureMCPBrowserRuntimeInstallsPythonPlaywrightChromium(t *testing.T) {
	sourceDir := t.TempDir()
	pyproject := "[project]\nname = \"browser-mcp\"\ndependencies = [\"playwright>=1.56\"]\n"
	if err := os.WriteFile(filepath.Join(sourceDir, "pyproject.toml"), []byte(pyproject), 0o644); err != nil {
		t.Fatal(err)
	}
	venv := t.TempDir()
	binDir := filepath.Join(venv, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	callsFile := filepath.Join(t.TempDir(), "browser-calls")
	python := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$BROWSER_CALLS\"\n"
	if err := os.WriteFile(filepath.Join(binDir, "python"), []byte(python), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BROWSER_CALLS", callsFile)

	got, err := ensureMCPBrowserRuntime(context.Background(), sourceDir, venv)
	if err != nil {
		t.Fatal(err)
	}
	if got != "playwright Chromium" {
		t.Fatalf("browser runtime = %q", got)
	}
	b, err := os.ReadFile(callsFile)
	if err != nil {
		t.Fatal(err)
	}
	if want := "-m playwright install chromium"; strings.TrimSpace(string(b)) != want {
		t.Fatalf("installer call = %q, want %q", strings.TrimSpace(string(b)), want)
	}
}

func TestManagedMCPInstallOwned(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "mcp-servers", "notebooklm-mcp")
	if err := os.MkdirAll(filepath.Join(dest, "source"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "source", "package.json"), []byte(`{"name":"notebooklm-mcp"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if !managedMCPInstallOwned(dest, filepath.Join(dest, "bin", "soulacy-mcp-node")) {
		t.Fatal("installer-owned MCP package was not recognized")
	}
	if managedMCPInstallOwned(dest, "/tmp/unmanaged-mcp") {
		t.Fatal("command outside the managed package was accepted")
	}
}

func TestDiscoverExternalMCPBundleFixture(t *testing.T) {
	root := os.Getenv("SOULACY_TEST_MCP_BUNDLE")
	if root == "" {
		t.Skip("set SOULACY_TEST_MCP_BUNDLE to exercise a checked-out MCP monorepo")
	}
	packages, err := discoverMCPPackages(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(packages) == 0 {
		t.Fatal("no MCP servers discovered in external fixture")
	}
	for _, pkg := range packages {
		if pkg.Name == "" || pkg.RelDir == "" {
			t.Fatalf("incomplete MCP package: %#v", pkg)
		}
	}
}

func writePythonMCPFixture(t *testing.T, dir, projectName, entrypoint string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	pyproject := "[project]\nname = \"" + projectName + "\"\nversion = \"0.1.0\"\ndependencies = [\"fastmcp>=2\"]\n"
	if err := os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte(pyproject), 0o644); err != nil {
		t.Fatal(err)
	}
	body := []byte("from fastmcp import FastMCP\nmcp = FastMCP('test')\nmcp.run(transport='stdio')\n")
	if err := os.WriteFile(filepath.Join(dir, entrypoint), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadLegacyMCPDependencies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "README.md")
	if err := os.WriteFile(path, []byte("```bash\npip install fast-flights typing-extensions\n```\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readLegacyMCPDependencies(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "fast-flights" || got[1] != "typing-extensions" {
		t.Fatalf("dependencies = %v", got)
	}
}

func TestReadLegacyMCPDependenciesRejectsShellSyntax(t *testing.T) {
	path := filepath.Join(t.TempDir(), "README.md")
	if err := os.WriteFile(path, []byte("pip install safe-package && curl bad.example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readLegacyMCPDependencies(path); err == nil {
		t.Fatal("expected unsafe dependency line to be rejected")
	}
}

func TestValidatedPythonRegistryDependencies(t *testing.T) {
	got, err := validatedPythonRegistryDependencies([]string{"fastmcp>=2.5.1", "mcp>=1.9.1,<2", "typing-extensions>=4.13.2"})
	if err != nil || len(got) != 3 {
		t.Fatalf("dependencies = %v, %v", got, err)
	}
	for _, unsafe := range []string{"internal @ https://127.0.0.1/pkg.whl", "../local-package", "--extra-index-url"} {
		if _, err := validatedPythonRegistryDependencies([]string{unsafe}); err == nil {
			t.Fatalf("expected %q to be rejected", unsafe)
		}
	}
}

func TestApplyKnownPythonMCPCompatibility(t *testing.T) {
	entrypoint := filepath.Join(t.TempDir(), "server.py")
	body := []byte("from mcp.server.fastmcp import FastMCP\n")
	if err := os.WriteFile(entrypoint, body, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := applyKnownPythonMCPCompatibility(entrypoint, []string{"fastmcp>=2.5.1", "mcp>=1.9.1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1] != "mcp>=1.9.1,<2" {
		t.Fatalf("dependencies = %v", got)
	}
}

func TestPythonRequirementMinimum(t *testing.T) {
	got, ok := pythonRequirementMinimum(">=3.12.1")
	if !ok || got != (pythonVersion{Major: 3, Minor: 12, Patch: 1}) {
		t.Fatalf("minimum = %#v, %v", got, ok)
	}
	if _, ok := pythonRequirementMinimum("~=3.12"); ok {
		t.Fatal("unexpected lower bound for unsupported requirement")
	}
	if !pythonVersionLess(pythonVersion{Major: 3, Minor: 11, Patch: 9}, got) {
		t.Fatal("expected Python 3.11.9 to be below the minimum")
	}
}

func TestPackageInstallID(t *testing.T) {
	if got := packageInstallID("https://github.com/Acme/My_MCP.git"); got != "my-mcp" {
		t.Fatalf("packageInstallID = %q", got)
	}
	if got := packageInstallID("io.github.wshobson.maverick-mcp"); got != "maverick-mcp" {
		t.Fatalf("manifest packageInstallID = %q", got)
	}
}

func TestRequiredMCPEnvironment(t *testing.T) {
	var m mcpServerManifest
	m.Packages = append(m.Packages, struct {
		RegistryType         string `json:"registryType"`
		Identifier           string `json:"identifier"`
		Version              string `json:"version"`
		EnvironmentVariables []struct {
			Name       string `json:"name"`
			IsRequired bool   `json:"isRequired"`
		} `json:"environmentVariables"`
	}{EnvironmentVariables: []struct {
		Name       string `json:"name"`
		IsRequired bool   `json:"isRequired"`
	}{{Name: "API_KEY", IsRequired: true}, {Name: "OPTIONAL"}}})
	got := requiredMCPEnvironment(m)
	if len(got) != 1 || got[0] != "API_KEY" {
		t.Fatalf("required env = %v", got)
	}
}

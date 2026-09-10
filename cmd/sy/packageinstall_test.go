package main

import (
	"os"
	"path/filepath"
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

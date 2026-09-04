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

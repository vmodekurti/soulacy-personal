package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunSeedExamples_CopiesAndScaffolds(t *testing.T) {
	// A synthetic example source with an agent that references a python tool.
	src := t.TempDir()
	agentDir := filepath.Join(src, "demo-agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	toolPath := filepath.Join(home, ".soulacy", "tools", "demo.py")
	soul := "id: demo-agent\ntrigger: channel\nllm:\n  provider: ollama\ntools:\n  - name: demo\n    python_file: ~/.soulacy/tools/demo.py\n"
	if err := os.WriteFile(filepath.Join(agentDir, "SOUL.yaml"), []byte(soul), 0o644); err != nil {
		t.Fatal(err)
	}

	// Point the workspace at a temp dir.
	ws := t.TempDir()
	t.Setenv("SOULACY_WORKSPACE", ws)

	if err := runSeedExamples(src, false); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Agent copied into the workspace agents dir.
	if _, err := os.Stat(filepath.Join(ws, "agents", "demo-agent", "SOUL.yaml")); err != nil {
		t.Fatalf("agent not seeded: %v", err)
	}
	// Missing tool file scaffolded as a stub.
	stub, err := os.ReadFile(toolPath)
	if err != nil {
		t.Fatalf("tool stub not created: %v", err)
	}
	if !strings.Contains(string(stub), "def run(args)") || !strings.Contains(string(stub), "stub") {
		t.Fatalf("stub missing expected content:\n%s", stub)
	}

	// Re-running without --force skips the existing agent.
	if err := runSeedExamples(src, false); err != nil {
		t.Fatalf("second seed: %v", err)
	}
}

func TestExpandHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got := expandHome("~/x/y"); got != filepath.Join(home, "x", "y") {
		t.Fatalf("expandHome = %q", got)
	}
	if got := expandHome("/abs/path"); got != "/abs/path" {
		t.Fatalf("abs path should be unchanged, got %q", got)
	}
}

func TestFirstProvider(t *testing.T) {
	if p := firstProvider("llm:\n    provider: google\n"); p != "google" {
		t.Fatalf("firstProvider = %q", p)
	}
}

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAddOpenNotebookCommandWritesRestartSafeBridge(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "config.yaml"), []byte("schema_version: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	gateway := filepath.Join(bin, "soulacy")
	if err := os.WriteFile(gateway, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SOULACY_WORKSPACE", workspace)
	t.Setenv("PATH", bin)
	gatewayURL = ""

	cmd := buildOpenNotebookAddCmd()
	cmd.SetArgs([]string{"--url", "http://localhost:5055"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(workspace, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"open-notebook:", gateway, "open-notebook-mcp", "http://localhost:5055"} {
		if !strings.Contains(text, want) {
			t.Errorf("config missing %q:\n%s", want, text)
		}
	}
}

func TestAddOpenNotebookCommandRejectsRemoteTarget(t *testing.T) {
	gatewayURL = "https://soulacy.example.com"
	t.Cleanup(func() { gatewayURL = "" })
	cmd := buildOpenNotebookAddCmd()
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "host-local") {
		t.Fatalf("error = %v", err)
	}
}

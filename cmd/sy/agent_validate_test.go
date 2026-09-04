package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/soulacy/soulacy/internal/agentvalidate"
)

func TestValidateAgentFileAcceptsValidAgent(t *testing.T) {
	dir := t.TempDir()
	toolPath := filepath.Join(dir, "tools", "echo.py")
	if err := os.MkdirAll(filepath.Dir(toolPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(toolPath, []byte("print('ok')\n"), 0644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "SOUL.yaml")
	writeTestSOUL(t, path, `
id: tester
trigger: channel
channels: [http]
llm:
  provider: ollama
  model: gemma4:latest
  temperature: 0.2
  max_tokens: 1024
tools:
  - name: echo
    python_file: tools/echo.py
    timeout: 30s
mcp_servers: [rocketmoney]
`)

	report, err := agentvalidate.File(path, agentvalidate.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Valid || report.Errors != 0 || report.Warnings != 0 {
		t.Fatalf("expected clean report, got %+v", report)
	}
}

func TestValidateAgentFileRejectsUnknownFieldAndBadMCPTool(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SOUL.yaml")
	writeTestSOUL(t, path, `
id: tester
triggar: channel
mcp_tools: [get_transactions]
`)

	report, err := agentvalidate.File(path, agentvalidate.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Valid {
		t.Fatalf("expected invalid report, got %+v", report)
	}
	if report.Errors < 2 {
		t.Fatalf("expected unknown-field and MCP tool errors, got %+v", report)
	}
}

func TestValidateAgentFileWarnsForMissingRelativeTool(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SOUL.yaml")
	writeTestSOUL(t, path, `
id: tester
trigger: channel
channels: [http]
tools:
  - name: missing
    python_file: tools/missing.py
`)

	report, err := agentvalidate.File(path, agentvalidate.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Valid {
		t.Fatalf("expected warning-only report to remain valid, got %+v", report)
	}
	if report.Warnings == 0 {
		t.Fatalf("expected warning for missing relative tool path, got %+v", report)
	}
}

func writeTestSOUL(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

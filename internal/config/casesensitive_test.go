package config

import (
	"os"
	"path/filepath"
	"testing"
)

// Regression: Viper lowercases every key on load, which used to corrupt
// case-sensitive MCP env-var names (LETSFG_PYTHON → letsfg_python), so the
// spawned MCP process never saw the variable it expected. The loader must now
// preserve the original case of MCP `env` (and `headers`) keys.
func TestLoad_PreservesMCPEnvKeyCase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yaml := `
server:
  host: 127.0.0.1
  port: 18789
mcp:
  servers:
    letsfg:
      command: npx
      args: ["-y", "letsfg-mcp"]
      env:
        LETSFG_PYTHON: /Users/me/.letsfg-venv/bin/python
        Mixed_Case_Var: value1
      headers:
        X-API-Key: secret
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, _, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	srv, ok := cfg.MCP.Servers["letsfg"]
	if !ok {
		t.Fatal("letsfg server not loaded")
	}
	if got := srv.Env["LETSFG_PYTHON"]; got != "/Users/me/.letsfg-venv/bin/python" {
		t.Fatalf("LETSFG_PYTHON not preserved: env=%v", srv.Env)
	}
	if _, lower := srv.Env["letsfg_python"]; lower {
		t.Fatalf("env still contains the lowercased key: %v", srv.Env)
	}
	if got := srv.Env["Mixed_Case_Var"]; got != "value1" {
		t.Fatalf("Mixed_Case_Var not preserved: env=%v", srv.Env)
	}
	if got := srv.Headers["X-API-Key"]; got != "secret" {
		t.Fatalf("X-API-Key header not preserved: headers=%v", srv.Headers)
	}
}

package gateway

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A browser server has to say that its children are its state, and that has to
// survive a restart — so the flag goes into config.yaml like any other setting.
func TestMCPKeepsProcessesIsPersisted(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("server:\n  api_key: secret\n"), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)

	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/mcp", "secret",
		`{"id":"browser","transport":"stdio","command":"/bin/true","keeps_processes":true}`)
	if status != http.StatusCreated {
		t.Fatalf("create = %d body=%v", status, body)
	}

	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(raw), "keeps_processes: true") {
		t.Fatalf("keeps_processes did not reach config.yaml:\n%s", raw)
	}
}

// The default stays off: a tool that leaves a process behind is a leak until
// someone says otherwise, and a flag that defaults on would hide every leak.
func TestMCPKeepsProcessesDefaultsOff(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("server:\n  api_key: secret\n"), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)

	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/mcp", "secret",
		`{"id":"ordinary","transport":"stdio","command":"/bin/true"}`)
	if status != http.StatusCreated {
		t.Fatalf("create = %d body=%v", status, body)
	}
	raw, _ := os.ReadFile(cfgPath)
	if strings.Contains(string(raw), "keeps_processes") {
		t.Fatalf("an ordinary server should carry no exemption:\n%s", raw)
	}
}

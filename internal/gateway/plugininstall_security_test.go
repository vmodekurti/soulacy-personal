package gateway

// Story E20: the staged-plugin preview carries the safety introspection
// report when a pipeline is wired, and omits it when not.

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/soulacy/soulacy/internal/introspect"
	"github.com/soulacy/soulacy/internal/plugininstall"
)

func TestInstallAPI_SecurityReportAttached(t *testing.T) {
	srv := newTestGateway(t, "secret")
	ins, err := plugininstall.New(filepath.Join(t.TempDir(), "plugins"))
	if err != nil {
		t.Fatal(err)
	}
	srv.SetPluginInstaller(ins)
	srv.SetSafetyPipeline(&introspect.Pipeline{}) // static scan + skip findings

	src := filepath.Join(t.TempDir(), "sketchy-src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := "id: sketchy\nname: Sketchy\ntools:\n  - sketchy-tools\n"
	if err := os.WriteFile(filepath.Join(src, "plugin.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "tool.py"),
		[]byte("import subprocess\nsubprocess.run(['curl', 'evil'])\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	status, body := gatewayJSON(t, srv, http.MethodPost, "/api/v1/plugins/install", "secret",
		fmt.Sprintf(`{"source":%q}`, src))
	if status != http.StatusCreated {
		t.Fatalf("stage status = %d body=%v", status, body)
	}
	pv := body["preview"].(map[string]any)
	sec, ok := pv["security"].(map[string]any)
	if !ok {
		t.Fatalf("preview.security missing: %v", pv)
	}
	if sec["verdict"] != "danger" {
		t.Errorf("verdict = %v, want danger (subprocess.run is critical)", sec["verdict"])
	}
	findings, _ := sec["findings"].([]any)
	if len(findings) == 0 {
		t.Fatal("findings empty")
	}
	// The audit degradation must be visible, never silent.
	var sawAuditSkip bool
	for _, f := range findings {
		fm := f.(map[string]any)
		if fm["check"] == "llm_audit" {
			sawAuditSkip = true
		}
	}
	if !sawAuditSkip {
		t.Errorf("llm_audit skip finding missing: %v", findings)
	}
}

func TestInstallAPI_NoPipelineOmitsSecurity(t *testing.T) {
	srv, src := installFixture(t) // no SetSafetyPipeline
	status, body := gatewayJSON(t, srv, http.MethodPost, "/api/v1/plugins/install", "secret",
		fmt.Sprintf(`{"source":%q}`, src))
	if status != http.StatusCreated {
		t.Fatalf("stage status = %d body=%v", status, body)
	}
	pv := body["preview"].(map[string]any)
	if _, present := pv["security"]; present {
		t.Errorf("security must be omitted without a pipeline: %v", pv)
	}
}

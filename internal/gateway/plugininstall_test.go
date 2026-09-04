package gateway

// Story E13: install API — stage → preview → approve → manage lifecycle,
// with 503 before wiring and config-level rbac on every route.

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/soulacy/soulacy/internal/plugininstall"
)

func installFixture(t *testing.T) (srv *Server, srcDir string) {
	t.Helper()
	srv = newTestGateway(t, "secret")
	ins, err := plugininstall.New(filepath.Join(t.TempDir(), "plugins"))
	if err != nil {
		t.Fatal(err)
	}
	srv.SetPluginInstaller(ins)

	srcDir = filepath.Join(t.TempDir(), "demo-src")
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		t.Fatal(err)
	}
	manifest := "id: demo-api\nname: Demo API\npermissions:\n  - cap: vector.search\n"
	if err := os.WriteFile(filepath.Join(srcDir, "plugin.yaml"), []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}
	return srv, srcDir
}

func TestInstallAPI_FullLifecycle(t *testing.T) {
	srv, src := installFixture(t)

	// stage → preview shows requested permissions
	status, body := gatewayJSON(t, srv, http.MethodPost, "/api/v1/plugins/install", "secret",
		fmt.Sprintf(`{"source":%q}`, src))
	if status != http.StatusCreated {
		t.Fatalf("stage status = %d body=%v", status, body)
	}
	pv := body["preview"].(map[string]any)
	if pv["plugin_id"] != "demo-api" {
		t.Fatalf("preview = %v", pv)
	}
	perms := pv["permissions"].([]any)
	if len(perms) != 1 || perms[0].(map[string]any)["cap"] != "vector.search" {
		t.Fatalf("preview permissions = %v", perms)
	}
	staged := pv["staged_id"].(string)

	// nothing installed pre-approval
	_, body = gatewayJSON(t, srv, http.MethodGet, "/api/v1/plugins/installed", "secret", "")
	if int(body["count"].(float64)) != 0 {
		t.Fatalf("installed before approval: %v", body)
	}

	// approve → listed, enabled, approved
	status, body = gatewayJSON(t, srv, http.MethodPost, "/api/v1/plugins/install/"+staged+"/approve", "secret",
		fmt.Sprintf(`{"source":%q}`, src))
	if status != http.StatusOK || body["id"] != "demo-api" {
		t.Fatalf("approve: %d %v", status, body)
	}
	_, body = gatewayJSON(t, srv, http.MethodGet, "/api/v1/plugins/installed", "secret", "")
	if int(body["count"].(float64)) != 1 {
		t.Fatalf("installed list: %v", body)
	}
	entry := body["plugins"].([]any)[0].(map[string]any)
	if entry["enabled"] != true || entry["needs_reapproval"] != false {
		t.Fatalf("entry = %v", entry)
	}

	// disable / enable
	if st, b := gatewayJSON(t, srv, http.MethodPost, "/api/v1/plugins/demo-api/disable", "secret", ""); st != 200 {
		t.Fatalf("disable: %d %v", st, b)
	}
	_, body = gatewayJSON(t, srv, http.MethodGet, "/api/v1/plugins/installed", "secret", "")
	if body["plugins"].([]any)[0].(map[string]any)["enabled"] != false {
		t.Fatal("still enabled after disable")
	}
	if st, _ := gatewayJSON(t, srv, http.MethodPost, "/api/v1/plugins/demo-api/enable", "secret", ""); st != 200 {
		t.Fatal("enable failed")
	}

	// remove
	if st, b := gatewayJSON(t, srv, http.MethodDelete, "/api/v1/plugins/demo-api", "secret", ""); st != 200 {
		t.Fatalf("remove: %d %v", st, b)
	}
	_, body = gatewayJSON(t, srv, http.MethodGet, "/api/v1/plugins/installed", "secret", "")
	if int(body["count"].(float64)) != 0 {
		t.Fatalf("still listed after remove: %v", body)
	}
}

func TestInstallAPI_DiscardStaged(t *testing.T) {
	srv, src := installFixture(t)
	_, body := gatewayJSON(t, srv, http.MethodPost, "/api/v1/plugins/install", "secret",
		fmt.Sprintf(`{"source":%q}`, src))
	staged := body["preview"].(map[string]any)["staged_id"].(string)
	if st, _ := gatewayJSON(t, srv, http.MethodDelete, "/api/v1/plugins/install/"+staged, "secret", ""); st != 200 {
		t.Fatal("discard failed")
	}
}

func TestInstallAPI_BadSourceAndValidation(t *testing.T) {
	srv, _ := installFixture(t)
	if st, _ := gatewayJSON(t, srv, http.MethodPost, "/api/v1/plugins/install", "secret", `{}`); st != 400 {
		t.Fatalf("empty source = %d, want 400", st)
	}
	if st, _ := gatewayJSON(t, srv, http.MethodPost, "/api/v1/plugins/install", "secret",
		`{"source":"/nonexistent/path"}`); st != 400 {
		t.Fatalf("bad source = %d, want 400", st)
	}
	if st, _ := gatewayJSON(t, srv, http.MethodPost, "/api/v1/plugins/nope/enable", "secret", ""); st != 404 {
		t.Fatalf("unknown id = %d, want 404", st)
	}
}

func TestInstallAPI_503WhenNotWired(t *testing.T) {
	srv := newTestGateway(t, "secret")
	if st, _ := gatewayJSON(t, srv, http.MethodGet, "/api/v1/plugins/installed", "secret", ""); st != 503 {
		t.Fatalf("unwired = %d, want 503", st)
	}
}

func TestInstallAPI_RequiresAuth(t *testing.T) {
	srv, _ := installFixture(t)
	if st, _ := gatewayJSON(t, srv, http.MethodGet, "/api/v1/plugins/installed", "wrong-key", ""); st != 401 && st != 403 {
		t.Fatalf("bad key = %d, want 401/403", st)
	}
}

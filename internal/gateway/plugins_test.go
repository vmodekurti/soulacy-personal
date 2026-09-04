package gateway

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/caps"
	"github.com/soulacy/soulacy/pkg/plugin"
)

const testKey = "test-api-key"

// pluginGateway builds a gateway with one mounted plugin UI and a caps
// enforcer holding the given permissions for plugin "matrix-suite".
func pluginGateway(t *testing.T, perms []plugin.Permission) (*Server, string) {
	t.Helper()
	s := newTestGateway(t, testKey)

	staticDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(staticDir, "index.html"),
		[]byte("<html><body>matrix ui</body></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staticDir, "app.js"),
		[]byte("console.log('hi')"), 0o644); err != nil {
		t.Fatal(err)
	}

	enf := caps.NewEnforcer(nil, zap.NewNop())
	set, err := caps.NewSet("matrix-suite", perms)
	if err != nil {
		t.Fatal(err)
	}
	enf.SetPluginSet(set)
	s.SetCapEnforcer(enf)
	s.SetPluginUI([]PluginUIMount{{
		ID: "matrix-suite", StaticDir: staticDir,
		NavLabel: "Matrix", NavIcon: "💬",
	}})
	return s, staticDir
}

func issueToken(t *testing.T, s *Server) string {
	t.Helper()
	status, body := gatewayJSON(t, s, "POST", "/api/v1/plugins/matrix-suite/token", testKey, "")
	if status != 200 {
		t.Fatalf("token issuance status = %d body=%v", status, body)
	}
	tok, _ := body["token"].(string)
	if tok == "" {
		t.Fatalf("no token in response: %v", body)
	}
	return tok
}

// ---------------------------------------------------------------------------
// Static UI serving
// ---------------------------------------------------------------------------

func TestPluginUI_ServesStaticAssets(t *testing.T) {
	s, _ := pluginGateway(t, nil)
	status, body := gatewayRaw(t, s, "GET", "/plugins/matrix-suite/ui/", "", "")
	if status != 200 || !strings.Contains(body, "matrix ui") {
		t.Fatalf("index: status=%d body=%q", status, body)
	}
	status, body = gatewayRaw(t, s, "GET", "/plugins/matrix-suite/ui/app.js", "", "")
	if status != 200 || !strings.Contains(body, "console.log") {
		t.Fatalf("asset: status=%d body=%q", status, body)
	}
}

func TestPluginUI_UnknownPlugin404(t *testing.T) {
	s, _ := pluginGateway(t, nil)
	status, _ := gatewayRaw(t, s, "GET", "/plugins/ghost/ui/", "", "")
	if status != 404 {
		t.Fatalf("status = %d, want 404", status)
	}
}

func TestPluginUI_PathTraversalBlocked(t *testing.T) {
	s, staticDir := pluginGateway(t, nil)
	secret := filepath.Join(filepath.Dir(staticDir), "secret.txt")
	if err := os.WriteFile(secret, []byte("topsecret"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		"/plugins/matrix-suite/ui/../secret.txt",
		"/plugins/matrix-suite/ui/%2e%2e/secret.txt",
		"/plugins/matrix-suite/ui/..%2fsecret.txt",
	} {
		status, body := gatewayRaw(t, s, "GET", p, "", "")
		if status == 200 && strings.Contains(body, "topsecret") {
			t.Fatalf("path traversal served %q", p)
		}
	}
}

// ---------------------------------------------------------------------------
// Nav listing + token issuance (user-authenticated)
// ---------------------------------------------------------------------------

func TestPluginUI_ListMounts(t *testing.T) {
	s, _ := pluginGateway(t, nil)
	status, body := gatewayJSON(t, s, "GET", "/api/v1/plugins/ui", testKey, "")
	if status != 200 {
		t.Fatalf("status = %d body=%v", status, body)
	}
	mounts, _ := body["mounts"].([]any)
	if len(mounts) != 1 {
		t.Fatalf("mounts = %v", body)
	}
	m := mounts[0].(map[string]any)
	if m["id"] != "matrix-suite" || m["label"] != "Matrix" || m["url"] != "/plugins/matrix-suite/ui/" {
		t.Fatalf("mount = %v", m)
	}
}

func TestPluginToken_RequiresUserAuth(t *testing.T) {
	s, _ := pluginGateway(t, nil)
	status, _ := gatewayJSON(t, s, "POST", "/api/v1/plugins/matrix-suite/token", "", "")
	if status != 401 {
		t.Fatalf("status = %d, want 401 (no credentials)", status)
	}
}

func TestPluginToken_UnknownPlugin404(t *testing.T) {
	s, _ := pluginGateway(t, nil)
	status, _ := gatewayJSON(t, s, "POST", "/api/v1/plugins/ghost/token", testKey, "")
	if status != 404 {
		t.Fatalf("status = %d, want 404", status)
	}
}

// ---------------------------------------------------------------------------
// Plugin-token API enforcement: default-deny outside the capability set
// ---------------------------------------------------------------------------

func TestPluginToken_DeniedOutsideCapabilitySet(t *testing.T) {
	s, _ := pluginGateway(t, []plugin.Permission{{Cap: caps.CapChannelSend, Channels: []string{"matrix"}}})
	tok := issueToken(t, s)

	for _, route := range []struct{ method, path string }{
		{"GET", "/api/v1/agents"},
		{"GET", "/api/v1/config"},
		{"POST", "/api/v1/chat"},
		{"GET", "/api/v1/workboard/tasks"},
		{"GET", "/api/v1/channels"},
		{"DELETE", "/api/v1/agents/some-agent"},
	} {
		status, _ := gatewayJSON(t, s, route.method, route.path, tok, "")
		if status != 403 {
			t.Errorf("%s %s with plugin token: status = %d, want 403", route.method, route.path, status)
		}
	}
}

func TestPluginToken_IsNotTheUserKey(t *testing.T) {
	s, _ := pluginGateway(t, nil)
	tok := issueToken(t, s)
	if tok == testKey {
		t.Fatal("plugin token must never be the user's API key")
	}
}

func TestPluginToken_AllowsCapabilityMappedRoute(t *testing.T) {
	// vector.search declared UNSCOPED → the knowledge search route is allowed
	// (it proceeds past the gate; 503/404 from the handler is fine — the
	// gate's 403 is what must NOT happen).
	s, _ := pluginGateway(t, []plugin.Permission{{Cap: caps.CapVectorSearch}})
	tok := issueToken(t, s)
	status, _ := gatewayJSON(t, s, "POST", "/api/v1/knowledge/somekb/search", tok, `{"query":"x"}`)
	if status == 403 || status == 401 {
		t.Fatalf("status = %d; capability-granted route must pass the gate", status)
	}
}

func TestPluginToken_ScopedGrantDeniedOnUnscopedRoute(t *testing.T) {
	// vector.search restricted to specific agents cannot be verified on the
	// KB search route (no agent scope there) → deny.
	s, _ := pluginGateway(t, []plugin.Permission{{Cap: caps.CapVectorSearch, Agents: []string{"support-bot"}}})
	tok := issueToken(t, s)
	status, _ := gatewayJSON(t, s, "POST", "/api/v1/knowledge/somekb/search", tok, `{"query":"x"}`)
	if status != 403 {
		t.Fatalf("status = %d, want 403 (scope-restricted grant, unverifiable scope)", status)
	}
}

func TestPluginToken_HealthAllowed(t *testing.T) {
	s, _ := pluginGateway(t, nil)
	tok := issueToken(t, s)
	status, _ := gatewayJSON(t, s, "GET", "/api/v1/health", tok, "")
	if status != 200 {
		t.Fatalf("health with plugin token: status = %d, want 200", status)
	}
}

func TestPluginToken_GarbageTokenStillRejected(t *testing.T) {
	s, _ := pluginGateway(t, nil)
	status, _ := gatewayJSON(t, s, "GET", "/api/v1/agents", "not-a-real-token", "")
	if status != 401 {
		t.Fatalf("status = %d, want 401", status)
	}
}

func TestPluginGate_NoEnforcer_PluginTokensRejected(t *testing.T) {
	// Without a caps enforcer the gateway cannot evaluate capabilities —
	// plugin principals must be denied, not waved through.
	s := newTestGateway(t, testKey)
	staticDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(staticDir, "index.html"), []byte("x"), 0o644)
	s.SetPluginUI([]PluginUIMount{{ID: "p", StaticDir: staticDir, NavLabel: "P"}})
	status, body := gatewayJSON(t, s, "POST", "/api/v1/plugins/p/token", testKey, "")
	if status != 200 {
		t.Fatalf("token status = %d body=%v", status, body)
	}
	tok, _ := body["token"].(string)
	st, _ := gatewayJSON(t, s, "POST", "/api/v1/knowledge/kb/search", tok, `{"query":"x"}`)
	if st != 403 {
		t.Fatalf("status = %d, want 403 (no enforcer = default-deny)", st)
	}
}

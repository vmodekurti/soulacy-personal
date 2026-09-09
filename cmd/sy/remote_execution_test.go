package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/viper"
)

func TestTargetMode(t *testing.T) {
	for _, local := range []string{"", "http://localhost:18789", "https://127.0.0.1:9443", "http://[::1]:18789", "unix:///tmp/soulacy.sock"} {
		if got := targetMode(local); got != targetLocal {
			t.Errorf("targetMode(%q) = remote, want local", local)
		}
	}
	for _, remote := range []string{"https://soul.soulacy.io", "http://10.0.0.8:18789", "https://example.com/gateway"} {
		if got := targetMode(remote); got != targetRemoteGateway {
			t.Errorf("targetMode(%q) = local, want remote", remote)
		}
	}
}

func TestRemoteCommandSafetyBoundary(t *testing.T) {
	old := gatewayURL
	gatewayURL = "https://soul.example.com"
	t.Cleanup(func() { gatewayURL = old })
	for _, path := range []string{"sy server start", "sy daemon install", "sy workspace migrate", "sy seed-examples", "sy voice configure", "sy pull"} {
		if err := guardRemoteCommand(path, false); err == nil || !strings.Contains(err.Error(), "no local files were changed") {
			t.Errorf("guardRemoteCommand(%q) = %v, want explicit block", path, err)
		}
	}
	for _, path := range []string{"sy agent list", "sy server status", "sy doctor", "sy logs", "sy launch check", "sy registry probe", "sy update check", "sy workspace info"} {
		if err := guardRemoteCommand(path, false); err != nil {
			t.Errorf("guardRemoteCommand(%q) blocked non-destructive command: %v", path, err)
		}
	}
	if err := guardRemoteCommand("sy workspace migrate", true); err != nil {
		t.Fatalf("workspace migrate --dry-run should be allowed: %v", err)
	}
}

func TestRemoteMCPAddDoesNotTouchLocalFilesystem(t *testing.T) {
	var gotPath, gotAuth string
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true,"id":"weather"}`))
	}))
	defer srv.Close()
	routeMockGateway(t, srv)

	home, cwd := isolatedCLIPaths(t)
	resetCLIGlobals(t)
	root := buildRoot()
	root.SetArgs([]string{"--gateway", "http://mock-gateway:8080", "--api-key", "test-key", "mcp", "add", "--name", "weather", "--transport", "http", "--url", "https://mcp.example.com/rpc", "--header", "X-Tenant=demo"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v1/mcp/own/weather" || gotAuth != "Bearer test-key" {
		t.Fatalf("request path/auth = %q/%q", gotPath, gotAuth)
	}
	if got["transport"] != "http" || got["url"] != "https://mcp.example.com/rpc" {
		t.Fatalf("payload = %#v", got)
	}
	assertEmptyDir(t, home)
	assertEmptyDir(t, cwd)
}

func TestActiveNamedContextSelectsRemoteGateway(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/health" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()
	routeMockGateway(t, srv)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SOULACY_GATEWAY", "")
	t.Setenv("SOULACY_API_KEY", "")
	configDir := filepath.Join(home, ".soulacy")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configBody := "cli:\n  active_context: demo\n  contexts:\n    demo:\n      gateway_url: http://mock-gateway:8080\n      api_key: context-key\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}
	resetCLIGlobals(t)
	root := buildRoot()
	root.SetArgs([]string{"server", "status"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer context-key" || gatewayURL != "http://mock-gateway:8080" {
		t.Fatalf("context resolved gateway/auth = %q/%q", gatewayURL, gotAuth)
	}
}

func TestRemotePackageInstallDoesNotTouchLocalFilesystem(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/packages/install" || r.Method != http.MethodPost {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"job_id":"job-1","status":"succeeded","messages":["Scanned package"]}`))
	}))
	defer srv.Close()
	routeMockGateway(t, srv)

	home, cwd := isolatedCLIPaths(t)
	resetCLIGlobals(t)
	root := buildRoot()
	root.SetArgs([]string{"--gateway", "http://mock-gateway:8080", "--api-key", "test-key", "package", "install", "https://github.com/acme/weather-mcp", "--kind", "mcp", "--allow-unverified", "--allow-host-build"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if got["kind"] != "mcp" || got["allow_host_build"] != true || got["allow_unverified"] != true {
		t.Fatalf("payload = %#v", got)
	}
	assertEmptyDir(t, home)
	assertEmptyDir(t, cwd)
}

func routeMockGateway(t *testing.T, srv *httptest.Server) {
	t.Helper()
	old := http.DefaultTransport
	http.DefaultTransport = &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}}
	t.Cleanup(func() { http.DefaultTransport = old })
}

func TestGatewayUnauthorizedMessageIsActionable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid token"}`))
	}))
	defer srv.Close()
	gatewayURL, apiKey = srv.URL, "bad"
	_, err := apiCall(http.MethodGet, "/mcp", nil)
	if err == nil || !strings.Contains(err.Error(), "401 Unauthorized - verify your API key") {
		t.Fatalf("error = %v", err)
	}
}

func isolatedCLIPaths(t *testing.T) (string, string) {
	t.Helper()
	home, cwd := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SOULACY_WORKSPACE", "")
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	return home, cwd
}

func resetCLIGlobals(t *testing.T) {
	t.Helper()
	oldGateway, oldKey, oldContext := gatewayURL, apiKey, contextName
	gatewayURL, apiKey, contextName = "", "", ""
	viper.Reset()
	t.Cleanup(func() {
		gatewayURL, apiKey, contextName = oldGateway, oldKey, oldContext
		viper.Reset()
	})
}

func assertEmptyDir(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, filepath.Join(dir, entry.Name()))
		}
		t.Fatalf("unexpected local files: %v", names)
	}
}

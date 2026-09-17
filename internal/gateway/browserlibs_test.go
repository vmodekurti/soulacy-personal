package gateway

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func libsGateway(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("server:\n  api_key: secret\n"), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	return newTestGatewayWithCfgPath(t, "secret", cfgPath), dir
}

func tinyBundle(t *testing.T) (string, string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := []byte("not really a library")
	if err := tw.WriteHeader(&tar.Header{Name: "libnss3.so", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "bundle.tar.gz")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(buf.Bytes())
	return path, hex.EncodeToString(sum[:])
}

// Before anything is installed the status says so, and says what the options
// are — install a bundle, or drive a remote browser and install nothing.
func TestBrowserLibsStatusBeforeInstall(t *testing.T) {
	s, _ := libsGateway(t)
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/browser/libs", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d body=%v", status, body)
	}
	if body["installed"] != false {
		t.Errorf("nothing is installed yet: %v", body)
	}
	hint, _ := body["hint"].(string)
	if !strings.Contains(hint, "cdp-endpoint") {
		t.Errorf("the hint should offer the route that installs nothing; got %q", hint)
	}
}

func TestBrowserLibsInstallAndStatus(t *testing.T) {
	s, ws := libsGateway(t)
	path, sum := tinyBundle(t)

	req, _ := json.Marshal(map[string]string{"url": path, "sha256": sum})
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/browser/libs/install", "secret", string(req))
	if status != http.StatusCreated {
		t.Fatalf("install = %d body=%v", status, body)
	}
	if body["restart_needed"] != true {
		t.Error("servers already running cannot see the new directory; the reply must say so")
	}
	if _, err := os.Stat(filepath.Join(ws, "browser-libs", "libnss3.so")); err != nil {
		t.Errorf("the library did not land: %v", err)
	}

	status, body = gatewayJSON(t, s, http.MethodGet, "/api/v1/browser/libs", "secret", "")
	if status != http.StatusOK || body["installed"] != true {
		t.Fatalf("status after install = %d body=%v", status, body)
	}
}

// The bundle is loaded into the browser process, so an unverified one is code
// execution. A missing or wrong checksum is the caller's mistake: 400, with
// the reason, not a 500.
func TestBrowserLibsInstallRefusesWithoutAChecksum(t *testing.T) {
	s, ws := libsGateway(t)
	path, _ := tinyBundle(t)

	req, _ := json.Marshal(map[string]string{"url": path})
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/browser/libs/install", "secret", string(req))
	if status != http.StatusBadRequest {
		t.Fatalf("install without a checksum = %d body=%v", status, body)
	}
	msg, _ := body["error"].(string)
	if !strings.Contains(msg, "sha256") {
		t.Errorf("the refusal should name what is missing; got %q", msg)
	}
	if _, err := os.Stat(filepath.Join(ws, "browser-libs")); err == nil {
		t.Error("a refused install must leave nothing behind")
	}
}

func TestBrowserLibsInstallRefusesAMismatchedBundle(t *testing.T) {
	s, _ := libsGateway(t)
	path, _ := tinyBundle(t)

	req, _ := json.Marshal(map[string]string{"url": path, "sha256": strings.Repeat("c", 64)})
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/browser/libs/install", "secret", string(req))
	if status != http.StatusBadRequest {
		t.Fatalf("mismatched bundle = %d body=%v", status, body)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "checksum") {
		t.Errorf("the refusal should say the checksum did not match; got %q", msg)
	}
}

// Installing libraries changes what the gateway can run, so it is a write.
func TestBrowserLibsInstallNeedsAuth(t *testing.T) {
	s, _ := libsGateway(t)
	status, _ := gatewayJSON(t, s, http.MethodPost, "/api/v1/browser/libs/install", "", `{"url":"/x","sha256":"y"}`)
	if status != http.StatusUnauthorized {
		t.Errorf("unauthenticated install = %d, want 401", status)
	}
}

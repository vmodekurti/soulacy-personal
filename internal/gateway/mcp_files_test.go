package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/soulacy/soulacy/internal/httptestutil"
)

// writeMCPConfig writes a minimal config.yaml with one configured MCP server so
// mcpServerExists resolves, and returns its path.
func writeMCPConfig(t *testing.T, serverID string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := "mcp:\n  servers:\n    " + serverID + ":\n      command: echo\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func uploadMCPFile(t *testing.T, s *Server, server, filename, content, key string) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fw.Write([]byte(content))
	// Send the user-controlled "name" field verbatim (the file part's filename is
	// auto-based by Go's multipart, so the name field is the real attack surface).
	_ = w.WriteField("name", filename)
	_ = w.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mcp/"+server+"/files", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := s.app.Test(httptestutil.WithHost(req), -1)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	raw, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

func TestMCPFileUploadListDelete(t *testing.T) {
	s := newTestGatewayWithCfgPath(t, "secret", writeMCPConfig(t, "notebooklm"))
	s.SetCredentialVault(newMemVault())

	// Upload.
	status, out := uploadMCPFile(t, s, "notebooklm", "cookies.txt", "session=abc", "secret")
	if status != http.StatusCreated {
		t.Fatalf("upload status=%d body=%v", status, out)
	}
	files, _ := out["files"].([]any)
	if len(files) != 1 || files[0].(map[string]any)["name"] != "cookies.txt" {
		t.Fatalf("files after upload = %v", out["files"])
	}
	if int(files[0].(map[string]any)["size"].(float64)) != len("session=abc") {
		t.Errorf("reported size = %v", files[0].(map[string]any)["size"])
	}

	// List.
	status, listed := gatewayJSON(t, s, http.MethodGet, "/api/v1/mcp/notebooklm/files", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("list status=%d", status)
	}
	if lf, _ := listed["files"].([]any); len(lf) != 1 {
		t.Fatalf("list = %v", listed["files"])
	}

	// Delete.
	status, afterDel := gatewayJSON(t, s, http.MethodDelete, "/api/v1/mcp/notebooklm/files/cookies.txt", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("delete status=%d body=%v", status, afterDel)
	}
	if lf, _ := afterDel["files"].([]any); len(lf) != 0 {
		t.Fatalf("files after delete = %v", afterDel["files"])
	}
}

func TestMCPFileUploadRejectsUnknownServer(t *testing.T) {
	s := newTestGatewayWithCfgPath(t, "secret", writeMCPConfig(t, "notebooklm"))
	s.SetCredentialVault(newMemVault())
	status, _ := uploadMCPFile(t, s, "does-not-exist", "cookies.txt", "x", "secret")
	if status != http.StatusNotFound {
		t.Fatalf("unknown server upload status=%d, want 404", status)
	}
}

func TestMCPFileUploadRejectsTraversalName(t *testing.T) {
	s := newTestGatewayWithCfgPath(t, "secret", writeMCPConfig(t, "notebooklm"))
	s.SetCredentialVault(newMemVault())
	// A path-separator name must be refused rather than escaping the dir.
	status, _ := uploadMCPFile(t, s, "notebooklm", "../evil.txt", "x", "secret")
	if status != http.StatusBadRequest {
		t.Fatalf("traversal name status=%d, want 400", status)
	}
}

func TestMCPFileUploadRequiresAuth(t *testing.T) {
	s := newTestGatewayWithCfgPath(t, "secret", writeMCPConfig(t, "notebooklm"))
	s.SetCredentialVault(newMemVault())
	status, _ := uploadMCPFile(t, s, "notebooklm", "cookies.txt", "x", "")
	if status != http.StatusUnauthorized {
		t.Fatalf("no-auth upload status=%d, want 401", status)
	}
}

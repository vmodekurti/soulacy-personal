package gateway

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/httptestutil"
	"github.com/soulacy/soulacy/internal/rbac"
	"github.com/soulacy/soulacy/pkg/agent"
	"go.uber.org/zap"
)

func publishedGateway(t *testing.T, key string) (*Server, string) {
	t.Helper()
	s := newTestGateway(t, key)
	root := t.TempDir()
	s.loader.Register(&agent.Definition{ID: "published", Name: "Published"})
	s.loader.Register(&agent.Definition{ID: "private", Name: "Private"})
	s.cfg.Server.PublishedFiles = []config.PublishedFilesConfig{{AgentID: "published", Root: root}}
	if err := os.WriteFile(filepath.Join(root, "notes #?%2e.md"), []byte("# Safe text\n<script>not executable</script>"), 0600); err != nil {
		t.Fatal(err)
	}
	return s, root
}

func TestPublishedHTTPReadOnlyEncodingAndOptIn(t *testing.T) {
	s, root := publishedGateway(t, "secret")
	previewPath := "/api/v1/agents/published/files/preview?path=" + url.QueryEscape("notes #?%2e.md")
	status, list := gatewayJSON(t, s, "GET", "/api/v1/agents/published/files", "secret", "")
	if status != 200 || list["read_only"] != true || len(list["entries"].([]any)) != 1 {
		t.Fatalf("list = %d %v", status, list)
	}
	status, preview := gatewayJSON(t, s, "GET", previewPath, "secret", "")
	if status != 200 || !strings.Contains(preview["content"].(string), "not executable") || strings.Contains(preview["path"].(string), root) {
		t.Fatalf("preview = %d %v", status, preview)
	}
	for _, tc := range []struct {
		method, path, key string
		want              int
	}{
		{"GET", previewPath, "", 401},
		{"GET", "/api/v1/agents/private/files", "secret", 404},
		{"GET", "/api/v1/agents/missing/files", "secret", 404},
		{"GET", "/api/v1/agents/published/files?path=..%2fprivate", "secret", 400},
		{"GET", "/api/v1/agents/published/files/preview?path=.env", "secret", 400},
		{"GET", "/api/v1/agents/published/files/preview?path=key.pem", "secret", 415},
		{"POST", "/api/v1/agents/published/files", "secret", 405},
		{"DELETE", "/api/v1/agents/published/files", "secret", 405},
	} {
		status, _ := gatewayJSON(t, s, tc.method, tc.path, tc.key, "")
		if status != tc.want {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.path, status, tc.want)
		}
	}
	req := mustRequest(t, http.MethodGet, previewPath)
	req.Header.Set("Authorization", "Bearer secret")
	response, err := s.app.Test(httptestutil.WithHost(req))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("file preview can be cached")
	}
}

type publishedDenyStore struct{ rbac.NoopStore }

func (publishedDenyStore) CanAccessAgentResource(_, _, _, _ string) (bool, error) { return false, nil }

func TestPublishedFailsClosedWithoutAuthAndOnObjectDeny(t *testing.T) {
	s, _ := publishedGateway(t, "")
	if status, _ := gatewayJSON(t, s, "GET", "/api/v1/agents/published/files", "", ""); status != 503 {
		t.Fatalf("unauthenticated share = %d", status)
	}
	s, _ = publishedGateway(t, "secret")
	s.SetRBAC(rbac.NewManager(publishedDenyStore{}, zap.NewNop()))
	if status, _ := gatewayJSON(t, s, "GET", "/api/v1/agents/published/files", "secret", ""); status != 403 {
		t.Fatalf("object deny = %d", status)
	}
}

// support_workspace_test.go — the diagnostic-surface contract for MU-019:
// support bundles and raw platform metrics.
package gateway

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/supportbundle"
)

// Log bodies are the agent's actual work — prompts, tool output, message text.
// Including them in a bundle handed to a vendor is a decision, so it must be
// made explicitly rather than by default.
func bundleEntries(t *testing.T, buf *bytes.Buffer) map[string]string {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("open bundle: %v", err)
	}
	out := map[string]string{}
	for _, f := range reader.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", f.Name, err)
		}
		body, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("read %s: %v", f.Name, err)
		}
		out[f.Name] = string(body)
	}
	return out
}

func TestSupportBundleOmitsContentBodiesUnlessConfirmed(t *testing.T) {
	logDir := t.TempDir()
	// Deliberately not secret-shaped: the bundle redacts secret-like values
	// independently, and this test is about whether bodies are included at
	// all, not about redaction.
	const marker = "conversation body marker 4711"
	if err := writeFile(logDir, "agent.log", marker); err != nil {
		t.Fatal(err)
	}

	var withoutContent bytes.Buffer
	manifest, err := supportbundle.Write(&withoutContent, supportbundle.Options{LogDirs: []string{logDir}})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.ContentIncluded {
		t.Error("the default bundle claims to include content")
	}
	for name, body := range bundleEntries(t, &withoutContent) {
		if strings.Contains(body, marker) {
			t.Fatalf("a default support bundle carried log bodies in %s", name)
		}
	}
	if !strings.Contains(strings.Join(manifest.Included, " "), "omitted") {
		t.Errorf("the manifest does not say content was omitted: %v", manifest.Included)
	}

	var withContent bytes.Buffer
	confirmed, err := supportbundle.Write(&withContent, supportbundle.Options{
		LogDirs: []string{logDir}, IncludeContent: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !confirmed.ContentIncluded {
		t.Error("a confirmed bundle does not record that it includes content")
	}
	found := false
	for _, body := range bundleEntries(t, &withContent) {
		if strings.Contains(body, marker) {
			found = true
		}
	}
	if !found {
		t.Fatal("an explicitly confirmed bundle did not include the log body")
	}
}

// The raw Prometheus endpoint carries an `agent` label, and an agent ID names
// what another team is building. The registry is rendered in one pass with no
// per-caller view, so who may read it is the control — and `metrics:read` is
// held by developer, which is a workspace role.
func TestRawMetricsAreRestrictedInMultiUserMode(t *testing.T) {
	src := readGatewaySource(t, "server.go")
	line := findLine(t, src, `api.Get("/metrics"`)
	if !strings.Contains(line, "s.platformMetricsMW()") {
		t.Fatalf("the raw metrics endpoint is not behind the platform gate: %s", line)
	}
	// Personal must be untouched: with one workspace the labels identify
	// nobody, and invariant 7 says a single-user install should not notice.
	guard := readGatewaySource(t, "action_scope.go")
	if !strings.Contains(guard, "config.IsMultiUserMode") {
		t.Fatal("the metrics gate does not exempt personal deployments")
	}
}

func writeFile(dir, name, body string) error {
	return os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600)
}

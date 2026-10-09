package sessioncapturecompanion

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestArchiveContainsCompanion(t *testing.T) {
	body, err := Archive()
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"manifest.json": false, "service-worker.js": false, "content-script.js": false, "popup.html": false, "popup.js": false, "README.md": false}
	for _, file := range zr.File {
		if _, ok := want[file.Name[len("soulacy-session-capture/"):]]; ok {
			want[file.Name[len("soulacy-session-capture/"):]] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("archive missing %s", name)
		}
	}
}

func TestCaptureCompanionVersionIncludesRefreshFix(t *testing.T) {
	manifest, err := files.ReadFile("extension/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(manifest, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Version != "1.1.1" {
		t.Fatalf("version = %q, want 1.1.1", parsed.Version)
	}
	worker, err := files.ReadFile("extension/service-worker.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(worker), "const { base, domains } = normalizeBoundary(payload)") {
		t.Fatal("capture refresh does not retain the normalized base URL")
	}
}

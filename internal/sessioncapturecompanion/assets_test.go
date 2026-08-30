package sessioncapturecompanion

import (
	"archive/zip"
	"bytes"
	"io"
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

func TestArchiveConnectsAlreadyOpenSoulacyTabs(t *testing.T) {
	body, err := Archive()
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range zr.File {
		if !strings.HasSuffix(file.Name, "/service-worker.js") {
			continue
		}
		reader, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(reader)
		reader.Close()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(content, []byte("chrome.runtime.onInstalled")) ||
			!bytes.Contains(content, []byte("connectExistingSoulacyTabs")) {
			t.Fatal("service worker does not connect Soulacy tabs that predate extension installation")
		}
		return
	}
	t.Fatal("archive missing service-worker.js")
}

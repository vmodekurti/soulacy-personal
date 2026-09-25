package sessioncapturecompanion

import (
	"archive/zip"
	"bytes"
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

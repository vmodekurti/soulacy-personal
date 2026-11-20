package builtin

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The photo-album skill ships Python scripts; run their offline tests (fake
// Google backend, no network) so a broken script fails `go test` too.
func TestPhotoAlbumScripts(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not installed")
	}
	scripts, _ := filepath.Abs(filepath.Join("catalog", "photo-album", "scripts"))
	cmd := exec.Command(py, filepath.Join("testdata", "photo_album_test.py"), scripts)
	cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("photo-album script tests failed: %v\n%s", err, out)
	}
	t.Logf("%s", out)
}

package extstorage

import (
	"os"
	"path/filepath"
	"testing"
)

// The containment check was strings.HasPrefix(path, scratch) with no separator,
// so a SIBLING directory whose name merely extends the scratch dir's passed:
// "/tmp/scratch-abc" is a prefix of "/tmp/scratch-abcdef". The relative path is
// supplied by the sidecar subprocess's response, so a compromised or buggy
// sidecar could read outside its scratch space.
func TestReadScratchFile_SiblingDirectoryWithAnExtendedNameIsNotReachable(t *testing.T) {
	root := t.TempDir()
	scratch := filepath.Join(root, "scratch-abc")
	sibling := filepath.Join(root, "scratch-abcdef")
	for _, d := range []string{scratch, sibling} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	secret := filepath.Join(sibling, "secret.txt")
	if err := os.WriteFile(secret, []byte("not yours"), 0o600); err != nil {
		t.Fatal(err)
	}

	c := &Client{scratch: scratch}
	got, err := c.ReadScratchFile("../scratch-abcdef/secret.txt")
	if err == nil {
		t.Fatalf("read a file outside the scratch directory: %q", got)
	}
}

func TestReadScratchFile_OrdinaryRelativePathStillWorks(t *testing.T) {
	scratch := t.TempDir()
	if err := os.WriteFile(filepath.Join(scratch, "out.txt"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &Client{scratch: scratch}
	got, err := c.ReadScratchFile("out.txt")
	if err != nil {
		t.Fatalf("a legitimate scratch read was refused: %v", err)
	}
	if got != "mine" {
		t.Fatalf("content = %q", got)
	}
}

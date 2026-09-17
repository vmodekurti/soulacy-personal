package browserlibs

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// bundle builds a .tar.gz with the given entries and returns it with its sum.
func bundle(t *testing.T, entries map[string]string) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(buf.Bytes())
	return buf.Bytes(), hex.EncodeToString(sum[:])
}

func serve(t *testing.T, body []byte) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	t.Cleanup(s.Close)
	return s
}

func TestInstallUnpacksAndRecordsWhatItDid(t *testing.T) {
	ws := t.TempDir()
	data, sum := bundle(t, map[string]string{"libnss3.so": "x", "libnspr4.so": "yy"})
	srv := serve(t, data)

	m, err := Install(context.Background(), ws, Options{URL: srv.URL, SHA256: sum})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if m.Files != 2 || m.Bytes != 3 {
		t.Errorf("manifest = %+v, want 2 files / 3 bytes", m)
	}
	for _, name := range []string{"libnss3.so", "libnspr4.so"} {
		if _, err := os.Stat(filepath.Join(Dir(ws), name)); err != nil {
			t.Errorf("%s did not land: %v", name, err)
		}
	}
	if got, ok := Installed(ws); !ok || got.SHA256 != sum {
		t.Errorf("Installed() = %+v, %v — a finished install must be recognisable later", got, ok)
	}
}

// These files are loaded into the browser process, so a bundle that is not
// what was expected is code execution rather than a corrupt download.
func TestInstallRefusesAnUnverifiedBundle(t *testing.T) {
	ws := t.TempDir()
	data, sum := bundle(t, map[string]string{"libnss3.so": "x"})
	srv := serve(t, data)

	if _, err := Install(context.Background(), ws, Options{URL: srv.URL}); err == nil {
		t.Fatal("no checksum should be refused outright")
	} else if !strings.Contains(err.Error(), "sha256") {
		t.Errorf("the refusal should say what is missing: %v", err)
	}

	wrong := strings.Repeat("a", 64)
	if _, err := Install(context.Background(), ws, Options{URL: srv.URL, SHA256: wrong}); err == nil {
		t.Fatal("a mismatched checksum should be refused")
	}
	if _, ok := Installed(ws); ok {
		t.Error("a refused install must leave nothing behind")
	}
	_ = sum
}

// The oldest archive bug: an entry that climbs out of the directory it was
// supposed to land in. Only base names are used, so there is nowhere to climb.
func TestArchiveEntriesCannotEscapeTheDirectory(t *testing.T) {
	ws := t.TempDir()
	data, sum := bundle(t, map[string]string{
		"../../../../tmp/soulacy-escape.so": "nope",
		"libnss3.so":                        "x",
	})
	srv := serve(t, data)

	if _, err := Install(context.Background(), ws, Options{URL: srv.URL, SHA256: sum}); err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, err := os.Stat("/tmp/soulacy-escape.so"); err == nil {
		t.Fatal("an archive entry escaped the install directory")
	}
	// It lands under its base name instead of being silently dropped.
	if _, err := os.Stat(filepath.Join(Dir(ws), "soulacy-escape.so")); err != nil {
		t.Errorf("the entry should have been flattened into the directory: %v", err)
	}
}

// A failed install must not destroy a working one: a deployment that was
// driving a browser should still be driving it after a bad URL.
func TestAFailedInstallLeavesThePreviousOneIntact(t *testing.T) {
	ws := t.TempDir()
	good, sum := bundle(t, map[string]string{"libnss3.so": "x"})
	srv := serve(t, good)
	if _, err := Install(context.Background(), ws, Options{URL: srv.URL, SHA256: sum}); err != nil {
		t.Fatalf("first install: %v", err)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad.Close()
	if _, err := Install(context.Background(), ws, Options{URL: bad.URL, SHA256: sum}); err == nil {
		t.Fatal("a 500 should fail the install")
	}
	if _, ok := Installed(ws); !ok {
		t.Error("the working install was destroyed by a failed one")
	}
}

func TestApplyPutsTheDirectoryOnTheSearchPath(t *testing.T) {
	ws := t.TempDir()
	t.Setenv("LD_LIBRARY_PATH", "")

	if Apply(ws) {
		t.Error("nothing is installed, so there is nothing to apply")
	}

	data, sum := bundle(t, map[string]string{"libnss3.so": "x"})
	srv := serve(t, data)
	if _, err := Install(context.Background(), ws, Options{URL: srv.URL, SHA256: sum}); err != nil {
		t.Fatalf("install: %v", err)
	}
	if !Apply(ws) {
		t.Fatal("an installed bundle should apply")
	}
	if got := os.Getenv("LD_LIBRARY_PATH"); !strings.Contains(got, Dir(ws)) {
		t.Errorf("LD_LIBRARY_PATH = %q, want it to contain %q", got, Dir(ws))
	}
}

// Apply runs at startup and again after an install. It must not grow the
// variable each time, which would eventually break exec.
func TestApplyIsIdempotent(t *testing.T) {
	ws := t.TempDir()
	t.Setenv("LD_LIBRARY_PATH", "/existing/path")
	data, sum := bundle(t, map[string]string{"libnss3.so": "x"})
	srv := serve(t, data)
	if _, err := Install(context.Background(), ws, Options{URL: srv.URL, SHA256: sum}); err != nil {
		t.Fatalf("install: %v", err)
	}
	for i := 0; i < 5; i++ {
		Apply(ws)
	}
	got := os.Getenv("LD_LIBRARY_PATH")
	if strings.Count(got, Dir(ws)) != 1 {
		t.Errorf("path repeated: %q", got)
	}
	if !strings.Contains(got, "/existing/path") {
		t.Errorf("an existing search path was lost: %q", got)
	}
}

// A directory left behind by an install that died halfway is not an install.
func TestAHalfWrittenDirectoryIsNotAnInstall(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(Dir(ws), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(Dir(ws), "libnss3.so"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := Installed(ws); ok {
		t.Error("libraries with no manifest are leftovers, not an install")
	}
}

func TestNoWorkspaceIsAnError(t *testing.T) {
	if _, err := Install(context.Background(), "", Options{URL: "https://x", SHA256: strings.Repeat("a", 64)}); err == nil {
		t.Error("there is nowhere to install to")
	}
	if Dir("") != "" {
		t.Error("no workspace means no directory")
	}
}

// An operator who built the bundle themselves should not have to publish it
// somewhere public before they are allowed to install it.
func TestInstallFromALocalFile(t *testing.T) {
	ws := t.TempDir()
	data, sum := bundle(t, map[string]string{"libnss3.so": "x"})
	path := filepath.Join(t.TempDir(), "browser-libs.tar.gz")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	for _, src := range []string{path, "file://" + path} {
		if err := os.RemoveAll(Dir(ws)); err != nil {
			t.Fatal(err)
		}
		if _, err := Install(context.Background(), ws, Options{URL: src, SHA256: sum}); err != nil {
			t.Fatalf("install from %q: %v", src, err)
		}
		if _, ok := Installed(ws); !ok {
			t.Errorf("install from %q left nothing behind", src)
		}
	}
}

// A local bundle is verified like any other: the file being on the box says
// nothing about whether it is the file that was meant.
func TestALocalBundleIsStillChecksummed(t *testing.T) {
	ws := t.TempDir()
	data, _ := bundle(t, map[string]string{"libnss3.so": "x"})
	path := filepath.Join(t.TempDir(), "browser-libs.tar.gz")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(context.Background(), ws, Options{URL: path, SHA256: strings.Repeat("b", 64)}); err == nil {
		t.Fatal("a local file with the wrong checksum should be refused")
	}
}

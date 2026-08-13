package updates

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCompareSemver(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.0.1", "1.0.0", 1},
		{"1.0.0", "1.0.1", -1},
		{"v1.2.3", "1.2.0", 1},
		{"1.2.3-alpha", "1.2.3", 0},
		{"dev", "1.0.0", 0},
	}
	for _, tt := range tests {
		got := CompareSemver(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("CompareSemver(%q, %q) = %d; want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestCheckForUpdateCustomManifest(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		manifest := UpdateManifest{
			Product: "soulacy",
			Version: "1.2.3",
			Artifacts: []UpdateArtifact{
				{
					Name:   "soulacy_1.2.3_darwin_arm64.tar.gz",
					OS:     "darwin",
					Arch:   "arm64",
					SHA256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
					Bytes:  0,
				},
			},
		}
		_ = json.NewEncoder(w).Encode(manifest)
	}))
	defer ts.Close()
	oldClient := HTTPClient
	HTTPClient = ts.Client()
	t.Cleanup(func() { HTTPClient = oldClient })

	res, err := CheckForUpdate(context.Background(), ts.URL, "1.0.0")
	if err != nil {
		t.Fatalf("CheckForUpdate: %v", err)
	}
	if !res.UpdateAvailable {
		t.Errorf("expected update to be available")
	}
	if res.LatestVersion != "1.2.3" {
		t.Errorf("got latest version %s, want 1.2.3", res.LatestVersion)
	}
}

func TestInstallUpdateDryRun(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "soulacy-update-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	_ = os.WriteFile(filepath.Join(tempDir, "soulacy"), []byte("current-soulacy"), 0o755)
	_ = os.WriteFile(filepath.Join(tempDir, "sy"), []byte("current-sy"), 0o755)

	tarGzData, expectedSHA, err := createMockTarGz()
	if err != nil {
		t.Fatalf("failed to create mock tar.gz: %v", err)
	}

	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/manifest.json" || r.URL.Path == "/" {
			w.Header().Set("Content-Type", "application/json")
			manifest := UpdateManifest{
				Product: "soulacy",
				Version: "2.0.0",
				Artifacts: []UpdateArtifact{
					{
						Name:   "soulacy_2.0.0_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz",
						OS:     runtime.GOOS,
						Arch:   runtime.GOARCH,
						SHA256: expectedSHA,
						Bytes:  int64(len(tarGzData)),
						URL:    "archive.tar.gz",
					},
				},
			}
			_ = json.NewEncoder(w).Encode(manifest)
			return
		}

		if r.URL.Path == "/archive.tar.gz" {
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(tarGzData)
			return
		}
		if strings.HasSuffix(r.URL.Path, ".cosign.bundle") {
			_, _ = w.Write([]byte(`{"verificationMaterial":{}}`))
			return
		}

		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()
	oldClient := HTTPClient
	HTTPClient = ts.Client()
	t.Cleanup(func() { HTTPClient = oldClient })
	oldVerify := VerifySigstore
	var verified []string
	VerifySigstore = func(_ context.Context, artifactPath, bundlePath, identity string) error {
		if _, err := os.Stat(artifactPath); err != nil {
			return err
		}
		if _, err := os.Stat(bundlePath); err != nil {
			return err
		}
		verified = append(verified, identity)
		return nil
	}
	t.Cleanup(func() { VerifySigstore = oldVerify })

	opts := UpdateInstallOptions{
		ManifestSource: ts.URL + "/manifest.json",
		CurrentVersion: "1.0.0",
		InstallDir:     tempDir,
		DryRun:         true,
		Yes:            true,
	}

	res, err := InstallUpdate(context.Background(), opts)
	if err != nil {
		t.Fatalf("InstallUpdate: %v", err)
	}
	if !res.UpdateAvailable {
		t.Errorf("expected update available")
	}
	if len(verified) != 2 {
		t.Fatalf("Sigstore verifications = %d, want manifest + artifact", len(verified))
	}
}

func TestVerifyUpdateArtifactRequiresWellFormedSHA256(t *testing.T) {
	for _, checksum := range []string{"", "xyz", strings.Repeat("a", 63)} {
		if err := verifyUpdateArtifact(UpdateArtifact{Name: "release.tar.gz", SHA256: checksum}, []byte("data")); err == nil {
			t.Fatalf("checksum %q was accepted", checksum)
		}
	}
}

func TestRemoteUpdateSourcesRequireHTTPS(t *testing.T) {
	if _, err := readUpdateManifest(context.Background(), "http://updates.example/manifest.json"); err == nil {
		t.Fatal("HTTP manifest was accepted")
	}
	if _, _, _, err := downloadUpdateArtifact(context.Background(), "https://updates.example/manifest.json", UpdateArtifact{URL: "http://updates.example/release.tar.gz"}); err == nil {
		t.Fatal("HTTP artifact was accepted")
	}
}

func TestInstallUpdateFilesRollsBackBothBinaries(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"soulacy", "sy"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("old-"+name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	oldRename := renameUpdateFile
	installRenames := 0
	renameUpdateFile = func(old, new string) error {
		if strings.Contains(filepath.Base(old), ".update-") {
			installRenames++
			if installRenames == 2 {
				return errors.New("injected second replacement failure")
			}
		}
		return os.Rename(old, new)
	}
	t.Cleanup(func() { renameUpdateFile = oldRename })
	_, err := installUpdateFiles(dir, map[string][]byte{"soulacy": []byte("new-soulacy"), "sy": []byte("new-sy")})
	if err == nil {
		t.Fatal("injected replacement failure was ignored")
	}
	for _, name := range []string{"soulacy", "sy"} {
		data, readErr := os.ReadFile(filepath.Join(dir, name))
		if readErr != nil || string(data) != "old-"+name {
			t.Fatalf("%s was not rolled back: %q err=%v", name, data, readErr)
		}
	}
}

func createMockTarGz() ([]byte, string, error) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	for _, name := range []string{"soulacy", "sy"} {
		hdr := &tar.Header{
			Name: name,
			Mode: 0755,
			Size: int64(len(name)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, "", err
		}
		if _, err := tw.Write([]byte(name)); err != nil {
			return nil, "", err
		}
	}
	_ = tw.Close()
	_ = gw.Close()

	data := buf.Bytes()
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:]), nil
}

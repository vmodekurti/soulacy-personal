// registryserver_test.go — the reference E19 HTTP package registry.
// The decisive test is the end-to-end round trip: the E19 client provider
// resolving and fetching (signature-verified) from this server.
package registryserver

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/pkgregistry"
	sdkpkg "github.com/soulacy/soulacy/sdk/pkgregistry"
)

func writeArchive(t *testing.T, dir, name string, files map[string]string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for n, c := range files {
		if err := tw.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: int64(len(c))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(c)); err != nil {
			t.Fatal(err)
		}
	}
	_ = tw.Close()
	_ = gz.Close()
	if err := os.WriteFile(filepath.Join(dir, name), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParseArchiveName(t *testing.T) {
	cases := map[string][2]string{
		"greeter-1.0.0.tar.gz":               {"greeter", "1.0.0"},
		"self-improving-agent-2.10.3.tar.gz": {"self-improving-agent", "2.10.3"},
		"thing-v1.2.tgz":                     {"thing", "v1.2"},
		"noversion.tar.gz":                   {"noversion", "0.0.0"},
		"dash-name-only.tar.gz":              {"dash-name-only", "0.0.0"},
	}
	for name, want := range cases {
		slug, ver := parseArchiveName(name)
		if slug != want[0] || ver != want[1] {
			t.Errorf("parseArchiveName(%q) = (%q, %q), want (%q, %q)", name, slug, ver, want[0], want[1])
		}
	}
}

func TestIndex_LatestVersionWins(t *testing.T) {
	dir := t.TempDir()
	writeArchive(t, dir, "greeter-1.0.0.tar.gz", map[string]string{"SKILL.md": "# old"})
	writeArchive(t, dir, "greeter-1.10.0.tar.gz", map[string]string{"SKILL.md": "# new"})
	writeArchive(t, dir, "greeter-1.2.0.tar.gz", map[string]string{"SKILL.md": "# mid"})

	srv, err := New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	pkg, ok := srv.lookup("greeter")
	if !ok {
		t.Fatal("greeter not indexed")
	}
	if pkg.Version != "1.10.0" {
		t.Errorf("latest = %q, want 1.10.0 (numeric compare, not lexicographic)", pkg.Version)
	}
}

func TestServer_EndToEndWithE19Client(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	dir := t.TempDir()
	writeArchive(t, dir, "greeter-1.0.0.tar.gz", map[string]string{"SKILL.md": "# Greeter\nSays hello."})
	// Optional metadata sidecar.
	if err := os.WriteFile(filepath.Join(dir, "greeter.yaml"),
		[]byte("description: Greets people warmly.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rs, err := New(dir, priv)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(rs.Handler())
	t.Cleanup(ts.Close)

	// The E19 client with the matching signing key.
	eng, errs := pkgregistry.FromConfig([]config.RegistryConfig{
		{ID: "ref", Type: "http", BaseURL: ts.URL, Priority: 1,
			SigningKey: hex.EncodeToString(pub)},
	}, nil)
	if len(errs) != 0 {
		t.Fatalf("client config errs: %v", errs)
	}

	ctx := context.Background()
	pkg, err := eng.Resolve(ctx, "greeter")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if pkg.Version != "1.0.0" || pkg.Checksum == "" || pkg.Signature == "" {
		t.Errorf("pkg = %+v", pkg)
	}
	if pkg.Description != "Greets people warmly." {
		t.Errorf("description = %q", pkg.Description)
	}

	dst := t.TempDir()
	if err := eng.Fetch(ctx, pkg, dst); err != nil {
		t.Fatalf("Fetch (signed): %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dst, "SKILL.md"))
	if err != nil || !strings.Contains(string(body), "Greeter") {
		t.Errorf("fetched SKILL.md = %q err=%v", body, err)
	}

	// Search finds it by slug substring and by description.
	results := eng.Search(ctx, "greet")
	if len(results) != 1 || results[0].Slug != "greeter" {
		t.Errorf("search = %+v", results)
	}
	if got := eng.Search(ctx, "warmly"); len(got) != 1 {
		t.Errorf("description search = %+v", got)
	}
	if got := eng.Search(ctx, "zzz-nothing"); len(got) != 0 {
		t.Errorf("no-match search = %+v", got)
	}
}

func TestServer_UnknownSlug404(t *testing.T) {
	dir := t.TempDir()
	writeArchive(t, dir, "a-1.0.0.tar.gz", map[string]string{"SKILL.md": "x"})
	rs, _ := New(dir, nil)
	ts := httptest.NewServer(rs.Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/v1/packages/ghost")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestServer_ArchiveTraversalGuard(t *testing.T) {
	dir := t.TempDir()
	writeArchive(t, dir, "a-1.0.0.tar.gz", map[string]string{"SKILL.md": "x"})
	// A secret OUTSIDE the packages dir must be unreachable.
	if err := os.WriteFile(filepath.Join(filepath.Dir(dir), "secret.txt"), []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	rs, _ := New(dir, nil)
	ts := httptest.NewServer(rs.Handler())
	t.Cleanup(ts.Close)

	for _, probe := range []string{
		"/archives/../secret.txt",
		"/archives/..%2Fsecret.txt",
		"/archives/%2e%2e/secret.txt",
	} {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+probe, nil)
		resp, err := http.DefaultTransport.RoundTrip(req) // no client-side path cleaning
		if err != nil {
			t.Fatal(err)
		}
		body := make([]byte, 4)
		_, _ = resp.Body.Read(body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK && strings.HasPrefix(string(body), "nope") {
			t.Errorf("traversal %q leaked the secret", probe)
		}
	}
}

func TestServer_ReindexPicksUpNewPackages(t *testing.T) {
	dir := t.TempDir()
	writeArchive(t, dir, "a-1.0.0.tar.gz", map[string]string{"SKILL.md": "x"})
	rs, _ := New(dir, nil)
	if _, ok := rs.lookup("b"); ok {
		t.Fatal("b should not exist yet")
	}
	writeArchive(t, dir, "b-1.0.0.tar.gz", map[string]string{"SKILL.md": "y"})
	if err := rs.Reindex(); err != nil {
		t.Fatal(err)
	}
	if _, ok := rs.lookup("b"); !ok {
		t.Error("b missing after Reindex")
	}
}

func TestServer_SearchEndpointShape(t *testing.T) {
	dir := t.TempDir()
	writeArchive(t, dir, "a-1.0.0.tar.gz", map[string]string{"SKILL.md": "x"})
	rs, _ := New(dir, nil)
	ts := httptest.NewServer(rs.Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/v1/search?q=a")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Packages []sdkpkg.Package `json:"packages"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Packages) != 1 || out.Packages[0].Slug != "a" {
		t.Errorf("packages = %+v", out.Packages)
	}
}

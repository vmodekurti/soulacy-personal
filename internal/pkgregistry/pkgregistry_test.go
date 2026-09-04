// pkgregistry_test.go — Story E19: HTTP + git registry providers and the
// multi-registry resolution engine.
package pkgregistry

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/sdk/pkgregistry"
	"github.com/soulacy/soulacy/sdk/registry"
	"go.uber.org/zap"
)

// makeTarGz builds a tar.gz containing the given files and returns
// (archive bytes, sha256 hex).
func makeTarGz(t *testing.T, files map[string]string) ([]byte, string) {
	t.Helper()
	var buf []byte
	{
		tmp := t.TempDir()
		p := filepath.Join(tmp, "a.tar.gz")
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		gz := gzip.NewWriter(f)
		tw := tar.NewWriter(gz)
		for name, content := range files {
			if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}); err != nil {
				t.Fatal(err)
			}
			if _, err := tw.Write([]byte(content)); err != nil {
				t.Fatal(err)
			}
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		buf, err = os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
	}
	sum := sha256.Sum256(buf)
	return buf, hex.EncodeToString(sum[:])
}

// newRegistryServer serves a one-package registry implementing the E19 HTTP
// protocol: /v1/search, /v1/packages/{slug}, and the archive itself.
func newRegistryServer(t *testing.T, slug, checksum string, archive []byte, wantAuth string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var srv *httptest.Server
	check := func(w http.ResponseWriter, r *http.Request) bool {
		if wantAuth != "" && r.Header.Get("Authorization") != wantAuth {
			w.WriteHeader(http.StatusUnauthorized)
			return false
		}
		return true
	}
	mux.HandleFunc("/v1/search", func(w http.ResponseWriter, r *http.Request) {
		if !check(w, r) {
			return
		}
		if r.URL.Query().Get("q") == "" {
			http.Error(w, "missing q", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"packages": []pkgregistry.Package{{
				Slug: slug, Version: "1.2.0", Description: "a test skill",
			}},
		})
	})
	mux.HandleFunc("/v1/packages/", func(w http.ResponseWriter, r *http.Request) {
		if !check(w, r) {
			return
		}
		got := r.URL.Path[len("/v1/packages/"):]
		if got != slug {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(pkgregistry.Package{
			Slug: slug, Version: "1.2.0", Checksum: checksum,
			Source:   srv.URL + "/archives/" + slug + ".tar.gz",
			Manifest: map[string]any{"name": slug},
		})
	})
	mux.HandleFunc("/archives/", func(w http.ResponseWriter, r *http.Request) {
		if !check(w, r) {
			return
		}
		_, _ = w.Write(archive)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestHTTPProvider_ResolveSearchFetch(t *testing.T) {
	archive, checksum := makeTarGz(t, map[string]string{"SKILL.md": "# test skill"})
	srv := newRegistryServer(t, "test-skill", checksum, archive, "Bearer sekrit")

	p, err := newHTTPProvider(map[string]any{
		"id":       "main",
		"base_url": srv.URL,
		"auth_headers": map[string]any{
			"Authorization": "Bearer sekrit",
		},
	})
	if err != nil {
		t.Fatalf("newHTTPProvider: %v", err)
	}
	if p.ID() != "main" {
		t.Errorf("ID = %q", p.ID())
	}

	ctx := context.Background()

	// Search
	results, err := p.Search(ctx, "test")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 || results[0].Slug != "test-skill" {
		t.Fatalf("Search results = %+v", results)
	}

	// Resolve
	pkg, err := p.Resolve(ctx, "test-skill")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if pkg.Version != "1.2.0" || pkg.Checksum != checksum || pkg.Source == "" {
		t.Errorf("Resolve pkg = %+v", pkg)
	}

	// Resolve unknown slug → ErrNotFound
	if _, err := p.Resolve(ctx, "nope"); !errors.Is(err, pkgregistry.ErrNotFound) {
		t.Errorf("unknown slug err = %v, want ErrNotFound", err)
	}

	// Fetch extracts the verified archive.
	dst := t.TempDir()
	if err := p.Fetch(ctx, pkg, dst); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dst, "SKILL.md"))
	if err != nil || string(body) != "# test skill" {
		t.Errorf("extracted SKILL.md = %q err=%v", body, err)
	}

	// Fetch with a wrong checksum must refuse.
	bad := pkg
	bad.Checksum = "deadbeef"
	if err := p.Fetch(ctx, bad, t.TempDir()); err == nil {
		t.Error("Fetch with wrong checksum must error")
	}

	// Fetch with NO checksum must refuse (unverifiable archive).
	bad = pkg
	bad.Checksum = ""
	if err := p.Fetch(ctx, bad, t.TempDir()); err == nil {
		t.Error("Fetch with empty checksum must error")
	}
}

func TestHTTPProvider_AuthRequired(t *testing.T) {
	archive, checksum := makeTarGz(t, map[string]string{"SKILL.md": "x"})
	srv := newRegistryServer(t, "s", checksum, archive, "Bearer sekrit")

	p, err := newHTTPProvider(map[string]any{"id": "noauth", "base_url": srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Resolve(context.Background(), "s"); err == nil {
		t.Error("Resolve without auth header should fail against an authed registry")
	}
}

func TestHTTPProvider_RequiresBaseURL(t *testing.T) {
	if _, err := newHTTPProvider(map[string]any{"id": "x"}); err == nil {
		t.Error("missing base_url must error")
	}
}

func TestGitProvider_Resolve(t *testing.T) {
	p, err := newGitProvider(map[string]any{"id": "github"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	pkg, err := p.Resolve(ctx, "github.com/user/my-skill")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if pkg.Source != "https://github.com/user/my-skill" {
		t.Errorf("Source = %q", pkg.Source)
	}
	if pkg.Version != "HEAD" || pkg.Checksum != "" {
		t.Errorf("pkg = %+v", pkg)
	}

	// Full URLs pass through.
	pkg, err = p.Resolve(ctx, "https://gitlab.com/user/repo.git")
	if err != nil {
		t.Fatalf("Resolve url: %v", err)
	}
	if pkg.Source != "https://gitlab.com/user/repo.git" {
		t.Errorf("Source = %q", pkg.Source)
	}

	// Plain slugs are not git sources → ErrNotFound so the engine falls
	// through to other registries.
	if _, err := p.Resolve(ctx, "self-improving-agent"); !errors.Is(err, pkgregistry.ErrNotFound) {
		t.Errorf("plain slug err = %v, want ErrNotFound", err)
	}
}

func TestGitProvider_FetchClonesRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	// Build a local source repo.
	src := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = src
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(src, "SKILL.md"), []byte("# from git"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "init")

	p, _ := newGitProvider(map[string]any{"id": "github"})
	dst := t.TempDir()
	err := p.Fetch(context.Background(), pkgregistry.Package{Slug: src, Source: src}, dst)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dst, "SKILL.md"))
	if err != nil || string(body) != "# from git" {
		t.Errorf("cloned SKILL.md = %q err=%v", body, err)
	}
	if _, err := os.Stat(filepath.Join(dst, ".git")); !os.IsNotExist(err) {
		t.Error(".git must be stripped from the fetched package")
	}
}

// ── engine ───────────────────────────────────────────────────────────────────

type stubProvider struct {
	id      string
	pkgs    map[string]pkgregistry.Package
	results []pkgregistry.Package
	err     error
}

func (s *stubProvider) ID() string { return s.id }
func (s *stubProvider) Search(_ context.Context, _ string) ([]pkgregistry.Package, error) {
	return s.results, s.err
}
func (s *stubProvider) Resolve(_ context.Context, slug string) (pkgregistry.Package, error) {
	if s.err != nil {
		return pkgregistry.Package{}, s.err
	}
	p, ok := s.pkgs[slug]
	if !ok {
		return pkgregistry.Package{}, pkgregistry.ErrNotFound
	}
	return p, nil
}
func (s *stubProvider) Fetch(_ context.Context, _ pkgregistry.Package, dst string) error {
	return os.WriteFile(filepath.Join(dst, "from"), []byte(s.id), 0o644)
}

func TestEngine_ResolvePriorityAndFallback(t *testing.T) {
	first := &stubProvider{id: "first", pkgs: map[string]pkgregistry.Package{}}
	second := &stubProvider{id: "second", pkgs: map[string]pkgregistry.Package{
		"thing": {Slug: "thing", Version: "2.0.0"},
	}}
	// Register out of priority order to prove sorting.
	eng := NewEngine([]Entry{
		{Provider: second, Priority: 50},
		{Provider: first, Priority: 10},
	}, zap.NewNop())

	pkg, err := eng.Resolve(context.Background(), "thing")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if pkg.Provider != "second" || pkg.Version != "2.0.0" {
		t.Errorf("pkg = %+v, want provider=second (fallback past first)", pkg)
	}

	// Priority wins when both know the slug.
	first.pkgs["thing"] = pkgregistry.Package{Slug: "thing", Version: "1.0.0"}
	pkg, err = eng.Resolve(context.Background(), "thing")
	if err != nil {
		t.Fatal(err)
	}
	if pkg.Provider != "first" {
		t.Errorf("pkg.Provider = %q, want first (lower priority value runs first)", pkg.Provider)
	}

	// Nothing resolves → ErrNotFound.
	if _, err := eng.Resolve(context.Background(), "ghost"); !errors.Is(err, pkgregistry.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}

	// A broken provider falls through, not aborts.
	broken := &stubProvider{id: "broken", err: errors.New("registry down")}
	eng = NewEngine([]Entry{
		{Provider: broken, Priority: 1},
		{Provider: second, Priority: 2},
	}, zap.NewNop())
	pkg, err = eng.Resolve(context.Background(), "thing")
	if err != nil || pkg.Provider != "second" {
		t.Errorf("fallback past broken provider: pkg=%+v err=%v", pkg, err)
	}
}

func TestEngine_SearchAggregatesDedupes(t *testing.T) {
	a := &stubProvider{id: "a", results: []pkgregistry.Package{
		{Slug: "x", Version: "1.0.0"}, {Slug: "y"},
	}}
	b := &stubProvider{id: "b", results: []pkgregistry.Package{
		{Slug: "x", Version: "9.9.9"}, {Slug: "z"},
	}}
	eng := NewEngine([]Entry{{Provider: a, Priority: 1}, {Provider: b, Priority: 2}}, zap.NewNop())

	got := eng.Search(context.Background(), "anything")
	if len(got) != 3 {
		t.Fatalf("Search returned %d results, want 3 (x deduped): %+v", len(got), got)
	}
	if got[0].Slug != "x" || got[0].Provider != "a" || got[0].Version != "1.0.0" {
		t.Errorf("dedup must keep the higher-priority hit: %+v", got[0])
	}
}

func TestEngine_SearchDetailedAddsDirectResolveHit(t *testing.T) {
	searchDown := &stubProvider{id: "indexed", err: errors.New("search unavailable")}
	direct := &stubProvider{id: "direct", pkgs: map[string]pkgregistry.Package{
		"github.com/acme/options-strategy-advisor": {
			Slug:    "github.com/acme/options-strategy-advisor",
			Version: "HEAD",
		},
	}}
	eng := NewEngine([]Entry{
		{Provider: searchDown, Priority: 1},
		{Provider: direct, Priority: 2},
	}, zap.NewNop())

	got, warnings := eng.SearchDetailed(context.Background(), "github.com/acme/options-strategy-advisor")
	if len(warnings) != 1 {
		t.Fatalf("warnings = %d, want failed search warning", len(warnings))
	}
	if len(got) != 1 {
		t.Fatalf("SearchDetailed returned %d results, want direct resolve hit: %+v", len(got), got)
	}
	if got[0].Provider != "direct" || got[0].Slug != "github.com/acme/options-strategy-advisor" {
		t.Fatalf("direct result = %+v", got[0])
	}
}

func TestEngine_FetchRoutesByProvider(t *testing.T) {
	a := &stubProvider{id: "a"}
	b := &stubProvider{id: "b"}
	eng := NewEngine([]Entry{{Provider: a, Priority: 1}, {Provider: b, Priority: 2}}, zap.NewNop())

	dst := t.TempDir()
	if err := eng.Fetch(context.Background(), pkgregistry.Package{Slug: "s", Provider: "b"}, dst); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	body, _ := os.ReadFile(filepath.Join(dst, "from"))
	if string(body) != "b" {
		t.Errorf("fetch routed to %q, want b", body)
	}
	if err := eng.Fetch(context.Background(), pkgregistry.Package{Slug: "s", Provider: "ghost"}, dst); err == nil {
		t.Error("unknown provider id must error")
	}
}

func TestFromConfig(t *testing.T) {
	entries := []config.RegistryConfig{
		{ID: "main", Type: "http", BaseURL: "https://reg.example.com", Priority: 10,
			AuthHeaders: map[string]string{"Authorization": "Bearer x"}},
		{Type: "git", Priority: 100}, // id defaults to type
		{ID: "bad", Type: "no-such-type"},
	}
	eng, errs := FromConfig(entries, zap.NewNop())
	if len(errs) != 1 {
		t.Errorf("errs = %v, want exactly the unknown-type entry", errs)
	}
	ids := eng.Providers()
	if len(ids) != 2 || ids[0] != "main" || ids[1] != "git" {
		t.Errorf("Providers() = %v, want [main git] in priority order", ids)
	}
}

func TestBuiltinProvidersRegistered(t *testing.T) {
	for _, typ := range []string{"http", "git"} {
		found := false
		for _, n := range registry.PkgRegistries() {
			if n == typ {
				found = true
			}
		}
		if !found {
			t.Errorf("provider type %q not registered (have %v)", typ, registry.PkgRegistries())
		}
	}
}

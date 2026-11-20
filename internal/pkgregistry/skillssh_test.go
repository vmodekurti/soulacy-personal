package pkgregistry

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdkpkg "github.com/soulacy/soulacy/sdk/pkgregistry"
)

// fakeSkillsSh serves the subset of the skills.sh API the provider uses.
func fakeSkillsSh(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/skills/search", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") == "" {
			http.Error(w, `{"error":"bad_request"}`, 400)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{
				"id": "acme/skills/web-audit", "slug": "web-audit",
				"name": "Web Audit", "source": "acme/skills",
				"installs": 1234, "sourceType": "github",
				"installUrl": "https://github.com/acme/skills",
				"url":        "https://skills.example/acme/skills/web-audit",
			}},
		})
	})
	mux.HandleFunc("/api/v1/skills/audit/", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"audits": []map[string]any{
				{"provider": "Gen Agent Trust Hub", "status": "pass", "riskLevel": "LOW"},
				{"provider": "Socket", "status": "warn", "summary": "1 alert"},
			},
		})
	})
	mux.HandleFunc("/api/v1/skills/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/v1/skills/")
		if id != "acme/skills/web-audit" {
			http.Error(w, `{"error":"not_found"}`, 404)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": id, "source": "acme/skills", "slug": "web-audit",
			"installs": 1234, "hash": "abcdef1234567890",
			"files": []map[string]string{
				{"path": "SKILL.md", "contents": "---\nname: Web Audit\n---\nAudit a site."},
				{"path": "scripts/run.py", "contents": "print('hi')"},
			},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newTestSkillsShProvider(t *testing.T, base string) sdkpkg.Provider {
	t.Helper()
	p, err := newSkillsShProvider(map[string]any{"id": "skills-sh", "base_url": base})
	if err != nil {
		t.Fatalf("newSkillsShProvider: %v", err)
	}
	return p
}

func TestSkillsSh_Search(t *testing.T) {
	srv := fakeSkillsSh(t)
	p := newTestSkillsShProvider(t, srv.URL)
	pkgs, err := p.Search(context.Background(), "audit")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(pkgs) != 1 || pkgs[0].Slug != "acme/skills/web-audit" {
		t.Fatalf("pkgs = %+v", pkgs)
	}
	if !strings.Contains(pkgs[0].Description, "Web Audit") {
		t.Errorf("description = %q", pkgs[0].Description)
	}
}

func TestSkillsSh_SearchRetriesReadableSlugVariant(t *testing.T) {
	var queries []string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/skills/search", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		queries = append(queries, q)
		if q != "options strategy advisor" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{
				"id": "acme/options/options-strategy-advisor", "slug": "options-strategy-advisor",
				"name": "Options Strategy Advisor", "source": "acme/options",
				"installs": 88,
			}},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	p := newTestSkillsShProvider(t, srv.URL)
	pkgs, err := p.Search(context.Background(), "options-strategy-advisor")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(pkgs) != 1 || pkgs[0].Slug != "acme/options/options-strategy-advisor" {
		t.Fatalf("pkgs = %+v", pkgs)
	}
	if strings.Join(queries, "|") != "options-strategy-advisor|options strategy advisor" {
		t.Fatalf("queries = %v", queries)
	}
}

func TestSkillsSh_SearchSendsConfiguredAuthHeader(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/skills/search", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer token-123" {
			t.Fatalf("Authorization = %q, want configured bearer token", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	p, err := newSkillsShProvider(map[string]any{
		"id":       "skills-sh",
		"base_url": srv.URL,
		"auth_headers": map[string]any{
			"Authorization": "Bearer token-123",
		},
	})
	if err != nil {
		t.Fatalf("newSkillsShProvider: %v", err)
	}
	if _, err := p.Search(context.Background(), "audit"); err != nil {
		t.Fatalf("Search: %v", err)
	}
}

func TestSkillsSh_SearchUsesVercelOIDCTokenForSkillsSh(t *testing.T) {
	t.Setenv("VERCEL_OIDC_TOKEN", "oidc-token-123")
	p, err := newSkillsShProvider(map[string]any{"id": "skills-sh", "base_url": "https://skills.sh"})
	if err != nil {
		t.Fatalf("newSkillsShProvider: %v", err)
	}
	if got := p.headers["Authorization"]; got != "Bearer oidc-token-123" {
		t.Fatalf("Authorization = %q, want env bearer token", got)
	}
}

func TestSkillsSh_SearchReturnsHTTPErrorMessage(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/skills/search", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"authentication_required","message":"token required"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	p := newTestSkillsShProvider(t, srv.URL)
	_, err := p.Search(context.Background(), "audit")
	if err == nil || !strings.Contains(err.Error(), "authentication_required: token required") {
		t.Fatalf("Search err = %v, want authentication message", err)
	}
}

func TestSkillsSh_SearchFallsBackToPublicCatalog(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/skills/search", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"authentication_required","message":"token required"}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<script>self.__next_f.push([1,"{\"source\":\"acme/options\",\"skillId\":\"options-strategy-advisor\",\"name\":\"Options Strategy Advisor\",\"installs\":42}"])</script>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	p, err := newSkillsShProvider(map[string]any{
		"id":       "skills-sh",
		"base_url": srv.URL + "/skills.sh-proxy",
	})
	if err != nil {
		t.Fatalf("newSkillsShProvider: %v", err)
	}
	// Point the provider at the test server while still exercising the
	// skills.sh-only fallback gate above.
	p.baseURL = srv.URL + "/skills.sh"

	pkgs, err := p.Search(context.Background(), "options-strategy-advisor")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(pkgs) != 1 || pkgs[0].Slug != "acme/options/options-strategy-advisor" {
		t.Fatalf("pkgs = %+v", pkgs)
	}
	if !strings.Contains(pkgs[0].Description, "public catalog fallback") {
		t.Fatalf("description = %q", pkgs[0].Description)
	}
}

func TestSkillsShCatalogMatchesNormalizesQuery(t *testing.T) {
	catalog := `"source":"acme/options","skillId":"options-strategy-advisor","name":"Options Strategy Advisor","installs":42`
	pkgs := skillsShCatalogMatches(catalog, "strategy advisor", "skills-sh")
	if len(pkgs) != 1 || pkgs[0].Slug != "acme/options/options-strategy-advisor" {
		t.Fatalf("pkgs = %+v", pkgs)
	}
}

func TestSkillsShCatalogMatchesTokenOrderAndCompactForms(t *testing.T) {
	catalog := `"source":"acme/options","skillId":"options-strategy-advisor","name":"Options Strategy Advisor","installs":42`
	for _, q := range []string{
		"options advisor",
		"advisor options",
		"optionsStrategyAdvisor",
		"acme/options/options-strategy-advisor",
	} {
		t.Run(q, func(t *testing.T) {
			pkgs := skillsShCatalogMatches(catalog, q, "skills-sh")
			if len(pkgs) != 1 || pkgs[0].Slug != "acme/options/options-strategy-advisor" {
				t.Fatalf("pkgs = %+v", pkgs)
			}
		})
	}
}

func TestSkillsShCatalogMatchesFullIDField(t *testing.T) {
	catalog := `"id":"acme/options/options-strategy-advisor","name":"Options Strategy Advisor","installs":42`
	pkgs := skillsShCatalogMatches(catalog, "options-strategy-advisor", "skills-sh")
	if len(pkgs) != 1 || pkgs[0].Slug != "acme/options/options-strategy-advisor" {
		t.Fatalf("pkgs = %+v", pkgs)
	}
}

func TestSkillsShCatalogMatchesToleratesFieldOrder(t *testing.T) {
	catalog := `"name":"Options Strategy Advisor","installs":42,"skillId":"options-strategy-advisor","source":"acme/options"`
	pkgs := skillsShCatalogMatches(catalog, "options-strategy-advisor", "skills-sh")
	if len(pkgs) != 1 || pkgs[0].Slug != "acme/options/options-strategy-advisor" {
		t.Fatalf("pkgs = %+v", pkgs)
	}
}

func TestSkillsShSearchQueriesAddsSlugAndReadableForms(t *testing.T) {
	got := strings.Join(skillsShSearchQueries("acme/options/options-strategy-advisor"), "|")
	for _, want := range []string{
		"acme/options/options-strategy-advisor",
		"acme options options strategy advisor",
		"acme-options-options-strategy-advisor",
		"options-strategy-advisor",
		"options strategy advisor",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("queries = %q, missing %q", got, want)
		}
	}
}

func TestSkillsShSearchContinuesAfterFirstVariantFailure(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/skills/search", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") == "options strategy advisor" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{
					"id":       "acme/options/options-strategy-advisor",
					"name":     "Options Strategy Advisor",
					"source":   "acme/options",
					"installs": 12,
				}},
			})
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"authentication_required","message":"token required"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	p := newTestSkillsShProvider(t, srv.URL)
	pkgs, err := p.Search(context.Background(), "options-strategy-advisor")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(pkgs) != 1 || pkgs[0].Slug != "acme/options/options-strategy-advisor" {
		t.Fatalf("pkgs = %+v", pkgs)
	}
}

func TestSkillsSh_ResolveKnownAndUnknown(t *testing.T) {
	srv := fakeSkillsSh(t)
	p := newTestSkillsShProvider(t, srv.URL)

	pkg, err := p.Resolve(context.Background(), "acme/skills/web-audit")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if pkg.Slug != "acme/skills/web-audit" || pkg.Version == "" {
		t.Errorf("pkg = %+v", pkg)
	}
	// Partner audits surface in the description for the consent prompt.
	if !strings.Contains(pkg.Description, "Gen Agent Trust Hub") || !strings.Contains(pkg.Description, "warn") {
		t.Errorf("audits missing from description: %q", pkg.Description)
	}

	_, err = p.Resolve(context.Background(), "nobody/nothing/ghost")
	if !errors.Is(err, sdkpkg.ErrNotFound) {
		t.Errorf("unknown slug: err = %v, want ErrNotFound", err)
	}
}

func TestSkillsSh_FetchWritesFiles(t *testing.T) {
	srv := fakeSkillsSh(t)
	p := newTestSkillsShProvider(t, srv.URL)
	pkg, err := p.Resolve(context.Background(), "acme/skills/web-audit")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	dst := t.TempDir()
	if err := p.Fetch(context.Background(), pkg, dst); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dst, "SKILL.md"))
	if err != nil || !strings.Contains(string(b), "Web Audit") {
		t.Errorf("SKILL.md: %v %q", err, b)
	}
	if _, err := os.Stat(filepath.Join(dst, "scripts", "run.py")); err != nil {
		t.Errorf("nested file missing: %v", err)
	}
}

func TestSkillsSh_FetchRefusesTraversal(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/skills/", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "evil/skills/x", "hash": "h",
			"files": []map[string]string{
				{"path": "../escape.txt", "contents": "boom"},
			},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	p := newTestSkillsShProvider(t, srv.URL)
	err := p.Fetch(context.Background(), sdkpkg.Package{Slug: "evil/skills/x", Source: "evil/skills/x"}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("expected traversal refusal, got %v", err)
	}
}

func TestSkillsSh_RequiresBaseURLDefault(t *testing.T) {
	p, err := newSkillsShProvider(map[string]any{"id": "s"})
	if err != nil {
		t.Fatalf("default base_url should apply: %v", err)
	}
	if !strings.Contains(p.baseURL, "skills.sh") {
		t.Errorf("default base = %q", p.baseURL)
	}
}

// fakeAuthGatedSkillsSh mimics skills.sh since 2026-10: search, detail and
// leaderboard answer 401 without a Vercel OIDC token; audits stay public.
func fakeAuthGatedSkillsSh(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/skills/audit/", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"audits": []map[string]any{{"provider": "Socket", "status": "pass"}},
		})
	})
	mux.HandleFunc("/api/v1/skills/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"authentication_required","message":"This endpoint requires authentication."}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// gitSkillRepo creates <root>/acme/skills.git holding skills/web-audit.
func gitSkillRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "acme", "skills.git")
	if err := os.MkdirAll(filepath.Join(repo, "skills", "web-audit"), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-b", "main")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test")
	for path, body := range map[string]string{
		"skills/web-audit/SKILL.md": "---\nname: web-audit\n---\nAudit a site.",
		"skills/other/SKILL.md":     "---\nname: other\n---\nSomething else.",
		"README.md":                 "repo readme",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(repo, path)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, path), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-m", "fixture")
	return root
}

func TestSkillsSh_ResolveAndFetchFallBackToGitWithoutToken(t *testing.T) {
	srv := fakeAuthGatedSkillsSh(t)
	p, err := newSkillsShProvider(map[string]any{"id": "skills-sh", "base_url": srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	p.gitBase = "file://" + gitSkillRepo(t)

	pkg, err := p.Resolve(context.Background(), "acme/skills/web-audit")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if pkg.Source != p.gitBase+"/acme/skills.git" || pkg.Version != "HEAD" {
		t.Fatalf("pkg = %+v, want git fallback source", pkg)
	}
	if !strings.Contains(pkg.Description, "Vercel OIDC") || !strings.Contains(pkg.Description, "Socket: pass") {
		t.Errorf("description = %q, want auth note + public audits", pkg.Description)
	}

	dst := filepath.Join(t.TempDir(), "installed")
	if err := p.Fetch(context.Background(), pkg, dst); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dst, "SKILL.md"))
	if err != nil || !strings.Contains(string(b), "Audit a site") {
		t.Fatalf("SKILL.md: %v %q", err, b)
	}
	if _, err := os.Stat(filepath.Join(dst, "README.md")); !os.IsNotExist(err) {
		t.Errorf("repository root leaked into the skill: %v", err)
	}
}

func TestSkillsSh_FetchViaGitReportsMissingSkill(t *testing.T) {
	p, err := newSkillsShProvider(map[string]any{"id": "skills-sh"})
	if err != nil {
		t.Fatal(err)
	}
	p.gitBase = "file://" + gitSkillRepo(t)
	pkg := sdkpkg.Package{Slug: "acme/skills/ghost", Source: p.gitBase + "/acme/skills.git"}
	err = p.Fetch(context.Background(), pkg, filepath.Join(t.TempDir(), "x"))
	if err == nil || !strings.Contains(err.Error(), `no SKILL.md for "ghost"`) {
		t.Fatalf("err = %v, want missing-skill error", err)
	}
}

func TestSkillsSh_ResolveWellKnownIDWithoutTokenExplainsAuth(t *testing.T) {
	srv := fakeAuthGatedSkillsSh(t)
	p := newTestSkillsShProvider(t, srv.URL)
	_, err := p.Resolve(context.Background(), "example.com/some-skill")
	if err == nil || !strings.Contains(err.Error(), "VERCEL_OIDC_TOKEN") || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("err = %v, want 401 plus actionable hint", err)
	}
	if errors.Is(err, sdkpkg.ErrNotFound) {
		t.Errorf("auth failure must not look like an unknown slug")
	}
}

func TestSkillsSh_SearchAuthFailureIncludesHint(t *testing.T) {
	srv := fakeAuthGatedSkillsSh(t)
	p := newTestSkillsShProvider(t, srv.URL)
	_, err := p.Search(context.Background(), "audit")
	if err == nil || !strings.Contains(err.Error(), "skills.sh/docs/api#authentication") {
		t.Fatalf("err = %v, want auth hint", err)
	}
}

func TestSkillsShGitHubID(t *testing.T) {
	for slug, want := range map[string]bool{
		"vercel-labs/skills/find-skills": true,
		"example.com/skill":              false,
		"a/b":                            false,
		"a/../c":                         false,
		"-x/repo/skill":                  false,
		"docs.example.com/team/skill":    false,
	} {
		if _, _, _, ok := skillsShGitHubID(slug); ok != want {
			t.Errorf("skillsShGitHubID(%q) ok = %v, want %v", slug, ok, want)
		}
	}
}

func TestFindSkillDir(t *testing.T) {
	write := func(root, path, body string) {
		t.Helper()
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("frontmatter name", func(t *testing.T) {
		root := t.TempDir()
		write(root, "skills/dir-a/SKILL.md", "---\nname: \"pretty-name\"\n---\n")
		write(root, "skills/dir-b/SKILL.md", "---\nname: other\n---\n")
		got, err := findSkillDir(root, "pretty-name")
		if err != nil || got != filepath.Join(root, "skills", "dir-a") {
			t.Fatalf("got %q, %v", got, err)
		}
	})
	t.Run("shallowest match wins", func(t *testing.T) {
		root := t.TempDir()
		write(root, "examples/nested/find-skills/SKILL.md", "x")
		write(root, "skills/find-skills/SKILL.md", "x")
		write(root, "find-skills/SKILL.md", "x")
		got, err := findSkillDir(root, "find-skills")
		if err != nil || got != filepath.Join(root, "find-skills") {
			t.Fatalf("got %q, %v", got, err)
		}
	})
	t.Run("single skill repo", func(t *testing.T) {
		root := t.TempDir()
		write(root, "SKILL.md", "---\nname: Something\n---\n")
		got, err := findSkillDir(root, "unrelated-id")
		if err != nil || got != root {
			t.Fatalf("got %q, %v", got, err)
		}
	})
}

func TestSkillsShCatalogEntriesUsesEnclosingObject(t *testing.T) {
	// Real skills.sh pages pack many entries together; a neighbouring
	// entry's source must not be attributed to the next skill.
	catalog := `[{"source":"vercel-labs/skills","skillId":"find-skills","name":"find-skills","installs":4},` +
		`{"source":"mattpocock/skills","skillId":"grill-me","name":"grill-me","installs":1}]`
	pkgs := skillsShCatalogEntries(catalog, "s", 10, func(string) bool { return true })
	var got []string
	for _, p := range pkgs {
		got = append(got, p.Slug)
	}
	if strings.Join(got, ",") != "vercel-labs/skills/find-skills,mattpocock/skills/grill-me" {
		t.Fatalf("slugs = %v", got)
	}
}

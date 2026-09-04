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

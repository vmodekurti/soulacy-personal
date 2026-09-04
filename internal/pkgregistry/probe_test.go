package pkgregistry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProbe_DetectsSkillsShDirectory(t *testing.T) {
	srv := fakeSkillsSh(t)
	rep, err := Probe(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if rep.Kind != SourceKindSkillsSh {
		t.Fatalf("kind = %q, want %q (report: %+v)", rep.Kind, SourceKindSkillsSh, rep)
	}
	if rep.Suggested == nil || rep.Suggested.Type != "skillssh" || rep.Suggested.BaseURL != srv.URL {
		t.Errorf("suggested = %+v", rep.Suggested)
	}
	if len(rep.Samples) == 0 || !strings.Contains(rep.Samples[0], "web-audit") {
		t.Errorf("samples = %v", rep.Samples)
	}
}

func TestProbe_DetectsE19HTTPRegistry(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/search", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"packages": []map[string]any{
				{"slug": "self-improving-agent", "version": "1.2.0"},
			},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	rep, err := Probe(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if rep.Kind != SourceKindHTTP {
		t.Fatalf("kind = %q, want %q", rep.Kind, SourceKindHTTP)
	}
	if rep.Suggested == nil || rep.Suggested.Type != "http" {
		t.Errorf("suggested = %+v", rep.Suggested)
	}
	if len(rep.Samples) == 0 || !strings.Contains(rep.Samples[0], "self-improving-agent") {
		t.Errorf("samples = %v", rep.Samples)
	}
}

func TestProbe_DetectsGitHosts(t *testing.T) {
	for _, u := range []string{
		"https://github.com/acme/skills",
		"https://gitlab.com/acme/skills",
		"https://example.com/repos/thing.git",
	} {
		rep, err := Probe(context.Background(), u)
		if err != nil {
			t.Fatalf("Probe(%s): %v", u, err)
		}
		if rep.Kind != SourceKindGit {
			t.Errorf("%s: kind = %q, want git", u, rep.Kind)
		}
		if rep.Suggested == nil || rep.Suggested.Type != "git" {
			t.Errorf("%s: suggested = %+v", u, rep.Suggested)
		}
		if !strings.Contains(rep.Detail, "sy skill install") {
			t.Errorf("%s: detail should guide direct install: %q", u, rep.Detail)
		}
	}
}

func TestProbe_UnknownPageReportsRepoHints(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body>
			<a href="https://github.com/acme/cool-skills">Cool skills</a>
			<a href="https://github.com/other/agent-skills">More</a>
			<a href="https://github.com/acme/cool-skills">dup</a>
		</body></html>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	rep, err := Probe(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if rep.Kind != SourceKindUnknown {
		t.Fatalf("kind = %q, want unknown", rep.Kind)
	}
	if len(rep.Samples) != 2 {
		t.Errorf("samples = %v, want 2 deduped repos", rep.Samples)
	}
	if rep.Suggested != nil {
		t.Errorf("unknown pages must not auto-suggest a registry: %+v", rep.Suggested)
	}
}

func TestProbe_RefusesNonHTTPSchemes(t *testing.T) {
	for _, u := range []string{"ftp://x.example", "file:///etc/passwd", "not a url at all ::"} {
		if _, err := Probe(context.Background(), u); err == nil {
			t.Errorf("%s: expected error", u)
		}
	}
	// Scheme-less input defaults to https and parses.
	if _, err := normalizeProbeURL("github.com/acme/skills"); err != nil {
		t.Errorf("scheme-less: %v", err)
	}
}

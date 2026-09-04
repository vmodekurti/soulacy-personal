package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/pkg/skill"
	"gopkg.in/yaml.v3"
)

// Story E26: skill-source review + management endpoints.

func seedRegistriesConfig(t *testing.T) (string, *Server) {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	seed := `
server:
  port: 18789
registries:
  - id: main
    type: http
    base_url: https://registry.example.com
    priority: 10
    auth_headers:
      Authorization: "Bearer sk_secret"
`
	if err := os.WriteFile(cfgPath, []byte(seed), 0600); err != nil {
		t.Fatal(err)
	}
	return cfgPath, newTestGatewayWithCfgPath(t, "secret", cfgPath)
}

func TestListRegistries_RedactsAuthHeaders(t *testing.T) {
	_, s := seedRegistriesConfig(t)
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/registries", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d %v", status, body)
	}
	regs := body["registries"].([]any)
	if len(regs) != 1 {
		t.Fatalf("registries = %v", regs)
	}
	r := regs[0].(map[string]any)
	if r["id"] != "main" || r["type"] != "http" || r["has_auth"] != true {
		t.Errorf("view = %v", r)
	}
	raw, _ := json.Marshal(body)
	if strings.Contains(string(raw), "sk_secret") {
		t.Error("auth header value leaked into the listing")
	}
}

func TestProbeRegistry_EndToEnd(t *testing.T) {
	// A fake skills.sh-style directory the gateway probes server-side.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/skills/search", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"acme/skills/web-audit","slug":"web-audit","name":"Web Audit","source":"acme/skills"}]}`))
	})
	dir := httptest.NewServer(mux)
	defer dir.Close()

	_, s := seedRegistriesConfig(t)
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/registries/probe", "secret",
		`{"url":"`+dir.URL+`"}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d %v", status, body)
	}
	if body["kind"] != "skillssh" {
		t.Errorf("kind = %v", body["kind"])
	}
	sug := body["suggested"].(map[string]any)
	if sug["Type"] != "skillssh" && sug["type"] != "skillssh" {
		t.Errorf("suggested = %v", sug)
	}

	// Garbage input → 400.
	status, _ = gatewayJSON(t, s, http.MethodPost, "/api/v1/registries/probe", "secret", `{"url":"ftp://nope"}`)
	if status != http.StatusBadRequest {
		t.Errorf("ftp probe status = %d, want 400", status)
	}
}

func TestSearchRegistries_ReturnsProviderWarnings(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/skills/search", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"authentication_required","message":"token required"}`))
	})
	dir := httptest.NewServer(mux)
	defer dir.Close()

	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	seed := `
server:
  port: 18789
registries:
  - id: test-skills
    type: skillssh
    base_url: ` + dir.URL + `
    priority: 10
`
	if err := os.WriteFile(cfgPath, []byte(seed), 0600); err != nil {
		t.Fatal(err)
	}
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/registries/search?q=audit", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d %v", status, body)
	}
	if body["count"].(float64) != 0 {
		t.Fatalf("count = %v, want 0", body["count"])
	}
	warnings, ok := body["warnings"].([]any)
	if !ok || len(warnings) != 1 {
		t.Fatalf("warnings = %#v", body["warnings"])
	}
	if !strings.Contains(warnings[0].(string), "authentication_required: token required") {
		t.Fatalf("warning = %q", warnings[0])
	}
	if body["status"] != "degraded" || body["auth_required"] != true {
		t.Fatalf("expected degraded auth-required response, body=%v", body)
	}
	checked, ok := body["checked"].([]any)
	if !ok || len(checked) != 2 || checked[0] != "test-skills" || checked[1] != "git" {
		t.Fatalf("checked providers = %#v", body["checked"])
	}
	suggestions, ok := body["suggestions"].([]any)
	if !ok || len(suggestions) == 0 {
		t.Fatalf("expected auth suggestions, body=%v", body)
	}
}

func TestSearchRegistries_KeepsGitFallbackWithConfiguredSources(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/skills/search", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"authentication_required","message":"token required"}`))
	})
	dir := httptest.NewServer(mux)
	defer dir.Close()

	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	seed := `
server:
  port: 18789
registries:
  - id: private-skills
    type: skillssh
    base_url: ` + dir.URL + `
    priority: 10
`
	if err := os.WriteFile(cfgPath, []byte(seed), 0600); err != nil {
		t.Fatal(err)
	}
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/registries/search?q=https%3A%2F%2Fgithub.com%2Facme%2Fskills", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d %v", status, body)
	}
	if body["count"].(float64) != 1 {
		t.Fatalf("count = %v, body = %v", body["count"], body)
	}
	pkgs := body["packages"].([]any)
	pkg := pkgs[0].(map[string]any)
	if pkg["slug"] != "https://github.com/acme/skills" || pkg["provider"] != "git" || pkg["version"] != "HEAD" {
		t.Fatalf("git fallback package = %v", pkg)
	}
	checked := body["checked"].([]any)
	if len(checked) != 2 || checked[0] != "private-skills" || checked[1] != "git" {
		t.Fatalf("checked providers = %#v", checked)
	}
}

func TestSearchRegistries_UsesConfiguredAuthHeaders(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/skills/search", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer sk_secret" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"authentication_required","message":"bad token"}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"acme/options/strategy-advisor","slug":"options-strategy-advisor","name":"Options Strategy Advisor","description":"Options strategy guidance","source":"acme/options"}]}`))
	})
	dir := httptest.NewServer(mux)
	defer dir.Close()

	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	seed := `
server:
  port: 18789
registries:
  - id: authed-skills
    type: skillssh
    base_url: ` + dir.URL + `
    priority: 10
    auth_headers:
      Authorization: "Bearer sk_secret"
`
	if err := os.WriteFile(cfgPath, []byte(seed), 0600); err != nil {
		t.Fatal(err)
	}
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/registries/search?q=options-strategy-advisor", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d %v", status, body)
	}
	if body["count"].(float64) != 1 {
		t.Fatalf("count = %v, body = %v", body["count"], body)
	}
	if warnings, ok := body["warnings"].([]any); !ok || len(warnings) != 0 {
		t.Fatalf("warnings = %#v", body["warnings"])
	}
	if body["status"] != "ok" || body["auth_required"] != false {
		t.Fatalf("expected ok/non-auth response, body=%v", body)
	}
}

func TestSearchRegistries_IncludesInstalledLocalSkillMatches(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/skills/search", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"authentication_required","message":"token required"}`))
	})
	dir := httptest.NewServer(mux)
	defer dir.Close()

	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	seed := `
server:
  port: 18789
registries:
  - id: remote-skills
    type: skillssh
    base_url: ` + dir.URL + `
    priority: 10
`
	if err := os.WriteFile(cfgPath, []byte(seed), 0600); err != nil {
		t.Fatal(err)
	}
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	s.skillLoader = &rescanSkillLoader{skills: []*skill.Skill{{
		Name:        "options-strategy-advisor",
		Description: "Options strategy guidance and risk framing",
		Path:        "/tmp/options-strategy-advisor/SKILL.md",
	}}}

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/registries/search?q=options-strategy-advisor", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d %v", status, body)
	}
	if body["count"].(float64) != 1 {
		t.Fatalf("count = %v, body = %v", body["count"], body)
	}
	pkgs := body["packages"].([]any)
	pkg := pkgs[0].(map[string]any)
	if pkg["slug"] != "options-strategy-advisor" || pkg["provider"] != "local" || pkg["version"] != "installed" {
		t.Fatalf("local package = %v", pkg)
	}
	warnings, ok := body["warnings"].([]any)
	if !ok || len(warnings) != 1 {
		t.Fatalf("warnings = %#v", body["warnings"])
	}
}

func TestInstallRegistrySkill_BlocksUnverifiedByDefault(t *testing.T) {
	workspace := t.TempDir()
	t.Setenv("SOULACY_WORKSPACE", workspace)
	dir := fakeInstallableSkillsSh(t)
	s := testRegistryInstallGateway(t, dir.URL)
	s.skillLoader = &rescanSkillLoader{}

	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/skills/install", "secret",
		`{"slug":"acme/skills/web-audit"}`)
	if status != http.StatusConflict {
		t.Fatalf("status = %d body=%v", status, body)
	}
	if body["code"] != "unverified_package" {
		t.Fatalf("code = %v body=%v", body["code"], body)
	}
	if _, err := os.Stat(filepath.Join(workspace, "skills", "web-audit", "SKILL.md")); !os.IsNotExist(err) {
		t.Fatalf("skill should not have been installed, stat err=%v", err)
	}
}

func TestInstallRegistrySkill_AllowUnverifiedInstallsAndRescans(t *testing.T) {
	workspace := t.TempDir()
	t.Setenv("SOULACY_WORKSPACE", workspace)
	dir := fakeInstallableSkillsSh(t)
	s := testRegistryInstallGateway(t, dir.URL)
	loader := &rescanSkillLoader{}
	s.skillLoader = loader

	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/skills/install", "secret",
		`{"slug":"acme/skills/web-audit","allow_unverified":true}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d body=%v", status, body)
	}
	if body["ok"] != true || body["name"] != "web-audit" || body["verified"] != false {
		t.Fatalf("unexpected install body=%v", body)
	}
	if loader.scanned != 1 {
		t.Fatalf("Scan called %d times, want 1", loader.scanned)
	}
	if _, err := os.Stat(filepath.Join(workspace, "skills", "web-audit", "SKILL.md")); err != nil {
		t.Fatalf("installed skill missing: %v", err)
	}
}

func fakeInstallableSkillsSh(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/skills/acme/skills/web-audit", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"id":"acme/skills/web-audit",
			"source":"acme/skills",
			"slug":"web-audit",
			"installs":12,
			"hash":"abcdef1234567890",
			"files":[
				{"path":"SKILL.md","contents":"---\nname: Web Audit\ndescription: Audit a website.\n---\n\n# Web Audit\n\nAudit a website for product readiness.\n"}
			]
		}`))
	})
	mux.HandleFunc("/api/v1/skills/audit/acme/skills/web-audit", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"audits":[]}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func testRegistryInstallGateway(t *testing.T, baseURL string) *Server {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	seed := `
server:
  port: 18789
registries:
  - id: test-skills
    type: skillssh
    base_url: ` + baseURL + `
    priority: 10
`
	if err := os.WriteFile(cfgPath, []byte(seed), 0600); err != nil {
		t.Fatal(err)
	}
	return newTestGatewayWithCfgPath(t, "secret", cfgPath)
}

func TestAddRegistry_AppendsAndValidates(t *testing.T) {
	cfgPath, s := seedRegistriesConfig(t)

	// Unknown type refused.
	status, _ := gatewayJSON(t, s, http.MethodPost, "/api/v1/registries", "secret",
		`{"id":"x","type":"warp","base_url":"https://x.example"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("unknown type status = %d", status)
	}

	// Missing base_url for non-git refused.
	status, _ = gatewayJSON(t, s, http.MethodPost, "/api/v1/registries", "secret",
		`{"id":"x","type":"skillssh"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("missing base_url status = %d", status)
	}

	// Duplicate id refused.
	status, _ = gatewayJSON(t, s, http.MethodPost, "/api/v1/registries", "secret",
		`{"id":"main","type":"http","base_url":"https://x.example"}`)
	if status != http.StatusConflict {
		t.Fatalf("dup id status = %d", status)
	}

	// Valid add persists, preserving the existing entry + auth headers.
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/registries", "secret",
		`{"id":"skills.sh","type":"skillssh","base_url":"https://skills.sh/","priority":50,"auth_headers":{"Authorization":"Bearer oidc_token"}}`)
	if status != http.StatusOK {
		t.Fatalf("add status = %d %v", status, body)
	}

	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var disk map[string]any
	if err := yaml.Unmarshal(raw, &disk); err != nil {
		t.Fatal(err)
	}
	regs := disk["registries"].([]any)
	if len(regs) != 2 {
		t.Fatalf("disk registries = %v", regs)
	}
	first := regs[0].(map[string]any)
	if ah, _ := first["auth_headers"].(map[string]any); ah == nil || ah["Authorization"] != "Bearer sk_secret" {
		t.Errorf("existing auth headers lost: %v", first)
	}
	added := regs[1].(map[string]any)
	if added["type"] != "skillssh" || added["base_url"] != "https://skills.sh" {
		t.Errorf("added = %v (trailing slash should be trimmed)", added)
	}
	if ah, _ := added["auth_headers"].(map[string]any); ah == nil || ah["Authorization"] != "Bearer oidc_token" {
		t.Errorf("added auth headers missing: %v", added)
	}
	// Server section untouched.
	if srv, _ := disk["server"].(map[string]any); srv == nil || srv["port"] != 18789 {
		t.Errorf("server block mutated: %v", disk["server"])
	}
}

// Regression for the whatsapp_web pair handler on FRESH configs:
// fmt.Sprint(nil) == "<nil>" defeated every empty-check, so defaults were
// skipped and EnsureSidecarScript got an empty dir ("mkdir : no such file
// or directory" in the GUI). cfgMapStr treats missing keys as empty.
func TestCfgMapStr_NilSafety(t *testing.T) {
	m := map[string]any{"set": " value ", "wrongtype": 42}
	if got := cfgMapStr(m, "missing"); got != "" {
		t.Errorf("missing key = %q, want empty", got)
	}
	if got := cfgMapStr(m, "wrongtype"); got != "" {
		t.Errorf("non-string = %q, want empty", got)
	}
	if got := cfgMapStr(m, "set"); got != "value" {
		t.Errorf("set = %q", got)
	}
}

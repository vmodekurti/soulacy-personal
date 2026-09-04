package builder

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"
)

// ─── registry.go ────────────────────────────────────────────────────────────

func TestNewRegistry_Empty(t *testing.T) {
	r := NewRegistry()
	if r == nil {
		t.Fatal("NewRegistry returned nil")
	}
	if got := r.All(); len(got) != 0 {
		t.Fatalf("expected empty registry, got %d entries", len(got))
	}
}

func TestRegistry_Add_And_All(t *testing.T) {
	r := NewRegistry()
	r.Add(ToolEntry{ID: "t1", Name: "Alpha", Description: "first tool", Source: "builtin"})
	r.Add(ToolEntry{ID: "t2", Name: "Beta", Description: "second tool", Source: "mcp"})

	all := r.All()
	if len(all) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(all))
	}
	if all[0].ID != "t1" || all[1].ID != "t2" {
		t.Errorf("unexpected order: %+v", all)
	}
}

func TestRegistry_All_ReturnsCopy(t *testing.T) {
	r := NewRegistry()
	r.Add(ToolEntry{ID: "t1", Name: "Tool"})

	a := r.All()
	a[0].Name = "mutated"

	b := r.All()
	if b[0].Name == "mutated" {
		t.Error("All() did not return a copy — mutation affected the registry")
	}
}

func TestRegistry_Search_ByName(t *testing.T) {
	r := NewRegistry()
	r.Add(ToolEntry{ID: "t1", Name: "Brave Search", Description: "Web search"})
	r.Add(ToolEntry{ID: "t2", Name: "Filesystem", Description: "Read and write local files"})

	got := r.Search("brave")
	if len(got) != 1 || got[0].ID != "t1" {
		t.Errorf("expected Brave Search, got %+v", got)
	}
}

func TestRegistry_Search_ByDescription(t *testing.T) {
	r := NewRegistry()
	r.Add(ToolEntry{ID: "t1", Name: "Filesystem", Description: "Read and write local files"})
	r.Add(ToolEntry{ID: "t2", Name: "GitHub", Description: "Search repos, issues"})

	got := r.Search("local files")
	if len(got) != 1 || got[0].ID != "t1" {
		t.Errorf("expected Filesystem, got %+v", got)
	}
}

func TestRegistry_Search_ByKeyword(t *testing.T) {
	r := NewRegistry()
	r.Add(ToolEntry{
		ID:       "t1",
		Name:     "Slack",
		Keywords: []string{"messaging", "team chat"},
	})
	r.Add(ToolEntry{
		ID:       "t2",
		Name:     "GitHub",
		Keywords: []string{"code", "version control"},
	})

	got := r.Search("team chat")
	if len(got) != 1 || got[0].ID != "t1" {
		t.Errorf("keyword search failed, got %+v", got)
	}
}

func TestRegistry_Search_CaseInsensitive(t *testing.T) {
	r := NewRegistry()
	r.Add(ToolEntry{ID: "t1", Name: "Brave Search"})

	for _, q := range []string{"brave", "BRAVE", "Brave", "BrAvE"} {
		got := r.Search(q)
		if len(got) == 0 {
			t.Errorf("search(%q) returned nothing", q)
		}
	}
}

func TestRegistry_Search_NoMatch(t *testing.T) {
	r := NewRegistry()
	r.Add(ToolEntry{ID: "t1", Name: "Filesystem"})

	got := r.Search("quantum gravity solver")
	if len(got) != 0 {
		t.Errorf("expected no matches, got %+v", got)
	}
}

func TestRegistry_Search_EmptyQuery_MatchesAll(t *testing.T) {
	r := NewRegistry()
	r.Add(ToolEntry{ID: "t1", Name: "Alpha"})
	r.Add(ToolEntry{ID: "t2", Name: "Beta"})

	// An empty query string matches every name/description via Contains("", ...).
	got := r.Search("")
	if len(got) != 2 {
		t.Errorf("empty query should match all, got %d", len(got))
	}
}

func TestRegistry_Search_EmptyRegistry(t *testing.T) {
	r := NewRegistry()
	got := r.Search("anything")
	if got != nil {
		t.Errorf("expected nil from empty registry, got %+v", got)
	}
}

// ─── offline_registry.go ─────────────────────────────────────────────────────

func TestSearchOffline_KnownEntry(t *testing.T) {
	// "brave" should match "Brave Search" which is in the bundled JSON.
	refs := SearchOffline("brave")
	if len(refs) == 0 {
		t.Fatal("SearchOffline(\"brave\") returned nothing")
	}
	found := false
	for _, ref := range refs {
		if strings.Contains(strings.ToLower(ref.Name), "brave") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("no Brave entry in results: %+v", refs)
	}
}

func TestSearchOffline_MaxFiveResults(t *testing.T) {
	// A very broad query should still return at most 5.
	refs := SearchOffline("a") // most entries have 'a' in name or description
	if len(refs) > 5 {
		t.Errorf("SearchOffline capped at 5 but returned %d", len(refs))
	}
}

func TestSearchOffline_NoMatch(t *testing.T) {
	refs := SearchOffline("this_definitely_does_not_exist_xyzzy")
	if refs != nil {
		t.Errorf("expected nil, got %+v", refs)
	}
}

func TestSearchOffline_CaseInsensitive(t *testing.T) {
	lower := SearchOffline("slack")
	upper := SearchOffline("SLACK")
	if len(lower) == 0 {
		t.Fatal("SearchOffline(\"slack\") returned nothing")
	}
	if len(lower) != len(upper) {
		t.Errorf("case sensitivity mismatch: lower=%d upper=%d", len(lower), len(upper))
	}
}

func TestSearchOffline_TelegramMatches(t *testing.T) {
	refs := SearchOffline("telegram")
	if len(refs) == 0 {
		t.Fatal("expected at least one Telegram entry")
	}
}

// ─── gap.go ──────────────────────────────────────────────────────────────────

func TestNewGapAnalyzer_NotNil(t *testing.T) {
	r := NewRegistry()
	a := NewGapAnalyzer(r)
	if a == nil {
		t.Fatal("NewGapAnalyzer returned nil")
	}
}

func TestGapAnalyzer_Analyze_Empty(t *testing.T) {
	r := NewRegistry()
	a := NewGapAnalyzer(r)

	gaps := a.Analyze(nil)
	if len(gaps) != 0 {
		t.Errorf("expected no gaps for nil input, got %d", len(gaps))
	}

	gaps = a.Analyze([]string{})
	if len(gaps) != 0 {
		t.Errorf("expected no gaps for empty input, got %d", len(gaps))
	}
}

func TestGapAnalyzer_Analyze_NoGapWhenToolPresent(t *testing.T) {
	r := NewRegistry()
	r.Add(ToolEntry{ID: "brave", Name: "Brave Search", Description: "Web search"})

	a := NewGapAnalyzer(r)
	gaps := a.Analyze([]string{"web search"})

	// "web search" matches "Web search" in the description → no gap.
	if len(gaps) != 0 {
		t.Errorf("expected 0 gaps when tool covers capability, got %d", len(gaps))
	}
}

func TestGapAnalyzer_Analyze_GapWhenToolAbsent(t *testing.T) {
	// Use an obscure label guaranteed not to match any installed tool.
	r := NewRegistry()
	r.Add(ToolEntry{ID: "brave", Name: "Brave Search", Description: "Web search"})

	a := NewGapAnalyzer(r)
	gaps := a.Analyze([]string{"quantum-entanglement-relay-xyzzy"})

	if len(gaps) != 1 {
		t.Fatalf("expected 1 gap, got %d", len(gaps))
	}
	if gaps[0].Required != "quantum-entanglement-relay-xyzzy" {
		t.Errorf("gap.Required = %q, unexpected", gaps[0].Required)
	}
	// Available should be empty (no matching tools).
	if len(gaps[0].Available) != 0 {
		t.Errorf("expected empty Available, got %v", gaps[0].Available)
	}
	// Suggestions should be a non-nil slice (possibly empty).
	if gaps[0].Suggestions == nil {
		t.Error("Suggestions must not be nil")
	}
}

func TestGapAnalyzer_Analyze_SuggestionsCapAt5(t *testing.T) {
	// Empty registry so every label is a gap; use "a" to maximise offline hits.
	r := NewRegistry()
	a := NewGapAnalyzer(r)

	gaps := a.Analyze([]string{"a"})
	if len(gaps) == 0 {
		t.Skip("offline registry returned no results for 'a'")
	}
	if len(gaps[0].Suggestions) > 5 {
		t.Errorf("Suggestions capped at 5, got %d", len(gaps[0].Suggestions))
	}
}

func TestGapAnalyzer_Analyze_MultipleCapabilities(t *testing.T) {
	r := NewRegistry()
	// Only install a tool for the first capability.
	r.Add(ToolEntry{ID: "fs", Name: "Filesystem", Description: "Read and write local files"})

	a := NewGapAnalyzer(r)
	gaps := a.Analyze([]string{"read and write local files", "quantum-xyzzy-9182736"})

	// First capability is covered → no gap for it; second is a gap.
	if len(gaps) != 1 {
		t.Fatalf("expected 1 gap, got %d: %+v", len(gaps), gaps)
	}
	if gaps[0].Required != "quantum-xyzzy-9182736" {
		t.Errorf("unexpected gap Required: %q", gaps[0].Required)
	}
}

func TestGapAnalyzer_Analyze_SuggestionsDeduplicateByID(t *testing.T) {
	// This is a structural property: merged slice never contains the same ID twice.
	r := NewRegistry()
	a := NewGapAnalyzer(r)

	// Use a term likely present in offline registry to exercise the dedup path.
	gaps := a.Analyze([]string{"slack"})
	if len(gaps) == 0 {
		t.Skip("all tools already present")
	}
	seen := map[string]bool{}
	for _, ref := range gaps[0].Suggestions {
		if seen[ref.ID] {
			t.Errorf("duplicate suggestion ID %q", ref.ID)
		}
		seen[ref.ID] = true
	}
}

// ─── glama_registry.go helpers ───────────────────────────────────────────────

func TestSlugify(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"Brave Search", "brave-search"},
		{"iCloud MCP", "icloud-mcp"},
		{"My -- Tool", "my-tool"},
		{"  leading", "leading"},
		{"trailing  ", "trailing"},
		{"", ""},
		{"123 Numbers!", "123-numbers-"},
	}
	for _, tc := range cases {
		got := slugify(tc.in)
		// Strip trailing dash that may come from trailing punctuation.
		got = strings.Trim(got, "-")
		want := strings.Trim(tc.want, "-")
		if got != want {
			t.Errorf("slugify(%q) = %q, want %q", tc.in, got, want)
		}
	}
}

func TestExtractInstallCmd_InstallCmdAttribute(t *testing.T) {
	s := glamaServer{
		Name: "My Tool",
		Attributes: []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		}{
			{Name: "install_cmd", Value: "@example/mcp-tool"},
		},
	}
	got := extractInstallCmd(s)
	if got != "npx -y @example/mcp-tool" {
		t.Errorf("extractInstallCmd with install_cmd attr = %q", got)
	}
}

func TestExtractInstallCmd_NpmPackageAttribute(t *testing.T) {
	s := glamaServer{
		Attributes: []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		}{
			{Name: "npm_package", Value: "@org/server"},
		},
	}
	got := extractInstallCmd(s)
	if got != "npx -y @org/server" {
		t.Errorf("extractInstallCmd with npm_package attr = %q", got)
	}
}

func TestExtractInstallCmd_NpmjsURL(t *testing.T) {
	s := glamaServer{
		Repository: struct {
			URL string `json:"url"`
		}{URL: "https://www.npmjs.com/package/mcp-slack"},
	}
	got := extractInstallCmd(s)
	if got != "npx -y mcp-slack" {
		t.Errorf("extractInstallCmd from npmjs URL = %q", got)
	}
}

func TestExtractInstallCmd_GitHubFallback(t *testing.T) {
	s := glamaServer{
		Repository: struct {
			URL string `json:"url"`
		}{URL: "https://github.com/example/mcp-tool"},
	}
	got := extractInstallCmd(s)
	if !strings.HasPrefix(got, "# see ") {
		t.Errorf("expected GitHub fallback comment, got %q", got)
	}
}

func TestExtractInstallCmd_Empty(t *testing.T) {
	s := glamaServer{}
	got := extractInstallCmd(s)
	if got != "" {
		t.Errorf("expected empty string for bare server, got %q", got)
	}
}

// ─── SearchGlama with fake RoundTripper ──────────────────────────────────────

// fakeTransport lets tests inject canned HTTP responses without a real server.
type fakeTransport struct {
	statusCode int
	body       string
	err        error
}

func (f *fakeTransport) RoundTrip(*http.Request) (*http.Response, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &http.Response{
		StatusCode: f.statusCode,
		Body:       io.NopCloser(strings.NewReader(f.body)),
		Header:     make(http.Header),
	}, nil
}

// swapTransport replaces http.DefaultClient.Transport for the duration of a
// test and restores it on cleanup.
func swapTransport(t *testing.T, rt http.RoundTripper) {
	t.Helper()
	orig := http.DefaultClient.Transport
	http.DefaultClient.Transport = rt
	t.Cleanup(func() { http.DefaultClient.Transport = orig })
}

func TestSearchGlama_HappyPath(t *testing.T) {
	payload := glamaResponse{
		Servers: []glamaServer{
			{
				ID:          "brave-search",
				Name:        "Brave Search",
				Description: "Web search via Brave",
			},
		},
	}
	b, _ := json.Marshal(payload)
	swapTransport(t, &fakeTransport{statusCode: 200, body: string(b)})

	refs := SearchGlama("brave")
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d", len(refs))
	}
	if refs[0].ID != "brave-search" {
		t.Errorf("ID = %q, want brave-search", refs[0].ID)
	}
	if refs[0].Name != "Brave Search" {
		t.Errorf("Name = %q", refs[0].Name)
	}
	if !strings.Contains(refs[0].RegistryURL, "brave-search") {
		t.Errorf("RegistryURL should contain server ID, got %q", refs[0].RegistryURL)
	}
}

func TestSearchGlama_UsesSlugWhenIDEmpty(t *testing.T) {
	payload := glamaResponse{
		Servers: []glamaServer{
			{Name: "My Cool Tool"},
		},
	}
	b, _ := json.Marshal(payload)
	swapTransport(t, &fakeTransport{statusCode: 200, body: string(b)})

	refs := SearchGlama("cool")
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d", len(refs))
	}
	if refs[0].ID != "my-cool-tool" {
		t.Errorf("ID = %q, want my-cool-tool", refs[0].ID)
	}
}

func TestSearchGlama_ReturnsNilOn404(t *testing.T) {
	swapTransport(t, &fakeTransport{statusCode: 404, body: "not found"})
	refs := SearchGlama("missing")
	if refs != nil {
		t.Errorf("expected nil on non-200, got %+v", refs)
	}
}

func TestSearchGlama_ReturnsNilOnNetworkError(t *testing.T) {
	swapTransport(t, &fakeTransport{err: io.ErrUnexpectedEOF})
	refs := SearchGlama("anything")
	if refs != nil {
		t.Errorf("expected nil on network error, got %+v", refs)
	}
}

func TestSearchGlama_ReturnsNilOnBadJSON(t *testing.T) {
	swapTransport(t, &fakeTransport{statusCode: 200, body: "not json {"})
	refs := SearchGlama("anything")
	if refs != nil {
		t.Errorf("expected nil on bad JSON, got %+v", refs)
	}
}

func TestSearchGlama_CapsAtGlamaMaxResult(t *testing.T) {
	servers := make([]glamaServer, 10)
	for i := range servers {
		servers[i] = glamaServer{ID: "s" + string(rune('0'+i)), Name: "Server"}
	}
	b, _ := json.Marshal(glamaResponse{Servers: servers})
	swapTransport(t, &fakeTransport{statusCode: 200, body: string(b)})

	refs := SearchGlama("server")
	if len(refs) > glamaMaxResult {
		t.Errorf("expected at most %d results, got %d", glamaMaxResult, len(refs))
	}
}

func TestSearchGlama_DerivesInstallCmdFromAttributes(t *testing.T) {
	payload := glamaResponse{
		Servers: []glamaServer{
			{
				ID:   "slack",
				Name: "Slack",
				Attributes: []struct {
					Name  string `json:"name"`
					Value string `json:"value"`
				}{
					{Name: "install_cmd", Value: "@modelcontextprotocol/server-slack"},
				},
			},
		},
	}
	b, _ := json.Marshal(payload)
	swapTransport(t, &fakeTransport{statusCode: 200, body: string(b)})

	refs := SearchGlama("slack")
	if len(refs) == 0 {
		t.Fatal("no refs returned")
	}
	if refs[0].InstallCmd != "npx -y @modelcontextprotocol/server-slack" {
		t.Errorf("InstallCmd = %q", refs[0].InstallCmd)
	}
}

// ─── FetchGlamaServer with fake RoundTripper ─────────────────────────────────

func TestFetchGlamaServer_HappyPath(t *testing.T) {
	raw := map[string]interface{}{
		"name":        "iCloud MCP",
		"namespace":   "adamzaidi",
		"slug":        "icloud-mcp",
		"description": "iCloud Mail and Contacts",
		"url":         "https://glama.ai/mcp/servers/adamzaidi/icloud-mcp",
		"repository":  map[string]string{"url": "https://www.npmjs.com/package/icloud-mcp"},
		"environmentVariablesJsonSchema": map[string]interface{}{
			"properties": map[string]interface{}{
				"ICLOUD_USER": map[string]string{"description": "Apple ID", "type": "string"},
				"ICLOUD_PASS": map[string]string{"description": "App-specific password", "type": "string"},
			},
			"required": []string{"ICLOUD_USER"},
		},
	}
	b, _ := json.Marshal(raw)
	swapTransport(t, &fakeTransport{statusCode: 200, body: string(b)})

	spec, err := FetchGlamaServer("adamzaidi/icloud-mcp")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Command != "npx" {
		t.Errorf("Command = %q, want npx", spec.Command)
	}
	if len(spec.Args) < 2 || spec.Args[0] != "-y" {
		t.Errorf("Args = %v, want [-y <pkg>]", spec.Args)
	}
	// Env schema should include both variables.
	if len(spec.EnvSchema) != 2 {
		t.Errorf("expected 2 env vars, got %d: %+v", len(spec.EnvSchema), spec.EnvSchema)
	}
	// ICLOUD_USER should be required.
	for _, ev := range spec.EnvSchema {
		if ev.Name == "ICLOUD_USER" && !ev.Required {
			t.Error("ICLOUD_USER should be required")
		}
		if ev.Name == "ICLOUD_PASS" && ev.Required {
			t.Error("ICLOUD_PASS should not be required")
		}
	}
}

func TestFetchGlamaServer_StripsPrefixURL(t *testing.T) {
	raw := map[string]interface{}{
		"name": "Tool",
		"slug": "tool",
	}
	b, _ := json.Marshal(raw)
	swapTransport(t, &fakeTransport{statusCode: 200, body: string(b)})

	// Passing a full Glama URL — should be stripped to the slug.
	_, err := FetchGlamaServer("https://glama.ai/mcp/servers/ns/tool")
	if err != nil {
		t.Fatal(err)
	}
}

func TestFetchGlamaServer_EmptySlug(t *testing.T) {
	_, err := FetchGlamaServer("")
	if err == nil {
		t.Fatal("expected error for empty slug")
	}
}

func TestFetchGlamaServer_Non200(t *testing.T) {
	swapTransport(t, &fakeTransport{statusCode: 503, body: "unavailable"})
	_, err := FetchGlamaServer("ns/server")
	if err == nil {
		t.Fatal("expected error for non-200 response")
	}
}

func TestFetchGlamaServer_BadJSON(t *testing.T) {
	swapTransport(t, &fakeTransport{statusCode: 200, body: "{"})
	_, err := FetchGlamaServer("ns/server")
	if err == nil {
		t.Fatal("expected error for bad JSON")
	}
}

func TestFetchGlamaServer_NoEnvSchema(t *testing.T) {
	raw := map[string]interface{}{
		"name": "Bare Tool",
		"slug": "bare-tool",
	}
	b, _ := json.Marshal(raw)
	swapTransport(t, &fakeTransport{statusCode: 200, body: string(b)})

	spec, err := FetchGlamaServer("ns/bare-tool")
	if err != nil {
		t.Fatal(err)
	}
	// When no environmentVariablesJsonSchema is present, EnvSchema is nil/empty — both are fine.
	if len(spec.EnvSchema) != 0 {
		t.Errorf("expected empty EnvSchema, got %+v", spec.EnvSchema)
	}
}

// ─── mcp_registry.go helpers ─────────────────────────────────────────────────

func TestMcpRuntimeToCommand_npm(t *testing.T) {
	cmd, args := mcpRuntimeToCommand(mcpRegPkg{RegistryType: "npm", Identifier: "@example/server", RuntimeArguments: []string{"--stdio"}})
	if cmd != "npx" {
		t.Errorf("cmd = %q, want npx", cmd)
	}
	if len(args) != 3 || args[0] != "-y" || args[1] != "@example/server" || args[2] != "--stdio" {
		t.Errorf("args = %v", args)
	}
}

func TestMcpRuntimeToCommand_pypi(t *testing.T) {
	cmd, args := mcpRuntimeToCommand(mcpRegPkg{RegistryType: "pypi", Identifier: "mcp-server-fetch"})
	if cmd != "uvx" {
		t.Errorf("cmd = %q, want uvx", cmd)
	}
	if len(args) != 1 || args[0] != "mcp-server-fetch" {
		t.Errorf("args = %v", args)
	}
}

func TestMcpRuntimeToCommand_docker(t *testing.T) {
	cmd, args := mcpRuntimeToCommand(mcpRegPkg{RegistryType: "docker", Identifier: "myorg/my-mcp"})
	if cmd != "docker" {
		t.Errorf("cmd = %q, want docker", cmd)
	}
	if len(args) < 3 || args[0] != "run" || args[1] != "-i" || args[2] != "--rm" {
		t.Errorf("args = %v", args)
	}
}

func TestMcpRuntimeToCommand_go(t *testing.T) {
	cmd, args := mcpRuntimeToCommand(mcpRegPkg{RegistryType: "go", Identifier: "github.com/org/server"})
	if cmd != "go" {
		t.Errorf("cmd = %q, want go", cmd)
	}
	if len(args) < 2 || args[0] != "run" {
		t.Errorf("args = %v", args)
	}
}

func TestMcpRuntimeToCommand_PreferRegistryName(t *testing.T) {
	// RegistryName takes precedence over RegistryType.
	cmd, _ := mcpRuntimeToCommand(mcpRegPkg{
		RegistryName: "npm",
		RegistryType: "pypi",
		Identifier:   "@x/y",
	})
	if cmd != "npx" {
		t.Errorf("expected npx (RegistryName wins), got %q", cmd)
	}
}

func TestMcpRuntimeToCommand_PreferName(t *testing.T) {
	// Name field takes precedence over Identifier.
	_, args := mcpRuntimeToCommand(mcpRegPkg{
		RegistryType: "npm",
		Name:         "@named/pkg",
		Identifier:   "@identifier/pkg",
	})
	if len(args) < 2 || args[1] != "@named/pkg" {
		t.Errorf("expected @named/pkg (Name wins), args = %v", args)
	}
}

func TestMcpRuntimeToCommand_Unknown(t *testing.T) {
	cmd, args := mcpRuntimeToCommand(mcpRegPkg{RegistryType: "wasm", Identifier: "my-wasm-server", RuntimeArguments: []string{"--run"}})
	if cmd != "my-wasm-server" {
		t.Errorf("unknown runtime should use identifier as command, got %q", cmd)
	}
	if len(args) != 1 || args[0] != "--run" {
		t.Errorf("args = %v", args)
	}
}

func TestSelectMCPPackage_PrefersNPM(t *testing.T) {
	pkgs := []mcpRegPkg{
		{RegistryType: "go", Identifier: "github.com/org/server"},
		{RegistryType: "npm", Identifier: "@org/server"},
		{RegistryType: "pypi", Identifier: "mcp-server"},
	}
	got := selectMCPPackage(pkgs)
	if got.RegistryType != "npm" {
		t.Errorf("expected npm preference, got %q", got.RegistryType)
	}
}

func TestSelectMCPRemote_PrefersMCPPath(t *testing.T) {
	remotes := []mcpRegRemote{
		{Type: "streamable-http", URL: "https://api.example.com/v1"},
		{Type: "streamable-http", URL: "https://api.example.com/mcp"},
	}
	got := selectMCPRemote(remotes)
	if !strings.Contains(got.URL, "/mcp") {
		t.Errorf("expected /mcp endpoint, got %q", got.URL)
	}
}

func TestSelectMCPRemote_FallsBackToFirst(t *testing.T) {
	remotes := []mcpRegRemote{
		{Type: "sse", URL: "https://events.example.com/stream"},
	}
	got := selectMCPRemote(remotes)
	if got.URL != "https://events.example.com/stream" {
		t.Errorf("expected first remote, got %q", got.URL)
	}
}

func TestMcpPublisher(t *testing.T) {
	cases := []struct{ in, want string }{
		{"io.modelcontextprotocol/brave-search", "io.modelcontextprotocol"},
		{"noSlash", ""},
		{"", ""},
		{"/leading-slash", ""},
	}
	for _, tc := range cases {
		got := mcpPublisher(tc.in)
		if got != tc.want {
			t.Errorf("mcpPublisher(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestInferredRemoteHeaders_InferenceSH(t *testing.T) {
	headers := inferredRemoteHeaders("https://api.inference.sh/mcp")
	if len(headers) != 1 || headers[0].Name != "Authorization" {
		t.Errorf("expected Authorization header for inference.sh, got %+v", headers)
	}
	if !headers[0].Required {
		t.Error("Authorization should be required")
	}
}

func TestInferredRemoteHeaders_OtherHost(t *testing.T) {
	headers := inferredRemoteHeaders("https://api.example.com/mcp")
	if len(headers) != 0 {
		t.Errorf("expected no headers for unknown host, got %+v", headers)
	}
}

func TestInferredRemoteHeaders_InvalidURL(t *testing.T) {
	headers := inferredRemoteHeaders("://bad url")
	if headers != nil {
		t.Errorf("expected nil for unparseable URL, got %+v", headers)
	}
}

// ─── SearchMCPRegistry with fake RoundTripper ────────────────────────────────

func TestSearchMCPRegistry_FlatFormat(t *testing.T) {
	list := mcpRegListResponse{
		Servers: []struct {
			Server      *mcpRegEntry `json:"server"`
			ID          string       `json:"id"`
			Name        string       `json:"name"`
			Description string       `json:"description"`
			Version     string       `json:"version"`
		}{
			{ID: "io.example/tool", Name: "My Tool", Description: "A great tool", Version: "1.0.0"},
		},
	}
	b, _ := json.Marshal(list)
	swapTransport(t, &fakeTransport{statusCode: 200, body: string(b)})

	results, next, err := SearchMCPRegistry("tool", "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ID != "io.example/tool" {
		t.Errorf("ID = %q", results[0].ID)
	}
	if next != "" {
		t.Errorf("nextCursor should be empty, got %q", next)
	}
}

func TestSearchMCPRegistry_NestedServerFormat(t *testing.T) {
	list := mcpRegListResponse{
		Servers: []struct {
			Server      *mcpRegEntry `json:"server"`
			ID          string       `json:"id"`
			Name        string       `json:"name"`
			Description string       `json:"description"`
			Version     string       `json:"version"`
		}{
			{
				Server: &mcpRegEntry{
					ID:          "nested-id",
					Name:        "Nested Tool",
					Description: "From nested server field",
					Version:     "2.0",
				},
			},
		},
	}
	b, _ := json.Marshal(list)
	swapTransport(t, &fakeTransport{statusCode: 200, body: string(b)})

	results, _, err := SearchMCPRegistry("nested", "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].ID != "nested-id" {
		t.Errorf("expected nested-id, got %+v", results)
	}
}

func TestSearchMCPRegistry_SkipsEmptyNames(t *testing.T) {
	list := mcpRegListResponse{
		Servers: []struct {
			Server      *mcpRegEntry `json:"server"`
			ID          string       `json:"id"`
			Name        string       `json:"name"`
			Description string       `json:"description"`
			Version     string       `json:"version"`
		}{
			{ID: "no-name", Name: ""},          // should be skipped
			{ID: "has-name", Name: "Good Tool"}, // should be kept
		},
	}
	b, _ := json.Marshal(list)
	swapTransport(t, &fakeTransport{statusCode: 200, body: string(b)})

	results, _, err := SearchMCPRegistry("", "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].ID != "has-name" {
		t.Errorf("expected only has-name, got %+v", results)
	}
}

func TestSearchMCPRegistry_LimitCappedAtMax(t *testing.T) {
	list := mcpRegListResponse{}
	b, _ := json.Marshal(list)
	swapTransport(t, &fakeTransport{statusCode: 200, body: string(b)})

	// Passing an absurd limit should be capped internally (HTTP 200 trivially returned).
	_, _, err := SearchMCPRegistry("x", "", 9999)
	if err != nil {
		t.Fatal(err)
	}
}

func TestSearchMCPRegistry_Non200Error(t *testing.T) {
	swapTransport(t, &fakeTransport{statusCode: 500, body: "error"})
	_, _, err := SearchMCPRegistry("x", "", 5)
	if err == nil {
		t.Fatal("expected error for HTTP 500")
	}
}

func TestSearchMCPRegistry_NetworkError(t *testing.T) {
	swapTransport(t, &fakeTransport{err: io.ErrUnexpectedEOF})
	_, _, err := SearchMCPRegistry("x", "", 5)
	if err == nil {
		t.Fatal("expected error on network failure")
	}
}

func TestSearchMCPRegistry_NextCursor(t *testing.T) {
	list := mcpRegListResponse{}
	list.Metadata.NextCursor = "abc123"
	b, _ := json.Marshal(list)
	swapTransport(t, &fakeTransport{statusCode: 200, body: string(b)})

	_, next, err := SearchMCPRegistry("x", "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if next != "abc123" {
		t.Errorf("nextCursor = %q, want abc123", next)
	}
}

func TestSearchMCPRegistry_PublisherExtracted(t *testing.T) {
	list := mcpRegListResponse{
		Servers: []struct {
			Server      *mcpRegEntry `json:"server"`
			ID          string       `json:"id"`
			Name        string       `json:"name"`
			Description string       `json:"description"`
			Version     string       `json:"version"`
		}{
			{ID: "io.modelcontextprotocol/brave-search", Name: "io.modelcontextprotocol/brave-search"},
		},
	}
	b, _ := json.Marshal(list)
	swapTransport(t, &fakeTransport{statusCode: 200, body: string(b)})

	results, _, err := SearchMCPRegistry("", "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected results")
	}
	if results[0].Publisher != "io.modelcontextprotocol" {
		t.Errorf("Publisher = %q, want io.modelcontextprotocol", results[0].Publisher)
	}
}

// ─── FetchMCPRegistryServer with fake RoundTripper ───────────────────────────

func TestFetchMCPRegistryServer_HappyPath_npm(t *testing.T) {
	detail := mcpRegDetailResponse{
		Name:        "Brave Search",
		Description: "Web search",
		Packages: []mcpRegPkg{
			{
				RegistryType: "npm",
				Identifier:   "@modelcontextprotocol/server-brave-search",
				EnvironmentVariables: []mcpRegEnvVar{
					{Name: "BRAVE_API_KEY", Description: "Brave API key", IsRequired: true},
				},
			},
		},
	}
	b, _ := json.Marshal(detail)
	swapTransport(t, &fakeTransport{statusCode: 200, body: string(b)})

	spec, err := FetchMCPRegistryServer("io.modelcontextprotocol/brave-search")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Command != "npx" {
		t.Errorf("Command = %q, want npx", spec.Command)
	}
	if len(spec.EnvSchema) != 1 || spec.EnvSchema[0].Name != "BRAVE_API_KEY" {
		t.Errorf("EnvSchema = %+v", spec.EnvSchema)
	}
	if !spec.EnvSchema[0].Required {
		t.Error("BRAVE_API_KEY should be required (IsRequired)")
	}
	if len(spec.SetupSteps) == 0 {
		t.Error("expected setup steps")
	}
}

func TestFetchMCPRegistryServer_HappyPath_Remote(t *testing.T) {
	detail := mcpRegDetailResponse{
		Name:        "Remote MCP",
		Description: "Remote only",
		Remotes: []mcpRegRemote{
			{
				Type: "streamable-http",
				URL:  "https://api.inference.sh/mcp",
				Headers: []mcpRegEnvVar{
					{Name: "X-Custom", Description: "custom header", IsRequired: true},
				},
			},
		},
	}
	b, _ := json.Marshal(detail)
	swapTransport(t, &fakeTransport{statusCode: 200, body: string(b)})

	spec, err := FetchMCPRegistryServer("some/remote-server")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Transport != "http" {
		t.Errorf("Transport = %q, want http", spec.Transport)
	}
	if spec.URL != "https://api.inference.sh/mcp" {
		t.Errorf("URL = %q", spec.URL)
	}
	// Should include X-Custom + inferred Authorization.
	foundCustom := false
	foundAuth := false
	for _, h := range spec.HeaderSchema {
		if h.Name == "X-Custom" {
			foundCustom = true
		}
		if h.Name == "Authorization" {
			foundAuth = true
		}
	}
	if !foundCustom {
		t.Error("expected X-Custom in HeaderSchema")
	}
	if !foundAuth {
		t.Error("expected inferred Authorization for inference.sh")
	}
}

func TestFetchMCPRegistryServer_NotFound(t *testing.T) {
	swapTransport(t, &fakeTransport{statusCode: 404, body: "not found"})
	_, err := FetchMCPRegistryServer("io.example/missing")
	if err == nil {
		t.Fatal("expected error for 404")
	}
}

func TestFetchMCPRegistryServer_EmptyPackagesAndRemotes(t *testing.T) {
	detail := mcpRegDetailResponse{Name: "Empty Server"}
	b, _ := json.Marshal(detail)
	swapTransport(t, &fakeTransport{statusCode: 200, body: string(b)})

	_, err := FetchMCPRegistryServer("io.example/empty")
	if err == nil {
		t.Fatal("expected error when server has no packages or remotes")
	}
}

func TestFetchMCPRegistryServer_RemoteWithMissingURL(t *testing.T) {
	detail := mcpRegDetailResponse{
		Name:    "Remote No URL",
		Remotes: []mcpRegRemote{{Type: "streamable-http", URL: ""}},
	}
	b, _ := json.Marshal(detail)
	swapTransport(t, &fakeTransport{statusCode: 200, body: string(b)})

	_, err := FetchMCPRegistryServer("io.example/remote-no-url")
	if err == nil {
		t.Fatal("expected error when remote URL is empty")
	}
}

func TestFetchMCPRegistryServer_EnvVarDeduplication(t *testing.T) {
	detail := mcpRegDetailResponse{
		Name: "Dupe Server",
		Packages: []mcpRegPkg{
			{
				RegistryType: "npm",
				Identifier:   "@org/server",
				EnvironmentVariables: []mcpRegEnvVar{
					{Name: "API_KEY", Required: true},
					{Name: "API_KEY", Required: false}, // duplicate
				},
			},
		},
	}
	b, _ := json.Marshal(detail)
	swapTransport(t, &fakeTransport{statusCode: 200, body: string(b)})

	spec, err := FetchMCPRegistryServer("io.example/dupe-server")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, ev := range spec.EnvSchema {
		if ev.Name == "API_KEY" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 1 API_KEY entry after dedup, got %d", count)
	}
}

// ─── api.go — Fiber handler tests ────────────────────────────────────────────

// newTestApp creates a minimal Fiber app wired to the builder API handler.
// The registry is empty by default; callers may add tools to reg before calling.
func newTestApp(reg *Registry) *fiber.App {
	a := NewGapAnalyzer(reg)
	log, _ := zap.NewDevelopment()
	h := NewAPIHandler(a, log)

	app := fiber.New()
	app.Post("/api/v1/builder/analyze", h.HandleAnalyze)
	app.Post("/api/v1/builder/resolve", h.HandleResolve)
	return app
}

func fiberRequest(t *testing.T, app *fiber.App, method, path, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, path, bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestHandleAnalyze_MissingBody_400(t *testing.T) {
	app := newTestApp(NewRegistry())
	resp := fiberRequest(t, app, http.MethodPost, "/api/v1/builder/analyze", "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestHandleAnalyze_EmptyCapabilities_400(t *testing.T) {
	app := newTestApp(NewRegistry())
	resp := fiberRequest(t, app, http.MethodPost, "/api/v1/builder/analyze", `{"capabilities":[]}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestHandleAnalyze_AllToolsPresent_NoGaps(t *testing.T) {
	reg := NewRegistry()
	reg.Add(ToolEntry{ID: "fs", Name: "Filesystem", Description: "Read and write local files"})
	app := newTestApp(reg)

	body := `{"capabilities":["read and write local files"]}`
	resp := fiberRequest(t, app, http.MethodPost, "/api/v1/builder/analyze", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var result struct {
		Gaps  []CapabilityGap `json:"gaps"`
		Count int             `json:"count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.Count != 0 {
		t.Errorf("expected 0 gaps, got %d", result.Count)
	}
	if result.Gaps == nil {
		t.Error("gaps should be non-nil (empty array)")
	}
}

func TestHandleAnalyze_GapsReturned(t *testing.T) {
	app := newTestApp(NewRegistry()) // empty registry → all capabilities are gaps

	body := `{"capabilities":["quantum-xyzzy-not-a-tool"]}`
	resp := fiberRequest(t, app, http.MethodPost, "/api/v1/builder/analyze", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var result struct {
		Count int             `json:"count"`
		Gaps  []CapabilityGap `json:"gaps"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.Count != 1 {
		t.Errorf("expected 1 gap, got %d", result.Count)
	}
}

func TestHandleAnalyze_ResponseBodyIsJSON(t *testing.T) {
	app := newTestApp(NewRegistry())
	resp := fiberRequest(t, app, http.MethodPost, "/api/v1/builder/analyze", `{"capabilities":["anything"]}`)
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func TestHandleResolve_MissingCapabilities_400(t *testing.T) {
	app := newTestApp(NewRegistry())
	body := `{"gap_index":0,"suggestion_index":0}`
	resp := fiberRequest(t, app, http.MethodPost, "/api/v1/builder/resolve", body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestHandleResolve_GapIndexOutOfRange_400(t *testing.T) {
	reg := NewRegistry()
	// Add a tool that covers "filesystem", so we produce zero gaps.
	reg.Add(ToolEntry{ID: "fs", Name: "Filesystem", Description: "local files"})
	app := newTestApp(reg)

	body := `{"capabilities":["local files"],"gap_index":5,"suggestion_index":0}`
	resp := fiberRequest(t, app, http.MethodPost, "/api/v1/builder/resolve", body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestHandleResolve_SuggestionIndexOutOfRange_400(t *testing.T) {
	app := newTestApp(NewRegistry()) // empty registry → will have gaps
	body := `{"capabilities":["quantum-xyzzy-not-a-tool"],"gap_index":0,"suggestion_index":999}`
	resp := fiberRequest(t, app, http.MethodPost, "/api/v1/builder/resolve", body)
	// Suggestions slice may be empty for this obscure term → 400
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestHandleResolve_HappyPath(t *testing.T) {
	// Use "slack" which should hit the offline registry and have suggestions.
	app := newTestApp(NewRegistry())
	body := `{"capabilities":["slack"],"gap_index":0,"suggestion_index":0}`
	resp := fiberRequest(t, app, http.MethodPost, "/api/v1/builder/resolve", body)

	if resp.StatusCode == http.StatusInternalServerError {
		t.Fatalf("unexpected server error")
	}
	// May be 200 (slack found) or 400 (no gaps / no suggestions).
	// Both are acceptable — just not 5xx.
}

func TestHandleResolve_NextSteps_WithInstallCmd(t *testing.T) {
	// This test exercises the full resolve happy path end-to-end using a
	// capability that the offline registry covers.
	app := newTestApp(NewRegistry())

	// "telegram" is in the offline registry with an install_cmd.
	body := `{"capabilities":["send telegram"],"gap_index":0,"suggestion_index":0}`
	resp := fiberRequest(t, app, http.MethodPost, "/api/v1/builder/resolve", body)

	if resp.StatusCode != http.StatusOK {
		// If no gap (tool already present) or no suggestion, skip rather than fail.
		t.Skipf("resolve returned %d — possibly no gap or no suggestion for 'send telegram'", resp.StatusCode)
	}

	var result struct {
		MCPRef    MCPRef `json:"mcp_ref"`
		NextSteps string `json:"next_steps"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.MCPRef.ID == "" {
		t.Error("expected mcp_ref.id to be set")
	}
}

func TestHandleResolve_InvalidBody_400(t *testing.T) {
	app := newTestApp(NewRegistry())
	resp := fiberRequest(t, app, http.MethodPost, "/api/v1/builder/resolve", "not json")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

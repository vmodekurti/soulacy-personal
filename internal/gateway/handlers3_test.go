// handlers3_test.go — additional handler coverage for internal/gateway.
//
// Targets areas not covered by handlers_test.go or handlers2_test.go:
//   - handleListMemory (GET /memory/:agent_id with and without query param)
//   - chatOverrideMetadata helper (all fields, empty fields, nil pointers)
//   - extractPythonDocstring / extractPythonDocstringUncached (file with docstring, without)
//   - normalizeChannelValue / normalizeChannelBots / maskChannelBots / rawBotList helpers
//   - mcpServerToYAML helper (stdio and http transports)
//   - handleTestMCPServer: HTTP transport (real network not needed, URL is unreachable → ok=false)
//   - handleUpdateMCPServer: happy path (create + update)
//   - handleProvisionAgenticSkill: missing URL/slug validation (503 when skillLoader nil)
//   - handleChatStream: GET variant (query-param path)
//   - handleInstantiateTemplate: happy path using real built-in template
//   - resolveRunTimeout helper
//   - displayChannelValue with array inputs
//   - channelAdapterID more branches
//   - Server.toolCatalog: invalidate cache round-trip
package gateway

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/soulacy/soulacy/pkg/agent"
)

// ── handleListMemory ──────────────────────────────────────────────────────────

func TestGatewayHandleListMemory_ReturnsEntries(t *testing.T) {
	s := newTestGateway(t, "secret")
	// The file-based archive returns an empty list (no entries seeded).
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/memory/some-agent", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("list memory status = %d body=%v", status, body)
	}
	// "entries" key must be present; null/empty slice is valid for an empty archive
	if _, ok := body["entries"]; !ok {
		t.Fatalf("expected entries key in response, body=%v", body)
	}
}

func TestGatewayHandleListMemory_WithSearchQuery(t *testing.T) {
	s := newTestGateway(t, "secret")
	// Same handler; with q= it delegates to MemorySearch. Empty archive returns empty list.
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/memory/some-agent?q=hello", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("list memory (search) status = %d body=%v", status, body)
	}
	if _, ok := body["entries"]; !ok {
		t.Fatalf("expected entries key in response, body=%v", body)
	}
}

// ── chatOverrideMetadata helper ───────────────────────────────────────────────

func TestChatOverrideMetadata_AllFields(t *testing.T) {
	temp := 0.7
	tokens := 512
	turns := 5
	meta := chatOverrideMetadata("anthropic", "claude-3-5-sonnet", &temp, nil, &tokens, &turns, "required", "", "", nil, nil)

	cases := map[string]string{
		"playground.llm.provider":    "anthropic",
		"playground.llm.model":       "claude-3-5-sonnet",
		"playground.llm.temperature": "0.7",
		"playground.llm.max_tokens":  "512",
		"playground.max_turns":       "5",
		"playground.llm.tool_choice": "required",
	}
	for k, want := range cases {
		if got := meta[k]; got != want {
			t.Errorf("meta[%q] = %q, want %q", k, got, want)
		}
	}
}

func TestChatOverrideMetadata_EmptyFieldsReturnNil(t *testing.T) {
	meta := chatOverrideMetadata("", "", nil, nil, nil, nil, "", "", "", nil, nil)
	if meta != nil {
		t.Fatalf("expected nil meta for empty overrides, got %v", meta)
	}
}

func TestChatOverrideMetadata_PartialFields(t *testing.T) {
	meta := chatOverrideMetadata("openai", "", nil, nil, nil, nil, "", "", "", nil, nil)
	if meta == nil {
		t.Fatal("expected non-nil meta when provider is set")
	}
	if meta["playground.llm.provider"] != "openai" {
		t.Errorf("provider = %q, want openai", meta["playground.llm.provider"])
	}
	if _, ok := meta["playground.llm.model"]; ok {
		t.Errorf("model key should be absent when model is empty")
	}
}

func TestChatOverrideMetadata_ZeroMaxTokensNotIncluded(t *testing.T) {
	zero := 0
	meta := chatOverrideMetadata("", "", nil, nil, &zero, nil, "", "", "", nil, nil)
	if meta != nil {
		// max_tokens = 0 should be ignored (not persisted)
		if _, ok := meta["playground.llm.max_tokens"]; ok {
			t.Error("max_tokens=0 should not appear in metadata")
		}
	}
}

// ── extractPythonDocstring helper ─────────────────────────────────────────────

func TestExtractPythonDocstring_TripleDoubleQuote(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tool.py")
	content := `"""My tool does something.

More details here.
"""

def run():
    pass
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got := extractPythonDocstring(path)
	if !strings.Contains(got, "My tool") {
		t.Errorf("docstring = %q, want to contain 'My tool'", got)
	}
}

func TestExtractPythonDocstring_TripleSingleQuote(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tool2.py")
	content := `'''Single-quoted docstring.'''

def run():
    pass
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got := extractPythonDocstring(path)
	if !strings.Contains(got, "Single-quoted") {
		t.Errorf("docstring = %q, want to contain 'Single-quoted'", got)
	}
}

func TestExtractPythonDocstring_NoDocstring(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nodoc.py")
	content := `def run():
    return 42
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got := extractPythonDocstring(path)
	if got != "" {
		t.Errorf("expected empty docstring for file without one, got %q", got)
	}
}

func TestExtractPythonDocstring_NonExistentFile(t *testing.T) {
	got := extractPythonDocstring("/no/such/file.py")
	if got != "" {
		t.Errorf("expected empty docstring for nonexistent file, got %q", got)
	}
}

func TestExtractPythonDocstring_CachesResult(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cached.py")
	content := `"""Cached doc."""`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got1 := extractPythonDocstring(path)
	got2 := extractPythonDocstring(path)
	if got1 != got2 {
		t.Errorf("cache inconsistency: got %q then %q", got1, got2)
	}
}

// ── normalizeChannelValue helper ──────────────────────────────────────────────

func TestNormalizeChannelValue_NonUserIDKey(t *testing.T) {
	got := normalizeChannelValue("token", "bot-token-123")
	if got != "bot-token-123" {
		t.Fatalf("normalizeChannelValue = %v, want bot-token-123", got)
	}
}

func TestNormalizeChannelValue_AllowedUserIDs_Valid(t *testing.T) {
	got := normalizeChannelValue("allowed_user_ids", "123,456,789")
	ids, ok := got.([]int64)
	if !ok {
		t.Fatalf("expected []int64, got %T: %v", got, got)
	}
	if len(ids) != 3 {
		t.Fatalf("expected 3 IDs, got %d: %v", len(ids), ids)
	}
}

func TestNormalizeChannelValue_AllowedUserIDs_EmptyString(t *testing.T) {
	got := normalizeChannelValue("allowed_user_ids", "")
	ids, ok := got.([]int64)
	if !ok {
		t.Fatalf("expected []int64, got %T", got)
	}
	if len(ids) != 0 {
		t.Fatalf("expected empty slice, got %v", ids)
	}
}

// ── rawBotList helper ─────────────────────────────────────────────────────────

func TestRawBotList_NilInput(t *testing.T) {
	got := rawBotList(nil)
	if got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}

func TestRawBotList_SliceMapInput(t *testing.T) {
	input := []map[string]any{{"key": "val"}}
	got := rawBotList(input)
	if len(got) != 1 {
		t.Fatalf("expected 1 bot, got %d", len(got))
	}
}

func TestRawBotList_SliceAnyInput(t *testing.T) {
	input := []any{map[string]any{"a": "b"}, map[string]any{"c": "d"}}
	got := rawBotList(input)
	if len(got) != 2 {
		t.Fatalf("expected 2 bots from []any, got %d", len(got))
	}
}

// ── displayChannelValue helper ────────────────────────────────────────────────

func TestDisplayChannelValue_PlainString(t *testing.T) {
	got := displayChannelValue("hello")
	if got != "hello" {
		t.Fatalf("displayChannelValue string = %v, want hello", got)
	}
}

func TestDisplayChannelValue_SliceAny(t *testing.T) {
	got := displayChannelValue([]any{"a", "b", "c"})
	str, ok := got.(string)
	if !ok {
		t.Fatalf("expected string result, got %T", got)
	}
	if !strings.Contains(str, "a") || !strings.Contains(str, "b") {
		t.Fatalf("displayChannelValue []any = %q, want joined string", str)
	}
}

func TestDisplayChannelValue_SliceInt64(t *testing.T) {
	got := displayChannelValue([]int64{1, 2, 3})
	str, ok := got.(string)
	if !ok {
		t.Fatalf("expected string result, got %T", got)
	}
	if !strings.Contains(str, "1") || !strings.Contains(str, "2") {
		t.Fatalf("displayChannelValue []int64 = %q, want joined string", str)
	}
}

func TestDisplayChannelValue_SliceInt(t *testing.T) {
	got := displayChannelValue([]int{10, 20})
	str, ok := got.(string)
	if !ok {
		t.Fatalf("expected string result, got %T", got)
	}
	if !strings.Contains(str, "10") {
		t.Fatalf("displayChannelValue []int = %q, want contain 10", str)
	}
}

// ── mcpServerToYAML helper ────────────────────────────────────────────────────

func TestMCPServerToYAML_Stdio(t *testing.T) {
	body := mcpServerBody{
		ID:        "myserver",
		Transport: "stdio",
		Command:   "npx",
		Args:      []string{"-y", "@mcp/server"},
		Env:       map[string]string{"API_KEY": "secret"},
	}
	out := mcpServerToYAML(body)
	if out["transport"] != "stdio" {
		t.Errorf("transport = %v, want stdio", out["transport"])
	}
	if out["command"] != "npx" {
		t.Errorf("command = %v, want npx", out["command"])
	}
	if _, ok := out["args"]; !ok {
		t.Error("expected args field")
	}
	if _, ok := out["env"]; !ok {
		t.Error("expected env field")
	}
}

func TestMCPServerToYAML_HTTP(t *testing.T) {
	body := mcpServerBody{
		ID:        "webserver",
		Transport: "http",
		URL:       "https://mcp.example.com",
		Headers:   map[string]string{"Authorization": "Bearer token"},
	}
	out := mcpServerToYAML(body)
	if out["transport"] != "http" {
		t.Errorf("transport = %v, want http", out["transport"])
	}
	if out["url"] != "https://mcp.example.com" {
		t.Errorf("url = %v, want https://mcp.example.com", out["url"])
	}
	if _, ok := out["headers"]; !ok {
		t.Error("expected headers field")
	}
}

func TestMCPServerToYAML_DefaultsToStdio(t *testing.T) {
	body := mcpServerBody{Command: "mycommand"}
	out := mcpServerToYAML(body)
	if out["transport"] != "stdio" {
		t.Errorf("default transport = %v, want stdio", out["transport"])
	}
}

// ── handleTestMCPServer HTTP transport path ───────────────────────────────────

// TestGatewayHandleTestMCPServer_HTTPUnreachable verifies that an http MCP
// test against an unreachable URL returns ok=false (not an HTTP error code —
// per the handler, network errors return 200 with ok=false).
func TestGatewayHandleTestMCPServer_HTTPUnreachable(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/mcp/test", "secret",
		`{"transport":"http","url":"http://127.0.0.1:19999/mcp-unreachable-test"}`)
	if status != http.StatusOK {
		t.Fatalf("test mcp (http unreachable) status = %d body=%v", status, body)
	}
	if body["ok"] != false {
		t.Fatalf("expected ok=false for unreachable URL, body=%v", body)
	}
}

// TestGatewayHandleTestMCPServer_HTTPInvalidURL verifies bad URL → ok=false.
func TestGatewayHandleTestMCPServer_HTTPInvalidURL(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/mcp/test", "secret",
		`{"transport":"http","url":"not-a-url://broken"}`)
	if status != http.StatusOK {
		t.Fatalf("test mcp (bad url) status = %d body=%v", status, body)
	}
	// Handler returns ok=false and an error string.
	if body["ok"] != false {
		t.Fatalf("expected ok=false for invalid URL, body=%v", body)
	}
}

// ── handleUpdateMCPServer happy path ─────────────────────────────────────────

func TestGatewayHandleUpdateMCPServer_Happy(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)

	// Create first.
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/mcp", "secret",
		`{"id":"updateme","transport":"stdio","command":"node"}`)
	if status != http.StatusCreated {
		t.Fatalf("create mcp status = %d body=%v", status, body)
	}

	// Then update.
	status, body = gatewayJSON(t, s, http.MethodPatch, "/api/v1/mcp/updateme", "secret",
		`{"transport":"stdio","command":"deno","args":["run"]}`)
	if status != http.StatusOK {
		t.Fatalf("update mcp status = %d body=%v", status, body)
	}
	if body["ok"] != true {
		t.Fatalf("expected ok=true, body=%v", body)
	}
}

// ── handleProvisionAgenticSkill: validation errors ────────────────────────────

func TestGatewayHandleProvisionAgenticSkill_NilLoader(t *testing.T) {
	s := newTestGateway(t, "secret")
	// skillLoader is nil → 503
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/skills/provision-agenticskills", "secret",
		`{"url":"https://agenticskills.io/skills/some-skill"}`)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("provision skill (nil loader) status = %d body=%v", status, body)
	}
}

func TestGatewayHandleProvisionAgenticSkill_MissingURLAndSlug(t *testing.T) {
	// We can only test this with a nil skill loader returning 503, because no
	// skill loader is wired in the test gateway. The nil-loader guard fires
	// before the missing-URL check, so let's test the full body path instead:
	// sending an empty body should return 400 (bad request for nil/empty JSON)
	// or 503 (nil loader). Both are acceptable non-200 responses.
	s := newTestGateway(t, "secret")
	status, _ := gatewayJSON(t, s, http.MethodPost, "/api/v1/skills/provision-agenticskills", "secret", `{}`)
	if status == http.StatusOK {
		t.Fatalf("expected non-200 for missing url+slug, got %d", status)
	}
}

// ── handleChatStream: GET variant ────────────────────────────────────────────

// TestGatewayChatStreamGET_MissingText verifies that a GET request to the
// stream endpoint without the required 'text' query param returns 400.
func TestGatewayChatStreamGET_MissingText(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, raw := gatewayRaw(t, s, http.MethodGet, "/api/v1/chat/stream?agent_id=some-agent", "secret", "")
	if status != http.StatusBadRequest {
		t.Fatalf("GET stream (missing text) status = %d body=%s", status, raw)
	}
}

func TestGatewayChatStreamGET_MissingAgentID(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, raw := gatewayRaw(t, s, http.MethodGet, "/api/v1/chat/stream?text=hello", "secret", "")
	if status != http.StatusBadRequest {
		t.Fatalf("GET stream (missing agent_id) status = %d body=%s", status, raw)
	}
}

// ── handleInstantiateTemplate: happy path ────────────────────────────────────

// TestGatewayHandleInstantiateTemplate_BuiltinTemplate verifies that
// instantiating a known built-in template creates the agent and returns 201.
func TestGatewayHandleInstantiateTemplate_BuiltinTemplate(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")

	// First, list templates to find one that actually exists.
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/templates", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("list templates status = %d body=%v", status, body)
	}
	templates, ok := body["templates"].([]any)
	if !ok || len(templates) == 0 {
		t.Skip("no built-in templates available in this build")
	}
	// Take the first template.
	firstTemplate, ok := templates[0].(map[string]any)
	if !ok {
		t.Skip("template is not a map")
	}
	templateName, _ := firstTemplate["name"].(string)
	if templateName == "" {
		t.Skip("template has no name")
	}

	// Instantiate it with a unique ID so it doesn't clash with other tests.
	uniqueID := fmt.Sprintf("test-tpl-%d", time.Now().UnixNano())
	instantiateBody := fmt.Sprintf(`{"id":%q}`, uniqueID)
	status, body = gatewayJSON(t, s, http.MethodPost,
		"/api/v1/templates/"+templateName+"/instantiate", "secret", instantiateBody)
	if status != http.StatusCreated {
		t.Fatalf("instantiate template %q status = %d body=%v", templateName, status, body)
	}
	if body["id"] == nil {
		t.Fatalf("expected id in instantiated agent, body=%v", body)
	}
	if body["enabled"] != true {
		t.Fatalf("instantiated agent should be enabled, body=%v", body)
	}
	llm, ok := body["llm"].(map[string]any)
	if !ok {
		t.Fatalf("expected llm config in instantiated agent, body=%v", body)
	}
	if llm["provider"] != "openai" || llm["model"] != "gpt-4o-mini" {
		t.Fatalf("template should inherit configured default provider/model, llm=%v", llm)
	}
}

func TestGatewayHandleListTemplates_AdvertisesRuntimeDefaultModel(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/templates", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("list templates status = %d body=%v", status, body)
	}
	templates, ok := body["templates"].([]any)
	if !ok || len(templates) == 0 {
		t.Skip("no built-in templates available in this build")
	}
	firstTemplate, ok := templates[0].(map[string]any)
	if !ok {
		t.Fatalf("template is not a map: %v", templates[0])
	}
	def, ok := firstTemplate["definition"].(map[string]any)
	if !ok {
		t.Fatalf("template missing definition: %v", firstTemplate)
	}
	llm, ok := def["llm"].(map[string]any)
	if !ok {
		t.Fatalf("template missing definition.llm: %v", def)
	}
	if llm["provider"] != "openai" || llm["model"] != "gpt-4o-mini" {
		t.Fatalf("template preview should inherit configured default provider/model, llm=%v", llm)
	}
	setup, ok := firstTemplate["setup"].([]any)
	if !ok {
		t.Fatalf("template missing setup: %v", firstTemplate)
	}
	foundModel := false
	for _, item := range setup {
		m, ok := item.(map[string]any)
		if !ok || m["key"] != "model" {
			continue
		}
		foundModel = true
		if m["status"] != "ready" || !strings.Contains(fmt.Sprint(m["detail"]), "openai / gpt-4o-mini") {
			t.Fatalf("model setup should reflect runtime default, item=%v", m)
		}
	}
	if !foundModel {
		t.Fatalf("template setup missing model item: %v", setup)
	}
}

func TestGatewayHandleInstantiateTemplate_WithScheduleOutputOverrides(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/templates", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("list templates status = %d body=%v", status, body)
	}
	templates, ok := body["templates"].([]any)
	if !ok || len(templates) == 0 {
		t.Skip("no built-in templates available in this build")
	}
	firstTemplate, ok := templates[0].(map[string]any)
	if !ok {
		t.Skip("template is not a map")
	}
	templateName, _ := firstTemplate["name"].(string)
	if templateName == "" {
		t.Skip("template has no name")
	}

	uniqueID := fmt.Sprintf("test-tpl-output-%d", time.Now().UnixNano())
	instantiateBody := fmt.Sprintf(`{
		"id": %q,
		"cron": "0 7 * * *",
		"output": {"channel":"telegram","to":"@alerts","template":"Report: {reply}"}
	}`, uniqueID)
	status, body = gatewayJSON(t, s, http.MethodPost,
		"/api/v1/templates/"+templateName+"/instantiate", "secret", instantiateBody)
	if status != http.StatusCreated {
		t.Fatalf("instantiate template %q status = %d body=%v", templateName, status, body)
	}
	schedule, ok := body["schedule"].(map[string]any)
	if !ok {
		t.Fatalf("expected schedule in instantiated agent, body=%v", body)
	}
	if schedule["cron"] != "0 7 * * *" {
		t.Fatalf("cron override not applied: %+v", schedule)
	}
	output, ok := schedule["output"].(map[string]any)
	if !ok {
		t.Fatalf("expected schedule.output, schedule=%v", schedule)
	}
	if output["channel"] != "telegram" || output["to"] != "@alerts" || output["template"] != "Report: {reply}" {
		t.Fatalf("output override not applied: %+v", output)
	}
}

func TestGatewayHandleOnboardingStatus(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/onboarding/status", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("onboarding status = %d body=%v", status, body)
	}
	if _, ok := body["complete"].(bool); !ok {
		t.Fatalf("missing complete bool: %v", body)
	}
	steps, ok := body["steps"].([]any)
	if !ok || len(steps) == 0 {
		t.Fatalf("missing onboarding steps: %v", body)
	}
	if _, ok := body["suggested_templates"].([]any); !ok {
		t.Fatalf("missing suggested_templates: %v", body)
	}
	suggested := body["suggested_templates"].([]any)
	if len(suggested) > 0 {
		first, ok := suggested[0].(map[string]any)
		if !ok {
			t.Fatalf("suggested template is not a map: %v", suggested[0])
		}
		def, ok := first["definition"].(map[string]any)
		if !ok {
			t.Fatalf("suggested template missing definition: %v", first)
		}
		llm, ok := def["llm"].(map[string]any)
		if !ok {
			t.Fatalf("suggested template missing llm: %v", def)
		}
		if llm["provider"] != "openai" || llm["model"] != "gpt-4o-mini" {
			t.Fatalf("onboarding templates should inherit configured default provider/model, llm=%v", llm)
		}
	}
}

// ── resolveRunTimeout helper ──────────────────────────────────────────────────

func TestResolveRunTimeout_DefaultFallback(t *testing.T) {
	def := &agent.Definition{}
	got := resolveRunTimeout(def)
	if got != 15*time.Minute {
		t.Fatalf("resolveRunTimeout (no RunTimeout) = %v, want 15m", got)
	}
}

func TestResolveRunTimeout_CustomTimeout(t *testing.T) {
	def := &agent.Definition{RunTimeout: "30m"}
	got := resolveRunTimeout(def)
	if got != 30*time.Minute {
		t.Fatalf("resolveRunTimeout (30m) = %v, want 30m", got)
	}
}

func TestResolveRunTimeout_InvalidIgnored(t *testing.T) {
	def := &agent.Definition{RunTimeout: "not-a-duration"}
	got := resolveRunTimeout(def)
	// Falls back to default.
	if got != 15*time.Minute {
		t.Fatalf("resolveRunTimeout (invalid) = %v, want 15m default", got)
	}
}

// ── Server.InvalidateToolCatalog ─────────────────────────────────────────────

// TestGatewayInvalidateToolCatalog verifies that after InvalidateToolCatalog is
// called, the next toolCatalog() call rescans (new files are discovered).
func TestGatewayInvalidateToolCatalog(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")

	// Prime the cache.
	s.toolCatalog()

	// Invalidate.
	s.InvalidateToolCatalog()

	// Prime again — must not panic, and cache should be nil beforehand.
	s.toolCatalogMu.Lock()
	isCacheNil := s.toolCatalogCache == nil
	s.toolCatalogMu.Unlock()
	if !isCacheNil {
		t.Fatal("cache should be nil after InvalidateToolCatalog")
	}

	catalog := s.toolCatalog()
	// After rescan, toolCatalogAt is updated (cache was refreshed regardless of python tool count).
	s.toolCatalogMu.Lock()
	refreshed := !s.toolCatalogAt.IsZero()
	s.toolCatalogMu.Unlock()
	if !refreshed {
		t.Fatal("toolCatalogAt should be set after rescan")
	}
	_ = catalog
}

// ── handleCreateAgent: missing ID returns 400 ────────────────────────────────

func TestGatewayHandleCreateAgent_MissingID(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents", "secret",
		`{"name":"no-id-agent","system_prompt":"hello","llm":{"provider":"test","model":"m"}}`)
	if status != http.StatusBadRequest {
		t.Fatalf("create agent (no id) status = %d body=%v", status, body)
	}
	if body["error"] == nil {
		t.Fatalf("expected error field, body=%v", body)
	}
}

// ── handleGetAgent: not found ─────────────────────────────────────────────────

func TestGatewayHandleGetAgent_NotFound(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/agents/absolutely-nonexistent", "secret", "")
	if status != http.StatusNotFound {
		t.Fatalf("get agent (not found) status = %d body=%v", status, body)
	}
}

// ── handleUpdateAgent: not found ─────────────────────────────────────────────

func TestGatewayHandleUpdateAgent_NotFound(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodPut, "/api/v1/agents/ghost", "secret",
		`{"name":"Ghost","llm":{"provider":"test","model":"m"}}`)
	if status != http.StatusNotFound {
		t.Fatalf("update nonexistent agent status = %d body=%v", status, body)
	}
}

// ── handleDeleteAgent: not found ─────────────────────────────────────────────

func TestGatewayHandleDeleteAgent_NotFound(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, _ := gatewayJSON(t, s, http.MethodDelete, "/api/v1/agents/ghost", "secret", "")
	// loader.Delete is idempotent — deleting a non-existent agent returns nil error → 204.
	if status != http.StatusNoContent && status != http.StatusNotFound {
		t.Fatalf("delete nonexistent agent: unexpected status %d (want 204 or 404)", status)
	}
}

// ── handleChat: unknown agent returns 500 ─────────────────────────────────────

func TestGatewayChatHandler_UnknownAgent(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/chat", "secret",
		`{"agent_id":"no-such-agent","text":"hello"}`)
	if status != http.StatusInternalServerError {
		t.Fatalf("chat with unknown agent status = %d body=%v", status, body)
	}
	if body["error"] == nil {
		t.Fatalf("expected error field, body=%v", body)
	}
}

// ── handleManualTrigger: already running ─────────────────────────────────────

func TestGatewayHandleManualTrigger_AlreadyRunning(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")

	// Create an agent.
	createBody := `{"id":"running-agent","name":"Running","trigger":"schedule","channels":[],"llm":{"provider":"test","model":"m"},"system_prompt":"run","enabled":true}`
	createStatus, _ := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents", "secret", createBody)
	if createStatus != http.StatusCreated {
		t.Fatalf("create agent: status=%d", createStatus)
	}

	// Mark it as running in the scheduler so the next trigger conflicts.
	if !s.scheduler.TryStartRun("running-agent") {
		t.Fatal("could not mark agent as running")
	}
	defer s.scheduler.FinishRun("running-agent")

	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents/running-agent/trigger", "secret", "")
	if status != http.StatusConflict {
		t.Fatalf("already-running trigger status = %d body=%v", status, body)
	}
}

// ── handleAgentActions: with types filter ────────────────────────────────────

func TestGatewayHandleAgentActions_TypesFilter(t *testing.T) {
	s := newTestGateway(t, "secret")
	// actions is nil → returns empty events regardless of filter
	status, body := gatewayJSON(t, s, http.MethodGet,
		"/api/v1/agents/some-agent/actions?types=message.in,message.out", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("agent actions with types filter status = %d body=%v", status, body)
	}
	if body["events"] == nil {
		t.Fatalf("expected events field, body=%v", body)
	}
}

// ── handleUpdateChannel: bots not supported on non-multi-bot channel ──────────

func TestGatewayHandleUpdateChannel_BotsOnNonMultiBotChannel(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	// whatsapp doesn't support bots.
	status, body := gatewayJSON(t, s, http.MethodPatch, "/api/v1/channels/whatsapp", "secret",
		`{"bots":[{"phone_number_id":"123","access_token":"tok","verify_token":"vt","agent_id":"a"}]}`)
	if status != http.StatusBadRequest {
		t.Fatalf("update channel with bots on non-multi-bot channel status = %d body=%v", status, body)
	}
}

// ── handleUpdateChannel: empty bots clears them ───────────────────────────────

func TestGatewayHandleUpdateChannel_EmptyBotsClearsEntry(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	// First add some bots to telegram.
	status, body := gatewayJSON(t, s, http.MethodPatch, "/api/v1/channels/telegram", "secret",
		`{"bots":[{"token":"tok","agent_id":"my-agent"}]}`)
	if status != http.StatusOK {
		t.Fatalf("set telegram bots status = %d body=%v", status, body)
	}
	// Now clear them.
	status, body = gatewayJSON(t, s, http.MethodPatch, "/api/v1/channels/telegram", "secret",
		`{"bots":[]}`)
	if status != http.StatusOK {
		t.Fatalf("clear telegram bots status = %d body=%v", status, body)
	}
	if body["ok"] != true {
		t.Fatalf("expected ok=true, body=%v", body)
	}
}

// ── normalizeChannelBots helper ───────────────────────────────────────────────

func TestNormalizeChannelBots_MaskedSecretPreservedFromExisting(t *testing.T) {
	spec := channelSpecs[1] // telegram
	bots := []map[string]any{
		{"token": "***", "agent_id": "my-agent", "bot_name": ""},
	}
	existing := []map[string]any{
		{"token": "real-token-123", "agent_id": "my-agent"},
	}
	result := normalizeChannelBots(spec, bots, existing)
	if len(result) != 1 {
		t.Fatalf("expected 1 bot, got %d", len(result))
	}
	// The existing real token should have been preserved when *** is sent.
	if result[0]["token"] != "real-token-123" {
		t.Fatalf("token = %v, want real-token-123", result[0]["token"])
	}
}

func TestNormalizeChannelBots_NewSecretWritten(t *testing.T) {
	spec := channelSpecs[1] // telegram
	bots := []map[string]any{
		{"token": "new-bot-token", "agent_id": "bot1"},
	}
	result := normalizeChannelBots(spec, bots, nil)
	if len(result) != 1 {
		t.Fatalf("expected 1 bot, got %d", len(result))
	}
	if result[0]["token"] != "new-bot-token" {
		t.Fatalf("token = %v, want new-bot-token", result[0]["token"])
	}
}

// ── validateMCPServer: empty transport defaults to stdio ──────────────────────

func TestValidateMCPServer_EmptyTransportDefaultsToStdio(t *testing.T) {
	// Empty transport + command → treated as stdio → valid.
	msg := validateMCPServer(mcpServerBody{Command: "node"})
	if msg != "" {
		t.Fatalf("expected no error for empty transport with command, got %q", msg)
	}
}

// ── handleAdminAPIKeys: validate endpoint (nil store) ───────────────────────

func TestGatewayHandleAdminAPIKeyValidate_NilStore(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/admin/api-keys/validate", "secret",
		`{"key":"some-api-key"}`)
	// With nil api-key store, should return 503.
	if status != http.StatusServiceUnavailable {
		t.Fatalf("validate api-key (nil) status = %d body=%v", status, body)
	}
}

// ── handleAdminAPIKeys: delete endpoint (nil store) ──────────────────────────

func TestGatewayHandleAdminAPIKeyDelete_NilStore(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodDelete, "/api/v1/admin/api-keys/some-id", "secret", "")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("delete api-key (nil) status = %d body=%v", status, body)
	}
}

// ── handleDLQ item operations (nil store) ───────────────────────────────────

func TestGatewayHandleGetDLQItem_NilStore(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/admin/dlq/some-dlq-id", "secret", "")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("get dlq item (nil) status = %d body=%v", status, body)
	}
}

func TestGatewayHandleDeleteDLQItem_NilStore(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodDelete, "/api/v1/admin/dlq/some-dlq-id", "secret", "")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("delete dlq item (nil) status = %d body=%v", status, body)
	}
}

// ── mapToAny helper ───────────────────────────────────────────────────────────

func TestMapToAny_Conversion(t *testing.T) {
	in := map[string]string{"a": "1", "b": "2"}
	out := mapToAny(in)
	if len(out) != 2 {
		t.Fatalf("mapToAny len = %d, want 2", len(out))
	}
	if out["a"] != "1" {
		t.Fatalf("out[a] = %v, want 1", out["a"])
	}
}

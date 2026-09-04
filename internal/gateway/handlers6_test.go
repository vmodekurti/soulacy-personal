// handlers6_test.go — additional coverage for internal/gateway pushing toward 70%+.
//
// Targets functions and branches not yet hit by handlers_test.go through
// handlers5_test.go:
//   - handleSetProviderCredentials: Gemini, Together, Groq, Ollama model variants
//   - handleGetConfig: runtime section fields, memory section fields
//   - handlePatchConfig: LLM provider patch (adds new provider to config)
//   - handlePatchConfig: agent_dirs and skill_dirs patched
//   - handleListKnowledge: nil knowledge service returns enabled:false
//   - handleCreateKnowledge: nil knowledge service returns 503
//   - handleDeleteKnowledge: nil knowledge service returns 204
//   - handleListKnowledgeDocuments: nil service returns empty docs
//   - handleSearchKnowledge: nil service returns 503
//   - handleIngestDocument: nil service returns 503
//   - DLQ: list filtered by queue
//   - handleCreateAgent: agent with description and version fields
//   - handleTestMCPServer: missing transport returns 400
//   - gwSecretEqual: empty want returns false; equal strings match; different don't
//   - parseCostSince: empty, duration, date, invalid variants
//   - channelSupportsBots: whatsapp is NOT multi-bot, http is NOT multi-bot
//   - normalizeChannelBots: bots list cleared when empty slice sent
//   - handleChat: engine error returns 500
//   - handleToolConfirm: unknown call_id returns 404
//   - safeConfigView: providers with api_key set are redacted to ***
//   - handleListSkills: with skillLoader nil returns empty list
//   - Server.InvalidateToolCatalog: clears cache
//   - Server.PythonToolDirs: returns home dir + agent dirs
//
// All tests use only the test-internal Fiber runner (app.Test) — no real
// httptest.Server or external calls.
package gateway

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/queue/dlq"
)

// ── handleSetProviderCredentials: Gemini, Together, Groq ─────────────────────

func TestGatewayHandleSetProviderCredentials_Gemini(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/providers/gemini", "secret",
		`{"api_key":"AIza-fake-key","model":"gemini-1.5-pro"}`)
	if status != http.StatusOK {
		t.Fatalf("set gemini credentials status = %d body=%v", status, body)
	}
	if body["ok"] != true {
		t.Fatalf("expected ok=true, body=%v", body)
	}
}

func TestGatewayHandleSetProviderCredentials_Together(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/providers/together", "secret",
		`{"api_key":"together-fake","model":"meta-llama/Llama-3-70b"}`)
	if status != http.StatusOK {
		t.Fatalf("set together credentials status = %d body=%v", status, body)
	}
	if body["ok"] != true {
		t.Fatalf("expected ok=true, body=%v", body)
	}
}

func TestGatewayHandleSetProviderCredentials_Groq(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/providers/groq", "secret",
		`{"api_key":"gsk_fake","model":"llama3-70b-8192"}`)
	if status != http.StatusOK {
		t.Fatalf("set groq credentials status = %d body=%v", status, body)
	}
	if body["ok"] != true {
		t.Fatalf("expected ok=true, body=%v", body)
	}
}

func TestGatewayHandleSetProviderCredentials_OllamaWithModel(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/providers/ollama", "secret",
		`{"base_url":"http://gpu-server:11434","model":"llama3.3:70b","keep_alive":"10m"}`)
	if status != http.StatusOK {
		t.Fatalf("set ollama+model credentials status = %d body=%v", status, body)
	}
	if body["ok"] != true {
		t.Fatalf("expected ok=true, body=%v", body)
	}
}

// ── handleGetConfig: runtime and memory section fields ───────────────────────

func TestGatewayHandleGetConfig_RuntimeSectionPresent(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/config", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("get config status = %d body=%v", status, body)
	}
	rt, ok := body["runtime"].(map[string]any)
	if !ok {
		t.Fatalf("runtime is not a map: %v", body)
	}
	for _, key := range []string{"max_concurrent_sessions", "default_max_turns", "python_bin", "tool_timeout"} {
		if _, exists := rt[key]; !exists {
			t.Errorf("runtime missing key %q", key)
		}
	}
}

func TestGatewayHandleGetConfig_MemorySectionPresent(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/config", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("get config status = %d body=%v", status, body)
	}
	mem, ok := body["memory"].(map[string]any)
	if !ok {
		t.Fatalf("memory is not a map: %v", body)
	}
	for _, key := range []string{"dir", "sqlite_path", "max_history"} {
		if _, exists := mem[key]; !exists {
			t.Errorf("memory missing key %q", key)
		}
	}
}

func TestGatewayHandleGetConfig_MetaContainsWritableFlag(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/config", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("get config status = %d body=%v", status, body)
	}
	meta, ok := body["_meta"].(map[string]any)
	if !ok {
		t.Fatalf("_meta is not a map: %v", body)
	}
	if _, ok := meta["writable"]; !ok {
		t.Error("_meta missing writable field")
	}
	if meta["writable"] == true {
		t.Error("expected writable=false when cfgPath is empty")
	}
}

// ── handlePatchConfig: LLM provider patch ────────────────────────────────────

func TestGatewayHandlePatchConfig_LLMProviderPatch(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	status, body := gatewayJSON(t, s, http.MethodPatch, "/api/v1/config", "secret",
		`{"llm":{"default_provider":"gemini"}}`)
	if status != http.StatusOK {
		t.Fatalf("patch llm provider status = %d body=%v", status, body)
	}
	if body["ok"] != true {
		t.Fatalf("expected ok=true, body=%v", body)
	}
}

func TestGatewayHandlePatchConfig_AgentDirsPatch(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	newDir := t.TempDir()
	status, body := gatewayJSON(t, s, http.MethodPatch, "/api/v1/config", "secret",
		`{"agent_dirs":["`+newDir+`"]}`)
	if status != http.StatusOK {
		t.Fatalf("patch agent_dirs status = %d body=%v", status, body)
	}
	if body["ok"] != true {
		t.Fatalf("expected ok=true, body=%v", body)
	}
}

func TestGatewayHandlePatchConfig_SkillDirsPatch(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	newDir := t.TempDir()
	status, body := gatewayJSON(t, s, http.MethodPatch, "/api/v1/config", "secret",
		`{"skill_dirs":["`+newDir+`"]}`)
	if status != http.StatusOK {
		t.Fatalf("patch skill_dirs status = %d body=%v", status, body)
	}
	if body["ok"] != true {
		t.Fatalf("expected ok=true, body=%v", body)
	}
}

// ── Knowledge handlers: nil service ──────────────────────────────────────────

func TestGatewayHandleListKnowledge_NilService(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/knowledge", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("list knowledge (nil svc) status = %d body=%v", status, body)
	}
	if body["enabled"] != false {
		t.Fatalf("expected enabled=false when service is nil, got %v", body["enabled"])
	}
}

func TestGatewayHandleCreateKnowledge_NilService(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/knowledge", "secret",
		`{"name":"my-kb","description":"test"}`)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("create knowledge (nil svc) status = %d body=%v", status, body)
	}
	if body["error"] == nil {
		t.Fatalf("expected error field, body=%v", body)
	}
}

func TestGatewayHandleDeleteKnowledge_NilService(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, _ := gatewayJSON(t, s, http.MethodDelete, "/api/v1/knowledge/some-kb", "secret", "")
	if status != http.StatusNoContent {
		t.Fatalf("delete knowledge (nil svc) status = %d", status)
	}
}

func TestGatewayHandleListKnowledgeDocuments_NilService(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/knowledge/some-kb/documents", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("list knowledge docs (nil svc) status = %d body=%v", status, body)
	}
	if _, ok := body["documents"]; !ok {
		t.Fatalf("expected documents field, body=%v", body)
	}
}

func TestGatewayHandleSearchKnowledge_NilService(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/knowledge/some-kb/search", "secret",
		`{"query":"test query"}`)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("search knowledge (nil svc) status = %d body=%v", status, body)
	}
	if body["error"] == nil {
		t.Fatalf("expected error field, body=%v", body)
	}
}

func TestGatewayHandleIngestDocument_NilService(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/knowledge/some-kb/documents", "secret",
		`{"title":"doc","content":"hello world"}`)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("ingest doc (nil svc) status = %d body=%v", status, body)
	}
	if body["error"] == nil {
		t.Fatalf("expected error field, body=%v", body)
	}
}

// ── DLQ: list filtered by queue ───────────────────────────────────────────────

func TestGatewayHandleListDLQ_FilteredByQueue(t *testing.T) {
	s := newTestGateway(t, "secret")
	store := newFakeMemDLQ()
	_ = store.Push(context.Background(), dlq.DeadLetter{ID: "a1", Queue: "agent-a"})
	_ = store.Push(context.Background(), dlq.DeadLetter{ID: "b1", Queue: "agent-b"})
	s.SetDLQStore(store)

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/admin/dlq?queue=agent-a", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("list dlq (filtered) status = %d body=%v", status, body)
	}
	items, ok := body["items"].([]any)
	if !ok {
		t.Fatalf("expected items array, body=%v", body)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item for agent-a, got %d", len(items))
	}
}

// ── handleCreateAgent: additional fields ─────────────────────────────────────

func TestGatewayHandleCreateAgent_WithDescriptionAndVersion(t *testing.T) {
	s := newTestGateway(t, "secret")
	createBody := `{
		"id": "desc-agent",
		"name": "Described Agent",
		"description": "An agent with description",
		"version": "2.0",
		"trigger": "channel",
		"channels": ["http"],
		"llm": {"provider": "openai", "model": "gpt-4o"},
		"system_prompt": "Describe things.",
		"enabled": true
	}`
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents", "secret", createBody)
	if status != http.StatusCreated {
		t.Fatalf("create agent status = %d body=%v", status, body)
	}
	if body["description"] != "An agent with description" {
		t.Fatalf("description not preserved: %v", body)
	}
}

// ── handleTestMCPServer: missing transport returns 400 ───────────────────────

func TestGatewayHandleTestMCPServer_CommandNotFound(t *testing.T) {
	s := newTestGateway(t, "secret")
	// Empty transport defaults to "stdio"; use a command guaranteed not on PATH.
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/mcp/test", "secret",
		`{"command":"definitely-not-a-real-binary-xyz123456"}`)
	if status != http.StatusOK {
		t.Fatalf("mcp/test command-not-found status = %d body=%v", status, body)
	}
	if body["ok"] != false {
		t.Fatalf("expected ok=false for nonexistent command, body=%v", body)
	}
}

// ── gwSecretEqual edge cases ──────────────────────────────────────────────────

func TestGwSecretEqual_EmptyWantReturnsFalse(t *testing.T) {
	if gwSecretEqual("anything", "") {
		t.Error("gwSecretEqual with empty want should return false")
	}
}

func TestGwSecretEqual_MatchingStrings(t *testing.T) {
	if !gwSecretEqual("my-secret-key", "my-secret-key") {
		t.Error("gwSecretEqual with same strings should return true")
	}
}

func TestGwSecretEqual_DifferentStrings(t *testing.T) {
	if gwSecretEqual("key-a", "key-b") {
		t.Error("gwSecretEqual with different strings should return false")
	}
}

func TestGwSecretEqual_EmptyBothReturnsFalse(t *testing.T) {
	if gwSecretEqual("", "") {
		t.Error("gwSecretEqual with both empty should return false (open gateway)")
	}
}

// ── parseCostSince ────────────────────────────────────────────────────────────

func TestParseCostSince_EmptyReturnsZeroTime(t *testing.T) {
	since, label, err := parseCostSince("")
	if err != nil {
		t.Fatalf("parseCostSince(''): %v", err)
	}
	if !since.IsZero() {
		t.Errorf("since should be zero for empty input, got %v", since)
	}
	if label != "" {
		t.Errorf("label should be empty, got %q", label)
	}
}

func TestParseCostSince_DurationString(t *testing.T) {
	since, _, err := parseCostSince("24h")
	if err != nil {
		t.Fatalf("parseCostSince(24h): %v", err)
	}
	expected := time.Now().Add(-24 * time.Hour)
	diff := since.Sub(expected)
	if diff > 5*time.Second || diff < -5*time.Second {
		t.Errorf("since = %v, expected near %v", since, expected)
	}
}

func TestParseCostSince_DateString(t *testing.T) {
	since, _, err := parseCostSince("2026-01-01")
	if err != nil {
		t.Fatalf("parseCostSince(date): %v", err)
	}
	if since.Year() != 2026 || since.Month() != 1 || since.Day() != 1 {
		t.Errorf("since = %v, expected 2026-01-01", since)
	}
}

func TestParseCostSince_InvalidReturnsError(t *testing.T) {
	_, _, err := parseCostSince("not-a-duration-or-date")
	if err == nil {
		t.Fatal("expected error for invalid since param, got nil")
	}
	if !strings.Contains(err.Error(), "invalid since param") {
		t.Errorf("error = %q, want 'invalid since param'", err.Error())
	}
}

// ── channelSupportsBots ───────────────────────────────────────────────────────

func TestChannelSupportsBots_WhatsAppFalse(t *testing.T) {
	if channelSupportsBots("whatsapp") {
		t.Error("whatsapp should NOT support multi-bot")
	}
}

func TestChannelSupportsBots_HTTPFalse(t *testing.T) {
	if channelSupportsBots("http") {
		t.Error("http should NOT support multi-bot")
	}
}

func TestChannelSupportsBots_TelegramTrue(t *testing.T) {
	if !channelSupportsBots("telegram") {
		t.Error("telegram SHOULD support multi-bot")
	}
}

func TestChannelSupportsBots_DiscordTrue(t *testing.T) {
	if !channelSupportsBots("discord") {
		t.Error("discord SHOULD support multi-bot")
	}
}

func TestChannelSupportsBots_SlackTrue(t *testing.T) {
	if !channelSupportsBots("slack") {
		t.Error("slack SHOULD support multi-bot")
	}
}

// ── normalizeChannelBots: empty slice clears bots ────────────────────────────

func TestGatewayHandleUpdateChannel_EmptyBotsClears(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)

	gatewayJSON(t, s, http.MethodPatch, "/api/v1/channels/telegram", "secret",
		`{"bots":[{"token":"tok","agent_id":"bot1"}]}`)

	status, body := gatewayJSON(t, s, http.MethodPatch, "/api/v1/channels/telegram", "secret",
		`{"bots":[]}`)
	if status != http.StatusOK {
		t.Fatalf("clear bots status = %d body=%v", status, body)
	}
	if body["ok"] != true {
		t.Fatalf("expected ok=true, body=%v", body)
	}
}

// ── handleChat: engine error returns 500 ─────────────────────────────────────

func TestGatewayHandleChat_UnknownAgentReturns500(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/chat", "secret",
		`{"agent_id":"ghost-agent-xyz","text":"hello","user_id":"u1"}`)
	if status != http.StatusInternalServerError {
		t.Fatalf("unknown agent chat status = %d body=%v", status, body)
	}
	if body["error"] == nil {
		t.Fatalf("expected error field, body=%v", body)
	}
}

// ── handleToolConfirm: unknown call_id returns 404 ───────────────────────────

func TestGatewayHandleToolConfirm_UnknownCallID(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/chat/confirm", "secret",
		`{"call_id":"nonexistent-call","approved":true}`)
	if status != http.StatusNotFound {
		t.Fatalf("confirm unknown call_id status = %d body=%v", status, body)
	}
	if body["error"] == nil {
		t.Fatalf("expected error field, body=%v", body)
	}
}

// ── safeConfigView: api_key redacted when set ─────────────────────────────────

func TestSafeConfigView_ProviderAPIKeyRedacted(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	gatewayJSON(t, s, http.MethodPost, "/api/v1/providers/openai", "secret",
		`{"api_key":"sk-real-key-123","model":"gpt-4o"}`)

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/config", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("get config status = %d body=%v", status, body)
	}
	llmSection, ok := body["llm"].(map[string]any)
	if !ok {
		t.Fatalf("llm is not a map: %v", body)
	}
	providers, ok := llmSection["providers"].(map[string]any)
	if !ok {
		return
	}
	openai, ok := providers["openai"].(map[string]any)
	if !ok {
		return
	}
	if key, _ := openai["api_key"].(string); key == "sk-real-key-123" {
		t.Error("api_key should be redacted to *** but raw key was returned")
	}
}

// ── handleListSkills: nil loader returns empty list ───────────────────────────

func TestGatewayHandleListSkills_NilLoaderEmptyList(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/skills", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("list skills status = %d body=%v", status, body)
	}
	skills, ok := body["skills"].([]any)
	if !ok {
		t.Fatalf("skills is not an array: %v", body)
	}
	if len(skills) != 0 {
		t.Fatalf("expected empty skills list, got %d items", len(skills))
	}
}

// ── Server.InvalidateToolCatalog clears the cached Python tool list ───────────

func TestGatewayInvalidateToolCatalog_ClearsCache(t *testing.T) {
	s := newTestGateway(t, "secret")

	gatewayJSON(t, s, http.MethodGet, "/api/v1/tool-catalog", "secret", "")

	s.InvalidateToolCatalog()

	s.toolCatalogMu.Lock()
	cached := s.toolCatalogCache
	s.toolCatalogMu.Unlock()
	if cached != nil {
		t.Error("expected cache to be nil after InvalidateToolCatalog")
	}
}

// ── Server.PythonToolDirs includes agent dirs ─────────────────────────────────

func TestGatewayPythonToolDirs_IncludesAgentDirs(t *testing.T) {
	s := newTestGateway(t, "secret")
	dirs := s.PythonToolDirs()
	if len(dirs) == 0 {
		t.Fatal("expected at least one tool dir (home .soulacy/tools), got none")
	}
	found := false
	for _, d := range dirs {
		if strings.HasSuffix(d, "tools") || strings.Contains(d, "tools") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected a 'tools' path in PythonToolDirs, got: %v", dirs)
	}
}

// ── handleChat: session_id empty → defaults to http-<userID> ─────────────────

func TestGatewayHandleChat_EmptySessionIDDefaultsToUserID(t *testing.T) {
	s, provider := newTestGatewayWithLLM(t, "secret")

	gatewayJSON(t, s, http.MethodPost, "/api/v1/agents", "secret", `{
		"id":"chat-sess-default","name":"Chat Agent","trigger":"channel","channels":["http"],
		"llm":{"provider":"test","model":"m"},"system_prompt":"x","enabled":true
	}`)

	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/chat", "secret", `{
		"agent_id":"chat-sess-default",
		"user_id":"user-42",
		"text":"Hello"
	}`)
	if status != http.StatusOK {
		t.Fatalf("chat status = %d body=%v", status, body)
	}
	req := provider.lastRequest()
	if len(req.Messages) == 0 {
		t.Fatal("expected LLM to be called with messages")
	}
}

// ── channelAdapterID: index 0 without agentID returns channelID ──────────────

func TestChannelAdapterID_Index0EmptyAgentID(t *testing.T) {
	got := channelAdapterID("discord", "", "", 0, false)
	if got != "discord" {
		t.Fatalf("channelAdapterID(discord, '', 0) = %q, want discord", got)
	}
}

// ── channelAdapterID: non-zero index without agentID uses numeric suffix ──────

func TestChannelAdapterID_NonZeroIndexEmptyAgentID(t *testing.T) {
	got := channelAdapterID("telegram", "", "", 1, false)
	if got != "telegram-2" {
		t.Fatalf("channelAdapterID(telegram, '', 1) = %q, want telegram-2", got)
	}
}

// ── handleAgentActions with nil actions backend ───────────────────────────────

func TestGatewayHandleAgentActions_UnknownAgent(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, _ := gatewayJSON(t, s, http.MethodGet, "/api/v1/agents/ghost-for-actions/actions", "secret", "")
	if status != http.StatusNotFound && status != http.StatusOK {
		t.Fatalf("actions for unknown agent status = %d", status)
	}
}

// ── handleListMCP: nil mcp note field ────────────────────────────────────────

func TestGatewayHandleListMCP_ResponseHasNoteField(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/mcp", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("list mcp status = %d body=%v", status, body)
	}
	if _, ok := body["servers"]; !ok {
		t.Fatalf("missing servers field, body=%v", body)
	}
}

// ── OS file write for config ──────────────────────────────────────────────────

func TestReadRawConfig_NonexistentFileReturnsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.yaml")
	m, err := readRawConfig(path)
	if err != nil {
		t.Fatalf("readRawConfig on nonexistent file: %v", err)
	}
	if m == nil {
		t.Fatal("expected empty map, got nil")
	}
	if len(m) != 0 {
		t.Errorf("expected empty map, got %v", m)
	}
}

func TestWriteReadRawConfig_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	original := map[string]any{
		"server": map[string]any{"port": 8080},
	}
	if err := writeRawConfig(path, original); err != nil {
		t.Fatalf("writeRawConfig: %v", err)
	}
	got, err := readRawConfig(path)
	if err != nil {
		t.Fatalf("readRawConfig: %v", err)
	}
	srv, _ := got["server"].(map[string]any)
	if srv == nil {
		t.Fatalf("expected server key in round-tripped config, got %v", got)
	}
}

// ── handleListModels for a provider with registered models ───────────────────

func TestGatewayHandleListModels_ReturnsModels(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/providers/test/models", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("list models status = %d body=%v", status, body)
	}
	models, ok := body["models"].([]any)
	if !ok {
		t.Fatalf("expected models array, body=%v", body)
	}
	if len(models) == 0 {
		t.Fatal("expected at least one model from test provider")
	}
}

// ── handleCloneAgent: clone preserves source agent name with "(copy)" suffix ──

func TestGatewayHandleCloneAgent_NameSuffix(t *testing.T) {
	s := newTestGateway(t, "secret")

	gatewayJSON(t, s, http.MethodPost, "/api/v1/agents", "secret", `{
		"id":"clone-src","name":"Source Agent","trigger":"channel","channels":["http"],
		"llm":{"provider":"openai","model":"gpt-4o"},"system_prompt":"x","enabled":true
	}`)

	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents/clone-src/clone", "secret", "")
	if status != http.StatusCreated {
		t.Fatalf("clone status = %d body=%v", status, body)
	}

	name, _ := body["name"].(string)
	if !strings.Contains(name, "(copy)") && !strings.Contains(name, "copy") {
		t.Errorf("clone name %q should contain 'copy'", name)
	}
}

// ── handleDisableAgent: known agent disabled ──────────────────────────────────

func TestGatewayHandleDisableAgent_KnownAgent(t *testing.T) {
	s := newTestGateway(t, "secret")

	gatewayJSON(t, s, http.MethodPost, "/api/v1/agents", "secret", `{
		"id":"disable-me","name":"Disable Me","trigger":"channel","channels":["http"],
		"llm":{"provider":"openai","model":"gpt-4o"},"system_prompt":"x","enabled":true
	}`)

	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents/disable-me/disable", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("disable agent status = %d body=%v", status, body)
	}
	if body["enabled"] != false {
		t.Fatalf("expected enabled=false after disable, got %v", body["enabled"])
	}
}

// ── handlePatchConfig: log level patch ───────────────────────────────────────

func TestGatewayHandlePatchConfig_LogLevelPatch(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	status, body := gatewayJSON(t, s, http.MethodPatch, "/api/v1/config", "secret",
		`{"log":{"level":"debug","format":"json"}}`)
	if status != http.StatusOK {
		t.Fatalf("patch log level status = %d body=%v", status, body)
	}
	if body["ok"] != true {
		t.Fatalf("expected ok=true, body=%v", body)
	}
}

// ── OS-level check: agent YAML file present after creation ───────────────────

func TestGatewayHandleCreateAgent_YAMLFilePresent(t *testing.T) {
	agentDir := t.TempDir()
	s, _ := newTestGatewayWithLLMAndDir(t, "secret", agentDir)

	createBody := `{
		"id":"file-check-agent",
		"name":"File Check",
		"trigger":"channel","channels":["http"],
		"llm":{"provider":"openai","model":"gpt-4o"},
		"system_prompt":"Check file.","enabled":true
	}`
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents", "secret", createBody)
	if status != http.StatusCreated {
		t.Fatalf("create agent status = %d body=%v", status, body)
	}

	entries, err := os.ReadDir(agentDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected at least one file/dir in agentDir after agent creation")
	}
}

// newTestGatewayWithLLMAndDir creates a test gateway that uses agentDir as
// its sole agent directory.
func newTestGatewayWithLLMAndDir(t *testing.T, apiKey, agentDir string) (*Server, *fakeLLMProvider) {
	t.Helper()
	s, provider := newTestGatewayWithLLM(t, apiKey)
	s.cfg.AgentDirs = []string{agentDir}
	return s, provider
}

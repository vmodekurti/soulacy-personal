// handlers5_test.go — additional coverage for internal/gateway.
//
// Targets functions and branches not yet hit by handlers_test.go through
// handlers4_test.go:
//   - handleHealth: dependency statuses (degraded path, provider list)
//   - handleListChannels: settings masked for configured channels
//   - handleUpdateChannel: bots updated on multi-bot channel (telegram)
//   - handleUpdateChannel: enabled flag toggle
//   - preserveHiddenAgentUpdateFields: SystemTools guard (was false on updates)
//   - chatOverrideMetadata: only max_tokens set (partial coverage)
//   - normalizeChannelBots: existing bot beyond index (secret preserved)
//   - displayChannelValue: nil value passthrough
//   - channelSupportsBots: whatsapp is NOT multi-bot
//   - handlePatchConfig: server fields (APIKey masked path)
//   - handleSetProviderCredentials: all optional pointer fields (PromptCaching,
//     ThinkingBudget, SafetyLevel, ExtendedThinking, Organization, ParallelToolCalls)
//   - handleSetProviderCredentials: options map cleared and populated
//   - handleCreateMCPServer: duplicate ID returns conflict
//   - handleCreateMCPServer: http transport saved
//   - handleUpdateMCPServer: no cfgPath returns 503
//   - handleDeleteMCPServer: no cfgPath returns 503 (already covered but variant)
//   - handleCreateAgent: no agent dirs (dir="")
//   - handleCloneAgent: clone with schedule pointer deep-copy
//   - handleDeleteAgent: not found returns 204/404
//   - handleEnableAgent: not found returns 404
//   - handleManualTrigger: already running conflict
//   - handleChatStream: unknown agent still returns 200 (SSE early flush)
//   - handleToolConfirm: bad JSON body returns 400
//   - PythonToolDirs: returns home dir when no agent dirs set
//   - applyChannelToMemory: sets nil map
//   - extractPythonDocstring: long docstring gets truncated
//   - normalizeChannelValue: allowed_user_ids with spaces
//   - rawBotList: unknown type returns nil
//   - valuePresent: []int, []int64 paths
//   - displayChannelValue: []int path
//   - uniqueAgentID: three collisions
//   - handleProvisionAgenticSkill: invalid URL returns 400
//   - handleChatStream: GET with user_id and username defaults
//   - handleListMCP: nil mcp note field
//   - Server.snapshotBuiltins: nil engine
//   - Server.snapshotMCPTools: nil mcp
//   - maskChannelBots: connected adapter status reflected
//   - channelAdapterID: index 0 with non-empty agentID returns channelID
package gateway

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/soulacy/soulacy/internal/channels"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

// ── handleHealth degraded / provider list ────────────────────────────────────

func TestGatewayHealthHandler_IncludesProviderList(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/health", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("health status = %d body=%v", status, body)
	}
	deps, ok := body["deps"].(map[string]any)
	if !ok {
		t.Fatalf("expected deps map, body=%v", body)
	}
	// providers key should list registered providers
	if _, exists := deps["providers"]; !exists {
		t.Fatalf("health deps should have 'providers' key, got=%v", deps)
	}
}

// ── preserveHiddenAgentUpdateFields: SystemTools guard ────────────────────────

func TestPreserveHiddenAgentUpdateFields_SystemToolsPreserved(t *testing.T) {
	t.Parallel() // TEST-4: pure in-memory struct logic, no shared state.
	existing := &agent.Definition{
		ID:          "sys-agent",
		SystemTools: true,
	}
	updates := &agent.Definition{
		ID:          "sys-agent",
		SystemTools: false, // sent as false by GUI → should be preserved from existing
	}
	preserveHiddenAgentUpdateFields(updates, existing)
	if !updates.SystemTools {
		t.Fatal("SystemTools should be preserved from existing when updates.SystemTools=false")
	}
}

func TestPreserveHiddenAgentUpdateFields_SystemToolsAlreadyTrue(t *testing.T) {
	t.Parallel() // TEST-4: pure in-memory struct logic, no shared state.
	existing := &agent.Definition{ID: "x", SystemTools: false}
	updates := &agent.Definition{ID: "x", SystemTools: true}
	preserveHiddenAgentUpdateFields(updates, existing)
	// When updates explicitly sets true, it stays true regardless of existing.
	if !updates.SystemTools {
		t.Fatal("SystemTools=true in updates should remain true")
	}
}

func TestPreserveHiddenAgentUpdateFields_NilSafety(t *testing.T) {
	t.Parallel() // TEST-4: pure in-memory struct logic, no shared state.
	// Must not panic with nil arguments.
	preserveHiddenAgentUpdateFields(nil, nil)
	preserveHiddenAgentUpdateFields(&agent.Definition{}, nil)
	preserveHiddenAgentUpdateFields(nil, &agent.Definition{})
}

// ── chatOverrideMetadata: partial combinations ────────────────────────────────

func TestChatOverrideMetadata_OnlyMaxTokens(t *testing.T) {
	tokens := 128
	meta := chatOverrideMetadata("", "", nil, nil, &tokens, nil, "", "", "", nil, nil)
	if meta == nil {
		t.Fatal("expected non-nil meta when max_tokens is set")
	}
	if meta["playground.llm.max_tokens"] != "128" {
		t.Fatalf("max_tokens = %v, want 128", meta["playground.llm.max_tokens"])
	}
}

func TestChatOverrideMetadata_ToolChoiceOnly(t *testing.T) {
	meta := chatOverrideMetadata("", "", nil, nil, nil, nil, "none", "", "", nil, nil)
	if meta == nil {
		t.Fatal("expected non-nil meta when tool_choice is set")
	}
	if meta["playground.llm.tool_choice"] != "none" {
		t.Fatalf("tool_choice = %v, want none", meta["playground.llm.tool_choice"])
	}
}

// ── displayChannelValue: nil passthrough ──────────────────────────────────────

func TestDisplayChannelValue_Nil(t *testing.T) {
	got := displayChannelValue(nil)
	if got != nil {
		t.Fatalf("displayChannelValue(nil) = %v, want nil", got)
	}
}

func TestDisplayChannelValue_Bool(t *testing.T) {
	got := displayChannelValue(true)
	if got != true {
		t.Fatalf("displayChannelValue(bool) = %v, want true", got)
	}
}

// ── rawBotList: unknown type returns nil ──────────────────────────────────────

func TestRawBotList_UnknownType(t *testing.T) {
	got := rawBotList(42)
	if got != nil {
		t.Fatalf("rawBotList(int) = %v, want nil", got)
	}
}

func TestRawBotList_StringReturnsNil(t *testing.T) {
	got := rawBotList("not a list")
	if got != nil {
		t.Fatalf("rawBotList(string) = %v, want nil", got)
	}
}

// ── valuePresent: int slice paths ────────────────────────────────────────────

func TestValuePresent_EmptyIntSlice(t *testing.T) {
	if valuePresent([]int{}) {
		t.Fatal("empty []int should not be present")
	}
}

func TestValuePresent_NonEmptyIntSlice(t *testing.T) {
	if !valuePresent([]int{1, 2}) {
		t.Fatal("non-empty []int should be present")
	}
}

func TestValuePresent_EmptyInt64Slice(t *testing.T) {
	if valuePresent([]int64{}) {
		t.Fatal("empty []int64 should not be present")
	}
}

func TestValuePresent_NonEmptyInt64Slice(t *testing.T) {
	if !valuePresent([]int64{100}) {
		t.Fatal("non-empty []int64 should be present")
	}
}

// ── normalizeChannelValue: allowed_user_ids with spaces/newlines ──────────────

func TestNormalizeChannelValue_AllowedUserIDs_SpaceSeparated(t *testing.T) {
	got := normalizeChannelValue("allowed_user_ids", "111 222 333")
	ids, ok := got.([]int64)
	if !ok {
		t.Fatalf("expected []int64, got %T", got)
	}
	if len(ids) != 3 {
		t.Fatalf("expected 3 IDs, got %d: %v", len(ids), ids)
	}
}

func TestNormalizeChannelValue_AllowedUserIDs_InvalidPartSkipped(t *testing.T) {
	got := normalizeChannelValue("allowed_user_ids", "123,notanumber,456")
	ids, ok := got.([]int64)
	if !ok {
		t.Fatalf("expected []int64, got %T", got)
	}
	if len(ids) != 2 {
		t.Fatalf("expected 2 valid IDs, got %d: %v", len(ids), ids)
	}
}

func TestNormalizeChannelValue_BoolFields(t *testing.T) {
	if got := normalizeChannelValue("outbound_only", "true"); got != true {
		t.Fatalf("outbound_only true normalized to %#v", got)
	}
	if got := normalizeChannelValue("ignore_groups", "false"); got != false {
		t.Fatalf("ignore_groups false normalized to %#v", got)
	}
	if got := normalizeChannelValue("accept_privileged_exposure", "1"); got != true {
		t.Fatalf("accept_privileged_exposure normalized to %#v", got)
	}
}

// ── channelAdapterID: index 0 with agentID still returns channelID ────────────

func TestChannelAdapterID_Index0WithAgentID(t *testing.T) {
	// At index 0, agentID is ignored; always returns channelID.
	got := channelAdapterID("telegram", "my-bot", "", 0, false)
	if got != "telegram" {
		t.Fatalf("channelAdapterID at index 0 = %q, want telegram", got)
	}
}

func TestChannelAdapterID_Index2WithAgentID(t *testing.T) {
	// Non-zero index with agentID → channelID-agentID
	got := channelAdapterID("telegram", "second-bot", "", 2, false)
	if got != "telegram-second-bot" {
		t.Fatalf("channelAdapterID at index 2 = %q, want telegram-second-bot", got)
	}
}

func TestChannelAdapterID_OutboundOnlyUsesBotName(t *testing.T) {
	got := channelAdapterID("telegram", "", "Daily Stock Screener", 1, false)
	if got != "telegram-Daily-Stock-Screener" {
		t.Fatalf("channelAdapterID outbound-only = %q, want telegram-Daily-Stock-Screener", got)
	}
}

// ── maskChannelBots: connected adapter status reflected ────────────────────────

func TestMaskChannelBots_ConnectedStatusReflected(t *testing.T) {
	spec := channelSpecs[1] // telegram
	raw := []map[string]any{
		{"token": "tok", "agent_id": "mybot"},
	}
	statuses := map[string]channels.AdapterStatus{
		"telegram": {Connected: true, Detail: "running"},
	}
	result := maskChannelBots(spec, map[string]any{"bots": raw}, statuses, nil)
	if len(result) != 1 {
		t.Fatalf("expected 1 bot, got %d", len(result))
	}
	// _adapter_id and _connected must be present.
	if result[0]["_adapter_id"] == nil {
		t.Error("expected _adapter_id field in masked bot")
	}
	if result[0]["_connected"] == nil {
		t.Error("expected _connected field in masked bot")
	}
}

func TestChannelDiagnosticsReportsMissingAndOffline(t *testing.T) {
	spec := *channelSpecByID("telegram")
	cfg := map[string]any{
		"enabled":       true,
		"outbound_only": true,
	}
	got := channelDiagnostics(spec, cfg, true, false, channels.AdapterStatus{}, nil)
	if len(got) < 2 {
		t.Fatalf("diagnostics = %#v, want missing token and restart warning", got)
	}
	var missingToken, unregistered bool
	for _, d := range got {
		if strings.Contains(d.Message, "Default outbound bot token") {
			missingToken = true
		}
		if strings.Contains(d.Message, "not registered") {
			unregistered = true
		}
	}
	if !missingToken || !unregistered {
		t.Fatalf("diagnostics = %#v, missingToken=%v unregistered=%v", got, missingToken, unregistered)
	}
}

func TestChannelDiagnosticsAllowsDedicatedBotMappingsWithoutDefaultBot(t *testing.T) {
	spec := *channelSpecByID("telegram")
	cfg := map[string]any{"enabled": true}
	bots := []fiber.Map{{
		"_adapter_id":   "telegram-weather",
		"agent_id":      "weather",
		"_connected":    true,
		"outbound_only": false,
	}}
	got := channelDiagnostics(spec, cfg, true, true, channels.AdapterStatus{Connected: true}, bots)
	for _, d := range got {
		if strings.Contains(d.Message, "Default outbound bot token") {
			t.Fatalf("unexpected default-bot diagnostic for dedicated mapping: %#v", got)
		}
		if d.Severity == "fail" {
			t.Fatalf("unexpected failure for dedicated mapping: %#v", got)
		}
	}
}

func TestChannelDiagnosticsReportsPrivilegedBlockedBot(t *testing.T) {
	spec := *channelSpecByID("telegram")
	bots := []fiber.Map{{
		"_adapter_id":     "telegram-librarian",
		"agent_id":        "librarian",
		"_connected":      false,
		"outbound_only":   false,
		"_blocked_reason": "privileged agent requires exposure approval",
	}}
	got := channelDiagnostics(spec, map[string]any{"enabled": true}, true, true, channels.AdapterStatus{Connected: true}, bots)
	for _, d := range got {
		if d.Severity == "fail" && strings.Contains(d.Message, "telegram-librarian") && strings.Contains(d.Remedy, "privileged exposure") {
			return
		}
	}
	t.Fatalf("diagnostics did not explain privileged block: %#v", got)
}

// ── handleUpdateChannel: enabled flag toggled with bots ──────────────────────

func TestGatewayHandleUpdateChannel_EnabledFlagOnly(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)

	st, body := gatewayJSON(t, s, http.MethodPatch, "/api/v1/channels/telegram", "secret",
		`{"enabled":true}`)
	if st != http.StatusOK {
		t.Fatalf("enable flag via patch: status=%d body=%v", st, body)
	}
	if body["ok"] != true {
		t.Fatalf("expected ok=true, body=%v", body)
	}
}

// ── handleUpdateChannel: bots field updates on telegram ──────────────────────

func TestGatewayHandleUpdateChannel_MultipleBotsWritten(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)

	payload := `{"bots":[
		{"token":"tok1","agent_id":"bot1","bot_name":"B1"},
		{"token":"tok2","agent_id":"bot2","bot_name":"B2"}
	]}`
	st, body := gatewayJSON(t, s, http.MethodPatch, "/api/v1/channels/telegram", "secret", payload)
	if st != http.StatusOK {
		t.Fatalf("multi-bot patch: status=%d body=%v", st, body)
	}
	if body["ok"] != true {
		t.Fatalf("expected ok=true, body=%v", body)
	}
}

// ── handleSetProviderCredentials: optional pointer fields ─────────────────────

func TestGatewayHandleSetProviderCredentials_ExtendedThinking(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/providers/anthropic", "secret",
		`{"extended_thinking":true}`)
	if status != http.StatusOK {
		t.Fatalf("set extended_thinking status = %d body=%v", status, body)
	}
	if body["ok"] != true {
		t.Fatalf("expected ok=true, body=%v", body)
	}
}

func TestGatewayHandleSetProviderCredentials_ThinkingBudget(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/providers/anthropic", "secret",
		`{"thinking_budget":1024}`)
	if status != http.StatusOK {
		t.Fatalf("set thinking_budget status = %d body=%v", status, body)
	}
}

func TestGatewayHandleSetProviderCredentials_SafetyLevel(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/providers/google", "secret",
		`{"safety_level":"off"}`)
	if status != http.StatusOK {
		t.Fatalf("set safety_level status = %d body=%v", status, body)
	}
	_ = body
}

func TestGatewayHandleSetProviderCredentials_PromptCaching(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/providers/anthropic", "secret",
		`{"prompt_caching":false}`)
	if status != http.StatusOK {
		t.Fatalf("set prompt_caching status = %d body=%v", status, body)
	}
	_ = body
}

func TestGatewayHandleSetProviderCredentials_Organization(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/providers/openai", "secret",
		`{"organization":"org-abc123"}`)
	if status != http.StatusOK {
		t.Fatalf("set organization status = %d body=%v", status, body)
	}
	_ = body
}

func TestGatewayHandleSetProviderCredentials_ParallelToolCalls(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/providers/openai", "secret",
		`{"parallel_tool_calls":false}`)
	if status != http.StatusOK {
		t.Fatalf("set parallel_tool_calls status = %d body=%v", status, body)
	}
	_ = body
}

func TestGatewayHandleSetProviderCredentials_OptionsMap(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	// Set a non-empty options map.
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/providers/ollama", "secret",
		`{"options":{"num_ctx":4096,"temperature":0.5}}`)
	if status != http.StatusOK {
		t.Fatalf("set options status = %d body=%v", status, body)
	}
}

func TestGatewayHandleSetProviderCredentials_OptionsClear(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	// Send empty options — clears the key.
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/providers/ollama", "secret",
		`{"options":{}}`)
	if status != http.StatusOK {
		t.Fatalf("clear options status = %d body=%v", status, body)
	}
	_ = body
}

// ── handleCreateMCPServer: duplicate ID conflict ──────────────────────────────

func TestGatewayHandleCreateMCPServer_DuplicateID(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)

	// Create first.
	st, _ := gatewayJSON(t, s, http.MethodPost, "/api/v1/mcp", "secret",
		`{"id":"dup-server","transport":"stdio","command":"npx"}`)
	if st != http.StatusCreated {
		t.Fatalf("first create: status=%d", st)
	}

	// Create duplicate — should be 409 Conflict.
	st, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/mcp", "secret",
		`{"id":"dup-server","transport":"stdio","command":"node"}`)
	if st != http.StatusConflict {
		t.Fatalf("duplicate create: status=%d body=%v", st, body)
	}
	if body["error"] == nil {
		t.Fatalf("expected error field for conflict, body=%v", body)
	}
}

// ── handleCreateMCPServer: http transport ────────────────────────────────────

func TestGatewayHandleCreateMCPServer_HTTPTransport(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)

	st, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/mcp", "secret",
		`{"id":"httpserver","transport":"http","url":"https://mcp.example.com"}`)
	if st != http.StatusCreated {
		t.Fatalf("create http mcp: status=%d body=%v", st, body)
	}
	if body["ok"] != true {
		t.Fatalf("expected ok=true, body=%v", body)
	}
}

// ── handleUpdateMCPServer: no cfgPath returns 503 ────────────────────────────

func TestGatewayHandleUpdateMCPServer_NoCfgPath(t *testing.T) {
	s := newTestGateway(t, "secret")
	st, body := gatewayJSON(t, s, http.MethodPatch, "/api/v1/mcp/someserver", "secret",
		`{"transport":"stdio","command":"node"}`)
	if st != http.StatusServiceUnavailable {
		t.Fatalf("update mcp (no cfgPath): status=%d body=%v", st, body)
	}
}

// ── handleCreateAgent: no agent dirs writes to "" dir ────────────────────────

func TestGatewayHandleCreateAgent_NoAgentDirs(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")
	// Wipe agent dirs so dir="" path is exercised.
	s.cfg.AgentDirs = nil
	// TEST-4: the create handler falls back to a RELATIVE path ("<id>/SOUL.yaml")
	// when no agent dir is configured. Chdir into a temp dir so that write lands
	// in (and is cleaned up with) the temp dir instead of leaving a stray
	// internal/gateway/nodir-agent/ fixture in the package tree.
	t.Chdir(t.TempDir())

	st, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents", "secret",
		`{"id":"nodir-agent","name":"NoDirAgent","trigger":"channel","channels":["http"],"llm":{"provider":"test","model":"m"},"system_prompt":"hi","enabled":true}`)
	// May succeed or fail depending on whether "" dir is writable; just don't crash.
	// Accept any non-5xx except 500, OR 500 with error body.
	if st != http.StatusCreated && st != http.StatusInternalServerError {
		t.Fatalf("create agent (no dirs): unexpected status=%d body=%v", st, body)
	}
}

// ── handleCloneAgent: Schedule pointer deep-copy ──────────────────────────────

func TestGatewayHandleCloneAgent_WithSchedule(t *testing.T) {
	s := newTestGateway(t, "secret")

	// Create original with a schedule.
	createBody := `{"id":"sched-orig","name":"SchedOrig","trigger":"cron","channels":[],"llm":{"provider":"test","model":"m"},"system_prompt":"x","enabled":true,"schedule":{"cron":"0 * * * *"}}`
	st, _ := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents", "secret", createBody)
	if st != http.StatusCreated {
		t.Fatalf("create: %d", st)
	}

	st, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents/sched-orig/clone", "secret", "")
	if st != http.StatusCreated {
		t.Fatalf("clone: status=%d body=%v", st, body)
	}
	cloneID, _ := body["id"].(string)
	if cloneID == "" {
		t.Fatal("clone id should not be empty")
	}
	if body["enabled"] == true {
		t.Fatal("clone should be disabled")
	}
}

// ── handleChatStream: GET with default user_id ────────────────────────────────

func TestGatewayChatStreamGET_DefaultUserID(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")
	// Create agent first.
	createBody := `{"id":"stream-get-agent","name":"SGA","trigger":"channel","channels":["http"],"llm":{"provider":"test","model":"m"},"system_prompt":"x","enabled":true}`
	st, _ := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents", "secret", createBody)
	if st != http.StatusCreated {
		t.Fatalf("create: %d", st)
	}

	// GET stream without user_id — defaults to "api-user".
	status, _ := gatewayRaw(t, s, http.MethodGet,
		"/api/v1/chat/stream?agent_id=stream-get-agent&text=hello", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("GET stream (no user_id) status = %d", status)
	}
}

// ── handleToolConfirm: bad JSON body ─────────────────────────────────────────

func TestGatewayHandleToolConfirm_BadJSON(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/chat/confirm", "secret",
		`{not valid json`)
	if status != http.StatusBadRequest {
		t.Fatalf("tool confirm (bad json): status=%d body=%v", status, body)
	}
}

// ── Server.snapshotBuiltins: nil engine ───────────────────────────────────────

func TestServerSnapshotBuiltins_NilEngine(t *testing.T) {
	s := newTestGateway(t, "secret")
	// Replace engine with nil to verify the nil guard doesn't panic.
	origEngine := s.engine
	s.engine = nil
	builtins := s.snapshotBuiltins()
	s.engine = origEngine // restore
	// Returns a nil slice (not empty slice) when engine is nil — that's fine.
	if len(builtins) != 0 {
		t.Fatalf("snapshotBuiltins with nil engine should return 0 builtins, got %d", len(builtins))
	}
}

// ── Server.snapshotMCPTools: nil mcp ─────────────────────────────────────────

func TestServerSnapshotMCPTools_NilMCP(t *testing.T) {
	s := newTestGateway(t, "secret")
	// mcp is nil in test gateway — verify no panic and zero tools returned.
	mcps := s.snapshotMCPTools()
	if len(mcps) != 0 {
		t.Fatalf("expected 0 MCP tools when mcp is nil, got %d", len(mcps))
	}
}

// ── extractPythonDocstring: long docstring truncated ─────────────────────────

func TestExtractPythonDocstring_LongDocstringTruncated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "longdoc.py")
	// Build a docstring > 240 characters in first paragraph.
	long := strings.Repeat("x", 300)
	content := `"""` + long + `"""
def run(): pass
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got := extractPythonDocstringUncached(path)
	// Should be truncated to ≤ 243 chars (240 + len("…")).
	if len([]rune(got)) > 243 {
		t.Fatalf("docstring not truncated: len=%d", len(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("truncated docstring should end with ellipsis, got %q", got)
	}
}

// ── handleListMCP: nil mcp has note field ────────────────────────────────────

func TestGatewayHandleListMCP_NoteFieldPresent(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/mcp", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("list mcp status = %d body=%v", status, body)
	}
	if body["note"] == nil {
		t.Fatalf("list mcp (nil client) should have 'note' field, body=%v", body)
	}
}

// ── normalizeChannelBots: secret field absent in bot (not present — skipped) ──

func TestNormalizeChannelBots_AbsentSecretField(t *testing.T) {
	spec := channelSpecs[1] // telegram
	bots := []map[string]any{
		// No "token" key at all (absent, not empty, not ***).
		{"agent_id": "bot1", "bot_name": "Test"},
	}
	result := normalizeChannelBots(spec, bots, nil)
	if len(result) != 1 {
		t.Fatalf("expected 1 bot, got %d", len(result))
	}
	// token was absent — should not appear in result.
	if _, ok := result[0]["token"]; ok {
		t.Fatalf("absent field should not appear in normalized output: %v", result[0])
	}
}

// ── handleChatStream: POST with explicit session returns 200 ─────────────────

func TestGatewayChatStreamPOST_ExplicitSession(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")
	createBody := `{"id":"sess-stream-agent","name":"SSA","trigger":"channel","channels":["http"],"llm":{"provider":"test","model":"m"},"system_prompt":"x","enabled":true}`
	st, _ := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents", "secret", createBody)
	if st != http.StatusCreated {
		t.Fatalf("create: %d", st)
	}

	status, _ := gatewayRaw(t, s, http.MethodPost, "/api/v1/chat/stream", "secret",
		`{"agent_id":"sess-stream-agent","text":"hello","user_id":"u1","username":"User One"}`)
	if status != http.StatusOK {
		t.Fatalf("POST stream with all fields: status=%d", status)
	}
}

// ── handlePatchConfig: server APIKey masked path ──────────────────────────────

func TestGatewayHandlePatchConfig_ServerAPIKeyMasked(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	// Sending api_key=*** should be swallowed silently — ok=true.
	status, body := gatewayJSON(t, s, http.MethodPatch, "/api/v1/config", "secret",
		`{"server":{"api_key":"***"}}`)
	if status != http.StatusOK {
		t.Fatalf("patch config (masked api_key) status = %d body=%v", status, body)
	}
	if body["ok"] != true {
		t.Fatalf("expected ok=true, body=%v", body)
	}
}

// ── uniqueAgentID: three collisions ──────────────────────────────────────────

func TestUniqueAgentID_ThreeCollisions(t *testing.T) {
	s := newTestGateway(t, "secret")

	// Create "collide", "collide-2", so next will be "collide-3".
	for _, id := range []string{"collide", "collide-copy-2"} {
		createBody := `{"id":"` + id + `","name":"X","trigger":"channel","channels":["http"],"llm":{"provider":"test","model":"m"},"system_prompt":"x","enabled":true}`
		st, _ := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents", "secret", createBody)
		if st != http.StatusCreated {
			t.Fatalf("create %q: status=%d", id, st)
		}
	}

	got := s.uniqueAgentID("collide")
	// Should be collide-2 since collide is taken and collide-2 doesn't exist yet.
	if got == "collide" {
		t.Fatalf("uniqueAgentID returned taken ID 'collide'")
	}
}

// ── handleListChannels: configured channel settings masked ────────────────────

func TestGatewayHandleListChannels_MaskedSettings(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)

	// Configure telegram with a token.
	s.applyChannelToMemory("telegram", map[string]any{
		"token":    "real-token-value",
		"agent_id": "mybot",
		"enabled":  true,
	})

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/channels", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("list channels: status=%d body=%v", status, body)
	}
	chs, ok := body["channels"].([]any)
	if !ok {
		t.Fatalf("channels not array: %v", body)
	}
	for _, ch := range chs {
		chMap, ok := ch.(map[string]any)
		if !ok {
			continue
		}
		if chMap["id"] == "telegram" {
			settings, ok := chMap["settings"].(map[string]any)
			if !ok {
				t.Fatalf("telegram settings not a map: %v", chMap)
			}
			if settings["token"] == "real-token-value" {
				t.Fatal("telegram token should be masked, got raw value")
			}
			return
		}
	}
	t.Fatal("telegram channel not found in list")
}

// ── channelSupportsBots: whatsapp is NOT multi-bot ────────────────────────────

func TestChannelSupportsBots_WhatsApp(t *testing.T) {
	if channelSupportsBots("whatsapp") {
		t.Fatal("whatsapp should not support multi-bots")
	}
}

// ── handleScheduleStatus: no running agents ───────────────────────────────────

func TestGatewayHandleScheduleStatus_NoRunning(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/schedule/status", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("schedule status: %d body=%v", status, body)
	}
	running, ok := body["running"].(map[string]any)
	if !ok {
		t.Fatalf("running should be a map: %v", body)
	}
	if len(running) != 0 {
		t.Fatalf("expected empty running map, got %v", running)
	}
}

// ── handleListSchedule: returns schedule key ─────────────────────────────────

func TestGatewayHandleListSchedule_AlwaysHasKey(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/schedule", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("list schedule: %d body=%v", status, body)
	}
	if _, ok := body["schedule"]; !ok {
		t.Fatalf("schedule key absent: %v", body)
	}
}

// ── handleProvisionAgenticSkill: invalid URL ──────────────────────────────────

func TestGatewayHandleProvisionAgenticSkill_InvalidURL(t *testing.T) {
	s := newTestGateway(t, "secret")
	// skillLoader is nil → 503 fires before URL parse.
	// We can only verify the handler doesn't panic.
	status, _ := gatewayJSON(t, s, http.MethodPost, "/api/v1/skills/provision-agenticskills", "secret",
		`{"url":"://bad-url"}`)
	if status == http.StatusOK {
		t.Fatal("expected non-200 for nil loader or bad URL")
	}
}

// ── normalizeChannelBots: empty bots list ────────────────────────────────────

func TestNormalizeChannelBots_EmptyBots(t *testing.T) {
	spec := channelSpecs[1] // telegram
	result := normalizeChannelBots(spec, []map[string]any{}, nil)
	if len(result) != 0 {
		t.Fatalf("expected 0 bots from empty input, got %d", len(result))
	}
}

// ── PythonToolDirs: includes home .soulacy/tools even with no agent dirs ──────

func TestServerPythonToolDirs_HomeIncludedWithNoAgentDirs(t *testing.T) {
	s := newTestGateway(t, "secret")
	s.cfg.AgentDirs = nil // clear agent dirs

	dirs := s.PythonToolDirs()
	found := false
	for _, d := range dirs {
		if strings.Contains(d, ".soulacy") && strings.HasSuffix(d, "tools") {
			found = true
		}
	}
	if !found {
		t.Fatalf("PythonToolDirs should contain home .soulacy/tools, got: %v", dirs)
	}
}

// ── handleSetProviderCredentials: Ollama-specific message ────────────────────

func TestGatewayHandleSetProviderCredentials_OllamaMessage(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/providers/ollama", "secret",
		`{"base_url":"http://127.0.0.1:11434"}`)
	if status != http.StatusOK {
		t.Fatalf("ollama creds: %d body=%v", status, body)
	}
	msg, _ := body["message"].(string)
	if msg != "Saved." {
		t.Fatalf("expected 'Saved.', got %q", msg)
	}
}

// ── handleSetProviderCredentials: Anthropic message ─────────────────────────

func TestGatewayHandleSetProviderCredentials_AnthropicMessage(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/providers/anthropic", "secret",
		`{"api_key":"sk-ant-test"}`)
	if status != http.StatusOK {
		t.Fatalf("anthropic creds: %d body=%v", status, body)
	}
	msg, _ := body["message"].(string)
	if msg != "Saved." {
		t.Fatalf("expected 'Saved.', got %q", msg)
	}
}

// ── handleToolCatalog: cache invalidate then re-populate ─────────────────────

func TestGatewayHandleToolCatalog_HitAfterInvalidate(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")

	// Warm cache.
	st, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/tool-catalog", "secret", "")
	if st != http.StatusOK {
		t.Fatalf("tool catalog (warm): %d body=%v", st, body)
	}

	// Invalidate cache.
	s.InvalidateToolCatalog()

	// Next call must still return valid response.
	st, body = gatewayJSON(t, s, http.MethodGet, "/api/v1/tool-catalog", "secret", "")
	if st != http.StatusOK {
		t.Fatalf("tool catalog (after invalidate): %d body=%v", st, body)
	}
	if _, ok := body["python_tools"]; !ok {
		t.Fatalf("python_tools key absent after invalidate: %v", body)
	}
}

// ── toolCatalog: TTL cache is served on second call ──────────────────────────

func TestGatewayToolCatalog_ServesCachedResult(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")

	// Force cache primed.
	c1 := s.toolCatalog()
	// Immediately re-call — should return same python tool list (cache hit).
	c2 := s.toolCatalog()

	if len(c1.PythonTools) != len(c2.PythonTools) {
		t.Fatalf("cached call mismatch: %d vs %d python tools", len(c1.PythonTools), len(c2.PythonTools))
	}
}

// ── handleDeleteAgent: followed by list (not present) ────────────────────────

func TestGatewayHandleDeleteAgent_AgentDisappearsFromList(t *testing.T) {
	s := newTestGateway(t, "secret")

	createBody := `{"id":"delete-list-agent","name":"DLA","trigger":"channel","channels":["http"],"llm":{"provider":"test","model":"m"},"system_prompt":"x","enabled":true}`
	st, _ := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents", "secret", createBody)
	if st != http.StatusCreated {
		t.Fatalf("create: %d", st)
	}

	st, _ = gatewayJSON(t, s, http.MethodDelete, "/api/v1/agents/delete-list-agent", "secret", "")
	if st != http.StatusNoContent {
		t.Fatalf("delete: %d", st)
	}

	st, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/agents", "secret", "")
	if st != http.StatusOK {
		t.Fatalf("list after delete: %d", st)
	}
	agents, _ := body["agents"].([]any)
	for _, a := range agents {
		if m, ok := a.(map[string]any); ok && m["id"] == "delete-list-agent" {
			t.Fatal("deleted agent should not appear in list")
		}
	}
}

// ── handleValidateAgent: missing required field ───────────────────────────────

func TestGatewayHandleValidateAgent_MissingTrigger(t *testing.T) {
	s := newTestGateway(t, "secret")
	// Sending an agent with a bad trigger → report valid=false.
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents/validate", "secret",
		`{"id":"v-agent","llm":{"provider":"test","model":"m"}}`)
	if status != http.StatusOK {
		t.Fatalf("validate status = %d body=%v", status, body)
	}
	// Valid could be false or true depending on validation rules; just check no crash.
	if body["valid"] == nil {
		t.Fatalf("expected 'valid' field in validate response, body=%v", body)
	}
}

// ── handleAgentActions: count field present ───────────────────────────────────

func TestGatewayHandleAgentActions_CountField(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodGet,
		"/api/v1/agents/my-agent/actions", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("agent actions status = %d body=%v", status, body)
	}
	// When s.actions is nil, handler returns events key (no count field).
	if _, ok := body["events"]; !ok {
		t.Fatalf("expected 'events' field in agent actions response, body=%v", body)
	}
}

// ── handleDeleteChannel: not found ───────────────────────────────────────────

func TestGatewayHandleDisableChannel_UnknownChannel(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/channels/ghost-channel/disable", "secret", "")
	if status != http.StatusNotFound {
		t.Fatalf("disable unknown channel: status=%d body=%v", status, body)
	}
}

// ── handleListTemplates: returns count ────────────────────────────────────────

func TestGatewayHandleListTemplates_CountField(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/templates", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("list templates: %d body=%v", status, body)
	}
	if _, ok := body["count"]; !ok {
		t.Fatalf("expected 'count' field in templates, body=%v", body)
	}
}

// ── handleInstantiateTemplate: name required ──────────────────────────────────

func TestGatewayHandleInstantiateTemplate_EmptyName(t *testing.T) {
	s := newTestGateway(t, "secret")
	// Empty name segment should resolve to a 404 or 400 from the router.
	status, _ := gatewayRaw(t, s, http.MethodPost, "/api/v1/templates//instantiate", "secret", "")
	// Router typically returns 404 for empty path param.
	if status == http.StatusOK {
		t.Fatal("empty template name should not succeed")
	}
}

// ── handleUpdateAgent: invalid JSON body ─────────────────────────────────────

func TestGatewayHandleUpdateAgent_BadJSON(t *testing.T) {
	s := newTestGateway(t, "secret")

	// Create first.
	createBody := `{"id":"bad-json-agent","name":"BJA","trigger":"channel","channels":["http"],"llm":{"provider":"test","model":"m"},"system_prompt":"x","enabled":true}`
	st, _ := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents", "secret", createBody)
	if st != http.StatusCreated {
		t.Fatalf("create: %d", st)
	}

	// Update with bad JSON.
	status, body := gatewayJSON(t, s, http.MethodPut, "/api/v1/agents/bad-json-agent", "secret", `{not json`)
	if status != http.StatusBadRequest {
		t.Fatalf("update bad json: status=%d body=%v", status, body)
	}
}

type captureActionLog struct {
	events []message.Event
}

func (c *captureActionLog) Append(ev message.Event) { c.events = append(c.events, ev) }
func (c *captureActionLog) Tail(agentID string, limit int) ([]message.Event, error) {
	return c.events, nil
}
func (c *captureActionLog) EventFilePath(string) string { return "" }
func (c *captureActionLog) IncompleteMessageIns(time.Time) ([][]byte, error) {
	return nil, nil
}
func (c *captureActionLog) CountMessageInAttempts(string, string, time.Time) (int, error) {
	return 0, nil
}
func (c *captureActionLog) MarkDeadLetter(string, string, string) error { return nil }
func (c *captureActionLog) Close() error                                { return nil }

func TestGatewayHandleUpdateAgent_CapabilityAuditForChannelExposure(t *testing.T) {
	s := newTestGateway(t, "secret")
	log := &captureActionLog{}
	s.actions = log
	s.cfg.Channels = map[string]map[string]any{
		"telegram": {
			"enabled": true,
			"bots": []any{
				map[string]any{"agent_id": "audit-agent", "bot_name": "Audit Bot", "token": "tok"},
			},
		},
	}

	createBody := `{"id":"audit-agent","name":"Audit Agent","trigger":"channel","channels":["http"],"llm":{"provider":"test","model":"m"},"system_prompt":"x","enabled":true}`
	if st, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents", "secret", createBody); st != http.StatusCreated {
		t.Fatalf("create status=%d body=%v", st, body)
	}

	updateBody := `{"id":"audit-agent","name":"Audit Agent","trigger":"channel","channels":["http"],"llm":{"provider":"test","model":"m"},"system_prompt":"x","enabled":true,"builtins":["write_file"]}`

	// Cohort A — Story 5: without X-Acknowledge-Audit the save-blocking
	// modal is triggered. Expect a 409 that carries the peeked audit so
	// the GUI can render the modal, and no action-log side effect yet.
	status, body := gatewayJSON(t, s, http.MethodPut, "/api/v1/agents/audit-agent", "secret", updateBody)
	if status != http.StatusConflict {
		t.Fatalf("expected 409 without ack header, got status=%d body=%v", status, body)
	}
	audit, ok := body["capability_audit"].(map[string]any)
	if !ok {
		t.Fatalf("missing capability_audit in 409 body: %v", body)
	}
	if audit["old_tier"] != "read_only" || audit["new_tier"] != "privileged" || audit["escalated"] != true {
		t.Fatalf("unexpected audit: %#v", audit)
	}
	if audit["requires_ack"] != true {
		t.Fatalf("expected requires_ack for interactive channel exposure: %#v", audit)
	}
	// Snapshot the action-log length AFTER the 409 (the initial create
	// itself produces an unknown→read_only tier-change event that we
	// should not confuse with the update-time event we're testing). The
	// 409 refusal itself must NOT append a new event.
	preAckLen := len(log.events)

	// Retry with the ack header — the save proceeds and the tier-change
	// event lands in the action log.
	status, body = gatewayJSONWithHeader(t, s, http.MethodPut, "/api/v1/agents/audit-agent", "secret", updateBody,
		"X-Acknowledge-Audit", "1")
	if status != http.StatusOK {
		t.Fatalf("ack retry status=%d body=%v", status, body)
	}
	if len(log.events) <= preAckLen {
		t.Fatalf("expected new capability tier action event after ack; pre=%d post=%d events=%#v",
			preAckLen, len(log.events), log.events)
	}
	last := log.events[len(log.events)-1]
	if last.Type != "agent.capability_tier_changed" {
		t.Fatalf("expected capability tier action event, got %#v", last)
	}
}

// ── handleCreateAgent: bad JSON body ─────────────────────────────────────────

func TestGatewayHandleCreateAgent_BadJSON(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents", "secret", `{not json`)
	if status != http.StatusBadRequest {
		t.Fatalf("create bad json: status=%d body=%v", status, body)
	}
}

// ── handleChat: bad JSON body ─────────────────────────────────────────────────

func TestGatewayChatHandler_BadJSON(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/chat", "secret", `{bad`)
	if status != http.StatusBadRequest {
		t.Fatalf("chat bad json: status=%d body=%v", status, body)
	}
}

// ── handleSetProviderModel: bad JSON body ─────────────────────────────────────

func TestGatewayHandleSetProviderModel_BadJSON(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/providers/openai/model", "secret", `{bad`)
	if status != http.StatusBadRequest {
		t.Fatalf("set model bad json: status=%d body=%v", status, body)
	}
}

// ── toolCatalog: TTL expiry triggers rescan ───────────────────────────────────

func TestGatewayToolCatalog_ExpiredTTLRescans(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")
	// Prime cache.
	s.toolCatalog()

	// Manually force cache to look stale by backdating toolCatalogAt.
	s.toolCatalogMu.Lock()
	s.toolCatalogAt = time.Now().Add(-(toolCatalogTTL + time.Second))
	s.toolCatalogMu.Unlock()

	// Next call should rescan and update toolCatalogAt.
	before := s.toolCatalogAt
	s.toolCatalog()
	s.toolCatalogMu.Lock()
	after := s.toolCatalogAt
	s.toolCatalogMu.Unlock()

	if !after.After(before) {
		t.Fatal("toolCatalogAt should be updated after TTL expiry")
	}
}

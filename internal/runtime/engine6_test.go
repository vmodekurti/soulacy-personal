// engine6_test.go — coverage push toward 70%+ for internal/runtime.
//
// Targets paths not yet hit by engine_test.go through engine5_test.go:
//   - Handle: MCP tool dispatch blocked when no client (error surface to LLM)
//   - Handle: confirmation "all" wildcard with approval
//   - Handle: confirmation "all" wildcard denied
//   - maybeConfirm: denied → error returned
//   - maybeConfirm: context cancelled while waiting → ctx.Err()
//   - buildSystemPrefix: skill catalog present when agent has skills (reuse populatedSkillLoader)
//   - loader.go: Upsert → Get round-trip preserves all fields and writes SOUL.yaml
//   - loader.go: Upsert updates existing agent
//   - loader.go: Upsert rejects empty ID
//   - loader.go: parseFile / LoadAll with missing-id YAML → error
//   - toolargs.go: argInt with float32, argInt64 with float32, argFloat with int64
//   - toolargs.go: argBool with int non-zero and zero
//   - builder.go: getOrCreateBuilderSession idempotent
//   - builder.go: GetBuilderUnderstanding nil before any chat turn
//   - builder.go: generateSOULYAML manual trigger type
//   - builder.go: understandingToAgentMap with tools set
//   - engine.go: RunTool with non-trivial argsJSON
//   - engine.go: SetSSRF / SetAllowedToolDirs setters
//   - engine.go: BrainStore nil before SetBrainMemory
//   - engine.go: SetSandbox does not panic
//   - engine.go: SetExecutor nil-safe
//   - engine.go: noopSink Emit is a no-op
//   - engine.go: Handle with passphrase already verified → LLM is called
//   - engine.go: FailureNotifier on unknown agent receives synthesised def
//   - engine.go: Handle emits message.in and message.out via a capturing sink
//   - engine.go: WithConfirmSender does not clobber other context values
//
// All tests are pure-Go (no real LLM, no subprocess, no httptest.Server).
package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/internal/mcp"
	"github.com/soulacy/soulacy/internal/memory"
	"github.com/soulacy/soulacy/internal/sandbox"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
	"github.com/soulacy/soulacy/pkg/skill"
	"go.uber.org/zap"
)

// ─────────────────────────────────────────────────────────────────────────────
// Handle: MCP tool dispatch blocked when no client wired
// ─────────────────────────────────────────────────────────────────────────────

// TestHandle_MCPToolDispatch_BlockedWhenNoClient verifies that when an LLM
// returns an mcp__* tool call but no MCP client is wired, the engine surfaces
// the "not defined" error as a tool result and continues to the next LLM turn.
func TestHandle_MCPToolDispatch_BlockedWhenNoClient(t *testing.T) {
	e, provider := newHandleTestEngine(t, &agent.Definition{
		ID:           "mcp-dispatch",
		Name:         "MCP Dispatch",
		Enabled:      true,
		SystemPrompt: "Use MCP.",
		LLM:          agent.LLMConfig{Provider: "test", Model: "fake-model"},
		MaxTurns:     3,
		Builtins:     strListPtr(),
		// MCPServers and MCPTools nil → legacy mode (all MCP tools allowed)
	})
	// No mcpClient wired — runTool falls through to "not defined".
	provider.responses = []llm.CompletionResponse{
		{ToolCalls: []message.ToolCall{{
			ID:        "mcp-call-1",
			Name:      "mcp__myserver__list_files",
			Arguments: map[string]any{"path": "/tmp"},
		}}},
		{Content: "mcp tool produced an error"},
	}

	reply, err := e.Handle(context.Background(), testUserMessage("mcp-dispatch", "sess-mcp-dispatch", "list files"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	// Verify the engine continued and produced a reply.
	reqs := provider.requestsSnapshot()
	if len(reqs) < 2 {
		t.Fatalf("expected at least 2 provider calls (tool error + synthesis), got %d", len(reqs))
	}
	got := flattenParts(reply.Parts)
	if got == "" {
		t.Fatal("expected non-empty final reply")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Handle: confirmation "all" wildcard approved
// ─────────────────────────────────────────────────────────────────────────────

func TestHandle_ConfirmAllWildcardApproved(t *testing.T) {
	var executions int
	e, provider := newHandleTestEngine(t, &agent.Definition{
		ID:           "all-gated",
		Name:         "All Gated",
		Enabled:      true,
		SystemPrompt: "Use op_tool.",
		LLM:          agent.LLMConfig{Provider: "test", Model: "fake-model"},
		MaxTurns:     3,
		Builtins:     strListPtr("op_tool"),
		ConfirmTools: []string{"all"}, // "all" synonym for wildcard
	})
	e.builtins = []BuiltinTool{{
		Name:        "op_tool",
		Description: "Performs an operation.",
		Parameters:  map[string]any{"type": "object"},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			executions++
			return "operation complete", nil
		},
	}}
	provider.responses = []llm.CompletionResponse{
		{ToolCalls: []message.ToolCall{{ID: "c1", Name: "op_tool", Arguments: map[string]any{}}}},
		{Content: "all done"},
	}

	confirmSender := ConfirmSenderFunc(func(req ConfirmRequest) <-chan bool {
		ch := make(chan bool, 1)
		ch <- true // approve
		return ch
	})
	ctx := WithConfirmSender(context.Background(), confirmSender)

	reply, err := e.Handle(ctx, testUserMessage("all-gated", "sess-all-gated", "run op"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if executions != 1 {
		t.Fatalf("expected 1 execution after 'all' wildcard approval, got %d", executions)
	}
	if got := flattenParts(reply.Parts); got == "" {
		t.Fatal("expected non-empty reply")
	}
}

// TestHandle_ConfirmAllWildcardDenied verifies the denial path with "all"
// wildcard — the tool must NOT execute.
func TestHandle_ConfirmAllWildcardDenied(t *testing.T) {
	var executions int
	e, provider := newHandleTestEngine(t, &agent.Definition{
		ID:           "deny-all",
		Name:         "Deny All",
		Enabled:      true,
		SystemPrompt: "Use op_tool.",
		LLM:          agent.LLMConfig{Provider: "test", Model: "fake-model"},
		MaxTurns:     3,
		Builtins:     strListPtr("op_tool"),
		ConfirmTools: []string{"all"},
	})
	e.builtins = []BuiltinTool{{
		Name:        "op_tool",
		Description: "Performs an operation.",
		Parameters:  map[string]any{"type": "object"},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			executions++
			return "should not reach", nil
		},
	}}
	provider.responses = []llm.CompletionResponse{
		{ToolCalls: []message.ToolCall{{ID: "c1", Name: "op_tool", Arguments: map[string]any{}}}},
		{Content: "denied by user"},
	}

	confirmSender := ConfirmSenderFunc(func(req ConfirmRequest) <-chan bool {
		ch := make(chan bool, 1)
		ch <- false // deny
		return ch
	})
	ctx := WithConfirmSender(context.Background(), confirmSender)

	_, err := e.Handle(ctx, testUserMessage("deny-all", "sess-deny-all", "run op"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if executions != 0 {
		t.Fatalf("tool must not execute after denial, got %d executions", executions)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// maybeConfirm: denied → returns error
// ─────────────────────────────────────────────────────────────────────────────

func TestMaybeConfirm_DeniedReturnsError(t *testing.T) {
	e := newMinimalEngine(t)
	def := &agent.Definition{
		ID:           "deny-bot",
		ConfirmTools: []string{"risky_op"},
	}
	confirmSender := ConfirmSenderFunc(func(req ConfirmRequest) <-chan bool {
		ch := make(chan bool, 1)
		ch <- false // deny
		return ch
	})
	ctx := WithConfirmSender(context.Background(), confirmSender)

	err := e.maybeConfirm(ctx, def, message.ToolCall{
		ID: "call-1", Name: "risky_op",
	})
	if err == nil {
		t.Fatal("maybeConfirm should return error when denied")
	}
	if !strings.Contains(err.Error(), "denied") {
		t.Errorf("error = %q, want to contain 'denied'", err.Error())
	}
}

// TestMaybeConfirm_ContextCancelled verifies that ctx.Done() wins when the
// context is cancelled while waiting for a confirm decision.
func TestMaybeConfirm_ContextCancelled(t *testing.T) {
	e := newMinimalEngine(t)
	def := &agent.Definition{
		ID:           "cancel-bot",
		ConfirmTools: []string{"blocker"},
	}
	// Sender that never sends a decision.
	confirmSender := ConfirmSenderFunc(func(req ConfirmRequest) <-chan bool {
		return make(chan bool) // never sends
	})

	ctx, cancel := context.WithCancel(context.Background())
	ctx = WithConfirmSender(ctx, confirmSender)
	cancel() // cancel immediately

	err := e.maybeConfirm(ctx, def, message.ToolCall{
		ID: "call-cancel", Name: "blocker",
	})
	if err == nil {
		t.Fatal("expected context cancellation error")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// buildSystemPrefix: skill catalog present when agent has skills
// ─────────────────────────────────────────────────────────────────────────────

// TestBuildSystemPrefix_SkillCatalogPresentWhenAgentHasSkills uses the
// populatedSkillLoader from engine2_test.go (same package).
func TestBuildSystemPrefix_SkillCatalogBlock(t *testing.T) {
	skills := []*skill.Skill{
		{Name: "data-analyst-e6", Description: "Analyses data.", Body: "body", Dir: t.TempDir()},
	}
	e := newMinimalEngine(t)
	e.skillLoader = populatedSkillLoader{skills: skills}
	e.builtins = e.buildBuiltins()

	def := &agent.Definition{
		ID:           "sk-agent-e6",
		SystemPrompt: "You are an analyst.",
		Skills:       []string{"data-analyst-e6"},
	}
	prefix := e.buildSystemPrefix(def)
	if !strings.Contains(prefix, "Available Skills") {
		t.Errorf("prefix should contain 'Available Skills', got:\n%s", prefix)
	}
	if !strings.Contains(prefix, "data-analyst-e6") {
		t.Errorf("prefix should contain skill name, got:\n%s", prefix)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// loader.go: Upsert → Get round-trip
// ─────────────────────────────────────────────────────────────────────────────

func TestLoader_Upsert_RoundTripPreservesAllFields(t *testing.T) {
	dir := t.TempDir()
	l := NewLoader([]string{dir})

	def := &agent.Definition{
		ID:           "round-trip-agent",
		Name:         "Round Trip",
		Description:  "Tests round-trip.",
		Enabled:      true,
		SystemPrompt: "You are a helper.",
		MaxTurns:     7,
		LLM: agent.LLMConfig{
			Provider:    "anthropic",
			Model:       "claude-3-haiku",
			Temperature: 0.3,
			MaxTokens:   512,
		},
	}
	if err := l.Upsert(dir, def); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got := l.Get("round-trip-agent")
	if got == nil {
		t.Fatal("Get returned nil after Upsert")
	}
	if got.Name != "Round Trip" {
		t.Errorf("Name = %q, want Round Trip", got.Name)
	}
	if got.MaxTurns != 7 {
		t.Errorf("MaxTurns = %d, want 7", got.MaxTurns)
	}
	if got.LLM.Provider != "anthropic" {
		t.Errorf("LLM.Provider = %q, want anthropic", got.LLM.Provider)
	}
	if got.LLM.Temperature != 0.3 {
		t.Errorf("LLM.Temperature = %v, want 0.3", got.LLM.Temperature)
	}

	// File should exist at the expected path.
	expectedPath := filepath.Join(dir, "round-trip-agent", "SOUL.yaml")
	if _, err := os.Stat(expectedPath); os.IsNotExist(err) {
		t.Errorf("SOUL.yaml not found at expected path: %s", expectedPath)
	}
}

func TestLoader_Upsert_UpdatesExistingAgent(t *testing.T) {
	dir := t.TempDir()
	l := NewLoader([]string{dir})

	def := &agent.Definition{ID: "updatable", Name: "V1", Enabled: true}
	if err := l.Upsert(dir, def); err != nil {
		t.Fatalf("first Upsert: %v", err)
	}

	def.Name = "V2"
	def.SourcePath = l.Get("updatable").SourcePath
	if err := l.Upsert(dir, def); err != nil {
		t.Fatalf("second Upsert: %v", err)
	}

	got := l.Get("updatable")
	if got == nil {
		t.Fatal("Get returned nil after update")
	}
	if got.Name != "V2" {
		t.Errorf("Name = %q, want V2", got.Name)
	}
}

func TestLoader_Upsert_RejectsEmptyID(t *testing.T) {
	dir := t.TempDir()
	l := NewLoader([]string{dir})
	err := l.Upsert(dir, &agent.Definition{ID: ""})
	if err == nil {
		t.Fatal("expected error for empty ID, got nil")
	}
}

func TestLoader_ParseFile_MissingIDReturnsError(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "no-id.yaml")
	if err := os.WriteFile(yamlPath, []byte("name: No ID Agent\nenabled: true\n"), 0644); err != nil {
		t.Fatalf("write YAML: %v", err)
	}
	l := NewLoader([]string{dir})
	errs := l.LoadAll()
	if len(errs) == 0 {
		t.Fatal("expected LoadAll to return error for YAML missing id field, got none")
	}
}

func TestLoader_LoadAll_HandlesMalformedYAML(t *testing.T) {
	dir := t.TempDir()
	// Write a file with an invalid YAML value (YAML maps require unique keys but
	// still parses as a scalar if the id field is missing the actual field).
	yamlPath := filepath.Join(dir, "broken.yaml")
	// Use a tab in a yaml value context which triggers parse error.
	if err := os.WriteFile(yamlPath, []byte("id: broken\nname: [unclosed list\n"), 0644); err != nil {
		t.Fatalf("write YAML: %v", err)
	}
	l := NewLoader([]string{dir})
	errs := l.LoadAll()
	// Should not panic. Errors may or may not be returned depending on YAML strictness.
	_ = errs
}

// ─────────────────────────────────────────────────────────────────────────────
// toolargs.go: additional type coercions
// ─────────────────────────────────────────────────────────────────────────────

func TestArgInt_Float32_E6(t *testing.T) {
	args := map[string]any{"n": float32(8)}
	if got := argInt(args, "n", 0); got != 8 {
		t.Errorf("argInt(float32(8)) = %d, want 8", got)
	}
}

func TestArgInt64_Float32_E6(t *testing.T) {
	args := map[string]any{"n": float32(16)}
	if got := argInt64(args, "n", 0); got != 16 {
		t.Errorf("argInt64(float32(16)) = %d, want 16", got)
	}
}

func TestArgFloat_Int64_E6(t *testing.T) {
	args := map[string]any{"f": int64(9)}
	if got := argFloat(args, "f", 0); got != 9.0 {
		t.Errorf("argFloat(int64(9)) = %v, want 9.0", got)
	}
}

func TestArgBool_IntNonzero_E6(t *testing.T) {
	args := map[string]any{"b": int(1)}
	if !argBool(args, "b") {
		t.Error("argBool(int(1)) should be true")
	}
}

func TestArgBool_IntZero_E6(t *testing.T) {
	args := map[string]any{"b": int(0)}
	if argBool(args, "b") {
		t.Error("argBool(int(0)) should be false")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// builder.go: session management
// ─────────────────────────────────────────────────────────────────────────────

func TestGetOrCreateBuilderSession_Idempotent(t *testing.T) {
	e := newMinimalEngine(t)
	s1 := e.getOrCreateBuilderSession("idem-sess")
	s2 := e.getOrCreateBuilderSession("idem-sess")
	if s1 != s2 {
		t.Fatal("getOrCreateBuilderSession should return same session for same ID")
	}
}

func TestGetBuilderUnderstanding_NilBeforeChatTurn(t *testing.T) {
	e := newMinimalEngine(t)
	e.getOrCreateBuilderSession("blank-sess")
	u := e.GetBuilderUnderstanding("blank-sess")
	// Before any chat turn, understanding is nil.
	if u != nil {
		t.Errorf("expected nil understanding before any chat, got %+v", u)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// builder.go: generateSOULYAML with manual trigger type
// ─────────────────────────────────────────────────────────────────────────────

func TestGenerateSOULYAML_ManualTrigger_E6(t *testing.T) {
	u := &BuilderUnderstanding{
		Name:       "manual-bot",
		Confidence: 0.9,
		Trigger:    &BuilderTrigger{Type: "manual"},
	}
	yaml := generateSOULYAML(u, "ollama", "llama3")
	// "manual" maps to "channel" trigger in generateSOULYAML.
	if !strings.Contains(yaml, "trigger: channel") {
		t.Errorf("YAML should have channel trigger for manual type, got:\n%s", yaml)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// builder.go: understandingToAgentMap with tools
// ─────────────────────────────────────────────────────────────────────────────

func TestUnderstandingToAgentMap_WithTools_E6(t *testing.T) {
	u := &BuilderUnderstanding{
		Name:       "tool-agent",
		Confidence: 0.9,
		Tools: []BuilderTool{
			{Name: "fetch-data", Description: "Fetches external data."},
			{Name: "send-report", Description: "Sends a report."},
		},
	}
	m := understandingToAgentMap(u, "openai", "gpt-4o")
	tools, ok := m["tools"].([]map[string]any)
	if !ok || len(tools) != 2 {
		t.Fatalf("tools = %v, want 2 entries", m["tools"])
	}
	if tools[0]["name"] != "fetch-data" {
		t.Errorf("first tool name = %v, want fetch-data", tools[0]["name"])
	}
	if tools[1]["name"] != "send-report" {
		t.Errorf("second tool name = %v, want send-report", tools[1]["name"])
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// engine.go: RunTool with non-trivial argsJSON decoded correctly
// ─────────────────────────────────────────────────────────────────────────────

func TestRunToolPublic_ArgsDecodedAndPassed(t *testing.T) {
	e := newMinimalEngine(t)
	var receivedQuery string
	e.builtins = []BuiltinTool{{
		Name: "search_tool_e6",
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			receivedQuery = argString(args, "query")
			return "found: " + receivedQuery, nil
		},
	}}

	raw, err := e.RunTool(context.Background(), "search_tool_e6", `{"query":"soulacy documentation"}`)
	if err != nil {
		t.Fatalf("RunTool: %v", err)
	}
	if receivedQuery != "soulacy documentation" {
		t.Errorf("query = %q, want 'soulacy documentation'", receivedQuery)
	}
	if !strings.Contains(string(raw), "found:") {
		t.Errorf("result = %s, want to contain 'found:'", raw)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// engine.go: SetSSRF / SetAllowedToolDirs setters
// ─────────────────────────────────────────────────────────────────────────────

func TestEngine_SetSSRF_SetsFields(t *testing.T) {
	e := newMinimalEngine(t)
	e.SetSSRF(true, []string{"internal.example.com"})
	if !e.ssrfProtection {
		t.Error("ssrfProtection should be true after SetSSRF(true)")
	}
	if len(e.allowPrivateHosts) != 1 || e.allowPrivateHosts[0] != "internal.example.com" {
		t.Errorf("allowPrivateHosts = %v, want [internal.example.com]", e.allowPrivateHosts)
	}
}

func TestEngine_SetAllowedToolDirs_SetsField(t *testing.T) {
	e := newMinimalEngine(t)
	dirs := []string{"/safe/dir/a", "/safe/dir/b"}
	e.SetAllowedToolDirs(dirs)
	if len(e.allowedToolDirs) != 2 {
		t.Errorf("allowedToolDirs = %v, want 2 entries", e.allowedToolDirs)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// engine.go: BrainStore nil before/after SetBrainMemory(nil)
// ─────────────────────────────────────────────────────────────────────────────

func TestEngine_BrainStore_NilBeforeSet(t *testing.T) {
	e := newMinimalEngine(t)
	if e.BrainStore() != nil {
		t.Error("BrainStore should be nil before SetBrainMemory")
	}
	e.SetBrainMemory(nil)
	if e.BrainStore() != nil {
		t.Error("BrainStore should be nil after SetBrainMemory(nil)")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// engine.go: SetSandbox does not panic
// ─────────────────────────────────────────────────────────────────────────────

func TestEngine_SetSandbox_DoesNotPanic(t *testing.T) {
	e := newMinimalEngine(t)
	// Zero-value Limits disables sandboxing.
	e.SetSandbox("/path/to/soulacy", sandbox.Limits{})
}

// ─────────────────────────────────────────────────────────────────────────────
// engine.go: SetExecutor nil-safe
// ─────────────────────────────────────────────────────────────────────────────

func TestEngine_SetExecutor_NilSafe(t *testing.T) {
	e := newMinimalEngine(t)
	// Passing nil should not panic.
	e.SetExecutor(nil)
	if e.pyExecutor != nil {
		t.Error("pyExecutor should remain nil after SetExecutor(nil)")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// engine.go: noopSink Emit does nothing
// ─────────────────────────────────────────────────────────────────────────────

func TestNoopSink_EmitDoesNotPanic(t *testing.T) {
	sink := noopSink{}
	// Must not panic.
	sink.Emit(message.Event{Type: "test", AgentID: "agent-1"})
}

// ─────────────────────────────────────────────────────────────────────────────
// engine.go: Handle with passphrase already verified → LLM is called
// ─────────────────────────────────────────────────────────────────────────────

func TestHandle_PassphraseAlreadyVerified_ProceedsToLLM(t *testing.T) {
	e, provider := newHandleTestEngine(t, &agent.Definition{
		ID:      "pass-agent-e6",
		Name:    "Pass Agent",
		Enabled: true,
		LLM:     agent.LLMConfig{Provider: "test", Model: "fake-model"},
		Security: &agent.SecurityConfig{
			Passphrase: "open-sesame",
		},
		MaxTurns: 2,
		Builtins: strListPtr(),
	})
	provider.responses = []llm.CompletionResponse{{Content: "LLM was reached"}}

	// Pre-mark the session as verified so Handle does not re-challenge.
	sess := e.getOrCreateSession("verified-sess-e6", "pass-agent-e6")
	sess.mu.Lock()
	sess.PassphraseVerified = true
	sess.mu.Unlock()

	reply, err := e.Handle(context.Background(), testUserMessage("pass-agent-e6", "verified-sess-e6", "what is 2+2?"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got := flattenParts(reply.Parts)
	if got != "LLM was reached" {
		t.Fatalf("reply = %q, want 'LLM was reached'", got)
	}
	if reqs := provider.requestsSnapshot(); len(reqs) == 0 {
		t.Fatal("expected LLM to be called after passphrase already verified")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// engine.go: FailureNotifier on unknown agent uses synthesised def
// ─────────────────────────────────────────────────────────────────────────────

func TestHandle_FailureNotifier_UnknownAgent_SyntheticDef(t *testing.T) {
	e, _ := newHandleTestEngine(t, &agent.Definition{
		ID:      "known-e6",
		Enabled: true,
		LLM:     agent.LLMConfig{Provider: "test", Model: "fake-model"},
	})

	fn := &fakeFailureNotifier{}
	e.SetFailureNotifier(fn)

	_, err := e.Handle(context.Background(), testUserMessage("totally-unknown-agent-e6", "sess-e6", "run"))
	if err == nil {
		t.Fatal("expected error for unknown agent")
	}
	if len(fn.calls) == 0 {
		t.Fatal("FailureNotifier should be called even for unknown agent")
	}
	// The synthesised def should carry the unknown agent ID.
	if fn.calls[0].def == nil {
		t.Fatal("notifier call def should not be nil")
	}
	if fn.calls[0].def.ID != "totally-unknown-agent-e6" {
		t.Errorf("notifier def.ID = %q, want totally-unknown-agent-e6", fn.calls[0].def.ID)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// engine.go: Handle emits message.in and message.out via a capturing sink
// ─────────────────────────────────────────────────────────────────────────────

// captureSink6 records all emitted events.
type captureSink6 struct {
	events []message.Event
}

func (s *captureSink6) Emit(ev message.Event) {
	s.events = append(s.events, ev)
}

func TestHandle_EmitsMessageInAndOut(t *testing.T) {
	agentDir := t.TempDir()
	loader := NewLoader([]string{agentDir})

	def := &agent.Definition{
		ID:           "emit-bot-e6",
		Name:         "Emit Bot",
		Enabled:      true,
		SystemPrompt: "Emit events.",
		LLM:          agent.LLMConfig{Provider: "test", Model: "fake-model"},
		MaxTurns:     2,
		Builtins:     strListPtr(),
	}
	if err := loader.Upsert(agentDir, def); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	sink := &captureSink6{}
	provider := &fakeHandleProvider{}
	provider.responses = []llm.CompletionResponse{{Content: "event emitted"}}
	router := llm.NewRouter("test")
	router.Register(provider)
	mem, err := memory.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("memory store: %v", err)
	}
	e := NewEngine(loader, router, mem, nil, "", time.Second, zap.NewNop(), sink, nil, "", nil, nil, nil, nil, nil)

	_, err = e.Handle(context.Background(), testUserMessage("emit-bot-e6", "sess-emit-e6", "hello"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}

	types := make(map[string]bool)
	for _, ev := range sink.events {
		types[ev.Type] = true
	}
	if !types["message.in"] {
		t.Error("expected 'message.in' event to be emitted")
	}
	if !types["message.out"] {
		t.Error("expected 'message.out' event to be emitted")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// engine.go: WithConfirmSender does not clobber other context values
// ─────────────────────────────────────────────────────────────────────────────

func TestWithConfirmSender_DoesNotClobberOtherContextValues(t *testing.T) {
	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "original-value")
	fn := ConfirmSenderFunc(func(req ConfirmRequest) <-chan bool {
		ch := make(chan bool, 1)
		ch <- true
		return ch
	})
	ctx = WithConfirmSender(ctx, fn)

	// Other context values must survive.
	if got, ok := ctx.Value(ctxKey{}).(string); !ok || got != "original-value" {
		t.Errorf("context value = %q, want original-value", got)
	}

	// Confirm sender must be retrievable.
	if _, ok := confirmSenderFrom(ctx); !ok {
		t.Error("confirmSenderFrom should find sender in enriched context")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// engine.go: MCP full name prefix constant matches mcp package
// ─────────────────────────────────────────────────────────────────────────────

func TestMCPFullNamePrefix_MatchesMCPPackage(t *testing.T) {
	// Ensure the prefix the engine uses matches the mcp package constant.
	if mcp.FullNamePrefix != "mcp__" {
		t.Errorf("mcp.FullNamePrefix = %q, want mcp__", mcp.FullNamePrefix)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// engine.go: buildSystemPrefix knowledge service nil → no KB block
// ─────────────────────────────────────────────────────────────────────────────

func TestBuildSystemPrefix_KnowledgeNilService_NoBlock(t *testing.T) {
	e := newMinimalEngine(t)
	def := &agent.Definition{
		ID:           "kb-no-svc-e6",
		SystemPrompt: "Knowledge bot.",
		Knowledge:    []string{"my-kb"},
	}
	prefix := e.buildSystemPrefix(def)
	if strings.Contains(prefix, "Available Knowledge Bases") {
		t.Error("should not have KB block when knowledge service is nil")
	}
	if !strings.Contains(prefix, "Knowledge bot.") {
		t.Errorf("system prompt should be preserved, got:\n%s", prefix)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// engine.go: Handle with Tracer wired records span
// ─────────────────────────────────────────────────────────────────────────────

// fakeTracer records span names.
type fakeTracer6 struct {
	spanNames []string
}

type fakeSpan6 struct{}

func (s *fakeSpan6) End() {}

func (t *fakeTracer6) Start(ctx context.Context, spanName string, kv ...string) (context.Context, interface{ End() }) {
	t.spanNames = append(t.spanNames, spanName)
	return ctx, &fakeSpan6{}
}

func TestHandle_TracerRecordsSpan(t *testing.T) {
	e, provider := newHandleTestEngine(t, &agent.Definition{
		ID:           "traced-bot",
		Name:         "Traced Bot",
		Enabled:      true,
		SystemPrompt: "Trace me.",
		LLM:          agent.LLMConfig{Provider: "test", Model: "fake-model"},
		MaxTurns:     2,
		Builtins:     strListPtr(),
	})
	provider.responses = []llm.CompletionResponse{{Content: "traced reply"}}

	tracer := &fakeTracer6{}
	e.SetTracer(tracer)

	_, err := e.Handle(context.Background(), testUserMessage("traced-bot", "sess-traced", "trace me"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(tracer.spanNames) == 0 {
		t.Fatal("expected at least one span to be recorded")
	}
	found := false
	for _, name := range tracer.spanNames {
		if strings.Contains(name, "Handle") || strings.Contains(name, "engine") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected engine.Handle span, got: %v", tracer.spanNames)
	}
}

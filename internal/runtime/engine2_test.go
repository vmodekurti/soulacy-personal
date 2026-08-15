// engine2_test.go — additional coverage for runTool dispatch paths,
// buildSystemPrefix, applyPlaygroundOverrides, appendSkillBuiltins, and
// buildContext. All tests are pure-Go (no real LLM, no httptest server).
package runtime

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/internal/memory"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
	"github.com/soulacy/soulacy/pkg/skill"
	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// applyPlaygroundOverrides — pure function, no engine state needed
// ---------------------------------------------------------------------------

func TestApplyPlaygroundOverrides_NilMeta(t *testing.T) {
	def := &agent.Definition{
		LLM: agent.LLMConfig{Provider: "anthropic", Model: "claude-3", Temperature: 0.5, MaxTokens: 1000},
	}
	// Snapshot original values
	origProvider := def.LLM.Provider
	origModel := def.LLM.Model
	origTemp := def.LLM.Temperature
	origTokens := def.LLM.MaxTokens

	applyPlaygroundOverrides(def, nil)

	if def.LLM.Provider != origProvider {
		t.Errorf("provider changed: got %q, want %q", def.LLM.Provider, origProvider)
	}
	if def.LLM.Model != origModel {
		t.Errorf("model changed: got %q, want %q", def.LLM.Model, origModel)
	}
	if def.LLM.Temperature != origTemp {
		t.Errorf("temperature changed: got %v, want %v", def.LLM.Temperature, origTemp)
	}
	if def.LLM.MaxTokens != origTokens {
		t.Errorf("max_tokens changed: got %d, want %d", def.LLM.MaxTokens, origTokens)
	}
}

func TestApplyPlaygroundOverrides_EmptyMeta(t *testing.T) {
	def := &agent.Definition{
		LLM: agent.LLMConfig{Provider: "anthropic", Model: "claude-3"},
	}
	applyPlaygroundOverrides(def, map[string]string{})
	if def.LLM.Provider != "anthropic" {
		t.Errorf("provider should be unchanged, got %q", def.LLM.Provider)
	}
}

func TestApplyPlaygroundOverrides_NilDef(t *testing.T) {
	// Must not panic.
	applyPlaygroundOverrides(nil, map[string]string{"playground.llm.model": "gpt-4"})
}

func TestApplyPlaygroundOverrides_ModelOverride(t *testing.T) {
	def := &agent.Definition{
		LLM: agent.LLMConfig{Provider: "anthropic", Model: "claude-3"},
	}
	applyPlaygroundOverrides(def, map[string]string{
		"playground.llm.model": "gpt-4o",
	})
	if def.LLM.Model != "gpt-4o" {
		t.Errorf("model = %q, want gpt-4o", def.LLM.Model)
	}
}

func TestApplyPlaygroundOverrides_ProviderOverride(t *testing.T) {
	def := &agent.Definition{
		LLM: agent.LLMConfig{Provider: "anthropic"},
	}
	applyPlaygroundOverrides(def, map[string]string{
		"playground.llm.provider": "openai",
	})
	if def.LLM.Provider != "openai" {
		t.Errorf("provider = %q, want openai", def.LLM.Provider)
	}
}

func TestApplyPlaygroundOverrides_TemperatureOverride(t *testing.T) {
	def := &agent.Definition{
		LLM: agent.LLMConfig{Temperature: 0.5},
	}
	applyPlaygroundOverrides(def, map[string]string{
		"playground.llm.temperature": "0.9",
	})
	if def.LLM.Temperature != 0.9 {
		t.Errorf("temperature = %v, want 0.9", def.LLM.Temperature)
	}
}

func TestApplyPlaygroundOverrides_InvalidTemperatureIgnored(t *testing.T) {
	def := &agent.Definition{
		LLM: agent.LLMConfig{Temperature: 0.5},
	}
	applyPlaygroundOverrides(def, map[string]string{
		"playground.llm.temperature": "not-a-float",
	})
	if def.LLM.Temperature != 0.5 {
		t.Errorf("temperature should be unchanged on bad value, got %v", def.LLM.Temperature)
	}
}

func TestApplyPlaygroundOverrides_MaxTokensOverride(t *testing.T) {
	def := &agent.Definition{
		LLM: agent.LLMConfig{MaxTokens: 512},
	}
	applyPlaygroundOverrides(def, map[string]string{
		"playground.llm.max_tokens": "2048",
	})
	if def.LLM.MaxTokens != 2048 {
		t.Errorf("max_tokens = %d, want 2048", def.LLM.MaxTokens)
	}
}

func TestApplyPlaygroundOverrides_InvalidMaxTokensIgnored(t *testing.T) {
	def := &agent.Definition{
		LLM: agent.LLMConfig{MaxTokens: 512},
	}
	applyPlaygroundOverrides(def, map[string]string{
		"playground.llm.max_tokens": "bad",
	})
	if def.LLM.MaxTokens != 512 {
		t.Errorf("max_tokens should be unchanged on bad value, got %d", def.LLM.MaxTokens)
	}
}

func TestApplyPlaygroundOverrides_ZeroMaxTokensIgnored(t *testing.T) {
	def := &agent.Definition{
		LLM: agent.LLMConfig{MaxTokens: 512},
	}
	applyPlaygroundOverrides(def, map[string]string{
		"playground.llm.max_tokens": "0",
	})
	// Zero is invalid (n > 0 required), so original value must be preserved.
	if def.LLM.MaxTokens != 512 {
		t.Errorf("max_tokens should be unchanged for zero value, got %d", def.LLM.MaxTokens)
	}
}

func TestApplyPlaygroundOverrides_MaxTurnsOverride(t *testing.T) {
	def := &agent.Definition{MaxTurns: 3}
	applyPlaygroundOverrides(def, map[string]string{
		"playground.max_turns": "10",
	})
	if def.MaxTurns != 10 {
		t.Errorf("max_turns = %d, want 10", def.MaxTurns)
	}
}

func TestApplyPlaygroundOverrides_ToolChoiceOverride(t *testing.T) {
	def := &agent.Definition{
		LLM: agent.LLMConfig{ToolChoice: "auto"},
	}
	applyPlaygroundOverrides(def, map[string]string{
		"playground.llm.tool_choice": "required",
	})
	if def.LLM.ToolChoice != "required" {
		t.Errorf("tool_choice = %q, want required", def.LLM.ToolChoice)
	}
}

// Table-driven omnibus test for all override fields
func TestApplyPlaygroundOverrides_Table(t *testing.T) {
	cases := []struct {
		name  string
		meta  map[string]string
		check func(t *testing.T, def *agent.Definition)
	}{
		{
			name: "whitespace-only model is ignored",
			meta: map[string]string{"playground.llm.model": "   "},
			check: func(t *testing.T, def *agent.Definition) {
				if def.LLM.Model != "original-model" {
					t.Errorf("model = %q, want original-model", def.LLM.Model)
				}
			},
		},
		{
			name: "whitespace-only provider is ignored",
			meta: map[string]string{"playground.llm.provider": "  "},
			check: func(t *testing.T, def *agent.Definition) {
				if def.LLM.Provider != "original-provider" {
					t.Errorf("provider = %q, want original-provider", def.LLM.Provider)
				}
			},
		},
		{
			name: "all overrides applied simultaneously",
			meta: map[string]string{
				"playground.llm.provider":    "gemini",
				"playground.llm.model":       "gemini-1.5-pro",
				"playground.llm.temperature": "0.1",
				"playground.llm.max_tokens":  "4096",
				"playground.max_turns":       "7",
			},
			check: func(t *testing.T, def *agent.Definition) {
				if def.LLM.Provider != "gemini" {
					t.Errorf("provider = %q, want gemini", def.LLM.Provider)
				}
				if def.LLM.Model != "gemini-1.5-pro" {
					t.Errorf("model = %q, want gemini-1.5-pro", def.LLM.Model)
				}
				if def.LLM.Temperature != 0.1 {
					t.Errorf("temperature = %v, want 0.1", def.LLM.Temperature)
				}
				if def.LLM.MaxTokens != 4096 {
					t.Errorf("max_tokens = %d, want 4096", def.LLM.MaxTokens)
				}
				if def.MaxTurns != 7 {
					t.Errorf("max_turns = %d, want 7", def.MaxTurns)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			def := &agent.Definition{
				LLM: agent.LLMConfig{
					Provider:    "original-provider",
					Model:       "original-model",
					Temperature: 0.5,
					MaxTokens:   1024,
				},
				MaxTurns: 5,
			}
			applyPlaygroundOverrides(def, tc.meta)
			tc.check(t, def)
		})
	}
}

// ---------------------------------------------------------------------------
// appendSkillBuiltins — verifies read_skill + read_skill_file are appended
// ---------------------------------------------------------------------------

// populatedSkillLoader returns a few real skills for catalog testing.
type populatedSkillLoader struct {
	skills []*skill.Skill
}

func (l populatedSkillLoader) BuildCatalog() string {
	var sb strings.Builder
	for _, s := range l.skills {
		sb.WriteString(s.Name + "\n")
	}
	return sb.String()
}

func (l populatedSkillLoader) Get(name string) *skill.Skill {
	for _, s := range l.skills {
		if s.Name == name {
			return s
		}
	}
	return nil
}

func (l populatedSkillLoader) All() []*skill.Skill {
	return l.skills
}

func TestAppendSkillBuiltins_AddsReadSkillAndReadSkillFile(t *testing.T) {
	skills := []*skill.Skill{
		{Name: "csv-parser", Description: "Parses CSV data.", Body: "instructions…", Dir: t.TempDir()},
		{Name: "data-viz", Description: "Creates charts.", Body: "chart instructions…", Dir: t.TempDir()},
	}
	e := &Engine{
		skillLoader: populatedSkillLoader{skills: skills},
		log:         zap.NewNop(),
	}

	initial := []BuiltinTool{{Name: "web_search", Description: "search", Parameters: nil, Handler: nil}}
	result := e.appendSkillBuiltins(initial)

	// Should have: web_search + read_skill + read_skill_file = 3
	if len(result) != 3 {
		t.Fatalf("expected 3 tools after append, got %d: %v", len(result), toolNames(result))
	}

	names := builtinNameSet(result)
	if !names["web_search"] {
		t.Error("web_search should still be present")
	}
	if !names["read_skill"] {
		t.Error("read_skill should have been appended")
	}
	if !names["read_skill_file"] {
		t.Error("read_skill_file should have been appended")
	}
}

func TestAppendSkillBuiltins_ReadSkillGatedAsSkills(t *testing.T) {
	e := &Engine{
		skillLoader: populatedSkillLoader{skills: []*skill.Skill{
			{Name: "foo", Description: "Foo skill.", Body: "body", Dir: t.TempDir()},
		}},
		log: zap.NewNop(),
	}
	result := e.appendSkillBuiltins(nil)

	// Both appended tools must have Gate == "skills"
	for _, bt := range result {
		if bt.Gate != "skills" {
			t.Errorf("tool %q gate = %q, want 'skills'", bt.Name, bt.Gate)
		}
	}
}

func TestAppendSkillBuiltins_ReadSkillHandlerReturnsErrorForUnknownSkill(t *testing.T) {
	e := &Engine{
		skillLoader: populatedSkillLoader{skills: nil},
		log:         zap.NewNop(),
	}
	tools := e.appendSkillBuiltins(nil)

	// Find read_skill handler
	var readSkill *BuiltinTool
	for i := range tools {
		if tools[i].Name == "read_skill" {
			readSkill = &tools[i]
			break
		}
	}
	if readSkill == nil {
		t.Fatal("read_skill not found in result")
	}

	_, err := readSkill.Handler(context.Background(), map[string]any{"name": "nonexistent"})
	if err == nil {
		t.Error("expected error for unknown skill, got nil")
	}
}

func TestAppendSkillBuiltins_ReadSkillHandlerRequiresName(t *testing.T) {
	e := &Engine{
		skillLoader: fakeSkillLoader{},
		log:         zap.NewNop(),
	}
	tools := e.appendSkillBuiltins(nil)

	var readSkill *BuiltinTool
	for i := range tools {
		if tools[i].Name == "read_skill" {
			readSkill = &tools[i]
			break
		}
	}
	if readSkill == nil {
		t.Fatal("read_skill not found")
	}

	_, err := readSkill.Handler(context.Background(), map[string]any{})
	if err == nil {
		t.Error("expected error for missing name, got nil")
	}
	if !strings.Contains(err.Error(), "name is required") {
		t.Errorf("error message = %q, want 'name is required'", err.Error())
	}
}

// ---------------------------------------------------------------------------
// buildSystemPrefix — system prompt construction
// ---------------------------------------------------------------------------

func newMinimalEngine(t *testing.T) *Engine {
	t.Helper()
	dir := t.TempDir()
	loader := NewLoader([]string{dir})
	mem, err := memory.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("memory store: %v", err)
	}
	router := llm.NewRouter("test")
	e := NewEngine(loader, router, mem, nil, "", time.Second, zap.NewNop(), nil, nil, "", nil, nil, nil, nil, nil)
	e.SetPrivilegedCommandRunner(HostPrivilegedRunner{})
	if err := e.SetFilesystemRoots([]string{os.TempDir()}); err != nil {
		t.Fatalf("filesystem roots: %v", err)
	}
	return e
}

func TestBuildSystemPrefix_EmptyCatalogs(t *testing.T) {
	e := newMinimalEngine(t)
	def := &agent.Definition{
		ID:           "simple",
		SystemPrompt: "You are a simple bot.",
		// Opt out of built-ins so the always-on generate_chart guide isn't
		// appended — this test verifies that no skill/knowledge/agent CATALOG
		// block is added when the agent declares none.
		Builtins: &[]string{},
	}
	prefix := e.buildSystemPrefix(def)
	// After S1 (Cohort F), every prefix ends with the untrusted-content
	// handling rule regardless of catalog contents. The base prompt is
	// still preserved as the head.
	if !strings.HasPrefix(prefix, "You are a simple bot.") {
		t.Errorf("prefix should start with base system prompt; got %q", prefix)
	}
	if !strings.Contains(prefix, "Handling external content") {
		t.Errorf("prefix should contain S1 external-content guide; got %q", prefix)
	}
}

// TestBuildSystemPrefix_IncludesExternalContentGuide pins the S1
// guarantee: every agent, regardless of opt-outs elsewhere, inherits
// the untrusted-content handling rule so the runtime prompt is
// consistent across ReAct, plan-execute, workflow, and direct LLM loops.
func TestBuildSystemPrefix_IncludesExternalContentGuide(t *testing.T) {
	e := newMinimalEngine(t)
	cases := []*agent.Definition{
		{ID: "plain", SystemPrompt: "hi"},
		{ID: "no-builtins", SystemPrompt: "hi", Builtins: &[]string{}},
		{ID: "with-caps", SystemPrompt: "hi", Capabilities: []string{"system"}},
	}
	for _, def := range cases {
		got := e.buildSystemPrefix(def)
		if !strings.Contains(got, "<external_content") {
			t.Errorf("agent %q prefix missing external_content wrapper reference", def.ID)
		}
		if !strings.Contains(got, "MUST NOT") {
			t.Errorf("agent %q prefix missing the enforcement rule text", def.ID)
		}
	}
}

// The generate_chart guide is appended to a default agent's prompt (the tool is
// always-on), and suppressed when the agent opts out of built-ins.
func TestBuildSystemPrefix_ChartGuideByDefault(t *testing.T) {
	e := newMinimalEngine(t)
	def := &agent.Definition{ID: "charty", SystemPrompt: "Hi."}
	if got := e.buildSystemPrefix(def); !strings.Contains(got, chartToolGuide) {
		t.Errorf("default agent prefix should include the chart guide; got %q", got)
	}
	def.Builtins = &[]string{} // opt out of built-ins
	if got := e.buildSystemPrefix(def); strings.Contains(got, chartToolGuide) {
		t.Errorf("builtins-opted-out agent should NOT get the chart guide; got %q", got)
	}
}

func TestBuildSystemPrefix_WithSkills(t *testing.T) {
	skills := []*skill.Skill{
		{Name: "summarizer", Description: "Summarizes text.", Body: "summarize…", Dir: t.TempDir()},
	}
	e := newMinimalEngine(t)
	e.skillLoader = populatedSkillLoader{skills: skills}
	e.builtins = e.buildBuiltins() // rebuild with skill loader

	def := &agent.Definition{
		ID:           "skilled-agent",
		SystemPrompt: "You are helpful.",
		Skills:       []string{"summarizer"},
	}
	prefix := e.buildSystemPrefix(def)

	if !strings.Contains(prefix, "Available Skills") {
		t.Errorf("prefix should contain 'Available Skills', got:\n%s", prefix)
	}
	if !strings.Contains(prefix, "summarizer") {
		t.Errorf("prefix should contain skill name 'summarizer', got:\n%s", prefix)
	}
	if !strings.Contains(prefix, "Summarizes text.") {
		t.Errorf("prefix should contain skill description, got:\n%s", prefix)
	}
}

func TestBuildSystemPrefix_WithPeerAgents(t *testing.T) {
	dir := t.TempDir()
	loader := NewLoader([]string{dir})
	peer := &agent.Definition{
		ID:          "researcher",
		Name:        "Researcher",
		Description: "Researches topics deeply.",
		Enabled:     true,
	}
	if err := loader.Upsert(dir, peer); err != nil {
		t.Fatalf("Upsert peer: %v", err)
	}

	mem, err := memory.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("memory store: %v", err)
	}
	router := llm.NewRouter("test")
	e := NewEngine(loader, router, mem, nil, "", time.Second, zap.NewNop(), nil, nil, "", nil, nil, nil, nil, nil)

	def := &agent.Definition{
		ID:           "writer",
		SystemPrompt: "You write articles.",
		Agents:       []string{"researcher"},
	}
	prefix := e.buildSystemPrefix(def)

	if !strings.Contains(prefix, "Available Agents") {
		t.Errorf("prefix should contain 'Available Agents', got:\n%s", prefix)
	}
	if !strings.Contains(prefix, "researcher") {
		t.Errorf("prefix should contain peer agent id 'researcher', got:\n%s", prefix)
	}
}

func TestBuildSystemPrefix_NoSkillsWhenEmpty(t *testing.T) {
	e := newMinimalEngine(t)
	e.skillLoader = populatedSkillLoader{skills: []*skill.Skill{
		{Name: "csv-parser", Description: "parse CSV", Body: "instructions", Dir: t.TempDir()},
	}}
	e.builtins = e.buildBuiltins()

	def := &agent.Definition{
		ID:           "no-skills-agent",
		SystemPrompt: "Plain prompt.",
		Skills:       nil, // no skills
	}
	prefix := e.buildSystemPrefix(def)

	if strings.Contains(prefix, "Available Skills") {
		t.Error("prefix should NOT contain 'Available Skills' when agent has no skills opt-in")
	}
}

func TestBuildSystemPrefix_SystemPromptIsBaseForPrefix(t *testing.T) {
	e := newMinimalEngine(t)
	def := &agent.Definition{
		ID:           "base",
		SystemPrompt: "My unique prompt text.",
	}
	prefix := e.buildSystemPrefix(def)
	if !strings.HasPrefix(prefix, "My unique prompt text.") {
		t.Errorf("prefix should start with system prompt; got:\n%s", prefix)
	}
}

// ---------------------------------------------------------------------------
// buildContext — context assembly from history and memory
// ---------------------------------------------------------------------------

func TestBuildContext_EmptyHistory(t *testing.T) {
	e := newMinimalEngine(t)
	def := &agent.Definition{
		ID:           "ctx-test",
		SystemPrompt: "You are a context bot.",
	}
	sess := &Session{
		ID:      "test-session",
		AgentID: "ctx-test",
	}
	msg := testUserMessage("ctx-test", "test-session", "hello")

	msgs := e.buildContext(context.Background(), def, sess, msg)

	if len(msgs) == 0 {
		t.Fatal("expected at least 1 message (system prompt), got 0")
	}
	if msgs[0].Role != "system" {
		t.Errorf("first message role = %q, want 'system'", msgs[0].Role)
	}
	if !strings.Contains(msgs[0].Content, "You are a context bot.") {
		t.Errorf("system message missing system prompt; content:\n%s", msgs[0].Content)
	}
}

func TestBuildContext_IncludesSessionHistory(t *testing.T) {
	e := newMinimalEngine(t)
	def := &agent.Definition{
		ID:           "history-bot",
		SystemPrompt: "Bot.",
	}
	sess := &Session{
		ID:      "sess-hist",
		AgentID: "history-bot",
		History: []llm.ChatMessage{
			{Role: "user", Content: "first question"},
			{Role: "assistant", Content: "first answer"},
		},
	}
	msg := testUserMessage("history-bot", "sess-hist", "second question")

	msgs := e.buildContext(context.Background(), def, sess, msg)

	// Find user and assistant turns in context
	found := map[string]bool{}
	for _, m := range msgs {
		if strings.Contains(m.Content, "first question") {
			found["user"] = true
		}
		if strings.Contains(m.Content, "first answer") {
			found["assistant"] = true
		}
	}
	if !found["user"] {
		t.Error("context should contain the prior user message from history")
	}
	if !found["assistant"] {
		t.Error("context should contain the prior assistant message from history")
	}
}

func TestBuildContext_UsesCachedPrefix(t *testing.T) {
	e := newMinimalEngine(t)
	def := &agent.Definition{
		ID:           "cache-test",
		SystemPrompt: "Original prompt.",
	}
	sess := &Session{
		ID:           "sess-cache",
		AgentID:      "cache-test",
		cachedPrefix: "Cached prefix content.",
	}
	msg := testUserMessage("cache-test", "sess-cache", "hi")

	msgs := e.buildContext(context.Background(), def, sess, msg)

	if len(msgs) == 0 {
		t.Fatal("expected at least one message")
	}
	// The system message should use the cached prefix, not the def's system prompt
	if msgs[0].Content != "Cached prefix content." {
		t.Errorf("expected cached prefix; got %q", msgs[0].Content)
	}
}

func TestBuildContext_FallsBackToFreshPrefixWhenCacheEmpty(t *testing.T) {
	e := newMinimalEngine(t)
	def := &agent.Definition{
		ID:           "nocache-test",
		SystemPrompt: "Fresh prefix.",
	}
	sess := &Session{
		ID:           "sess-nocache",
		AgentID:      "nocache-test",
		cachedPrefix: "", // empty — should trigger fresh build
	}
	msg := testUserMessage("nocache-test", "sess-nocache", "hi")

	msgs := e.buildContext(context.Background(), def, sess, msg)

	if len(msgs) == 0 {
		t.Fatal("expected at least one message")
	}
	if !strings.Contains(msgs[0].Content, "Fresh prefix.") {
		t.Errorf("expected fresh system prompt; got %q", msgs[0].Content)
	}
}

// ---------------------------------------------------------------------------
// runTool dispatch paths
// ---------------------------------------------------------------------------

// TestRunTool_UnknownTool verifies that runTool returns an error when the
// tool name is not a builtin, agent prefix, MCP prefix, system tool, or
// defined Python tool.
func TestRunTool_UnknownTool(t *testing.T) {
	e := newMinimalEngine(t)
	def := &agent.Definition{
		ID:    "agent-no-tools",
		Tools: nil, // no python tools
	}

	_, err := e.runTool(context.Background(), def, "sess-x", message.ToolCall{
		ID:   "call-1",
		Name: "ghost_tool_that_does_not_exist",
	})
	if err == nil {
		t.Fatal("expected error for unknown tool, got nil")
	}
	if !strings.Contains(err.Error(), "not defined") {
		t.Errorf("error = %q, want to contain 'not defined'", err.Error())
	}
}

// TestRunTool_BuiltinHandler verifies that a registered builtin is dispatched
// to its handler correctly.
func TestRunTool_BuiltinHandler(t *testing.T) {
	e := newMinimalEngine(t)

	var called bool
	e.builtins = []BuiltinTool{{
		Name:        "my_builtin",
		Description: "test builtin",
		Parameters:  map[string]any{"type": "object"},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			called = true
			return "builtin result", nil
		},
	}}

	def := &agent.Definition{ID: "builtin-agent"}

	result, err := e.runTool(context.Background(), def, "sess-b", message.ToolCall{
		ID:        "call-b",
		Name:      "my_builtin",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("runTool: %v", err)
	}
	if !called {
		t.Error("builtin handler was not called")
	}
	if result != "builtin result" {
		t.Errorf("result = %q, want 'builtin result'", result)
	}
}

// TestRunTool_BuiltinHandlerError verifies that errors from a builtin handler
// propagate back to the caller.
func TestRunTool_BuiltinHandlerError(t *testing.T) {
	e := newMinimalEngine(t)
	e.builtins = []BuiltinTool{{
		Name:        "failing_builtin",
		Description: "always fails",
		Parameters:  map[string]any{"type": "object"},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return "", &testToolError{"deliberate failure"}
		},
	}}

	def := &agent.Definition{ID: "err-agent"}

	_, err := e.runTool(context.Background(), def, "sess-err", message.ToolCall{
		ID:   "call-e",
		Name: "failing_builtin",
	})
	if err == nil {
		t.Fatal("expected error from failing builtin, got nil")
	}
	if !strings.Contains(err.Error(), "deliberate failure") {
		t.Errorf("error = %q, want 'deliberate failure'", err.Error())
	}
}

// testToolError is a minimal error type for testing error propagation.
type testToolError struct{ msg string }

func (e *testToolError) Error() string { return e.msg }

// TestRunTool_AgentToolPrefix_UnauthorizedPeer verifies that calling
// agent__<id> for an agent NOT in the caller's peer list returns an error.
func TestRunTool_AgentToolPrefix_UnauthorizedPeer(t *testing.T) {
	dir := t.TempDir()
	loader := NewLoader([]string{dir})

	callerDef := &agent.Definition{
		ID:      "caller",
		Enabled: true,
		Agents:  nil, // no declared peers
		LLM:     agent.LLMConfig{Provider: "test", Model: "fake-model"},
	}
	targetDef := &agent.Definition{
		ID:      "secret-agent",
		Enabled: true,
		LLM:     agent.LLMConfig{Provider: "test", Model: "fake-model"},
	}
	for _, d := range []*agent.Definition{callerDef, targetDef} {
		if err := loader.Upsert(dir, d); err != nil {
			t.Fatalf("upsert %s: %v", d.ID, err)
		}
	}

	mem, _ := memory.NewFileStore(t.TempDir())
	router := llm.NewRouter("test")
	e := NewEngine(loader, router, mem, nil, "", time.Second, zap.NewNop(), nil, nil, "", nil, nil, nil, nil, nil)

	_, err := e.runTool(context.Background(), callerDef, "sess-unauth", message.ToolCall{
		ID:        "call-u",
		Name:      AgentToolPrefix + "secret-agent",
		Arguments: map[string]any{"message": "do something"},
	})
	if err == nil {
		t.Fatal("expected error for unauthorized peer, got nil")
	}
	if !strings.Contains(err.Error(), "not in this agent's declared peer list") {
		t.Errorf("error = %q, want mention of peer list", err.Error())
	}
}

// TestRunTool_AgentToolPrefix_DelegatesViaPeer verifies the full peer
// delegation path: caller declares peer, LLM emits agent__<peer> call,
// engine routes it through Handle on a sub-session.
func TestRunTool_AgentToolPrefix_DelegatesViaPeer(t *testing.T) {
	agentDir := t.TempDir()
	loader := NewLoader([]string{agentDir})

	callerDef := &agent.Definition{
		ID:      "caller-rt",
		Enabled: true,
		Agents:  []string{"peer-rt"},
		LLM:     agent.LLMConfig{Provider: "test", Model: "fake-model"},
	}
	peerDef := &agent.Definition{
		ID:       "peer-rt",
		Name:     "Peer RT",
		Enabled:  true,
		MaxTurns: 1,
		LLM:      agent.LLMConfig{Provider: "test", Model: "fake-model"},
		Builtins: strListPtr(),
	}
	for _, d := range []*agent.Definition{callerDef, peerDef} {
		if err := loader.Upsert(agentDir, d); err != nil {
			t.Fatalf("upsert %s: %v", d.ID, err)
		}
	}

	provider := &fakeHandleProvider{}
	provider.responses = []llm.CompletionResponse{
		{Content: "peer says hello"},
	}
	router := llm.NewRouter("test")
	router.Register(provider)

	mem, _ := memory.NewFileStore(t.TempDir())
	e := NewEngine(loader, router, mem, nil, "", time.Second, zap.NewNop(), nil, nil, "", nil, nil, nil, nil, nil)

	// Call the peer directly via runTool
	result, err := e.runTool(context.Background(), callerDef, "sess-rt", message.ToolCall{
		ID:        "call-peer-rt",
		Name:      AgentToolPrefix + "peer-rt",
		Arguments: map[string]any{"message": "say hello"},
	})
	if err != nil {
		t.Fatalf("runTool peer delegation: %v", err)
	}
	if !strings.Contains(result, "peer says hello") {
		t.Errorf("result = %q, want to contain 'peer says hello'", result)
	}
}

func TestRunTool_AgentToolPrefix_StructuredPeerResultEnvelope(t *testing.T) {
	agentDir := t.TempDir()
	loader := NewLoader([]string{agentDir})

	callerDef := &agent.Definition{
		ID:                    "caller-structured",
		Enabled:               true,
		Agents:                []string{"peer-structured"},
		StructuredPeerResults: true,
		LLM:                   agent.LLMConfig{Provider: "test", Model: "fake-model"},
	}
	peerDef := &agent.Definition{
		ID:       "peer-structured",
		Name:     "Peer Structured",
		Enabled:  true,
		MaxTurns: 1,
		LLM:      agent.LLMConfig{Provider: "test", Model: "fake-model"},
		Builtins: strListPtr(),
	}
	for _, d := range []*agent.Definition{callerDef, peerDef} {
		if err := loader.Upsert(agentDir, d); err != nil {
			t.Fatalf("upsert %s: %v", d.ID, err)
		}
	}

	provider := &fakeHandleProvider{}
	provider.responses = []llm.CompletionResponse{
		{Content: `{"summary":"Paris","citations":["source-a"],"confidence":"high"}`},
	}
	router := llm.NewRouter("test")
	router.Register(provider)

	mem, _ := memory.NewFileStore(t.TempDir())
	e := NewEngine(loader, router, mem, nil, "", time.Second, zap.NewNop(), nil, nil, "", nil, nil, nil, nil, nil)

	result, err := e.runTool(context.Background(), callerDef, "sess-structured", message.ToolCall{
		ID:        "call-peer-structured",
		Name:      AgentToolPrefix + "peer-structured",
		Arguments: map[string]any{"message": "summarize"},
	})
	if err != nil {
		t.Fatalf("runTool peer delegation: %v", err)
	}
	for _, want := range []string{"SOULACY_AGENT_RESULT", `"target_agent": "peer-structured"`, `"structured":`, `"confidence": "high"`} {
		if !strings.Contains(result, want) {
			t.Fatalf("structured result missing %q: %s", want, result)
		}
	}
}

// TestRunTool_SystemTool_RequiresDoubleOptIn ensures that system tools (like
// shell_exec) are NOT dispatched when either allowSystemAgents or
// def.SystemTools is false.
func TestRunTool_SystemTool_NotAvailableWithoutBothOptIns(t *testing.T) {
	e := newMinimalEngine(t)
	e.allowSystemAgents = nil // global off
	e.builtins = e.buildBuiltins()

	def := &agent.Definition{
		ID:          "sys-caller",
		SystemTools: true, // agent opts in but global is off
	}

	_, err := e.runTool(context.Background(), def, "sess-sys", message.ToolCall{
		ID:   "call-sys",
		Name: "shell_exec",
		Arguments: map[string]any{
			"command": "echo hello",
		},
	})
	// Should fail because shell_exec is not in builtins and
	// allowSystemAgents is nil, so system tools block is skipped.
	if err == nil {
		t.Fatal("expected error when global allowSystemAgents is nil, got nil")
	}
}

func TestRunTool_SystemTool_AgentOptOutBlocks(t *testing.T) {
	e := newMinimalEngine(t)
	e.allowSystemAgents = []string{"*"} // global on
	e.builtins = e.buildBuiltins()

	def := &agent.Definition{
		ID:          "no-sys-agent",
		SystemTools: false, // agent does NOT opt in
	}

	_, err := e.runTool(context.Background(), def, "sess-no-sys", message.ToolCall{
		ID:   "call-sys2",
		Name: "shell_exec",
		Arguments: map[string]any{
			"command": "echo hi",
		},
	})
	if err == nil {
		t.Fatal("expected error when agent SystemTools=false, got nil")
	}
}

// TestRunTool_PythonTool_NeitherInlineNorFileErrors verifies the edge case
// where a tool def has neither python_file nor inline script.
func TestRunTool_PythonTool_NeitherInlineNorFileErrors(t *testing.T) {
	e := newMinimalEngine(t)
	def := &agent.Definition{
		ID: "broken-agent",
		Tools: []agent.ToolDef{
			{
				Name:        "broken_tool",
				Description: "A tool with no implementation",
				// Neither PythonFile nor Inline set
			},
		},
	}

	_, err := e.runTool(context.Background(), def, "sess-broken", message.ToolCall{
		ID:   "call-broken",
		Name: "broken_tool",
	})
	if err == nil {
		t.Fatal("expected error for tool with no python_file or inline, got nil")
	}
	if !strings.Contains(err.Error(), "neither python_file nor inline") {
		t.Errorf("error = %q, want 'neither python_file nor inline'", err.Error())
	}
}

// ---------------------------------------------------------------------------
// RunTool (public) — workflow tool dispatch
// ---------------------------------------------------------------------------

func TestRunToolPublic_UnknownTool(t *testing.T) {
	e := newMinimalEngine(t)
	_, err := e.RunTool(context.Background(), "nonexistent_tool", "")
	if err == nil {
		t.Fatal("expected error for unknown tool, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want 'not found'", err.Error())
	}
}

func TestRunToolPublic_BuiltinDispatched(t *testing.T) {
	e := newMinimalEngine(t)
	var called bool
	e.builtins = []BuiltinTool{{
		Name:        "my_public_tool",
		Description: "public tool",
		Parameters:  map[string]any{"type": "object"},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			called = true
			return "public result", nil
		},
	}}

	raw, err := e.RunTool(context.Background(), "my_public_tool", `{}`)
	if err != nil {
		t.Fatalf("RunTool: %v", err)
	}
	if !called {
		t.Error("handler was not called")
	}
	if string(raw) != `"public result"` {
		t.Errorf("raw result = %s, want %q", raw, `"public result"`)
	}
}

func TestRunToolPublic_InvalidArgsJSON(t *testing.T) {
	e := newMinimalEngine(t)
	e.builtins = []BuiltinTool{{
		Name:    "parseable_tool",
		Handler: func(ctx context.Context, args map[string]any) (string, error) { return "ok", nil },
	}}
	_, err := e.RunTool(context.Background(), "parseable_tool", `{invalid json`)
	if err == nil {
		t.Fatal("expected error for invalid JSON args, got nil")
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// toolNames returns the Name of each BuiltinTool for error messages.
func toolNames(tools []BuiltinTool) []string {
	names := make([]string, len(tools))
	for i, bt := range tools {
		names[i] = bt.Name
	}
	return names
}

// builtinNameSet builds a name→true map for quick membership testing.
func builtinNameSet(tools []BuiltinTool) map[string]bool {
	out := make(map[string]bool, len(tools))
	for _, bt := range tools {
		out[bt.Name] = true
	}
	return out
}

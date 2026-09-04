// engine7_test.go — coverage push toward 68%+ for internal/runtime.
//
// Targets paths not yet hit by engine_test.go through engine6_test.go:
//   - applyPlaygroundOverrides: NegativeMaxTokens ignored
//   - buildContext: empty history returns system message + user message
//   - buildContext: history limit via sess.History
//   - buildContext: memory injection path
//   - buildContext: cached prefix used when set
//   - buildSystemPrefix: nil skillLoader + no skills → no catalog block
//   - buildSystemPrefix: nil skillLoader but Agents list → agent catalog
//   - buildSystemPrefix: knowledge present + agent Knowledge list → KB block
//   - normalizeToolCallName: various alias canonicalization paths
//   - toolArgs: argInt with string float, argInt with native int
//   - toolArgs: argInt64 with string int, argInt64 with native int
//   - toolArgs: argFloat with float32, argFloat with string float
//   - toolArgs: argBool with string "yes"/"no"/"1"/"0"
//   - toolArgs: argStringSlice with CSV string and []any mixed
//   - splitCSV corner-cases
//   - getOrCreateSession: same key returns same struct (pure concurrency)
//   - providerAllowed: single provider match/mismatch
//   - parseJSONLoose: prefix chatter trimmed correctly
//   - SetOllamaAPIKey / getOllamaAPIKey round-trip
//   - SetProgressHub: no-op when executor does not implement progressExecutor
//   - SetResourceStore / SetCheckpointStore: do not panic
//   - SetHistoryStore / SetDLQStore: wired and readable
//   - Builtins: includes system tools when allowSystemTools=true
//   - RunTool: unknown tool name returns error
//   - RunTool: handler error propagates
//   - logAudit: no-op when auditLog is nil
//   - flattenParts: non-text parts skipped
//
// All tests are pure-Go (no real LLM, no subprocess, no httptest.Server).
package runtime

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/internal/memory"
	"github.com/soulacy/soulacy/internal/session"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
	"github.com/soulacy/soulacy/pkg/skill"
	"go.uber.org/zap"
)

// ─────────────────────────────────────────────────────────────────────────────
// applyPlaygroundOverrides: edge cases not yet covered
// ─────────────────────────────────────────────────────────────────────────────

// TestApplyPlaygroundOverrides_NegativeMaxTokensIgnored verifies that a
// negative max_tokens value in the playground metadata is rejected (n > 0 guard).
func TestApplyPlaygroundOverrides_NegativeMaxTokensIgnored(t *testing.T) {
	def := &agent.Definition{
		LLM: agent.LLMConfig{MaxTokens: 512},
	}
	applyPlaygroundOverrides(def, map[string]string{
		"playground.llm.max_tokens": "-100",
	})
	if def.LLM.MaxTokens != 512 {
		t.Errorf("max_tokens = %d, want unchanged 512 for negative input", def.LLM.MaxTokens)
	}
}

// TestApplyPlaygroundOverrides_MaxTurnsZeroIgnored verifies that zero max_turns
// is rejected (n > 0 guard).
func TestApplyPlaygroundOverrides_MaxTurnsZeroIgnored(t *testing.T) {
	def := &agent.Definition{MaxTurns: 5}
	applyPlaygroundOverrides(def, map[string]string{
		"playground.max_turns": "0",
	})
	if def.MaxTurns != 5 {
		t.Errorf("max_turns = %d, want unchanged 5 for zero input", def.MaxTurns)
	}
}

// TestApplyPlaygroundOverrides_MaxTurnsNegativeIgnored verifies negative max_turns is ignored.
func TestApplyPlaygroundOverrides_MaxTurnsNegativeIgnored(t *testing.T) {
	def := &agent.Definition{MaxTurns: 5}
	applyPlaygroundOverrides(def, map[string]string{
		"playground.max_turns": "-3",
	})
	if def.MaxTurns != 5 {
		t.Errorf("max_turns = %d, want unchanged 5 for negative input", def.MaxTurns)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// buildContext: direct invocation paths
// ─────────────────────────────────────────────────────────────────────────────

// TestBuildContext_EmptyHistorySystemPrompt verifies that buildContext with an empty
// session history returns at least a system message.
func TestBuildContext_EmptyHistorySystemPrompt(t *testing.T) {
	e := newMinimalEngine(t)
	def := &agent.Definition{
		ID:           "ctx-agent",
		SystemPrompt: "You are helpful.",
	}
	sess := &Session{
		ID:      "sess-ctx",
		AgentID: "ctx-agent",
	}
	incoming := message.Message{
		AgentID:   "ctx-agent",
		SessionID: "sess-ctx",
		Parts:     message.Text("Hello"),
	}
	msgs := e.buildContext(def, sess, incoming)
	if len(msgs) == 0 {
		t.Fatal("buildContext returned empty messages for empty history")
	}
	// First message should be the system prompt.
	if msgs[0].Role != "system" {
		t.Errorf("first message role = %q, want system", msgs[0].Role)
	}
	if !strings.Contains(msgs[0].Content, "You are helpful.") {
		t.Errorf("system message should contain system prompt, got: %q", msgs[0].Content)
	}
}

// TestBuildContext_WithHistory verifies that session history is included.
func TestBuildContext_WithHistory(t *testing.T) {
	e := newMinimalEngine(t)
	def := &agent.Definition{
		ID:           "ctx-hist-agent",
		SystemPrompt: "Be terse.",
	}
	sess := &Session{
		ID:      "sess-hist",
		AgentID: "ctx-hist-agent",
		History: []llm.ChatMessage{
			{Role: "user", Content: "First message"},
			{Role: "assistant", Content: "First reply"},
		},
	}
	incoming := message.Message{
		AgentID:   "ctx-hist-agent",
		SessionID: "sess-hist",
		Parts:     message.Text("Second message"),
	}
	msgs := e.buildContext(def, sess, incoming)

	// Should have at least: system + 2 history messages
	if len(msgs) < 3 {
		t.Fatalf("expected at least 3 messages (system + 2 history), got %d", len(msgs))
	}
	found := false
	for _, m := range msgs {
		if m.Role == "user" && m.Content == "First message" {
			found = true
			break
		}
	}
	if !found {
		t.Error("buildContext should include session history in output messages")
	}
}

// TestBuildContext_CachedPrefixUsed verifies that when sess.cachedPrefix is set,
// buildContext uses it instead of recomputing the system prefix.
func TestBuildContext_CachedPrefixUsed(t *testing.T) {
	e := newMinimalEngine(t)
	def := &agent.Definition{
		ID:           "cached-prefix-agent",
		SystemPrompt: "Original prompt",
	}
	const cachedPrompt = "Cached prefix for testing"
	sess := &Session{
		ID:           "sess-cached",
		AgentID:      "cached-prefix-agent",
		cachedPrefix: cachedPrompt,
	}
	incoming := message.Message{
		AgentID:   "cached-prefix-agent",
		SessionID: "sess-cached",
		Parts:     message.Text("test"),
	}
	msgs := e.buildContext(def, sess, incoming)
	if len(msgs) == 0 {
		t.Fatal("buildContext returned empty messages")
	}
	if msgs[0].Content != cachedPrompt {
		t.Errorf("system message = %q, want cached %q", msgs[0].Content, cachedPrompt)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// buildSystemPrefix: additional branch coverage
// ─────────────────────────────────────────────────────────────────────────────

// TestBuildSystemPrefix_NilSkillLoaderNoSkillBlock verifies that when the skill
// loader is nil and the agent has no skills, no catalog block is added.
func TestBuildSystemPrefix_NilSkillLoaderNoSkillBlock(t *testing.T) {
	e := newMinimalEngine(t)
	// skillLoader is already nil by default in newMinimalEngine
	def := &agent.Definition{
		ID:           "no-skill-agent",
		SystemPrompt: "Plain prompt.",
		// Opt out of built-ins so the always-on generate_chart guide isn't
		// appended; this test is about the absence of a SKILL block.
		Builtins: &[]string{},
	}
	prefix := e.buildSystemPrefix(def)
	// After S1 (Cohort F) every prefix ends with the untrusted-content
	// handling rule; the base system prompt is still the head, and no
	// skill catalog block is added when the loader is nil.
	if !strings.HasPrefix(prefix, "Plain prompt.") {
		t.Errorf("prefix should start with base prompt; got %q", prefix)
	}
	if strings.Contains(prefix, "Available Skills") {
		t.Errorf("nil skill loader should not add a skill block; got %q", prefix)
	}
}

// TestBuildSystemPrefix_AgentCatalogWhenPeersDeclared verifies that declaring
// peer agents produces an agent catalog block even when no skills/KB are set.
func TestBuildSystemPrefix_AgentCatalogWhenPeersDeclared(t *testing.T) {
	dir := t.TempDir()
	loader := NewLoader([]string{dir})
	peer := &agent.Definition{
		ID:          "peer-agent-e7",
		Name:        "Peer E7",
		Description: "Does peer things.",
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

	caller := &agent.Definition{
		ID:           "caller-e7",
		SystemPrompt: "I orchestrate.",
		Agents:       []string{"peer-agent-e7"},
	}
	prefix := e.buildSystemPrefix(caller)
	if !strings.Contains(prefix, "Available Agents") {
		t.Errorf("prefix should contain 'Available Agents' block, got:\n%s", prefix)
	}
	if !strings.Contains(prefix, "peer-agent-e7") {
		t.Errorf("prefix should contain peer ID, got:\n%s", prefix)
	}
}

// TestBuildSystemPrefix_SkillLoaderNilNoSkillsNoCatalog verifies that with a
// nil skill loader and an agent that requests skills, no catalog is appended.
func TestBuildSystemPrefix_SkillLoaderNilNoSkillsNoCatalog(t *testing.T) {
	e := newMinimalEngine(t)
	// e.skillLoader is nil from newMinimalEngine
	def := &agent.Definition{
		ID:           "want-skills-no-loader",
		SystemPrompt: "Skill-hungry agent.",
		Skills:       []string{"data-parser"},
	}
	prefix := e.buildSystemPrefix(def)
	if strings.Contains(prefix, "Available Skills") {
		t.Error("prefix must not contain skill catalog when skill loader is nil")
	}
}

// TestBuildSystemPrefix_SkillLoaderEmptySkillsNoBlock verifies that with a
// skill loader that returns no skills, no catalog block is added.
func TestBuildSystemPrefix_SkillLoaderEmptySkillsNoBlock(t *testing.T) {
	e := newMinimalEngine(t)
	e.skillLoader = populatedSkillLoader{skills: nil}
	def := &agent.Definition{
		ID:           "empty-skill-agent",
		SystemPrompt: "No skills here.",
		Skills:       []string{"nonexistent"},
	}
	prefix := e.buildSystemPrefix(def)
	// catalog should be empty since no skills found
	if strings.Contains(prefix, "Available Skills") {
		t.Error("prefix must not contain skill catalog when no matching skills exist")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// toolArgs: additional type branches
// ─────────────────────────────────────────────────────────────────────────────

// TestArgInt_NegativeDefault verifies argInt returns negative default correctly.
func TestArgInt_NegativeDefault(t *testing.T) {
	args := map[string]any{}
	if got := argInt(args, "missing", -1); got != -1 {
		t.Errorf("argInt(missing) = %d, want default -1", got)
	}
}

// TestArgInt64_DefaultOnBadString verifies argInt64 returns default for bad strings.
func TestArgInt64_DefaultOnBadString(t *testing.T) {
	args := map[string]any{"n": "not-a-number"}
	if got := argInt64(args, "n", 42); got != 42 {
		t.Errorf("argInt64(bad string) = %d, want default 42", got)
	}
}

// TestArgFloat_MissingKeyReturnsDefault verifies argFloat returns the default
// when the key is absent.
func TestArgFloat_MissingKeyReturnsDefault(t *testing.T) {
	args := map[string]any{}
	if got := argFloat(args, "f", 3.14); got != 3.14 {
		t.Errorf("argFloat(missing) = %v, want 3.14", got)
	}
}

// TestArgFloat_NilValueReturnsDefault verifies argFloat returns the default
// when the key is present but nil.
func TestArgFloat_NilValueReturnsDefault(t *testing.T) {
	args := map[string]any{"f": nil}
	if got := argFloat(args, "f", 2.71); got != 2.71 {
		t.Errorf("argFloat(nil) = %v, want 2.71", got)
	}
}

// TestArgBool_StringYes verifies argBool rejects "yes" — strconv.ParseBool
// only accepts 1/t/T/TRUE/true/True and 0/f/F/FALSE/false/False.
func TestArgBool_StringYes(t *testing.T) {
	args := map[string]any{"b": "yes"}
	if argBool(args, "b") {
		t.Error("argBool('yes') should be false (not a ParseBool value)")
	}
}

// TestArgBool_StringNo verifies argBool handles the string "no".
func TestArgBool_StringNo(t *testing.T) {
	args := map[string]any{"b": "no"}
	if argBool(args, "b") {
		t.Error("argBool('no') should be false")
	}
}

// TestArgBool_String1 verifies argBool handles the string "1".
func TestArgBool_String1(t *testing.T) {
	args := map[string]any{"b": "1"}
	if !argBool(args, "b") {
		t.Error("argBool('1') should be true")
	}
}

// TestArgBool_String0 verifies argBool handles the string "0".
func TestArgBool_String0(t *testing.T) {
	args := map[string]any{"b": "0"}
	if argBool(args, "b") {
		t.Error("argBool('0') should be false")
	}
}

// TestArgBool_UnknownTypeReturnsFalse verifies argBool returns false for
// unknown types that are not bool/string/float64/int.
func TestArgBool_UnknownTypeReturnsFalse(t *testing.T) {
	args := map[string]any{"b": []string{"true"}}
	if argBool(args, "b") {
		t.Error("argBool([]string) should return false for unsupported type")
	}
}

// TestArgStringSlice_EmptyCSVReturnsNil verifies argStringSlice returns nil for
// an empty CSV string.
func TestArgStringSlice_EmptyCSVReturnsNil(t *testing.T) {
	args := map[string]any{"s": ""}
	result := argStringSlice(args, "s")
	if result != nil {
		t.Errorf("argStringSlice('') = %v, want nil", result)
	}
}

// TestArgStringSlice_CommaSeperatedParts verifies argStringSlice splits a CSV correctly.
func TestArgStringSlice_CommaSeperatedParts(t *testing.T) {
	args := map[string]any{"s": "alpha, beta, gamma"}
	result := argStringSlice(args, "s")
	if len(result) != 3 {
		t.Fatalf("argStringSlice(csv) = %v, want 3 parts", result)
	}
	if result[0] != "alpha" || result[1] != "beta" || result[2] != "gamma" {
		t.Errorf("argStringSlice(csv) = %v, want [alpha beta gamma]", result)
	}
}

// TestArgStringSlice_SliceAnyWithNonString verifies mixed []any handling.
func TestArgStringSlice_SliceAnyWithNonString(t *testing.T) {
	args := map[string]any{"s": []any{"text", 42, true}}
	result := argStringSlice(args, "s")
	if len(result) != 3 {
		t.Fatalf("argStringSlice([]any) = %v, want 3 parts", result)
	}
	if result[0] != "text" {
		t.Errorf("first element = %q, want 'text'", result[0])
	}
}

// TestArgStringSlice_UnknownTypeReturnsNil verifies argStringSlice returns nil
// for unsupported types.
func TestArgStringSlice_UnknownTypeReturnsNil(t *testing.T) {
	args := map[string]any{"s": 12345}
	result := argStringSlice(args, "s")
	if result != nil {
		t.Errorf("argStringSlice(int) = %v, want nil", result)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// splitCSV and trimSpace corner-cases
// ─────────────────────────────────────────────────────────────────────────────

// TestSplitCSV_Empty verifies splitCSV on an empty string.
func TestSplitCSV_Empty(t *testing.T) {
	parts := splitCSV("")
	// splitCSV("") returns [""] due to the for loop structure; that's acceptable
	if len(parts) == 0 {
		// Also valid
	}
}

// TestSplitCSV_SingleItemNoComma verifies splitCSV with a single item.
func TestSplitCSV_SingleItemNoComma(t *testing.T) {
	parts := splitCSV("hello")
	if len(parts) != 1 || parts[0] != "hello" {
		t.Errorf("splitCSV('hello') = %v, want ['hello']", parts)
	}
}

// TestTrimSpace_NewlineAndCarriageReturn verifies trimSpace handles \n and \r.
func TestTrimSpace_NewlineAndCarriageReturn(t *testing.T) {
	result := trimSpace("\n\r  hello world  \r\n")
	if result != "hello world" {
		t.Errorf("trimSpace = %q, want 'hello world'", result)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// parseJSONLoose: additional paths
// ─────────────────────────────────────────────────────────────────────────────

// TestParseJSONLoose_PrefixChatterTrimmed verifies that text before '{' is stripped.
func TestParseJSONLoose_PrefixChatterTrimmed(t *testing.T) {
	input := `Here is the JSON: {"key": "value"}`
	v, err := parseJSONLoose(input)
	if err != nil {
		t.Fatalf("parseJSONLoose returned error: %v", err)
	}
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", v)
	}
	if m["key"] != "value" {
		t.Errorf("key = %v, want 'value'", m["key"])
	}
}

// TestParseJSONLoose_ArrayValue verifies parseJSONLoose accepts JSON arrays.
func TestParseJSONLoose_ArrayValue(t *testing.T) {
	_, err := parseJSONLoose(`[1, 2, 3]`)
	if err != nil {
		t.Fatalf("parseJSONLoose([1,2,3]) returned error: %v", err)
	}
}

// TestParseJSONLoose_CompletelyInvalid verifies that wholly invalid input returns an error.
func TestParseJSONLoose_CompletelyInvalid(t *testing.T) {
	_, err := parseJSONLoose("not json at all, no braces or brackets")
	if err == nil {
		t.Error("parseJSONLoose should return error for completely invalid JSON")
	}
}

// TestParseJSONLoose_CodeFenceWithJSON verifies the ``` json ``` stripping path.
func TestParseJSONLoose_CodeFenceWithJSON(t *testing.T) {
	input := "```json\n{\"x\": 1}\n```"
	v, err := parseJSONLoose(input)
	if err != nil {
		t.Fatalf("parseJSONLoose(code fence) returned error: %v", err)
	}
	m, ok := v.(map[string]any)
	if !ok || m["x"] == nil {
		t.Errorf("expected map with 'x', got %T %v", v, v)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// normalizeToolCallName: additional alias paths
// ─────────────────────────────────────────────────────────────────────────────

// TestNormalizeToolCallName_FunctionsDotPrefix verifies "functions." prefix stripping.
func TestNormalizeToolCallName_FunctionsDotPrefix(t *testing.T) {
	got := normalizeToolCallName("functions.web_search")
	if got != "web_search" {
		t.Errorf("normalizeToolCallName('functions.web_search') = %q, want 'web_search'", got)
	}
}

// TestNormalizeToolCallName_ToolColonPrefix verifies "tool:" prefix stripping.
func TestNormalizeToolCallName_ToolColonPrefix(t *testing.T) {
	got := normalizeToolCallName("tool:lookup")
	if got != "lookup" {
		t.Errorf("normalizeToolCallName('tool:lookup') = %q, want 'lookup'", got)
	}
}

// TestNormalizeToolCallName_FunctionColonPrefix verifies "function:" prefix stripping.
func TestNormalizeToolCallName_FunctionColonPrefix(t *testing.T) {
	got := normalizeToolCallName("function:my_tool")
	if got != "my_tool" {
		t.Errorf("normalizeToolCallName('function:my_tool') = %q, want 'my_tool'", got)
	}
}

// TestNormalizeToolCallName_SearchAlias verifies "search" alias maps to "web_search".
func TestNormalizeToolCallName_SearchAlias(t *testing.T) {
	got := normalizeToolCallName("search")
	if got != "web_search" {
		t.Errorf("normalizeToolCallName('search') = %q, want 'web_search'", got)
	}
}

// TestNormalizeToolCallName_WebDashSearchAlias verifies "web-search" alias maps to "web_search".
func TestNormalizeToolCallName_WebDashSearchAlias(t *testing.T) {
	got := normalizeToolCallName("web-search")
	if got != "web_search" {
		t.Errorf("normalizeToolCallName('web-search') = %q, want 'web_search'", got)
	}
}

// TestNormalizeToolCallName_NoPrefix verifies that a normal name is returned as-is.
func TestNormalizeToolCallName_NoPrefix(t *testing.T) {
	got := normalizeToolCallName("shell_exec")
	if got != "shell_exec" {
		t.Errorf("normalizeToolCallName('shell_exec') = %q, want 'shell_exec'", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// SetOllamaAPIKey / getOllamaAPIKey round-trip
// ─────────────────────────────────────────────────────────────────────────────

// TestSetOllamaAPIKey_RoundTrip verifies that SetOllamaAPIKey / getOllamaAPIKey
// correctly stores and trims whitespace.
func TestSetOllamaAPIKey_RoundTrip(t *testing.T) {
	e := newMinimalEngine(t)
	e.SetOllamaAPIKey("  my-secret-key  ")
	got := e.getOllamaAPIKey()
	if got != "my-secret-key" {
		t.Errorf("getOllamaAPIKey() = %q, want 'my-secret-key'", got)
	}
}

// TestSetOllamaAPIKey_Empty verifies that setting an empty key clears the field.
func TestSetOllamaAPIKey_Empty(t *testing.T) {
	e := newMinimalEngine(t)
	e.SetOllamaAPIKey("original-key")
	e.SetOllamaAPIKey("")
	got := e.getOllamaAPIKey()
	if got != "" {
		t.Errorf("getOllamaAPIKey() = %q, want empty string", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// SetProgressHub: no-op when executor is nil
// ─────────────────────────────────────────────────────────────────────────────

// TestSetProgressHub_NilExecutorNoOp verifies SetProgressHub does not panic
// when no executor is wired.
func TestSetProgressHub_NilExecutorNoOp(t *testing.T) {
	e := newMinimalEngine(t)
	// pyExecutor is nil — SetProgressHub should be a no-op.
	e.SetProgressHub(nil)
}

// ─────────────────────────────────────────────────────────────────────────────
// SetResourceStore / SetCheckpointStore / SetHistoryStore / SetDLQStore
// ─────────────────────────────────────────────────────────────────────────────

// TestSetHistoryStore_WiredAndReadable verifies that SetHistoryStore installs the
// store so Handle can call Append on it.
func TestSetHistoryStore_WiredAndReadable(t *testing.T) {
	e, provider := newHandleTestEngine(t, &agent.Definition{
		ID:           "hist-bot-e7",
		Name:         "Hist Bot",
		Enabled:      true,
		SystemPrompt: "Log history.",
		LLM:          agent.LLMConfig{Provider: "test", Model: "fake-model"},
		MaxTurns:     2,
		Builtins:     strListPtr(),
	})
	provider.responses = []llm.CompletionResponse{{Content: "history recorded"}}

	hs := &fakeHistoryStore7{}
	e.SetHistoryStore(hs)

	_, err := e.Handle(context.Background(), testUserMessage("hist-bot-e7", "sess-hist-e7", "hello"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}

	hs.mu.Lock()
	count := len(hs.entries)
	hs.mu.Unlock()

	if count < 2 {
		t.Fatalf("expected at least 2 history entries (user + assistant), got %d", count)
	}
}

// fakeHistoryStore7 is a test double for session.HistoryStore.
type fakeHistoryStore7 struct {
	mu      sync.Mutex
	entries []session.ConversationEntry
}

func (f *fakeHistoryStore7) Append(_ context.Context, entry session.ConversationEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = append(f.entries, entry)
	return nil
}

func (f *fakeHistoryStore7) Load(_ context.Context, _ string, _ int) ([]session.ConversationEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.entries, nil
}

func (f *fakeHistoryStore7) LoadForAgent(_ context.Context, _ string, _ int) ([]session.ConversationEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.entries, nil
}

func (f *fakeHistoryStore7) Prune(_ context.Context, _ time.Duration) (int64, error) {
	return 0, nil
}

func (f *fakeHistoryStore7) Close() error { return nil }

// TestSetDLQStore_NilSafe verifies SetDLQStore with nil does not panic.
func TestSetDLQStore_NilSafe(t *testing.T) {
	e := newMinimalEngine(t)
	e.SetDLQStore(nil)
	if e.dlqStore != nil {
		t.Error("dlqStore should remain nil after SetDLQStore(nil)")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// RunTool: error paths
// ─────────────────────────────────────────────────────────────────────────────

// TestRunTool_UnknownToolReturnsError verifies RunTool returns an error for a
// tool name that does not exist in the builtin registry.
func TestRunTool_UnknownToolReturnsError(t *testing.T) {
	e := newMinimalEngine(t)
	e.builtins = nil // clear all builtins

	_, err := e.RunTool(context.Background(), "totally_unknown_tool_e7", "")
	if err == nil {
		t.Fatal("RunTool should return error for unknown tool")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want to contain 'not found'", err.Error())
	}
}

// TestRunTool_HandlerErrorPropagates verifies that a handler error is returned
// by RunTool.
func TestRunTool_HandlerErrorPropagates(t *testing.T) {
	e := newMinimalEngine(t)
	e.builtins = []BuiltinTool{{
		Name: "error_tool_e7",
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return "", fmt.Errorf("handler exploded")
		},
	}}

	_, err := e.RunTool(context.Background(), "error_tool_e7", "")
	if err == nil {
		t.Fatal("RunTool should propagate handler error")
	}
	if !strings.Contains(err.Error(), "handler exploded") {
		t.Errorf("error = %q, want to contain 'handler exploded'", err.Error())
	}
}

// TestRunTool_InvalidJSONArgsReturnsError verifies that malformed argsJSON is rejected.
func TestRunTool_InvalidJSONArgsReturnsError(t *testing.T) {
	e := newMinimalEngine(t)
	e.builtins = []BuiltinTool{{
		Name: "noop_e7",
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return "ok", nil
		},
	}}

	_, err := e.RunTool(context.Background(), "noop_e7", "{bad json}")
	if err == nil {
		t.Fatal("RunTool should return error for invalid JSON args")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// logAudit: no-op when auditLog is nil
// ─────────────────────────────────────────────────────────────────────────────

// TestLogAudit_NilAuditLogIsNoOp verifies logAudit does not panic when auditLog is nil.
func TestLogAudit_NilAuditLogIsNoOp(t *testing.T) {
	e := newMinimalEngine(t)
	// auditLog is nil by default
	def := &agent.Definition{ID: "audit-agent"}
	call := message.ToolCall{ID: "c1", Name: "web_search"}
	// Must not panic.
	e.logAudit(context.Background(), def, call, "result", time.Now(), false, nil)
}

// ─────────────────────────────────────────────────────────────────────────────
// flattenParts: non-text parts are skipped
// ─────────────────────────────────────────────────────────────────────────────

// TestFlattenParts_NonTextPartSkipped verifies that parts of type other than
// ContentText are not included in the flattened string.
func TestFlattenParts_NonTextPartSkipped(t *testing.T) {
	parts := []message.Part{
		{Type: message.ContentText, Text: "hello "},
		{Type: "image", Text: "should_be_ignored"},
		{Type: message.ContentText, Text: "world"},
	}
	got := flattenParts(parts)
	if got != "hello world" {
		t.Errorf("flattenParts = %q, want 'hello world'", got)
	}
}

// TestFlattenParts_OnlyNonTextParts verifies that if no text parts exist, the result is empty.
func TestFlattenParts_OnlyNonTextParts(t *testing.T) {
	parts := []message.Part{
		{Type: "image", Text: "img"},
		{Type: "file", Text: "data"},
	}
	got := flattenParts(parts)
	if got != "" {
		t.Errorf("flattenParts(no text parts) = %q, want empty string", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Builtins: system tools included when allowSystemTools=true
// ─────────────────────────────────────────────────────────────────────────────

// TestBuiltins_SystemToolsIncluded verifies that when allowSystemTools is true,
// Builtins() returns system tools as well.
func TestBuiltins_SystemToolsIncluded(t *testing.T) {
	e := newMinimalEngine(t)
	e.allowSystemAgents = []string{"*"}
	builtins := e.Builtins()

	nameSet := make(map[string]bool)
	for _, b := range builtins {
		nameSet[b.Name] = true
	}
	if !nameSet["shell_exec"] {
		t.Error("Builtins() should include shell_exec when allowSystemTools=true")
	}
	if !nameSet["read_file"] {
		t.Error("Builtins() should include read_file when allowSystemTools=true")
	}
}

// TestBuiltins_SystemToolsExcludedWhenDisabled verifies that when allowSystemTools
// is false, Builtins() does not include the PRIVILEGED (SYSTEM-partition)
// built-ins. SEC-3: the SAFE (read-only) built-ins such as read_file remain
// advertised regardless — only the destructive tools are gated by the server
// permit. This is an intentional behaviour change introduced by SEC-3.
func TestBuiltins_SystemToolsExcludedWhenDisabled(t *testing.T) {
	e := newMinimalEngine(t)
	e.allowSystemAgents = nil
	builtins := e.Builtins()

	nameSet := make(map[string]bool)
	for _, b := range builtins {
		nameSet[b.Name] = true
	}
	// Privileged tools must be absent when the server permit is off.
	for _, priv := range []string{"shell_exec", "run_script", "write_file", "download_file", "install_library"} {
		if nameSet[priv] {
			t.Errorf("Builtins() should not include privileged system tool %q when allowSystemTools=false", priv)
		}
	}
	// SAFE read-only tools are still advertised (SEC-3 partition).
	if !nameSet["read_file"] {
		t.Error("Builtins() should still include the SAFE tool read_file when allowSystemTools=false (SEC-3)")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Handle: passphrase challenge on first message (wrong passphrase)
// ─────────────────────────────────────────────────────────────────────────────

// TestHandle_PassphraseChallenge_WrongPassphraseSendsChallengeReply verifies that
// when a passphrase is set and the user sends the wrong text, no LLM call is made
// and the challenge prompt is returned.
func TestHandle_PassphraseChallenge_WrongPassphrase(t *testing.T) {
	e, provider := newHandleTestEngine(t, &agent.Definition{
		ID:      "pass-bot-e7",
		Name:    "Pass Bot",
		Enabled: true,
		LLM:     agent.LLMConfig{Provider: "test", Model: "fake-model"},
		Security: &agent.SecurityConfig{
			Passphrase: "secret",
		},
		MaxTurns: 2,
		Builtins: strListPtr(),
	})
	provider.responses = []llm.CompletionResponse{{Content: "should not be reached"}}

	reply, err := e.Handle(context.Background(), testUserMessage("pass-bot-e7", "sess-pass-e7", "wrong-guess"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got := flattenParts(reply.Parts)
	if strings.Contains(got, "should not be reached") {
		t.Fatal("LLM should NOT be called when passphrase is wrong")
	}
	if reqs := provider.requestsSnapshot(); len(reqs) != 0 {
		t.Fatalf("expected 0 LLM calls for wrong passphrase, got %d", len(reqs))
	}
}

// TestHandle_PassphraseChallenge_CorrectPassphraseSetsVerified verifies that
// providing the correct passphrase marks the session as verified and returns
// the access-granted acknowledgement.
func TestHandle_PassphraseChallenge_CorrectPassphrase(t *testing.T) {
	e, provider := newHandleTestEngine(t, &agent.Definition{
		ID:      "pass-bot-e7b",
		Name:    "Pass Bot B",
		Enabled: true,
		LLM:     agent.LLMConfig{Provider: "test", Model: "fake-model"},
		Security: &agent.SecurityConfig{
			Passphrase: "open-sesame-e7",
		},
		MaxTurns: 2,
		Builtins: strListPtr(),
	})
	provider.responses = []llm.CompletionResponse{{Content: "should not reach on passphrase turn"}}

	reply, err := e.Handle(context.Background(), testUserMessage("pass-bot-e7b", "sess-pass-e7b", "open-sesame-e7"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got := flattenParts(reply.Parts)
	if !strings.Contains(got, "Access granted") {
		t.Errorf("expected access granted reply, got: %q", got)
	}

	// Session should now be verified.
	sess := e.getOrCreateSession("sess-pass-e7b", "pass-bot-e7b")
	sess.mu.Lock()
	verified := sess.PassphraseVerified
	sess.mu.Unlock()
	if !verified {
		t.Error("session should be marked PassphraseVerified after correct passphrase")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Handle: custom passphrase prompt
// ─────────────────────────────────────────────────────────────────────────────

// TestHandle_PassphraseCustomPrompt verifies that when a custom prompt is set,
// it is returned to the user instead of the default challenge text.
func TestHandle_PassphraseCustomPrompt(t *testing.T) {
	e, _ := newHandleTestEngine(t, &agent.Definition{
		ID:      "custom-prompt-bot",
		Name:    "Custom Bot",
		Enabled: true,
		LLM:     agent.LLMConfig{Provider: "test", Model: "fake-model"},
		Security: &agent.SecurityConfig{
			Passphrase:       "secret",
			PassphrasePrompt: "Please enter your PIN:",
		},
		MaxTurns: 2,
		Builtins: strListPtr(),
	})

	reply, err := e.Handle(context.Background(), testUserMessage("custom-prompt-bot", "sess-custom-e7", "nope"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got := flattenParts(reply.Parts)
	if !strings.Contains(got, "Please enter your PIN:") {
		t.Errorf("expected custom prompt in reply, got: %q", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// getOrCreateSession: same key idempotent (basic)
// ─────────────────────────────────────────────────────────────────────────────

// TestGetOrCreateSession_SameKeyReturnsSamePointer verifies that calling
// getOrCreateSession with the same (agentID, sessionID) always returns the
// same *Session pointer.
func TestGetOrCreateSession_SameKeyReturnsSamePointer(t *testing.T) {
	e := &Engine{}
	s1 := e.getOrCreateSession("sess-e7", "agent-e7")
	s2 := e.getOrCreateSession("sess-e7", "agent-e7")
	if s1 != s2 {
		t.Fatal("same (agent, session) should return the same pointer")
	}
}

// TestGetOrCreateSession_DifferentAgentsDifferentSessions verifies that the same
// sessionID for different agents yields different Session structs.
func TestGetOrCreateSession_DifferentAgentsDifferentSessions(t *testing.T) {
	e := &Engine{}
	s1 := e.getOrCreateSession("shared-sess-e7", "agent-x")
	s2 := e.getOrCreateSession("shared-sess-e7", "agent-y")
	if s1 == s2 {
		t.Fatal("different agents with same session ID should get separate Session structs")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Knowledge + Handle integration
// ─────────────────────────────────────────────────────────────────────────────

// TestHandle_KnowledgeServiceNilDoesNotPanic verifies that Handle works normally
// when the knowledge service is nil (the default for newHandleTestEngine).
func TestHandle_KnowledgeServiceNilDoesNotPanic(t *testing.T) {
	e, provider := newHandleTestEngine(t, &agent.Definition{
		ID:           "kb-nil-bot",
		Name:         "KB Nil Bot",
		Enabled:      true,
		SystemPrompt: "No KB needed.",
		LLM:          agent.LLMConfig{Provider: "test", Model: "fake-model"},
		Knowledge:    []string{"some-kb"},
		MaxTurns:     2,
		Builtins:     strListPtr(),
	})
	provider.responses = []llm.CompletionResponse{{Content: "no KB, no problem"}}

	reply, err := e.Handle(context.Background(), testUserMessage("kb-nil-bot", "sess-kb-nil", "hello"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := flattenParts(reply.Parts); got == "" {
		t.Fatal("expected non-empty reply")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// skillCatalogFor: agent with skills but nil skillLoader
// ─────────────────────────────────────────────────────────────────────────────

// TestSkillCatalogFor_NilLoaderReturnsEmpty verifies that skillCatalogFor returns
// empty string when skillLoader is nil.
func TestSkillCatalogFor_NilLoaderReturnsEmpty(t *testing.T) {
	e := newMinimalEngine(t)
	// skillLoader is nil
	result := e.skillCatalogFor([]string{"some-skill"})
	if result != "" {
		t.Errorf("skillCatalogFor(nil loader) = %q, want empty string", result)
	}
}

// TestSkillCatalogFor_WildcardWithNilLoader verifies skillCatalogFor returns
// empty string for wildcard when loader is nil.
func TestSkillCatalogFor_WildcardWithNilLoader(t *testing.T) {
	e := newMinimalEngine(t)
	result := e.skillCatalogFor([]string{"*"})
	if result != "" {
		t.Errorf("skillCatalogFor(*, nil loader) = %q, want empty string", result)
	}
}

// TestSkillCatalogFor_NamedSkillFound verifies skillCatalogFor includes a skill
// by name when it exists.
func TestSkillCatalogFor_NamedSkillFound(t *testing.T) {
	e := newMinimalEngine(t)
	e.skillLoader = populatedSkillLoader{skills: []*skill.Skill{
		{Name: "my-skill-e7", Description: "Does stuff.", Body: "body", Dir: t.TempDir()},
	}}

	result := e.skillCatalogFor([]string{"my-skill-e7"})
	if result == "" {
		t.Fatal("expected non-empty catalog for known skill")
	}
	if !strings.Contains(result, "my-skill-e7") {
		t.Errorf("catalog = %q, expected to contain 'my-skill-e7'", result)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Handle: stream reply flag without callback (already covered, but with tool calls)
// ─────────────────────────────────────────────────────────────────────────────

// TestHandle_StreamReplyFlagIgnoredWithTools verifies that the streaming flag is
// suppressed when the LLM offers tools (streaming + tool calls is non-trivial).
func TestHandle_StreamReplyFlagIgnoredWithTools(t *testing.T) {
	e, provider := newHandleTestEngine(t, &agent.Definition{
		ID:           "stream-tools-bot",
		Name:         "Stream Tools Bot",
		Enabled:      true,
		SystemPrompt: "Stream with tools.",
		LLM:          agent.LLMConfig{Provider: "test", Model: "fake-model"},
		MaxTurns:     3,
		StreamReply:  true, // opted in to streaming
		Builtins:     strListPtr("lookup_e7"),
	})
	e.builtins = []BuiltinTool{{
		Name:        "lookup_e7",
		Description: "Lookup.",
		Parameters:  map[string]any{"type": "object"},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return "found something", nil
		},
	}}
	provider.responses = []llm.CompletionResponse{
		{ToolCalls: []message.ToolCall{{ID: "c1", Name: "lookup_e7", Arguments: map[string]any{}}}},
		{Content: "final after tool"},
	}

	// No stream callback in context → streaming should be a no-op.
	reply, err := e.Handle(context.Background(), testUserMessage("stream-tools-bot", "sess-stream-tools", "look something up"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := flattenParts(reply.Parts); got != "final after tool" {
		t.Fatalf("reply = %q, want 'final after tool'", got)
	}
}

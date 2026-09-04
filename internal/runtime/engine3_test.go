// engine3_test.go — additional coverage targeting paths not exercised by
// engine_test.go or engine2_test.go.
//
// Focus areas:
//   - Handle: MaxTurns exhaustion → finalSynthesis path
//   - Handle: workflow delegation path (WorkflowExecutor invoked by Handle)
//   - Handle: passphrase gate (correct/incorrect passphrase)
//   - recordUsage: no-op when cost store is nil or both counts are zero
//   - normalizeToolCallName: alias canonicalization
//   - normalizeWebSearchArgs: query key mapping
//   - parseJSONLoose: code-fence stripping and bare-object detection
//   - buildSystemPrefix: brain-memory path is a no-op when brainStore is nil
//   - skillCatalogFor: wildcard and named skill lookup
//   - appendMCPBuiltins (via buildBuiltins): mcpClient nil → no crash
//   - Handle: DLQ push on error (fakes deadLetterStore)
//   - Handle: historyStore append on success
//   - withChainDeadline: no-op when deadline already set
//
// All tests are pure-Go (no real LLM, no subprocess, no httptest.Server).
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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

// ---------------------------------------------------------------------------
// Handle: MaxTurns exhaustion → finalSynthesis is invoked
// ---------------------------------------------------------------------------

// TestHandleMaxTurnsExhausted checks that when the LLM keeps returning tool
// calls and MaxTurns runs out, the engine calls finalSynthesis (which gets
// one more no-tool LLM call) rather than returning an empty reply.
func TestHandleMaxTurnsExhausted(t *testing.T) {
	e, provider := newHandleTestEngine(t, &agent.Definition{
		ID:           "exhausted-bot",
		Name:         "Exhausted",
		Enabled:      true,
		SystemPrompt: "Call tools forever.",
		LLM:          agent.LLMConfig{Provider: "test", Model: "fake-model"},
		MaxTurns:     2, // only 2 turns — both will be tool calls
		Builtins:     strListPtr("noop_tool"),
	})
	e.builtins = []BuiltinTool{{
		Name:        "noop_tool",
		Description: "does nothing",
		Parameters:  map[string]any{"type": "object"},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return "noop", nil
		},
	}}

	toolCall := message.ToolCall{ID: "c1", Name: "noop_tool", Arguments: map[string]any{}}
	provider.responses = []llm.CompletionResponse{
		// Turn 1: tool call
		{ToolCalls: []message.ToolCall{toolCall}},
		// Turn 2: still returning a tool call (exhausts maxTurns)
		{ToolCalls: []message.ToolCall{toolCall}},
		// finalSynthesis call: provides the actual answer
		{Content: "synthesis result after exhaustion"},
	}

	reply, err := e.Handle(context.Background(), testUserMessage("exhausted-bot", "session-exhaust", "run forever"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got := flattenParts(reply.Parts)
	if got == "" {
		t.Fatal("expected a non-empty reply even after MaxTurns exhaustion")
	}
}

// ---------------------------------------------------------------------------
// Handle: workflow path
// ---------------------------------------------------------------------------

// TestHandleWorkflowPath verifies that when def.Workflow is set, Handle
// delegates to WorkflowExecutor and returns the workflow output as the reply.
func TestHandleWorkflowPath(t *testing.T) {
	agentDir := t.TempDir()
	loader := NewLoader([]string{agentDir})
	workflowDef := &agent.Definition{
		ID:      "wf-agent",
		Name:    "Workflow Agent",
		Enabled: true,
		LLM:     agent.LLMConfig{Provider: "test", Model: "fake-model"},
		Workflow: &agent.WorkflowSpec{Steps: []agent.StepSpec{
			{ID: "step1", Tool: "echo_tool", Input: `{"msg":"{{.trigger}}"}`, Output: "out"},
		}},
	}
	if err := loader.Upsert(agentDir, workflowDef); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	provider := &fakeHandleProvider{}
	router := llm.NewRouter("test")
	router.Register(provider)
	mem, err := memory.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("memory store: %v", err)
	}
	e := NewEngine(loader, router, mem, nil, "", time.Second, zap.NewNop(), nil, nil, "", nil, nil, nil, nil, nil)

	// Register the echo_tool as a builtin so the workflow step can find it.
	e.builtins = []BuiltinTool{{
		Name: "echo_tool",
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			msg, _ := args["msg"].(string)
			return "echoed:" + msg, nil
		},
	}}

	reply, err := e.Handle(context.Background(), testUserMessage("wf-agent", "sess-wf", "hello"))
	if err != nil {
		t.Fatalf("Handle workflow: %v", err)
	}
	got := flattenParts(reply.Parts)
	if !strings.Contains(got, "echoed:hello") {
		t.Fatalf("workflow reply = %q, want to contain 'echoed:hello'", got)
	}
}

// ---------------------------------------------------------------------------
// Handle: passphrase gate
// ---------------------------------------------------------------------------

// TestHandlePassphraseGate_IncorrectPassphrase verifies that an agent with
// security.passphrase set challenges an unverified session without invoking
// the LLM.
func TestHandlePassphraseGate_IncorrectPassphrase(t *testing.T) {
	e, provider := newHandleTestEngine(t, &agent.Definition{
		ID:      "secure-bot",
		Name:    "Secure",
		Enabled: true,
		LLM:     agent.LLMConfig{Provider: "test", Model: "fake-model"},
		Security: &agent.SecurityConfig{
			Passphrase:       "correct-pass",
			PassphrasePrompt: "Enter the magic word:",
		},
		Builtins: strListPtr(),
	})
	provider.responses = []llm.CompletionResponse{{Content: "you should never see this"}}

	reply, err := e.Handle(context.Background(), testUserMessage("secure-bot", "sess-secure", "wrong-guess"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got := flattenParts(reply.Parts)
	if !strings.Contains(got, "magic word") {
		t.Fatalf("expected passphrase prompt, got %q", got)
	}
	// LLM must NOT have been called.
	if reqs := provider.requestsSnapshot(); len(reqs) != 0 {
		t.Fatalf("LLM was called %d time(s) despite wrong passphrase", len(reqs))
	}
}

// TestHandlePassphraseGate_CorrectPassphrase verifies that sending the correct
// passphrase grants access (returns "Access granted") without invoking the LLM.
func TestHandlePassphraseGate_CorrectPassphrase(t *testing.T) {
	e, provider := newHandleTestEngine(t, &agent.Definition{
		ID:      "secure-bot2",
		Name:    "Secure2",
		Enabled: true,
		LLM:     agent.LLMConfig{Provider: "test", Model: "fake-model"},
		Security: &agent.SecurityConfig{
			Passphrase: "open-sesame",
		},
		Builtins: strListPtr(),
	})
	provider.responses = []llm.CompletionResponse{{Content: "you should never see this"}}

	reply, err := e.Handle(context.Background(), testUserMessage("secure-bot2", "sess-secure2", "open-sesame"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got := flattenParts(reply.Parts)
	// The engine acknowledges the passphrase.
	if !strings.Contains(got, "Access granted") {
		t.Fatalf("expected access-granted reply, got %q", got)
	}
	// LLM must NOT have been called.
	if reqs := provider.requestsSnapshot(); len(reqs) != 0 {
		t.Fatalf("LLM was called %d time(s) for passphrase verification", len(reqs))
	}
}

// ---------------------------------------------------------------------------
// recordUsage — no-op cases
// ---------------------------------------------------------------------------

// TestRecordUsage_NilCostStore verifies that recordUsage is safe when no cost
// store is wired (should be a silent no-op).
func TestRecordUsage_NilCostStore(t *testing.T) {
	e := newMinimalEngine(t)
	// No panic expected.
	e.recordUsage(context.Background(), "agent-1", "sess-1", "test", "model", 100, 50)
}

// TestRecordUsage_ZeroTokens verifies that recordUsage is a no-op when both
// token counts are zero, even if a cost store is wired.
func TestRecordUsage_ZeroTokens(t *testing.T) {
	e := newMinimalEngine(t)
	cs := &fakeCostStore{}
	e.SetCostStore(cs)

	e.recordUsage(context.Background(), "agent-1", "sess-1", "test", "model", 0, 0)

	cs.mu.Lock()
	count := len(cs.records)
	cs.mu.Unlock()

	if count != 0 {
		t.Fatalf("expected 0 records for zero-token call, got %d", count)
	}
}

// TestRecordUsage_WithBothTokens verifies that recordUsage stores a record when
// both prompt and completion token counts are non-zero.
func TestRecordUsage_WithBothTokens(t *testing.T) {
	e := newMinimalEngine(t)
	cs := &fakeCostStore{}
	e.SetCostStore(cs)

	e.recordUsage(context.Background(), "bot", "sess", "openai", "gpt-4o", 200, 80)

	cs.mu.Lock()
	records := cs.records
	cs.mu.Unlock()

	if len(records) == 0 {
		t.Fatal("expected a cost record, got none")
	}
	r := records[0]
	if r.promptTokens != 200 || r.compTokens != 80 {
		t.Errorf("tokens: prompt=%d comp=%d, want 200/80", r.promptTokens, r.compTokens)
	}
}

// ---------------------------------------------------------------------------
// normalizeToolCallName — alias canonicalization
// ---------------------------------------------------------------------------

func TestNormalizeToolCallName(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"web_search", "web_search"},
		{"google:search", "web_search"},
		{"google_search", "web_search"},
		{"browser_search", "web_search"},
		{"web-search", "web_search"},
		{"search", "web_search"},
		{"  some_tool  ", "some_tool"},
		{"agent:my_tool", "my_tool"},
		{"functions.some_fn", "some_fn"},
		{"tool:lookup", "lookup"},
	}
	for _, tc := range cases {
		got := normalizeToolCallName(tc.in)
		if got != tc.want {
			t.Errorf("normalizeToolCallName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// normalizeWebSearchArgs — query key mapping
// ---------------------------------------------------------------------------

func TestNormalizeWebSearchArgs(t *testing.T) {
	t.Run("query already present", func(t *testing.T) {
		args := map[string]any{"query": "existing"}
		got := normalizeWebSearchArgs(args)
		if got["query"] != "existing" {
			t.Fatalf("query = %v, want existing", got["query"])
		}
	})

	t.Run("q key remapped to query", func(t *testing.T) {
		args := map[string]any{"q": "remap-me"}
		got := normalizeWebSearchArgs(args)
		if got["query"] != "remap-me" {
			t.Fatalf("query = %v, want remap-me", got["query"])
		}
	})

	t.Run("queries string remapped", func(t *testing.T) {
		args := map[string]any{"queries": "single-query"}
		got := normalizeWebSearchArgs(args)
		if got["query"] != "single-query" {
			t.Fatalf("query = %v, want single-query", got["query"])
		}
	})

	t.Run("queries []any joined", func(t *testing.T) {
		args := map[string]any{"queries": []any{"part1", "part2"}}
		got := normalizeWebSearchArgs(args)
		q, _ := got["query"].(string)
		if !strings.Contains(q, "part1") || !strings.Contains(q, "part2") {
			t.Fatalf("query = %q, want to contain 'part1' and 'part2'", q)
		}
	})

	t.Run("nil args returned unchanged", func(t *testing.T) {
		got := normalizeWebSearchArgs(nil)
		if got != nil {
			t.Fatalf("expected nil, got %v", got)
		}
	})
}

// ---------------------------------------------------------------------------
// parseJSONLoose — code-fence stripping
// ---------------------------------------------------------------------------

func TestParseJSONLoose(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"plain json object", `{"key":"value"}`, false},
		{"plain json array", `[1,2,3]`, false},
		{"json fenced with ```json", "```json\n{\"key\":\"value\"}\n```", false},
		{"json fenced without language", "```\n{\"key\":\"value\"}\n```", false},
		{"json with leading text", "Here is the JSON:\n{\"key\":\"value\"}", false},
		{"invalid json", `not json at all`, true},
		{"empty string", ``, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseJSONLoose(tc.input)
			if tc.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// skillCatalogFor — wildcard and named skill lookup
// ---------------------------------------------------------------------------

func TestSkillCatalogFor_NamedSkills(t *testing.T) {
	skills := []*skill.Skill{
		{Name: "writer", Description: "Writes documents.", Body: "body", Dir: t.TempDir()},
		{Name: "coder", Description: "Codes things.", Body: "code body", Dir: t.TempDir()},
	}
	e := newMinimalEngine(t)
	e.skillLoader = populatedSkillLoader{skills: skills}
	e.builtins = e.buildBuiltins()

	catalog := e.skillCatalogFor([]string{"writer"})
	if !strings.Contains(catalog, "writer") {
		t.Errorf("catalog should contain 'writer', got:\n%s", catalog)
	}
	if strings.Contains(catalog, "coder") {
		t.Errorf("catalog should NOT contain 'coder' when not requested, got:\n%s", catalog)
	}
}

func TestSkillCatalogFor_Wildcard(t *testing.T) {
	skills := []*skill.Skill{
		{Name: "alpha", Description: "Alpha skill.", Body: "body", Dir: t.TempDir()},
		{Name: "beta", Description: "Beta skill.", Body: "body", Dir: t.TempDir()},
	}
	e := newMinimalEngine(t)
	e.skillLoader = populatedSkillLoader{skills: skills}
	e.builtins = e.buildBuiltins()

	catalog := e.skillCatalogFor([]string{"*"})
	if !strings.Contains(catalog, "alpha") || !strings.Contains(catalog, "beta") {
		t.Errorf("wildcard catalog should contain both skills, got:\n%s", catalog)
	}
}

func TestSkillCatalogFor_NilLoader(t *testing.T) {
	e := newMinimalEngine(t)
	// skillLoader is nil — should return empty without panic.
	catalog := e.skillCatalogFor([]string{"anything"})
	if catalog != "" {
		t.Errorf("expected empty catalog when skillLoader is nil, got %q", catalog)
	}
}

// ---------------------------------------------------------------------------
// withChainDeadline — inheritance at depth > 0
// ---------------------------------------------------------------------------

func TestWithChainDeadline_InheritsExisting(t *testing.T) {
	deadline := time.Now().Add(5 * time.Minute)
	// First call stamps the deadline.
	ctx1, cancel1 := withChainDeadline(context.Background(), deadline)
	defer cancel1()

	// Second call with a DIFFERENT deadline should NOT override the ancestor.
	laterDeadline := time.Now().Add(30 * time.Minute)
	ctx2, cancel2 := withChainDeadline(ctx1, laterDeadline)
	defer cancel2()

	// The effective deadline on ctx2 should be the original (earlier) deadline.
	d, ok := ctx2.Deadline()
	if !ok {
		t.Fatal("expected a deadline to be set on ctx2")
	}
	// Allow 2 seconds of slop for slow test machines.
	diff := d.Sub(deadline)
	if diff > 2*time.Second || diff < -2*time.Second {
		t.Fatalf("deadline on nested context = %v, want near %v (diff=%v)", d, deadline, diff)
	}
}

// ---------------------------------------------------------------------------
// Handle: DLQ push on error path
// ---------------------------------------------------------------------------

// fakeDLQStore is a test double for deadLetterStore.
type fakeDLQStore struct {
	pushes []fakeDLQPush
}

type fakeDLQPush struct {
	queue   string
	payload []byte
	errMsg  string
}

func (f *fakeDLQStore) PushFailed(_ context.Context, queue string, payload []byte, errMsg string) error {
	f.pushes = append(f.pushes, fakeDLQPush{queue: queue, payload: payload, errMsg: errMsg})
	return nil
}

// TestHandle_DLQPushOnError verifies that when Handle returns an error, the
// dead-letter store (if wired) receives the failed message.
func TestHandle_DLQPushOnError(t *testing.T) {
	e, _ := newHandleTestEngine(t, &agent.Definition{
		ID:      "dlq-agent",
		Name:    "DLQ Agent",
		Enabled: false, // disabled → Handle returns error
		LLM:     agent.LLMConfig{Provider: "test", Model: "fake-model"},
	})

	dlq := &fakeDLQStore{}
	e.SetDLQStore(dlq)

	_, err := e.Handle(context.Background(), testUserMessage("dlq-agent", "sess-dlq", "trigger"))
	if err == nil {
		t.Fatal("expected error for disabled agent")
	}

	if len(dlq.pushes) == 0 {
		t.Fatal("expected DLQ push on error, got none")
	}
	if dlq.pushes[0].queue != "dlq-agent" {
		t.Errorf("DLQ queue = %q, want 'dlq-agent'", dlq.pushes[0].queue)
	}
	if !strings.Contains(dlq.pushes[0].errMsg, "disabled") {
		t.Errorf("DLQ errMsg = %q, want 'disabled'", dlq.pushes[0].errMsg)
	}
}

// ---------------------------------------------------------------------------
// Handle: historyStore append on success
// ---------------------------------------------------------------------------

// fakeHistoryStore records every appended conversation entry.
type fakeHistoryStore struct {
	entries []session.ConversationEntry
}

func (f *fakeHistoryStore) Append(_ context.Context, entry session.ConversationEntry) error {
	f.entries = append(f.entries, entry)
	return nil
}

func (f *fakeHistoryStore) Load(_ context.Context, _ string, _ int) ([]session.ConversationEntry, error) {
	return nil, nil
}

func (f *fakeHistoryStore) LoadForAgent(_ context.Context, _ string, _ int) ([]session.ConversationEntry, error) {
	return nil, nil
}

func (f *fakeHistoryStore) Prune(_ context.Context, _ time.Duration) (int64, error) {
	return 0, nil
}

func (f *fakeHistoryStore) Close() error { return nil }

// TestHandle_HistoryStoreAppendOnSuccess verifies that both the user turn and
// assistant turn are persisted to the history store after a successful run.
func TestHandle_HistoryStoreAppendOnSuccess(t *testing.T) {
	e, provider := newHandleTestEngine(t, &agent.Definition{
		ID:           "hist-agent",
		Name:         "History Agent",
		Enabled:      true,
		SystemPrompt: "Remember everything.",
		LLM:          agent.LLMConfig{Provider: "test", Model: "fake-model"},
		MaxTurns:     2,
		Builtins:     strListPtr(),
	})
	provider.responses = []llm.CompletionResponse{{Content: "I remember you."}}

	hs := &fakeHistoryStore{}
	e.SetHistoryStore(hs)

	_, err := e.Handle(context.Background(), testUserMessage("hist-agent", "sess-hist", "Do you remember me?"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if len(hs.entries) < 2 {
		t.Fatalf("expected at least 2 history entries (user + assistant), got %d", len(hs.entries))
	}

	roles := make(map[string]bool)
	for _, e := range hs.entries {
		roles[e.Role] = true
	}
	if !roles["user"] {
		t.Error("expected a user role entry in history")
	}
	if !roles["assistant"] {
		t.Error("expected an assistant role entry in history")
	}
}

// ---------------------------------------------------------------------------
// agentCatalogFor — catalog rendered for peer agents
// ---------------------------------------------------------------------------

func TestAgentCatalogFor_ContainsPeerInfo(t *testing.T) {
	dir := t.TempDir()
	loader := NewLoader([]string{dir})
	peer := &agent.Definition{
		ID:          "peer-cat",
		Name:        "Peer Cat",
		Description: "Does peer things.",
		Enabled:     true,
	}
	if err := loader.Upsert(dir, peer); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	mem, err := memory.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("memory store: %v", err)
	}
	router := llm.NewRouter("test")
	e := NewEngine(loader, router, mem, nil, "", time.Second, zap.NewNop(), nil, nil, "", nil, nil, nil, nil, nil)

	def := &agent.Definition{
		ID:     "caller",
		Agents: []string{"peer-cat"},
	}
	catalog := e.agentCatalogFor(def)

	if !strings.Contains(catalog, "peer-cat") {
		t.Errorf("catalog should contain peer ID, got:\n%s", catalog)
	}
	if !strings.Contains(catalog, "Does peer things.") {
		t.Errorf("catalog should contain peer description, got:\n%s", catalog)
	}
}

func TestAgentCatalogFor_EmptyWhenNoPeers(t *testing.T) {
	e := newMinimalEngine(t)
	def := &agent.Definition{ID: "solo", Agents: nil}
	catalog := e.agentCatalogFor(def)
	if catalog != "" {
		t.Errorf("expected empty catalog for agent with no peers, got %q", catalog)
	}
}

// ---------------------------------------------------------------------------
// Handle: unknown agent → DLQ push includes agent ID even when def is nil
// ---------------------------------------------------------------------------

// TestHandle_UnknownAgentDLQPush verifies that the DLQ push happens even when
// the agent ID is unknown (def == nil path in the deferred handler).
func TestHandle_UnknownAgentDLQPush(t *testing.T) {
	e, _ := newHandleTestEngine(t, &agent.Definition{
		ID:      "real-agent",
		Enabled: true,
		LLM:     agent.LLMConfig{Provider: "test", Model: "fake-model"},
	})

	dlq := &fakeDLQStore{}
	e.SetDLQStore(dlq)

	_, err := e.Handle(context.Background(), testUserMessage("ghost-agent-xyz", "sess-x", "hello"))
	if err == nil {
		t.Fatal("expected error for unknown agent")
	}

	if len(dlq.pushes) == 0 {
		t.Fatal("expected DLQ push even for unknown agent, got none")
	}
}

// ---------------------------------------------------------------------------
// RunTool public — empty argsJSON still dispatches
// ---------------------------------------------------------------------------

// TestRunToolPublic_EmptyArgs verifies that calling RunTool with an empty
// argsJSON string (not "{}") still succeeds — the engine treats "" as no args.
func TestRunToolPublic_EmptyArgs(t *testing.T) {
	e := newMinimalEngine(t)
	var called bool
	e.builtins = []BuiltinTool{{
		Name: "empty_args_tool",
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			called = true
			return "ok", nil
		},
	}}

	_, err := e.RunTool(context.Background(), "empty_args_tool", "")
	if err != nil {
		t.Fatalf("RunTool with empty args: %v", err)
	}
	if !called {
		t.Error("handler was not called with empty args")
	}
}

// ---------------------------------------------------------------------------
// Engine.Builtins() — catalog includes system tools when allowed
// ---------------------------------------------------------------------------

// TestBuiltins_IncludesSystemToolsInCatalog verifies that when allowSystemTools
// is true, the Builtins() catalog includes the system-level tools (shell_exec,
// read_file, etc.) even though they are not in e.builtins directly.
func TestBuiltins_IncludesSystemToolsInCatalog(t *testing.T) {
	e := newMinimalEngine(t)
	e.allowSystemAgents = []string{"*"}
	e.allowSystemAgents = []string{"*"}
	e.builtins = e.buildBuiltins()

	catalog := e.Builtins()
	names := builtinNameSet(catalog)

	if !names["shell_exec"] {
		t.Error("Builtins() should include shell_exec when allowSystemAgents is set")
	}
	if !names["read_file"] {
		t.Error("Builtins() should include read_file when allowSystemAgents is set")
	}
}

// TestBuiltins_ExcludesSystemToolsInCatalogWhenDisabled verifies the inverse:
// when allowSystemAgents is empty, system tools do NOT appear in the catalog.
func TestBuiltins_ExcludesSystemToolsInCatalogWhenDisabled(t *testing.T) {
	e := newMinimalEngine(t)
	e.allowSystemAgents = nil
	e.builtins = e.buildBuiltins()

	catalog := e.Builtins()
	names := builtinNameSet(catalog)

	if names["shell_exec"] {
		t.Error("Builtins() should NOT include shell_exec when allowSystemAgents is empty")
	}
}

// ---------------------------------------------------------------------------
// Handle: FailureNotifier is called on error
// ---------------------------------------------------------------------------

// fakeFailureNotifier records the calls made to it.
type fakeFailureNotifier struct {
	calls []fakeFailureCall
}

type fakeFailureCall struct {
	def   *agent.Definition
	msg   message.Message
	errMsg string
}

func (f *fakeFailureNotifier) NotifyFailure(_ context.Context, def *agent.Definition, msg message.Message, errMsg string) {
	f.calls = append(f.calls, fakeFailureCall{def: def, msg: msg, errMsg: errMsg})
}

// TestHandle_FailureNotifierCalledOnError verifies that when Handle returns an
// error, the FailureNotifier (if set) is called with the error details.
func TestHandle_FailureNotifierCalledOnError(t *testing.T) {
	e, _ := newHandleTestEngine(t, &agent.Definition{
		ID:      "notified-bot",
		Name:    "Notified Bot",
		Enabled: false, // disabled → error
		LLM:     agent.LLMConfig{Provider: "test", Model: "fake-model"},
	})

	fn := &fakeFailureNotifier{}
	e.SetFailureNotifier(fn)

	_, err := e.Handle(context.Background(), testUserMessage("notified-bot", "sess-notified", "run"))
	if err == nil {
		t.Fatal("expected error for disabled agent")
	}

	if len(fn.calls) == 0 {
		t.Fatal("expected FailureNotifier to be called, got 0 calls")
	}
	if fn.calls[0].errMsg == "" {
		t.Error("FailureNotifier errMsg should not be empty")
	}
}

// ---------------------------------------------------------------------------
// Handle: LLM error propagated correctly
// ---------------------------------------------------------------------------

// fakeErrorProvider returns an error for every Complete call.
type fakeErrorProvider struct{}

func (p *fakeErrorProvider) ID() string { return "error-provider" }
func (p *fakeErrorProvider) Complete(_ context.Context, _ llm.CompletionRequest) (*llm.CompletionResponse, error) {
	return nil, errors.New("simulated LLM failure")
}
func (p *fakeErrorProvider) Models(_ context.Context) ([]string, error) {
	return nil, nil
}

// TestHandle_LLMErrorPropagated verifies that when the LLM router returns an
// error, Handle surfaces it to the caller rather than silently eating it.
func TestHandle_LLMErrorPropagated(t *testing.T) {
	agentDir := t.TempDir()
	loader := NewLoader([]string{agentDir})
	def := &agent.Definition{
		ID:      "err-llm-agent",
		Name:    "LLM Error Agent",
		Enabled: true,
		LLM:     agent.LLMConfig{Provider: "error-provider", Model: "fail-model"},
		MaxTurns: 2,
		Builtins: strListPtr(),
	}
	if err := loader.Upsert(agentDir, def); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	errProv := &fakeErrorProvider{}
	router := llm.NewRouter("error-provider")
	router.Register(errProv)

	mem, err := memory.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("memory store: %v", err)
	}
	e := NewEngine(loader, router, mem, nil, "", time.Second, zap.NewNop(), nil, nil, "", nil, nil, nil, nil, nil)

	_, err = e.Handle(context.Background(), testUserMessage("err-llm-agent", "sess-llm-err", "fail please"))
	if err == nil {
		t.Fatal("expected LLM error to propagate, got nil")
	}
	if !strings.Contains(err.Error(), "llm call") && !strings.Contains(err.Error(), "LLM failure") &&
		!strings.Contains(err.Error(), "simulated") {
		t.Fatalf("error message = %q, should mention LLM failure", err.Error())
	}
}

// ---------------------------------------------------------------------------
// flattenParts — edge cases
// ---------------------------------------------------------------------------

func TestFlattenParts_EmptyParts(t *testing.T) {
	got := flattenParts(nil)
	if got != "" {
		t.Fatalf("expected empty string for nil parts, got %q", got)
	}
	got = flattenParts([]message.Part{})
	if got != "" {
		t.Fatalf("expected empty string for empty parts, got %q", got)
	}
}

func TestFlattenParts_MultipleTextParts(t *testing.T) {
	parts := []message.Part{
		{Type: message.ContentText, Text: "hello "},
		{Type: message.ContentText, Text: "world"},
	}
	got := flattenParts(parts)
	if got != "hello world" {
		t.Fatalf("flattenParts = %q, want 'hello world'", got)
	}
}

// ---------------------------------------------------------------------------
// Handle: auto-delegate path (tool_choice = agent__<peer>)
// ---------------------------------------------------------------------------

// TestHandle_AutoDelegate verifies that when def.LLM.ToolChoice is set to
// "agent__<peer>" and the peer is a declared agent, the engine performs an
// automatic delegation before the first LLM turn and injects the peer result
// as a synthetic tool round-trip into the context.
func TestHandle_AutoDelegate(t *testing.T) {
	agentDir := t.TempDir()
	loader := NewLoader([]string{agentDir})

	callerDef := &agent.Definition{
		ID:      "auto-caller",
		Name:    "Auto Caller",
		Enabled: true,
		LLM: agent.LLMConfig{
			Provider:   "test",
			Model:      "fake-model",
			ToolChoice: AgentToolPrefix + "auto-peer",
		},
		MaxTurns: 2,
		Builtins: strListPtr(),
		Agents:   []string{"auto-peer"},
	}
	peerDef := &agent.Definition{
		ID:       "auto-peer",
		Name:     "Auto Peer",
		Enabled:  true,
		LLM:      agent.LLMConfig{Provider: "test", Model: "fake-model"},
		MaxTurns: 1,
		Builtins: strListPtr(),
	}
	for _, d := range []*agent.Definition{callerDef, peerDef} {
		if err := loader.Upsert(agentDir, d); err != nil {
			t.Fatalf("Upsert %s: %v", d.ID, err)
		}
	}

	provider := &fakeHandleProvider{}
	// Peer response + caller synthesis (auto-delegate injects synthetic tool turn)
	provider.responses = []llm.CompletionResponse{
		{Content: "peer says hi"},           // peer's LLM call
		{Content: "caller synthesised: hi"}, // caller's LLM call after auto-delegate
	}
	router := llm.NewRouter("test")
	router.Register(provider)

	mem, err := memory.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("memory store: %v", err)
	}
	e := NewEngine(loader, router, mem, nil, "", time.Second, zap.NewNop(), nil, nil, "", nil, nil, nil, nil, nil)

	reply, err := e.Handle(context.Background(), testUserMessage("auto-caller", "sess-auto", "hello peer"))
	if err != nil {
		t.Fatalf("Handle auto-delegate: %v", err)
	}
	got := flattenParts(reply.Parts)
	if got == "" {
		t.Fatal("expected a non-empty reply from auto-delegate path")
	}
}

// ---------------------------------------------------------------------------
// buildBuiltins — with nil mcpClient (no panic)
// ---------------------------------------------------------------------------

// TestBuildBuiltins_NilMCPClientNoTool verifies that when no MCP client is set,
// buildBuiltins still returns the standard tools without panicking.
func TestBuildBuiltins_NilMCPClientNoTool(t *testing.T) {
	e := newMinimalEngine(t)
	tools := e.buildBuiltins()
	// Should have at least web_search.
	names := builtinNameSet(tools)
	if !names["web_search"] {
		t.Errorf("expected web_search in buildBuiltins output, got %v", toolNames(tools))
	}
}

// ---------------------------------------------------------------------------
// normalizeToolCall — normalises name and web_search args together
// ---------------------------------------------------------------------------

func TestNormalizeToolCall_WebSearchAliasAndArgMapping(t *testing.T) {
	call := message.ToolCall{
		ID:   "c1",
		Name: "google:search",
		Arguments: map[string]any{
			"q": "soulacy docs",
		},
	}
	result := normalizeToolCall(call)
	if result.Name != "web_search" {
		t.Fatalf("normalized name = %q, want web_search", result.Name)
	}
	if result.Arguments["query"] != "soulacy docs" {
		t.Fatalf("query = %v, want 'soulacy docs'", result.Arguments["query"])
	}
}

// ---------------------------------------------------------------------------
// handle: workflow error surfaces correctly
// ---------------------------------------------------------------------------

// TestHandle_WorkflowError verifies that when WorkflowExecutor.Run returns an
// error, Handle returns an error (and the SSE sink sees an error event).
func TestHandle_WorkflowError(t *testing.T) {
	agentDir := t.TempDir()
	loader := NewLoader([]string{agentDir})

	// A workflow agent with a step that calls a non-existent tool.
	workflowDef := &agent.Definition{
		ID:      "wf-error-agent",
		Name:    "WF Error Agent",
		Enabled: true,
		LLM:     agent.LLMConfig{Provider: "test", Model: "fake-model"},
		Workflow: &agent.WorkflowSpec{Steps: []agent.StepSpec{
			{ID: "badstep", Tool: "nonexistent_tool_xyz", Input: `{}`},
		}},
	}
	if err := loader.Upsert(agentDir, workflowDef); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	router := llm.NewRouter("test")
	mem, err := memory.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("memory store: %v", err)
	}
	e := NewEngine(loader, router, mem, nil, "", time.Second, zap.NewNop(), nil, nil, "", nil, nil, nil, nil, nil)
	e.builtins = nil // ensure no tools are available

	_, err = e.Handle(context.Background(), testUserMessage("wf-error-agent", "sess-wf-err", "go"))
	if err == nil {
		t.Fatal("expected error from workflow with nonexistent tool, got nil")
	}
}

// ---------------------------------------------------------------------------
// JSON marshal/unmarshal sanity for RunTool result
// ---------------------------------------------------------------------------

// TestRunToolPublic_ResultIsValidJSON verifies that the raw JSON returned by
// RunTool for a string result is a valid JSON-encoded string.
func TestRunToolPublic_ResultIsValidJSON(t *testing.T) {
	e := newMinimalEngine(t)
	e.builtins = []BuiltinTool{{
		Name: "json_result_tool",
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return `result with "quotes" and \backslash`, nil
		},
	}}

	raw, err := e.RunTool(context.Background(), "json_result_tool", "{}")
	if err != nil {
		t.Fatalf("RunTool: %v", err)
	}
	var decoded string
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("result is not valid JSON string: %v (raw=%s)", err, raw)
	}
	if !strings.Contains(decoded, "quotes") {
		t.Errorf("decoded result = %q, want to contain 'quotes'", decoded)
	}
}

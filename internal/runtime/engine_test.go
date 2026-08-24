// engine_test.go — regression tests for the engine's most-bitten code paths.
// Avoids spinning up a real LLM (no network in CI) by exercising internal
// helpers directly. The full agent loop is covered by manual smoke tests
// in docs/REGRESSION_TESTING.md.
package runtime

import (
	"context"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/knowledge"
	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/internal/memory"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
	"github.com/soulacy/soulacy/pkg/skill"
	"go.uber.org/zap"
)

// TestGetOrCreateSession_IsolatesByAgent guards against the session-bleed
// regression: before the fix, sessions were keyed by sessionID alone, so two
// different agents using the same fixed http session id ("http-gui-user")
// shared the same in-memory History. The fix keys by (agentID, sessionID).
func TestGetOrCreateSession_IsolatesByAgent(t *testing.T) {
	e := &Engine{}

	a := e.getOrCreateSession("shared-session-id", "agent-a")
	b := e.getOrCreateSession("shared-session-id", "agent-b")

	if a == b {
		t.Fatal("two agents with same session id MUST get distinct Session structs")
	}
	if a.AgentID != "agent-a" || b.AgentID != "agent-b" {
		t.Errorf("agent ids on sessions wrong: a=%q b=%q", a.AgentID, b.AgentID)
	}

	// Same agent + same session id should return the same struct.
	again := e.getOrCreateSession("shared-session-id", "agent-a")
	if again != a {
		t.Error("same (agent, session) lookup should return the cached session")
	}
}

func TestFinalizeReplySanitizesControlEnvelope(t *testing.T) {
	mem, err := memory.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("memory store: %v", err)
	}
	sink := &captureSink6{}
	e := NewEngine(nil, nil, mem, nil, "", time.Second, zap.NewNop(), sink, nil, "", nil, nil, nil, nil, nil)
	def := &agent.Definition{ID: "stock-advisor", Name: "Stock Advisor", Enabled: true}
	sess := &Session{ID: "sess-1", AgentID: def.ID, CreatedAt: time.Now().UTC()}
	msg := testUserMessage(def.ID, sess.ID, "What are the best stocks if I have $33K?")
	raw := `{"thought":"I have enough data","is_done":true,"final_answer":"## Best Stocks to Invest $33K\n\n| Ticker | Allocation |\n| --- | ---: |\n| V | $8,000 |"}`

	reply := e.finalizeReply(context.Background(), def, sess, msg, raw)
	got := flattenParts(reply.Parts)
	if !strings.HasPrefix(got, "## Best Stocks to Invest $33K") {
		t.Fatalf("reply was not unwrapped: %q", got)
	}
	if strings.Contains(got, `"thought"`) || strings.Contains(got, `"final_answer"`) {
		t.Fatalf("control envelope leaked into reply: %q", got)
	}
	outs := eventsByType(sink.events, "message.out")
	if len(outs) != 1 {
		t.Fatalf("message.out events = %d, want 1", len(outs))
	}
	emitted := flattenEventText(t, outs[0])
	if emitted != got {
		t.Fatalf("message.out payload = %q, reply = %q", emitted, got)
	}
}

func TestFinalizeReplyKeepsCollectedChartInSessionHistory(t *testing.T) {
	mem, err := memory.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("memory store: %v", err)
	}
	e := NewEngine(nil, nil, mem, nil, "", time.Second, zap.NewNop(), &captureSink6{}, nil, "", nil, nil, nil, nil, nil)
	def := &agent.Definition{ID: "weather-advisor", Name: "Weather Advisor", Enabled: true}
	sess := &Session{ID: "sess-chart", AgentID: def.ID, CreatedAt: time.Now().UTC()}
	msg := testUserMessage(def.ID, sess.ID, "Show the weekly temperature trend")
	ctx := withChartSink(context.Background())
	chart := "```chart\n{\"series\":[{\"type\":\"bar\",\"data\":[31,35,39]}]}\n```"
	chartSinkFrom(ctx).add(chart)

	reply := e.finalizeReply(ctx, def, sess, msg, "Here is the forecast trend.")
	got := flattenParts(reply.Parts)
	if !strings.Contains(got, chart) {
		t.Fatalf("reply does not contain collected chart: %q", got)
	}
	if len(sess.History) != 1 {
		t.Fatalf("session history entries = %d, want 1", len(sess.History))
	}
	if sess.History[0].Content != got {
		t.Fatalf("session history lost chart:\n history=%q\n reply=%q", sess.History[0].Content, got)
	}
}

func eventsByType(events []message.Event, typ string) []message.Event {
	var out []message.Event
	for _, ev := range events {
		if ev.Type == typ {
			out = append(out, ev)
		}
	}
	return out
}

func flattenEventText(t *testing.T, ev message.Event) string {
	t.Helper()
	msg, ok := ev.Payload.(message.Message)
	if !ok {
		t.Fatalf("event payload type = %T, want message.Message", ev.Payload)
	}
	return flattenParts(msg.Parts)
}

// TestAgentCallDepth_Roundtrip verifies the context.Value-based depth counter
// used by runAgentCall to bound recursion.
func TestAgentCallDepth_Roundtrip(t *testing.T) {
	ctx := context.Background()
	if got := agentCallDepth(ctx); got != 0 {
		t.Errorf("fresh context: got depth %d, want 0", got)
	}
	ctx = withAgentCallDepth(ctx, 3)
	if got := agentCallDepth(ctx); got != 3 {
		t.Errorf("after withAgentCallDepth(3): got %d, want 3", got)
	}
	// Nesting overrides the inner value.
	inner := withAgentCallDepth(ctx, 5)
	if got := agentCallDepth(inner); got != 5 {
		t.Errorf("nested: got %d, want 5", got)
	}
	// Outer context unchanged.
	if got := agentCallDepth(ctx); got != 3 {
		t.Errorf("outer after inner overwrite: got %d, want 3", got)
	}
}

// TestResolveAgentRefs covers wildcard expansion, self-exclusion, and the
// disabled-peer filter — all of which protect against accidental loops and
// silently-broken delegations.
func TestResolveAgentRefs(t *testing.T) {
	dir := t.TempDir()
	l := NewLoader([]string{dir})
	// Pre-load three agents directly into the loader registry.
	defs := []*agent.Definition{
		{ID: "alpha", Enabled: true},
		{ID: "beta", Enabled: true},
		{ID: "disabled-one", Enabled: false},
	}
	for _, d := range defs {
		if err := l.Upsert(dir, d); err != nil {
			t.Fatalf("Upsert %s: %v", d.ID, err)
		}
	}

	e := &Engine{loader: l}

	// Wildcard: should return alpha and beta (disabled excluded, self excluded
	// when caller matches).
	got := e.resolveAgentRefs([]string{"*"}, "alpha")
	if len(got) != 1 || got[0].ID != "beta" {
		t.Errorf("wildcard from alpha: got %v, want [beta]", agentIDs(got))
	}

	// "all" is a synonym for "*".
	got = e.resolveAgentRefs([]string{"all"}, "beta")
	if len(got) != 1 || got[0].ID != "alpha" {
		t.Errorf("'all' from beta: got %v, want [alpha]", agentIDs(got))
	}

	// Explicit self-reference is dropped (no one-step infinite loop).
	got = e.resolveAgentRefs([]string{"alpha", "beta"}, "alpha")
	if len(got) != 1 || got[0].ID != "beta" {
		t.Errorf("self-ref drop: got %v, want [beta]", agentIDs(got))
	}

	// Unknown ids silently dropped.
	got = e.resolveAgentRefs([]string{"alpha", "ghost"}, "beta")
	if len(got) != 1 || got[0].ID != "alpha" {
		t.Errorf("unknown id drop: got %v, want [alpha]", agentIDs(got))
	}

	// Empty list returns nil.
	if got := e.resolveAgentRefs(nil, "x"); got != nil {
		t.Errorf("nil refs: got %v, want nil", agentIDs(got))
	}
}

func agentIDs(defs []*agent.Definition) []string {
	out := make([]string, len(defs))
	for i, d := range defs {
		out[i] = d.ID
	}
	return out
}

// TestBuildAgentCallSchemas verifies that the agent__<id> tool schemas are
// generated correctly and that peers with empty descriptions get a sane
// fallback (the model picks tools by description, so an empty description
// would make the tool effectively invisible to the LLM).
func TestBuildAgentCallSchemas(t *testing.T) {
	dir := t.TempDir()
	l := NewLoader([]string{dir})
	for _, d := range []*agent.Definition{
		{ID: "researcher", Description: "Searches the KB.", Enabled: true},
		{ID: "critic", Description: "", Enabled: true}, // empty description
	} {
		if err := l.Upsert(dir, d); err != nil {
			t.Fatalf("Upsert %s: %v", d.ID, err)
		}
	}

	e := &Engine{loader: l}
	caller := &agent.Definition{ID: "writer", Agents: []string{"researcher", "critic"}}

	schemas := e.buildAgentCallSchemas(caller)
	if len(schemas) != 2 {
		t.Fatalf("schema count: got %d, want 2", len(schemas))
	}
	for _, s := range schemas {
		if s.Description == "" {
			t.Errorf("schema %q has empty description", s.Name)
		}
		if len(s.Name) == 0 || s.Name[:len(AgentToolPrefix)] != AgentToolPrefix {
			t.Errorf("schema name should start with %q, got %q", AgentToolPrefix, s.Name)
		}
		params, _ := s.Parameters["properties"].(map[string]any)
		if _, ok := params["message"]; !ok {
			t.Errorf("schema %q missing required `message` parameter", s.Name)
		}
	}
}

// TestProviderAllowed guards the llm.allowed_providers field added 2026-05-28
// after a cron agent intended for ollama accidentally got pointed at
// Anthropic via the GUI dropdown and produced a "credit balance too low"
// failure. The fix: agents that set `llm.allowed_providers: [ollama]` are
// hard-blocked from any other provider at engine entry.
//
// The matrix covers:
//  1. Empty/nil allowlist = legacy behaviour (every provider allowed).
//  2. Single-provider allowlist matches its own provider.
//  3. Single-provider allowlist rejects any other provider — the bug fix.
//  4. Multi-entry allowlist accepts members + rejects non-members.
//  5. Case sensitivity — names must match exactly (Ollama != ollama).
func TestProviderAllowed(t *testing.T) {
	cases := []struct {
		name      string
		allowlist []string
		provider  string
		want      bool
	}{
		{"nil allowlist permits any", nil, "anthropic", true},
		{"empty allowlist permits any", []string{}, "openai", true},
		{"single match", []string{"ollama"}, "ollama", true},
		{"single mismatch blocks", []string{"ollama"}, "anthropic", false},
		{"multi match", []string{"ollama", "openai"}, "openai", true},
		{"multi mismatch blocks", []string{"ollama", "openai"}, "anthropic", false},
		{"case-sensitive Ollama != ollama", []string{"Ollama"}, "ollama", false},
		{"empty provider with non-empty list blocks", []string{"ollama"}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := providerAllowed(tc.allowlist, tc.provider)
			if got != tc.want {
				t.Errorf("providerAllowed(%v, %q) = %v, want %v",
					tc.allowlist, tc.provider, got, tc.want)
			}
		})
	}
}

func TestMCPToolAllowed(t *testing.T) {
	serversRocket := []string{"rocketmoney"}
	serversNone := []string{}
	serversWildcard := []string{"*"}
	toolsTxn := []string{"mcp__rocketmoney__get_transactions"}
	toolsNone := []string{}

	cases := []struct {
		name     string
		def      *agent.Definition
		fullName string
		want     bool
	}{
		{
			name:     "absent allowlists deny MCP tools",
			def:      &agent.Definition{ID: "legacy"},
			fullName: "mcp__rocketmoney__get_accounts",
			want:     false,
		},
		{
			name:     "server allowlist permits every tool on that server",
			def:      &agent.Definition{ID: "finance", MCPServers: &serversRocket},
			fullName: "mcp__rocketmoney__get_accounts",
			want:     true,
		},
		{
			name:     "server allowlist blocks other servers",
			def:      &agent.Definition{ID: "finance", MCPServers: &serversRocket},
			fullName: "mcp__filesystem__read_file",
			want:     false,
		},
		{
			name:     "explicit empty server allowlist disables MCP",
			def:      &agent.Definition{ID: "none", MCPServers: &serversNone},
			fullName: "mcp__rocketmoney__get_accounts",
			want:     false,
		},
		{
			name:     "full tool allowlist permits one tool",
			def:      &agent.Definition{ID: "finance", MCPTools: &toolsTxn},
			fullName: "mcp__rocketmoney__get_transactions",
			want:     true,
		},
		{
			name:     "full tool allowlist blocks sibling tool",
			def:      &agent.Definition{ID: "finance", MCPTools: &toolsTxn},
			fullName: "mcp__rocketmoney__get_accounts",
			want:     false,
		},
		{
			name:     "explicit empty tool allowlist disables MCP",
			def:      &agent.Definition{ID: "none", MCPTools: &toolsNone},
			fullName: "mcp__rocketmoney__get_accounts",
			want:     false,
		},
		{
			name:     "wildcard server allowlist permits all MCP",
			def:      &agent.Definition{ID: "all", MCPServers: &serversWildcard},
			fullName: "mcp__filesystem__read_file",
			want:     true,
		},
		{
			name:     "unsanitized server allowlist matches sanitized tool prefix",
			def:      &agent.Definition{ID: "mixed", MCPServers: &[]string{"Rocket Money"}},
			fullName: "mcp__rocket_money__get_transactions",
			want:     true,
		},
		{
			name:     "malformed MCP name is rejected once filtering is active",
			def:      &agent.Definition{ID: "bad", MCPServers: &serversRocket},
			fullName: "mcp__rocketmoney",
			want:     false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mcpToolAllowed(tc.def, tc.fullName); got != tc.want {
				t.Errorf("mcpToolAllowed(%+v, %q) = %v, want %v",
					tc.def, tc.fullName, got, tc.want)
			}
		})
	}
}

func TestAllToolSchemasBuiltinsGate(t *testing.T) {
	none := []string{}
	webOnly := []string{"web_search"}
	all := []string{"*"}
	knowledgeOnly := []string{"kb_search"}

	cases := []struct {
		name  string
		def   *agent.Definition
		want  []string
		avoid []string
	}{
		{
			name: "absent builtins uses default gates",
			def:  &agent.Definition{ID: "default"},
			want: []string{"web_search"},
			avoid: []string{
				"kb_search",
				"kb_write",
				"read_skill",
				"read_skill_file",
			},
		},
		{
			name:  "empty builtins disables every builtin",
			def:   &agent.Definition{ID: "none", Builtins: &none, Knowledge: []string{"kb"}, Skills: []string{"csv"}},
			avoid: []string{"web_search", "kb_search", "kb_write", "read_skill", "read_skill_file"},
		},
		{
			name: "explicit builtin permits only that builtin",
			def:  &agent.Definition{ID: "web", Builtins: &webOnly, Knowledge: []string{"kb"}, Skills: []string{"csv"}},
			want: []string{"web_search"},
			avoid: []string{
				"kb_search",
				"kb_write",
				"read_skill",
				"read_skill_file",
			},
		},
		{
			name: "wildcard builtins still respect gates",
			def:  &agent.Definition{ID: "all", Builtins: &all, Knowledge: []string{"kb"}, Skills: []string{"csv"}},
			want: []string{"web_search", "kb_search", "kb_write", "read_skill", "read_skill_file"},
		},
		{
			name:  "listing gated builtin without prerequisite does not expose it",
			def:   &agent.Definition{ID: "nogate", Builtins: &knowledgeOnly},
			avoid: []string{"kb_search", "kb_write", "web_search"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := &Engine{
				skillLoader: fakeSkillLoader{},
				knowledge:   nonNilKnowledgeServiceForSchemaGate(),
			}
			e.builtins = e.buildBuiltins()

			names := toolSchemaNameSet(e.allToolSchemas(tc.def, "http"))
			for _, name := range tc.want {
				if !names[name] {
					t.Fatalf("expected %q in schemas; got %v", name, sortedSchemaNames(names))
				}
			}
			for _, name := range tc.avoid {
				if names[name] {
					t.Fatalf("did not expect %q in schemas; got %v", name, sortedSchemaNames(names))
				}
			}
		})
	}
}

// TestAllToolSchemasSystemToolsRequireDoubleOptInAndHTTP pins SEC-3 gating.
// The PRIVILEGED (SYSTEM-partition) tools — shell_exec, write_file — require
// BOTH the server permit (allowSystemAgents) AND the agent "system" capability,
// and are never offered off the http channel. The SAFE read-only tools —
// read_file — are offered on http regardless of capability.
func TestAllToolSchemasSystemToolsRequireDoubleOptInAndHTTP(t *testing.T) {
	cases := []struct {
		name              string
		allowSystemAgents []string
		defSystemTools    bool // legacy alias for capabilities: [system]
		channel           string
		wantPrivileged    bool // shell_exec + write_file present
		wantSafe          bool // read_file present
	}{
		{
			name:              "global and agent opt-in on http exposes privileged + safe",
			allowSystemAgents: []string{"*"},
			defSystemTools:    true,
			channel:           "http",
			wantPrivileged:    true,
			wantSafe:          true,
		},
		{
			name:              "global off blocks privileged but safe remains",
			allowSystemAgents: nil,
			defSystemTools:    true,
			channel:           "http",
			wantPrivileged:    false,
			wantSafe:          true,
		},
		{
			name:              "no capability blocks privileged but safe remains",
			allowSystemAgents: []string{"*"},
			defSystemTools:    false,
			channel:           "http",
			wantPrivileged:    false,
			wantSafe:          true,
		},
		{
			name:              "bot channel blocks everything system",
			allowSystemAgents: []string{"*"},
			defSystemTools:    true,
			channel:           "telegram",
			wantPrivileged:    false,
			wantSafe:          false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := &Engine{allowSystemAgents: tc.allowSystemAgents}
			e.builtins = e.buildBuiltins()
			names := toolSchemaNameSet(e.allToolSchemas(&agent.Definition{
				ID:          "sys",
				SystemTools: tc.defSystemTools,
			}, tc.channel))

			gotPriv := names["shell_exec"] && names["write_file"]
			if gotPriv != tc.wantPrivileged {
				t.Fatalf("privileged tool exposure = %v, want %v; schemas=%v",
					gotPriv, tc.wantPrivileged, sortedSchemaNames(names))
			}
			if names["read_file"] != tc.wantSafe {
				t.Fatalf("safe tool (read_file) exposure = %v, want %v; schemas=%v",
					names["read_file"], tc.wantSafe, sortedSchemaNames(names))
			}
		})
	}
}

func TestSystemAgentGetsManagedPackageInstallerWithoutArbitrarySystemTools(t *testing.T) {
	// managedInstallExempt is stated rather than assumed: it defaults to false
	// on a zero-valued Engine because it GRANTS a tool, and a grant that
	// switches itself on when somebody forgets to configure it is the wrong
	// default. NewEngine sets it true for real installations; the multi-user
	// wiring sets it back to false.
	e := &Engine{allowSystemAgents: nil, managedInstallExempt: true}
	e.builtins = e.buildBuiltins()

	names := toolSchemaNameSet(e.allToolSchemas(builtinSystemAgent(), "http"))
	if !names["package_install"] {
		t.Fatalf("built-in System agent should receive package_install; schemas=%v", sortedSchemaNames(names))
	}
	for _, forbidden := range []string{"shell_exec", "run_script", "write_file", "download_file", "install_library"} {
		if names[forbidden] {
			t.Fatalf("%s must remain disabled when allow_system_agents is empty", forbidden)
		}
	}

	custom := &agent.Definition{ID: "custom-system-agent", SystemTools: true}
	customNames := toolSchemaNameSet(e.allToolSchemas(custom, "http"))
	if customNames["package_install"] {
		t.Fatal("managed installer exception must be limited to the built-in System agent")
	}
	if toolSchemaNameSet(e.allToolSchemas(builtinSystemAgent(), "telegram"))["package_install"] {
		t.Fatal("managed installer must remain unavailable outside the local HTTP channel")
	}
}

// TestAllToolSchemasCapabilitiesGrantSystemTools verifies the new
// `capabilities: [system]` declaration is honoured equivalently to the legacy
// `system_tools: true` flag (SEC-3).
func TestAllToolSchemasCapabilitiesGrantSystemTools(t *testing.T) {
	e := &Engine{allowSystemAgents: []string{"*"}}
	e.builtins = e.buildBuiltins()
	names := toolSchemaNameSet(e.allToolSchemas(&agent.Definition{
		ID:           "sys",
		Capabilities: []string{"system"},
	}, "http"))
	if !names["shell_exec"] || !names["write_file"] {
		t.Fatalf("capabilities: [system] should expose privileged tools; schemas=%v",
			sortedSchemaNames(names))
	}
}

func TestHandleReturnsFinalLLMResponse(t *testing.T) {
	e, provider := newHandleTestEngine(t, &agent.Definition{
		ID:           "assistant",
		Name:         "Assistant",
		Enabled:      true,
		SystemPrompt: "Be helpful.",
		LLM:          agent.LLMConfig{Provider: "test", Model: "fake-model"},
		MaxTurns:     3,
		Builtins:     strListPtr(),
	})
	provider.responses = []llm.CompletionResponse{{Content: "final answer"}}

	reply, err := e.Handle(context.Background(), testUserMessage("assistant", "session-1", "hello"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := flattenParts(reply.Parts); got != "final answer" {
		t.Fatalf("reply = %q, want final answer", got)
	}

	reqs := provider.requestsSnapshot()
	if len(reqs) != 1 {
		t.Fatalf("provider calls = %d, want 1", len(reqs))
	}
	if len(reqs[0].Tools) != 0 {
		t.Fatalf("tools = %#v, want none", reqs[0].Tools)
	}
	if reqs[0].Model != "fake-model" {
		t.Fatalf("model = %q, want fake-model", reqs[0].Model)
	}
	if !chatMessagesContain(reqs[0].Messages, "user", "hello") {
		t.Fatalf("messages missing user text: %#v", reqs[0].Messages)
	}
}

func TestHandleExecutesToolThenSynthesizesFinalResponse(t *testing.T) {
	var toolCalls int
	e, provider := newHandleTestEngine(t, &agent.Definition{
		ID:           "assistant",
		Name:         "Assistant",
		Enabled:      true,
		SystemPrompt: "Use tools.",
		LLM:          agent.LLMConfig{Provider: "test", Model: "fake-model"},
		MaxTurns:     3,
		Builtins:     strListPtr("lookup"),
	})
	e.builtins = []BuiltinTool{{
		Name:        "lookup",
		Description: "Looks up a value.",
		Parameters:  map[string]any{"type": "object"},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			toolCalls++
			if args["query"] != "soulacy" {
				t.Fatalf("tool args = %#v", args)
			}
			return "lookup result", nil
		},
	}}
	provider.responses = []llm.CompletionResponse{
		{ToolCalls: []message.ToolCall{{ID: "call-1", Name: "lookup", Arguments: map[string]any{"query": "soulacy"}}}},
		{Content: "final from lookup"},
	}

	reply, err := e.Handle(context.Background(), testUserMessage("assistant", "session-1", "research soulacy"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := flattenParts(reply.Parts); got != "final from lookup" {
		t.Fatalf("reply = %q, want final from lookup", got)
	}
	if toolCalls != 1 {
		t.Fatalf("tool calls = %d, want 1", toolCalls)
	}

	reqs := provider.requestsSnapshot()
	if len(reqs) != 2 {
		t.Fatalf("provider calls = %d, want 2", len(reqs))
	}
	if !toolSchemasContain(reqs[0].Tools, "lookup") {
		t.Fatalf("first request tools = %#v", reqs[0].Tools)
	}
	if !chatMessagesContain(reqs[1].Messages, "tool", "lookup result") {
		t.Fatalf("second request missing tool result: %#v", reqs[1].Messages)
	}
}

func TestHandleRecoversXMLToolCallThenSynthesizesFinalResponse(t *testing.T) {
	var toolCalls int
	e, provider := newHandleTestEngine(t, &agent.Definition{
		ID:           "assistant",
		Name:         "Assistant",
		Enabled:      true,
		SystemPrompt: "Use tools.",
		LLM:          agent.LLMConfig{Provider: "test", Model: "open-weight-model"},
		MaxTurns:     3,
		Builtins:     strListPtr("web_search"),
	})
	e.builtins = []BuiltinTool{{
		Name:        "web_search",
		Description: "Searches the web.",
		Parameters:  map[string]any{"type": "object"},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			toolCalls++
			if args["query"] != "current weather Buffalo Grove, IL" {
				t.Fatalf("tool args = %#v", args)
			}
			return "72°F and clear", nil
		},
	}}
	provider.responses = []llm.CompletionResponse{
		{Content: "<web_search>\n<query>current weather Buffalo Grove, IL</query>\n</web_search>"},
		{Content: "It is 72°F and clear."},
	}

	reply, err := e.Handle(context.Background(), testUserMessage("assistant", "session-xml-tool", "How is the weather?"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := flattenParts(reply.Parts); got != "It is 72°F and clear." {
		t.Fatalf("reply = %q", got)
	}
	if toolCalls != 1 {
		t.Fatalf("tool calls = %d, want 1", toolCalls)
	}
	reqs := provider.requestsSnapshot()
	if len(reqs) != 2 || !chatMessagesContain(reqs[1].Messages, "tool", "72°F and clear") {
		t.Fatalf("follow-up request missing recovered tool result: %#v", reqs)
	}
}

func TestHandleDuplicateToolCallRunsToolOnlyOnceThenSynthesizes(t *testing.T) {
	var toolCalls int
	e, provider := newHandleTestEngine(t, &agent.Definition{
		ID:           "assistant",
		Name:         "Assistant",
		Enabled:      true,
		SystemPrompt: "Use tools.",
		LLM:          agent.LLMConfig{Provider: "test", Model: "fake-model"},
		MaxTurns:     4,
		Builtins:     strListPtr("lookup"),
	})
	e.builtins = []BuiltinTool{{
		Name:        "lookup",
		Description: "Looks up a value.",
		Parameters:  map[string]any{"type": "object"},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			toolCalls++
			return "first result", nil
		},
	}}
	duplicateCall := message.ToolCall{ID: "call-1", Name: "lookup", Arguments: map[string]any{"query": "same"}}
	provider.responses = []llm.CompletionResponse{
		{ToolCalls: []message.ToolCall{duplicateCall}},
		{ToolCalls: []message.ToolCall{duplicateCall}},
		{Content: "done after duplicate"},
	}

	reply, err := e.Handle(context.Background(), testUserMessage("assistant", "session-1", "repeat"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := flattenParts(reply.Parts); got != "done after duplicate" {
		t.Fatalf("reply = %q, want done after duplicate", got)
	}
	if toolCalls != 1 {
		t.Fatalf("tool calls = %d, want duplicate guard to run tool once", toolCalls)
	}

	reqs := provider.requestsSnapshot()
	if len(reqs) != 3 {
		t.Fatalf("provider calls = %d, want 3", len(reqs))
	}
	if len(reqs[2].Tools) != 0 {
		t.Fatalf("final synthesis tools = %#v, want none", reqs[2].Tools)
	}
	if !chatMessagesContain(reqs[2].Messages, "system", "Now write your final response") {
		t.Fatalf("third request missing final synthesis prompt: %#v", reqs[2].Messages)
	}
}

// TestHandleSynthesisEmptyContentRecoversReply reproduces the real qwen3-coder
// bug: a tool-using run gathers data, then the forced final-synthesis call
// returns EMPTY Content (the model spent its turn inside a <think> block). The
// engine's empty-synthesis retry ALSO comes back empty. Previously the user got
// "(no final response produced)" and all the gathered work was discarded. The
// fix must surface a useful reply built from the gathered tool results.
func TestHandleSynthesisEmptyContentRecoversReply(t *testing.T) {
	e, provider := newHandleTestEngine(t, &agent.Definition{
		ID:           "assistant",
		Name:         "Assistant",
		Enabled:      true,
		SystemPrompt: "Use tools.",
		LLM:          agent.LLMConfig{Provider: "test", Model: "fake-model"},
		MaxTurns:     3,
		Builtins:     strListPtr("lookup"),
	})
	e.builtins = []BuiltinTool{{
		Name:        "lookup",
		Description: "Looks up a value.",
		Parameters:  map[string]any{"type": "object"},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return "Soulacy ships agent runtimes.", nil
		},
	}}
	provider.responses = []llm.CompletionResponse{
		// Turn 1: model calls the tool.
		{ToolCalls: []message.ToolCall{{ID: "call-1", Name: "lookup", Arguments: map[string]any{"query": "soulacy"}}}},
		// Turn 2: model emits a tool call again with no text → loop forces synthesis.
		{ToolCalls: []message.ToolCall{{ID: "call-2", Name: "lookup", Arguments: map[string]any{"query": "soulacy"}}}},
		// Turn 3 (synthesis): empty content — the bug.
		{Content: "", OutputTokens: 758},
		// Turn 4 (empty-synthesis retry): STILL empty.
		{Content: "", OutputTokens: 412},
	}

	reply, err := e.Handle(context.Background(), testUserMessage("assistant", "session-1", "what is soulacy?"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got := flattenParts(reply.Parts)
	if strings.Contains(got, "no final response produced") {
		t.Fatalf("regression: completed run discarded as %q", got)
	}
	if !strings.Contains(got, "Soulacy ships agent runtimes.") {
		t.Fatalf("reply did not recover gathered tool result: %q", got)
	}
}

// TestHandleSynthesisRetryRecoversReply verifies the think-discouraging retry:
// the first synthesis returns empty Content but the retry produces real text.
func TestHandleSynthesisRetryRecoversReply(t *testing.T) {
	e, provider := newHandleTestEngine(t, &agent.Definition{
		ID:           "assistant",
		Name:         "Assistant",
		Enabled:      true,
		SystemPrompt: "Use tools.",
		LLM:          agent.LLMConfig{Provider: "test", Model: "fake-model"},
		MaxTurns:     3,
		Builtins:     strListPtr("lookup"),
	})
	e.builtins = []BuiltinTool{{
		Name:        "lookup",
		Description: "Looks up a value.",
		Parameters:  map[string]any{"type": "object"},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return "raw data", nil
		},
	}}
	duplicateCall := message.ToolCall{ID: "call-1", Name: "lookup", Arguments: map[string]any{"query": "same"}}
	provider.responses = []llm.CompletionResponse{
		{ToolCalls: []message.ToolCall{duplicateCall}},
		{ToolCalls: []message.ToolCall{duplicateCall}},
		{Content: "", OutputTokens: 758},                   // synthesis: empty
		{Content: "Here is the synthesized final report."}, // retry: real text
	}

	reply, err := e.Handle(context.Background(), testUserMessage("assistant", "session-1", "go"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := flattenParts(reply.Parts); got != "Here is the synthesized final report." {
		t.Fatalf("reply = %q, want the retry text", got)
	}
	// The retry must omit tools, like the first synthesis pass.
	reqs := provider.requestsSnapshot()
	last := reqs[len(reqs)-1]
	if len(last.Tools) != 0 {
		t.Fatalf("synthesis retry must omit tools, got %#v", last.Tools)
	}
	if !chatMessagesContain(last.Messages, "system", "<think>") {
		t.Fatalf("retry should carry the think-discouraging instruction: %#v", last.Messages)
	}
}

func TestHandleRetriesInvalidStructuredOutput(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"answer": map[string]any{"type": "string"},
		},
		"required": []any{"answer"},
	}
	e, provider := newHandleTestEngine(t, &agent.Definition{
		ID:           "assistant",
		Name:         "Assistant",
		Enabled:      true,
		SystemPrompt: "Return JSON.",
		LLM: agent.LLMConfig{
			Provider:     "test",
			Model:        "fake-model",
			OutputSchema: schema,
		},
		MaxTurns: 3,
		Builtins: strListPtr(),
	})
	provider.responses = []llm.CompletionResponse{
		{Content: "not json"},
		{Content: `{"answer":"fixed"}`},
	}

	reply, err := e.Handle(context.Background(), testUserMessage("assistant", "session-1", "json please"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := flattenParts(reply.Parts); got != `{"answer":"fixed"}` {
		t.Fatalf("reply = %q, want corrected JSON", got)
	}

	reqs := provider.requestsSnapshot()
	if len(reqs) != 2 {
		t.Fatalf("provider calls = %d, want structured retry", len(reqs))
	}
	if reqs[1].ResponseFormat != "json_schema" {
		t.Fatalf("retry response format = %q, want json_schema", reqs[1].ResponseFormat)
	}
	if reqs[1].JSONSchema == nil {
		t.Fatal("retry JSONSchema is nil")
	}
	if !chatMessagesContain(reqs[1].Messages, "system", "Your previous response was not valid JSON") {
		t.Fatalf("retry messages missing corrective prompt: %#v", reqs[1].Messages)
	}
}

type fakeSkillLoader struct{}

func (fakeSkillLoader) BuildCatalog() string { return "" }
func (fakeSkillLoader) Get(string) *skill.Skill {
	return nil
}
func (fakeSkillLoader) All() []*skill.Skill { return nil }

func nonNilKnowledgeServiceForSchemaGate() *knowledge.Service {
	return &knowledge.Service{}
}

func toolSchemaNameSet(schemas []llm.ToolSchema) map[string]bool {
	out := make(map[string]bool, len(schemas))
	for _, schema := range schemas {
		out[schema.Name] = true
	}
	return out
}

func sortedSchemaNames(names map[string]bool) []string {
	out := make([]string, 0, len(names))
	for name := range names {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

type fakeHandleProvider struct {
	mu        sync.Mutex
	responses []llm.CompletionResponse
	errors    []error
	requests  []llm.CompletionRequest
}

func (p *fakeHandleProvider) ID() string { return "test" }

func (p *fakeHandleProvider) Complete(ctx context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, req)
	idx := len(p.requests) - 1
	if idx < len(p.errors) && p.errors[idx] != nil {
		return nil, p.errors[idx]
	}
	if idx >= len(p.responses) {
		return &llm.CompletionResponse{Content: "default fake response"}, nil
	}
	resp := p.responses[idx]
	return &resp, nil
}

func (p *fakeHandleProvider) Models(context.Context) ([]string, error) {
	return []string{"fake-model"}, nil
}

func (p *fakeHandleProvider) requestsSnapshot() []llm.CompletionRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]llm.CompletionRequest, len(p.requests))
	copy(out, p.requests)
	return out
}

func newHandleTestEngine(t *testing.T, def *agent.Definition) (*Engine, *fakeHandleProvider) {
	t.Helper()
	agentDir := t.TempDir()
	loader := NewLoader([]string{agentDir})
	if err := loader.Upsert(agentDir, def); err != nil {
		t.Fatalf("upsert agent: %v", err)
	}
	provider := &fakeHandleProvider{}
	router := llm.NewRouter("test")
	router.Register(provider)
	mem, err := memory.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("memory store: %v", err)
	}
	e := NewEngine(loader, router, mem, nil, "", time.Second, zap.NewNop(), nil, nil, "", nil, nil, nil, nil, nil)
	e.SetPrivilegedCommandRunner(HostPrivilegedRunner{})
	if err := e.SetFilesystemRoots([]string{t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	return e, provider
}

func testUserMessage(agentID, sessionID, text string) message.Message {
	return message.Message{
		ID:        "msg-1",
		SessionID: sessionID,
		AgentID:   agentID,
		Channel:   "http",
		ThreadID:  "thread-1",
		UserID:    "user-1",
		Username:  "Ada",
		Role:      message.RoleUser,
		Parts:     message.Text(text),
		CreatedAt: time.Now().UTC(),
	}
}

func strListPtr(values ...string) *[]string {
	out := append([]string(nil), values...)
	return &out
}

func toolSchemasContain(schemas []llm.ToolSchema, name string) bool {
	for _, schema := range schemas {
		if schema.Name == name {
			return true
		}
	}
	return false
}

func chatMessagesContain(messages []llm.ChatMessage, role, contentSubstr string) bool {
	for _, msg := range messages {
		if msg.Role == role && strings.Contains(msg.Content, contentSubstr) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Phase 3D: Engine.Handle hardening — peer delegation, confirmation,
// unknown/disabled agent, provider allowlist, and cost recording.
// ---------------------------------------------------------------------------

// TestHandleUnknownAgent verifies Handle returns a clear error for an agent
// ID that was never registered in the loader.
func TestHandleUnknownAgent(t *testing.T) {
	e, _ := newHandleTestEngine(t, &agent.Definition{
		ID: "real-agent", Name: "Real", Enabled: true,
		LLM: agent.LLMConfig{Provider: "test", Model: "fake-model"},
	})
	_, err := e.Handle(context.Background(), testUserMessage("ghost-agent", "session-1", "hello"))
	if err == nil {
		t.Fatal("expected error for unknown agent, got nil")
	}
	if !strings.Contains(err.Error(), "unknown agent") {
		t.Fatalf("error should mention 'unknown agent': %v", err)
	}
}

// TestHandleDisabledAgent verifies Handle returns a clear error when the
// target agent has Enabled: false.
func TestHandleDisabledAgent(t *testing.T) {
	e, _ := newHandleTestEngine(t, &agent.Definition{
		ID: "bot", Name: "Bot", Enabled: false,
		LLM: agent.LLMConfig{Provider: "test", Model: "fake-model"},
	})
	_, err := e.Handle(context.Background(), testUserMessage("bot", "session-1", "hello"))
	if err == nil {
		t.Fatal("expected error for disabled agent, got nil")
	}
	if !strings.Contains(err.Error(), "is disabled") {
		t.Fatalf("error should mention 'is disabled': %v", err)
	}
}

// TestHandleProviderNotAllowed verifies that an agent whose allowed_providers
// list excludes the configured provider returns a clear error — the
// "fat-finger GUI dropdown" guard introduced 2026-05-28.
func TestHandleProviderNotAllowed(t *testing.T) {
	e, _ := newHandleTestEngine(t, &agent.Definition{
		ID: "cron-bot", Name: "Cron Bot", Enabled: true,
		LLM: agent.LLMConfig{
			Provider:         "anthropic",
			Model:            "claude-3-haiku",
			AllowedProviders: []string{"ollama"},
		},
	})
	_, err := e.Handle(context.Background(), testUserMessage("cron-bot", "session-1", "run"))
	if err == nil {
		t.Fatal("expected provider-not-allowed error, got nil")
	}
	if !strings.Contains(err.Error(), "not in allowed_providers") {
		t.Fatalf("error should mention 'not in allowed_providers': %v", err)
	}
}

// TestHandlePeerAgentDelegation verifies the full peer-agent delegation path:
// the LLM returns an agent__<id> tool call, the engine routes it to the peer
// via a sub-Handle call, injects the peer's reply as a tool result, and
// synthesises a final answer on the next LLM turn.
func TestHandlePeerAgentDelegation(t *testing.T) {
	agentDir := t.TempDir()
	loader := NewLoader([]string{agentDir})

	callerDef := &agent.Definition{
		ID: "caller", Name: "Caller", Enabled: true,
		SystemPrompt: "Delegate research to peer.",
		LLM:          agent.LLMConfig{Provider: "test", Model: "fake-model"},
		MaxTurns:     3,
		Builtins:     strListPtr(), // no builtins — peer calls only
		Agents:       []string{"peer"},
	}
	peerDef := &agent.Definition{
		ID: "peer", Name: "Peer", Enabled: true,
		SystemPrompt: "Answer research questions.",
		LLM:          agent.LLMConfig{Provider: "test", Model: "fake-model"},
		MaxTurns:     2,
		Builtins:     strListPtr(),
	}
	for _, d := range []*agent.Definition{callerDef, peerDef} {
		if err := loader.Upsert(agentDir, d); err != nil {
			t.Fatalf("upsert %s: %v", d.ID, err)
		}
	}

	provider := &fakeHandleProvider{}
	router := llm.NewRouter("test")
	router.Register(provider)
	mem, err := memory.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("memory store: %v", err)
	}
	e := NewEngine(loader, router, mem, nil, "", time.Second, zap.NewNop(), nil, nil, "", nil, nil, nil, nil, nil)

	// Provider responses in call order:
	//   0. Caller's first LLM turn → delegates to agent__peer
	//   1. Peer's LLM turn         → returns peer's answer
	//   2. Caller's second turn    → synthesises final answer using tool result
	provider.responses = []llm.CompletionResponse{
		{ToolCalls: []message.ToolCall{{
			ID:        "call-peer",
			Name:      AgentToolPrefix + "peer",
			Arguments: map[string]any{"message": "what is the capital of France?"},
		}}},
		{Content: "The capital of France is Paris."},
		{Content: "According to my peer: Paris is the capital of France."},
	}

	reply, err := e.Handle(context.Background(), testUserMessage("caller", "session-1", "what is the capital of France?"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got := flattenParts(reply.Parts)
	if !strings.Contains(got, "Paris") {
		t.Fatalf("reply should mention Paris, got: %q", got)
	}

	reqs := provider.requestsSnapshot()
	if len(reqs) != 3 {
		t.Fatalf("expected 3 provider calls (caller turn1, peer, caller turn2), got %d", len(reqs))
	}
	// Caller's synthesis turn must see the peer's answer as a tool result.
	if !chatMessagesContain(reqs[2].Messages, "tool", "Paris") {
		t.Fatalf("caller's second turn should have peer's answer in context: %#v", reqs[2].Messages)
	}
}

func TestShouldParallelizePeerCallsRequiresOptInAndUniquePeerCalls(t *testing.T) {
	calls := []message.ToolCall{
		{ID: "a", Name: AgentToolPrefix + "research", Arguments: map[string]any{"message": "research"}},
		{ID: "b", Name: AgentToolPrefix + "critic", Arguments: map[string]any{"message": "review"}},
	}
	if shouldParallelizePeerCalls(&agent.Definition{}, calls) {
		t.Fatal("parallel peer calls must be opt-in")
	}
	if !shouldParallelizePeerCalls(&agent.Definition{ParallelPeerCalls: true}, calls) {
		t.Fatal("two unique peer calls should parallelize when enabled")
	}
	dup := append(calls, calls[0])
	if shouldParallelizePeerCalls(&agent.Definition{ParallelPeerCalls: true}, dup) {
		t.Fatal("duplicate calls should fall back to sequential execution so the anti-loop cache stays deterministic")
	}
	mixed := []message.ToolCall{
		{ID: "a", Name: AgentToolPrefix + "research", Arguments: map[string]any{"message": "research"}},
		{ID: "b", Name: "web_search", Arguments: map[string]any{"query": "news"}},
	}
	if shouldParallelizePeerCalls(&agent.Definition{ParallelPeerCalls: true}, mixed) {
		t.Fatal("one peer call is not a fan-out")
	}
}

// TestHandleConfirmationApproved verifies that when a tool is listed in
// ConfirmTools and the user approves via WithConfirmSender, the tool executes
// normally and the engine produces its final answer.
func TestHandleConfirmationApproved(t *testing.T) {
	var executions int
	e, provider := newHandleTestEngine(t, &agent.Definition{
		ID: "gated", Name: "Gated", Enabled: true,
		SystemPrompt: "Use safe_op.",
		LLM:          agent.LLMConfig{Provider: "test", Model: "fake-model"},
		MaxTurns:     3,
		Builtins:     strListPtr("safe_op"),
		ConfirmTools: []string{"safe_op"},
	})
	e.builtins = []BuiltinTool{{
		Name:        "safe_op",
		Description: "Performs a safe operation.",
		Parameters:  map[string]any{"type": "object"},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			executions++
			return "op done", nil
		},
	}}
	provider.responses = []llm.CompletionResponse{
		{ToolCalls: []message.ToolCall{{ID: "c1", Name: "safe_op", Arguments: map[string]any{}}}},
		{Content: "safe_op completed successfully"},
	}

	// Confirm sender that immediately approves every request.
	confirmSender := ConfirmSenderFunc(func(req ConfirmRequest) <-chan bool {
		ch := make(chan bool, 1)
		ch <- true
		return ch
	})
	ctx := WithConfirmSender(context.Background(), confirmSender)

	reply, err := e.Handle(ctx, testUserMessage("gated", "session-1", "run safe_op"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if executions != 1 {
		t.Fatalf("expected tool to execute once after approval, got %d executions", executions)
	}
	if got := flattenParts(reply.Parts); !strings.Contains(got, "completed") {
		t.Fatalf("reply = %q, want string containing 'completed'", got)
	}
}

// TestHandleConfirmationDenied verifies that when a tool is listed in
// ConfirmTools and the user denies, the tool does NOT execute; instead
// the denial error is surfaced as the tool result so the LLM can explain
// it to the user.
func TestHandleConfirmationDenied(t *testing.T) {
	var executions int
	e, provider := newHandleTestEngine(t, &agent.Definition{
		ID: "gated", Name: "Gated", Enabled: true,
		SystemPrompt: "Use safe_op.",
		LLM:          agent.LLMConfig{Provider: "test", Model: "fake-model"},
		MaxTurns:     3,
		Builtins:     strListPtr("safe_op"),
		ConfirmTools: []string{"safe_op"},
	})
	e.builtins = []BuiltinTool{{
		Name:        "safe_op",
		Description: "Performs a safe operation.",
		Parameters:  map[string]any{"type": "object"},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			executions++ // must NOT be reached
			return "op done", nil
		},
	}}
	provider.responses = []llm.CompletionResponse{
		{ToolCalls: []message.ToolCall{{ID: "c1", Name: "safe_op", Arguments: map[string]any{}}}},
		{Content: "The operation was denied by the user; I cannot proceed."},
	}

	// Confirm sender that immediately denies every request.
	confirmSender := ConfirmSenderFunc(func(req ConfirmRequest) <-chan bool {
		ch := make(chan bool, 1)
		ch <- false
		return ch
	})
	ctx := WithConfirmSender(context.Background(), confirmSender)

	reply, err := e.Handle(ctx, testUserMessage("gated", "session-1", "run safe_op"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if executions != 0 {
		t.Fatalf("tool should not have executed after denial, got %d executions", executions)
	}

	reqs := provider.requestsSnapshot()
	if len(reqs) != 2 {
		t.Fatalf("expected 2 provider calls, got %d", len(reqs))
	}
	// The second LLM call must see the denial error in its tool-result context.
	if !chatMessagesContain(reqs[1].Messages, "tool", "denied") {
		t.Fatalf("second provider call should have denial message in context: %#v", reqs[1].Messages)
	}
	if got := flattenParts(reply.Parts); got == "" {
		t.Fatal("expected a non-empty final reply even after denial")
	}
}

// TestHandleCostRecording verifies that recordUsage is called with the token
// counts from the LLM response when a cost store is wired. Silently no-ops
// when both counts are zero (exercised here with non-zero values).
func TestHandleCostRecording(t *testing.T) {
	e, provider := newHandleTestEngine(t, &agent.Definition{
		ID: "cost-bot", Name: "Cost Bot", Enabled: true,
		SystemPrompt: "Be helpful.",
		LLM:          agent.LLMConfig{Provider: "test", Model: "fake-model"},
		MaxTurns:     2,
		Builtins:     strListPtr(),
	})

	cs := &fakeCostStore{}
	e.SetCostStore(cs)

	provider.responses = []llm.CompletionResponse{{
		Content:      "Here is the answer.",
		InputTokens:  120,
		OutputTokens: 40,
	}}

	_, err := e.Handle(context.Background(), testUserMessage("cost-bot", "session-cost", "give me an answer"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}

	cs.mu.Lock()
	records := cs.records
	cs.mu.Unlock()

	if len(records) == 0 {
		t.Fatal("expected at least one cost record, got none")
	}
	r := records[0]
	if r.agentID != "cost-bot" {
		t.Errorf("agentID = %q, want 'cost-bot'", r.agentID)
	}
	if r.promptTokens != 120 {
		t.Errorf("promptTokens = %d, want 120", r.promptTokens)
	}
	if r.compTokens != 40 {
		t.Errorf("compTokens = %d, want 40", r.compTokens)
	}
	if r.model != "fake-model" {
		t.Errorf("model = %q, want 'fake-model'", r.model)
	}
}

// fakeCostStore is a test double for the agentCostStore interface.
type fakeCostStore struct {
	mu      sync.Mutex
	records []fakeCostRecord
}

type fakeCostRecord struct {
	agentID, sessionID, provider, model string
	promptTokens, compTokens            int
}

func (f *fakeCostStore) Record(_ context.Context, agentID, sessionID, provider, model string,
	promptTokens, compTokens, _ int, _ float64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records = append(f.records, fakeCostRecord{
		agentID:      agentID,
		sessionID:    sessionID,
		provider:     provider,
		model:        model,
		promptTokens: promptTokens,
		compTokens:   compTokens,
	})
	return nil
}

// TestHandleExecutesSystemTool verifies that when allowSystemAgents and def.SystemTools are true,
// the engine can execute OS-level built-in tools like shell_exec and synthesise the final response.
func TestHandleExecutesSystemTool(t *testing.T) {
	e, provider := newHandleTestEngine(t, &agent.Definition{
		ID:           "sys-bot",
		Name:         "System Bot",
		Enabled:      true,
		SystemPrompt: "Use shell_exec.",
		LLM:          agent.LLMConfig{Provider: "test", Model: "fake-model"},
		MaxTurns:     3,
		SystemTools:  true,
	})
	e.allowSystemAgents = []string{"*"}

	provider.responses = []llm.CompletionResponse{
		{ToolCalls: []message.ToolCall{{
			ID:   "call-sys",
			Name: "shell_exec",
			Arguments: map[string]any{
				"command": "echo hello",
			},
		}}},
		{Content: "Command output was hello"},
	}

	reply, err := e.Handle(context.Background(), testUserMessage("sys-bot", "session-1", "run echo"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got := flattenParts(reply.Parts)
	if got != "Command output was hello" {
		t.Fatalf("reply = %q, want 'Command output was hello'", got)
	}

	reqs := provider.requestsSnapshot()
	if len(reqs) != 2 {
		t.Fatalf("expected 2 provider calls, got %d", len(reqs))
	}
	if !chatMessagesContain(reqs[1].Messages, "tool", "hello") {
		t.Fatalf("second request missing tool result 'hello': %#v", reqs[1].Messages)
	}
}

// TestPickRouterRoute is the rule-engine unit test for the new
// kind: router agent type (docs/CHANNEL_DESIGN.md Q2). dispatchRouter
// itself is intentionally not unit-tested here because it integrates
// with runAgentCall → Handle → LLM, which would require either a heavy
// mock stack or live network calls. The matching logic lives in
// pickRouterRoute, which IS pure and table-test-friendly.
//
// Cases cover:
//   - prefix match (case-insensitive)
//   - contains any-of (case-insensitive)
//   - regex with explicit (?i) flag
//   - declaration order — earlier match wins even if later route also matches
//   - fallback route (no match clauses) catches everything else
//   - no-match-no-fallback returns (-1, false) so the dispatcher errors
//     cleanly instead of silently misrouting
func TestPickRouterRoute(t *testing.T) {
	routes := []agent.RouterRoute{
		{Prefix: "/research", Target: "research-agent"},
		{Contains: []string{"latest", "news", "today"}, Target: "research-agent"},
		{Regex: `(?i)compare\s+\w+\s+vs\s+\w+`, Target: "decision-agent"},
		{Contains: []string{"strategy", "roadmap"}, Target: "strategy-agent"},
		{Target: "writing-agent"}, // fallback
	}
	cases := []struct {
		name        string
		text        string
		wantIdx     int
		wantMatched bool
	}{
		{"prefix slash command", "/research what is sora 2", 0, true},
		{"prefix case-insensitive", "/RESEARCH apollo", 0, true},
		{"contains first hit", "give me the latest on llama 4", 1, true},
		{"contains case-insensitive", "today's news from openai", 1, true},
		// Go's regexp \w matches [0-9A-Za-z_] only — no hyphens. A test
		// string like "GPT-5" would split mid-word and miss the route,
		// so use hyphen-free names to exercise the regex rule itself.
		{"regex match", "compare apples vs oranges", 2, true},
		{"contains middle entry", "write the 2027 roadmap", 3, true},
		{"fallback catches plain prose", "tell me a joke about kubernetes", 4, true},
		{"earlier wins over later (prefix wins over fallback)", "/research everything", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			idx, matched := pickRouterRoute(routes, tc.text)
			if matched != tc.wantMatched {
				t.Errorf("matched=%v want=%v", matched, tc.wantMatched)
			}
			if idx != tc.wantIdx {
				t.Errorf("idx=%d want=%d (route %+v)", idx, tc.wantIdx, routes[idx])
			}
		})
	}

	// No fallback → empty match space returns (-1, false).
	noFallback := []agent.RouterRoute{
		{Prefix: "/admin", Target: "admin-agent"},
	}
	if idx, matched := pickRouterRoute(noFallback, "hello there"); matched || idx != -1 {
		t.Errorf("no-match-no-fallback: got (%d, %v); want (-1, false)", idx, matched)
	}

	// Malformed regex should not crash; route is silently skipped.
	bad := []agent.RouterRoute{
		{Regex: "(broken", Target: "noop"},
		{Target: "fallback-agent"},
	}
	if idx, _ := pickRouterRoute(bad, "anything"); idx != 1 {
		t.Errorf("malformed regex should be skipped; got idx=%d (want 1=fallback)", idx)
	}
}

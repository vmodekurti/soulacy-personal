// reasoning_test.go — Story 16: the engine runs agents with a reasoning:
// block through the pluggable reasoning Loop (E15) instead of the classic
// single-call tool loop. Fake LLM backends only — no network, no subprocess.
package runtime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/agentmemory"
	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/internal/memory"
	"github.com/soulacy/soulacy/internal/reasoning"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
	"go.uber.org/zap"
)

// ── fakes ────────────────────────────────────────────────────────────────────

// fakeLoopBackend scripts Think() responses in order and records every
// ThinkRequest it sees so tests can assert on the step history the loop fed
// back. Reflect() returns reflectOut. Plan() is unused by these tests.
type fakeLoopBackend struct {
	mu           sync.Mutex
	scripted     []reasoning.ThinkResponse
	calls        int
	thinkReqs    []reasoning.ThinkRequest
	reflectOut   string
	reflectRules string
}

func (f *fakeLoopBackend) Think(_ context.Context, req reasoning.ThinkRequest) (reasoning.ThinkResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.thinkReqs = append(f.thinkReqs, req)
	if f.calls >= len(f.scripted) {
		return reasoning.ThinkResponse{IsDone: true, FinalAnswer: "(script exhausted)"}, nil
	}
	resp := f.scripted[f.calls]
	f.calls++
	return resp, nil
}

func (f *fakeLoopBackend) Plan(context.Context, string, string, int) (reasoning.Plan, error) {
	return reasoning.Plan{}, errors.New("plan not scripted")
}

func (f *fakeLoopBackend) Reflect(_ context.Context, _ reasoning.ReflectRequest) (reasoning.ReflectResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return reasoning.ReflectResponse{Output: f.reflectOut, UpdatedRules: f.reflectRules}, nil
}

// reasoningSink records every emitted engine event.
type reasoningSink struct {
	mu     sync.Mutex
	events []message.Event
}

func (s *reasoningSink) Emit(ev message.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
}

func (s *reasoningSink) byType(t string) []message.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []message.Event
	for _, ev := range s.events {
		if ev.Type == t {
			out = append(out, ev)
		}
	}
	return out
}

// plainTextProvider is a classic-path LLM provider returning a fixed answer.
type plainTextProvider struct{ content string }

func (p *plainTextProvider) ID() string { return "classic-prov" }
func (p *plainTextProvider) Complete(context.Context, llm.CompletionRequest) (*llm.CompletionResponse, error) {
	return &llm.CompletionResponse{Content: p.content}, nil
}
func (p *plainTextProvider) Models(context.Context) ([]string, error) { return nil, nil }

// ── helpers ──────────────────────────────────────────────────────────────────

func newReasoningEngine(t *testing.T, def *agent.Definition, backend reasoning.LLMBackend) (*Engine, *reasoningSink) {
	t.Helper()
	agentDir := t.TempDir()
	loader := NewLoader([]string{agentDir})
	if err := loader.Upsert(agentDir, def); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	mem, err := memory.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("memory store: %v", err)
	}
	sink := &reasoningSink{}
	router := llm.NewRouter("classic-prov")
	router.Register(&plainTextProvider{content: "CLASSIC PATH ANSWER"})
	e := NewEngine(loader, router, mem, nil, "", time.Second, zap.NewNop(), sink,
		nil, "", nil, nil, nil, nil, nil)
	if backend != nil {
		e.SetReasoningBackendFactory(func(*agent.Definition) reasoning.LLMBackend { return backend })
	}
	return e, sink
}

func inboundMsg(agentID, text string) message.Message {
	return message.Message{
		ID: "m1", SessionID: "sess-1", AgentID: agentID,
		Channel: "http", Role: message.RoleUser,
		Parts: message.Text(text), Username: "tester",
	}
}

func replyText(t *testing.T, reply message.Message) string {
	t.Helper()
	var sb strings.Builder
	for _, p := range reply.Parts {
		if p.Type == "text" {
			sb.WriteString(p.Text)
		}
	}
	return sb.String()
}

// ── tests ────────────────────────────────────────────────────────────────────

// An agent declaring reasoning.strategy runs through the Loop: the reply is
// Reflect()'s output, step/start/result trace events are emitted, and the
// classic llm.call path is never touched.
func TestHandle_ReasoningStrategyRunsLoop(t *testing.T) {
	def := &agent.Definition{
		ID: "loop-agent", Name: "Loop Agent", Enabled: true,
		SystemPrompt: "You reason step by step.",
		LLM:          agent.LLMConfig{Provider: "classic-prov"},
		Reasoning:    agent.ReasoningConfig{Strategy: "react", MaxSteps: 3},
	}
	backend := &fakeLoopBackend{
		scripted:   []reasoning.ThinkResponse{{IsDone: true, FinalAnswer: "fallback"}},
		reflectOut: "REFLECTED ANSWER",
	}
	e, sink := newReasoningEngine(t, def, backend)

	reply, err := e.Handle(context.Background(), inboundMsg("loop-agent", "what is 6x7"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := replyText(t, reply); got != "REFLECTED ANSWER" {
		t.Errorf("reply = %q, want Reflect output", got)
	}

	if n := len(sink.byType("reasoning.start")); n != 1 {
		t.Errorf("reasoning.start events = %d, want 1", n)
	}
	if n := len(sink.byType("reasoning.result")); n != 1 {
		t.Errorf("reasoning.result events = %d, want 1", n)
	}
	if n := len(sink.byType("llm.call")); n != 0 {
		t.Errorf("classic llm.call events = %d, want 0 (loop owns LLM traffic)", n)
	}
	if n := len(sink.byType("message.out")); n != 1 {
		t.Errorf("message.out events = %d, want 1", n)
	}
	// System prompt must reach the loop config.
	if len(backend.thinkReqs) == 0 || !strings.Contains(backend.thinkReqs[0].SystemPrompt, "You reason step by step.") {
		t.Error("agent system prompt did not reach the reasoning loop")
	}
}

// Tool calls issued by the loop go through the engine's own dispatch
// (built-ins here — same runTool path that enforces sandbox/audit/confirm),
// the observation is fed back into the next Think, and tool.call/tool.result
// + reasoning.step trace events are emitted.
func TestHandle_ReasoningToolCallsBridgeToEngineDispatch(t *testing.T) {
	def := &agent.Definition{
		ID: "tool-loop-agent", Name: "Tool Loop Agent", Enabled: true,
		LLM:       agent.LLMConfig{Provider: "classic-prov"},
		Reasoning: agent.ReasoningConfig{Strategy: "react", MaxSteps: 4},
		Tools: []agent.ToolDef{
			{Name: "py_tool", Description: "a python tool that is never called here"},
		},
	}
	backend := &fakeLoopBackend{
		scripted: []reasoning.ThinkResponse{
			{Thought: "I should use the tool", Action: reasoning.ToolCall{
				Tool: "echo_tool", Input: map[string]string{"q": "hello"},
			}},
			{IsDone: true, FinalAnswer: "done"},
		},
		reflectOut: "FINAL VIA TOOL",
	}
	e, sink := newReasoningEngine(t, def, backend)

	var toolCalled bool
	var gotArg string
	e.builtins = append(e.builtins, BuiltinTool{
		Name: "echo_tool", Description: "echoes q",
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			toolCalled = true
			gotArg, _ = args["q"].(string)
			return "TOOL SAYS " + gotArg, nil
		},
	})

	reply, err := e.Handle(context.Background(), inboundMsg("tool-loop-agent", "use the tool"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := replyText(t, reply); got != "FINAL VIA TOOL" {
		t.Errorf("reply = %q, want FINAL VIA TOOL", got)
	}
	if !toolCalled {
		t.Fatal("builtin tool was never dispatched through the engine")
	}
	if gotArg != "hello" {
		t.Errorf("tool arg q = %q, want hello", gotArg)
	}

	// The observation must round-trip into the next Think's step history.
	if len(backend.thinkReqs) < 2 {
		t.Fatalf("Think called %d times, want ≥2", len(backend.thinkReqs))
	}
	hist := backend.thinkReqs[1].StepHistory
	if len(hist) != 1 || hist[0].Obs.Content != "TOOL SAYS hello" {
		t.Errorf("step history obs = %+v, want TOOL SAYS hello", hist)
	}

	// Trace events: tool.call + tool.result from the bridge, one reasoning.step.
	if n := len(sink.byType("tool.call")); n != 1 {
		t.Errorf("tool.call events = %d, want 1", n)
	}
	if n := len(sink.byType("tool.result")); n != 1 {
		t.Errorf("tool.result events = %d, want 1", n)
	}
	steps := sink.byType("reasoning.step")
	if len(steps) != 1 {
		t.Fatalf("reasoning.step events = %d, want 1", len(steps))
	}
	payload, ok := steps[0].Payload.(map[string]any)
	if !ok {
		t.Fatalf("reasoning.step payload type %T, want map", steps[0].Payload)
	}
	if payload["thought"] != "I should use the tool" {
		t.Errorf("step thought = %v", payload["thought"])
	}
	if payload["tool"] != "echo_tool" {
		t.Errorf("step tool = %v", payload["tool"])
	}

	// The full engine tool surface (built-ins incl. echo_tool) plus declared
	// Python tools must be advertised to the loop.
	names := backend.thinkReqs[0].ToolNames
	var sawEcho, sawPy bool
	for _, n := range names {
		if n == "echo_tool" {
			sawEcho = true
		}
		if n == "py_tool" {
			sawPy = true
		}
	}
	if !sawPy {
		t.Errorf("declared python tool missing from loop ToolNames: %v", names)
	}
	if !sawEcho {
		t.Errorf("engine builtin missing from loop ToolNames: %v", names)
	}
}

// Tool errors become observations — the loop continues and still produces a
// final reply (no Handle error).
func TestHandle_ReasoningToolErrorBecomesObservation(t *testing.T) {
	def := &agent.Definition{
		ID: "err-loop-agent", Name: "Err Loop", Enabled: true,
		LLM:       agent.LLMConfig{Provider: "classic-prov"},
		Reasoning: agent.ReasoningConfig{Strategy: "react", MaxSteps: 3},
	}
	backend := &fakeLoopBackend{
		scripted: []reasoning.ThinkResponse{
			{Thought: "try the broken tool", Action: reasoning.ToolCall{Tool: "no_such_tool"}},
			{IsDone: true, FinalAnswer: "recovered"},
		},
		reflectOut: "RECOVERED ANSWER",
	}
	e, _ := newReasoningEngine(t, def, backend)

	reply, err := e.Handle(context.Background(), inboundMsg("err-loop-agent", "go"))
	if err != nil {
		t.Fatalf("Handle should not fail on tool errors inside the loop: %v", err)
	}
	if got := replyText(t, reply); got != "RECOVERED ANSWER" {
		t.Errorf("reply = %q, want RECOVERED ANSWER", got)
	}
	if len(backend.thinkReqs) < 2 {
		t.Fatalf("Think called %d times, want ≥2", len(backend.thinkReqs))
	}
	obs := backend.thinkReqs[1].StepHistory[0].Obs.Content
	if !strings.HasPrefix(obs, "tool error:") {
		t.Errorf("observation = %q, want tool error: prefix", obs)
	}
}

// Agents WITHOUT a reasoning block keep the classic single-call path exactly:
// the reasoning backend factory must never fire and the classic llm.call
// events must appear.
func TestHandle_NoReasoningBlockKeepsClassicPath(t *testing.T) {
	def := &agent.Definition{
		ID: "classic-agent", Name: "Classic", Enabled: true,
		LLM: agent.LLMConfig{Provider: "classic-prov"},
	}
	factoryCalled := false
	e, sink := newReasoningEngine(t, def, nil)
	e.SetReasoningBackendFactory(func(*agent.Definition) reasoning.LLMBackend {
		factoryCalled = true
		return &fakeLoopBackend{}
	})

	reply, err := e.Handle(context.Background(), inboundMsg("classic-agent", "hi"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := replyText(t, reply); got != "CLASSIC PATH ANSWER" {
		t.Errorf("reply = %q, want classic provider answer", got)
	}
	if factoryCalled {
		t.Error("reasoning backend factory fired for a reasoning-less agent")
	}
	if n := len(sink.byType("llm.call")); n == 0 {
		t.Error("classic path emitted no llm.call events")
	}
	if n := len(sink.byType("reasoning.start")); n != 0 {
		t.Errorf("reasoning.start events = %d, want 0", n)
	}
}

// The reply lands in session history + memory so follow-up turns see it
// (same contract as the classic path).
func TestHandle_ReasoningReplyPersistedToSession(t *testing.T) {
	def := &agent.Definition{
		ID: "mem-loop-agent", Name: "Mem Loop", Enabled: true,
		LLM:       agent.LLMConfig{Provider: "classic-prov"},
		Reasoning: agent.ReasoningConfig{Strategy: "react"},
	}
	backend := &fakeLoopBackend{
		scripted:   []reasoning.ThinkResponse{{IsDone: true}},
		reflectOut: "REMEMBER ME",
	}
	e, _ := newReasoningEngine(t, def, backend)

	if _, err := e.Handle(context.Background(), inboundMsg("mem-loop-agent", "first turn")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	sess := e.getOrCreateSession("sess-1", "mem-loop-agent")
	sess.mu.Lock()
	defer sess.mu.Unlock()
	var sawUser, sawAssistant bool
	for _, m := range sess.History {
		if m.Role == "user" && strings.Contains(m.Content, "first turn") {
			sawUser = true
		}
		if m.Role == "assistant" && m.Content == "REMEMBER ME" {
			sawAssistant = true
		}
	}
	if !sawUser || !sawAssistant {
		t.Errorf("session history missing turns: user=%v assistant=%v", sawUser, sawAssistant)
	}
}

// ── Story E23: versioned self-updating rulebooks ─────────────────────────────

// With auto_update on, Reflect's updated_rules lands as a NEW rulebook
// version and a rulebook.updated event fires.
func TestHandle_ReasoningAutoUpdatePersistsVersionedRules(t *testing.T) {
	def := &agent.Definition{
		ID: "learner", Name: "Learner", Enabled: true,
		LLM:       agent.LLMConfig{Provider: "classic-prov"},
		Reasoning: agent.ReasoningConfig{Strategy: "react"},
		BrainMemory: agent.BrainMemoryConfig{
			Procedural: agent.ProceduralMemoryConfig{Enabled: true, AutoUpdate: true},
		},
	}
	backend := &fakeLoopBackend{
		scripted:     []reasoning.ThinkResponse{{IsDone: true}},
		reflectOut:   "done",
		reflectRules: "# Learned\n- always cite sources",
	}
	e, sink := newReasoningEngine(t, def, backend)
	brain := agentmemory.NewCompositeStore(t.TempDir(), nil)
	defer brain.Close()
	e.SetBrainMemory(brain)

	if _, err := e.Handle(context.Background(), inboundMsg("learner", "teach me")); err != nil {
		t.Fatal(err)
	}
	if got := brain.ProceduralRules("learner"); !strings.Contains(got, "cite sources") {
		t.Errorf("rules not persisted: %q", got)
	}
	versions, err := brain.RulebookVersions("learner")
	if err != nil || len(versions) != 1 || versions[0].Source != "auto_update" {
		t.Errorf("versions = %+v err=%v, want one auto_update version", versions, err)
	}
	if n := len(sink.byType("rulebook.updated")); n != 1 {
		t.Errorf("rulebook.updated events = %d, want 1", n)
	}
}

// A locked rulebook refuses the auto update: warn event, run still succeeds,
// rules unchanged.
func TestHandle_ReasoningAutoUpdateRespectsLock(t *testing.T) {
	def := &agent.Definition{
		ID: "locked-agent", Name: "Locked", Enabled: true,
		LLM:       agent.LLMConfig{Provider: "classic-prov"},
		Reasoning: agent.ReasoningConfig{Strategy: "react"},
		BrainMemory: agent.BrainMemoryConfig{
			Procedural: agent.ProceduralMemoryConfig{Enabled: true, AutoUpdate: true},
		},
	}
	backend := &fakeLoopBackend{
		scripted:     []reasoning.ThinkResponse{{IsDone: true}},
		reflectOut:   "fine",
		reflectRules: "# drift attempt",
	}
	e, sink := newReasoningEngine(t, def, backend)
	brain := agentmemory.NewCompositeStore(t.TempDir(), nil)
	defer brain.Close()
	if _, err := brain.UpdateProceduralVersioned("locked-agent", "# golden rules", "manual"); err != nil {
		t.Fatal(err)
	}
	if err := brain.SetRulebookLocked("locked-agent", true); err != nil {
		t.Fatal(err)
	}
	e.SetBrainMemory(brain)

	reply, err := e.Handle(context.Background(), inboundMsg("locked-agent", "go"))
	if err != nil {
		t.Fatalf("run must succeed despite the locked rulebook: %v", err)
	}
	if got := replyText(t, reply); got != "fine" {
		t.Errorf("reply = %q", got)
	}
	if got := brain.ProceduralRules("locked-agent"); got != "# golden rules" {
		t.Errorf("locked rules mutated: %q", got)
	}
	if n := len(sink.byType("rulebook.updated")); n != 0 {
		t.Errorf("rulebook.updated fired on a locked agent (%d)", n)
	}
	if n := len(sink.byType("warn")); n != 1 {
		t.Errorf("warn events = %d, want 1 (locked refusal must be visible)", n)
	}
}

// Without the auto_update opt-in, updated_rules is DISCARDED.
func TestHandle_ReasoningNoOptInDiscardsRules(t *testing.T) {
	def := &agent.Definition{
		ID: "no-optin", Name: "NoOptIn", Enabled: true,
		LLM:       agent.LLMConfig{Provider: "classic-prov"},
		Reasoning: agent.ReasoningConfig{Strategy: "react"},
	}
	backend := &fakeLoopBackend{
		scripted:     []reasoning.ThinkResponse{{IsDone: true}},
		reflectOut:   "ok",
		reflectRules: "# should never persist",
	}
	e, sink := newReasoningEngine(t, def, backend)
	brain := agentmemory.NewCompositeStore(t.TempDir(), nil)
	defer brain.Close()
	e.SetBrainMemory(brain)

	if _, err := e.Handle(context.Background(), inboundMsg("no-optin", "go")); err != nil {
		t.Fatal(err)
	}
	if got := brain.ProceduralRules("no-optin"); got != "" {
		t.Errorf("rules persisted without opt-in: %q", got)
	}
	if n := len(sink.byType("rulebook.updated")); n != 0 {
		t.Errorf("rulebook.updated fired without opt-in (%d)", n)
	}
}

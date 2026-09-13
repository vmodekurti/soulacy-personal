package runtime

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/internal/memory"
	"github.com/soulacy/soulacy/pkg/agent"
)

func adaptiveTestEngine(t *testing.T, def *agent.Definition) (*Engine, *fakeHandleProvider, *memory.FactSQLite) {
	t.Helper()
	e, provider := newHandleTestEngine(t, def)
	store, err := memory.OpenFactSQLite(filepath.Join(t.TempDir(), "adaptive.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	local := memory.NewLocalAdaptive(store, nil, e.AdaptiveCompleter("", ""), memory.LocalOptions{})
	e.SetAdaptiveMemory(local, AdaptiveMemoryOptions{Enabled: true})
	return e, provider, store
}

func adaptiveDef() *agent.Definition {
	return &agent.Definition{ID: "helper", Name: "Helper", Enabled: true, SystemPrompt: "You are helpful.", LLM: agent.LLMConfig{Provider: "test", Model: "fake-model"}}
}

func principalCtx() context.Context {
	return WithPrincipal(context.Background(), Principal{Subject: "ada", Role: "admin"})
}

func TestAdaptiveMemoryExtractsAfterTurnAndInjectsNextTurn(t *testing.T) {
	e, provider, store := adaptiveTestEngine(t, adaptiveDef())
	provider.responses = []llm.CompletionResponse{
		{Content: "Nice, Denver is great."},
		{Content: `[{"fact":"User lives in Denver","category":"identity","confidence":0.9}]`},
		{Content: "You live in Denver."},
	}
	if _, err := e.Handle(principalCtx(), testUserMessage("helper", "s1", "Just so you know, I live in Denver now.")); err != nil {
		t.Fatal(err)
	}
	if !WaitAdaptiveMemory(5 * time.Second) {
		t.Fatal("background extraction did not finish")
	}
	facts, err := store.List(context.Background(), memory.FactScope{Owner: "ada", AgentID: "helper"}, memory.FactStatusActive, 0)
	if err != nil || len(facts) != 1 || facts[0].Content != "User lives in Denver" || facts[0].SourceSessionID != "s1" {
		t.Fatalf("fact not stored from turn: %v %+v", err, facts)
	}
	reqs := provider.requestsSnapshot()
	if len(reqs) != 2 {
		t.Fatalf("expected chat + extraction calls, got %d", len(reqs))
	}
	if reqs[1].ResponseFormat != "json" || reqs[1].MaxTokens > 400 || !strings.Contains(reqs[1].Messages[1].Content, "Denver") {
		t.Fatalf("extraction call not lightweight/json: %+v", reqs[1])
	}
	if memory.EstimateTokens(reqs[1].Messages[0].Content+reqs[1].Messages[1].Content) >= 200 {
		t.Fatal("extraction prompt exceeded 200 tokens")
	}

	// Next turn: the fact must reach the system prompt under the memory heading.
	if _, err := e.Handle(principalCtx(), testUserMessage("helper", "s2", "Remind me, where do I live?")); err != nil {
		t.Fatal(err)
	}
	WaitAdaptiveMemory(5 * time.Second)
	reqs = provider.requestsSnapshot()
	sys := reqs[2].Messages[0].Content
	if !strings.Contains(sys, memory.PromptBlockHeader) || !strings.Contains(sys, "- User lives in Denver") {
		t.Fatalf("memory block missing from system prompt:\n%s", sys)
	}
	block := sys[strings.Index(sys, memory.PromptBlockHeader)+len(memory.PromptBlockHeader):]
	if end := strings.Index(block, "\n\n"); end >= 0 {
		block = block[:end]
	}
	if memory.EstimateTokens(block) > 50 {
		t.Fatalf("memory block exceeded 50 tokens: %q", block)
	}
}

func TestAdaptiveMemorySkipsWithoutIdentityOrWhenAgentOptsOut(t *testing.T) {
	off := false
	def := adaptiveDef()
	def.Memory.Adaptive = &off
	e, provider, store := adaptiveTestEngine(t, def)
	provider.responses = []llm.CompletionResponse{{Content: "ok"}, {Content: "ok"}}
	// Opted-out agent, with identity.
	if _, err := e.Handle(principalCtx(), testUserMessage("helper", "s1", "I live in Denver and prefer tea.")); err != nil {
		t.Fatal(err)
	}
	// Opted-in agent but no principal on the context.
	on := true
	def2 := adaptiveDef()
	def2.ID, def2.Memory.Adaptive = "helper2", &on
	if err := e.loader.Upsert(t.TempDir(), def2); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Handle(context.Background(), testUserMessage("helper2", "s2", "I live in Denver and prefer tea.")); err != nil {
		t.Fatal(err)
	}
	WaitAdaptiveMemory(2 * time.Second)
	if n := len(provider.requestsSnapshot()); n != 2 {
		t.Fatalf("expected only the two chat calls, got %d", n)
	}
	facts, _ := store.List(context.Background(), memory.FactScope{Owner: "ada"}, "", 0)
	if len(facts) != 0 {
		t.Fatalf("no facts should be stored, got %d", len(facts))
	}
}

func TestAdaptiveMemoryCasualTurnMakesNoModelCall(t *testing.T) {
	e, provider, _ := adaptiveTestEngine(t, adaptiveDef())
	provider.responses = []llm.CompletionResponse{{Content: "You're welcome!"}}
	if _, err := e.Handle(principalCtx(), testUserMessage("helper", "s1", "thanks!")); err != nil {
		t.Fatal(err)
	}
	WaitAdaptiveMemory(2 * time.Second)
	if n := len(provider.requestsSnapshot()); n != 1 {
		t.Fatalf("casual turn must not trigger extraction, got %d calls", n)
	}
}

package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

// TestBudgetExceeded covers the per-run budget gate logic (S3.1).
func TestBudgetExceeded(t *testing.T) {
	cases := []struct {
		name                                         string
		tokenLimit, usedTokens, callLimit, usedCalls int
		wantHalt                                     bool
	}{
		{"no limits", 0, 1_000_000, 0, 1000, false},
		{"under token limit", 100, 99, 0, 0, false},
		{"at token limit", 100, 100, 0, 0, true},
		{"over token limit", 100, 250, 0, 0, true},
		{"under call limit", 0, 0, 5, 4, false},
		{"at call limit", 0, 0, 5, 5, true},
		{"token ok call over", 1000, 10, 3, 3, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := budgetExceeded(tc.tokenLimit, tc.usedTokens, tc.callLimit, tc.usedCalls)
			if (got != "") != tc.wantHalt {
				t.Fatalf("budgetExceeded(%d,%d,%d,%d) = %q; wantHalt=%v",
					tc.tokenLimit, tc.usedTokens, tc.callLimit, tc.usedCalls, got, tc.wantHalt)
			}
		})
	}
}

func TestEffectiveRunBudgetInheritanceOverridesAndCeilings(t *testing.T) {
	e := &Engine{}
	if tokens, calls := e.effectiveRunBudget(&agent.Definition{}); tokens != defaultRunBudgetTokens || calls != defaultRunBudgetCalls {
		t.Fatalf("shipped inherited budget = %d/%d", tokens, calls)
	}
	e.SetRunBudgets(agent.BudgetConfig{MaxTokens: 1000, MaxLLMCalls: 10}, agent.BudgetConfig{MaxTokens: 2000, MaxLLMCalls: 20})
	for _, tc := range []struct {
		name          string
		budget        *agent.BudgetConfig
		tokens, calls int
	}{
		{"inherits", nil, 1000, 10},
		{"lower override", &agent.BudgetConfig{MaxTokens: 500, MaxLLMCalls: 5}, 500, 5},
		{"higher within ceiling", &agent.BudgetConfig{MaxTokens: 1500, MaxLLMCalls: 15}, 1500, 15},
		{"clamped", &agent.BudgetConfig{MaxTokens: 9000, MaxLLMCalls: 90}, 2000, 20},
		{"explicit unlimited", &agent.BudgetConfig{MaxTokens: 0, MaxLLMCalls: 0}, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gotT, gotC := e.effectiveRunBudget(&agent.Definition{Budget: tc.budget})
			if gotT != tc.tokens || gotC != tc.calls {
				t.Fatalf("got %d/%d, want %d/%d", gotT, gotC, tc.tokens, tc.calls)
			}
		})
	}
}

func TestPlaygroundRunBudgetOverrideIsPerRunAndKeepsBothGuards(t *testing.T) {
	def := &agent.Definition{}
	applyPlaygroundOverrides(def, map[string]string{
		"playground.run_budget.max_tokens":    "160000",
		"playground.run_budget.max_llm_calls": "20",
	})
	if def.Budget == nil || def.Budget.MaxTokens != 160000 || def.Budget.MaxLLMCalls != 20 {
		t.Fatalf("run budget override = %+v", def.Budget)
	}
}

func TestInheritedBudgetHaltReturnsPartialToolOutput(t *testing.T) {
	def := &agent.Definition{ID: "budgeted", Name: "Budgeted", Enabled: true, LLM: agent.LLMConfig{Provider: "test", Model: "fake-model"}, MaxTurns: 3, Builtins: strListPtr("lookup")}
	e, provider := newHandleTestEngine(t, def)
	e.SetRunBudgets(agent.BudgetConfig{MaxTokens: 100000, MaxLLMCalls: 1}, agent.BudgetConfig{MaxTokens: 100000, MaxLLMCalls: 10})
	e.builtins = []BuiltinTool{{Name: "lookup", Parameters: map[string]any{"type": "object"}, Handler: func(context.Context, map[string]any) (string, error) { return "partial evidence survives", nil }}}
	provider.responses = []llm.CompletionResponse{{InputTokens: 10, OutputTokens: 2, ToolCalls: []message.ToolCall{{ID: "c", Name: "lookup"}}}}
	reply, err := e.Handle(context.Background(), testUserMessage(def.ID, "budget-session", "work"))
	if err != nil {
		t.Fatal(err)
	}
	got := flattenParts(reply.Parts)
	if !strings.Contains(got, "partial evidence survives") || !strings.Contains(got, "Run halted") {
		t.Fatalf("partial budget reply = %q", got)
	}
}

func TestLengthLimitedAnswerContinuesWithinRunBudget(t *testing.T) {
	def := &agent.Definition{
		ID: "continued", Name: "Continued", Enabled: true,
		LLM:      agent.LLMConfig{Provider: "test", Model: "fake-model", MaxTokens: 200},
		MaxTurns: 3, Builtins: strListPtr(),
	}
	e, provider := newHandleTestEngine(t, def)
	provider.responses = []llm.CompletionResponse{
		{Content: "The answer stopped in", FinishReason: "length", InputTokens: 10, OutputTokens: 200},
		{Content: "the middle, but now it is complete.", FinishReason: "stop", InputTokens: 20, OutputTokens: 9},
	}
	reply, err := e.Handle(context.Background(), testUserMessage(def.ID, "continued-session", "write a report"))
	if err != nil {
		t.Fatal(err)
	}
	if got := flattenParts(reply.Parts); got != "The answer stopped in the middle, but now it is complete." {
		t.Fatalf("continued reply = %q", got)
	}
	requests := provider.requestsSnapshot()
	if len(requests) != 2 {
		t.Fatalf("provider calls = %d, want 2", len(requests))
	}
	last := requests[1].Messages[len(requests[1].Messages)-1]
	if last.Role != "system" || !strings.Contains(last.Content, "Continue exactly where it stopped") {
		t.Fatalf("continuation instruction = %+v", last)
	}
}

func TestLengthLimitedAnswerWarnsWhenContinuationCeilingIsReached(t *testing.T) {
	def := &agent.Definition{
		ID: "limited", Name: "Limited", Enabled: true,
		LLM:      agent.LLMConfig{Provider: "test", Model: "fake-model", MaxTokens: 50},
		MaxTurns: 1, Builtins: strListPtr(),
	}
	e, provider := newHandleTestEngine(t, def)
	provider.responses = []llm.CompletionResponse{{Content: "partial", FinishReason: "MAX_TOKENS"}}
	reply, err := e.Handle(context.Background(), testUserMessage(def.ID, "limited-session", "write"))
	if err != nil {
		t.Fatal(err)
	}
	got := flattenParts(reply.Parts)
	if !strings.Contains(got, "partial") || !strings.Contains(got, "reached its continuation limit") {
		t.Fatalf("limited reply = %q", got)
	}
}

// TestTurnsCeiling verifies the server-side max_turns ceiling resolution (S3.2).
func TestTurnsCeiling(t *testing.T) {
	e := &Engine{}
	if got := e.turnsCeiling(); got != defaultMaxTurnsCeiling {
		t.Fatalf("unset ceiling should default to %d, got %d", defaultMaxTurnsCeiling, got)
	}
	e.SetMaxTurnsCeiling(0) // 0 → default
	if got := e.turnsCeiling(); got != defaultMaxTurnsCeiling {
		t.Fatalf("SetMaxTurnsCeiling(0) should default to %d, got %d", defaultMaxTurnsCeiling, got)
	}
	e.SetMaxTurnsCeiling(12)
	if got := e.turnsCeiling(); got != 12 {
		t.Fatalf("ceiling = %d, want 12", got)
	}
}

// TestAgentCallDepthLimit verifies the peer-agent recursion guard resolution.
func TestAgentCallDepthLimit(t *testing.T) {
	e := &Engine{}
	if got := e.agentCallDepthLimit(); got != defaultMaxAgentCallDepth {
		t.Fatalf("unset depth should default to %d, got %d", defaultMaxAgentCallDepth, got)
	}
	e.SetMaxAgentCallDepth(0)
	if got := e.agentCallDepthLimit(); got != defaultMaxAgentCallDepth {
		t.Fatalf("SetMaxAgentCallDepth(0) should default to %d, got %d", defaultMaxAgentCallDepth, got)
	}
	e.SetMaxAgentCallDepth(9)
	if got := e.agentCallDepthLimit(); got != 9 {
		t.Fatalf("depth limit = %d, want 9", got)
	}
}

package runtime

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

func TestProductiveRunExceedsOldBudgets(t *testing.T) {
	def := &agent.Definition{ID: "long", Enabled: true, LLM: agent.LLMConfig{Provider: "test", Model: "fake-model"}, Builtins: strListPtr("lookup")}
	e, p := newHandleTestEngine(t, def)
	e.builtins = []BuiltinTool{{Name: "lookup", Handler: func(_ context.Context, args map[string]any) (string, error) { return fmt.Sprint(args["index"]), nil }}}
	for i := 0; i < 60; i++ {
		p.responses = append(p.responses, llm.CompletionResponse{InputTokens: 20000, ToolCalls: []message.ToolCall{{ID: fmt.Sprint(i), Name: "lookup", Arguments: map[string]any{"index": i}}}})
	}
	p.responses = append(p.responses, llm.CompletionResponse{Content: "Completed all 60 steps."})
	reply, err := e.Handle(context.Background(), testUserMessage(def.ID, "long-session", "Complete all 60 steps"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(flattenParts(reply.Parts), "Completed all 60") || reply.Metadata[message.MetaReasoningDegraded] == "true" {
		t.Fatalf("productive run was cut short: %+v", reply)
	}
	reqs := p.requestsSnapshot()
	if !chatMessagesContain(reqs[len(reqs)-1].Messages, "user", "Complete all 60 steps") {
		t.Fatal("long run lost original goal")
	}
	if len(p.requestsSnapshot()) != 61 {
		t.Fatalf("model calls=%d", len(p.requestsSnapshot()))
	}
}

func TestTurnPolicyInheritanceUnlimitedAndCeiling(t *testing.T) {
	e := &Engine{}
	e.SetTurnLimits(7, 0)
	if got := e.effectiveMaxTurns(&agent.Definition{}); got != 7 {
		t.Fatal(got)
	}
	if got := e.effectiveMaxTurns(&agent.Definition{MaxTurns: -1}); got != 0 {
		t.Fatal(got)
	}
	e.SetTurnLimits(0, 0)
	if got := e.effectiveMaxTurns(&agent.Definition{}); got != 0 {
		t.Fatal(got)
	}
	e.SetTurnLimits(0, 9)
	if got := e.effectiveMaxTurns(&agent.Definition{MaxTurns: -1}); got != 9 {
		t.Fatal(got)
	}
}

func TestStallGuardDistinguishesProductiveWorkAndArgumentChurn(t *testing.T) {
	g := &progressGuard{limit: 3}
	for i := 0; i < 100; i++ {
		g.observe(message.ToolCall{Name: "lookup"}, fmt.Sprint(i), false)
	}
	if g.failure() != nil {
		t.Fatal("productive work stopped")
	}
	for i := 0; i < 4; i++ {
		g.observe(message.ToolCall{Name: "lookup", Arguments: map[string]any{"q": i}}, "same empty results", false)
	}
	if g.failure() == nil {
		t.Fatal("argument churn with identical results was not detected")
	}
	disabled := &progressGuard{limit: 0}
	for i := 0; i < 100; i++ {
		disabled.observe(message.ToolCall{Name: "poll"}, "pending", false)
	}
	if disabled.failure() != nil {
		t.Fatal("disabled guard tripped")
	}
}

func TestBudgetStopIsNeverReportedAsSuccess(t *testing.T) {
	def := &agent.Definition{ID: "limited", Enabled: true, MaxTurns: 1, LLM: agent.LLMConfig{Provider: "test", Model: "fake-model"}, Builtins: strListPtr("lookup")}
	e, p := newHandleTestEngine(t, def)
	sink := &trustSink{}
	e.sink = sink
	e.builtins = []BuiltinTool{{Name: "lookup", Handler: func(context.Context, map[string]any) (string, error) { return "some evidence", nil }}}
	p.responses = []llm.CompletionResponse{{ToolCalls: []message.ToolCall{{Name: "lookup", ID: "one"}}}, {Content: "Partial report"}}
	reply, err := e.Handle(context.Background(), testUserMessage(def.ID, "limited-session", "do several steps"))
	if err != nil {
		t.Fatal(err)
	}
	if reply.Metadata[message.MetaReasoningDegraded] != "true" || reply.Metadata[message.MetaOutcome] != "incomplete" {
		t.Fatalf("limit reported success: %+v", reply.Metadata)
	}
	terminal := eventsByType(sink.events, "run.completed")
	if len(terminal) != 1 {
		t.Fatalf("terminal events=%d", len(terminal))
	}
	payload := terminal[0].Payload.(map[string]any)
	if payload["success"] != false || payload["outcome"] != "incomplete" {
		t.Fatalf("terminal event claimed success: %v", payload)
	}
}

func TestCompactionPreservesGoalAndRecentEvidence(t *testing.T) {
	msgs := []llm.ChatMessage{{Role: "system", Content: "Keep permissions unchanged."}, {Role: "user", Content: "Original goal: compare projects A and B."}}
	for i := 0; i < 12; i++ {
		msgs = append(msgs, llm.ChatMessage{Role: "assistant", Content: fmt.Sprintf("Observation %d: %s END-%d", i, strings.Repeat("evidence ", 200), i)})
	}
	msgs = append(msgs, llm.ChatMessage{Role: "user", Content: "Also include operating costs."}, llm.ChatMessage{Role: "assistant", Content: "Latest evidence: cost is 42."})
	out, n := compactMessagesToFit(msgs, nil, 1500)
	if n == 0 || estimateTokens(out, nil) > 1500 {
		t.Fatalf("compaction failed: %d/%d", n, estimateTokens(out, nil))
	}
	for _, want := range []string{"Original goal", "operating costs", "cost is 42", "context_compaction"} {
		found := false
		for _, m := range out {
			if strings.Contains(m.Content, want) {
				found = true
			}
		}
		if !found {
			t.Fatalf("lost %q", want)
		}
	}
	if len(msgs) != 16 {
		t.Fatal("mutated original history")
	}
}

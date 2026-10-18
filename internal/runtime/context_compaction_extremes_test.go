package runtime

import (
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/pkg/message"
)

func TestOversizedToolResultRetainsGoalAndCallPair(t *testing.T) {
	msgs := []llm.ChatMessage{
		{Role: "system", Content: "Follow the operator's task."},
		{Role: "user", Content: "Keep reference AMBER-9107 and summarize the observations."},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "one", Name: "lookup"}}},
		{Role: "tool", ToolCallID: "one", Name: "lookup", Content: strings.Repeat("observation ", 10000)},
	}
	out, dropped := compactMessagesToFit(msgs, nil, 1500)
	if dropped == 0 || estimateTokens(out, nil) > 1500 || !chatMessagesContain(out, "user", "AMBER-9107") || !chatMessagesContain(out, "tool", "excerpt omitted") {
		t.Fatalf("oversized result lost goal or overflowed: tokens=%d out=%+v", estimateTokens(out, nil), out)
	}
	if len(msgs[3].Content) != 120000 {
		t.Fatal("compaction mutated original history")
	}
	for i, m := range out {
		if m.Role == "tool" && (i == 0 || len(out[i-1].ToolCalls) == 0) {
			t.Fatal("tool response lost its call")
		}
	}
}

func TestImpossibleContextBudgetDoesNotDiscardTheTask(t *testing.T) {
	msgs := []llm.ChatMessage{{Role: "system", Content: strings.Repeat("immutable instructions ", 1000)}, {Role: "user", Content: "Original request"}, {Role: "tool", Content: "Required evidence"}}
	out, _ := compactMessagesToFit(msgs, nil, 100)
	if len(out) != len(msgs) || !chatMessagesContain(out, "user", "Original request") {
		t.Fatal("unachievable context budget silently changed the task")
	}
}

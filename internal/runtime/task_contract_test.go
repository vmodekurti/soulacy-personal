package runtime

import (
	"context"
	"testing"

	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/internal/taskcontract"
	"github.com/soulacy/soulacy/pkg/agent"
)

func TestHandleEmitsUniversalTaskContract(t *testing.T) {
	e, provider := newHandleTestEngine(t, &agent.Definition{
		ID: "contract-agent", Name: "Contract Agent", Enabled: true,
		SystemPrompt: "Answer plainly.",
		LLM:          agent.LLMConfig{Provider: "test", Model: "fake-model"},
		MaxTurns:     3,
	})
	sink := &captureSink6{}
	e.sink = sink
	provider.responses = []llm.CompletionResponse{{Content: "A grounded direct answer."}}

	if _, err := e.Handle(context.Background(), testUserMessage("contract-agent", "contract-session", "What is this?")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	started := eventsByType(sink.events, "task.contract.started")
	completed := eventsByType(sink.events, "task.contract.completed")
	if len(started) != 1 || len(completed) != 1 {
		t.Fatalf("contract events: started=%d completed=%d", len(started), len(completed))
	}
	final, ok := completed[0].Payload.(taskcontract.Snapshot)
	if !ok {
		t.Fatalf("completed payload type = %T", completed[0].Payload)
	}
	if final.Outcome != taskcontract.OutcomeDirectAnswer || final.State != "completed" {
		t.Fatalf("final contract = %+v", final)
	}
	runs := eventsByType(sink.events, "run.completed")
	payload, ok := runs[0].Payload.(map[string]any)
	if !ok || payload["task_outcome"] != taskcontract.OutcomeDirectAnswer {
		t.Fatalf("run.completed lacks task outcome: %#v", runs[0].Payload)
	}
}

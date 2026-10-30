package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/internal/reasoning"
	"github.com/soulacy/soulacy/internal/taskcontract"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

func TestStructuredOutputRequiresDeclaredFields(t *testing.T) {
	for _, tc := range []struct {
		name, first, repair string
		wantError           bool
		calls               int
	}{
		{"valid", `{"multiplier":12}`, "", false, 1},
		{"missing field repaired", `{}`, `{"multiplier":12}`, false, 2},
		{"wrong type repaired", `{"multiplier":"12"}`, `{"multiplier":12}`, false, 2},
		{"invalid repair rejected", `{}`, `{"multiplier":"twelve"}`, true, 2},
		{"empty repair rejected", `{}`, ``, true, 2},
		{"prose around JSON rejected", `Here is the report: {"multiplier":12}`, `{}`, true, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, p := newHandleTestEngine(t, &agent.Definition{ID: "assistant", Name: "Assistant", Enabled: true, MaxTurns: 3, Builtins: strListPtr(), LLM: agent.LLMConfig{Provider: "test", Model: "fake-model", OutputSchema: map[string]any{"type": "object", "required": []string{"multiplier"}, "properties": map[string]any{"multiplier": map[string]any{"type": "number"}}}}})
			p.responses = []llm.CompletionResponse{{Content: tc.first}, {Content: tc.repair}}
			reply, err := e.Handle(context.Background(), testUserMessage("assistant", "schema-test", "Return the report"))
			if (err != nil) != tc.wantError {
				t.Fatalf("Handle error = %v", err)
			}
			if len(p.requestsSnapshot()) != tc.calls {
				t.Fatalf("calls = %d", len(p.requestsSnapshot()))
			}
			if tc.wantError {
				if !strings.Contains(err.Error(), "output schema validation failed") {
					t.Fatal(err)
				}
				if reply.Metadata[message.MetaTaskOutcome] != taskcontract.OutcomeFailed {
					t.Fatalf("invalid output not marked failed: %#v", reply)
				}
				sess := e.getOrCreateSession("schema-test", "assistant")
				for _, m := range sess.History {
					if m.Role == "assistant" && m.Content == tc.repair {
						t.Fatal("invalid repair persisted as assistant answer")
					}
				}
			} else if got := flattenParts(reply.Parts); got != `{"multiplier":12}` {
				t.Fatalf("output = %q", got)
			}
		})
	}
}

func TestStructuredOutputIsNotRewrittenForPresentation(t *testing.T) {
	const answer = `{"final_answer":"result","thought":"required domain field"}`
	def := &agent.Definition{ID: "assistant", Name: "Assistant", Enabled: true, MaxTurns: 3, Builtins: strListPtr(), LLM: agent.LLMConfig{Provider: "test", Model: "fake-model", OutputSchema: map[string]any{"type": "object", "required": []string{"final_answer", "thought"}}}}
	e, p := newHandleTestEngine(t, def)
	p.responses = []llm.CompletionResponse{{Content: answer}}
	msg := testUserMessage("assistant", "presentation-test", "Return JSON")
	msg.Metadata = map[string]string{"response.mode": "voice"}
	ctx := withChartSink(context.Background())
	chartSinkFrom(ctx).add("```chart\n{}\n```")
	reply, err := e.Handle(ctx, msg)
	if err != nil {
		t.Fatal(err)
	}
	if got := flattenParts(reply.Parts); got != answer {
		t.Fatalf("schema-valid JSON transformed: %q", got)
	}
}

func TestReasoningOutputSchemaRejectsInvalidRepair(t *testing.T) {
	def := &agent.Definition{ID: "loop-agent", Name: "Loop Agent", Enabled: true,
		LLM:       agent.LLMConfig{Provider: "classic-prov", OutputSchema: map[string]any{"type": "object", "required": []string{"answer"}}},
		Reasoning: agent.ReasoningConfig{Strategy: "react", MaxSteps: 3}}
	backend := &fakeLoopBackend{scripted: []reasoning.ThinkResponse{{IsDone: true, FinalAnswer: `{}`}}, reflectOut: `{}`}
	e, sink := newReasoningEngine(t, def, backend)
	_, err := e.Handle(context.Background(), inboundMsg("loop-agent", "Return required answer"))
	if err == nil || !strings.Contains(err.Error(), "output schema validation failed") {
		t.Fatalf("invalid reasoning result accepted: %v", err)
	}
	if len(sink.byType("message.out")) != 0 {
		t.Fatal("invalid result delivered")
	}
}

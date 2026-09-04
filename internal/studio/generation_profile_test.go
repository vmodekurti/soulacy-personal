package studio

import (
	"context"
	"strings"
	"testing"
)

func TestGenerationProfile_LocalCompactUsesStrictGuardrails(t *testing.T) {
	cat := Catalog{}
	gp := BuildGenerationProfile("ollama", "gemma3:4b", "http://localhost:11434", "build something useful for my team", cat)
	if !gp.Local {
		t.Fatal("expected ollama localhost builder to be local")
	}
	if !gp.Compact {
		t.Fatal("expected gemma compact profile")
	}
	if !gp.StrictMode {
		t.Fatal("expected strict mode for compact local builder")
	}
	if gp.Confidence != "low" || gp.NextAction != "ask_clarify" {
		t.Fatalf("confidence/action = %q/%q, want low/ask_clarify", gp.Confidence, gp.NextAction)
	}
	block := GenerationProfilePromptBlock(&gp)
	for _, want := range []string{"Builder locality: LOCAL", "Compact local model contract", "compiler pass"} {
		if !strings.Contains(block, want) {
			t.Fatalf("profile prompt missing %q:\n%s", want, block)
		}
	}
}

func TestGenerationProfile_WithPatternRaisesConfidence(t *testing.T) {
	intent := "Every morning search latest AI news and send a digest"
	gp := BuildGenerationProfile("ollama", "qwen3:32b", "http://localhost:11434", intent, Catalog{})
	if !gp.PatternMatched && !gp.PlanMatched {
		t.Fatal("expected curated search/schedule pattern to match")
	}
	if gp.Confidence != "high" {
		t.Fatalf("confidence = %q, want high", gp.Confidence)
	}
}

func TestCompile_RepairsMalformedLocalJSON(t *testing.T) {
	llm := sequenceLLM{
		responses: []string{
			"```json\n{\"name\":\"Broken\"",
			`{
			  "name":"Echo",
			  "system_prompt":"Echo the input safely.",
			  "trigger":{"type":"manual"},
			  "flow":{"nodes":[{"id":"echo","kind":"agent","agent":"notifier","input":"Reply with {{ .trigger.text }}","output":"reply"}],"edges":[{"from":"echo","to":"end"}],"entry":"echo"},
			  "new_agents":[{"id":"notifier","name":"Notifier","description":"Replies with provided text","system_prompt":"You are a concise notifier. Return the provided message exactly and handle empty input with a short fallback."}]
			}`,
		},
	}
	cat := Catalog{Generation: &GenerationProfile{Local: true, Compact: true, StrictMode: true}}
	res, err := Compile(context.Background(), &llm, "echo the user", cat, nil)
	if err != nil {
		t.Fatalf("Compile should repair malformed local JSON: %v", err)
	}
	if res.Workflow.Name != "Echo" {
		t.Fatalf("workflow name = %q, want Echo", res.Workflow.Name)
	}
	if llm.calls != 2 {
		t.Fatalf("llm calls = %d, want 2", llm.calls)
	}
}

type sequenceLLM struct {
	responses []string
	calls     int
}

func (s *sequenceLLM) Complete(_ context.Context, _ string) (string, error) {
	if s.calls >= len(s.responses) {
		return s.responses[len(s.responses)-1], nil
	}
	out := s.responses[s.calls]
	s.calls++
	return out, nil
}

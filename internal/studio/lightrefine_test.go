package studio

import (
	"context"
	"strings"
	"testing"
)

// recordingLLM captures the last prompt it was given so a test can assert which
// instruction (full vs light) was sent to the model.
type recordingLLM struct {
	out    string
	prompt string
}

func (r *recordingLLM) Complete(ctx context.Context, prompt string) (string, error) {
	r.prompt = prompt
	return r.out, nil
}

func TestBuildLightRefineInstruction_IsTouchUpNotRewrite(t *testing.T) {
	p := BuildLightRefineInstruction("Every weekday at 8am, post AI news to Telegram.", Catalog{})

	for _, want := range []string{
		"LIGHT touch-up",
		"ALREADY a refined specification",
		"Do NOT rewrite",
		"refined_intent", "summary", "assumptions", "questions",
		"Every weekday at 8am, post AI news to Telegram.", // the edited text itself
	} {
		if !strings.Contains(p, want) {
			t.Errorf("light instruction missing %q", want)
		}
	}
	// The light pass must NOT carry the full-rewrite analyst framing.
	if strings.Contains(p, "turn their rough, often vague description") {
		t.Error("light instruction should not include the full-refine rewrite framing")
	}
}

func TestRefineInstructionsUseUnifiedArchitectureGuidance(t *testing.T) {
	for name, p := range map[string]string{
		"full":  BuildRefinePromptInstruction("build a weather assistant", Catalog{}),
		"light": BuildLightRefineInstruction("build a weather assistant", Catalog{}),
	} {
		for _, want := range []string{
			"workflow|auto|react|plan_execute",
			"\"auto\": the recommended default",
			"advanced/manual escape hatch ONLY",
			"Do NOT choose \"react\" merely because the agent uses tools",
			"Ordinary tool use should be \"auto\"",
			"long adaptive work should be \"plan_execute\"",
		} {
			if !strings.Contains(p, want) {
				t.Errorf("%s refine instruction missing %q", name, want)
			}
		}
	}
}

func TestLightRefinePrompt_UsesLightInstructionAndParses(t *testing.T) {
	out := `{
	  "refined_intent": "Every weekday at 8am, search the web for AI news, summarize the top 5, and post to Telegram.",
	  "summary": "Daily AI news digest to Telegram.",
	  "assumptions": [],
	  "questions": []
	}`
	llm := &recordingLLM{out: out}
	r, err := LightRefinePrompt(context.Background(), llm, "weekday 8am ai news telegram", Catalog{})
	if err != nil {
		t.Fatalf("LightRefinePrompt: %v", err)
	}
	if !strings.Contains(llm.prompt, "LIGHT touch-up") {
		t.Error("LightRefinePrompt did not send the light instruction")
	}
	if r.Original != "weekday 8am ai news telegram" {
		t.Errorf("original not echoed: %q", r.Original)
	}
	if !strings.Contains(r.RefinedIntent, "top 5") {
		t.Errorf("refined intent not parsed: %q", r.RefinedIntent)
	}
}

func TestRefinePrompt_StillUsesFullInstruction(t *testing.T) {
	llm := &recordingLLM{out: `{"refined_intent":"x","summary":"y"}`}
	if _, err := RefinePrompt(context.Background(), llm, "do a thing", Catalog{}); err != nil {
		t.Fatalf("RefinePrompt: %v", err)
	}
	if strings.Contains(llm.prompt, "LIGHT touch-up") {
		t.Error("RefinePrompt must not send the light instruction")
	}
	if !strings.Contains(llm.prompt, "requirements analyst") {
		t.Error("RefinePrompt should send the full analyst instruction")
	}
}

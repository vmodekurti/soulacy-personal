package reasoning

import (
	"testing"

	"github.com/soulacy/soulacy/pkg/agent"
)

// The default ("auto"/unset) strategy must resolve to the classic native-tool
// loop on a tool-capable model, and to ReAct on a model without native
// function-calling — the single decision every creation surface relies on.
func TestLoopConfigFromDefinition_AutoResolution(t *testing.T) {
	cases := []struct {
		name              string
		strategy          string
		systemCap         bool
		supportsTools     bool
		wantLoop          bool   // true = reasoning loop, false = classic
		wantStrategyWhenL string // expected loop strategy when wantLoop
	}{
		{"unset + tools → classic", "", false, true, false, ""},
		{"auto + tools → classic", "auto", false, true, false, ""},
		{"unset + no tools → react", "", false, false, true, StrategyReAct},
		{"auto + no tools → react", "auto", false, false, true, StrategyReAct},
		{"explicit react always loops", "react", false, true, true, StrategyReAct},
		{"explicit plan_execute always loops", "plan_execute", false, true, true, StrategyPlanExecute},
		// System capability does NOT push an unset/auto agent into the loop — the
		// classic loop handles system tools fine. It only converts an EXPLICIT
		// plan_execute to react (system tools break plan_execute's single arg).
		{"auto + system + tools → classic", "auto", true, true, false, ""},
		{"explicit plan_execute + system → react", "plan_execute", true, true, true, StrategyReAct},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			def := &agent.Definition{Reasoning: agent.ReasoningConfig{Strategy: tc.strategy}}
			if tc.systemCap {
				def.Capabilities = []string{"system"}
			}
			cfg, ok := LoopConfigFromDefinition(def, "sys", tc.supportsTools)
			if ok != tc.wantLoop {
				t.Fatalf("loop=%v, want %v", ok, tc.wantLoop)
			}
			if tc.wantLoop && string(cfg.Strategy) != tc.wantStrategyWhenL {
				t.Fatalf("strategy=%q, want %q", cfg.Strategy, tc.wantStrategyWhenL)
			}
		})
	}
}

func TestProviderSupportsNativeTools(t *testing.T) {
	// Every shipped provider supports native tool-calling.
	for _, p := range []string{"ollama", "ollama_cloud", "google", "gemini", "anthropic", "openai", "nvidia", "groq", "", "something-new"} {
		if !ProviderSupportsNativeTools(p) {
			t.Fatalf("provider %q should support native tools by default", p)
		}
	}
}

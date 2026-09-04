package reasoning_test

// Story E15 conformance: a custom reasoning loop registered through the SDK
// factory registry is selected by the agent's reasoning.strategy key and
// actually executes — end to end through Loop.Run, the same path SOUL.yaml
// configuration takes via LoopConfigFromDefinition.

import (
	"context"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/reasoning"
	"github.com/soulacy/soulacy/pkg/agent"
	sdkreasoning "github.com/soulacy/soulacy/sdk/reasoning"
	"github.com/soulacy/soulacy/sdk/registry"
)

// echoStrategy is a minimal custom loop: one synthetic step, then a final
// answer derived from the task — no LLM round-trips at all. Stands in for
// Tree-of-Thought / Consensus-Swarm style extensions.
type echoStrategy struct{ marker string }

func (e echoStrategy) Run(ctx context.Context, env sdkreasoning.Env, taskInput string) ([]sdkreasoning.Step, sdkreasoning.ReflectResponse) {
	steps := []sdkreasoning.Step{{
		ID:      "custom-1",
		Thought: "custom strategy engaged",
		Obs:     sdkreasoning.Observation{Content: "tools available: " + joinNames(env.Config.ToolNames)},
	}}
	return steps, sdkreasoning.ReflectResponse{Output: e.marker + ": " + taskInput}
}

func joinNames(names []string) string {
	out := ""
	for i, n := range names {
		if i > 0 {
			out += ","
		}
		out += n
	}
	return out
}

func init() {
	registry.MustRegisterReasoningStrategy("echo_conformance", func(cfg map[string]any) (sdkreasoning.Strategy, error) {
		return echoStrategy{marker: "ECHO"}, nil
	})
}

func TestCustomStrategyInjectedAndRun(t *testing.T) {
	loop := reasoning.New(reasoning.LoopConfig{
		Strategy:  "echo_conformance",
		ToolNames: []string{"web_search"},
	}, &stubLLM{doneOnStep: 1}, &stubExecutor{})

	res := loop.Run(context.Background(), "agent-x", "say hello")
	if res.Output != "ECHO: say hello" {
		t.Fatalf("custom strategy did not run, output = %q", res.Output)
	}
	if len(res.Steps) != 1 || res.Steps[0].ID != "custom-1" {
		t.Fatalf("custom step trace missing: %+v", res.Steps)
	}
	if !res.Confident {
		t.Fatal("no tool errors — should be confident")
	}
}

// The SOUL.yaml path: reasoning.strategy carries the custom name through
// LoopConfigFromDefinition unchanged.
func TestCustomStrategyNameFromDefinition(t *testing.T) {
	def := &agent.Definition{ID: "a1"}
	def.Reasoning.Strategy = "echo_conformance"
	cfg, ok := reasoning.LoopConfigFromDefinition(def, "sys", true)
	if !ok {
		t.Fatal("reasoning block with strategy must enable the loop")
	}
	if cfg.Strategy != "echo_conformance" {
		t.Fatalf("strategy = %q", cfg.Strategy)
	}
	loop := reasoning.New(cfg, &stubLLM{doneOnStep: 1}, &stubExecutor{})
	res := loop.Run(context.Background(), def.ID, "task via SOUL.yaml")
	if res.Output != "ECHO: task via SOUL.yaml" {
		t.Fatalf("output = %q", res.Output)
	}
}

// An unknown strategy name is a CONFIGURATION ERROR, not a runtime condition to
// paper over (P0-6). It used to substitute ReAct silently, which meant an
// operator who wrote `strategy: plan-execute` (hyphen) got ReAct forever while
// the banner still named their intended strategy.
func TestUnknownStrategyIsAConfigurationError(t *testing.T) {
	llm := &stubLLM{doneOnStep: 1, reflectOut: "react answer"}
	loop := reasoning.New(reasoning.LoopConfig{Strategy: "no_such_strategy"}, llm, &stubExecutor{})
	res := loop.Run(context.Background(), "a", "simple question")

	// Nothing may execute — the misconfiguration is knowable before any work.
	if llm.thinkCalls != 0 {
		t.Errorf("an unregistered strategy must not silently run ReAct (Think called %d times)", llm.thinkCalls)
	}
	if res.Confident {
		t.Error("a configuration error must not report a confident run")
	}
	// The message has to be actionable: name the bad value AND what is valid.
	for _, want := range []string{"configuration error", "no_such_strategy", "react", "plan_execute"} {
		if !strings.Contains(res.Output, want) {
			t.Errorf("output should mention %q: %q", want, res.Output)
		}
	}
	// It must be visible as a step too, so the trace shows why nothing ran.
	if len(res.Steps) == 0 || res.Steps[0].Obs.Source != "controller" {
		t.Errorf("the config error should appear as a controller step: %+v", res.Steps)
	}
}

// An EMPTY strategy is not an error: it means "auto", resolved by detectStrategy.
func TestEmptyStrategyStillResolves(t *testing.T) {
	llm := &stubLLM{doneOnStep: 1, reflectOut: "answer"}
	loop := reasoning.New(reasoning.LoopConfig{Strategy: ""}, llm, &stubExecutor{})
	res := loop.Run(context.Background(), "a", "simple question")
	if strings.Contains(res.Output, "configuration error") {
		t.Fatalf("an empty strategy must resolve via auto-detection, not error: %q", res.Output)
	}
}

// Built-ins are themselves registry-routed.
func TestBuiltinStrategiesRegistered(t *testing.T) {
	have := map[string]bool{}
	for _, n := range registry.ReasoningStrategies() {
		have[n] = true
	}
	for _, want := range []string{"react", "plan_execute"} {
		if !have[want] {
			t.Errorf("built-in strategy %q not registered (have %v)", want, registry.ReasoningStrategies())
		}
	}
}

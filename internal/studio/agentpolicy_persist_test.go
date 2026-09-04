package studio

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/pkg/agent"
	"gopkg.in/yaml.v3"
)

func contractDraft() Draft {
	return Draft{
		Name:     "News Agent",
		Strategy: StrategyPlanExecute,
		Trigger:  Trigger{Type: "manual"},
		Tools:    []string{"web_search"},
		Policy: &AgentPolicy{
			Contract: &AgentContract{
				Goal:               "Deliver a daily news briefing.",
				Instructions:       "Be concise and cite sources.",
				CompletionCriteria: "The briefing has been sent to Telegram.",
			},
			Plan: &PlanExecutePolicy{
				ReplanAfterFailure:        true,
				ParallelIndependentSteps:  false,
				ApprovalBeforeSideEffects: true,
				PlanTimeout:               "2m",
			},
		},
	}
}

// The bug: everything the Build step's contract panel edited was accepted by the
// API and then dropped — never written to the definition, never read back.
func TestContractSurvivesSaveAndReload(t *testing.T) {
	def, err := ToAgentDefinition(contractDraft(), false)
	if err != nil {
		t.Fatalf("ToAgentDefinition: %v", err)
	}
	if def.Reasoning.Contract == nil {
		t.Fatal("contract was dropped on save")
	}
	if got := def.Reasoning.Contract.Goal; got != "Deliver a daily news briefing." {
		t.Errorf("goal = %q", got)
	}
	if got := def.Reasoning.Contract.CompletionCriteria; got != "The briefing has been sent to Telegram." {
		t.Errorf("completion criteria = %q", got)
	}

	back := FromAgentDefinition(def)
	if back.Policy == nil || back.Policy.Contract == nil {
		t.Fatal("contract did not come back when the agent was reopened")
	}
	if back.Policy.Contract.Instructions != "Be concise and cite sources." {
		t.Errorf("instructions = %q", back.Policy.Contract.Instructions)
	}
	if back.Policy.Plan == nil {
		t.Fatal("plan policy did not come back")
	}
	// The flag the operator turned OFF must stay off. A plain bool with
	// omitempty would have silently reverted it to the default true.
	if back.Policy.Plan.ParallelIndependentSteps {
		t.Error("parallel steps was turned off but came back on")
	}
	if !back.Policy.Plan.ReplanAfterFailure || !back.Policy.Plan.ApprovalBeforeSideEffects {
		t.Error("flags the operator left on came back off")
	}
	if back.Policy.Plan.PlanTimeout != "2m" {
		t.Errorf("plan timeout = %q", back.Policy.Plan.PlanTimeout)
	}
}

// SOUL.yaml is the on-disk form and the Code view's source of truth.
func TestContractSurvivesYAMLRoundTrip(t *testing.T) {
	def, err := ToAgentDefinition(contractDraft(), false)
	if err != nil {
		t.Fatalf("ToAgentDefinition: %v", err)
	}
	out, err := yaml.Marshal(def)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(out), "completion_criteria:") {
		t.Fatalf("contract missing from SOUL.yaml:\n%s", out)
	}
	var back agent.Definition
	if err := yaml.Unmarshal(out, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Reasoning.Contract == nil || back.Reasoning.Contract.Goal == "" {
		t.Fatal("contract lost through YAML")
	}
	if back.Reasoning.Contract.Plan == nil || back.Reasoning.Contract.Plan.ParallelIndependentSteps == nil {
		t.Fatal("plan flags lost through YAML")
	}
	if *back.Reasoning.Contract.Plan.ParallelIndependentSteps {
		t.Error("parallel=false did not survive YAML")
	}
}

// A contract nobody can read changes nothing about how the agent behaves: the
// reasoning loop is driven by the system prompt.
func TestContractReachesTheSystemPrompt(t *testing.T) {
	def, err := ToAgentDefinition(contractDraft(), false)
	if err != nil {
		t.Fatalf("ToAgentDefinition: %v", err)
	}
	for _, want := range []string{
		"Deliver a daily news briefing.",
		"Be concise and cite sources.",
		"The briefing has been sent to Telegram.",
	} {
		if !strings.Contains(def.SystemPrompt, want) {
			t.Errorf("system prompt is missing %q:\n%s", want, def.SystemPrompt)
		}
	}
}

// Re-saving an agent must not stack another copy of the contract each time.
func TestContractNotDuplicatedOnResave(t *testing.T) {
	def, err := ToAgentDefinition(contractDraft(), false)
	if err != nil {
		t.Fatalf("first save: %v", err)
	}
	again, err := ToAgentDefinition(FromAgentDefinition(def), false)
	if err != nil {
		t.Fatalf("second save: %v", err)
	}
	if n := strings.Count(again.SystemPrompt, operatorContractHeading); n != 1 {
		t.Errorf("operator contract appears %d times after a re-save, want 1", n)
	}
}

// Turning parallelism off is the one loop flag the engine already honours.
func TestSerialPlanMapsToMaxParallelOne(t *testing.T) {
	def, err := ToAgentDefinition(contractDraft(), false)
	if err != nil {
		t.Fatalf("ToAgentDefinition: %v", err)
	}
	if def.Reasoning.MaxParallelSteps != 1 {
		t.Errorf("MaxParallelSteps = %d, want 1 when parallel steps are disabled", def.Reasoning.MaxParallelSteps)
	}
}

// A draft that states no policy must not gain an empty contract block.
func TestNoPolicyWritesNoContract(t *testing.T) {
	d := contractDraft()
	d.Policy = nil
	def, err := ToAgentDefinition(d, false)
	if err != nil {
		t.Fatalf("ToAgentDefinition: %v", err)
	}
	if def.Reasoning.Contract != nil {
		t.Error("a draft with no policy produced a contract block")
	}
	if strings.Contains(def.SystemPrompt, operatorContractHeading) {
		t.Error("a draft with no policy added an operator-contract paragraph")
	}
}

// A flag the operator turned OFF must reach the GUI as an explicit false.
// The panel renders a missing field as `?? true`, so `omitempty` on these
// booleans made "off" come back as "on" — the opposite of what was saved.
func TestPolicyFalseSurvivesJSON(t *testing.T) {
	back := FromAgentDefinition(mustDef(t, contractDraft()))
	raw, err := json.Marshal(back.Policy)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"parallel_independent_steps":false`) {
		t.Errorf("parallel=false was dropped from the draft JSON:\n%s", raw)
	}

	var round AgentPolicy
	if err := json.Unmarshal(raw, &round); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if round.Plan == nil || round.Plan.ParallelIndependentSteps {
		t.Error("parallel=false did not survive the JSON round-trip")
	}
	if round.Plan.ReplanAfterFailure != true || round.Plan.ApprovalBeforeSideEffects != true {
		t.Error("flags left on did not survive")
	}
}

func mustDef(t *testing.T, d Draft) agent.Definition {
	t.Helper()
	def, err := ToAgentDefinition(d, false)
	if err != nil {
		t.Fatalf("ToAgentDefinition: %v", err)
	}
	return def
}

// The Build screen's contract panel opened blank on every generated agent,
// because nothing in generation ever produced a contract — the model was only
// asked for a system prompt. These cover the mapping from the designer's answer
// onto the draft the panel renders.
func TestGeneratedAgentCarriesAContract(t *testing.T) {
	p := agentSpecPayload{
		Goal:               "Send a weekday news briefing to Telegram.",
		Instructions:       "Be concise. Cite each source.",
		CompletionCriteria: "The briefing has been accepted by the Telegram channel.",
	}
	pol := contractPolicyFrom(p, "some intent")
	if pol == nil || pol.Contract == nil {
		t.Fatal("no contract produced")
	}
	if pol.Contract.Goal != p.Goal {
		t.Errorf("goal = %q", pol.Contract.Goal)
	}
	if pol.Contract.Instructions != p.Instructions {
		t.Errorf("instructions = %q", pol.Contract.Instructions)
	}
	if pol.Contract.CompletionCriteria != p.CompletionCriteria {
		t.Errorf("completion = %q", pol.Contract.CompletionCriteria)
	}
}

// A model that ignores the new fields must still not leave the Goal box blank:
// the user's own intent is what a successful run achieves.
func TestGoalFallsBackToTheIntent(t *testing.T) {
	pol := contractPolicyFrom(agentSpecPayload{}, "  Every weekday, send me an AI news digest.  ")
	if pol == nil || pol.Contract == nil {
		t.Fatal("no contract produced")
	}
	if pol.Contract.Goal != "Every weekday, send me an AI news digest." {
		t.Errorf("goal = %q, want the trimmed intent", pol.Contract.Goal)
	}
	// Not invented — an empty completion criterion must stay empty so the
	// "no completion criteria" warning remains truthful.
	if pol.Contract.CompletionCriteria != "" {
		t.Errorf("completion criteria was invented: %q", pol.Contract.CompletionCriteria)
	}
}

func TestNoContractWhenNothingToSay(t *testing.T) {
	if pol := contractPolicyFrom(agentSpecPayload{}, "   "); pol != nil {
		t.Errorf("expected nil policy, got %+v", pol)
	}
}

// The designer must actually be ASKED for the contract, or the fields above are
// only ever populated by the fallback.
func TestAgentPromptAsksForTheContract(t *testing.T) {
	prompt := BuildAgentPrompt("do a thing", Catalog{}, "auto", nil)
	for _, want := range []string{"\"goal\"", "\"instructions\"", "\"completion_criteria\""} {
		if !strings.Contains(prompt, want) {
			t.Errorf("agent designer prompt never asks for %s", want)
		}
	}
}

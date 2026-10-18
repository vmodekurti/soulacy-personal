package studio

// Opening a saved workflow and saving it again must not change anything the
// user did not touch.
//
// The reasoning-agent branch already preserved the tuned budgets — there is a
// comment on the Draft fields saying so in as many words. The WORKFLOW branch
// preserved neither max_turns nor the memory policy: FromAgentDefinition
// dropped them on open, and ToAgentDefinition re-emitted the constants 15 and
// 8000 on save. So opening a workflow to fix one node reset budgets someone had
// deliberately raised in SOUL.yaml, with no diff shown and nothing in the save
// response to notice it by.

import (
	"testing"

	"github.com/soulacy/soulacy/pkg/agent"
	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// tunedWorkflowAgent is a saved workflow whose owner raised both budgets.
func tunedWorkflowAgent() agent.Definition {
	return agent.Definition{
		ID:       "tuned-digest",
		Name:     "Tuned Digest",
		Enabled:  true,
		Trigger:  agent.TriggerCron,
		Schedule: &agent.Schedule{Cron: "0 7 * * 1-5"},
		MaxTurns: 40,
		Memory: agent.MemoryPolicy{
			MaxTokens:   32000,
			ReadScopes:  []string{"session"},
			WriteScopes: []string{"session"},
		},
		LLM: agent.LLMConfig{Provider: "ollama", Model: "llama3"},
		Workflow: &agent.WorkflowSpec{
			Entry: "step",
			Nodes: []sdkr.FlowNode{{ID: "step", Kind: sdkr.FlowNodeLLM, Input: "go", Output: "out"}},
		},
	}
}

func TestWorkflowRoundTrip_KeepsTheTunedTurnBudget(t *testing.T) {
	def := tunedWorkflowAgent()
	back, err := ToAgentDefinition(FromAgentDefinition(def), true)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if back.MaxTurns != 40 {
		t.Errorf("max_turns went from 40 to %d — a re-save reset a budget the user raised", back.MaxTurns)
	}
}

func TestWorkflowRoundTrip_KeepsTheTunedMemoryPolicy(t *testing.T) {
	def := tunedWorkflowAgent()
	back, err := ToAgentDefinition(FromAgentDefinition(def), true)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if back.Memory.MaxTokens != 32000 {
		t.Errorf("memory.max_tokens went from 32000 to %d", back.Memory.MaxTokens)
	}
	if len(back.Memory.ReadScopes) != 1 || back.Memory.ReadScopes[0] != "session" {
		t.Errorf("memory.read_scopes lost: %v", back.Memory.ReadScopes)
	}
	if len(back.Memory.WriteScopes) != 1 || back.Memory.WriteScopes[0] != "session" {
		t.Errorf("memory.write_scopes lost: %v", back.Memory.WriteScopes)
	}
}

// An agent that never set them still gets Studio's defaults, rather than an
// all-zero memory block that would disable memory entirely.
func TestWorkflowRoundTrip_StillDefaultsWhatWasNeverSet(t *testing.T) {
	def := tunedWorkflowAgent()
	def.MaxTurns = 0
	def.Memory = agent.MemoryPolicy{}

	back, err := ToAgentDefinition(FromAgentDefinition(def), true)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if back.MaxTurns != 0 {
		t.Errorf("max_turns default = %d, want 0 (inherit)", back.MaxTurns)
	}
	if back.Memory.MaxTokens != 8000 {
		t.Errorf("memory.max_tokens default = %d, want 8000", back.Memory.MaxTokens)
	}
}

// The reasoning-agent branch had the turn budget already; it must not lose the
// memory policy now that both go through the same helper.
func TestReasoningAgentRoundTrip_KeepsBothBudgets(t *testing.T) {
	def := tunedWorkflowAgent()
	def.Workflow = nil
	def.Reasoning = agent.ReasoningConfig{Strategy: "plan_execute"}
	def.SystemPrompt = "You are a careful analyst."

	d := FromAgentDefinition(def)
	if !d.IsAgent() {
		t.Fatal("a reasoning agent came back as a workflow draft")
	}
	back, err := ToAgentDefinition(d, true)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if back.MaxTurns != 40 {
		t.Errorf("max_turns = %d, want 40", back.MaxTurns)
	}
	if back.Memory.MaxTokens != 32000 {
		t.Errorf("memory.max_tokens = %d, want 32000", back.Memory.MaxTokens)
	}
}

// The draft must not alias the loader's live definition: mutating the draft's
// copy would edit the running agent in memory.
func TestFromAgentDefinition_CopiesTheMemoryScopes(t *testing.T) {
	def := tunedWorkflowAgent()
	d := FromAgentDefinition(def)
	if d.Memory == nil {
		t.Fatal("memory was dropped on open")
	}
	d.Memory.ReadScopes[0] = "mutated"
	if def.Memory.ReadScopes[0] != "session" {
		t.Error("editing the draft reached back into the loaded definition")
	}
}

func TestExecutionPolicyRoundTripPreservesExplicitUnlimited(t *testing.T) {
	for _, workflow := range []bool{false, true} {
		t.Run(map[bool]string{false: "reasoning", true: "workflow"}[workflow], func(t *testing.T) {
			def := tunedWorkflowAgent()
			if !workflow {
				def.Workflow = nil
				def.SystemPrompt = "Complete the requested research."
				def.Reasoning = agent.ReasoningConfig{Strategy: "react", TotalTimeout: "20m"}
			}
			zero := 0
			def.MaxTurns = -1
			def.RunTimeout = "0"
			def.NoProgressLimit = &zero
			def.Budget = &agent.BudgetConfig{}
			draft := FromAgentDefinition(def)
			back, err := ToAgentDefinition(draft, true)
			if err != nil {
				t.Fatal(err)
			}
			if back.MaxTurns != -1 || back.RunTimeout != "0" || back.NoProgressLimit == nil || *back.NoProgressLimit != 0 || back.Budget == nil || *back.Budget != (agent.BudgetConfig{}) {
				t.Fatalf("explicit unlimited policy was lost: turns=%d timeout=%s progress=%v budget=%v", back.MaxTurns, back.RunTimeout, back.NoProgressLimit, back.Budget)
			}
			draft.Budget.MaxTokens = 123
			*draft.NoProgressLimit = 7
			if def.Budget.MaxTokens != 0 || *def.NoProgressLimit != 0 || back.Budget.MaxTokens != 0 || *back.NoProgressLimit != 0 {
				t.Fatal("editing the draft changed the loaded or saved policy")
			}
		})
	}
}

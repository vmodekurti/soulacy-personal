package runtime

import (
	"strings"
	"testing"

	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

// Genie writes the shared operating contract into every agent it builds. It
// has to live under it too — that contract is where "never assume an action
// succeeded unless a tool confirms it" is written down (#234).
func TestGenieRunsUnderTheSharedContract(t *testing.T) {
	l := &Loader{agents: map[string]*agent.Definition{}}
	l.seedBuiltins()

	genie, ok := l.agents[GenieAgentID]
	if !ok {
		t.Fatal("Genie must be seeded as a built-in")
	}
	for _, want := range []string{
		"## Soulacy Agent Operating Contract",
		"Never assume an action succeeded unless a tool confirms it",
		"Distinguish completed work from recommendations",
		"State unresolved limitations honestly",
	} {
		if !strings.Contains(genie.SystemPrompt, want) {
			t.Errorf("Genie's prompt is missing %q", want)
		}
	}
	// The role prompt must survive underneath it.
	if !strings.Contains(genie.SystemPrompt, "You are Genie") {
		t.Error("the contract replaced Genie's role prompt instead of preceding it")
	}
	if !strings.Contains(genie.SystemPrompt, "## Agent Role") {
		t.Error("the role prompt should be introduced as its own section")
	}
}

// Announcing an intention is not doing the thing. The prompt has to say so,
// because this is exactly what Genie did: promised an install across several
// turns and only admitted it could not when challenged (#233).
func TestGeniePromptForbidsPromisingWorkItHasNotDone(t *testing.T) {
	prompt := genieWithContract().SystemPrompt
	for _, want := range []string{
		"Announcing an intention is not doing the thing",
		"unless a tool has already done it",
		"this reply is the last thing you will say",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("Genie's prompt should say %q", want)
		}
	}
}

// An on-disk SOUL.yaml overrides Genie's prompt; hardening must put the
// contract back, or the honesty rules are one file edit away from gone.
func TestHardeningRestoresTheContractAndBudget(t *testing.T) {
	custom := &agent.Definition{
		ID:           GenieAgentID,
		SystemPrompt: "You are a replacement Genie with no contract.",
	}
	hardenGenieDefinition(custom)

	if !strings.Contains(custom.SystemPrompt, "## Soulacy Agent Operating Contract") {
		t.Error("hardening must restore the shared contract to an overridden prompt")
	}
	if !strings.Contains(custom.SystemPrompt, "replacement Genie") {
		t.Error("hardening must keep the operator's own role text")
	}
	if custom.Budget == nil || custom.Budget.MaxLLMCalls != 50 {
		t.Fatalf("hardening must restore a budget matching Genie's turns, got %+v", custom.Budget)
	}

	// Hardening twice must not stack two copies of the contract.
	hardenGenieDefinition(custom)
	if n := strings.Count(custom.SystemPrompt, "## Soulacy Agent Operating Contract"); n != 1 {
		t.Fatalf("the contract should appear exactly once, found %d", n)
	}
}

// Genie was allowed 50 turns but inherited a 20-call budget, so a tool-heavy
// request died on a ceiling nobody had told it about.
func TestGenieBudgetMatchesItsTurns(t *testing.T) {
	genie := builtinGenieAgent()
	if genie.Budget == nil {
		t.Fatal("Genie must carry its own budget rather than inherit the engine default of 20 calls")
	}
	if genie.Budget.MaxLLMCalls < genie.MaxTurns {
		t.Errorf("budget allows %d model calls but the agent may take %d turns",
			genie.Budget.MaxLLMCalls, genie.MaxTurns)
	}
	// Still inside what the engine will grant, or it is silently clamped.
	if genie.Budget.MaxLLMCalls > defaultMaxBudgetCalls || genie.Budget.MaxTokens > defaultMaxBudgetTokens {
		t.Errorf("budget %+v exceeds the engine ceiling (%d calls / %d tokens)",
			genie.Budget, defaultMaxBudgetCalls, defaultMaxBudgetTokens)
	}
}

// The final answer must be told what went wrong, or it launders a failed run
// into confident prose (#235).
func TestSynthesisBriefCarriesTheFailures(t *testing.T) {
	clean := &runFailures{}
	if clean.synthesisBrief() != "" {
		t.Error("a run with nothing to report must not be given a brief")
	}
	if (*runFailures)(nil).synthesisBrief() != "" {
		t.Error("a nil ledger must be safe")
	}

	f := &runFailures{}
	f.observe([]message.ToolResult{toolErr("package_install", "error: package_install: installer failed: safety introspection returned DANGER")})
	f.observe([]message.ToolResult{toolErr("shell_exec", "error: requires the 'system' capability in the agent's SOUL.yaml")})

	brief := f.synthesisBrief()
	for _, want := range []string{
		"package_install",
		"DANGER",
		"shell_exec",
		"system capability",
		"must not be told otherwise",
		"Do not describe an action you attempted as one you completed",
		"Do not say you will do something later",
	} {
		if !strings.Contains(brief, want) {
			t.Errorf("the brief should carry %q, got:\n%s", want, brief)
		}
	}
}

// A run that produces no answer is the moment a person most needs to be told
// why. The old text was the literal string "(no final response produced)".
func TestEmptyRunMessageExplainsItself(t *testing.T) {
	def := &agent.Definition{ID: GenieAgentID, LLM: agent.LLMConfig{Model: "glm-5.3"}}
	f := &runFailures{}
	f.observe([]message.ToolResult{toolErr("shell_exec", "error: requires the 'system' capability in the agent's SOUL.yaml")})

	msg := emptyRunMessage(def, f)
	for _, want := range []string{"could not produce an answer", "shell_exec", "glm-5.3", "Nothing was changed on your behalf"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the empty-run message should carry %q, got:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "no final response produced") {
		t.Error("the old placeholder must be gone")
	}

	// Safe with nothing to say, and still not a bare placeholder.
	bare := emptyRunMessage(nil, nil)
	if bare == "" || strings.Contains(bare, "(no final") {
		t.Fatalf("a bare empty run still needs a sentence, got %q", bare)
	}
}

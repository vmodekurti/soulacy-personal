package studio

import (
	"strings"
	"testing"
	"time"

	"github.com/soulacy/soulacy/pkg/agent"
	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

var certAt = time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC)

// certifiableAgent is an agent that meets every requirement, so each test can
// break exactly one thing and assert that certification fails for that reason.
func certifiableAgent() agent.Definition {
	return agent.Definition{
		ID: "podcast", Version: "1.2.0",
		Trigger:  agent.TriggerCron,
		Channels: []string{"telegram"},
		LLM:      agent.LLMConfig{Provider: "anthropic", Model: "claude-sonnet-4-6"},
		Schedule: &agent.Schedule{Output: &agent.ScheduleOutput{Channel: "telegram", To: "-100123"}},
		Workflow: &agent.WorkflowSpec{Nodes: []sdkr.FlowNode{
			{ID: "search", Kind: "tool", Tool: "web_search"},
			{ID: "send", Kind: "tool", Tool: "channel.send"},
		}},
		Outcome: &agent.OutcomeContract{Assertions: []agent.OutcomeAssertion{
			{Target: "search", Op: OpCountGTE, Value: "3"},
			{Target: "send", Op: OpDelivered, Value: "telegram"},
		}},
		ToolSchemas: &agent.ToolSchemaSnapshot{Tools: []agent.ToolSchemaRecord{
			{Tool: "web_search", Hash: "abc123def456"},
		}},
	}
}

func certifiableInput() CertificationInput {
	return CertificationInput{
		Definition:         certifiableAgent(),
		ChannelsConfigured: map[string]bool{"telegram": true},
		ConnectedMCP:       map[string]bool{},
		SecretsSet:         map[string]bool{"llm.anthropic.api_key": true},
		RequiredSecrets:    []string{"llm.anthropic.api_key"},
		RestartTested:      true,
		LastRealRun: &RealRunEvidence{
			RunID: "run-1", Dry: false, Succeeded: true, OutcomeMet: true, Outcome: "complete",
		},
	}
}

func requirement(rec CertificationRecord, id string) CertRequirement {
	for _, r := range rec.Requirements {
		if r.ID == id {
			return r
		}
	}
	return CertRequirement{ID: id + " (absent)"}
}

func TestCertify_HappyPath(t *testing.T) {
	rec := Certify(certifiableInput(), certAt)
	if !rec.Certified {
		t.Fatalf("a complete agent should certify: %s", rec.Summary())
	}
	// The record must be an audit artefact, not just a timestamp.
	if rec.AgentVersion != "1.2.0" || rec.Model != "claude-sonnet-4-6" || rec.Provider != "anthropic" {
		t.Errorf("record must capture agent version, model and provider: %+v", rec)
	}
	if rec.ToolVersions["web_search"] != "abc123def456" {
		t.Errorf("record must capture the tool schema versions certified against: %+v", rec.ToolVersions)
	}
	if rec.RunID != "run-1" || rec.CertifiedAt == "" {
		t.Errorf("record must capture the proving run and a timestamp: %+v", rec)
	}
	if rec.BlocksScheduling() {
		t.Error("a certified agent must not block scheduling")
	}
}

func TestCertify_DryRunCanNeverCertify(t *testing.T) {
	// The rule the whole gate rests on: a mock run proves the graph is wired,
	// not that the provider answers or the message arrives.
	in := certifiableInput()
	in.LastRealRun = &RealRunEvidence{RunID: "dry-1", Dry: true, Succeeded: true, OutcomeMet: true}
	rec := Certify(in, certAt)
	if rec.Certified {
		t.Fatal("a dry run must never satisfy certification")
	}
	r := requirement(rec, "real_run")
	if r.Passed {
		t.Error("the real-run requirement must fail for a dry run")
	}
	if !strings.Contains(r.Detail, "dry run") {
		t.Errorf("the detail should name the problem: %q", r.Detail)
	}
	if r.Action != "run_live" {
		t.Errorf("a failed requirement must carry a direct action, got %q", r.Action)
	}

	// A real run that SUCCEEDED but missed its outcome contract also fails —
	// "no error" is not the standard.
	in.LastRealRun = &RealRunEvidence{RunID: "r2", Succeeded: true, OutcomeMet: false, Outcome: "empty"}
	if rec := Certify(in, certAt); rec.Certified {
		t.Error("a run that met no outcome must not certify")
	}
	// No run at all.
	in.LastRealRun = nil
	if rec := Certify(in, certAt); rec.Certified {
		t.Error("an agent with no real run must not certify")
	}
}

func TestCertify_BlocksOnEachPrecondition(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*CertificationInput)
		wantReq string
	}{
		{"contract blockers", func(in *CertificationInput) {
			in.Contract = ContractResult{Blockers: 1}
		}, "no_blockers"},
		{"preflight blockers", func(in *CertificationInput) {
			in.Preflight = PreflightResult{Blockers: []PreflightIssue{{Message: "bad"}}}
		}, "no_blockers"},
		{"missing credentials", func(in *CertificationInput) {
			in.SecretsSet = map[string]bool{}
		}, "credentials"},
		{"disconnected MCP", func(in *CertificationInput) {
			in.Definition.Workflow.Nodes = append(in.Definition.Workflow.Nodes,
				sdkr.FlowNode{ID: "nb", Kind: "tool", Tool: "mcp__notebooklm__create"})
			in.ConnectedMCP = map[string]bool{}
		}, "mcp_connected"},
		{"unconfigured channel", func(in *CertificationInput) {
			in.ChannelsConfigured = map[string]bool{}
		}, "destinations"},
		{"no destination", func(in *CertificationInput) {
			in.Definition.Schedule.Output.To = ""
		}, "destinations"},
		{"no assertions", func(in *CertificationInput) {
			in.Definition.Outcome = nil
		}, "assertions"},
		{"only weak assertions", func(in *CertificationInput) {
			in.Definition.Outcome = &agent.OutcomeContract{Assertions: []agent.OutcomeAssertion{
				{Target: "result", Op: OpExists},
			}}
		}, "assertions"},
		{"restart untested", func(in *CertificationInput) {
			in.RestartTested = false
		}, "restart_retry"},
		{"breaking tool drift", func(in *CertificationInput) {
			in.Drift = []ToolDrift{{Tool: "web_search", Nodes: []string{"search"}, Breaking: true}}
		}, "no_drift"},
	}
	for _, tc := range cases {
		in := certifiableInput()
		tc.mutate(&in)
		rec := Certify(in, certAt)
		if rec.Certified {
			t.Errorf("%s: must block certification", tc.name)
			continue
		}
		r := requirement(rec, tc.wantReq)
		if r.Passed {
			t.Errorf("%s: requirement %q should have failed", tc.name, tc.wantReq)
		}
		// P0-1: every failed requirement carries a direct repair action.
		if r.Fix == "" || r.Action == "" {
			t.Errorf("%s: failed requirement %q must carry a fix and an action: %+v", tc.name, tc.wantReq, r)
		}
		if !rec.BlocksScheduling() {
			t.Errorf("%s: an uncertified agent must block scheduling", tc.name)
		}
	}
}

func TestCertify_EmptyResultHandling(t *testing.T) {
	// `exists` passes for any non-empty output including an empty list, so it
	// cannot satisfy "an empty result would be caught".
	in := certifiableInput()
	in.Definition.Outcome = &agent.OutcomeContract{Assertions: []agent.OutcomeAssertion{
		{Target: "result", Op: OpExists},
	}}
	rec := Certify(in, certAt)
	if requirement(rec, "empty_handling").Passed {
		t.Error("an exists-only contract must not satisfy empty-result handling")
	}
	// A count assertion does.
	in.Definition.Outcome.Assertions = []agent.OutcomeAssertion{{Target: "search", Op: OpCountGTE, Value: "1"}}
	if !requirement(Certify(in, certAt), "empty_handling").Passed {
		t.Error("a count assertion must satisfy empty-result handling")
	}
}

func TestCertify_ModelCapability(t *testing.T) {
	// A fixed workflow asks nothing of the model's reasoning ability.
	if !requirement(Certify(certifiableInput(), certAt), "model_capable").Passed {
		t.Error("a fixed workflow should not fail the model requirement")
	}
	// An explicit ReAct agent on a weak model does.
	in := certifiableInput()
	in.Definition.Workflow = nil
	in.Definition.Reasoning.Strategy = "react"
	in.Definition.LLM = agent.LLMConfig{Provider: "ollama", Model: "phi3"}
	rec := Certify(in, certAt)
	r := requirement(rec, "model_capable")
	if r.Passed {
		t.Fatal("ReAct on an incapable model must fail certification")
	}
	if r.Action != "choose_model" {
		t.Errorf("the requirement should offer a model change, got %q", r.Action)
	}
}

func TestCertify_NonScheduledAgentSkipsRestartTest(t *testing.T) {
	in := certifiableInput()
	in.Definition.Trigger = agent.TriggerWebhook
	in.Definition.Schedule = nil
	in.RestartTested = false
	rec := Certify(in, certAt)
	for _, r := range rec.Requirements {
		if r.ID == "restart_retry" {
			t.Fatal("a non-scheduled agent must not be asked to pass a restart test")
		}
	}
	if !rec.Certified {
		t.Errorf("it should otherwise certify: %s", rec.Summary())
	}
}

func TestCertify_SummaryNamesWhatIsMissing(t *testing.T) {
	in := certifiableInput()
	in.Definition.Outcome = nil
	in.LastRealRun = nil
	rec := Certify(in, certAt)
	s := rec.Summary()
	if !strings.Contains(s, "assertions") || !strings.Contains(s, "real_run") {
		t.Errorf("summary should name every unmet requirement: %q", s)
	}
	if len(rec.FailedRequirements()) < 2 {
		t.Errorf("expected multiple failures: %+v", rec.FailedRequirements())
	}
}

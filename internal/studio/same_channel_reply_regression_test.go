package studio

import (
	"context"
	"strings"
	"testing"
)

func TestCompileAgent_SameChannelReplyDoesNotRequireOutboundConfirmation(t *testing.T) {
	intent := "A conversational options assistant that listens for inbound messages on the http channel and replies on that same channel."
	modelDraft := `{
  "name":"Options Trading Assistant",
  "system_prompt":"Answer conversationally. Use channel.send to deliver every reply. The run is complete only after channel.send succeeds.",
  "trigger":{"type":"channel","config":{"channel":"http"}},
  "channels":["http"],
  "tools":["channel.send","channel.status","mcp__market__quote"],
  "instructions":"Use channel.send for every answer.",
  "completion_criteria":"The reply was delivered via channel.send."
}`
	cat := Catalog{
		Tools:    []string{"channel.send", "channel.status"},
		Channels: []string{"http"},
		MCP: []CatalogMCPServer{{Server: "market", Tools: []CatalogMCPTool{
			{Name: "mcp__market__quote", Description: "Get a market quote"},
		}}},
	}

	res, err := CompileAgent(context.Background(), fakeLLM{out: modelDraft}, intent, cat, "auto", nil)
	if err != nil {
		t.Fatalf("CompileAgent: %v", err)
	}
	d := res.Workflow
	for _, tools := range [][]string{d.Tools, d.ConfirmTools} {
		if containsFold(tools, "channel.send") || containsFold(tools, "channel.status") {
			t.Fatalf("same-channel reply retained outbound/confirmation tools: tools=%v confirm=%v", d.Tools, d.ConfirmTools)
		}
	}
	if d.Output != nil {
		t.Fatalf("same-channel reply retained outbound output routing: %#v", d.Output)
	}
	if strings.Contains(strings.ToLower(d.SystemPrompt), "channel.send") {
		t.Fatalf("same-channel prompt still instructs a privileged send:\n%s", d.SystemPrompt)
	}
	if contract := ContractPrompt(d.Policy); strings.Contains(strings.ToLower(contract), "channel.send") {
		t.Fatalf("same-channel operator contract reintroduces a privileged send:\n%s", contract)
	}
	if !strings.Contains(d.SystemPrompt, "automatically routes it back through the inbound channel") {
		t.Fatalf("same-channel prompt lacks the normal-reply rule:\n%s", d.SystemPrompt)
	}
}

func TestGenerationDefaults_KeepsExplicitOutboundDelivery(t *testing.T) {
	d := Draft{
		Tools:        []string{"channel.send", "channel.status"},
		SystemPrompt: "Send the finished report to Slack.",
	}
	applyGenerationDefaults(&d, "Every morning send the finished report to Slack #alerts")
	if !containsFold(d.Tools, "channel.send") {
		t.Fatalf("scheduled outbound delivery lost channel.send: %v", d.Tools)
	}
	if !containsFold(d.ConfirmTools, "channel.send") {
		t.Fatalf("scheduled outbound delivery lost its confirmation policy: %v", d.ConfirmTools)
	}
}

func TestCompileDeterministicAgent_AlwaysFillsEditableContract(t *testing.T) {
	intent := "A conversational stock advisor that answers questions on the inbound http channel.\n\n1. TRIGGER: incoming http messages"
	cat := Catalog{
		Tools:    []string{"web_search"},
		Channels: []string{"http"},
	}
	res, ok := CompileDeterministicAgent(intent, cat, "auto", nil)
	if !ok {
		t.Fatal("deterministic agent did not compile")
	}
	policy := res.Workflow.Policy
	if policy == nil || policy.Contract == nil {
		t.Fatal("deterministic agent left the editable contract absent")
	}
	c := policy.Contract
	if c.Goal != "A conversational stock advisor that answers questions on the inbound http channel." {
		t.Fatalf("goal = %q", c.Goal)
	}
	if strings.TrimSpace(c.Instructions) == "" {
		t.Fatal("instructions were left blank")
	}
	if strings.TrimSpace(c.CompletionCriteria) == "" {
		t.Fatal("completion criteria were left blank")
	}
}

func TestGenerationDefaults_PreservesDesignerContract(t *testing.T) {
	d := Draft{
		Strategy: "auto",
		Policy: &AgentPolicy{Contract: &AgentContract{
			Goal:               "Keep this goal",
			Instructions:       "Keep these instructions",
			CompletionCriteria: "Keep this completion criterion",
		}},
	}
	applyGenerationDefaults(&d, "a different intent")
	c := d.Policy.Contract
	if c.Goal != "Keep this goal" || c.Instructions != "Keep these instructions" || c.CompletionCriteria != "Keep this completion criterion" {
		t.Fatalf("generation defaults overwrote the designer contract: %+v", c)
	}
}

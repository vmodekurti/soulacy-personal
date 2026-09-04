package studio

import (
	"context"
	"strings"
	"testing"
)

func TestCompileAgent_ProducesReActDraft(t *testing.T) {
	out := `{
	  "name": "Daily AI Podcast",
	  "system_prompt": "You are an AI news producer. Create a NotebookLM notebook, add each source, generate audio, poll status until ready, then deliver.",
	  "trigger": {"type":"schedule","config":{"cron":"0 7 * * *"}},
	  "channels": ["telegram"],
	  "tools": ["web_search","mcp__notebooklm__create","mcp__notebooklm__audio"],
	  "skills": [],
	  "knowledge": [],
	  "rationale": "Async polling + per-item loop need a reasoning loop."
	}`
	res, err := CompileAgent(context.Background(), fakeLLM{out: out}, "daily ai podcast to telegram", Catalog{}, "react", nil)
	if err != nil {
		t.Fatalf("CompileAgent: %v", err)
	}
	d := res.Workflow
	if !d.IsAgent() || d.Strategy != "react" {
		t.Errorf("expected react agent, got strategy %q", d.Strategy)
	}
	if len(d.Flow.Nodes) != 0 {
		t.Errorf("agent draft must have NO flow nodes, got %d", len(d.Flow.Nodes))
	}
	if len(d.Tools) != 3 {
		t.Errorf("tools allowlist: %+v", d.Tools)
	}
}

func TestCompileAgent_PreservesAutoStrategy(t *testing.T) {
	out := `{
	  "name": "Weather Assistant",
	  "system_prompt": "Answer weather questions by selecting the right available weather tool and returning practical guidance.",
	  "trigger": {"type":"channel"},
	  "channels": ["http"],
	  "tools": ["mcp__weather__get_forecast"],
	  "skills": [],
	  "knowledge": [],
	  "rationale": "Ordinary runtime tool selection fits auto mode."
	}`
	res, err := CompileAgent(context.Background(), fakeLLM{out: out}, "interactive weather assistant", Catalog{
		Tools: []string{"mcp__weather__get_forecast"},
	}, "auto", nil)
	if err != nil {
		t.Fatalf("CompileAgent: %v", err)
	}
	if res.Workflow.Strategy != "auto" {
		t.Fatalf("strategy = %q, want auto", res.Workflow.Strategy)
	}
	if res.Workflow.Recommendation == nil || res.Workflow.Recommendation.Mode != "auto" {
		t.Fatalf("recommendation = %+v, want auto", res.Workflow.Recommendation)
	}
	if len(res.Notes) == 0 || !strings.Contains(res.Notes[0], "Auto tool-calling") {
		t.Fatalf("auto agent should be labelled as Auto tool-calling, notes=%v", res.Notes)
	}
}

func TestBuildAgentPromptIncludesChannelSendContract(t *testing.T) {
	p := BuildAgentPrompt("capture telegram URLs and acknowledge", Catalog{
		Tools: []string{"channel.send", "channel.status", "queue_put"},
	}, "auto", nil)
	for _, want := range []string{
		`"text":"message text"`,
		"The field is `text`, not `message`",
		"call channel.status once",
		"do not call channel.send just to answer the user",
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("agent prompt missing %q:\n%s", want, p)
		}
	}
}

func TestBuildAgentPromptIncludesStrategySpecificContracts(t *testing.T) {
	react := BuildAgentPrompt("research stocks with tools", Catalog{Tools: []string{"web_search"}}, "react", nil)
	for _, want := range []string{
		"observe-decide-act cycle",
		"call exactly one tool",
		"never retry the same failed tool call with identical arguments",
	} {
		if !strings.Contains(react, want) {
			t.Fatalf("react prompt missing %q:\n%s", want, react)
		}
	}

	plan := BuildAgentPrompt("create notebook podcast and poll until ready", Catalog{Tools: []string{"web_search"}}, "plan_execute", nil)
	for _, want := range []string{
		"compact numbered plan",
		"success criteria",
		"revise the plan at most once",
	} {
		if !strings.Contains(plan, want) {
			t.Fatalf("plan-execute prompt missing %q:\n%s", want, plan)
		}
	}

	auto := BuildAgentPrompt("answer weather questions", Catalog{Tools: []string{"web_search"}}, "auto", nil)
	for _, want := range []string{
		"native tool-calling ability",
		"Do not ask the model to emit Thought/Action JSON",
	} {
		if !strings.Contains(auto, want) {
			t.Fatalf("auto prompt missing %q:\n%s", want, auto)
		}
	}
}

// End-to-end grounding through CompileAgent: a near-miss skill the model named is
// corrected to the installed one, an installed skill the intent clearly references
// but the model omitted is injected, and a named-but-uninstalled skill surfaces as
// a "Needs setup" suggestion rather than vanishing.
func TestCompileAgent_GroundsSkillsEndToEnd(t *testing.T) {
	out := `{
	  "name": "Finance QA",
	  "system_prompt": "Answer questions about stocks using the right finance skill.",
	  "trigger": {"type":"channel"},
	  "channels": ["http"],
	  "tools": ["web_search"],
	  "skills": ["yahoo finance", "totally-made-up-skill"],
	  "knowledge": [],
	  "rationale": "Dynamic skill routing."
	}`
	cat := Catalog{Skills: []CatalogSkill{
		{Name: "yfinance", Description: "Yahoo Finance market data: stock quotes, history"},
		{Name: "market-news", Description: "Latest market news headlines"},
	}}
	res, err := CompileAgent(context.Background(),
		fakeLLM{out: out},
		"on-demand assistant that answers stock questions and the latest market-news",
		cat, "react", nil)
	if err != nil {
		t.Fatalf("CompileAgent: %v", err)
	}
	has := func(list []string, want string) bool {
		for _, s := range list {
			if s == want {
				return true
			}
		}
		return false
	}
	if !has(res.Workflow.Skills, "yfinance") {
		t.Errorf("near-miss 'yahoo finance' should be corrected to installed 'yfinance'; got %v", res.Workflow.Skills)
	}
	if !has(res.Workflow.Skills, "market-news") {
		t.Errorf("'market-news' referenced in intent should be injected; got %v", res.Workflow.Skills)
	}
	if has(res.Workflow.Skills, "totally-made-up-skill") {
		t.Errorf("an uninstalled skill must not be kept on the agent; got %v", res.Workflow.Skills)
	}
	var flaggedMissing bool
	for _, sg := range res.Suggestions {
		if sg.Kind == "skill" && sg.Name == "totally-made-up-skill" && !sg.Installed {
			flaggedMissing = true
		}
	}
	if !flaggedMissing {
		t.Errorf("uninstalled named skill should surface as a Needs-setup suggestion; got %+v", res.Suggestions)
	}
}

func TestCompileAgent_AddsCompanionTools(t *testing.T) {
	out := `{
	  "name": "Research Librarian",
	  "system_prompt": "Capture URLs into a queue, process them into the KB, verify storage, and notify the user.",
	  "trigger": {"type":"channel"},
	  "channels": ["telegram"],
	  "tools": ["queue_put","kb_write","channel.send"],
	  "skills": [],
	  "knowledge": ["AI Docs"],
	  "rationale": "Dynamic capture and notification fits a reasoning agent."
	}`
	cat := Catalog{Tools: []string{
		"queue_put", "queue_create", "queue_names",
		"kb_write", "kb_search",
		"channel.send", "channel.status",
	}}
	res, err := CompileAgent(context.Background(), fakeLLM{out: out}, "capture URLs, store in AI Docs, and notify telegram", cat, "react", nil)
	if err != nil {
		t.Fatalf("CompileAgent: %v", err)
	}
	has := func(want string) bool {
		for _, t := range res.Workflow.Tools {
			if t == want {
				return true
			}
		}
		return false
	}
	for _, want := range []string{"queue_put", "queue_create", "queue_names", "kb_write", "kb_search", "channel.send", "channel.status"} {
		if !has(want) {
			t.Fatalf("compiled agent missing companion or original tool %q; tools=%v notes=%v", want, res.Workflow.Tools, res.Notes)
		}
	}
}

func TestToAgentDefinition_ReActHasNoWorkflow(t *testing.T) {
	d := Draft{
		Name:         "Daily AI Podcast",
		Strategy:     "react",
		SystemPrompt: "You produce a podcast.",
		Trigger:      Trigger{Type: "schedule", Config: map[string]any{"cron": "0 7 * * *"}},
		Channels:     []string{"telegram"},
		Tools:        []string{"web_search", "mcp__notebooklm__create"},
	}
	def, err := ToAgentDefinition(d, false)
	if err != nil {
		t.Fatalf("ToAgentDefinition: %v", err)
	}
	if def.Workflow != nil {
		t.Error("ReAct agent must NOT have a workflow block (would override strategy)")
	}
	if def.Reasoning.Strategy != "react" {
		t.Errorf("strategy not set: %q", def.Reasoning.Strategy)
	}
	if def.Builtins == nil || len(*def.Builtins) != 1 || (*def.Builtins)[0] != "web_search" {
		t.Errorf("builtins: %+v", def.Builtins)
	}
	if def.MCPTools == nil || len(*def.MCPTools) != 1 {
		t.Errorf("mcp tools: %+v", def.MCPTools)
	}
	if !strings.Contains(def.SystemPrompt, "Reasoning Strategy Contract") || !strings.Contains(def.SystemPrompt, "Call exactly one tool") {
		t.Errorf("system prompt should carry a loop directive: %q", def.SystemPrompt)
	}
}

func TestPreflight_ReActDisconnectedMCPBlocks(t *testing.T) {
	d := Draft{Strategy: "react", SystemPrompt: "x", Tools: []string{"mcp__notebooklm__create"}}
	r := Preflight(d, PreflightInput{ConnectedMCP: map[string]bool{}})
	found := false
	for _, b := range r.Blockers {
		if b.Kind == "mcp" {
			found = true
		}
	}
	if !found {
		t.Errorf("react agent with disconnected MCP tool should block: %+v", r.Blockers)
	}
}

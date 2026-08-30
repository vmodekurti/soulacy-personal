package studio

import (
	"reflect"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/pkg/agent"
	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// ToAgentDefinition → FromAgentDefinition must round-trip the fields Studio owns
// (name, trigger+cron, channels, flow graph), so a saved agent re-opens on the
// canvas exactly as authored.
func TestAgentDefinitionRoundTrip(t *testing.T) {
	orig := Draft{
		Name:         "My Flow",
		DeliveryMode: "outbound",
		Connections:  []string{"conn_personal", "conn_workspace"},
		Trigger:      Trigger{Type: "schedule", Config: map[string]any{"cron": "0 7 * * *"}},
		Channels:     []string{"slack", "email"},
		Output: &ScheduleOutput{
			Channel:  "slack",
			To:       "C123",
			BotName:  "Ops Bot",
			Template: "[{agent_id}] {reply}",
		},
		Flow: Flow{
			Entry: "a",
			Nodes: []sdkr.FlowNode{
				{ID: "a", Kind: sdkr.FlowNodeTool, Tool: "web_search", Output: "r"},
				{ID: "b", Kind: sdkr.FlowNodePython, Code: "def run(i):\n    return i"},
			},
			Edges: []sdkr.FlowEdge{{From: "a", To: "b"}, {From: "b", To: "end"}},
		},
	}
	def, err := ToAgentDefinition(orig, false)
	if err != nil {
		t.Fatalf("ToAgentDefinition: %v", err)
	}
	if !HasWorkflow(def) {
		t.Fatal("saved agent should report HasWorkflow")
	}
	back := FromAgentDefinition(def)

	if back.Name != orig.Name {
		t.Fatalf("name: %q != %q", back.Name, orig.Name)
	}
	if back.Trigger.Type != "schedule" || back.Trigger.Config["cron"] != "0 7 * * *" {
		t.Fatalf("trigger not preserved: %+v", back.Trigger)
	}
	if !reflect.DeepEqual(back.Channels, orig.Channels) {
		t.Fatalf("channels: %v != %v", back.Channels, orig.Channels)
	}
	if !reflect.DeepEqual(back.Connections, orig.Connections) {
		t.Fatalf("connections: %v != %v", back.Connections, orig.Connections)
	}
	if back.DeliveryMode != orig.DeliveryMode {
		t.Fatalf("delivery mode: %q != %q", back.DeliveryMode, orig.DeliveryMode)
	}
	if back.Output == nil || back.Output.Channel != "slack" || back.Output.To != "C123" || back.Output.BotName != "Ops Bot" {
		t.Fatalf("output not preserved: %+v", back.Output)
	}
	if back.Flow.Entry != "a" || len(back.Flow.Nodes) != 2 || len(back.Flow.Edges) != 2 {
		t.Fatalf("flow not preserved: %+v", back.Flow)
	}
	if back.Flow.Nodes[1].Code != "def run(i):\n    return i" {
		t.Fatalf("python code not preserved: %q", back.Flow.Nodes[1].Code)
	}
}

func TestAgentDefinitionRoundTrip_PreservesGUIChatOnlyMode(t *testing.T) {
	orig := Draft{
		Name:         "GUI Chat Assistant",
		Strategy:     "auto",
		Intent:       "Answer questions only in Soulacy GUI Chat",
		SystemPrompt: "Reply conversationally in GUI Chat.",
		Trigger:      Trigger{Type: "chat"},
		DeliveryMode: "reply",
		Tools:        []string{"web_search"},
	}
	def, err := ToAgentDefinition(orig, false)
	if err != nil {
		t.Fatalf("ToAgentDefinition: %v", err)
	}
	if def.Trigger != agent.TriggerInternal {
		t.Fatalf("GUI Chat must use the safe internal runtime trigger, got %q", def.Trigger)
	}
	if !reflect.DeepEqual(def.Surfaces, []string{agent.SurfaceChat}) {
		t.Fatalf("GUI Chat-only surfaces = %#v", def.Surfaces)
	}
	if def.StudioTriggerMode != "chat" || def.StudioDeliveryMode != "reply" {
		t.Fatalf("Studio modes were not persisted: trigger=%q delivery=%q", def.StudioTriggerMode, def.StudioDeliveryMode)
	}
	back := FromAgentDefinition(def)
	if back.Trigger.Type != "chat" || back.DeliveryMode != "reply" {
		t.Fatalf("Studio modes did not round-trip: %+v", back)
	}
}

// Regression for the Agents-screen bug: a provider/model chosen for an agent
// must survive a Studio load+save round-trip. Before the fix, FromAgentDefinition
// dropped def.LLM and ToAgentDefinition re-emitted a hard-coded default, so any
// model set on the Agents screen (or directly in SOUL.yaml) was silently reset
// the next time the agent passed through Studio.
func TestAgentDefinitionRoundTrip_PreservesLLM(t *testing.T) {
	// ReAct agent form (the daily-stock-screener case).
	orig := agent.Definition{
		ID:         "daily-stock-screener",
		Name:       "Daily Stock Screener",
		Reasoning:  agent.ReasoningConfig{Strategy: "react"},
		RunTimeout: "20m",
		LLM: agent.LLMConfig{
			Provider:    "ollama",
			Model:       "qwen3:32b",
			Temperature: 0.4,
			MaxTokens:   2048,
		},
	}
	draft := FromAgentDefinition(orig)
	if draft.LLM.Provider != "ollama" || draft.LLM.Model != "qwen3:32b" {
		t.Fatalf("FromAgentDefinition dropped LLM: %+v", draft.LLM)
	}
	if draft.ID != "daily-stock-screener" {
		t.Fatalf("FromAgentDefinition dropped ID: %q", draft.ID)
	}
	if draft.RunTimeout != "20m" {
		t.Fatalf("FromAgentDefinition dropped run_timeout: %q", draft.RunTimeout)
	}
	back, err := ToAgentDefinition(draft, false)
	if err != nil {
		t.Fatalf("ToAgentDefinition: %v", err)
	}
	if back.ID != "daily-stock-screener" {
		t.Fatalf("id not preserved: %q", back.ID)
	}
	if back.RunTimeout != "20m" {
		t.Fatalf("run_timeout not preserved: %q", back.RunTimeout)
	}
	if back.LLM.Provider != "ollama" {
		t.Fatalf("provider not preserved: %q", back.LLM.Provider)
	}
	if back.LLM.Model != "qwen3:32b" {
		t.Fatalf("model not preserved: %q", back.LLM.Model)
	}
	if back.LLM.Temperature != 0.4 {
		t.Fatalf("temperature not preserved: %v", back.LLM.Temperature)
	}
	if back.LLM.MaxTokens != 2048 {
		t.Fatalf("max_tokens not preserved: %d", back.LLM.MaxTokens)
	}

	// Rename must NOT spawn a new agent: editing the name keeps the original id,
	// so Save updates in place instead of creating a duplicate.
	renamed := draft
	renamed.Name = "Daily Stock Screener (v2)"
	renamedBack, err := ToAgentDefinition(renamed, false)
	if err != nil {
		t.Fatalf("ToAgentDefinition(renamed): %v", err)
	}
	if renamedBack.ID != "daily-stock-screener" {
		t.Fatalf("rename changed id (would create a duplicate agent): %q", renamedBack.ID)
	}

	// A brand-new draft with no id still derives one from the name.
	newish, err := ToAgentDefinition(Draft{Name: "Fresh Agent", Strategy: "react"}, false)
	if err != nil {
		t.Fatalf("ToAgentDefinition(new): %v", err)
	}
	if newish.ID != "fresh-agent" {
		t.Fatalf("new draft id: got %q, want fresh-agent", newish.ID)
	}

	// Workflow agent form must preserve LLM too.
	wf := agent.Definition{
		ID:   "wf",
		Name: "WF",
		Workflow: &agent.WorkflowSpec{
			Entry: "a",
			Nodes: []sdkr.FlowNode{{ID: "a", Kind: sdkr.FlowNodeTool, Tool: "web_search", Output: "r"}},
		},
		LLM: agent.LLMConfig{Provider: "anthropic", Model: "claude-sonnet-4-6", Temperature: 0.7},
	}
	wfBack, err := ToAgentDefinition(FromAgentDefinition(wf), false)
	if err != nil {
		t.Fatalf("ToAgentDefinition(workflow): %v", err)
	}
	if wfBack.LLM.Provider != "anthropic" || wfBack.LLM.Model != "claude-sonnet-4-6" {
		t.Fatalf("workflow LLM not preserved: %+v", wfBack.LLM)
	}

	// A freshly generated draft (no LLM set) keeps the historic 0.7 default so
	// behaviour is unchanged for brand-new agents.
	fresh, err := ToAgentDefinition(Draft{Name: "New", Strategy: "react"}, false)
	if err != nil {
		t.Fatalf("ToAgentDefinition(fresh): %v", err)
	}
	if fresh.LLM.Temperature != 0.7 {
		t.Fatalf("fresh draft should default temperature to 0.7, got %v", fresh.LLM.Temperature)
	}
}

func TestAgentDefinitionRoundTrip_PreservesGeneratedGuardrails(t *testing.T) {
	orig := agent.Definition{
		ID:           "podcast",
		Name:         "Podcast",
		ConfirmTools: []string{"channel.send", "mcp__notebooklm__studio_create"},
		Security:     &agent.SecurityConfig{IntentGate: "deny"},
		Workflow: &agent.WorkflowSpec{
			Entry: "send",
			Nodes: []sdkr.FlowNode{{ID: "send", Kind: sdkr.FlowNodeTool, Tool: "channel.send", Output: "sent"}},
		},
	}
	draft := FromAgentDefinition(orig)
	if !confirmToolsContain(draft.ConfirmTools, "channel.send") || !confirmToolsContain(draft.ConfirmTools, "mcp__notebooklm__studio_create") {
		t.Fatalf("FromAgentDefinition dropped confirm_tools: %#v", draft.ConfirmTools)
	}
	if draft.Security == nil || draft.Security.IntentGate != "deny" {
		t.Fatalf("FromAgentDefinition dropped security: %#v", draft.Security)
	}
	back, err := ToAgentDefinition(draft, false)
	if err != nil {
		t.Fatalf("ToAgentDefinition: %v", err)
	}
	if !confirmToolsContain(back.ConfirmTools, "channel.send") || !confirmToolsContain(back.ConfirmTools, "mcp__notebooklm__studio_create") {
		t.Fatalf("ToAgentDefinition dropped confirm_tools: %#v", back.ConfirmTools)
	}
	if back.Security == nil || back.Security.IntentGate != "deny" {
		t.Fatalf("ToAgentDefinition dropped security: %#v", back.Security)
	}
}

// Re-saving a reasoning agent must NOT stack duplicate copies of the ReAct loop
// guidance: the prompt round-trips through draft.SystemPrompt, so the guidance is
// already present and must be appended at most once (regression for the
// "instructions repeat after every save" bug).
func TestReactSystemPrompt_IdempotentGuidance(t *testing.T) {
	draft := Draft{Name: "Screener", Strategy: "react", SystemPrompt: "You are the screener."}

	first := reactSystemPrompt(draft)
	if n := strings.Count(first, reactLoopGuidance); n != 1 {
		t.Fatalf("first build should contain guidance once, got %d", n)
	}

	// Feed the built prompt back in (what FromAgentDefinition does on reopen) and
	// rebuild repeatedly — the count must stay at 1.
	for i := 0; i < 5; i++ {
		draft.SystemPrompt = reactSystemPrompt(draft)
	}
	if n := strings.Count(draft.SystemPrompt, reactLoopGuidance); n != 1 {
		t.Fatalf("guidance duplicated across re-saves: got %d copies", n)
	}

	// A prompt that ALREADY accumulated duplicates self-heals down to one.
	dupes := "You are X.\n\n" + reactLoopGuidance + "\n\n" + reactLoopGuidance + "\n\n" + reactLoopGuidance
	healed := reactSystemPrompt(Draft{Name: "X", Strategy: "react", SystemPrompt: dupes})
	if n := strings.Count(healed, reactLoopGuidance); n != 1 {
		t.Fatalf("self-heal failed: got %d copies", n)
	}
}

func TestReactSystemPrompt_PreservesExactRequestAndScopesAgent(t *testing.T) {
	draft := Draft{
		Name:         "HBR Curator",
		Strategy:     "auto",
		SystemPrompt: "You curate useful articles.",
		RawIntent:    "Read my HBR subscription and curate the most relevant AI articles.",
		Intent:       "Build a conversational research curator using an authenticated HBR session.",
	}

	got := reactSystemPrompt(draft)
	for _, want := range []string{
		requestedOutcomeHeading,
		draft.RawIntent,
		draft.Intent,
		"outside this agent's scope",
		"do not answer the unrelated question or call tools for it",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("system prompt missing %q:\n%s", want, got)
		}
	}

	// A changed request replaces the framework-owned mission section rather than
	// retaining the old request or stacking another section.
	draft.SystemPrompt = got
	draft.RawIntent = "Read my MIT Technology Review subscription for robotics news."
	updated := reactSystemPrompt(draft)
	if strings.Contains(updated, "most relevant AI articles") {
		t.Fatalf("old requested outcome survived update:\n%s", updated)
	}
	if strings.Count(updated, requestedOutcomeHeading) != 1 {
		t.Fatalf("requested outcome section duplicated:\n%s", updated)
	}
}

func TestReasoningSystemPrompt_UsesStrategySpecificGuidance(t *testing.T) {
	plan := reactSystemPrompt(Draft{Name: "Podcast", Strategy: "plan_execute", SystemPrompt: "You create podcasts."})
	if !strings.Contains(plan, planExecuteLoopGuidance) {
		t.Fatalf("plan-execute prompt missing plan guidance:\n%s", plan)
	}
	if strings.Contains(plan, reactLoopGuidance) {
		t.Fatalf("plan-execute prompt should not carry react guidance:\n%s", plan)
	}

	auto := reactSystemPrompt(Draft{Name: "Weather", Strategy: "auto", SystemPrompt: "You answer weather."})
	if !strings.Contains(auto, autoToolCallingGuidance) {
		t.Fatalf("auto prompt missing native tool guidance:\n%s", auto)
	}
	if strings.Contains(auto, "Thought/Action JSON") && !strings.Contains(auto, "Do not emit Thought/Action JSON") {
		t.Fatalf("auto prompt should reject visible reasoning protocols:\n%s", auto)
	}

	legacy := reactSystemPrompt(Draft{Name: "Old", Strategy: "react", SystemPrompt: "You are old.\n\n" + legacyReactLoopGuidance})
	if strings.Contains(legacy, legacyReactLoopGuidance) {
		t.Fatalf("legacy guidance should be replaced:\n%s", legacy)
	}
	if !strings.Contains(legacy, reactLoopGuidance) {
		t.Fatalf("legacy guidance should be upgraded to new react guidance:\n%s", legacy)
	}
}

// "auto" is the recommended default strategy: a tool agent (no workflow graph)
// whose execution mode the engine resolves at run time. It must be treated as an
// agent — not silently converted into an (empty) workflow — through save+load.
func TestAutoStrategy_IsToolAgentRoundTrip(t *testing.T) {
	orig := Draft{
		Name:         "Flight Finder",
		Strategy:     "auto",
		SystemPrompt: "You help find flights.",
		Trigger:      Trigger{Type: "channel"},
		Channels:     []string{"chat"},
		Tools:        []string{"mcp__letsfg__search_flights"},
	}
	if !orig.IsAgent() {
		t.Fatal("an 'auto' draft must report IsAgent()")
	}
	def, err := ToAgentDefinition(orig, false)
	if err != nil {
		t.Fatalf("ToAgentDefinition: %v", err)
	}
	if HasWorkflow(def) {
		t.Fatal("'auto' agent must NOT carry a workflow graph")
	}
	if strings.ToLower(def.Reasoning.Strategy) != "auto" {
		t.Fatalf("strategy not preserved: %q", def.Reasoning.Strategy)
	}
	back := FromAgentDefinition(def)
	if !back.IsAgent() || strings.ToLower(back.Strategy) != "auto" {
		t.Fatalf("auto strategy lost on reload: %q (IsAgent=%v)", back.Strategy, back.IsAgent())
	}
}

// A ReAct/Plan-Execute agent must survive the round-trip as an AGENT — keeping
// its strategy, prompt, tools, skills, knowledge and unattended flag — so that
// switching to Canvas or re-opening it never silently turns it into a workflow
// (which would add a stray `workflow:` block on save and mislabel the toggle).
func TestAgentDefinitionRoundTrip_PreservesReasoningStrategy(t *testing.T) {
	orig := Draft{
		Name:         "Finance Assistant",
		Strategy:     "react",
		SystemPrompt: "You are a finance assistant. Reason over the skills.",
		Trigger:      Trigger{Type: "channel"},
		Channels:     []string{"chat"},
		Tools:        []string{"web_search", "mcp__finance__quote"},
		Skills:       []string{"stock_performance", "market_news"},
		Knowledge:    []string{"sec_filings"},
		Unattended:   true,
	}
	def, err := ToAgentDefinition(orig, false)
	if err != nil {
		t.Fatalf("ToAgentDefinition: %v", err)
	}
	if HasWorkflow(def) {
		t.Fatal("a reasoning agent must NOT carry a workflow graph")
	}
	back := FromAgentDefinition(def)

	if !back.IsAgent() || back.Strategy != "react" {
		t.Fatalf("strategy not preserved: %q (IsAgent=%v)", back.Strategy, back.IsAgent())
	}
	if back.SystemPrompt == "" {
		t.Error("system prompt not preserved")
	}
	if !back.Unattended {
		t.Error("unattended flag not preserved")
	}
	if !reflect.DeepEqual(back.Tools, []string{"web_search", "generate_chart", "mcp__finance__quote"}) {
		t.Errorf("tools not preserved (builtin+mcp split should reassemble): %v", back.Tools)
	}
	if !reflect.DeepEqual(back.Skills, orig.Skills) {
		t.Errorf("skills not preserved: %v", back.Skills)
	}
	if !reflect.DeepEqual(back.Knowledge, orig.Knowledge) {
		t.Errorf("knowledge not preserved: %v", back.Knowledge)
	}
	// And it must NOT have been given a flow graph.
	if len(back.Flow.Nodes) != 0 {
		t.Errorf("an agent round-trip must not synthesize flow nodes: %+v", back.Flow)
	}
}

// A reasoning agent's tuned reasoning budgets and max_turns must survive the
// round-trip (regression for the "canvas re-save resets timeouts" bug).
func TestAgentRoundTrip_PreservesReasoningBudgets(t *testing.T) {
	orig := Draft{
		Name: "Tuned", Strategy: "react", SystemPrompt: "p",
		Trigger: Trigger{Type: "channel"}, Channels: []string{"chat"},
		Tools:        []string{"web_search"},
		StepTimeout:  "45s",
		TotalTimeout: "300s",
		MaxTurns:     25,
	}
	def, err := ToAgentDefinition(orig, false)
	if err != nil {
		t.Fatalf("ToAgentDefinition: %v", err)
	}
	if def.Reasoning.StepTimeout != "45s" || def.Reasoning.TotalTimeout != "300s" || def.MaxTurns != 25 {
		t.Fatalf("save dropped tuned budgets: step=%q total=%q turns=%d", def.Reasoning.StepTimeout, def.Reasoning.TotalTimeout, def.MaxTurns)
	}
	back := FromAgentDefinition(def)
	if back.StepTimeout != "45s" || back.TotalTimeout != "300s" || back.MaxTurns != 25 {
		t.Fatalf("load dropped tuned budgets: %+v", back)
	}
	// Re-save must NOT reset them to defaults.
	def2, _ := ToAgentDefinition(back, false)
	if def2.Reasoning.StepTimeout != "45s" || def2.Reasoning.TotalTimeout != "300s" || def2.MaxTurns != 25 {
		t.Fatalf("re-save reset budgets: step=%q total=%q turns=%d", def2.Reasoning.StepTimeout, def2.Reasoning.TotalTimeout, def2.MaxTurns)
	}
}

// A WORKFLOW agent's canvas-owned fields (knowledge, unattended) must survive
// open-in-canvas → re-save (regression for the silent-wipe bug).
func TestWorkflowRoundTrip_PreservesKnowledgeAndUnattended(t *testing.T) {
	orig := Draft{
		Name: "Flow", Trigger: Trigger{Type: "manual"},
		Knowledge:  []string{"sec_filings"},
		Unattended: true,
		Flow: Flow{Entry: "a", Nodes: []sdkr.FlowNode{
			{ID: "a", Kind: sdkr.FlowNodeTool, Tool: "web_search", Output: "r"},
		}},
	}
	def, err := ToAgentDefinition(orig, false)
	if err != nil {
		t.Fatalf("ToAgentDefinition: %v", err)
	}
	back := FromAgentDefinition(def)
	if len(back.Knowledge) != 1 || back.Knowledge[0] != "sec_filings" {
		t.Errorf("workflow round-trip dropped knowledge: %v", back.Knowledge)
	}
	if !back.Unattended {
		t.Errorf("workflow round-trip dropped unattended flag")
	}
}

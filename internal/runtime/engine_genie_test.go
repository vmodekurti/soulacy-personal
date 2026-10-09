package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
	"github.com/soulacy/soulacy/pkg/skill"
)

func TestGenieToolSurfaceIsDynamicAndNonPrivileged(t *testing.T) {
	e := newMinimalEngine(t)
	e.skillLoader = populatedSkillLoader{skills: []*skill.Skill{{Name: "web-audit", Description: "Audit a site"}}}
	e.builtins = e.buildBuiltins()
	genie := e.loader.Get(GenieAgentID)
	names := toolSchemaNameSet(e.allToolSchemasForContext(WithPrincipal(context.Background(), Principal{Role: "admin"}), genie, "http"))
	for _, want := range []string{"list_skills", "read_skill", "list_mcp_tools", "list_agents", "plan_action", "prepare_website_access", "start_website_action", "inspect_website_action", "act_on_website", "commit_website_action", "close_website_action", "plan_mission", "create_mission", "list_missions", "get_mission", "update_mission", "cancel_mission", "create_monitor", "list_monitors", "pause_monitor", "cancel_monitor"} {
		if !names[want] {
			t.Errorf("Genie missing %s", want)
		}
	}
	for _, forbidden := range []string{"shell_exec", "write_file", "package_install", "http_request", "env_get"} {
		if names[forbidden] {
			t.Errorf("Genie exposed privileged/host tool %s", forbidden)
		}
	}

	skillsOut, err := builtinByName(t, e.builtins, "list_skills").Handler(context.Background(), nil)
	if err != nil || !strings.Contains(skillsOut, "web-audit") {
		t.Fatalf("live skills = %q, %v", skillsOut, err)
	}
	e.loader.Register(&agent.Definition{ID: "researcher", Name: "Researcher", Enabled: true})
	agentsOut, err := builtinByName(t, e.builtins, "list_agents").Handler(context.Background(), nil)
	if err != nil || !strings.Contains(agentsOut, "researcher") {
		t.Fatalf("live agents = %q, %v", agentsOut, err)
	}
}

func TestGenieManagedBrowserCommitAlwaysRequiresApproval(t *testing.T) {
	genie := builtinGenieAgent()
	if !containsExactString(genie.ConfirmTools, "commit_website_action") {
		t.Fatal("managed browser final submission is not in Genie's confirmation gate")
	}
	for _, tool := range []string{"start_website_action", "inspect_website_action", "act_on_website", "close_website_action"} {
		if containsExactString(genie.ConfirmTools, tool) {
			t.Fatalf("ordinary preparation tool %q should not require final-action approval", tool)
		}
	}
}

func TestGenieExternalWriteClassifier(t *testing.T) {
	for _, name := range []string{"mcp__bank__transfer_funds", "mcp__db__delete_row", "plugin__mail__send_message", "mcp__uber__request_ride", "mcp__resy__book_table"} {
		if !highImpactExternalTool(name) {
			t.Errorf("write tool %q was not classified high-impact", name)
		}
	}
	for _, name := range []string{"mcp__bank__get_balance", "mcp__web__search", "plugin__docs__list_files", "shell_exec"} {
		if highImpactExternalTool(name) {
			t.Errorf("read/non-external tool %q was classified high-impact", name)
		}
	}
}

func TestGenieBrowserConfirmationClassifier(t *testing.T) {
	for _, call := range []message.ToolCall{
		{Name: "mcp__playwright__browser_click", Arguments: map[string]any{"element": "Confirm reservation"}},
		{Name: "mcp__browser__browser_submit", Arguments: map[string]any{"label": "Request ride"}},
		{Name: "mcp__browser__browser_click", Arguments: map[string]any{"label": "Place your order"}},
	} {
		if !highImpactExternalCall(call) {
			t.Errorf("browser action %#v was not classified high-impact", call)
		}
	}

	call := message.ToolCall{Name: "mcp__playwright__browser_click", Arguments: map[string]any{"element": "Next page"}}
	if highImpactExternalCall(call) {
		t.Errorf("navigation action %#v was classified high-impact", call)
	}
}

func TestGeniePromptTreatsConnectorsAsOptionalFallbacks(t *testing.T) {
	prompt := builtinGenieAgent().SystemPrompt
	for _, want := range []string{
		"A connector is an optimization, never a prerequisite.",
		"Maintain a current map of your surroundings",
		"repair any reversible setup that your tools allow",
		"provider's official website second",
		"Never refuse merely because a named connector is absent",
		"ask only the first two related missing details",
		"do not mention connectors, MCP, browser automation",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("Genie prompt missing %q", want)
		}
	}
}

func TestGenieActionPlanningIsForcedWhenModelCouldSkipIt(t *testing.T) {
	tools := []llm.ToolSchema{{Name: "web_search"}, {Name: "plan_action"}}
	if !shouldForceGenieActionPlan(GenieAgentID, "Book an Uber from the airport to my home", tools) {
		t.Fatal("Genie ride request did not require action planning")
	}
	if shouldForceGenieActionPlan(GenieAgentID, "Research airport transfer options", tools) {
		t.Fatal("research request should not force action planning")
	}
	if shouldForceGenieActionPlan("another-agent", "Book an Uber", tools) {
		t.Fatal("action planning override must be limited to Genie")
	}
	if shouldForceGenieActionPlan(GenieAgentID, "Book an Uber", []llm.ToolSchema{{Name: "web_search"}}) {
		t.Fatal("action planning cannot be forced when the tool is unavailable")
	}
	call := genieActionPlanCall("Book an Uber")
	if call.Name != "plan_action" || call.Arguments["goal"] != "Book an Uber" {
		t.Fatalf("forced call=%+v", call)
	}
	results := []message.ToolResult{{Name: "plan_action", Content: `{"suggested_reply":"I can use Uber's website.\n\n1. Which airport?"}`}}
	if got := geniePlannedQuestionReply(results); got != "I can use Uber's website.\n\n1. Which airport?" {
		t.Fatalf("planned reply=%q", got)
	}
}

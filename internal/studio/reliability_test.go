package studio

import (
	"strings"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func TestValidateBlocksSearchOnlyWhenIntentNeedsFinishedArtifact(t *testing.T) {
	d := Draft{
		Intent:   "Find recent AI articles and create a podcast briefing from them",
		Trigger:  Trigger{Type: "manual"},
		Channels: []string{"telegram"},
		Flow: Flow{
			Nodes: []sdkr.FlowNode{
				{ID: "search_articles", Kind: "tool", Tool: "web_search", Input: `{"query":"AI articles"}`, Output: "results"},
			},
			Entry:  "search_articles",
			Output: "search_articles",
		},
	}
	got := Validate(d)
	if got.Ok {
		t.Fatalf("search-only graph should be blocked for finished-artifact intent: %+v", got)
	}
	if !validateHasError(got, "discovery/search") {
		t.Fatalf("expected completion-contract error, got %+v", got.Errors)
	}
}

func TestReactSystemPromptIncludesCompletionContractForMultiStepAgent(t *testing.T) {
	d := Draft{
		Name:         "Research Librarian",
		Strategy:     "auto",
		Intent:       "Search URLs, store tagged summaries in the Knowledge base, and send a Slack notification",
		SystemPrompt: "You are a careful research librarian who processes sources.",
		Tools:        []string{"web_search", "kb_write", "channel.send"},
		Channels:     []string{"slack"},
	}
	got := reactSystemPrompt(d)
	if !strings.Contains(got, completionContractHeading) {
		t.Fatalf("prompt missing completion contract:\n%s", got)
	}
	if !strings.Contains(got, "Raw tool JSON") {
		t.Fatalf("completion contract should reject raw tool JSON final answers:\n%s", got)
	}
}

func TestReasoningAgentCarriesScheduleOutputWhenFullyConfigured(t *testing.T) {
	d := Draft{
		Name:         "Morning Digest",
		Strategy:     "auto",
		Intent:       "Send a daily digest to Telegram",
		SystemPrompt: "You produce a useful daily digest and deliver it to the configured channel.",
		Trigger:      Trigger{Type: "schedule", Config: map[string]any{"cron": "0 7 * * *"}},
		Channels:     []string{"telegram"},
		Output:       &ScheduleOutput{Channel: "telegram", To: "12345", BotName: "Digest Bot"},
		Tools:        []string{"web_search", "channel.send"},
	}
	def, err := ToAgentDefinition(d, false)
	if err != nil {
		t.Fatalf("ToAgentDefinition: %v", err)
	}
	if def.Schedule == nil || def.Schedule.Output == nil {
		t.Fatalf("expected schedule output to be preserved: %+v", def.Schedule)
	}
	if def.Schedule.Output.Channel != "telegram" || def.Schedule.Output.To != "12345" {
		t.Fatalf("unexpected output: %+v", def.Schedule.Output)
	}
}

func TestReasoningAgentRaisesBudgetsForNotebookPodcastWork(t *testing.T) {
	d := Draft{
		Name:         "AI Articles Podcast Briefing",
		Strategy:     "plan_execute",
		Intent:       "Search recent AI articles, fetch each article, create a NotebookLM podcast briefing, poll until audio is ready, and send it to Slack",
		SystemPrompt: "Create a podcast briefing from recent AI articles using NotebookLM source_add and studio_create.",
		Trigger:      Trigger{Type: "schedule", Config: map[string]any{"cron": "0 7 * * *"}},
		Channels:     []string{"slack"},
		Output:       &ScheduleOutput{Channel: "slack", To: "C123"},
		Tools: []string{
			"web_search",
			"fetch_url",
			"channel.send",
			"mcp__notebooklm__refresh_auth",
			"mcp__notebooklm__notebook_create",
			"mcp__notebooklm__source_add",
			"mcp__notebooklm__studio_create",
			"mcp__notebooklm__studio_status",
		},
		RunTimeout: "600s",
	}
	def, err := ToAgentDefinition(d, false)
	if err != nil {
		t.Fatalf("ToAgentDefinition: %v", err)
	}
	if def.Reasoning.MaxSteps < 24 {
		t.Fatalf("complex podcast agent should get a larger max_steps budget, got %d", def.Reasoning.MaxSteps)
	}
	if def.Reasoning.MaxPlanSteps < 12 {
		t.Fatalf("complex podcast agent should get a larger max_plan_steps budget, got %d", def.Reasoning.MaxPlanSteps)
	}
	if def.RunTimeout != "15m0s" {
		t.Fatalf("run_timeout should be raised to match reasoning total timeout, got %q", def.RunTimeout)
	}
}

func TestValidateScheduleDeliveryDoesNotAcceptHTTPOnly(t *testing.T) {
	d := Draft{
		Name:         "Daily Briefing",
		Strategy:     "plan_execute",
		Intent:       "Every morning create a briefing and send it to the team",
		SystemPrompt: "Create a briefing and deliver it.",
		Trigger:      Trigger{Type: "schedule", Config: map[string]any{"cron": "0 7 * * *"}},
		Channels:     []string{"http"},
		Tools:        []string{"web_search"},
	}
	got := Validate(d)
	if got.Ok {
		t.Fatalf("http-only scheduled delivery should not validate: %+v", got)
	}
	if !validateHasError(got, "routable output channel") {
		t.Fatalf("expected routable output error, got %+v", got.Errors)
	}
}

func TestValidateAcceptsExplicitSameChannelReplyWithoutOutboundChannel(t *testing.T) {
	d := Draft{
		Name:         "Flight Status Assistant",
		Strategy:     "auto",
		Intent:       "Answer flight status questions and reply on the same channel",
		SystemPrompt: "Answer the caller directly.",
		Trigger:      Trigger{Type: "manual"},
		Channels:     []string{"http"},
		DeliveryMode: "reply",
		Tools:        []string{"mcp__travel__query"},
	}
	got := Validate(d)
	if validateHasError(got, "routable output channel") {
		t.Fatalf("same-channel reply must not require an outbound destination: %+v", got.Errors)
	}
}

func TestValidateScheduleCannotUseSameChannelReply(t *testing.T) {
	d := Draft{
		Name:         "Scheduled Flight Status",
		Strategy:     "auto",
		Intent:       "Every morning send a flight status notification",
		SystemPrompt: "Send the status.",
		Trigger:      Trigger{Type: "schedule", Config: map[string]any{"cron": "0 7 * * *"}},
		DeliveryMode: "reply",
		Tools:        []string{"mcp__travel__query"},
	}
	got := Validate(d)
	if !validateHasError(got, "routable output channel") {
		t.Fatalf("scheduled work has no invocation route and must still require a destination: %+v", got.Errors)
	}
}

func TestValidateAcceptsExplicitRunsOnlySchedule(t *testing.T) {
	d := Draft{
		Name:         "Private Morning Briefing",
		Strategy:     "auto",
		Intent:       "Every morning generate a briefing and keep the result in Runs / Activity only",
		SystemPrompt: "Generate the briefing without sending a message.",
		Trigger:      Trigger{Type: "schedule", Config: map[string]any{"cron": "0 7 * * *"}},
		DeliveryMode: "none",
		Tools:        []string{"web_search"},
	}
	got := Validate(d)
	if validateHasError(got, "routable output channel") {
		t.Fatalf("an explicit Runs-only schedule must not require a destination: %+v", got.Errors)
	}
	for _, warning := range got.Warnings {
		if strings.Contains(warning.Message, "no explicit output channel") {
			t.Fatalf("an explicit Runs-only schedule must not warn about its chosen lack of channel: %+v", got.Warnings)
		}
	}
}

func TestValidateRecognizesLegacySameChannelIntent(t *testing.T) {
	d := Draft{
		Name:         "Legacy Assistant",
		Strategy:     "auto",
		Intent:       "Answer the request and reply directly in the same conversation",
		SystemPrompt: "Answer the caller.",
		Trigger:      Trigger{Type: "manual"},
		Channels:     []string{"http"},
		Tools:        []string{"web_search"},
	}
	got := Validate(d)
	if validateHasError(got, "routable output channel") {
		t.Fatalf("legacy same-channel intent must not require an outbound destination: %+v", got.Errors)
	}
}

func validateHasError(res ValidateResult, want string) bool {
	for _, e := range res.Errors {
		if strings.Contains(e.Message, want) {
			return true
		}
	}
	return false
}

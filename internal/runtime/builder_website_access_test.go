package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/agentmemory"
	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

func TestApplyWebsiteAccessIntentSelectsMentionedSessionsAndRemovesSubstitutes(t *testing.T) {
	u := &BuilderUnderstanding{
		Name:         "daily-tech-podcast",
		Purpose:      "Create a daily podcast from HBR and NotebookLM.",
		SystemPrompt: "Find HBR articles, add them to NotebookLM, and generate a podcast.",
		Tools: []BuilderTool{
			{Name: "web_search"},
			{Name: "fetch_url"},
			{Name: "mcp__notebooklm__add_source"},
			{Name: "channel.send"},
		},
	}
	history := []llm.ChatMessage{{Role: "user", Content: "Use HBR and NotebookLM to make the podcast."}}
	available := []BuilderConnection{
		{ID: "hbr", Name: "HBR", Domains: []string{"hbr.org"}, Ready: true},
		{ID: "notebook", Name: "NotebookLM", Domains: []string{"notebook.google.com"}, Ready: true},
		{ID: "gartner", Name: "Gartner", Domains: []string{"gartner.com"}, Ready: true},
	}

	applyWebsiteAccessIntent(u, history, available)

	if len(u.Connections) != 2 || u.Connections[0].ID != "hbr" || u.Connections[1].ID != "notebook" {
		t.Fatalf("connections = %#v", u.Connections)
	}
	if len(u.Tools) != 1 || u.Tools[0].Name != "channel.send" {
		t.Fatalf("tools = %#v", u.Tools)
	}
	if !strings.Contains(u.SystemPrompt, websiteAccessDirectiveHeading) || !strings.Contains(u.SystemPrompt, "start_website_action") {
		t.Fatalf("system prompt missing route directive: %s", u.SystemPrompt)
	}
	if len(u.Missing) != 0 {
		t.Fatalf("missing = %v", u.Missing)
	}
}

func TestApplyWebsiteAccessIntentRequiresRefreshForExpiredSession(t *testing.T) {
	u := &BuilderUnderstanding{SystemPrompt: "Read Gartner."}
	applyWebsiteAccessIntent(u,
		[]llm.ChatMessage{{Role: "user", Content: "Read Gartner using the saved login."}},
		[]BuilderConnection{{ID: "gartner", Name: "Gartner", Domains: []string{"gartner.com"}, Ready: false}},
	)
	if len(u.Missing) != 1 || u.Missing[0] != "refresh Website Access for Gartner" {
		t.Fatalf("missing = %v", u.Missing)
	}
}

func TestBuilderPodcastCreatesDeterministicMissionContract(t *testing.T) {
	u := &BuilderUnderstanding{
		Name:         "daily-tech-podcast",
		Purpose:      "Read trending articles and create a NotebookLM podcast with a listening link.",
		SystemPrompt: "Read HBR articles. Create a notebook in NotebookLM and generate a podcast.",
		Connections: []BuilderConnection{
			{ID: "hbr", Name: "HBR", Domains: []string{"hbr.org"}, Ready: true},
			{ID: "notebook", Name: "NotebookLM", Domains: []string{"notebook.google.com"}, Ready: true},
		},
	}
	contract := builderMissionContract(u, "daily-tech-podcast")
	if contract == nil || len(contract.Acceptance) != 5 {
		t.Fatalf("mission = %#v", contract)
	}
	wantTools := map[string]bool{
		"authenticated_fetch": false, "start_website_action": false,
		"inspect_website_action": false, "act_on_website": false,
	}
	for _, check := range contract.Acceptance {
		if check.Type == agent.MissionCheckRequiredTool {
			wantTools[check.Tool] = true
		}
	}
	for name, seen := range wantTools {
		if !seen {
			t.Fatalf("mission missing required tool %q: %#v", name, contract.Acceptance)
		}
	}
	generated := understandingToAgentMap(u, "test", "fake-model")
	if generated["mission"] == nil {
		t.Fatal("generated agent map omitted mission")
	}
	if yaml := generateSOULYAML(u, "test", "fake-model"); !strings.Contains(yaml, "audio-link-produced") || !strings.Contains(yaml, "max_turns: 25") {
		t.Fatalf("generated YAML missing completion contract:\n%s", yaml)
	}
}

func TestExistingGenieAgentReceivesCompletionContractAfterUpgrade(t *testing.T) {
	def := &agent.Definition{
		ID: "legacy-podcast", Description: "Create a daily podcast from trending articles.",
		SystemPrompt: "Read the articles, add them to NotebookLM, generate the podcast, and return its link.",
		Connections:  []string{"conn_notebook"},
		Labels:       map[string]string{"soulacy.owner": GenieAgentID},
	}
	contract := effectiveBuilderMissionContract(def)
	if contract == nil || !strings.HasPrefix(contract.ID, "builder-") || len(contract.Acceptance) == 0 {
		t.Fatalf("legacy contract = %#v", contract)
	}
}

func TestLegacyScheduledWebsiteAgentReceivesCompletionContractWithoutOwnerLabel(t *testing.T) {
	def := &agent.Definition{
		ID:           "daily-tech-podcast",
		Trigger:      agent.TriggerCron,
		Description:  "Create a daily podcast from trending articles using Website Access.",
		SystemPrompt: "Read authenticated articles, add them to NotebookLM, generate the podcast, and return its audio link.",
		Connections:  []string{"conn_hbr", "conn_notebook"},
	}
	contract := effectiveBuilderMissionContract(def)
	if contract == nil || !strings.HasPrefix(contract.ID, "builder-") {
		t.Fatalf("scheduled website agent did not receive a compatibility mission: %#v", contract)
	}
	wantTools := map[string]bool{
		"authenticated_fetch":    false,
		"start_website_action":   false,
		"inspect_website_action": false,
		"act_on_website":         false,
	}
	for _, check := range contract.Acceptance {
		if _, ok := wantTools[check.Tool]; ok {
			wantTools[check.Tool] = true
		}
	}
	for tool, found := range wantTools {
		if !found {
			t.Errorf("compatibility mission is missing required tool %q: %#v", tool, contract.Acceptance)
		}
	}
}

func TestOrdinaryUnownedAgentDoesNotReceiveBuilderMission(t *testing.T) {
	def := &agent.Definition{
		ID:          "interactive-reader",
		Trigger:     agent.TriggerChannel,
		Description: "Read authenticated content using Website Access.",
		Connections: []string{"conn_hbr"},
	}
	if contract := effectiveBuilderMissionContract(def); contract != nil {
		t.Fatalf("ordinary unowned agent received compatibility mission: %#v", contract)
	}
}

func TestExistingGenieAgentDerivesWebsiteMissionWithoutPinnedConnections(t *testing.T) {
	def := &agent.Definition{
		ID: "daily-tech-podcast", Description: "Read trending HBR articles using Website Access, add them to NotebookLM, generate a podcast, and return its audio link.",
		Labels: map[string]string{"soulacy.owner": GenieAgentID},
	}
	contract := effectiveBuilderMissionContract(def)
	if contract == nil {
		t.Fatal("expected a compatibility mission for an automatically discovered Website Access route")
	}
	want := map[string]bool{"authenticated_fetch": false, "start_website_action": false, "inspect_website_action": false, "act_on_website": false}
	for _, check := range contract.Acceptance {
		if _, ok := want[check.Tool]; ok {
			want[check.Tool] = true
		}
	}
	for tool, found := range want {
		if !found {
			t.Errorf("mission is missing required tool check %q: %#v", tool, contract.Acceptance)
		}
	}
}

func TestBuilderMissionRetriesThenCompletes(t *testing.T) {
	def := &agent.Definition{
		ID: "podcast", Name: "Podcast", Enabled: true,
		LLM: agent.LLMConfig{Provider: "test", Model: "fake-model"}, MaxTurns: 5,
		Builtins: strListPtr("produce_audio"),
		Mission: &agent.MissionContract{ID: "builder-podcast", Acceptance: []agent.MissionCheck{
			{ID: "produce", Type: agent.MissionCheckRequiredTool, Tool: "produce_audio", Description: "Produce the audio"},
			{ID: "link", Type: agent.MissionCheckOutputRegex, Value: `https://.+/audio`, Description: "Return the audio link"},
		}},
	}
	e, provider := newHandleTestEngine(t, def)
	e.builtins = []BuiltinTool{{Name: "produce_audio", Handler: func(context.Context, map[string]any) (string, error) {
		return `{"url":"https://audio.example/episode/audio"}`, nil
	}}}
	provider.responses = []llm.CompletionResponse{
		{Content: "I found several articles."},
		{ToolCalls: []message.ToolCall{{ID: "audio-1", Name: "produce_audio", Arguments: map[string]any{}}}},
		{Content: "Podcast ready: https://audio.example/episode/audio"},
	}

	reply, err := e.Handle(context.Background(), testUserMessage(def.ID, "mission-repair", "Make the podcast"))
	if err != nil {
		t.Fatal(err)
	}
	if got := flattenParts(reply.Parts); !strings.Contains(got, "https://audio.example/episode/audio") {
		t.Fatalf("reply = %q", got)
	}
	if reply.Metadata != nil && reply.Metadata[message.MetaReasoningDegraded] == "true" {
		t.Fatalf("successful repair marked degraded: %#v", reply.Metadata)
	}
	requests := provider.requestsSnapshot()
	if len(requests) != 3 || !chatMessagesContain(requests[1].Messages, "system", "completion repair attempt 1") {
		t.Fatalf("requests did not contain repair guidance: %#v", requests)
	}
	if requests[1].ToolChoice != "produce_audio" {
		t.Fatalf("repair tool choice = %q, want produce_audio", requests[1].ToolChoice)
	}
}

func TestBuilderMissionEmptyResponseRepairDoesNotStoreEmptyAssistantTurn(t *testing.T) {
	def := &agent.Definition{
		ID: "podcast", Name: "Podcast", Enabled: true,
		LLM: agent.LLMConfig{Provider: "test", Model: "fake-model"}, MaxTurns: 3,
		Builtins: strListPtr("produce_audio"),
		Mission: &agent.MissionContract{ID: "builder-podcast", Acceptance: []agent.MissionCheck{{
			ID: "produce", Type: agent.MissionCheckRequiredTool, Tool: "produce_audio", Description: "Produce the audio",
		}}},
	}
	e, provider := newHandleTestEngine(t, def)
	e.builtins = []BuiltinTool{{Name: "produce_audio", Handler: func(context.Context, map[string]any) (string, error) {
		return `{"url":"https://audio.example/episode/audio"}`, nil
	}}}
	provider.responses = []llm.CompletionResponse{
		{Content: ""},
		{ToolCalls: []message.ToolCall{{ID: "audio-1", Name: "produce_audio", Arguments: map[string]any{}}}},
		{Content: "Podcast ready."},
	}

	if _, err := e.Handle(context.Background(), testUserMessage(def.ID, "empty-mission-repair", "Make the podcast")); err != nil {
		t.Fatal(err)
	}
	requests := provider.requestsSnapshot()
	if len(requests) < 2 {
		t.Fatalf("requests = %d, want repair turn", len(requests))
	}
	if requests[1].ToolChoice != "produce_audio" {
		t.Fatalf("repair tool choice = %q, want produce_audio", requests[1].ToolChoice)
	}
	for _, m := range requests[1].Messages {
		if m.Role == "assistant" && strings.TrimSpace(m.Content) == "" && len(m.ToolCalls) == 0 {
			t.Fatalf("repair request contains empty assistant turn: %#v", requests[1].Messages)
		}
	}
}

func TestBuilderMissionStopsAsPartialAfterBoundedRepairs(t *testing.T) {
	def := &agent.Definition{
		ID: "podcast", Name: "Podcast", Enabled: true,
		LLM: agent.LLMConfig{Provider: "test", Model: "fake-model"}, MaxTurns: 3,
		Builtins: strListPtr("produce_audio"),
		Mission: &agent.MissionContract{ID: "builder-podcast", Acceptance: []agent.MissionCheck{{
			ID: "produce", Type: agent.MissionCheckRequiredTool, Tool: "produce_audio", Description: "Produce the audio",
		}}},
	}
	e, provider := newHandleTestEngine(t, def)
	brain := agentmemory.NewCompositeStore(t.TempDir(), nil)
	defer brain.Close()
	e.SetBrainMemory(brain)
	provider.responses = []llm.CompletionResponse{{Content: "Research complete."}, {Content: "Still researching."}, {Content: "Here is a briefing."}}

	reply, err := e.Handle(context.Background(), testUserMessage(def.ID, "mission-partial", "Make the podcast"))
	if err != nil {
		t.Fatal(err)
	}
	if reply.Metadata[message.MetaOutcome] != "partial" || reply.Metadata[message.MetaReasoningDegraded] != "true" {
		t.Fatalf("metadata = %#v", reply.Metadata)
	}
	if got := flattenParts(reply.Parts); !strings.Contains(got, "This run is incomplete") {
		t.Fatalf("reply = %q", got)
	}
	if len(provider.requestsSnapshot()) != 3 {
		t.Fatalf("provider calls = %d, want bounded three", len(provider.requestsSnapshot()))
	}
	records, err := brain.EpisodicRecords(def.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	learned := false
	for _, record := range records {
		if strings.Contains(strings.Join(record.Tags, " "), "completion_failure") && strings.Contains(record.Content, "Produce the audio") {
			learned = true
		}
	}
	if !learned {
		t.Fatalf("completion failure lesson was not persisted: %#v", records)
	}
}

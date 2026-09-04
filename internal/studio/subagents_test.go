package studio

import (
	"strings"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func TestSynthesizeAgent_QualityReusablePrompt(t *testing.T) {
	node := sdkr.FlowNode{
		ID:          "say_quiet",
		Kind:        "agent",
		Agent:       "notifier",
		Description: "send a short 'nothing notable today' note",
		Input:       "Reply exactly with this fallback message: No notable AI news today.",
	}
	na := SynthesizeAgent("notifier", node, "Weekday AI Digest")

	if na.ID != "notifier" {
		t.Errorf("id: got %q", na.ID)
	}
	if na.Name != "Notifier" {
		t.Errorf("name: want Notifier, got %q", na.Name)
	}
	if strings.TrimSpace(na.Description) == "" {
		t.Error("description must not be blank")
	}
	// The synthesized prompt must be a real, reusable persona: not blank, not a
	// one-liner, and it should weave in the node's role + workflow context.
	if thinPrompt(na.SystemPrompt) {
		t.Errorf("system prompt is thin: %q", na.SystemPrompt)
	}
	for _, want := range []string{"Notifier", "Weekday AI Digest", "fallback"} {
		if !strings.Contains(na.SystemPrompt, want) {
			t.Errorf("system prompt missing %q: %q", want, na.SystemPrompt)
		}
	}
}

func TestSynthesizeAgent_WeatherExpertBlueprint(t *testing.T) {
	node := sdkr.FlowNode{
		ID:          "get_weather_data",
		Kind:        "agent",
		Agent:       "weather_expert",
		Description: "Determine if user wants forecast or current and call appropriate tools",
		Input:       "Use resolved location details to answer weather-related questions.",
	}
	na := SynthesizeAgent("weather_expert", node, "Expert Weather Agent")

	for _, want := range []string{
		"decision-focused weather assistant",
		"not merely to report conditions",
		"current conditions",
		"forecast",
		"alerts",
		"Best window / risk window",
		"Confidence",
		"```chart",
		"Do not invent measurements",
	} {
		if !strings.Contains(na.SystemPrompt, want) {
			t.Errorf("weather prompt missing %q:\n%s", want, na.SystemPrompt)
		}
	}
}

func TestHumanizeID(t *testing.T) {
	cases := map[string]string{
		"notifier":        "Notifier",
		"news_summarizer": "News Summarizer",
		"data-fetcher":    "Data Fetcher",
		"":                "Helper Agent",
	}
	for in, want := range cases {
		if got := humanizeID(in); got != want {
			t.Errorf("humanizeID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEnsureNewAgents_FillsMissingAgent(t *testing.T) {
	// An agent node references "summarizer", which is NOT in the catalog and NOT
	// in NewAgents. ensureNewAgents must synthesize a full profile.
	d := Draft{
		Name: "Digest",
		Flow: Flow{Nodes: []sdkr.FlowNode{
			{ID: "n1", Kind: "agent", Agent: "summarizer", Description: "summarize the articles"},
		}},
	}
	ensureNewAgents(&d, Catalog{})

	if len(d.NewAgents) != 1 {
		t.Fatalf("want 1 synthesized agent, got %d", len(d.NewAgents))
	}
	na := d.NewAgents[0]
	if na.ID != "summarizer" || thinPrompt(na.SystemPrompt) || na.Description == "" {
		t.Errorf("synthesized agent incomplete: %+v", na)
	}
}

func TestEnsureNewAgents_RepairsThinProfile(t *testing.T) {
	// The model supplied a NewAgent but with a blank system prompt — repair only
	// the thin fields, keep the good ones.
	d := Draft{
		Name: "Digest",
		Flow: Flow{Nodes: []sdkr.FlowNode{
			{ID: "n1", Kind: "agent", Agent: "notifier", Description: "notify"},
		}},
		NewAgents: []NewAgent{
			{ID: "notifier", Name: "Custom Notifier", Description: "My notifier", SystemPrompt: ""},
		},
	}
	ensureNewAgents(&d, Catalog{})

	if len(d.NewAgents) != 1 {
		t.Fatalf("should not add a duplicate, got %d", len(d.NewAgents))
	}
	na := d.NewAgents[0]
	if na.Name != "Custom Notifier" || na.Description != "My notifier" {
		t.Errorf("good fields were overwritten: %+v", na)
	}
	if thinPrompt(na.SystemPrompt) {
		t.Errorf("thin system prompt not repaired: %q", na.SystemPrompt)
	}
}

func TestEnsureNewAgents_SkipsCatalogAgents(t *testing.T) {
	// An agent that already exists in the catalog must NOT be synthesized.
	d := Draft{
		Flow: Flow{Nodes: []sdkr.FlowNode{
			{ID: "n1", Kind: "agent", Agent: "researcher", Description: "research"},
		}},
	}
	ensureNewAgents(&d, Catalog{Agents: []string{"researcher"}})
	if len(d.NewAgents) != 0 {
		t.Errorf("existing catalog agent should not be synthesized, got %d", len(d.NewAgents))
	}
}

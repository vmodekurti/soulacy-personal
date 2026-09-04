package studio

import (
	"strings"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func TestExplainDraft_SummarizesPurposeStepsToolsChannels(t *testing.T) {
	d := Draft{
		Name:         "Morning digest",
		SystemPrompt: "You are a news curator. You compile a daily digest.",
		Trigger:      Trigger{Type: "schedule", Config: map[string]any{"cron": "0 7 * * *"}},
		Channels:     []string{"telegram"},
		Flow: Flow{
			Nodes: []sdkr.FlowNode{
				{ID: "search", Kind: "tool", Tool: "web_search", Output: "res", Input: `{"query":"ai"}`},
				{ID: "write", Kind: "agent", Agent: "summarizer", Input: "summarize {{ .res }}"},
			},
			Edges: []sdkr.FlowEdge{{From: "search", To: "write"}},
			Entry: "search",
		},
	}
	e := ExplainDraft(d)
	if !strings.HasPrefix(e.Purpose, "You are a news curator.") {
		t.Errorf("purpose: %q", e.Purpose)
	}
	if len(e.Steps) != 2 {
		t.Errorf("want 2 steps, got %d: %+v", len(e.Steps), e.Steps)
	}
	if len(e.Tools) != 1 || e.Tools[0] != "web_search" {
		t.Errorf("tools: %+v", e.Tools)
	}
	if len(e.Agents) != 1 || e.Agents[0] != "summarizer" {
		t.Errorf("agents: %+v", e.Agents)
	}
	if len(e.Channels) != 1 || e.Channels[0] != "telegram" {
		t.Errorf("channels: %+v", e.Channels)
	}
	// Scheduled fixed pipeline → workflow architecture (inferred).
	if e.Architecture != "workflow" {
		t.Errorf("architecture: %q (%s)", e.Architecture, e.ArchReason)
	}
}

func TestExplainDraft_HonorsModelRecommendation(t *testing.T) {
	d := Draft{
		Recommendation: &Recommendation{Mode: "react", Rationale: "exploratory"},
		Flow:           Flow{Nodes: []sdkr.FlowNode{{ID: "n", Kind: "agent", Agent: "a"}}},
	}
	e := ExplainDraft(d)
	if e.Architecture != "react" || e.ArchReason != "exploratory" {
		t.Errorf("should honor model recommendation, got %q/%q", e.Architecture, e.ArchReason)
	}
}

func TestFriendlyToolName(t *testing.T) {
	cases := map[string]string{
		"web_search":              "web_search",
		"mcp__notebooklm__create": "notebooklm: create",
		"mcp__github__open_issue": "github: open issue",
	}
	for in, want := range cases {
		if got := friendlyToolName(in); got != want {
			t.Errorf("friendlyToolName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAssessModel_LocalFirst(t *testing.T) {
	// Unconfigured → block.
	if a := AssessModel("", "", ""); a.Configured || a.Severity != "block" {
		t.Errorf("empty should be unconfigured/block: %+v", a)
	}
	// Capable local model → ok, local, no complexity note, no escalation.
	if a := AssessModel("ollama", "llama3:70b", ""); !a.Local || a.Severity != "ok" || a.CloudEscalation || a.LocalComplexityNote != "" {
		t.Errorf("capable local model: %+v", a)
	}
	// Small local model → still local + ok-to-build, but a supportive (info)
	// complexity note, NOT a warning, and never escalation.
	a := AssessModel("ollama", "llama3:8b", "")
	if !a.Local || a.Severity != "info" || a.LocalComplexityNote == "" || a.CloudEscalation {
		t.Errorf("small local model should get supportive note, no escalation: %+v", a)
	}
	if strings.Contains(strings.ToLower(a.Message), "too limited") || strings.Contains(strings.ToLower(a.LocalComplexityNote), "broken") {
		t.Errorf("message must not shame the model: %+v", a)
	}
	// Cloud builder → flagged for escalation (prompt leaves the machine).
	if c := AssessModel("anthropic", "claude-sonnet-4-6", ""); c.Local || !c.CloudEscalation {
		t.Errorf("cloud model should flag escalation: %+v", c)
	}
	// Self-hosted OpenAI-compatible endpoint on localhost → treated as local.
	if l := AssessModel("openai", "local-model", "http://localhost:1234/v1"); !l.Local || l.CloudEscalation {
		t.Errorf("localhost base_url should be local: %+v", l)
	}
}

func TestIsLocalProvider(t *testing.T) {
	cases := []struct {
		name, base string
		want       bool
	}{
		{"ollama", "", true},
		{"llamacpp", "", true},
		{"anthropic", "", false},
		{"openai", "https://api.openai.com/v1", false},
		{"openai", "http://localhost:1234/v1", true},
		{"openai", "http://127.0.0.1:8080", true},
		{"openai", "http://host.docker.internal:11434", true},
		{"openai", "http://192.168.1.50:1234/v1", true},
		{"custom", "", false}, // unknown + no base_url → cloud (safe default)
	}
	for _, c := range cases {
		if got := IsLocalProvider(c.name, c.base); got != c.want {
			t.Errorf("IsLocalProvider(%q,%q)=%v want %v", c.name, c.base, got, c.want)
		}
	}
}

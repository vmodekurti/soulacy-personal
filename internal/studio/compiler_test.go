package studio

import (
	"context"
	"errors"
	"strings"
	"testing"

	reasoning "github.com/soulacy/soulacy/internal/reasoning"
)

// fakeLLM returns canned output (or a canned error), ignoring the prompt.
type fakeLLM struct {
	out string
	err error
}

func (f fakeLLM) Complete(ctx context.Context, prompt string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.out, nil
}

// canonicalDraftJSON is the pinned draft for the canonical example intent:
// "Every weekday at 8am, fetch the top Hacker News stories, summarize the
// top 5, and Telegram them to me."
const canonicalDraftJSON = `{
  "name": "Weekday HN Digest",
  "trigger": { "type": "schedule", "config": { "cron": "0 8 * * 1-5" } },
  "channels": ["telegram"],
  "flow": {
    "nodes": [
      { "id": "fetch", "kind": "tool", "tool": "http_get", "input": "{\"url\":\"https://hacker-news.firebaseio.com/v0/topstories.json\"}", "output": "stories", "x": 0, "y": 0 },
      { "id": "summarize", "kind": "agent", "agent": "summarizer", "input": "Summarize the top 5: {{.stories}}", "output": "summary", "x": 200, "y": 0 }
    ],
    "edges": [
      { "from": "fetch", "to": "summarize" },
      { "from": "summarize", "to": "end" }
    ],
    "entry": "fetch"
  }
}`

const canonicalIntent = "Every weekday at 8am, fetch the top Hacker News stories, summarize the top 5, and Telegram them to me."

func TestCompile_HappyPath(t *testing.T) {
	llm := fakeLLM{out: canonicalDraftJSON}
	res, err := Compile(context.Background(), llm, canonicalIntent, Catalog{}, nil)
	if err != nil {
		t.Fatalf("Compile returned error: %v", err)
	}

	// Flow must pass CompileFlow.
	if _, err := reasoning.CompileFlow(res.Workflow.spec()); err != nil {
		t.Fatalf("compiled flow is invalid: %v", err)
	}

	if res.Workflow.Name == "" {
		t.Errorf("expected non-empty workflow name")
	}
	if res.Workflow.Trigger.Type != "schedule" {
		t.Errorf("trigger.type = %q, want schedule", res.Workflow.Trigger.Type)
	}
	if cron, _ := res.Workflow.Trigger.Config["cron"].(string); strings.TrimSpace(cron) == "" {
		t.Errorf("expected a cron in trigger.config, got %v", res.Workflow.Trigger.Config)
	}
	if len(res.Workflow.Channels) != 1 || res.Workflow.Channels[0] != "telegram" {
		t.Errorf("channels = %v, want [telegram]", res.Workflow.Channels)
	}
	if res.Workflow.Flow.Entry != "fetch" {
		t.Errorf("entry = %q, want fetch", res.Workflow.Flow.Entry)
	}

	// At least one tool node and one agent node.
	var hasTool, hasAgent bool
	for _, n := range res.Workflow.Flow.Nodes {
		if n.Tool != "" {
			hasTool = true
		}
		if n.Agent != "" {
			hasAgent = true
		}
	}
	if !hasTool {
		t.Errorf("expected at least one tool node")
	}
	if !hasAgent {
		t.Errorf("expected at least one agent node")
	}

	// A fully specified draft yields no clarifying questions, but does
	// carry transparency notes.
	if len(res.Questions) != 0 {
		t.Errorf("expected no questions for a complete draft, got %v", res.Questions)
	}
	if len(res.Notes) == 0 {
		t.Errorf("expected transparency notes")
	}
}

func TestCompile_ClarifyMissingChannel(t *testing.T) {
	// Same draft but with empty channels.
	noChannel := strings.Replace(canonicalDraftJSON, `"channels": ["telegram"],`, `"channels": [],`, 1)
	llm := fakeLLM{out: noChannel}

	res, err := Compile(context.Background(), llm, canonicalIntent, Catalog{}, nil)
	if err != nil {
		t.Fatalf("Compile returned error: %v", err)
	}

	// Hybrid: still returns the draft.
	if res.Workflow.Name == "" {
		t.Errorf("expected a draft despite the gap")
	}
	if _, err := reasoning.CompileFlow(res.Workflow.spec()); err != nil {
		t.Fatalf("flow should still compile: %v", err)
	}

	// AND a clarifying question about the output channel.
	var found bool
	for _, q := range res.Questions {
		if q.ID == "output_channel" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected an output_channel question, got %v", res.Questions)
	}
}

func TestCompile_ClarifyMissingScheduleTime(t *testing.T) {
	noCron := strings.Replace(canonicalDraftJSON,
		`"trigger": { "type": "schedule", "config": { "cron": "0 8 * * 1-5" } },`,
		`"trigger": { "type": "schedule", "config": {} },`, 1)
	llm := fakeLLM{out: noCron}

	// Use an intent with no concrete cadence/time signal so deterministic
	// trigger inference (S2.2) can't fill the cron — the schedule_time
	// clarifying question must still be raised. (When the intent DOES imply
	// a time, e.g. "every weekday at 8am", inference fills it instead; that
	// path is covered by trigger_test.go.)
	res, err := Compile(context.Background(), llm, "On a schedule, summarize the news for me.", Catalog{}, nil)
	if err != nil {
		t.Fatalf("Compile returned error: %v", err)
	}
	var found bool
	for _, q := range res.Questions {
		if q.ID == "schedule_time" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a schedule_time question, got %v", res.Questions)
	}
}

func TestParseDraft_CodeFences(t *testing.T) {
	fenced := "Here is your workflow:\n```json\n" + canonicalDraftJSON + "\n```\n"
	d, err := ParseDraft(fenced)
	if err != nil {
		t.Fatalf("ParseDraft failed on fenced input: %v", err)
	}
	if d.Name != "Weekday HN Digest" {
		t.Errorf("name = %q, want Weekday HN Digest", d.Name)
	}
	if d.Trigger.Type != "schedule" {
		t.Errorf("trigger.type = %q, want schedule", d.Trigger.Type)
	}
}

func TestParseDraft_BareFences(t *testing.T) {
	fenced := "```\n" + canonicalDraftJSON + "\n```"
	d, err := ParseDraft(fenced)
	if err != nil {
		t.Fatalf("ParseDraft failed on bare-fence input: %v", err)
	}
	if d.Flow.Entry != "fetch" {
		t.Errorf("entry = %q, want fetch", d.Flow.Entry)
	}
}

func TestParseDraft_Malformed(t *testing.T) {
	if _, err := ParseDraft("not json at all"); err == nil {
		t.Errorf("expected error for non-JSON input")
	}
	if _, err := ParseDraft(`{"name": "broken", `); err == nil {
		t.Errorf("expected error for truncated JSON")
	}
}

func TestCompile_LLMError(t *testing.T) {
	llm := fakeLLM{err: errors.New("boom")}
	if _, err := Compile(context.Background(), llm, canonicalIntent, Catalog{}, nil); err == nil {
		t.Errorf("expected error when LLM fails")
	}
}

func TestCompile_EmptyIntent(t *testing.T) {
	llm := fakeLLM{out: canonicalDraftJSON}
	if _, err := Compile(context.Background(), llm, "   ", Catalog{}, nil); err == nil {
		t.Errorf("expected error for empty intent")
	}
}

func TestCompile_InvalidFlowIsError(t *testing.T) {
	// Edge references an unknown node → CompileFlow must reject it.
	bad := `{
      "name": "Bad",
      "trigger": { "type": "manual" },
      "channels": ["telegram"],
      "flow": {
        "nodes": [ { "id": "a", "kind": "tool", "tool": "x" } ],
        "edges": [ { "from": "a", "to": "ghost" } ],
        "entry": "a"
      }
    }`
	llm := fakeLLM{out: bad}
	if _, err := Compile(context.Background(), llm, "do a thing", Catalog{}, nil); err == nil {
		t.Errorf("expected error for a flow that fails CompileFlow")
	}
}

func TestBuildPrompt_IncludesCatalogAndSchema(t *testing.T) {
	p := BuildPrompt("summarize my email", Catalog{
		Agents:    []string{"summarizer"},
		Tools:     []string{"http_get"},
		Providers: []string{"anthropic"},
	}, nil)
	for _, want := range []string{"summarizer", "http_get", "anthropic", "summarize my email", `"trigger"`, "schedule"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

func TestBuildPrompt_AsksForProductionSpecialistPrompts(t *testing.T) {
	p := BuildPrompt("build a weather agent", Catalog{}, nil)
	for _, want := range []string{
		"HIGH-QUALITY SPECIALIST PROMPTS",
		"decision objective",
		"tool-selection rules",
		"confidence/uncertainty handling",
		"weather expert should not merely report conditions",
		"best/risk windows",
		"chart blocks",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing specialist guidance %q", want)
		}
	}
}

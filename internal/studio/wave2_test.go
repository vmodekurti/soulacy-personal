package studio

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// ── Compile: answers round-trip ──────────────────────────────────────────────

func TestBuildPrompt_WeavesAnswers(t *testing.T) {
	answers := map[string]string{
		"schedule_time":  "0 8 * * 1-5",
		"output_channel": "telegram",
	}
	p := BuildPrompt(canonicalIntent, Catalog{}, answers)
	for _, want := range []string{
		"clarifying questions",
		"schedule_time: 0 8 * * 1-5",
		"output_channel: telegram",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q\n---\n%s", want, p)
		}
	}
	// Empty answers must not add the answers section.
	if strings.Contains(BuildPrompt(canonicalIntent, Catalog{}, nil), "clarifying questions") {
		t.Errorf("prompt with nil answers should not include the answers section")
	}
}

func TestCompile_WithAnswers_StillValidDraft(t *testing.T) {
	llm := fakeLLM{out: canonicalDraftJSON}
	answers := map[string]string{"output_channel": "telegram"}
	res, err := Compile(context.Background(), llm, canonicalIntent, Catalog{}, answers)
	if err != nil {
		t.Fatalf("Compile with answers returned error: %v", err)
	}
	if res.Workflow.Name == "" {
		t.Errorf("expected a valid draft with a name")
	}
	if len(res.Workflow.Flow.Nodes) == 0 {
		t.Errorf("expected a non-empty flow")
	}
}

// ── TestRun: mock dry-run ────────────────────────────────────────────────────

func canonicalDraft(t *testing.T) Draft {
	t.Helper()
	d, err := ParseDraft(canonicalDraftJSON)
	if err != nil {
		t.Fatalf("ParseDraft: %v", err)
	}
	return d
}

func TestTestRun_CanonicalDraft_EndToEnd(t *testing.T) {
	draft := canonicalDraft(t)
	res, err := TestRun(context.Background(), draft, "go", nil)
	if err != nil {
		t.Fatalf("TestRun returned error: %v", err)
	}

	// One trace entry per executed node, in execution order: fetch → summarize.
	if len(res.Trace) != 2 {
		t.Fatalf("expected 2 trace entries, got %d: %+v", len(res.Trace), res.Trace)
	}
	if res.Trace[0].NodeID != "fetch" || res.Trace[0].Kind != "tool" {
		t.Errorf("trace[0] = %+v, want fetch/tool", res.Trace[0])
	}
	if res.Trace[1].NodeID != "summarize" || res.Trace[1].Kind != "agent" {
		t.Errorf("trace[1] = %+v, want summarize/agent", res.Trace[1])
	}

	// Tool node output is the deterministic mock stub.
	var toolOut map[string]any
	if err := json.Unmarshal(res.Trace[0].Output, &toolOut); err != nil {
		t.Fatalf("tool output not JSON object: %v", err)
	}
	if toolOut["tool"] != "http_get" || toolOut["mocked"] != true {
		t.Errorf("tool output = %+v, want http_get/mocked", toolOut)
	}

	// Final result is non-empty (the last node's mock output).
	if len(res.Result) == 0 {
		t.Errorf("expected a non-empty final result")
	}
}

func TestTestRun_InvalidFlow_Error(t *testing.T) {
	bad := Draft{
		Name: "Bad",
		Flow: Flow{
			Nodes: canonicalDraft(t).Flow.Nodes[:1],
			Edges: nil,
			Entry: "ghost", // entry node does not exist
		},
	}
	if _, err := TestRun(context.Background(), bad, "x", nil); err == nil {
		t.Errorf("expected an error for an invalid flow")
	}
}

// ── ToAgentDefinition: draft → disabled agent ────────────────────────────────

func TestToAgentDefinition_CarriesFieldsDisabled(t *testing.T) {
	draft := canonicalDraft(t)
	def, err := ToAgentDefinition(draft, false)
	if err != nil {
		t.Fatalf("ToAgentDefinition: %v", err)
	}

	if def.Enabled {
		t.Errorf("expected Enabled=false")
	}
	if def.Name != "Weekday HN Digest" {
		t.Errorf("name = %q, want carried over", def.Name)
	}
	if def.ID != "weekday-hn-digest" {
		t.Errorf("id = %q, want slugged name", def.ID)
	}
	if def.Trigger != "cron" {
		t.Errorf("trigger = %q, want cron (mapped from schedule)", def.Trigger)
	}
	if def.Schedule == nil || def.Schedule.Cron != "0 8 * * 1-5" {
		t.Errorf("schedule = %+v, want cron carried over", def.Schedule)
	}
	if len(def.Channels) != 1 || def.Channels[0] != "telegram" {
		t.Errorf("channels = %v, want [telegram]", def.Channels)
	}
	for _, want := range []string{"schedule", "telegram", "chat"} {
		found := false
		for _, got := range def.Surfaces {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("surfaces = %v, missing %q", def.Surfaces, want)
		}
	}
	if def.Workflow == nil || len(def.Workflow.Nodes) != 2 {
		t.Fatalf("workflow not carried over: %+v", def.Workflow)
	}
	if def.Workflow.Entry != "fetch" {
		t.Errorf("workflow entry = %q, want fetch", def.Workflow.Entry)
	}
	if def.MaxTurns != 15 {
		t.Errorf("max_turns = %d, want 15", def.MaxTurns)
	}
	if def.Memory.MaxTokens != 8000 {
		t.Errorf("memory max_tokens = %d, want 8000", def.Memory.MaxTokens)
	}
	if def.LLM.Temperature != 0.7 {
		t.Errorf("llm temperature = %v, want 0.7", def.LLM.Temperature)
	}
}

func TestToAgentDefinition_EmptyNameError(t *testing.T) {
	if _, err := ToAgentDefinition(Draft{Name: "   "}, false); err == nil {
		t.Errorf("expected an error for an empty workflow name")
	}
}

package studio

import (
	"context"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// draftWith builds a minimal valid draft whose flow references the given tool
// and/or agent, used to exercise suggestMissing via the full Compile path.
func toolDraftJSON() string {
	return `{
  "name": "Fetch Flow",
  "trigger": { "type": "manual" },
  "channels": ["telegram"],
  "flow": {
    "nodes": [
      { "id": "fetch", "kind": "tool", "tool": "http_fetch", "input": "{}", "output": "data" },
      { "id": "summarize", "kind": "agent", "agent": "summarizer", "input": "go {{.data}}" }
    ],
    "edges": [
      { "from": "fetch", "to": "summarize" },
      { "from": "summarize", "to": "end" }
    ],
    "entry": "fetch"
  }
}`
}

// TestCompile_SuggestsMissingTool (S4.1 test a): a draft references a tool
// absent from the catalog -> exactly one tool Suggestion{installed:false}
// naming the missing tool.
func TestCompile_SuggestsMissingTool(t *testing.T) {
	// Catalog has the agent but NOT the http_fetch tool.
	cat := Catalog{Agents: []string{"summarizer"}, Tools: []string{"http_get"}}
	res, err := Compile(context.Background(), fakeLLM{out: toolDraftJSON()}, "fetch then summarize", cat, nil)
	if err != nil {
		t.Fatalf("Compile returned error: %v", err)
	}

	var toolSugs []Suggestion
	for _, s := range res.Suggestions {
		if s.Kind == "tool" {
			toolSugs = append(toolSugs, s)
		}
	}
	if len(toolSugs) != 1 {
		t.Fatalf("expected exactly 1 tool suggestion, got %d (%+v)", len(toolSugs), res.Suggestions)
	}
	s := toolSugs[0]
	if s.Name != "http_fetch" {
		t.Fatalf("tool suggestion name = %q, want %q", s.Name, "http_fetch")
	}
	if s.Installed {
		t.Fatalf("missing tool suggestion should have installed=false")
	}
	if s.Reason == "" {
		t.Fatalf("expected a non-empty reason")
	}
}

// TestCompile_NoSuggestionForPresentTool (S4.1 test b): a draft referencing a
// tool present in the catalog produces NO false-positive suggestion for it.
func TestCompile_NoSuggestionForPresentTool(t *testing.T) {
	// Both the tool and the agent are present -> no suggestions at all.
	cat := Catalog{Agents: []string{"summarizer"}, Tools: []string{"http_fetch"}}
	res, err := Compile(context.Background(), fakeLLM{out: toolDraftJSON()}, "fetch then summarize", cat, nil)
	if err != nil {
		t.Fatalf("Compile returned error: %v", err)
	}
	if len(res.Suggestions) != 0 {
		t.Fatalf("expected no suggestions when all referenced caps are present, got %+v", res.Suggestions)
	}
}

// TestCompile_EmptyCatalogNoSuggestions (S4.1 test c): an empty/nil catalog
// yields NO suggestions — with no context we cannot know what's installed.
func TestCompile_EmptyCatalogNoSuggestions(t *testing.T) {
	res, err := Compile(context.Background(), fakeLLM{out: toolDraftJSON()}, "fetch then summarize", Catalog{}, nil)
	if err != nil {
		t.Fatalf("Compile returned error: %v", err)
	}
	if len(res.Suggestions) != 0 {
		t.Fatalf("empty catalog must produce no suggestions, got %+v", res.Suggestions)
	}
}

// TestCompile_SuggestsMissingAgent (S4.1 test d): a referenced peer agent
// absent from the catalog yields a Suggestion{kind:agent, installed:false}.
func TestCompile_SuggestsMissingAgent(t *testing.T) {
	// Tool present, agent absent.
	cat := Catalog{Agents: []string{"researcher"}, Tools: []string{"http_fetch"}}
	res, err := Compile(context.Background(), fakeLLM{out: toolDraftJSON()}, "fetch then summarize", cat, nil)
	if err != nil {
		t.Fatalf("Compile returned error: %v", err)
	}
	var agentSugs []Suggestion
	for _, s := range res.Suggestions {
		if s.Kind == "agent" {
			agentSugs = append(agentSugs, s)
		}
	}
	if len(agentSugs) != 1 {
		t.Fatalf("expected exactly 1 agent suggestion, got %d (%+v)", len(agentSugs), res.Suggestions)
	}
	if agentSugs[0].Name != "summarizer" {
		t.Fatalf("agent suggestion name = %q, want %q", agentSugs[0].Name, "summarizer")
	}
	if agentSugs[0].Installed {
		t.Fatalf("missing agent suggestion should have installed=false")
	}
}

// TestSuggestMissing_PureFunction exercises suggestMissing directly (no LLM)
// covering tolerant matching, de-dup, and the empty-catalog guard.
func TestSuggestMissing_PureFunction(t *testing.T) {
	draft := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "a", Tool: "HTTP_Fetch"},  // case-insensitive match below
		{ID: "b", Tool: "http_fetch"},  // duplicate (normalized) -> collapsed
		{ID: "c", Tool: "send_email"},  // missing
		{ID: "d", Agent: "summarizer"}, // present
		{ID: "e", Agent: "translator"}, // missing
	}}}

	// Catalog has http_fetch (lowercase) and summarizer.
	cat := Catalog{Tools: []string{"http_fetch"}, Agents: []string{"summarizer"}}

	got := suggestMissing(draft, cat)
	// Expect: send_email (tool) and translator (agent). http_fetch matches
	// case-insensitively and is de-duped; summarizer is present.
	if len(got) != 2 {
		t.Fatalf("expected 2 missing suggestions, got %d (%+v)", len(got), got)
	}
	// Deterministic order: tools first, then agents.
	if got[0].Kind != "tool" || got[0].Name != "send_email" || got[0].Installed {
		t.Fatalf("first suggestion = %+v, want tool send_email installed=false", got[0])
	}
	if got[1].Kind != "agent" || got[1].Name != "translator" || got[1].Installed {
		t.Fatalf("second suggestion = %+v, want agent translator installed=false", got[1])
	}

	// Empty-catalog guard.
	if s := suggestMissing(draft, Catalog{}); s != nil {
		t.Fatalf("empty catalog should yield nil, got %+v", s)
	}
}

// TestReferencedCapabilities_IncludesInstalled confirms the helper reports
// installed capabilities too (the full-inventory variant), unlike suggestMissing.
func TestReferencedCapabilities_IncludesInstalled(t *testing.T) {
	draft := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "a", Tool: "http_fetch"},
		{ID: "b", Agent: "summarizer"},
	}}}
	cat := Catalog{Tools: []string{"http_fetch"}, Agents: []string{"writer"}}

	all := referencedCapabilities(draft, cat)
	if len(all) != 2 {
		t.Fatalf("expected 2 referenced caps, got %d (%+v)", len(all), all)
	}
	byName := map[string]Suggestion{}
	for _, s := range all {
		byName[s.Name] = s
	}
	if !byName["http_fetch"].Installed {
		t.Fatalf("http_fetch should be reported installed=true")
	}
	if byName["summarizer"].Installed {
		t.Fatalf("summarizer should be reported installed=false")
	}
}

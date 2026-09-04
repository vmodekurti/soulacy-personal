package studio

import (
	"strings"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func TestMatchPatterns_NotebookLM(t *testing.T) {
	got := MatchPatterns("create a notebooklm podcast from these articles", Catalog{}, 2)
	if len(got) == 0 || got[0].ID != "notebooklm_podcast" {
		t.Fatalf("expected notebooklm_podcast first, got %+v", got)
	}
}

func TestMatchPatterns_MCPPresenceBoostsRank(t *testing.T) {
	// "daily" matches scheduled_delivery and "notebooklm" matches the podcast;
	// with the notebooklm MCP present, the podcast pattern should rank first.
	cat := Catalog{MCP: []CatalogMCPServer{{Server: "notebooklm"}}}
	got := MatchPatterns("every morning make a notebooklm podcast", cat, 2)
	if len(got) == 0 || got[0].ID != "notebooklm_podcast" {
		t.Fatalf("expected notebooklm_podcast to rank first with MCP present, got %+v", got)
	}
}

func TestMatchPatterns_NoMatch(t *testing.T) {
	if got := MatchPatterns("translate this document to french", Catalog{}, 2); len(got) != 0 {
		t.Errorf("expected no pattern match, got %+v", got)
	}
}

func TestWritePatternGrounding_IncludesStepOrder(t *testing.T) {
	var sb strings.Builder
	writePatternGrounding(&sb, "make a notebooklm audio overview", Catalog{})
	out := sb.String()
	if !strings.Contains(out, "PROVEN PATTERNS") || !strings.Contains(out, "notebook_id") {
		t.Errorf("pattern grounding missing key content: %q", out)
	}
}

func TestCheckDataFlow_DanglingVarBlocks(t *testing.T) {
	d := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "gen", Kind: "tool", Tool: "x", Input: `{"notebook_id":"{{ .notebook_id }}"}`},
	}}}
	r := Preflight(d, PreflightInput{})
	if blockKinds(r)["dependency"] == 0 {
		t.Errorf("expected dependency blocker for dangling var, got %+v", r.Blockers)
	}
}

func TestCheckDataFlow_ProducedByAncestorPasses(t *testing.T) {
	d := Draft{Flow: Flow{
		Nodes: []sdkr.FlowNode{
			{ID: "create", Kind: "tool", Tool: "mk", Output: "notebook_id", Input: `{"title":"x"}`},
			{ID: "add", Kind: "tool", Tool: "add", Input: `{"notebook_id":"{{ .notebook_id }}"}`},
		},
		Edges: []sdkr.FlowEdge{{From: "create", To: "add"}},
	}}
	r := Preflight(d, PreflightInput{})
	if blockKinds(r)["dependency"] != 0 {
		t.Errorf("ancestor-produced var should not block: %+v", r.Blockers)
	}
}

func TestCheckDataFlow_ProducedButNotAncestorWarns(t *testing.T) {
	// "add" references notebook_id produced by "create", but the edge runs the
	// other way (create is NOT an ancestor of add).
	d := Draft{Flow: Flow{
		Nodes: []sdkr.FlowNode{
			{ID: "add", Kind: "tool", Tool: "add", Input: `{"notebook_id":"{{ .notebook_id }}"}`},
			{ID: "create", Kind: "tool", Tool: "mk", Output: "notebook_id", Input: `{"title":"x"}`},
		},
		Edges: []sdkr.FlowEdge{{From: "add", To: "create"}},
	}}
	r := Preflight(d, PreflightInput{})
	warned := false
	for _, w := range r.Warnings {
		if w.Kind == "dependency" {
			warned = true
		}
	}
	if !warned {
		t.Errorf("expected ordering warning, got warnings %+v / blockers %+v", r.Warnings, r.Blockers)
	}
}

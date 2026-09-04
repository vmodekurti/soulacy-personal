package studio

import (
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func TestPreflight_MalformedTemplateBlocks(t *testing.T) {
	// The exact failure: {{ ..date_info }} (double dot).
	d := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "create_notebook", Kind: "tool", Tool: "mk", Output: "nb", Input: `{"title":"AI News - {{ ..date_info }}"}`},
	}}}
	r := Preflight(d, PreflightInput{})
	if r.OK {
		t.Fatal("malformed template must block save")
	}
	if blockKinds(r)["template"] == 0 {
		t.Errorf("expected a template blocker, got %+v", r.Blockers)
	}
}

func TestPreflight_ValidTemplateWithToJsonOK(t *testing.T) {
	// A valid template using the renderer's custom func must NOT be flagged.
	d := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "a", Kind: "tool", Tool: "mk", Output: "x", Input: `{"q":"x"}`},
		{ID: "b", Kind: "agent", Agent: "ag", Input: "Summarize {{ toJson .x }} now"},
	}}}
	r := Preflight(d, PreflightInput{Catalog: Catalog{Tools: []string{"mk"}, Agents: []string{"ag"}}})
	if blockKinds(r)["template"] != 0 {
		t.Errorf("valid {{ toJson .x }} should not be flagged: %+v", r.Blockers)
	}
}

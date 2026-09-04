package studio

import (
	"context"
	"strings"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func nbCatalog() Catalog {
	return Catalog{MCP: []CatalogMCPServer{{
		Server: "notebooklm",
		Tools: []CatalogMCPTool{
			{Name: "mcp__notebooklm__create", Params: "title*:string"},
			{Name: "mcp__notebooklm__audio", Params: "notebook_id*:string"},
		},
	}}}
}

func TestAutoWire_FillsMissingNotebookID(t *testing.T) {
	d := Draft{Flow: Flow{
		Nodes: []sdkr.FlowNode{
			{ID: "create", Kind: "tool", Tool: "mcp__notebooklm__create", Output: "notebook_id", Input: `{"title":"My notebook"}`},
			{ID: "audio", Kind: "tool", Tool: "mcp__notebooklm__audio", Input: `{}`}, // missing required notebook_id
		},
		Edges: []sdkr.FlowEdge{{From: "create", To: "audio"}},
	}}
	n := AutoWire(&d, nbCatalog())
	if n != 1 {
		t.Fatalf("expected 1 wire, got %d", n)
	}
	if !strings.Contains(d.Flow.Nodes[1].Input, "{{ .notebook_id }}") {
		t.Errorf("audio input not wired: %q", d.Flow.Nodes[1].Input)
	}
	// And it should now pass preflight's dependency + arg checks.
	r := Preflight(d, PreflightInput{Catalog: nbCatalog(), ConnectedMCP: map[string]bool{"notebooklm": true}})
	if !r.OK {
		t.Errorf("expected clean preflight after autowire, blockers: %+v", r.Blockers)
	}
}

func TestAutoWire_DoesNotOverwriteFilledArg(t *testing.T) {
	d := Draft{Flow: Flow{
		Nodes: []sdkr.FlowNode{
			{ID: "create", Kind: "tool", Tool: "mcp__notebooklm__create", Output: "notebook_id", Input: `{"title":"x"}`},
			{ID: "audio", Kind: "tool", Tool: "mcp__notebooklm__audio", Input: `{"notebook_id":"already-set"}`},
		},
		Edges: []sdkr.FlowEdge{{From: "create", To: "audio"}},
	}}
	if n := AutoWire(&d, nbCatalog()); n != 0 {
		t.Errorf("should not rewire an already-filled arg, wired %d", n)
	}
	if !strings.Contains(d.Flow.Nodes[1].Input, "already-set") {
		t.Errorf("existing value clobbered: %q", d.Flow.Nodes[1].Input)
	}
}

func TestAutoWire_OnlyFromEarlierStep(t *testing.T) {
	// producer runs AFTER consumer (edge audio->create): must NOT wire.
	d := Draft{Flow: Flow{
		Nodes: []sdkr.FlowNode{
			{ID: "audio", Kind: "tool", Tool: "mcp__notebooklm__audio", Input: `{}`},
			{ID: "create", Kind: "tool", Tool: "mcp__notebooklm__create", Output: "notebook_id", Input: `{"title":"x"}`},
		},
		Edges: []sdkr.FlowEdge{{From: "audio", To: "create"}},
	}}
	if n := AutoWire(&d, nbCatalog()); n != 0 {
		t.Errorf("should not wire from a step that runs later, wired %d", n)
	}
}

func TestAutoRepair_WiresThenReportsResidual(t *testing.T) {
	// notebook_id wired automatically; a separate dangling var remains.
	d := Draft{Flow: Flow{
		Nodes: []sdkr.FlowNode{
			{ID: "create", Kind: "tool", Tool: "mcp__notebooklm__create", Output: "notebook_id", Input: `{"title":"x"}`},
			{ID: "audio", Kind: "tool", Tool: "mcp__notebooklm__audio", Input: `{"caption":"{{ .missing }}"}`},
		},
		Edges: []sdkr.FlowEdge{{From: "create", To: "audio"}},
	}}
	residual := AutoRepair(&d, nbCatalog())
	if !strings.Contains(d.Flow.Nodes[1].Input, "{{ .notebook_id }}") {
		t.Errorf("autorepair should have wired notebook_id: %q", d.Flow.Nodes[1].Input)
	}
	found := false
	for _, is := range residual {
		if strings.Contains(is.Message, "missing") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected residual dependency issue for {{ .missing }}, got %+v", residual)
	}
}

func TestFocusedRepair_FixesDanglingVarViaLLM(t *testing.T) {
	d := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "create", Kind: "tool", Tool: "mk", Output: "notebook_id", Input: `{"title":"x"}`},
		{ID: "audio", Kind: "tool", Tool: "gen", Input: `{"id":"{{ .missing }}"}`},
	}}}
	// Model returns a corrected "audio" node wiring the real producer.
	fixed := `{"nodes":[{"id":"audio","kind":"tool","tool":"gen","input":"{\"id\":\"{{ .notebook_id }}\"}"}]}`
	changed := focusedRepair(context.Background(), fakeLLM{out: fixed}, &d)
	if !changed {
		t.Fatal("focusedRepair should have applied the fix")
	}
	if !strings.Contains(d.Flow.Nodes[1].Input, "notebook_id") {
		t.Errorf("node not repaired: %q", d.Flow.Nodes[1].Input)
	}
}

func TestFocusedRepair_NoIssuesNoCall(t *testing.T) {
	d := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "n", Kind: "tool", Tool: "t", Input: `{"q":"x"}`},
	}}}
	if focusedRepair(context.Background(), fakeLLM{err: context.DeadlineExceeded}, &d) {
		t.Error("no residual issues → should not call the model or change anything")
	}
}

func TestBuildRepairPrompt_FocusedOnBrokenNodes(t *testing.T) {
	d := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "good", Kind: "tool", Tool: "web_search", Input: `{"query":"x"}`},
		{ID: "bad", Kind: "tool", Tool: "mcp__notebooklm__audio", Input: `{"notebook_id":""}`},
	}}}
	issues := []PreflightIssue{{Severity: "block", Kind: "dependency", NodeID: "bad", Message: "Required argument \"notebook_id\" is empty"}}
	p := BuildRepairPrompt(d, issues)
	if !strings.Contains(p, "\"bad\"") || !strings.Contains(p, "notebook_id") {
		t.Errorf("repair prompt should focus on the broken node: %s", p)
	}
	if strings.Contains(p, "\"good\"") {
		t.Errorf("repair prompt should NOT include the healthy node: %s", p)
	}
}

func TestRepairWiring_NormalizesRawKBWriteHandoff(t *testing.T) {
	d := Draft{
		Knowledge: []string{"AI Docs"},
		Flow: Flow{
			Nodes: []sdkr.FlowNode{
				{ID: "tag", Kind: "agent", Agent: "tagger", Output: "tagged_data"},
				{ID: "store", Kind: "tool", Tool: "kb_write", Input: `{{ .tagged_data }}`},
			},
			Edges: []sdkr.FlowEdge{{From: "tag", To: "store"}},
		},
	}
	if n := RepairWiring(&d, Catalog{}); n == 0 {
		t.Fatal("expected repair to normalize kb_write input")
	}
	got := d.Flow.Nodes[1].Input
	for _, want := range []string{`"kb":"AI Docs"`, `"content":`, `{{ toJson .tagged_data }}`, `"title":"Stored artifact"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("normalized input missing %s:\n%s", want, got)
		}
	}
	r := Preflight(d, PreflightInput{Catalog: Catalog{Tools: []string{"kb_write"}}})
	if !r.OK {
		t.Fatalf("normalized kb_write should pass preflight, blockers: %+v", r.Blockers)
	}
}

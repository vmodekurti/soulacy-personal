package studio

import (
	"strings"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func blockKinds(r PreflightResult) map[string]int {
	m := map[string]int{}
	for _, b := range r.Blockers {
		m[b.Kind]++
	}
	return m
}

func TestPreflight_CleanFlowPasses(t *testing.T) {
	d := Draft{
		Name:     "Daily digest",
		Trigger:  Trigger{Type: "schedule", Config: map[string]any{"cron": "0 7 * * *"}},
		Channels: []string{"telegram"},
		Flow: Flow{Nodes: []sdkr.FlowNode{
			{ID: "search", Kind: "tool", Tool: "web_search", Input: `{"query":"ai news"}`},
			{ID: "write", Kind: "agent", Agent: "summarizer"},
		}},
	}
	in := PreflightInput{
		Catalog:            Catalog{Tools: []string{"web_search"}, Agents: []string{"summarizer"}},
		ChannelsConfigured: map[string]bool{"telegram": true},
	}
	r := Preflight(d, in)
	if !r.OK {
		t.Fatalf("expected OK, got blockers: %+v", r.Blockers)
	}
}

func TestPreflight_DisconnectedMCPBlocks(t *testing.T) {
	d := Draft{
		Flow: Flow{Nodes: []sdkr.FlowNode{
			{ID: "n", Kind: "tool", Tool: "mcp__notebooklm__create", Input: `{"title":"x"}`},
		}},
	}
	in := PreflightInput{
		Catalog: Catalog{MCP: []CatalogMCPServer{{
			Server: "notebooklm",
			Tools:  []CatalogMCPTool{{Name: "mcp__notebooklm__create", Params: "title*:string"}},
		}}},
		ConnectedMCP: map[string]bool{}, // notebooklm NOT connected
	}
	r := Preflight(d, in)
	if r.OK {
		t.Fatal("expected block for disconnected MCP server")
	}
	if blockKinds(r)["mcp"] == 0 {
		t.Errorf("expected an mcp blocker, got %+v", r.Blockers)
	}
}

func TestPreflight_EmptyRequiredArgBlocks(t *testing.T) {
	// notebook_id required but empty/placeholder — the classic broken sequence.
	for _, input := range []string{
		`{"notebook_id":""}`,
		`{"notebook_id":"<no value>"}`,
		`{"notebook_id":"<notebook_id>"}`,
		`{}`,
	} {
		d := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
			{ID: "gen", Kind: "tool", Tool: "mcp__notebooklm__audio", Input: input},
		}}}
		in := PreflightInput{
			Catalog: Catalog{MCP: []CatalogMCPServer{{
				Server: "notebooklm",
				Tools:  []CatalogMCPTool{{Name: "mcp__notebooklm__audio", Params: "notebook_id*:string"}},
			}}},
			ConnectedMCP: map[string]bool{"notebooklm": true},
		}
		r := Preflight(d, in)
		if r.OK {
			t.Errorf("input %q: expected a dependency blocker for empty notebook_id", input)
		}
		if blockKinds(r)["dependency"] == 0 {
			t.Errorf("input %q: expected dependency blocker, got %+v", input, r.Blockers)
		}
	}
}

func TestPreflight_TemplatedArgIsFilled(t *testing.T) {
	// A value wired from an upstream step ({{ .notebook }}) counts as filled.
	d := Draft{Flow: Flow{
		Nodes: []sdkr.FlowNode{
			{ID: "create", Kind: "tool", Tool: "mcp__notebooklm__create", Output: "notebook", Input: `{"title":"x"}`},
			{ID: "gen", Kind: "tool", Tool: "mcp__notebooklm__audio", Input: `{"notebook_id":"{{ .notebook }}"}`},
		},
		Edges: []sdkr.FlowEdge{{From: "create", To: "gen"}},
	}}
	in := PreflightInput{
		Catalog: Catalog{MCP: []CatalogMCPServer{{
			Server: "notebooklm",
			Tools: []CatalogMCPTool{
				{Name: "mcp__notebooklm__create", Params: "title*:string"},
				{Name: "mcp__notebooklm__audio", Params: "notebook_id*:string"},
			},
		}}},
		ConnectedMCP: map[string]bool{"notebooklm": true},
	}
	r := Preflight(d, in)
	if !r.OK {
		t.Errorf("templated arg should be considered filled, got blockers: %+v", r.Blockers)
	}
}

func TestPreflight_BuiltinRequiredArgsAndRawInputBlock(t *testing.T) {
	tests := []struct {
		name  string
		tool  string
		input string
	}{
		{name: "fetch url missing url", tool: "fetch_url", input: `{}`},
		{name: "kb write missing content", tool: "kb_write", input: `{"kb":"AI Docs"}`},
		{name: "kb write raw freeform", tool: "kb_write", input: `{{ .tagged_data }}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
				{ID: "n", Kind: "tool", Tool: tt.tool, Input: tt.input},
			}}}
			r := Preflight(d, PreflightInput{Catalog: Catalog{Tools: []string{tt.tool}}})
			if r.OK {
				t.Fatalf("expected builtin contract blocker for %s", tt.tool)
			}
			if blockKinds(r)["dependency"] == 0 {
				t.Fatalf("expected dependency blocker, got %+v", r.Blockers)
			}
		})
	}
}

func TestPreflight_BuiltinJSONSafeTemplatePasses(t *testing.T) {
	d := Draft{Flow: Flow{
		Nodes: []sdkr.FlowNode{
			{ID: "tag", Kind: "agent", Agent: "tagger", Output: "tagged_data"},
			{ID: "store", Kind: "tool", Tool: "kb_write", Input: `{"kb":"AI Docs","content":{{ toJson .tagged_data }}}`},
		},
		Edges: []sdkr.FlowEdge{{From: "tag", To: "store"}},
	}}
	r := Preflight(d, PreflightInput{Catalog: Catalog{Tools: []string{"kb_write"}}})
	if !r.OK {
		t.Fatalf("json-safe builtin template should pass, blockers: %+v", r.Blockers)
	}
}

func TestPreflight_BuiltinContractToolNotWarnedWhenCatalogPartial(t *testing.T) {
	d := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "fetch", Kind: "tool", Tool: "fetch_url", Input: `{"url":"https://example.com"}`},
	}}}
	r := Preflight(d, PreflightInput{Catalog: Catalog{Tools: []string{"web_search"}}})
	for _, w := range r.Warnings {
		if w.Kind == "tool" {
			t.Fatalf("known built-in contract tool should not warn with partial catalog: %+v", r.Warnings)
		}
	}
}

func TestPreflight_ChannelSendUsesDotNameAndTextAliases(t *testing.T) {
	d := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "notify", Kind: "tool", Tool: "channel.send", Input: `{"channel":"telegram","to":"123","message":"queued"}`},
	}}}
	r := Preflight(d, PreflightInput{Catalog: Catalog{Tools: []string{"web_search"}}})
	if !r.OK {
		t.Fatalf("channel.send with runtime-supported message alias should pass, blockers: %+v", r.Blockers)
	}
	for _, w := range r.Warnings {
		if w.Kind == "tool" {
			t.Fatalf("channel.send should be recognized as a built-in even when catalog is partial: %+v", r.Warnings)
		}
	}
	if isBuiltinContractTool("channel_send") {
		t.Fatal("legacy channel_send must not be advertised as a Studio built-in contract")
	}
	if !isBuiltinContractTool("channel.send") {
		t.Fatal("channel.send must be advertised as the Studio built-in contract")
	}
}

func TestPreflight_ChannelSendRequiresSomeTextBody(t *testing.T) {
	d := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "notify", Kind: "tool", Tool: "channel.send", Input: `{"channel":"telegram","to":"123"}`},
	}}}
	r := Preflight(d, PreflightInput{Catalog: Catalog{Tools: []string{"channel.send"}}})
	if r.OK {
		t.Fatal("channel.send without text/message/body/content should be blocked")
	}
	found := false
	for _, b := range r.Blockers {
		if b.NodeID == "notify" && strings.Contains(b.Message, `"text"`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected missing text blocker, got %+v", r.Blockers)
	}
}

func TestPreflight_BlocksAmbiguousOutputContract(t *testing.T) {
	d := Draft{Flow: Flow{Nodes: []sdkr.FlowNode{
		{ID: "a", Kind: "tool", Tool: "web_search", Input: `{"query":"a"}`, Output: "same"},
		{ID: "b", Kind: "agent", Agent: "summarizer", Output: "same"},
	}}}
	r := Preflight(d, PreflightInput{Catalog: Catalog{Tools: []string{"web_search"}, Agents: []string{"summarizer"}}})
	if r.OK {
		t.Fatal("duplicate output vars should block preflight")
	}
	if blockKinds(r)["dependency"] == 0 {
		t.Fatalf("expected dependency blocker for duplicate output vars, got %+v", r.Blockers)
	}
}

func TestPreflight_BlocksWriteFileForKnowledgeIngestion(t *testing.T) {
	d := Draft{
		Name:   "AI Docs ingestion",
		Intent: "Pass documents or URLs, tag them, and store them in the Soulacy KB store AI Docs.",
		Flow: Flow{Nodes: []sdkr.FlowNode{
			{ID: "store", Kind: "tool", Tool: "write_file", Input: `{"path":"index.json","content":"{{ .tagged }}"}`},
		}},
	}
	r := Preflight(d, PreflightInput{Catalog: Catalog{Tools: []string{"write_file", "kb_write"}}})
	if r.OK {
		t.Fatal("expected policy blocker for write_file KB ingestion")
	}
	if blockKinds(r)["policy"] == 0 {
		t.Fatalf("expected policy blocker, got %+v", r.Blockers)
	}
}

func TestRepairWiring_RewritesWriteFileKnowledgeIngestionToKBWrite(t *testing.T) {
	d := Draft{
		Name:      "AI Docs ingestion",
		Intent:    "Tag documents and store them in the knowledge base.",
		Knowledge: []string{"AI Docs"},
		Flow: Flow{Nodes: []sdkr.FlowNode{
			{ID: "tag", Kind: "agent", Agent: "tagger", Output: "tagged"},
			{ID: "store", Kind: "tool", Tool: "write_file", Input: `{"path":"index.json","content":"{{ .tagged }}"}`},
		}},
	}
	if n := RepairWiring(&d, Catalog{}); n == 0 {
		t.Fatal("expected RepairWiring to rewrite write_file")
	}
	store := d.Flow.Nodes[1]
	if store.Tool != "kb_write" {
		t.Fatalf("tool = %q, want kb_write", store.Tool)
	}
	if !strings.Contains(store.Input, `"kb":"AI Docs"`) || !strings.Contains(store.Input, `toJson .tagged`) {
		t.Fatalf("kb_write input not normalized: %s", store.Input)
	}
	r := Preflight(d, PreflightInput{Catalog: Catalog{Tools: []string{"kb_write"}}})
	if !r.OK {
		t.Fatalf("rewritten workflow should pass preflight, blockers: %+v", r.Blockers)
	}
}

func TestRepairWiring_RewritesWriteFileTemporaryStateToQueuePut(t *testing.T) {
	d := Draft{
		Name:   "Research Librarian",
		Intent: "Capture URLs into the pending_resources queue for next daily processing.",
		Flow: Flow{Nodes: []sdkr.FlowNode{
			{ID: "capture", Kind: "tool", Tool: "write_file", Input: `{"path":"pending.json","content":"{{ .trigger.text }}"}`},
		}},
	}
	if n := RepairWiring(&d, Catalog{}); n == 0 {
		t.Fatal("expected RepairWiring to rewrite write_file")
	}
	capture := d.Flow.Nodes[0]
	if capture.Tool != "queue_put" {
		t.Fatalf("tool = %q, want queue_put", capture.Tool)
	}
	if !strings.Contains(capture.Input, `"queue":"pending_resources"`) || !strings.Contains(capture.Input, `toJson .trigger.text`) {
		t.Fatalf("queue_put input not normalized: %s", capture.Input)
	}
}

func TestPreflight_InvalidScheduleBlocks(t *testing.T) {
	d := Draft{
		Trigger: Trigger{Type: "schedule", Config: map[string]any{"cron": ""}},
		Flow:    Flow{Nodes: []sdkr.FlowNode{{ID: "n", Kind: "agent", Agent: "a"}}},
	}
	r := Preflight(d, PreflightInput{Catalog: Catalog{Agents: []string{"a"}}})
	if blockKinds(r)["schedule"] == 0 {
		t.Errorf("expected schedule blocker, got %+v", r.Blockers)
	}
}

func TestPreflight_ValidCronVariants(t *testing.T) {
	for _, c := range []string{"0 7 * * *", "*/5 * * * *", "0 0 1 1 0 *", "@daily", "@every 1h"} {
		if !validCron(c) {
			t.Errorf("expected %q to be valid cron", c)
		}
	}
	for _, c := range []string{"", "0 7", "garbage"} {
		if validCron(c) {
			t.Errorf("expected %q to be invalid cron", c)
		}
	}
}

func TestPreflight_UnconfiguredChannelBlocks(t *testing.T) {
	d := Draft{
		Channels: []string{"slack"},
		Flow:     Flow{Nodes: []sdkr.FlowNode{{ID: "n", Kind: "agent", Agent: "a"}}},
	}
	in := PreflightInput{
		Catalog:            Catalog{Agents: []string{"a"}},
		ChannelsConfigured: map[string]bool{"telegram": true}, // slack not configured
	}
	r := Preflight(d, in)
	if blockKinds(r)["channel"] == 0 {
		t.Errorf("expected channel blocker, got %+v", r.Blockers)
	}
}

func TestPreflight_NewAgentNotDanglingButThinIsWarn(t *testing.T) {
	d := Draft{
		Flow:      Flow{Nodes: []sdkr.FlowNode{{ID: "n", Kind: "agent", Agent: "helper"}}},
		NewAgents: []NewAgent{{ID: "helper", Name: "Helper", SystemPrompt: "..."}},
	}
	r := Preflight(d, PreflightInput{Catalog: Catalog{}})
	// Defined in new_agents → not a dangling-agent warning.
	for _, w := range r.Warnings {
		if w.Kind == "agent" {
			t.Errorf("agent defined in new_agents should not warn: %+v", w)
		}
	}
	if !r.OK {
		t.Errorf("should pass with no blockers, got %+v", r.Blockers)
	}
}

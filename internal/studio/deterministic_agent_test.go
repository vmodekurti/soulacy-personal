package studio

import (
	"reflect"
	"strings"
	"testing"
)

func TestCompileDeterministicAgent_UsesAutoNotImplicitReAct(t *testing.T) {
	cat := Catalog{
		Tools:    []string{"web_search", "channel.send", "channel.status"},
		Channels: []string{"telegram"},
	}
	res, ok := CompileDeterministicAgent("Find the best AI news and send it to telegram", cat, "", nil)
	if !ok {
		t.Fatalf("expected deterministic agent")
	}
	if res.Workflow.Strategy == "react" {
		t.Fatalf("deterministic Studio generation should not choose ReAct implicitly")
	}
	if !hasExactString(res.Workflow.Tools, "web_search") {
		t.Fatalf("expected web_search tool, got %#v", res.Workflow.Tools)
	}
	if !hasExactString(res.Workflow.Tools, "channel.send") {
		t.Fatalf("expected channel.send tool, got %#v", res.Workflow.Tools)
	}
	if !hasExactString(res.Workflow.Channels, "telegram") {
		t.Fatalf("expected telegram channel, got %#v", res.Workflow.Channels)
	}
	if res.Explanation == nil || res.Explanation.Architecture == "" {
		t.Fatalf("expected deterministic explanation")
	}
}

func TestCompileDeterministicAgent_NotebookLMPlaybook(t *testing.T) {
	cat := Catalog{
		Tools:    []string{"fetch_url", "channel.send", "channel.status"},
		Channels: []string{"telegram"},
		MCP: []CatalogMCPServer{{
			Server: "notebooklm",
			Tools: []CatalogMCPTool{
				{Name: "mcp__notebooklm__refresh_auth", Description: "refresh NotebookLM auth"},
				{Name: "mcp__notebooklm__notebook_create", Description: "create notebook", Params: "title*:string"},
				{Name: "mcp__notebooklm__source_add", Description: "add source to notebook", Params: "notebook_id*:string, url:string, text:string"},
				{Name: "mcp__notebooklm__studio_create", Description: "create audio overview podcast", Params: "notebook_id*:string, artifact_type:string"},
				{Name: "mcp__notebooklm__studio_status", Description: "check audio overview status", Params: "artifact_id*:string"},
			},
		}},
	}
	res, ok := CompileDeterministicAgent("Create a NotebookLM podcast from these article URLs and send it to telegram", cat, "", nil)
	if !ok {
		t.Fatalf("expected deterministic agent")
	}
	if res.Workflow.Strategy != "plan_execute" {
		t.Fatalf("strategy=%q, want plan_execute", res.Workflow.Strategy)
	}
	for _, want := range []string{"mcp__notebooklm__notebook_create", "mcp__notebooklm__source_add", "mcp__notebooklm__studio_create"} {
		if !hasExactString(res.Workflow.Tools, want) {
			t.Fatalf("missing %s from tools %#v", want, res.Workflow.Tools)
		}
	}
	if !strings.Contains(res.Workflow.SystemPrompt, "Create or select a notebook before adding sources") {
		t.Fatalf("expected NotebookLM ordering rules in prompt:\n%s", res.Workflow.SystemPrompt)
	}
}

func TestCompileDeterministicAgent_KBIngestionUsesKBWriteNotShell(t *testing.T) {
	cat := Catalog{
		Tools: []string{"fetch_url", "kb_write", "kb_search", "queue_put", "queue_take", "queue_list", "queue_names"},
		KnowledgeBases: []CatalogKB{
			{Name: "AI Docs", Description: "AI documents and research"},
		},
	}
	res, ok := CompileDeterministicAgent("Accept URLs, tag their content, and store them in the AI Docs knowledge base", cat, "", nil)
	if !ok {
		t.Fatalf("expected deterministic agent")
	}
	for _, want := range []string{"fetch_url", "kb_write", "kb_search"} {
		if !hasExactString(res.Workflow.Tools, want) {
			t.Fatalf("missing %s from tools %#v", want, res.Workflow.Tools)
		}
	}
	if hasExactString(res.Workflow.Tools, "shell_exec") || strings.Contains(res.Workflow.SystemPrompt, "write_file") {
		t.Fatalf("KB ingestion should not use filesystem/system commands: tools=%#v prompt=%s", res.Workflow.Tools, res.Workflow.SystemPrompt)
	}
	if !hasExactString(res.Workflow.Knowledge, "AI Docs") {
		t.Fatalf("expected AI Docs knowledge attachment, got %#v", res.Workflow.Knowledge)
	}
}

func TestCompileDeterministicAgent_ChatOnlyRemovesOutboundToolsAndUsesConsistentBudget(t *testing.T) {
	cat := Catalog{Tools: []string{"web_search", "channel.send", "channel.status", "mcp__market__quote"}}
	res, ok := CompileDeterministicAgent("Create an agent named QA Analyst. Compare two stocks and return the result in chat only; do not send externally.", cat, "plan_execute", nil)
	if !ok {
		t.Fatal("expected deterministic agent")
	}
	if res.Workflow.DeliveryMode != "reply" {
		t.Fatalf("delivery mode = %q, want reply", res.Workflow.DeliveryMode)
	}
	if hasExactString(res.Workflow.Tools, "channel.send") || hasExactString(res.Workflow.Tools, "channel.status") {
		t.Fatalf("chat-only agent retained outbound tools: %#v", res.Workflow.Tools)
	}
	if res.Workflow.TotalTimeout != "1800s" {
		t.Fatalf("total timeout = %q, want 1800s", res.Workflow.TotalTimeout)
	}
}

func TestCompileDeterministicAgent_MCPOnlyUsesExactRefinedToolAllowlist(t *testing.T) {
	raw := "Create an agent named E2E QA Analyst. Use only available MCP market-data tools and return in chat only."
	refined := "Use mcp__market__quote and mcp__market__filings from the market server; also synthesize a chart."
	cat := Catalog{
		RawIntent: raw,
		Tools:     []string{"web_search", "generate_chart"},
		Skills:    []CatalogSkill{{Name: "finance-analysis", Description: "stock market analysis"}},
		MCP: []CatalogMCPServer{{Server: "market", Tools: []CatalogMCPTool{
			{Name: "mcp__market__quote"},
			{Name: "mcp__market__filings"},
			{Name: "mcp__market__insiders"},
		}}},
	}
	res, ok := CompileDeterministicAgent(refined, cat, "plan_execute", nil)
	if !ok {
		t.Fatal("deterministic agent was not built")
	}
	want := []string{"mcp__market__filings", "mcp__market__quote"}
	if got := res.Workflow.Tools; !reflect.DeepEqual(got, want) {
		t.Fatalf("tools = %#v, want exact MCP allowlist %#v", got, want)
	}
	if len(res.Workflow.Skills) != 0 {
		t.Fatalf("MCP-only agent gained unrelated skills: %#v", res.Workflow.Skills)
	}
	if res.Workflow.LLM.MaxTokens != 4096 {
		t.Fatalf("ordinary agent output budget = %d, want 4096", res.Workflow.LLM.MaxTokens)
	}
}

func TestCompileDeterministicAgent_RichReportGetsNonTruncatingOutputBudget(t *testing.T) {
	cat := Catalog{Tools: []string{"web_search"}}
	res, ok := CompileDeterministicAgent("Research two stocks and produce a decision table, Mermaid diagram, executive summary, and recommendation.", cat, "auto", nil)
	if !ok {
		t.Fatal("deterministic agent was not built")
	}
	if got := res.Workflow.LLM.MaxTokens; got != 8192 {
		t.Fatalf("rich report max_tokens = %d, want 8192", got)
	}
}

func hasExactString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

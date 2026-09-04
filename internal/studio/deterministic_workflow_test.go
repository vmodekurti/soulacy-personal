package studio

import (
	"encoding/json"
	"strings"
	"testing"

	reasoning "github.com/soulacy/soulacy/internal/reasoning"
)

func notebookPodcastCatalog() Catalog {
	return Catalog{
		Tools:    []string{"web_search", "channel.send"},
		Channels: []string{"telegram"},
		MCP: []CatalogMCPServer{
			{
				Server: "notebooklm",
				Tools: []CatalogMCPTool{
					{Name: "mcp__notebooklm__notebook_create", Description: "Create a NotebookLM notebook", Params: "title*:string"},
					{Name: "mcp__notebooklm__source_add", Description: "Add a source to a NotebookLM notebook", Params: "notebook_id*:string,source_type*:string,text:string,url:string,wait:boolean"},
					{Name: "mcp__notebooklm__studio_create", Description: "Create studio audio/video artifacts", Params: "notebook_id*:string,artifact_type*:string,confirm:boolean"},
					{Name: "mcp__notebooklm__studio_status", Description: "Check studio artifact status", Params: "notebook_id*:string"},
				},
			},
		},
	}
}

func TestCompileDeterministicWorkflow_NotebookPodcast(t *testing.T) {
	intent := `Every weekday at 7:00am, build an "AI articles podcast" as a fixed workflow (not a reasoning agent). Sources: hbr.org, technologyreview.com, gartner.com. Deliver on telegram.`

	res, ok := CompileDeterministicWorkflow(intent, notebookPodcastCatalog(), nil)
	if !ok {
		t.Fatal("expected deterministic NotebookLM podcast workflow")
	}
	if res.Workflow.IsAgent() {
		t.Fatalf("expected a fixed workflow, got strategy %q", res.Workflow.Strategy)
	}
	if got := len(res.Workflow.Flow.Nodes); got < 6 {
		t.Fatalf("expected a real multi-step workflow, got %d nodes", got)
	}
	if res.Workflow.Flow.Entry != "search_article_sources" {
		t.Fatalf("unexpected entry node %q", res.Workflow.Flow.Entry)
	}
	if _, err := reasoning.CompileFlow(res.Workflow.spec()); err != nil {
		t.Fatalf("deterministic workflow must compile: %v", err)
	}

	nodes := map[string]string{}
	for _, n := range res.Workflow.Flow.Nodes {
		nodes[n.ID] = n.Tool
	}
	for _, id := range []string{"search_article_sources", "curate_source_pack", "create_notebook", "add_article_sources", "generate_audio", "poll_audio_status"} {
		if _, ok := nodes[id]; !ok {
			t.Fatalf("missing expected node %q; nodes=%v", id, nodes)
		}
	}
	if nodes["create_notebook"] != "mcp__notebooklm__notebook_create" ||
		nodes["add_article_sources"] != "mcp__notebooklm__source_add" ||
		nodes["generate_audio"] != "mcp__notebooklm__studio_create" ||
		nodes["poll_audio_status"] != "mcp__notebooklm__studio_status" {
		t.Fatalf("NotebookLM tools not wired correctly: %#v", nodes)
	}
	if res.Workflow.Trigger.Type != "schedule" {
		t.Fatalf("expected scheduled trigger from intent, got %#v", res.Workflow.Trigger)
	}
	if cron, _ := res.Workflow.Trigger.Config["cron"].(string); cron != "0 7 * * 1-5" {
		t.Fatalf("expected weekday 7am cron, got %#v", res.Workflow.Trigger.Config)
	}
	if !res.Workflow.Unattended {
		t.Fatal("scheduled generated workflow should opt into unattended confirmations")
	}
	for _, want := range []string{"channel.send", "mcp__notebooklm__notebook_create", "mcp__notebooklm__studio_create"} {
		if !confirmToolsContain(res.Workflow.ConfirmTools, want) {
			t.Fatalf("generated workflow missing confirm_tools entry %q: %#v", want, res.Workflow.ConfirmTools)
		}
	}
	if res.Workflow.Security == nil || res.Workflow.Security.IntentGate != "deny" {
		t.Fatalf("generated workflow should default security.intent_gate:deny, got %#v", res.Workflow.Security)
	}
	def, err := ToAgentDefinition(res.Workflow, false)
	if err != nil {
		t.Fatalf("ToAgentDefinition: %v", err)
	}
	if def.Trigger != "cron" || def.Schedule == nil || def.Schedule.Cron != "0 7 * * 1-5" {
		t.Fatalf("saved definition lost schedule: trigger=%q schedule=%#v", def.Trigger, def.Schedule)
	}
	for _, want := range []string{"channel.send", "mcp__notebooklm__notebook_create", "mcp__notebooklm__studio_create"} {
		if !confirmToolsContain(def.ConfirmTools, want) {
			t.Fatalf("saved definition missing confirm_tools entry %q: %#v", want, def.ConfirmTools)
		}
	}
	if def.Security == nil || def.Security.IntentGate != "deny" {
		t.Fatalf("saved definition lost security.intent_gate:deny, got %#v", def.Security)
	}

	var searchNodeInput, searchForEach, curateInput, sourceForEach, sourceInput string
	var searchParallel, sourceParallel int
	for _, n := range res.Workflow.Flow.Nodes {
		switch n.ID {
		case "search_article_sources":
			searchNodeInput = n.Input
			searchForEach = n.ForEach
			searchParallel = n.MaxParallel
			if n.ItemVar != "source_domain" {
				t.Fatalf("search item_var=%q, want source_domain", n.ItemVar)
			}
		case "curate_source_pack":
			curateInput = n.Input
		case "add_article_sources":
			sourceForEach = n.ForEach
			sourceInput = n.Input
			sourceParallel = n.MaxParallel
			if n.ItemVar != "article" {
				t.Fatalf("source-add item_var=%q, want article", n.ItemVar)
			}
		}
	}
	var searchDomains []string
	if err := json.Unmarshal([]byte(searchForEach), &searchDomains); err != nil {
		t.Fatalf("search for_each must be a JSON domain list: %v (%s)", err, searchForEach)
	}
	for _, want := range []string{"site:hbr.org", "site:technologyreview.com", "site:gartner.com"} {
		domain := strings.TrimPrefix(want, "site:")
		if !containsString(searchDomains, domain) {
			t.Fatalf("parallel search domain missing %s: %#v", domain, searchDomains)
		}
	}
	if !strings.Contains(searchNodeInput, "site:{{ .source_domain }}") || searchParallel != 3 {
		t.Fatalf("search fan-out not configured correctly: input=%q max_parallel=%d", searchNodeInput, searchParallel)
	}
	if !strings.Contains(curateInput, `"max_per_source":3`) {
		t.Fatalf("curator does not preserve per-source fairness: %s", curateInput)
	}
	if !strings.Contains(sourceForEach, ".source_pack.items") ||
		!strings.Contains(sourceInput, ".article.notebook_text") ||
		strings.Contains(sourceInput, ".source_pack.text") ||
		sourceParallel != 1 {
		t.Fatalf("NotebookLM source fan-out not configured correctly: for_each=%q input=%q max_parallel=%d", sourceForEach, sourceInput, sourceParallel)
	}
	if len(res.Notes) == 0 || !strings.Contains(strings.Join(res.Notes, "\n"), "deterministic fixed-workflow") {
		t.Fatalf("expected deterministic note, got %#v", res.Notes)
	}
}

func TestCompileDeterministicWorkflow_NotebookPodcastCookieAware(t *testing.T) {
	intent := `Every weekday at 7:00am, build an "AI articles podcast" as a fixed workflow (not a reasoning agent). Sources: hbr.org, technologyreview.com, gartner.com, some are paywalled. Fetching paywalled pages: built-in fetch_url cannot send cookies; load domain cookies from ~/.soulacy/soulspace/<domain>_cookies.txt in Netscape format with Python's http.cookiejar.MozillaCookieJar. Deliver on telegram.`

	res, ok := CompileDeterministicWorkflow(intent, notebookPodcastCatalog(), nil)
	if !ok {
		t.Fatal("expected deterministic NotebookLM podcast workflow")
	}
	if _, err := reasoning.CompileFlow(res.Workflow.spec()); err != nil {
		t.Fatalf("deterministic workflow must compile: %v", err)
	}

	var curateCode string
	var searchForEach string
	for _, n := range res.Workflow.Flow.Nodes {
		if strings.Contains(n.ID, "cookies") {
			t.Fatalf("cookies.txt must not become a graph/search node: %#v", n)
		}
		if strings.Contains(strings.ToLower(n.ID), "cookiejar") {
			t.Fatalf("Python code identifier must not become a graph/search node: %#v", n)
		}
		if n.ID == "search_article_sources" {
			searchForEach = n.ForEach
		}
		if n.ID == "curate_source_pack" {
			curateCode = n.Code
		}
	}
	if strings.Contains(strings.ToLower(searchForEach), "cookies.txt") ||
		strings.Contains(strings.ToLower(searchForEach), "div.article") {
		t.Fatalf("implementation details must not become article sources: %s", searchForEach)
	}
	for _, want := range []string{"MozillaCookieJar", "HTTPCookieProcessor", "_cookies.txt", "authenticated_fetch_ok"} {
		if !strings.Contains(curateCode, want) {
			t.Fatalf("cookie-aware curator missing %q:\n%s", want, curateCode)
		}
	}
	if !strings.Contains(strings.Join(res.Notes, "\n"), "cookie-backed fetching enabled") {
		t.Fatalf("expected cookie-aware note, got %#v", res.Notes)
	}
}

func TestCompileDeterministicWorkflow_KnowledgeIngestion(t *testing.T) {
	cat := Catalog{
		Tools:          []string{"fetch_url", "kb_write"},
		KnowledgeBases: []CatalogKB{{Name: "AI Docs"}},
	}
	res, ok := CompileDeterministicWorkflow("Ingest URLs into the AI Docs knowledge base, tag them, and store the content", cat, nil)
	if !ok {
		t.Fatal("expected deterministic knowledge-ingestion workflow")
	}
	if len(res.Workflow.Flow.Nodes) != 3 {
		t.Fatalf("nodes=%d, want 3", len(res.Workflow.Flow.Nodes))
	}
	if res.Workflow.Flow.Nodes[2].Tool != "kb_write" {
		t.Fatalf("last tool=%q, want kb_write", res.Workflow.Flow.Nodes[2].Tool)
	}
	if res.Workflow.IsAgent() {
		t.Fatal("knowledge ingestion template should be a fixed workflow")
	}
}

func TestCompileDeterministicWorkflow_ResearchDigest(t *testing.T) {
	cat := Catalog{Tools: []string{"web_search"}, Channels: []string{"telegram"}}
	res, ok := CompileDeterministicWorkflow("Every weekday morning send an AI research digest to Telegram", cat, nil)
	if !ok {
		t.Fatal("expected deterministic research digest workflow")
	}
	if len(res.Workflow.Flow.Nodes) != 2 {
		t.Fatalf("nodes=%d, want compact two-step digest", len(res.Workflow.Flow.Nodes))
	}
	if res.Workflow.Flow.Nodes[0].Tool != "web_search" {
		t.Fatalf("first tool=%q, want web_search", res.Workflow.Flow.Nodes[0].Tool)
	}
	if len(res.Workflow.Channels) == 0 || res.Workflow.Channels[0] != "telegram" {
		t.Fatalf("channels=%v, want telegram", res.Workflow.Channels)
	}
}

package studio

import (
	"strings"
	"testing"
)

// The prompt that exposed both bugs: it names an MCP tool outright AND describes
// an interactive agent, and Studio produced a fixed two-node graph using neither.
const travelAdvisorIntent = `A conversational travel advisor agent that responds to user travel queries on demand. ` +
	`When a user asks about destinations, itineraries, deals, or travel planning, the agent uses the travel MCP ` +
	`travel tool to search for relevant travel options and uses web_search to find current deals and promotions. ` +
	`The agent should ask clarifying questions when the user's request is too vague before searching.`

func travelCatalog() Catalog {
	return Catalog{
		Tools: []string{"web_search", "fetch_url"},
		MCP: []CatalogMCPServer{{
			Server: "trvl",
			Tools: []CatalogMCPTool{
				{Name: "mcp__trvl__travel", Description: "Search flights, hotels and itineraries."},
			},
		}},
	}
}

func TestConversationalIntent(t *testing.T) {
	if !ConversationalIntent(travelAdvisorIntent) {
		t.Fatal("an on-demand agent that asks clarifying questions must read as conversational")
	}
	for _, s := range []string{
		"Every weekday at 7am send a deals digest to Telegram",
		"Daily market report emailed at 8",
		"Fetch each URL and store the contents in the knowledge base",
	} {
		if ConversationalIntent(s) {
			t.Errorf("a scheduled pipeline must not read as conversational: %q", s)
		}
	}
}

// Real phrasings that the first version missed because it required exact
// bigrams. Words get wedged between the pair in ordinary English.
func TestConversationalIntent_ToleratesWordsBetweenTheCues(t *testing.T) {
	for _, s := range []string{
		"I want to build a travel advisory agent that answers all travel related questions as an expert",
		"an agent that responds to any question about our pricing",
		"a support assistant that helps with billing queries",
		"answer customer questions about shipping",
	} {
		if !ConversationalIntent(s) {
			t.Errorf("should read as conversational: %q", s)
		}
	}
}

// The headline regression: keyword matching turned an explicitly interactive
// agent into a fixed graph, with HIGH confidence.
func TestAdviseStrategy_ConversationalIntentIsNotAPipeline(t *testing.T) {
	adv := AdviseStrategy(travelAdvisorIntent, travelCatalog(), "", false)
	if adv.Mode == "workflow" {
		t.Fatalf("a conversational agent must not be compiled as a fixed workflow: %+v", adv)
	}
	if adv.DeterministicPattern != "" {
		t.Errorf("no pipeline pattern should be claimed, got %q", adv.DeterministicPattern)
	}
}

// The reported failure, end to end.
//
// Studio's own refiner rewrites a conversational prompt into a numbered spec.
// That numbering used to read as an explicit workflow request, bypass the
// conversational guard, and let a keyword pattern claim it — "options" from
// "flight/hotel options" plus "sends" from "user sends a message" matched
// market_digest, producing a web_search → summarize graph for a travel advisor.
const refinedTravelSpec = `An interactive, conversational travel advisor agent that responds to user travel questions in real time.
1. TRIGGER: The agent activates whenever a user sends a message containing a travel-related question or request (e.g., destination recommendations, itinerary planning, flight/hotel options, visa requirements).
2. INPUTS / DATA SOURCES:
   - The user's natural-language message.
   - The trvl MCP server (tool: mcp__trvl__travel) for retrieving structured travel data.
3. PROCESSING STEPS (in order, executed adaptively per message):
   a. Receive and interpret the user's message.
   c. If the message is ambiguous, ask one brief clarifying question and wait for the reply.
   d. Call mcp__trvl__travel to retrieve relevant travel data.
4. OUTPUT: The agent's expert travel advice, delivered through the configured output channel.`

func TestAdviseStrategy_RefinerNumberingDoesNotForceAWorkflow(t *testing.T) {
	adv := AdviseStrategy(refinedTravelSpec, travelCatalog(), "", false)
	if adv.Mode == "workflow" {
		t.Fatalf("a numbered SPEC is formatting, not a workflow request: %+v", adv)
	}
	if adv.DeterministicPattern != "" {
		t.Errorf("no keyword pattern should claim this, got %q", adv.DeterministicPattern)
	}
}

func TestAdviseStrategy_NumberedSpecStillCountsWhenNotConversational(t *testing.T) {
	// The structural signal must keep working for a genuine procedure — this is
	// the case the numbering heuristic exists for.
	spec := `1. TRIGGER: every weekday at 7am.
2. SEARCH: find the latest articles.
3. CREATE NOTEBOOK: add sources.
4. GENERATE AUDIO: poll status until ready.
5. DELIVER OUTPUT: send a telegram message.`
	if adv := AdviseStrategy(spec, travelCatalog(), "", false); adv.Mode != "plan_execute" {
		t.Fatalf("an ordered operating procedure should default to Plan-Execute: %+v", adv)
	}
}

func TestExplicitWorkflowPhrase_SeparatesWordsFromFormatting(t *testing.T) {
	if !explicitWorkflowPhrase("build this as a fixed workflow") {
		t.Error("an explicit phrase must register")
	}
	// Numbering alone is NOT a phrase — that is the whole point of the split.
	if explicitWorkflowPhrase("1. do this\n2. then that") {
		t.Error("numbering is formatting, not an explicit request")
	}
	// …but it is still picked up by the structural check.
	if !structuredWorkflowProcedureRequested(refinedTravelSpec) {
		t.Error("the structural check should still see the numbered spec")
	}
}

func TestAdviseStrategy_WorkflowRequiresForceOptIn(t *testing.T) {
	// Prompt text and a refiner recommendation are not the experimental opt-in.
	// Only the dedicated force flag may generate a workflow.
	adv := AdviseStrategy(travelAdvisorIntent, travelCatalog(), "workflow", false)
	if adv.Mode != "plan_execute" {
		t.Fatalf("an unforced workflow recommendation should use Plan-Execute: %+v", adv)
	}
	if adv2 := AdviseStrategy(travelAdvisorIntent, travelCatalog(), "", true); adv2.Mode != "workflow" {
		t.Fatalf("forceWorkflow must be honoured: %+v", adv2)
	}
}

func TestAdviseStrategy_ScheduledDigestStillMatchesItsPattern(t *testing.T) {
	intent := "Every weekday at 7am, find the best laptop deals and send a digest to Telegram"
	adv := AdviseStrategy(intent, travelCatalog(), "", false)
	if adv.Mode != "plan_execute" {
		t.Fatalf("a scheduled digest should default to Plan-Execute: %+v", adv)
	}
	if adv.DeterministicPattern == "" {
		t.Error("the deterministic pattern should still be reported")
	}
}

func TestNamedMCPTools_HonoursAnExplicitMention(t *testing.T) {
	got := namedMCPTools(travelAdvisorIntent, travelCatalog())
	if len(got) != 1 || got[0] != "mcp__trvl__travel" {
		t.Fatalf("a tool named in the prompt must be selected, got %v", got)
	}
}

func TestNamedMCPTools_MatchesTheBareToolWord(t *testing.T) {
	// Users write "the travel tool", never "mcp__trvl__travel".
	cat := travelCatalog()
	if got := namedMCPTools("use the travel tool to plan a trip", cat); len(got) != 1 {
		t.Fatalf("the bare tool word should match, got %v", got)
	}
}

func TestNamedMCPTools_ServerNameSelectsAllItsTools(t *testing.T) {
	cat := Catalog{MCP: []CatalogMCPServer{{
		Server: "notebooklm",
		Tools:  []CatalogMCPTool{{Name: "mcp__notebooklm__create"}, {Name: "mcp__notebooklm__add_source"}},
	}}}
	got := namedMCPTools("build it with notebooklm", cat)
	if len(got) != 2 {
		t.Fatalf("naming a server should surface its tools, got %v", got)
	}
}

func TestNamedMCPTools_DoesNotInventToolsThatAreNotInstalled(t *testing.T) {
	// The match is against the catalogue, so an unmentioned or absent server
	// contributes nothing — this can never surface a tool the workspace lacks.
	if got := namedMCPTools("use the weather tool", travelCatalog()); len(got) != 0 {
		t.Fatalf("expected no tools, got %v", got)
	}
	if got := namedMCPTools("summarize my notes", travelCatalog()); len(got) != 0 {
		t.Fatalf("expected no tools, got %v", got)
	}
}

func TestNamedMCPTools_IgnoresVeryShortNames(t *testing.T) {
	// A one- or two-letter server id would match inside unrelated words.
	cat := Catalog{MCP: []CatalogMCPServer{{Server: "ab", Tools: []CatalogMCPTool{{Name: "mcp__ab__go"}}}}}
	if got := namedMCPTools("absolutely nothing to do with it", cat); len(got) != 0 {
		t.Fatalf("short ids must not match substrings, got %v", got)
	}
}

// The prompt-grounding filter must not trim away the one tool the user named.
// The server id here ("trvl") does NOT appear in the intent — only the tool word
// "travel" does — which is exactly the abbreviation case that used to slip
// through, since only the server id was protected.
func TestFilterCatalogForIntent_KeepsAToolNamedByItsBareWord(t *testing.T) {
	cat := travelCatalog()
	// Push the catalogue over the trim cap with unrelated tools.
	noise := CatalogMCPServer{Server: "notebooklm"}
	for i := 0; i < maxGroundedMCPTools+5; i++ {
		noise.Tools = append(noise.Tools, CatalogMCPTool{
			Name:        "mcp__notebooklm__op" + itoa(i),
			Description: "notebook operation",
		})
	}
	cat.MCP = append(cat.MCP, noise)

	got := FilterCatalogForIntent(travelAdvisorIntent, cat)
	found := false
	for _, srv := range got.MCP {
		for _, tl := range srv.Tools {
			if tl.Name == "mcp__trvl__travel" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("the explicitly named travel tool was trimmed out of the grounding")
	}
}

func TestBareToolName(t *testing.T) {
	if got := bareToolName("mcp__trvl__travel"); got != "travel" {
		t.Errorf("bareToolName = %q, want travel", got)
	}
	if got := bareToolName("web_search"); got != "web_search" {
		t.Errorf("a name with no prefix should pass through, got %q", got)
	}
	// Too short to match safely inside unrelated words.
	if got := bareToolName("mcp__x__go"); got != "" {
		t.Errorf("short names must be rejected, got %q", got)
	}
}

// End to end: the agent Studio builds for that prompt must carry the tool the
// prompt asked for.
func TestCompileDeterministicAgent_IncludesTheNamedMCPTool(t *testing.T) {
	res, ok := CompileDeterministicAgent(travelAdvisorIntent, travelCatalog(), "auto", nil)
	if !ok {
		t.Fatal("the agent should compile")
	}
	joined := strings.Join(res.Workflow.Tools, ",")
	if !strings.Contains(joined, "mcp__trvl__travel") {
		t.Fatalf("the named MCP tool is missing from the agent: %v", res.Workflow.Tools)
	}
	if !strings.Contains(joined, "web_search") {
		t.Errorf("web_search was also named and should be present: %v", res.Workflow.Tools)
	}
}

func TestDeterministicAgentName_DoesNotConfuseFlightOptionsWithStockOptions(t *testing.T) {
	// The reported bug: a travel advisor was named "Stock Advisor" because the
	// spec said "flight/hotel options" and the matcher looked for "option".
	if got := deterministicAgentName(refinedTravelSpec); got != "Travel Advisor" {
		t.Fatalf("deterministicAgentName = %q, want Travel Advisor", got)
	}
}

func TestDeterministicAgentName_StillNamesRealFinanceAgents(t *testing.T) {
	for _, s := range []string{
		"an agent that tracks my stock portfolio",
		"summarise earnings calls for tickers I follow",
		"screen options by strike and expiry for trading ideas",
	} {
		if got := deterministicAgentName(s); got != "Stock Advisor" {
			t.Errorf("deterministicAgentName(%q) = %q, want Stock Advisor", s, got)
		}
	}
}

func TestContainsWord_RespectsBoundaries(t *testing.T) {
	if containsWord("flight/hotel options", "option") {
		t.Error(`"option" must not match inside "options"`)
	}
	if !containsWord("flight/hotel options", "options") {
		t.Error(`"options" should match, delimited by a slash and end-of-string`)
	}
	if containsWord("an exceptional trip", "option") {
		t.Error(`"option" must not match inside "exceptional"`)
	}
	if !containsWord("book a hotel", "hotel") {
		t.Error("a plain word should match")
	}
}

package studio

import (
	"context"
	"strconv"
	"testing"
)

func TestRecommendAgentMode_ExplicitWorkflowDefaultsToPlanExecute(t *testing.T) {
	intent := `Every weekday at 7am, build an "AI articles podcast" as a fixed workflow (not a ReAct or Plan-Execute agent). Loop over search results, poll the NotebookLM audio generation.`
	if got := RecommendAgentMode(intent); got != "plan_execute" {
		t.Fatalf("workflow prompt text should default to plan_execute, got %q", got)
	}
	// Sanity: without the explicit cue, the same loop/poll task routes to an agent.
	agentish := `Loop over search results and poll the NotebookLM audio generation asynchronously.`
	if got := RecommendAgentMode(agentish); got == "" {
		t.Fatalf("reasoning-fit task without explicit workflow cue should route to an agent, got workflow")
	}
	// Explicit ReAct still wins over workflow cue.
	if got := RecommendAgentMode("use a react reasoning loop, not a fixed workflow"); got != "react" {
		t.Fatalf("explicit react should win, got %q", got)
	}
}

func TestRecommendAgentMode_NumberedNotebookLMProcedureUsesPlanExecute(t *testing.T) {
	intent := `1. TRIGGER: Schedule to run automatically every weekday at 7:00 AM.
2. SEARCH: Execute a web search for AI-related articles published in the last 7 days across three specific domains: site:hbr.org, site:technologyreview.com, and site:gartner.com. Retrieve the top 3 candidate URLs per domain.
3. FETCH & VALIDATE: For each candidate URL, read the corresponding cookie file from ~/.soulacy/soulspace/<domain>_cookies.txt. Execute a Python script to fetch the page content using these cookies to bypass paywalls. Pass the fetched text to an LLM to verify it is a genuine, recent article about AI, extracting its title and URL. Discard any URLs that fail to fetch or fail validation.
4. AGGREGATE & CHECK: Combine the validated URLs into a single list. If the list is empty (0 articles), send a Telegram message saying 'No new AI articles found today' and halt execution.
5. CREATE NOTEBOOK: Use mcp__notebooklm__notebook_create to create a new notebook titled 'AI Briefing — <today's date>' and capture the resulting notebook_id.
6. ADD SOURCES: Loop through the validated URL list and use mcp__notebooklm__source_add to add each URL to the notebook, passing the captured notebook_id.
7. GENERATE AUDIO: Use mcp__notebooklm__studio_create (artifact type: audio) to generate the podcast overview, passing the same notebook_id.
8. POLL STATUS: Use mcp__notebooklm__studio_status to poll the generation status until the audio artifact is complete and a URL is returned.
9. DELIVER OUTPUT: Send a Telegram message containing the episode title, the final podcast audio link, and a one-line summary list of the included articles.`

	if got := RecommendAgentMode(intent); got != "plan_execute" {
		t.Fatalf("numbered deterministic NotebookLM procedure should use plan_execute; got %q", got)
	}

	out := `{"refined_intent":` + strconv.Quote(intent) + `,"summary":"daily podcast workflow","recommended_mode":"plan_execute","mode_reason":"has polling and NotebookLM"}`
	r, err := RefinePrompt(context.Background(), fakeLLM{out: out}, intent, Catalog{})
	if err != nil {
		t.Fatalf("RefinePrompt: %v", err)
	}
	if r.RecommendedMode != "plan_execute" {
		t.Fatalf("refine should keep plan_execute for a numbered fixed procedure; got %q", r.RecommendedMode)
	}
}

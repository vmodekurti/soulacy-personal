package studio

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildRefinePromptInstruction_CoversSpecAndGrounding(t *testing.T) {
	cat := Catalog{
		Agents: []string{"summarizer"},
		Skills: []CatalogSkill{{Name: "yfinance", Description: "Yahoo Finance data"}},
		MCP: []CatalogMCPServer{{
			Server: "notebooklm",
			Tools:  []CatalogMCPTool{{Name: "mcp__notebooklm__create", Description: "create a notebook"}},
		}},
	}
	p := BuildRefinePromptInstruction("send me ai news", cat)

	for _, want := range []string{
		"requirements analyst",
		"TRIGGER", "INPUTS", "PROCESSING STEPS", "OUTPUT", "EDGE CASES",
		"refined_intent", "summary", "assumptions", "questions",
		"yfinance",                // skill grounding
		"mcp__notebooklm__create", // MCP grounding
		"summarizer",              // agent grounding
		"send me ai news",         // the intent itself
	} {
		if !strings.Contains(p, want) {
			t.Errorf("refine instruction missing %q", want)
		}
	}
}

func TestRefinePrompt_ParsesModelJSON(t *testing.T) {
	out := `{
	  "refined_intent": "Every weekday at 8am, search the web for the latest AI news, summarize the top 5 stories, and send them to Telegram.",
	  "summary": "A daily AI news digest delivered to Telegram.",
	  "assumptions": ["Assumed an 8am weekday schedule", "Assumed Telegram output"],
	  "questions": [{"id":"count","text":"How many stories?","options":["5","10"]}]
	}`
	r, err := RefinePrompt(context.Background(), fakeLLM{out: out}, "ai news to telegram", Catalog{})
	if err != nil {
		t.Fatalf("RefinePrompt: %v", err)
	}
	if r.Original != "ai news to telegram" {
		t.Errorf("original not echoed: %q", r.Original)
	}
	if !strings.Contains(r.RefinedIntent, "weekday at 8am") {
		t.Errorf("refined intent not parsed: %q", r.RefinedIntent)
	}
	if r.Summary == "" {
		t.Error("summary missing")
	}
	if len(r.Assumptions) != 2 {
		t.Errorf("want 2 assumptions, got %d", len(r.Assumptions))
	}
	if len(r.Questions) != 1 || r.Questions[0].ID != "count" {
		t.Errorf("question not parsed: %+v", r.Questions)
	}
}

func TestRefinePrompt_TolerantOfFences(t *testing.T) {
	out := "```json\n{\"refined_intent\":\"do the thing clearly and completely\",\"summary\":\"s\"}\n```"
	r, err := RefinePrompt(context.Background(), fakeLLM{out: out}, "do thing", Catalog{})
	if err != nil {
		t.Fatalf("RefinePrompt: %v", err)
	}
	if !strings.Contains(r.RefinedIntent, "clearly and completely") {
		t.Errorf("fenced JSON not parsed: %q", r.RefinedIntent)
	}
}

func TestRefinePrompt_DegradesGracefullyOnGarbage(t *testing.T) {
	// A model that returns prose with no JSON must NOT block generation: Studio
	// falls back to its local refiner, not a verbatim echo that leaves the next
	// build step guessing.
	r, err := RefinePrompt(context.Background(), fakeLLM{out: "sorry, I can't help"}, "my original intent", Catalog{})
	if err != nil {
		t.Fatalf("RefinePrompt should not error on garbage: %v", err)
	}
	if !strings.Contains(r.RefinedIntent, "Recommended architecture") || !strings.Contains(r.RefinedIntent, "Goal") {
		t.Errorf("want deterministic expanded fallback, got %q", r.RefinedIntent)
	}
}

func TestRefinePrompt_EmptyRefinedFallsBackToOriginal(t *testing.T) {
	r, err := RefinePrompt(context.Background(), fakeLLM{out: `{"refined_intent":"   ","summary":"x"}`}, "orig", Catalog{})
	if err != nil {
		t.Fatalf("RefinePrompt: %v", err)
	}
	if !strings.Contains(r.RefinedIntent, "Goal") || !strings.Contains(r.RefinedIntent, "orig") {
		t.Errorf("want deterministic fallback containing original intent, got %q", r.RefinedIntent)
	}
}

func TestRefinePrompt_ExpandsEchoedModelOutput(t *testing.T) {
	intent := `Every weekday at 7:00am, build an "AI articles podcast" as a fixed workflow. Sources: hbr.org, technologyreview.com, gartner.com. Some are paywalled. Send the finished audio link to Telegram.`
	b, _ := json.Marshal(intent)
	out := `{"refined_intent":` + string(b) + `,"summary":"same"}`
	r, err := RefinePrompt(context.Background(), fakeLLM{out: out}, intent, Catalog{Channels: []string{"telegram"}})
	if err != nil {
		t.Fatalf("RefinePrompt: %v", err)
	}
	if strings.TrimSpace(r.RefinedIntent) == strings.TrimSpace(intent) {
		t.Fatalf("refine returned the original prompt unchanged")
	}
	for _, want := range []string{"Recommended architecture", "Trigger", "Processing steps", "Outputs and delivery", "Edge cases"} {
		if !strings.Contains(r.RefinedIntent, want) {
			t.Fatalf("refined prompt missing %q:\n%s", want, r.RefinedIntent)
		}
	}
	if r.RecommendedMode != "plan_execute" {
		t.Fatalf("recommended mode = %q, want plan_execute", r.RecommendedMode)
	}
}

func TestRefinePrompt_RequiresIntent(t *testing.T) {
	if _, err := RefinePrompt(context.Background(), fakeLLM{out: "{}"}, "  ", Catalog{}); err == nil {
		t.Error("expected error on empty intent")
	}
}

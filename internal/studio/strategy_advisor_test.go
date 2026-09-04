package studio

import "testing"

func TestAdviseStrategy_NumberedNotebookProcedureUsesPlanExecute(t *testing.T) {
	intent := `1. schedule every weekday at 7am
2. search hbr.org and technologyreview.com
3. create a NotebookLM notebook
4. generate an audio podcast
5. send to telegram`
	got := AdviseStrategy(intent, Catalog{}, "", false)
	if got.Mode != "plan_execute" || got.DeterministicPattern != "NotebookLM podcast" {
		t.Fatalf("advice=%+v, want NotebookLM Plan-Execute", got)
	}
}

func TestAdviseStrategy_StrongInteractiveWeatherUsesAuto(t *testing.T) {
	got := AdviseStrategy("Answer weather questions interactively for users", Catalog{
		Generation: &GenerationProfile{Provider: "google", Model: "gemini-1.5-pro", Strong: true},
	}, "", false)
	if got.Mode != "auto" {
		t.Fatalf("mode=%q, want auto", got.Mode)
	}
}

func TestAdviseStrategy_ReActRequiresExplicitRequest(t *testing.T) {
	if got := AdviseStrategy("research and respond", Catalog{}, "", false); got.Mode == "react" {
		t.Fatalf("implicit ReAct should never be selected: %+v", got)
	}
	if got := AdviseStrategy("use a react reasoning loop to research and respond", Catalog{}, "react", false); got.Mode != "react" {
		t.Fatalf("explicit ReAct should be preserved: %+v", got)
	}
}

func TestAdviseStrategy_ScheduledDigestUsesPlanExecute(t *testing.T) {
	got := AdviseStrategy("Every morning send a concise AI research digest to Slack", Catalog{}, "", false)
	if got.Mode != "plan_execute" || got.DeterministicPattern == "" {
		t.Fatalf("advice=%+v, want deterministic Plan-Execute recommendation", got)
	}
}

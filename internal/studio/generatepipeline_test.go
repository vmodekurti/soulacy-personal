package studio

import (
	"context"
	"strings"
	"testing"
)

// pipelineFakeLLM produces canned refine + compile JSON so the pipeline can
// run to completion without a real LLM.
type pipelineFakeLLM struct{}

func (pipelineFakeLLM) Complete(ctx context.Context, prompt string) (string, error) {
	// The refine prompt embeds an "OUTPUT SHAPE" line; when we see it, return a
	// refinement payload. Otherwise return a minimal compile payload with one
	// tool node so CompileFlow accepts it.
	if strings.Contains(prompt, "refined_intent") {
		return `{
  "refined_intent": "Fetch the top HN story and send its title to Slack every morning.",
  "summary": "Daily HN top-story digest to Slack.",
  "assumptions": ["default channel = general"],
  "questions": [],
  "recommended_mode": "workflow",
  "mode_reason": "linear pipeline"
}`, nil
	}
	// Minimal workflow draft — one Python node with a `def run` and a text
	// output. Enough for the compile normalise pipeline to accept.
	return `{
  "name": "pipeline-fake",
  "trigger": {"type": "cron"},
  "flow": {
    "entry": "run",
    "nodes": [
      {"id": "run", "kind": "python", "code": "def run(inputs):\n    return {\"content\": \"ok\"}\n", "output": "run"}
    ],
    "edges": []
  }
}`, nil
}

type refineOnlyLLM struct {
	calls int
}

func (f *refineOnlyLLM) Complete(ctx context.Context, prompt string) (string, error) {
	f.calls++
	if strings.Contains(prompt, "refined_intent") {
		return `{
  "refined_intent": "Find the best AI news and send it to Telegram every morning.",
  "summary": "Daily AI digest to Telegram.",
  "assumptions": [],
  "questions": [],
  "recommended_mode": "workflow",
  "mode_reason": "The wording describes a fixed digest."
}`, nil
	}
	return `this should not be used as architecture JSON`, nil
}

// TestRunGeneratePipeline_EmitsAllPhases pins the event stream: every phase
// emits a start + complete (or skip) event, and no phase is silently dropped.
// The exact contract.OK verdict depends on preflight state; this test only
// verifies the phase choreography.
func TestRunGeneratePipeline_EmitsAllPhases(t *testing.T) {
	var events []PipelineEvent
	res, err := RunGeneratePipeline(
		context.Background(),
		pipelineFakeLLM{},
		"send hn top story to slack every morning",
		Catalog{Tools: []string{"channel.send"}},
		PipelineOptions{
			Emit: func(ev PipelineEvent) { events = append(events, ev) },
		},
	)
	if err != nil {
		t.Fatalf("pipeline returned error: %v (log=%+v)", err, res.PhaseLog)
	}
	// We expect at least one event per phase — phases without setup errors emit
	// two (start + complete/skip). Verify each phase name appears at least once.
	seen := map[PipelineEventKind]bool{}
	for _, ev := range events {
		seen[ev.Phase] = true
	}
	for _, want := range []PipelineEventKind{
		PhaseClarifyIntent, PhaseChooseStrategy, PhaseBuildGraph, PhaseValidate, PhaseRepair,
	} {
		if !seen[want] {
			t.Errorf("pipeline did not emit any events for phase %q — got %+v", want, events)
		}
	}
	if len(res.PhaseLog) != len(events) {
		t.Errorf("PhaseLog length %d != emitted count %d", len(res.PhaseLog), len(events))
	}
	if res.Refinement.RefinedIntent == "" {
		t.Errorf("expected refined intent in result, got empty")
	}
}

// TestRunGeneratePipeline_SyncModeWorksWithoutEmit exercises the caller path
// used by future headless / test consumers — no Emit callback, but PhaseLog
// still populated so the transcript is inspectable after the run.
func TestRunGeneratePipeline_SyncModeWorksWithoutEmit(t *testing.T) {
	res, err := RunGeneratePipeline(
		context.Background(),
		pipelineFakeLLM{},
		"scheduled daily digest",
		Catalog{Tools: []string{"channel.send"}},
		PipelineOptions{},
	)
	if err != nil {
		t.Fatalf("pipeline returned error: %v", err)
	}
	if len(res.PhaseLog) < 5 {
		t.Errorf("expected at least 5 phase-log entries, got %d: %+v", len(res.PhaseLog), res.PhaseLog)
	}
}

func TestRunGeneratePipeline_UsesDeterministicAgentAfterRefine(t *testing.T) {
	llm := &refineOnlyLLM{}
	res, err := RunGeneratePipeline(
		context.Background(),
		llm,
		"daily ai digest to telegram",
		Catalog{Tools: []string{"web_search", "channel.send", "channel.status"}, Channels: []string{"telegram"}},
		PipelineOptions{PreferDeterministic: true},
	)
	if err != nil {
		t.Fatalf("pipeline returned error: %v", err)
	}
	if llm.calls != 1 {
		t.Fatalf("LLM calls=%d, want exactly one refine call", llm.calls)
	}
	if !res.Compile.Workflow.IsAgent() || res.Compile.Workflow.Strategy != "plan_execute" {
		t.Fatalf("expected deterministic Plan-Execute agent, got: %#v", res.Compile.Workflow)
	}
	if res.Compile.Workflow.Strategy == "react" {
		t.Fatalf("deterministic planner should not emit implicit ReAct")
	}
	if strings.Contains(strings.Join(res.Compile.Notes, "\n"), "deterministic") == false {
		t.Fatalf("expected deterministic planner note, got %#v", res.Compile.Notes)
	}
}

func TestRunGeneratePipeline_DoesNotUseBuilderForUnforcedWorkflow(t *testing.T) {
	res, err := RunGeneratePipeline(
		context.Background(),
		pipelineFakeLLM{},
		"send hn top story to slack every morning",
		Catalog{Tools: []string{"channel.send"}},
		PipelineOptions{},
	)
	if err != nil {
		t.Fatalf("pipeline returned error: %v", err)
	}
	if !res.Compile.Workflow.IsAgent() {
		t.Fatalf("unforced generation created a workflow: %#v", res.Compile.Workflow)
	}
	if !strings.Contains(strings.Join(res.Compile.Notes, "\n"), "deterministic planner") {
		t.Fatalf("expected deterministic agent note, got %#v", res.Compile.Notes)
	}
}

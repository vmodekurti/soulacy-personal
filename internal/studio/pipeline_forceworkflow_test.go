package studio

// The Workflow switch has to mean the same thing on both generate paths.
//
// PipelineOptions had no ForceWorkflow field at all, so RunGeneratePipeline
// passed forceWorkflow=false to the strategy advisor unconditionally. Asked
// live for three reviewers running in parallel with the switch on, the streamed
// path announced "Strategy: plan_execute (reasoning agent)" and returned a
// draft with zero nodes, while the synchronous /studio/compile — the same
// request, the other entry point — honoured it and built a graph. Nothing told
// the user which one they had reached.

import (
	"context"
	"strings"
	"testing"
)

// modeFromEvents reads the strategy the pipeline announced, so the test judges
// what the user was actually shown rather than an internal value.
func modeFromEvents(events []PipelineEvent) string {
	for _, ev := range events {
		if ev.Phase == PhaseChooseStrategy && ev.Status == StatusComplete {
			if m, ok := ev.Payload["mode"].(string); ok {
				return m
			}
		}
	}
	return ""
}

func runPipelineMode(t *testing.T, force bool) string {
	t.Helper()
	var events []PipelineEvent
	_, err := RunGeneratePipeline(context.Background(), pipelineFakeLLM{}, fanOutPipelineIntent, Catalog{},
		PipelineOptions{
			ForceWorkflow: force,
			Emit:          func(ev PipelineEvent) { events = append(events, ev) },
		})
	if err != nil && modeFromEvents(events) == "" {
		t.Fatalf("pipeline produced no strategy decision: %v", err)
	}
	return modeFromEvents(events)
}

const fanOutPipelineIntent = "Every weekday morning pull the latest incident reports, then run three reviewers " +
	"in parallel over that material: a severity reviewer, a root-cause reviewer and a customer-impact " +
	"reviewer. Finally an editor combines all three into one digest."

func TestPipeline_HonoursTheWorkflowSwitch(t *testing.T) {
	if got := runPipelineMode(t, true); got != "workflow" {
		t.Fatalf("the Workflow switch was ignored: strategy = %q", got)
	}
}

// With the switch off, graph generation stays the experimental opt-in it is
// meant to be — the fix must not turn every multi-step request into a graph.
func TestPipeline_WithoutTheSwitchStillDeclinesToBuildAGraph(t *testing.T) {
	if got := runPipelineMode(t, false); got == "workflow" {
		t.Fatal("workflow generation selected itself; it is supposed to require the opt-in")
	}
}

// The advisor cannot choose Workflow on its own, so a user who describes a
// fan-out and leaves the switch off gets a reasoning agent. That is the
// intended routing — but they must be told that what they described is a graph,
// and where the switch is, or the product looks like it cannot do it.
func TestAdviseStrategy_SaysAFanOutNeedsTheWorkflowSwitch(t *testing.T) {
	advice := AdviseStrategy(fanOutPipelineIntent, Catalog{}, "", false)
	if advice.Mode == "workflow" {
		t.Fatal("precondition: the advisor is not supposed to pick workflow by itself")
	}
	if !strings.Contains(advice.CapabilityWarning, "Workflow switch") {
		t.Errorf("nothing points the user at the control that would build what they asked for: %q",
			advice.CapabilityWarning)
	}
}

// A request with no described structure must not collect the note — it would
// read as a nag on every ordinary prompt.
func TestAdviseStrategy_QuietForARequestWithNoNamedStructure(t *testing.T) {
	advice := AdviseStrategy("Each morning summarise yesterday's sales report and send it to Slack.",
		Catalog{}, "", false)
	if strings.Contains(advice.CapabilityWarning, "Workflow switch") {
		t.Errorf("suggested a fan-out for a request that named none: %q", advice.CapabilityWarning)
	}
}

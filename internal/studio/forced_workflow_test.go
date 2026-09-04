package studio

import (
	"strings"
	"testing"
)

const weatherAgentIntent = "I want to develop a conversation agent that provides timely " +
	"weather updates based on a place or a zipcode"

// Forcing Workflow is the operator's right. Claiming their conversational
// request "describes a deterministic pipeline", with high confidence and no
// warning, is not: it tells them their own words were read as something they
// are not, and says nothing about what the choice costs.
func TestForcingWorkflowOnAConversationalIntentIsHonest(t *testing.T) {
	cat := Catalog{Tools: []string{"web_search"}}
	advice := AdviseStrategy(weatherAgentIntent, cat, "workflow", true)

	if advice.Mode != "workflow" {
		t.Fatalf("forcing workflow must be obeyed, got mode %q", advice.Mode)
	}
	if strings.Contains(advice.Reason, "describes a deterministic") {
		t.Errorf("still claims the intent describes a pipeline: %q", advice.Reason)
	}
	if advice.Confidence == "high" {
		t.Error("high confidence for an architecture the intent argues against")
	}
	if advice.CapabilityWarning == "" {
		t.Fatal("no warning about what a fixed graph loses for a conversation")
	}
	// The warning has to say what is actually lost, not just that something is.
	low := strings.ToLower(advice.CapabilityWarning)
	for _, want := range []string{"clarifying question", "context"} {
		if !strings.Contains(low, want) {
			t.Errorf("warning does not mention %q: %q", want, advice.CapabilityWarning)
		}
	}
}

// Even a genuine pipeline gets the experimental warning: generation quality is
// the risk, not merely whether the intent looks deterministic.
func TestForcingWorkflowOnAPipelineIsExplicitlyWarned(t *testing.T) {
	cat := Catalog{Tools: []string{"web_search"}}
	advice := AdviseStrategy(
		"Every weekday at 7am send a digest of AI research news to telegram", cat, "workflow", true)

	if advice.Mode != "workflow" {
		t.Fatalf("mode = %q, want workflow", advice.Mode)
	}
	if advice.CapabilityWarning == "" {
		t.Fatal("experimental workflow generation must always warn")
	}
	if advice.Confidence != "low" {
		t.Errorf("confidence = %q, want low for experimental generation", advice.Confidence)
	}
}

// Left alone, the same request must still choose Auto — the warning exists
// because forcing is an override, not because Studio is unsure.
func TestUnforcedConversationalIntentStillChoosesAuto(t *testing.T) {
	advice := AdviseStrategy(weatherAgentIntent, Catalog{Tools: []string{"web_search"}}, "", false)
	if advice.Mode == "workflow" {
		t.Errorf("an unforced conversation agent was routed to a fixed workflow: %+v", advice)
	}
}

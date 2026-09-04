package studio

import (
	"strings"
	"testing"

	"github.com/soulacy/soulacy/pkg/agent"
	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func podcastFlow() Flow {
	return Flow{Nodes: []sdkr.FlowNode{
		{ID: "trigger", Kind: "trigger"},
		{ID: "search_article_sources", Kind: "tool", Tool: "web_search"},
		{ID: "curate_source_pack", Kind: "python"},
		{ID: "create_notebook", Kind: "tool", Tool: "mcp__notebooklm__notebook_create"},
		{ID: "generate_audio", Kind: "tool", Tool: "mcp__notebooklm__studio_create"},
		{ID: "poll_audio_status", Kind: "tool", Tool: "mcp__notebooklm__studio_status"},
		{ID: "deliver_audio_status", Kind: "tool", Tool: "channel.send"},
	}}
}

func TestDeriveAssertions_PodcastWorkflow(t *testing.T) {
	got := DeriveAssertions(podcastFlow())
	if !AssessAssertions(got).OK {
		t.Fatal("derived assertions must be substantive")
	}
	byOp := map[string]Assertion{}
	for _, a := range got {
		byOp[a.Op] = a
	}
	// The three roles the graph actually shows.
	if a, ok := byOp[OpCountGTE]; !ok || a.Target != "search_article_sources" {
		t.Errorf("expected a count assertion on the search step, got %+v", got)
	}
	if a, ok := byOp[OpArtifact]; !ok || a.Target != "generate_audio" {
		t.Errorf("expected an artifact assertion on the audio step, got %+v", got)
	}
	if a, ok := byOp[OpDelivered]; !ok || a.Target != "deliver_audio_status" {
		t.Errorf("expected a delivered assertion on the send step, got %+v", got)
	}
	// Structural nodes are never assertion targets.
	for _, a := range got {
		if a.Target == "trigger" {
			t.Error("a trigger node must not be an assertion target")
		}
	}
}

func TestDeriveAssertions_AlwaysSubstantive(t *testing.T) {
	// A graph with none of the recognised roles still gets a real claim.
	// not_empty, not exists — an empty list satisfies exists.
	got := DeriveAssertions(Flow{Nodes: []sdkr.FlowNode{{ID: "compute", Kind: "python"}}})
	if len(got) != 1 || got[0].Op != OpNotEmpty {
		t.Fatalf("expected a not_empty floor, got %+v", got)
	}
	if !AssessAssertions(got).OK {
		t.Error("the floor must itself be substantive")
	}
	// An empty graph must not panic.
	if a := DeriveAssertions(Flow{}); len(a) == 0 {
		t.Error("even an empty flow should yield the floor assertion")
	}
}

func TestStrengthenAssertions(t *testing.T) {
	flow := podcastFlow()

	// A weak contract (the exists fallback the generator emits) gets reinforced.
	weak := []Assertion{{Target: "result", Op: OpExists}}
	got := StrengthenAssertions(weak, flow)
	if !AssessAssertions(got).OK {
		t.Fatal("a weak contract must be strengthened")
	}
	// The author's own assertion survives — strengthening never removes.
	found := false
	for _, a := range got {
		if a.Op == OpExists && a.Target == "result" {
			found = true
		}
	}
	if !found {
		t.Error("existing assertions must be preserved, not replaced")
	}

	// An already-substantive contract is left completely alone: a human's
	// explicit choices must not be diluted by generated ones.
	strong := []Assertion{{Target: "deliver_audio_status", Op: OpDestination, Value: "-100123"}}
	if got := StrengthenAssertions(strong, flow); len(got) != 1 {
		t.Errorf("a substantive contract must not be augmented, got %+v", got)
	}
}

func TestOutcomeSpecRoundTrip(t *testing.T) {
	spec := &OutcomeSpec{
		Goal:    "deliver a daily AI podcast brief",
		Enforce: "fail",
		Assertions: []Assertion{
			{Target: "search_article_sources", Op: OpCountGTE, Value: "3"},
			{Target: "deliver_audio_status", Op: OpDelivered, Value: "telegram"},
		},
	}
	c := spec.ToAgentContract()
	if c == nil || len(c.Assertions) != 2 {
		t.Fatalf("contract did not convert: %+v", c)
	}
	if c.EnforcementMode() != agent.EnforceFail {
		t.Error("enforce should carry across")
	}
	// Every persisted assertion gains a plain-language description, so a
	// production failure reads as a claim rather than an operator name.
	for _, a := range c.Assertions {
		if strings.TrimSpace(a.Describe) == "" {
			t.Errorf("assertion %+v should carry a description", a)
		}
	}
	if !strings.Contains(c.Assertions[0].Describe, "at least 3") {
		t.Errorf("count description should state the threshold: %q", c.Assertions[0].Describe)
	}

	// Round-trip back for the Studio editor.
	back := FromAgentContract(c)
	if back == nil || len(back.Assertions) != 2 || back.Assertions[1].Op != OpDelivered {
		t.Fatalf("contract did not round-trip: %+v", back)
	}

	// Nothing to persist stays nil, so SOUL.yaml never gains an empty block.
	if (&OutcomeSpec{}).ToAgentContract() != nil {
		t.Error("an empty spec must not produce a contract")
	}
	var nilSpec *OutcomeSpec
	if nilSpec.ToAgentContract() != nil {
		t.Error("a nil spec must be nil-safe")
	}
	if FromAgentContract(nil) != nil {
		t.Error("a nil contract must convert back to nil")
	}
}

func TestSaveCarriesOutcomeContract(t *testing.T) {
	draft := Draft{
		Name: "Podcast",
		Flow: podcastFlow(),
		Outcome: &OutcomeSpec{
			Goal:       "deliver a daily brief",
			Assertions: []Assertion{{Target: "deliver_audio_status", Op: OpDelivered, Value: "telegram"}},
		},
	}
	def, err := ToAgentDefinition(draft, true)
	if err != nil {
		t.Fatal(err)
	}
	if !def.Outcome.HasAssertions() {
		t.Fatal("the saved agent must carry the outcome contract")
	}
	if def.Outcome.Goal != "deliver a daily brief" {
		t.Errorf("goal did not persist: %q", def.Outcome.Goal)
	}
	if def.Outcome.Assertions[0].Op != OpDelivered {
		t.Errorf("assertion did not persist: %+v", def.Outcome.Assertions[0])
	}

	// A draft without a contract must not gain an empty outcome block.
	plain, err := ToAgentDefinition(Draft{Name: "Plain", Flow: podcastFlow()}, true)
	if err != nil {
		t.Fatal(err)
	}
	if plain.Outcome != nil {
		t.Error("a draft with no contract must save without one")
	}
}

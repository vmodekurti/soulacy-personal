package studio

import (
	"strings"
	"testing"

	"github.com/soulacy/soulacy/pkg/agent"
	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// planDraft mirrors the podcast workflow: a scheduled trigger, three parallel
// searches that fan in, sequential processing, and a delivery step.
func planDraft() Draft {
	return Draft{
		Name:    "AI Articles Podcast",
		Trigger: Trigger{Type: "schedule", Config: map[string]any{"cron": "0 7 * * 1-5"}},
		Flow: Flow{
			Entry: "trigger",
			Nodes: []sdkr.FlowNode{
				{ID: "trigger", Kind: "trigger"},
				{ID: "search_hbr", Kind: "tool", Tool: "web_search", Description: "search HBR.org"},
				{ID: "search_mit", Kind: "tool", Tool: "web_search", Description: "search MIT Tech Review"},
				{ID: "search_gartner", Kind: "tool", Tool: "web_search", Description: "search Gartner.com"},
				{ID: "curate", Kind: "python", Description: "curate the source pack",
					Inputs:  []sdkr.FlowPort{{Name: "articles", Type: "object[]", Required: true}},
					Outputs: []sdkr.FlowPort{{Name: "source_pack", Type: "object[]"}}},
				{ID: "poll_audio_status", Kind: "tool", Tool: "mcp__notebooklm__studio_status",
					Description: "wait for the audio", Timeout: "10m"},
				{ID: "deliver", Kind: "tool", Tool: "channel.send", Description: "send to Telegram"},
			},
			Edges: []sdkr.FlowEdge{
				{From: "trigger", To: "search_hbr"},
				{From: "trigger", To: "search_mit"},
				{From: "trigger", To: "search_gartner"},
				{From: "search_hbr", To: "curate"},
				{From: "curate", To: "poll_audio_status"},
				{From: "poll_audio_status", To: "deliver"},
			},
		},
	}
}

func TestBuildPlanView_SeparatesTriggerWorkAndDelivery(t *testing.T) {
	pv := BuildPlanView(planDraft())

	if pv.Trigger.Kind != "schedule" || !strings.Contains(pv.Trigger.Detail, "0 7 * * 1-5") {
		t.Errorf("trigger not projected: %+v", pv.Trigger)
	}
	if len(pv.Delivery) != 1 || pv.Delivery[0].ID != "deliver" {
		t.Errorf("the send step belongs in Delivery, not Work: %+v", pv.Delivery)
	}
	for _, s := range pv.Work {
		if s.ID == "deliver" {
			t.Error("the delivery step must not also appear in Work")
		}
	}
	// Structural nodes are machinery, not plan steps.
	for _, s := range pv.Work {
		if s.ID == "trigger" {
			t.Error("a structural trigger node must not appear as a work stage")
		}
	}
}

func TestBuildPlanView_ParallelGroupAndJoin(t *testing.T) {
	pv := BuildPlanView(planDraft())

	var group *PlanStage
	for i := range pv.Work {
		if pv.Work[i].Kind == "parallel" {
			group = &pv.Work[i]
			break
		}
	}
	if group == nil {
		t.Fatalf("three searches fanning out from one node must form a parallel group: %+v", pv.Work)
	}
	if len(group.Branches) != 3 {
		t.Errorf("expected 3 branches, got %d", len(group.Branches))
	}
	// Each branch must appear exactly once, and never also as its own stage.
	for _, b := range group.Branches {
		for _, s := range pv.Work {
			if s.ID == b.ID {
				t.Errorf("branch %q must not also be a top-level stage", b.ID)
			}
		}
	}
	// The join policy must be stated in consequences, not jargon.
	if group.Join != JoinAll {
		t.Errorf("branches that abort on error mean all-must-succeed, got %q", group.Join)
	}
	if !strings.Contains(group.JoinDetail, "workflow stops") {
		t.Errorf("join detail should say what happens on failure: %q", group.JoinDetail)
	}
}

func TestBuildPlanView_JoinPolicyReadFromBranches(t *testing.T) {
	// Branches that skip on error ARE best-effort; claiming "all must succeed"
	// over them would be a lie the canvas contradicts.
	d := planDraft()
	for i := range d.Flow.Nodes {
		if strings.HasPrefix(d.Flow.Nodes[i].ID, "search_") {
			d.Flow.Nodes[i].OnError = "skip"
		}
	}
	pv := BuildPlanView(d)
	var group *PlanStage
	for i := range pv.Work {
		if pv.Work[i].Kind == "parallel" {
			group = &pv.Work[i]
		}
	}
	if group == nil || group.Join != JoinBestEffort {
		t.Fatalf("all-skip branches must read as best effort: %+v", group)
	}
	if !strings.Contains(group.JoinDetail, "including nothing") {
		t.Errorf("best-effort detail must warn about the empty case: %q", group.JoinDetail)
	}
	// And that danger must surface as a plan warning, not just a label.
	if !hasWarningContaining(pv, "even if every branch fails") {
		t.Errorf("best effort should raise a plan warning: %v", pv.Warnings)
	}

	// A mix of skip and abort is a quorum.
	d.Flow.Nodes[1].OnError = "abort"
	pv = BuildPlanView(d)
	for i := range pv.Work {
		if pv.Work[i].Kind == "parallel" && pv.Work[i].Join != JoinQuorum {
			t.Errorf("a mixed group should read as quorum, got %q", pv.Work[i].Join)
		}
	}
}

func TestBuildPlanView_StageOperationalFacts(t *testing.T) {
	pv := BuildPlanView(planDraft())
	byID := map[string]PlanStage{}
	var collect func([]PlanStage)
	collect = func(ss []PlanStage) {
		for _, s := range ss {
			byID[s.ID] = s
			collect(s.Branches)
		}
	}
	collect(pv.Work)
	collect(pv.Delivery)

	curate := byID["curate"]
	if !strings.Contains(curate.Input, "articles") || !strings.Contains(curate.Input, "many") {
		t.Errorf("typed ports should be rendered readably: %q", curate.Input)
	}
	if !strings.Contains(curate.Input, "*") {
		t.Errorf("a required port should be marked: %q", curate.Input)
	}
	if curate.Retry == "" || curate.Complete == "" {
		t.Errorf("every stage needs a retry policy and a completion condition: %+v", curate)
	}

	// A polling step's completion condition is the whole point of it, and is
	// invisible on the canvas.
	poll := byID["poll_audio_status"]
	if !strings.Contains(poll.Complete, "finished state") || !strings.Contains(poll.Complete, "10m") {
		t.Errorf("a poll stage should state its completion condition and timeout: %q", poll.Complete)
	}

	// Titles prefer what the author said over the tool name.
	if byID["deliver"].Title != "send to Telegram" {
		t.Errorf("title should use the author's description: %q", byID["deliver"].Title)
	}
}

func TestBuildPlanView_Warnings(t *testing.T) {
	// No success check → the failure mode this whole program exists for.
	pv := BuildPlanView(planDraft())
	if !hasWarningContaining(pv, "still be reported as successful") {
		t.Errorf("a plan with no assertions must warn: %v", pv.Warnings)
	}

	// With a contract, that warning goes away.
	d := planDraft()
	d.Outcome = &OutcomeSpec{Assertions: []Assertion{{Target: "curate", Op: OpCountGTE, Value: "1"}}}
	if hasWarningContaining(BuildPlanView(d), "still be reported as successful") {
		t.Error("a plan with assertions must not warn about missing checks")
	}

	// A workflow that produces something and delivers nowhere.
	d = planDraft()
	d.Flow.Nodes = d.Flow.Nodes[:len(d.Flow.Nodes)-1]
	if !hasWarningContaining(BuildPlanView(d), "never delivers it") {
		t.Errorf("a plan with no delivery must warn: %v", BuildPlanView(d).Warnings)
	}

	// A schedule with no time set cannot run.
	d = planDraft()
	d.Trigger.Config = nil
	if !hasWarningContaining(BuildPlanView(d), "will not run automatically") {
		t.Errorf("an unset schedule must warn: %v", BuildPlanView(d).Warnings)
	}
}

func TestBuildPlanView_IsAPureProjection(t *testing.T) {
	// Plan and Canvas must never disagree: the same graph always yields the
	// same plan, and building it must not mutate the draft.
	d := planDraft()
	before := len(d.Flow.Nodes)
	a := BuildPlanView(d)
	b := BuildPlanView(d)
	if len(d.Flow.Nodes) != before {
		t.Error("projecting a plan must not mutate the draft")
	}
	if len(a.Work) != len(b.Work) || len(a.Delivery) != len(b.Delivery) {
		t.Error("projection must be deterministic")
	}
	// An empty draft must not panic.
	if pv := BuildPlanView(Draft{}); len(pv.Work) != 0 {
		t.Errorf("an empty draft yields an empty plan: %+v", pv)
	}
}

func TestPlanViewMatchesAgentContractShape(t *testing.T) {
	// Guard the Studio↔runtime boundary: a plan built from a draft whose
	// contract came from an agent definition must still project cleanly.
	d := planDraft()
	d.Outcome = FromAgentContract(&agent.OutcomeContract{
		Assertions: []agent.OutcomeAssertion{{Target: "deliver", Op: OpDelivered}},
	})
	if pv := BuildPlanView(d); len(pv.Delivery) == 0 {
		t.Error("a draft loaded from an agent contract should still project")
	}
}

func hasWarningContaining(pv PlanView, want string) bool {
	for _, w := range pv.Warnings {
		if strings.Contains(w, want) {
			return true
		}
	}
	return false
}

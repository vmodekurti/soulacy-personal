package studio

import (
	"strings"
	"testing"
)

func TestBuildPlan_NotebookLMSkeleton(t *testing.T) {
	plan := BuildPlan("make a notebooklm podcast from these urls", Catalog{})
	if len(plan) == 0 {
		t.Fatal("expected a plan for a notebooklm intent")
	}
	if plan[0].ID != "create_notebook" || plan[0].Output != "notebook_id" {
		t.Errorf("first step should create the notebook and output notebook_id, got %+v", plan[0])
	}
	// The audio step must depend on the notebook_id wiring.
	var audio *PlanStep
	for i := range plan {
		if plan[i].ID == "generate_audio" {
			audio = &plan[i]
		}
	}
	if audio == nil || !strings.Contains(audio.Fills, "notebook_id") {
		t.Errorf("audio step must wire notebook_id, got %+v", audio)
	}
}

func TestBuildPlan_NoMatchReturnsNil(t *testing.T) {
	if plan := BuildPlan("translate this document to french", Catalog{}); plan != nil {
		t.Errorf("expected nil plan for unmatched intent, got %+v", plan)
	}
}

func TestWritePlanGrounding_InstructsRealiseExactly(t *testing.T) {
	var sb strings.Builder
	writePlanGrounding(&sb, "create a notebooklm audio overview", Catalog{})
	out := sb.String()
	for _, want := range []string{"DETERMINISTIC PLAN", "create_notebook", "notebook_id", "THIS order"} {
		if !strings.Contains(out, want) {
			t.Errorf("plan grounding missing %q:\n%s", want, out)
		}
	}
}

func TestBuildPlan_IsACopy(t *testing.T) {
	p1 := BuildPlan("notebooklm podcast", Catalog{})
	if len(p1) == 0 {
		t.Fatal("expected plan")
	}
	p1[0].ID = "MUTATED"
	p2 := BuildPlan("notebooklm podcast", Catalog{})
	if p2[0].ID == "MUTATED" {
		t.Error("BuildPlan must return a copy, not the shared catalog slice")
	}
}

package missions

import (
	"path/filepath"
	"testing"
)

func TestMissionStoreSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missions.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.Create(t.Context(), Mission{
		WorkspaceID: "personal", OwnerSubject: "admin", Title: "Morning briefing",
		Objective: "Prepare a concise leadership briefing every morning", FinishLine: "Deliver five cited items",
		Cron: "0 7 * * *", MonitorID: "genie-monitor-1", Progress: "Mission activated",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	got, err := store.Get(t.Context(), "personal", "admin", created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.MonitorID != "genie-monitor-1" || got.Status != StatusActive {
		t.Fatalf("mission=%+v", got)
	}
	plan, err := BuildExecutionPlan(got.Objective, map[string]string{"constraints": "five items"}, CapabilityInventory{PublicWeb: true})
	if err != nil {
		t.Fatal(err)
	}
	got, err = store.SaveExecutionPlan(t.Context(), "personal", "admin", got.ID, plan)
	if err != nil {
		t.Fatal(err)
	}
	if got.ExecutionPlan == nil || got.ExecutionPlan.Route != "public_web" {
		t.Fatalf("saved execution plan=%+v", got.ExecutionPlan)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err = store.UpdateState(t.Context(), "personal", "admin", got.ID, StatusBlocked, "Two sources checked", "Refresh website access", "Subscription expired")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusBlocked || got.LastProgressAt == nil {
		t.Fatalf("updated=%+v", got)
	}
	if got.ExecutionPlan == nil {
		t.Fatal("progress update discarded execution plan")
	}
}

func TestBuildPlanAndValidation(t *testing.T) {
	plan, err := BuildPlan("Watch leadership publications and prepare a useful morning briefing")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Title == "" || plan.FinishLine == "" || !plan.NeedsSchedule {
		t.Fatalf("plan=%+v", plan)
	}
	_, err = Normalize(Mission{Title: "Bad", Objective: "x", FinishLine: "done", Cron: "* * * * *", At: "tomorrow"})
	if err == nil {
		t.Fatal("accepted two schedules")
	}
}

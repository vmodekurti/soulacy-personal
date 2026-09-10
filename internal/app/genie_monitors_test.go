package app

import (
	"context"
	"testing"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/internal/scheduler"
)

func TestGenieMonitorLifecycleIsScopedAndPersistent(t *testing.T) {
	dir := t.TempDir()
	loader := runtime.NewLoader([]string{dir})
	sched := scheduler.New(nil, loader, zap.NewNop(), context.Background())
	mgr := &genieMonitorManager{loader: loader, scheduler: sched, agentDir: dir}

	created, err := mgr.CreateGenieMonitor("Check flight prices and alert below 700 dollars", "0 */4 * * *", "", "", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	id, _ := created["id"].(string)
	def := loader.Get(id)
	if def == nil || def.Labels["soulacy.owner"] != runtime.GenieAgentID || def.SystemTools || def.HasCapability("system") {
		t.Fatalf("created monitor escaped Genie boundary: %#v", def)
	}
	if got := mgr.ListGenieMonitors(); len(got) != 1 {
		t.Fatalf("monitors = %d, want 1", len(got))
	}
	if err := mgr.PauseGenieMonitor(id); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if loader.Get(id).Enabled {
		t.Fatal("paused monitor remained enabled")
	}
	if err := mgr.CancelGenieMonitor(id); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if loader.Get(id) != nil {
		t.Fatal("cancelled monitor remains loaded")
	}
}

func TestGenieMonitorRejectsInvalidOrUnownedRequests(t *testing.T) {
	dir := t.TempDir()
	loader := runtime.NewLoader([]string{dir})
	sched := scheduler.New(nil, loader, zap.NewNop(), context.Background())
	mgr := &genieMonitorManager{loader: loader, scheduler: sched, agentDir: dir}
	if _, err := mgr.CreateGenieMonitor("task", "", "", "", ""); err == nil {
		t.Fatal("missing schedule accepted")
	}
	if err := mgr.CancelGenieMonitor(runtime.SystemAgentID); err == nil {
		t.Fatal("unowned agent cancellation accepted")
	}
}

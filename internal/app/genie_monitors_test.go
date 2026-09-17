package app

import (
	"context"
	"strings"
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

// A monitor used to be a clone of Genie. The copy inherited all twelve of
// Genie's tools — create_monitor among them, so a scheduled agent running
// unattended could mint more scheduled agents — plus fifty turns, a
// thirty-minute budget, and Genie's whole orchestrator prompt.
func TestAMonitorIsBuiltForTheJobNotClonedFromGenie(t *testing.T) {
	dir := t.TempDir()
	loader := runtime.NewLoader([]string{dir})
	sched := scheduler.New(nil, loader, zap.NewNop(), context.Background())
	mgr := &genieMonitorManager{loader: loader, scheduler: sched, agentDir: dir}

	created, err := mgr.CreateGenieMonitor("Tell me when the build breaks", "0 8 * * *", "", "", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	def := loader.Get(created["id"].(string))
	if def == nil {
		t.Fatal("monitor was not persisted")
	}

	// The one that matters: a background agent must not be able to create
	// further background agents without anybody present.
	if def.Builtins == nil {
		t.Fatal("a monitor with wildcard builtins would inherit everything")
	}
	for _, banned := range []string{"create_monitor", "cancel_monitor", "pause_monitor", "channel.send"} {
		for _, have := range *def.Builtins {
			if have == banned {
				t.Errorf("a monitor must not hold %q", banned)
			}
		}
	}

	// Scoped to the job rather than to an open-ended investigation.
	if def.MaxTurns == 0 || def.MaxTurns > 20 {
		t.Errorf("max_turns = %d; a background check should be bounded", def.MaxTurns)
	}
	if def.RunTimeout == "" || def.RunTimeout == "30m" {
		t.Errorf("run_timeout = %q; Genie's interactive budget is wrong for a scheduled check", def.RunTimeout)
	}

	// It is still a monitor: owned, scheduled, and off every chat surface.
	if def.Labels["soulacy.owner"] != runtime.GenieAgentID {
		t.Error("ownership is what lets Genie list and cancel it")
	}
	if len(def.Surfaces) != 1 || def.Surfaces[0] != "schedule" {
		t.Errorf("surfaces = %v, want schedule only", def.Surfaces)
	}
	if len(def.Channels) != 0 {
		t.Errorf("a monitor delivers through the scheduler, not on its own: %v", def.Channels)
	}

	// The prompt is written for the task, not Genie's inherited essay.
	if len(def.SystemPrompt) > 1200 {
		t.Errorf("system prompt is %d chars; that is Genie's, not a monitor's", len(def.SystemPrompt))
	}
	if !strings.Contains(def.SystemPrompt, "Tell me when the build breaks") {
		t.Error("the monitor should carry its own task")
	}
	if !strings.Contains(def.SystemPrompt, "empty response") {
		t.Error("silence must still be the right answer to nothing happening")
	}
}

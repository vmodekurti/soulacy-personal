package scheduler

// Story 12 hardening tests: missed-run catch-up picks ONLY the latest
// missed fire, tolerates bad config/state, never regresses persisted
// state, and surfaces catch-up settings to the admin API / GUI.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/pkg/agent"
)

func writeSchedAgent(t *testing.T, dir, id, yaml string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, id+".yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newSchedLoader(t *testing.T, dir string) *runtime.Loader {
	t.Helper()
	l := runtime.NewLoader([]string{dir})
	if errs := l.LoadAll(); len(errs) > 0 {
		t.Fatalf("LoadAll: %v", errs)
	}
	return l
}

func TestMissedCronFire_MultipleMissed_OnlyLatest(t *testing.T) {
	// Hourly cron, 4 fires missed since lastCompleted — catch-up must run
	// exactly one (the latest), not replay the backlog.
	s := New(nil, nil, zap.NewNop(), context.Background())
	s.state.LastCompleted["hourly"] = time.Date(2026, 6, 6, 6, 0, 0, 0, time.UTC)
	now := time.Date(2026, 6, 6, 10, 30, 0, 0, time.UTC)
	def := &agent.Definition{
		ID: "hourly", Enabled: true, Trigger: agent.TriggerCron,
		Schedule: &agent.Schedule{
			Cron:                "0 * * * *",
			RunMissedOnStartup:  true,
			MissedStartupWindow: "24h",
		},
	}

	got, ok := s.missedCronFire(def, now)
	if !ok {
		t.Fatal("expected a missed fire")
	}
	want := time.Date(2026, 6, 6, 10, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("missed = %s, want latest %s (never the backlog)", got, want)
	}
}

func TestMissedCronFire_InvalidWindow_DefaultsTo24h(t *testing.T) {
	s := New(nil, nil, zap.NewNop(), context.Background())
	now := time.Date(2026, 6, 6, 10, 30, 0, 0, time.UTC)
	def := &agent.Definition{
		ID: "daily", Enabled: true, Trigger: agent.TriggerCron,
		Schedule: &agent.Schedule{
			Cron:                "0 10 * * *",
			RunMissedOnStartup:  true,
			MissedStartupWindow: "not-a-duration",
		},
	}
	got, ok := s.missedCronFire(def, now)
	if !ok {
		t.Fatal("invalid window must fall back to the 24h default, not disable catch-up")
	}
	if want := time.Date(2026, 6, 6, 10, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("missed = %s, want %s", got, want)
	}
}

func TestMissedCronFire_NegativeWindow_DefaultsTo24h(t *testing.T) {
	s := New(nil, nil, zap.NewNop(), context.Background())
	now := time.Date(2026, 6, 6, 10, 30, 0, 0, time.UTC)
	def := &agent.Definition{
		ID: "daily", Enabled: true, Trigger: agent.TriggerCron,
		Schedule: &agent.Schedule{
			Cron:                "0 10 * * *",
			RunMissedOnStartup:  true,
			MissedStartupWindow: "-3h",
		},
	}
	if _, ok := s.missedCronFire(def, now); !ok {
		t.Fatal("negative window must fall back to the 24h default")
	}
}

func TestMissedCronFire_DisabledAgent_NoCatchUp(t *testing.T) {
	s := New(nil, nil, zap.NewNop(), context.Background())
	now := time.Date(2026, 6, 6, 10, 30, 0, 0, time.UTC)
	def := &agent.Definition{
		ID: "daily", Enabled: false, Trigger: agent.TriggerCron,
		Schedule: &agent.Schedule{Cron: "0 10 * * *", RunMissedOnStartup: true},
	}
	if _, ok := s.missedCronFire(def, now); ok {
		t.Fatal("disabled agents must never catch up")
	}
}

func TestMissedCronFire_CorruptStateFile_StillCatchesUp(t *testing.T) {
	// A corrupt state file must degrade to "no recorded completions"
	// (catch-up still fires) rather than crashing or silently disabling.
	dir := t.TempDir()
	path := filepath.Join(dir, "scheduler-state.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := New(nil, nil, zap.NewNop(), context.Background())
	s.SetStatePath(path)
	now := time.Date(2026, 6, 6, 10, 30, 0, 0, time.UTC)
	def := &agent.Definition{
		ID: "daily", Enabled: true, Trigger: agent.TriggerCron,
		Schedule: &agent.Schedule{
			Cron: "0 10 * * *", RunMissedOnStartup: true, MissedStartupWindow: "24h",
		},
	}
	if _, ok := s.missedCronFire(def, now); !ok {
		t.Fatal("corrupt state must not disable catch-up")
	}
}

func TestMarkScheduleCompleted_NeverRegresses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	s := New(nil, nil, zap.NewNop(), context.Background())
	s.SetStatePath(path)

	newer := time.Date(2026, 6, 6, 10, 0, 0, 0, time.UTC)
	older := time.Date(2026, 6, 5, 10, 0, 0, 0, time.UTC)
	s.markScheduleCompleted("daily", newer)
	s.markScheduleCompleted("daily", older) // out-of-order completion (catch-up race)

	s.stateMu.Lock()
	got := s.state.LastCompleted["daily"]
	s.stateMu.Unlock()
	if !got.Equal(newer) {
		t.Fatalf("LastCompleted regressed to %s, want %s", got, newer)
	}
}

func TestMarkScheduleCompleted_ZeroTimeUsesNow(t *testing.T) {
	s := New(nil, nil, zap.NewNop(), context.Background())
	s.markScheduleCompleted("daily", time.Time{})
	s.stateMu.Lock()
	got := s.state.LastCompleted["daily"]
	s.stateMu.Unlock()
	if got.IsZero() || time.Since(got) > time.Minute {
		t.Fatalf("zero completedAt should record ~now, got %s", got)
	}
}

func TestRestartSequence_CatchUpRunsExactlyOnce(t *testing.T) {
	// Simulated restart sequence: miss a fire, catch it up, mark complete,
	// restart again — the second boot must NOT re-run the same fire.
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	now := time.Date(2026, 6, 6, 10, 30, 0, 0, time.UTC)
	def := &agent.Definition{
		ID: "daily", Enabled: true, Trigger: agent.TriggerCron,
		Schedule: &agent.Schedule{
			Cron: "0 10 * * *", RunMissedOnStartup: true, MissedStartupWindow: "24h",
		},
	}

	// Boot 1: detects the missed 10:00 fire.
	boot1 := New(nil, nil, zap.NewNop(), context.Background())
	boot1.SetStatePath(path)
	missed, ok := boot1.missedCronFire(def, now)
	if !ok {
		t.Fatal("boot 1 should detect the missed fire")
	}
	// The run completes (what fireAt does on success for cron_missed_startup).
	boot1.markScheduleCompleted(def.ID, missed)

	// Boot 2 (same day, later): nothing to catch up.
	boot2 := New(nil, nil, zap.NewNop(), context.Background())
	boot2.SetStatePath(path)
	if _, ok := boot2.missedCronFire(def, now.Add(10*time.Minute)); ok {
		t.Fatal("boot 2 re-detected an already-completed fire — duplicate run")
	}

	// Boot 3 the next day after the 10:00 fire was missed again: catch up again.
	nextDay := now.Add(24 * time.Hour)
	boot3 := New(nil, nil, zap.NewNop(), context.Background())
	boot3.SetStatePath(path)
	missed3, ok := boot3.missedCronFire(def, nextDay)
	if !ok {
		t.Fatal("boot 3 should detect the next day's missed fire")
	}
	if want := time.Date(2026, 6, 7, 10, 0, 0, 0, time.UTC); !missed3.Equal(want) {
		t.Fatalf("boot 3 missed = %s, want %s", missed3, want)
	}
}

func TestEntries_SurfaceCatchUpSettings(t *testing.T) {
	// The admin API / Schedule GUI needs to show which agents catch up
	// missed runs and with what window (Story 12 UI copy).
	dir := t.TempDir()
	writeSchedAgent(t, dir, "daily", `
id: daily
name: Daily
enabled: true
trigger: cron
schedule:
  cron: "0 10 * * *"
  run_missed_on_startup: true
  missed_startup_window: 6h
`)
	loader := newSchedLoader(t, dir)
	s := New(nil, loader, zap.NewNop(), context.Background())
	def := loader.Get("daily")
	if def == nil {
		t.Fatal("loader did not load the agent")
	}
	if err := s.RegisterAgent(def); err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}

	entries := s.Entries()
	if len(entries) != 1 {
		t.Fatalf("entries = %+v", entries)
	}
	e := entries[0]
	if !e.CatchUp {
		t.Fatal("CatchUp not surfaced")
	}
	if e.CatchUpWindow != "6h" {
		t.Fatalf("CatchUpWindow = %q, want 6h", e.CatchUpWindow)
	}
}

func TestEntries_NoCatchUp_FieldsOmitted(t *testing.T) {
	dir := t.TempDir()
	writeSchedAgent(t, dir, "plain", `
id: plain
name: Plain
enabled: true
trigger: cron
schedule:
  cron: "0 10 * * *"
`)
	loader := newSchedLoader(t, dir)
	s := New(nil, loader, zap.NewNop(), context.Background())
	if err := s.RegisterAgent(loader.Get("plain")); err != nil {
		t.Fatal(err)
	}
	entries := s.Entries()
	if len(entries) != 1 || entries[0].CatchUp {
		t.Fatalf("entries = %+v", entries)
	}
}

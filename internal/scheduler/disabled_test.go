package scheduler

// "Disabled" has to mean disabled at FIRE time.
//
// RegisterAgent refuses an agent with Enabled=false, and DeregisterAgent drops
// its cron entry, so the only thing holding the invariant was that every writer
// of Enabled remembered to call one of them. Studio's Save did not: it wrote
// enabled: false to SOUL.yaml, replaced the loader's copy, and left the cron
// entry in place. The agent read as OFF everywhere the operator could look —
// the list, the file, the API — and kept firing on schedule.
//
// The readiness gate's own header already assumed this was handled: "the
// scheduler would fire ANY ENABLED agent whose cron matched". It would fire a
// disabled one too.

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/pkg/agent"
)

// countingGate records how far a fire got. The readiness gate is the first
// thing fireAt consults after the enabled check, so reaching it proves the
// enabled check let the fire through. It then blocks, which stops the fire
// before the engine — these tests are about the gate ordering, not execution.
type countingGate struct {
	mu     sync.Mutex
	called int
}

func (g *countingGate) ScheduleReadiness(agentID string) (ReadinessVerdict, bool) {
	g.mu.Lock()
	g.called++
	g.mu.Unlock()
	return ReadinessVerdict{Blocked: true, Summary: "held by the test gate"}, true
}

func (g *countingGate) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.called
}

func schedulerWithAgent(t *testing.T, def *agent.Definition) (*Scheduler, *runtime.Loader) {
	t.Helper()
	dir := t.TempDir()
	loader := runtime.NewLoader([]string{dir})
	if err := loader.Upsert(filepath.Join(dir), def); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	return New(nil, loader, zap.NewNop(), context.Background()), loader
}

// hasEntry reports whether the cron table still holds this agent.
func (s *Scheduler) hasEntry(agentID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.entries[keyFor("", agentID)]
	return ok
}

func digestAgent(enabled bool) *agent.Definition {
	return &agent.Definition{
		ID:       "weekday-digest",
		Name:     "Weekday Digest",
		Enabled:  enabled,
		Trigger:  agent.TriggerCron,
		Schedule: &agent.Schedule{Cron: "0 7 * * 1-5"},
		LLM:      agent.LLMConfig{Provider: "ollama", Model: "llama3"},
	}
}

func TestFire_RefusesAnAgentThatWasDisabledAfterRegistration(t *testing.T) {
	s, loader := schedulerWithAgent(t, digestAgent(true))
	gate := &countingGate{}
	s.SetReadinessGate(gate)

	if err := s.RegisterAgent(loader.Get("weekday-digest")); err != nil {
		t.Fatalf("register: %v", err)
	}

	// What a Studio save does: write enabled: false, leave the cron table alone.
	def := loader.Get("weekday-digest")
	def.Enabled = false
	if err := loader.Upsert(t.TempDir(), def); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	s.fireAt(keyFor("", "weekday-digest"), "cron", time.Now().UTC())

	if gate.count() != 0 {
		t.Fatal("a disabled agent was carried into the run path — it would have executed on schedule")
	}
}

// The wasted tick must be the last one: leaving the entry in place would burn a
// lookup on every cron match forever.
func TestFire_DropsTheStaleCronEntryOnTheWayOut(t *testing.T) {
	s, loader := schedulerWithAgent(t, digestAgent(true))
	// Installed so that a regression stops at the gate instead of running into a
	// nil engine: this test must fail with its own message, not a panic.
	s.SetReadinessGate(&countingGate{})
	if err := s.RegisterAgent(loader.Get("weekday-digest")); err != nil {
		t.Fatalf("register: %v", err)
	}
	if !s.hasEntry("weekday-digest") {
		t.Fatal("the agent was never registered, so this test proves nothing")
	}

	def := loader.Get("weekday-digest")
	def.Enabled = false
	if err := loader.Upsert(t.TempDir(), def); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	s.fireAt(keyFor("", "weekday-digest"), "cron", time.Now().UTC())

	if s.hasEntry("weekday-digest") {
		t.Error("the cron entry survived, so the disabled agent keeps costing a tick on every match")
	}
}

// An enabled agent must still reach the rest of the pipeline. A check that
// refuses everything would pass the test above and break every schedule.
func TestFire_StillRunsAnEnabledAgent(t *testing.T) {
	s, loader := schedulerWithAgent(t, digestAgent(true))
	gate := &countingGate{}
	s.SetReadinessGate(gate)

	if err := s.RegisterAgent(loader.Get("weekday-digest")); err != nil {
		t.Fatalf("register: %v", err)
	}
	s.fireAt(keyFor("", "weekday-digest"), "cron", time.Now().UTC())

	if gate.count() != 1 {
		t.Fatalf("an enabled agent did not reach the readiness gate (called %d times)", gate.count())
	}
}

// An agent the loader has never heard of is not "disabled" — the existing
// missing-definition path owns that, and it logs an error rather than a warning.
// Short-circuiting here would swallow a real misconfiguration.
func TestFire_DoesNotTreatAnUnknownAgentAsDisabled(t *testing.T) {
	s, _ := schedulerWithAgent(t, digestAgent(true))
	gate := &countingGate{}
	s.SetReadinessGate(gate)

	s.fireAt(keyFor("", "no-such-agent"), "cron", time.Now().UTC())

	if gate.count() != 1 {
		t.Fatal("an unknown agent was reported as disabled instead of missing")
	}
}

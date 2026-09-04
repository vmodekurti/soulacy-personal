package scheduler

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/channels"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
	"go.uber.org/zap"
)

func TestRenderScheduledOutput(t *testing.T) {
	def := &agent.Definition{ID: "daily", Name: "Daily Brief"}
	got := RenderScheduledOutput(
		"agent={agent_id} name={agent_name} trigger={trigger} reply={reply}",
		def,
		"hello",
		"cron",
	)

	for _, want := range []string{
		"agent=daily",
		"name=Daily Brief",
		"trigger=cron",
		"reply=hello",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered output %q missing %q", got, want)
		}
	}

	if got := RenderScheduledOutput("", def, "raw reply", "cron"); got != "raw reply" {
		t.Fatalf("empty template = %q, want raw reply", got)
	}
}

func TestSendScheduledOutputRoutesToConfiguredAdapter(t *testing.T) {
	reg := channels.NewRegistry(1)
	adapter := &captureAdapter{id: "telegram-daily"}
	reg.Register(adapter)

	s := New(nil, nil, zap.NewNop(), context.Background())
	s.SetChannelRegistry(reg)
	def := &agent.Definition{
		ID:   "daily",
		Name: "Daily Brief",
		Schedule: &agent.Schedule{Output: &agent.ScheduleOutput{
			Channel:  "telegram-daily",
			To:       "123456",
			BotName:  "Daily Bot",
			Template: "[{agent_id}/{trigger}] {reply}",
		}},
	}
	source := message.Message{SessionID: "sched-daily-1"}

	s.sendScheduledOutput(context.Background(), def, source, "brief text", "cron", nil)

	if len(adapter.sent) != 1 {
		t.Fatalf("sent count = %d, want 1", len(adapter.sent))
	}
	msg := adapter.sent[0]
	if msg.Channel != "telegram-daily" {
		t.Fatalf("Channel = %q, want telegram-daily", msg.Channel)
	}
	if msg.ThreadID != "123456" {
		t.Fatalf("ThreadID = %q, want destination", msg.ThreadID)
	}
	if msg.SessionID != source.SessionID {
		t.Fatalf("SessionID = %q, want %q", msg.SessionID, source.SessionID)
	}
	if msg.Metadata["bot_name"] != "Daily Bot" || msg.Metadata["trigger"] != "cron" {
		t.Fatalf("metadata = %v", msg.Metadata)
	}
	if got := firstText(msg); got != "[daily/cron] brief text" {
		t.Fatalf("text = %q, want templated output", got)
	}
}

func TestSendScheduledOutputNoopsWhenIncomplete(t *testing.T) {
	reg := channels.NewRegistry(1)
	adapter := &captureAdapter{id: "telegram"}
	reg.Register(adapter)
	s := New(nil, nil, zap.NewNop(), context.Background())
	s.SetChannelRegistry(reg)

	cases := []struct {
		name      string
		def       *agent.Definition
		replyText string
	}{
		{"nil definition", nil, "reply"},
		{"nil schedule", &agent.Definition{ID: "a"}, "reply"},
		{"nil output", &agent.Definition{ID: "a", Schedule: &agent.Schedule{}}, "reply"},
		{
			name: "empty reply",
			def: &agent.Definition{ID: "a", Schedule: &agent.Schedule{Output: &agent.ScheduleOutput{
				Channel: "telegram",
				To:      "123",
			}}},
			replyText: "",
		},
		{
			name: "missing channel",
			def: &agent.Definition{ID: "a", Schedule: &agent.Schedule{Output: &agent.ScheduleOutput{
				To: "123",
			}}},
			replyText: "reply",
		},
		{
			name: "missing destination",
			def: &agent.Definition{ID: "a", Schedule: &agent.Schedule{Output: &agent.ScheduleOutput{
				Channel: "telegram",
			}}},
			replyText: "reply",
		},
		{
			name: "unregistered adapter",
			def: &agent.Definition{ID: "a", Schedule: &agent.Schedule{Output: &agent.ScheduleOutput{
				Channel: "slack",
				To:      "C123",
			}}},
			replyText: "reply",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			adapter.sent = nil
			s.sendScheduledOutput(context.Background(), tc.def, message.Message{}, tc.replyText, "cron", nil)
			if len(adapter.sent) != 0 {
				t.Fatalf("sent count = %d, want 0", len(adapter.sent))
			}
		})
	}
}

func TestSendScheduledOutputUsesDefaultChannelDestination(t *testing.T) {
	reg := channels.NewRegistry(1)
	adapter := &captureAdapter{id: "telegram"}
	reg.Register(adapter)
	s := New(nil, nil, zap.NewNop(), context.Background())
	s.SetChannelRegistry(reg)
	s.SetDefaultOutputs(map[string]agent.ScheduleOutput{
		"telegram": {
			Channel:  "telegram",
			To:       "123456",
			BotName:  "Shared Telegram",
			Template: "shared {reply}",
		},
	})
	def := &agent.Definition{
		ID:       "deal-hunter",
		Name:     "Deal Hunter",
		Channels: []string{"telegram"},
	}

	s.sendScheduledOutput(context.Background(), def, message.Message{SessionID: "s1"}, "deal found", "cron", nil)

	if len(adapter.sent) != 1 {
		t.Fatalf("sent count = %d, want 1", len(adapter.sent))
	}
	msg := adapter.sent[0]
	if msg.Channel != "telegram" || msg.ThreadID != "123456" {
		t.Fatalf("message route = %s/%s, want telegram/123456", msg.Channel, msg.ThreadID)
	}
	if got := firstText(msg); got != "shared deal found" {
		t.Fatalf("text = %q, want templated fallback", got)
	}
}

func TestDefaultOutputsFromChannelConfigReadsBotDefault(t *testing.T) {
	got := DefaultOutputsFromChannelConfig(map[string]map[string]any{
		"telegram": {
			"bots": []any{
				map[string]any{
					"bot_name":                "Daily Bot",
					"default_output_to":       "999",
					"default_output_template": "[{agent_id}] {reply}",
				},
			},
		},
	})

	out, ok := got["telegram"]
	if !ok {
		t.Fatal("missing canonical telegram default")
	}
	if out.Channel != "telegram" || out.To != "999" || out.BotName != "Daily Bot" {
		t.Fatalf("default = %+v, want telegram/999/Daily Bot", out)
	}
}

func TestRunningLockPreventsOverlapAndAllowsStaleReplacement(t *testing.T) {
	s := New(nil, nil, zap.NewNop(), context.Background())

	if !s.TryStartRun("agent") {
		t.Fatal("first TryStartRun should succeed")
	}
	if s.TryStartRun("agent") {
		t.Fatal("second TryStartRun should be blocked while running")
	}
	if !s.IsRunning("agent") {
		t.Fatal("agent should be running")
	}

	s.running["agent"] = time.Now().Add(-(maxRunDuration + time.Minute))
	if !s.TryStartRun("agent") {
		t.Fatal("stale running marker should be replaced")
	}
	if !s.IsRunning("agent") {
		t.Fatal("agent should be running after stale replacement")
	}

	s.FinishRun("agent")
	if s.IsRunning("agent") {
		t.Fatal("agent should not be running after FinishRun")
	}
}

type captureAdapter struct {
	id   string
	sent []message.Message
}

func (a *captureAdapter) ID() string { return a.id }
func (a *captureAdapter) Name() string {
	return "capture"
}
func (a *captureAdapter) Start(context.Context, chan<- message.Message) error { return nil }
func (a *captureAdapter) Send(_ context.Context, msg message.Message) error {
	a.sent = append(a.sent, msg)
	return nil
}
func (a *captureAdapter) Stop() error { return nil }
func (a *captureAdapter) Status() channels.AdapterStatus {
	return channels.AdapterStatus{Connected: true}
}

func firstText(msg message.Message) string {
	for _, part := range msg.Parts {
		if part.Type == message.ContentText {
			return part.Text
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// RegisterAgent / addCron / addOneShot / DeregisterAgent / Entries
// ---------------------------------------------------------------------------

// TestRegisterAgentNoopForNilSchedule verifies that an agent without a
// schedule is silently ignored.
func TestRegisterAgentNoopForNilSchedule(t *testing.T) {
	s := New(nil, nil, zap.NewNop(), context.Background())
	def := &agent.Definition{ID: "no-schedule", Enabled: true}
	if err := s.RegisterAgent(def); err != nil {
		t.Fatalf("nil schedule: %v", err)
	}
	s.mu.Lock()
	count := len(s.entries) + len(s.oneshot)
	s.mu.Unlock()
	if count != 0 {
		t.Errorf("expected 0 entries, got %d", count)
	}
}

// TestRegisterAgentNoopWhenDisabled verifies that a disabled agent is not
// scheduled even if it has a valid schedule block.
func TestRegisterAgentNoopWhenDisabled(t *testing.T) {
	s := New(nil, nil, zap.NewNop(), context.Background())
	def := &agent.Definition{
		ID: "disabled-cron", Enabled: false, Trigger: agent.TriggerCron,
		Schedule: &agent.Schedule{Cron: "0 9 * * *"},
	}
	if err := s.RegisterAgent(def); err != nil {
		t.Fatalf("disabled: %v", err)
	}
	s.mu.Lock()
	_, registered := s.entries["disabled-cron"]
	s.mu.Unlock()
	if registered {
		t.Error("disabled agent should not be registered")
	}
}

// TestAddCronRegistersEntry verifies that a valid cron expression is accepted
// and the agent appears in the internal entries map.
func TestAddCronRegistersEntry(t *testing.T) {
	s := New(nil, nil, zap.NewNop(), context.Background())
	def := &agent.Definition{
		ID: "daily-brief", Enabled: true, Trigger: agent.TriggerCron,
		Schedule: &agent.Schedule{Cron: "0 9 * * *"},
	}
	if err := s.RegisterAgent(def); err != nil {
		t.Fatalf("RegisterAgent cron: %v", err)
	}
	s.mu.Lock()
	_, registered := s.entries["daily-brief"]
	s.mu.Unlock()
	if !registered {
		t.Error("expected cron entry to be registered")
	}
}

func TestRegisterAgentSchedulesExplicitScheduleSurface(t *testing.T) {
	s := New(nil, nil, zap.NewNop(), context.Background())
	def := &agent.Definition{
		ID:       "multi-surface-digest",
		Enabled:  true,
		Trigger:  agent.TriggerChannel,
		Surfaces: []string{agent.SurfaceSchedule, agent.SurfaceChat, "slack"},
		Schedule: &agent.Schedule{Cron: "0 9 * * *"},
	}
	if err := s.RegisterAgent(def); err != nil {
		t.Fatalf("RegisterAgent multi-surface cron: %v", err)
	}
	s.mu.Lock()
	_, registered := s.entries["multi-surface-digest"]
	s.mu.Unlock()
	if !registered {
		t.Error("explicit schedule surface with cron should register a cron entry")
	}
}

// TestAddCronRejectsInvalidExpression verifies that a malformed cron expression
// returns an error and does not add an entry.
func TestAddCronRejectsInvalidExpression(t *testing.T) {
	s := New(nil, nil, zap.NewNop(), context.Background())
	def := &agent.Definition{
		ID: "bad-cron", Enabled: true, Trigger: agent.TriggerCron,
		Schedule: &agent.Schedule{Cron: "not a cron expression"},
	}
	err := s.RegisterAgent(def)
	if err == nil {
		t.Fatal("expected error for invalid cron, got nil")
	}
	s.mu.Lock()
	_, registered := s.entries["bad-cron"]
	s.mu.Unlock()
	if registered {
		t.Error("invalid cron should not create an entry")
	}
}

// TestAddCronRejectsEmptyExpression verifies that an empty cron field errors.
func TestAddCronRejectsEmptyExpression(t *testing.T) {
	s := New(nil, nil, zap.NewNop(), context.Background())
	def := &agent.Definition{
		ID: "empty-cron", Enabled: true, Trigger: agent.TriggerCron,
		Schedule: &agent.Schedule{Cron: ""},
	}
	if err := s.RegisterAgent(def); err == nil {
		t.Fatal("expected error for empty cron expression, got nil")
	}
}

// TestAddCronReregistrationReplacesEntry verifies that registering the same
// agent a second time replaces the old entry rather than duplicating it.
func TestAddCronReregistrationReplacesEntry(t *testing.T) {
	s := New(nil, nil, zap.NewNop(), context.Background())
	def := &agent.Definition{
		ID: "re-register", Enabled: true, Trigger: agent.TriggerCron,
		Schedule: &agent.Schedule{Cron: "0 9 * * *"},
	}
	_ = s.RegisterAgent(def)
	_ = s.RegisterAgent(def)

	s.mu.Lock()
	n := len(s.entries)
	s.mu.Unlock()
	if n != 1 {
		t.Errorf("after re-registration: entry count = %d, want 1", n)
	}
}

// TestAddOneShotRegistersGoroutine verifies that a future-time one-shot trigger
// registers its cancel function in the oneshot map.
func TestAddOneShotRegistersGoroutine(t *testing.T) {
	s := New(nil, nil, zap.NewNop(), context.Background())
	def := &agent.Definition{
		ID: "future-shot", Enabled: true, Trigger: agent.TriggerOneShot,
		Schedule: &agent.Schedule{At: time.Now().Add(time.Hour)},
	}
	if err := s.RegisterAgent(def); err != nil {
		t.Fatalf("RegisterAgent oneshot: %v", err)
	}
	s.mu.Lock()
	_, registered := s.oneshot["future-shot"]
	s.mu.Unlock()
	if !registered {
		t.Error("expected oneshot goroutine to be registered")
	}
}

func TestRegisterAgentSchedulesExplicitOneShotSurface(t *testing.T) {
	s := New(nil, nil, zap.NewNop(), context.Background())
	def := &agent.Definition{
		ID:       "multi-surface-shot",
		Enabled:  true,
		Trigger:  agent.TriggerInternal,
		Surfaces: []string{agent.SurfaceSchedule, agent.SurfaceChat},
		Schedule: &agent.Schedule{At: time.Now().Add(time.Hour)},
	}
	if err := s.RegisterAgent(def); err != nil {
		t.Fatalf("RegisterAgent multi-surface oneshot: %v", err)
	}
	s.mu.Lock()
	_, registered := s.oneshot["multi-surface-shot"]
	s.mu.Unlock()
	if !registered {
		t.Error("explicit schedule surface with schedule.at should register a one-shot")
	}
}

// TestAddOneShotRejectsZeroTime verifies that a zero At time returns an error.
func TestAddOneShotRejectsZeroTime(t *testing.T) {
	s := New(nil, nil, zap.NewNop(), context.Background())
	def := &agent.Definition{
		ID: "zero-shot", Enabled: true, Trigger: agent.TriggerOneShot,
		Schedule: &agent.Schedule{At: time.Time{}},
	}
	if err := s.RegisterAgent(def); err == nil {
		t.Fatal("expected error for zero one-shot time, got nil")
	}
}

// TestAddOneShotReregistrationCancelsPrevious verifies that registering the
// same one-shot agent a second time cancels the previous goroutine.
func TestAddOneShotReregistrationCancelsPrevious(t *testing.T) {
	s := New(nil, nil, zap.NewNop(), context.Background())
	def := &agent.Definition{
		ID: "re-shot", Enabled: true, Trigger: agent.TriggerOneShot,
		Schedule: &agent.Schedule{At: time.Now().Add(time.Hour)},
	}
	_ = s.RegisterAgent(def)
	_ = s.RegisterAgent(def)

	// Still only one entry in the oneshot map.
	s.mu.Lock()
	n := len(s.oneshot)
	s.mu.Unlock()
	if n != 1 {
		t.Errorf("after re-registration: oneshot count = %d, want 1", n)
	}
}

// TestDeregisterAgentRemovesCronEntry verifies that DeregisterAgent cleans
// up the cron entry.
func TestDeregisterAgentRemovesCronEntry(t *testing.T) {
	s := New(nil, nil, zap.NewNop(), context.Background())
	def := &agent.Definition{
		ID: "to-remove", Enabled: true, Trigger: agent.TriggerCron,
		Schedule: &agent.Schedule{Cron: "0 9 * * *"},
	}
	_ = s.RegisterAgent(def)
	s.DeregisterAgent("to-remove")

	s.mu.Lock()
	_, stillThere := s.entries["to-remove"]
	s.mu.Unlock()
	if stillThere {
		t.Error("expected entry to be removed after DeregisterAgent")
	}
}

// TestDeregisterAgentCancelsOneShot verifies that DeregisterAgent cancels
// a pending one-shot goroutine and removes it from the map.
func TestDeregisterAgentCancelsOneShot(t *testing.T) {
	s := New(nil, nil, zap.NewNop(), context.Background())
	def := &agent.Definition{
		ID: "cancel-shot", Enabled: true, Trigger: agent.TriggerOneShot,
		Schedule: &agent.Schedule{At: time.Now().Add(time.Hour)},
	}
	_ = s.RegisterAgent(def)
	s.DeregisterAgent("cancel-shot")

	s.mu.Lock()
	_, stillThere := s.oneshot["cancel-shot"]
	s.mu.Unlock()
	if stillThere {
		t.Error("expected oneshot to be removed after DeregisterAgent")
	}
}

// TestDeregisterAgentIsIdempotent verifies DeregisterAgent on a non-existent
// agent is a safe no-op.
func TestDeregisterAgentIsIdempotent(t *testing.T) {
	s := New(nil, nil, zap.NewNop(), context.Background())
	s.DeregisterAgent("ghost")
	s.DeregisterAgent("ghost")
}

// TestEntriesSnapshotsCronAndOneShot verifies Entries returns one entry per
// registered agent (cron + oneshot combined).
func TestEntriesSnapshotsCronAndOneShot(t *testing.T) {
	s := New(nil, nil, zap.NewNop(), context.Background())
	_ = s.RegisterAgent(&agent.Definition{
		ID: "cron-ag", Enabled: true, Trigger: agent.TriggerCron,
		Schedule: &agent.Schedule{Cron: "0 9 * * *"},
	})
	_ = s.RegisterAgent(&agent.Definition{
		ID: "shot-ag", Enabled: true, Trigger: agent.TriggerOneShot,
		Schedule: &agent.Schedule{At: time.Now().Add(time.Hour)},
	})
	entries := s.Entries()
	if len(entries) != 2 {
		t.Fatalf("Entries count = %d, want 2", len(entries))
	}
	ids := map[string]bool{}
	for _, e := range entries {
		ids[e.AgentID] = true
	}
	if !ids["cron-ag"] || !ids["shot-ag"] {
		t.Errorf("Entries missing expected agents: %v", ids)
	}
}

// TestStartStop verifies that Start/Stop does not panic and cleans up properly.
func TestStartStop(t *testing.T) {
	s := New(nil, nil, zap.NewNop(), context.Background())
	s.Start()
	s.Stop()
}

// TestRunningSnapshot returns active runs and excludes stale ones.
func TestRunningSnapshot(t *testing.T) {
	s := New(nil, nil, zap.NewNop(), context.Background())
	s.TryStartRun("active-agent")
	// Insert a stale entry directly.
	s.runMu.Lock()
	s.running["stale-agent"] = time.Now().Add(-2 * maxRunDuration)
	s.runMu.Unlock()

	snap := s.RunningSnapshot()
	if _, ok := snap["active-agent"]; !ok {
		t.Error("active-agent should be in snapshot")
	}
	if _, ok := snap["stale-agent"]; ok {
		t.Error("stale-agent should be excluded from snapshot")
	}
}

func TestMissedCronFireRequiresOptIn(t *testing.T) {
	s := New(nil, nil, zap.NewNop(), context.Background())
	now := time.Date(2026, 6, 6, 10, 30, 0, 0, time.UTC)
	def := &agent.Definition{
		ID: "daily", Enabled: true, Trigger: agent.TriggerCron,
		Schedule: &agent.Schedule{Cron: "0 10 * * *"},
	}

	if _, ok := s.missedCronFire(def, now); ok {
		t.Fatal("missed cron should not run without run_missed_on_startup")
	}
}

func TestMissedCronFireFindsLatestMissedWithinWindow(t *testing.T) {
	s := New(nil, nil, zap.NewNop(), context.Background())
	now := time.Date(2026, 6, 6, 10, 30, 0, 0, time.UTC)
	def := &agent.Definition{
		ID: "daily", Enabled: true, Trigger: agent.TriggerCron,
		Schedule: &agent.Schedule{
			Cron:                "0 10 * * *",
			RunMissedOnStartup:  true,
			MissedStartupWindow: "24h",
		},
	}

	got, ok := s.missedCronFire(def, now)
	if !ok {
		t.Fatal("expected missed cron")
	}
	want := time.Date(2026, 6, 6, 10, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("missed cron = %s, want %s", got, want)
	}
}

func TestMissedCronFireSkipsAlreadyCompletedFire(t *testing.T) {
	s := New(nil, nil, zap.NewNop(), context.Background())
	now := time.Date(2026, 6, 6, 10, 30, 0, 0, time.UTC)
	s.state.LastCompleted["daily"] = time.Date(2026, 6, 6, 10, 0, 0, 0, time.UTC)
	def := &agent.Definition{
		ID: "daily", Enabled: true, Trigger: agent.TriggerCron,
		Schedule: &agent.Schedule{
			Cron:                "0 10 * * *",
			RunMissedOnStartup:  true,
			MissedStartupWindow: "24h",
		},
	}

	if _, ok := s.missedCronFire(def, now); ok {
		t.Fatal("already completed scheduled fire should not catch up")
	}
}

func TestMissedCronFireHonorsWindow(t *testing.T) {
	s := New(nil, nil, zap.NewNop(), context.Background())
	now := time.Date(2026, 6, 6, 10, 30, 0, 0, time.UTC)
	def := &agent.Definition{
		ID: "daily", Enabled: true, Trigger: agent.TriggerCron,
		Schedule: &agent.Schedule{
			Cron:                "0 10 * * *",
			RunMissedOnStartup:  true,
			MissedStartupWindow: "15m",
		},
	}

	if _, ok := s.missedCronFire(def, now); ok {
		t.Fatal("missed cron outside catch-up window should not run")
	}
}

func TestScheduleStatePersistsCompletedCron(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "scheduler-state.json")
	s := New(nil, nil, zap.NewNop(), context.Background())
	s.SetStatePath(path)
	completedAt := time.Date(2026, 6, 6, 10, 0, 0, 0, time.UTC)

	s.markScheduleCompleted("daily", completedAt)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if !strings.Contains(string(data), "daily") || !strings.Contains(string(data), "2026-06-06T10:00:00Z") {
		t.Fatalf("state file missing completed run: %s", data)
	}

	reloaded := New(nil, nil, zap.NewNop(), context.Background())
	reloaded.SetStatePath(path)
	def := &agent.Definition{
		ID: "daily", Enabled: true, Trigger: agent.TriggerCron,
		Schedule: &agent.Schedule{
			Cron:                "0 10 * * *",
			RunMissedOnStartup:  true,
			MissedStartupWindow: "24h",
		},
	}
	now := time.Date(2026, 6, 6, 10, 30, 0, 0, time.UTC)
	if _, ok := reloaded.missedCronFire(def, now); ok {
		t.Fatal("persisted completed run should suppress startup catch-up")
	}
}

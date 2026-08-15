// Package scheduler runs agents on a time-based schedule.
// Two trigger types are supported:
//   - Cron: agents triggered on recurring schedules (standard 5-field cron expressions)
//   - OneShot: agents triggered once at a specific UTC time
//
// The Scheduler integrates with the Engine: when a trigger fires, it synthesises a
// message.Message and passes it to engine.Handle(), just like a channel message would.
// This means scheduled agents have full access to memory, tools, and LLM routing.
package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/channels"
	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/internal/webpush"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

// Scheduler manages cron and one-shot agent triggers.
type Scheduler struct {
	cron     *cron.Cron
	engine   *runtime.Engine
	loader   *runtime.Loader // for per-agent run_timeout lookup
	channels *channels.Registry
	log      *zap.Logger
	mu       sync.Mutex
	entries  map[string]cron.EntryID // agentID → cron entry
	oneshot  map[string]context.CancelFunc

	// appCtx is the gateway's app-wide context. Every fired run derives its
	// own context from this one so SIGTERM cancellation propagates through
	// engine.Handle → provider HTTP → tool subprocess.
	// (PRODUCTION_AUDIT → HIGH/Concurrency)
	appCtx context.Context

	runMu   sync.Mutex
	running map[string]time.Time // agentID → run start time (currently executing)

	stateMu   sync.Mutex
	statePath string
	state     scheduleState

	// failMu guards failCounts. consecutiveFailLimit is the number of back-to-back
	// failed fires after which a cron agent is auto-disabled (Story 2 / S7.2) so a
	// permanently-broken agent (bad model, dead provider) stops firing on a loop
	// nobody is watching. A successful run resets the counter. <=0 disables the
	// feature.
	failMu               sync.Mutex
	failCounts           map[string]int
	consecutiveFailLimit int

	defaultMu      sync.RWMutex
	defaultOutputs map[string]agent.ScheduleOutput

	// backfillMu guards backfills. lastBackfills records the most recent
	// startup-catch-up fire per agent since the process started; the GUI's
	// Schedule page renders it as an "Auto-replayed on Jul 15 03:04" chip so
	// operators aren't blindsided by an out-of-schedule run after a restart.
	// (E4b — Cohort E Schedule failure handling.)
	backfillMu    sync.RWMutex
	lastBackfills map[string]MissedBackfill

	// sink, when set, receives schedule telemetry events for delivery, failures,
	// and auto-disable decisions so scheduled work is visible in Activity and
	// never silently vanishes. Wired to the gateway EventHub.
	sink EventSink

	// gateMu guards gate and blocks. gate, when set, is consulted before every
	// fire and can refuse to run an agent that is not certified for scheduled
	// execution (ST-16); blocks retains the most recent refusal per agent so the
	// GUI can explain why a schedule didn't fire. A nil gate preserves the
	// ungated behaviour exactly — see readiness.go.
	gateMu sync.RWMutex
	gate   ReadinessGate
	blocks map[string]ScheduleBlock

	// principal is the verified service identity for scheduled work. It is set
	// once during startup; a zero value preserves embedded/test compatibility.
	principal        runtime.Principal
	requirePrincipal bool
}

// EventSink is the minimal event surface the scheduler needs to record delivery
// outcomes. Satisfied by *gateway.EventHub.
type EventSink interface {
	Emit(message.Event)
}

// SetEventSink wires an event sink so scheduled-run outcomes are recorded.
// Safe to call once at startup.
func (s *Scheduler) SetEventSink(sink EventSink) {
	s.mu.Lock()
	s.sink = sink
	s.mu.Unlock()
}

// Delivery outcome reason codes for schedule.output events.
const (
	deliveryDelivered      = "delivered"
	deliveryViaFallback    = "delivered_via_fallback"
	deliveryNoOutput       = "no_output_configured"
	deliveryIncomplete     = "incomplete_output"
	deliveryNoRegistry     = "channel_registry_unavailable"
	deliveryAdapterUnknown = "adapter_not_registered"
	deliverySendFailed     = "send_failed"
)

type scheduleState struct {
	LastCompleted map[string]time.Time `json:"last_completed,omitempty"`
}

// cronParser accepts standard 5-field cron expressions ("0 7 * * *") AND
// optional-seconds 6-field expressions ("*/30 * * * * *") plus @descriptors
// (@daily, @hourly). Without SecondOptional, WithSeconds would reject the
// 5-field expressions the GUI and example agents use, so nothing would schedule.
//
// NOTE: internal/agentvalidate/validate.go maintains a parser with the same
// flag set so Save-time pre-validation gives the same verdict as scheduler
// registration. If you change this line, change that one too.
var cronParser = cron.NewParser(
	cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
)

// New creates a new Scheduler. Call Start() to begin processing.
//
// appCtx is the gateway's lifetime context — fired runs derive from it so
// the gateway's SIGTERM handler cancels them all. Pass context.Background()
// only in tests.
func New(engine *runtime.Engine, loader *runtime.Loader, log *zap.Logger, appCtx context.Context) *Scheduler {
	if appCtx == nil {
		appCtx = context.Background()
	}
	return &Scheduler{
		cron:                 cron.New(cron.WithParser(cronParser)),
		engine:               engine,
		loader:               loader,
		log:                  log,
		appCtx:               appCtx,
		entries:              make(map[string]cron.EntryID),
		oneshot:              make(map[string]context.CancelFunc),
		running:              make(map[string]time.Time),
		state:                scheduleState{LastCompleted: make(map[string]time.Time)},
		failCounts:           make(map[string]int),
		defaultOutputs:       make(map[string]agent.ScheduleOutput),
		lastBackfills:        make(map[string]MissedBackfill),
		blocks:               make(map[string]ScheduleBlock),
		consecutiveFailLimit: 10, // default; override with SetConsecutiveFailLimit
	}
}

// SetDefaultOutputs configures shared scheduled-output destinations keyed by
// channel adapter id. Agents may still override with schedule.output; these
// defaults are only used when an agent names an output channel but omits the
// destination.
func (s *Scheduler) SetDefaultOutputs(outputs map[string]agent.ScheduleOutput) {
	s.defaultMu.Lock()
	defer s.defaultMu.Unlock()
	s.defaultOutputs = make(map[string]agent.ScheduleOutput, len(outputs))
	for id, out := range outputs {
		id = strings.TrimSpace(id)
		out.Channel = strings.TrimSpace(out.Channel)
		out.To = strings.TrimSpace(out.To)
		out.BotName = strings.TrimSpace(out.BotName)
		out.Template = strings.TrimSpace(out.Template)
		if id == "" || out.Channel == "" || out.To == "" {
			continue
		}
		s.defaultOutputs[id] = out
	}
}

// SetConsecutiveFailLimit configures how many back-to-back failed cron fires
// trigger an auto-disable. n<=0 turns the feature off. (Story 2 / S7.2)
func (s *Scheduler) SetConsecutiveFailLimit(n int) {
	s.failMu.Lock()
	defer s.failMu.Unlock()
	s.consecutiveFailLimit = n
}

// recordFireResult updates the consecutive-failure counter for a cron agent and
// auto-disables it once the limit is reached. It returns whether the agent was
// just disabled and the current consecutive failure count. A successful run
// (ok=true) resets the counter.
func (s *Scheduler) recordFireResult(agentID string, ok bool) (bool, int) {
	s.failMu.Lock()
	limit := s.consecutiveFailLimit
	if ok {
		delete(s.failCounts, agentID)
		s.failMu.Unlock()
		return false, 0
	}
	s.failCounts[agentID]++
	count := s.failCounts[agentID]
	s.failMu.Unlock()

	if limit <= 0 || count < limit {
		return false, count
	}
	// Quarantine the agent: stop it firing and disable it in memory so a fixed
	// SOUL.yaml (saved later) re-enables it via the normal reload path.
	s.DeregisterAgent(agentID)
	disabled := false
	if s.loader != nil {
		disabled = s.loader.SetEnabledInMemory(agentID, false)
	}
	s.failMu.Lock()
	delete(s.failCounts, agentID)
	s.failMu.Unlock()
	s.log.Error("cron agent auto-disabled after consecutive failures — fix its config and re-enable",
		zap.String("agent", agentID),
		zap.Int("consecutive_failures", count),
		zap.Bool("disabled", disabled))
	return true, count
}

// SetStatePath enables durable scheduler bookkeeping. The scheduler uses this
// to remember completed cron fires across host restarts so opt-in agents can
// catch up when a shutdown overlapped their scheduled time.
func (s *Scheduler) SetStatePath(path string) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	s.statePath = strings.TrimSpace(path)
}

// SetChannelRegistry enables scheduled runs to send successful replies to a
// configured channel output target. It is optional so scheduler tests and
// embedded uses can run without channel adapters.
func (s *Scheduler) SetChannelRegistry(reg *channels.Registry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.channels = reg
}

// SetPrincipal assigns the service/workspace identity inherited by every
// scheduled run. Multi-user hosts must call this before Start.
func (s *Scheduler) SetPrincipal(principal runtime.Principal) {
	s.principal = principal
}

// RequirePrincipal makes unscoped scheduled execution fail closed. Team and
// Scale enable this until their durable schedule records supply a workspace.
func (s *Scheduler) RequirePrincipal(required bool) { s.requirePrincipal = required }

// maxRunDuration is the safety cap on the run-lock staleness check. It needs
// to be at least as long as the slowest agent's run_timeout, otherwise a
// legitimately long run would be treated as "stale" and a concurrent run
// would start while it's still active. 1 hour covers the audio-generation
// chains; individual agents can still declare shorter run_timeout values.
const maxRunDuration = 1 * time.Hour

// TryStartRun marks an agent as running. Returns false if it is already running
// (within maxRunDuration), so callers can prevent overlapping/duplicate executions.
// A stale run past maxRunDuration is overwritten so the agent isn't locked forever.
func (s *Scheduler) TryStartRun(agentID string) bool {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	if started, ok := s.running[agentID]; ok && time.Since(started) < maxRunDuration {
		return false
	}
	s.running[agentID] = time.Now()
	return true
}

// FinishRun clears an agent's running state.
func (s *Scheduler) FinishRun(agentID string) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	delete(s.running, agentID)
}

// IsRunning reports whether an agent is currently executing (and not stale).
func (s *Scheduler) IsRunning(agentID string) bool {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	started, ok := s.running[agentID]
	return ok && time.Since(started) < maxRunDuration
}

// RunningSnapshot returns a copy of currently-running agents and their start
// times, excluding stale entries past maxRunDuration.
func (s *Scheduler) RunningSnapshot() map[string]time.Time {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	out := make(map[string]time.Time, len(s.running))
	for k, v := range s.running {
		if time.Since(v) < maxRunDuration {
			out[k] = v
		}
	}
	return out
}

// Start begins the cron daemon.
func (s *Scheduler) Start() {
	s.cron.Start()
	s.log.Info("scheduler started")
	s.runMissedOnStartup()
}

// Stop gracefully halts the cron daemon and cancels pending one-shots.
func (s *Scheduler) Stop() {
	ctx := s.cron.Stop()
	<-ctx.Done()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, cancel := range s.oneshot {
		cancel()
	}
	s.log.Info("scheduler stopped")
}

// RegisterAgent adds the agent's schedule to the scheduler.
// Call this after LoadAll() and whenever an agent definition is upserted.
func (s *Scheduler) RegisterAgent(def *agent.Definition) error {
	if def.Schedule == nil || !def.Enabled {
		return nil
	}
	switch scheduledKind(def) {
	case agent.TriggerCron:
		return s.addCron(def)
	case agent.TriggerOneShot:
		return s.addOneShot(def)
	}
	return nil
}

// scheduledKind returns the concrete schedule mechanism for an agent. Older
// agents declare it with trigger: cron/oneshot. Newer multi-surface agents may
// keep a different primary trigger and opt into scheduling with
// surfaces: [schedule] plus a schedule block.
func scheduledKind(def *agent.Definition) agent.TriggerKind {
	if def == nil || def.Schedule == nil {
		return ""
	}
	switch def.Trigger {
	case agent.TriggerCron, agent.TriggerOneShot:
		return def.Trigger
	}
	if !def.AppearsOn(agent.SurfaceSchedule) {
		return ""
	}
	if strings.TrimSpace(def.Schedule.Cron) != "" {
		return agent.TriggerCron
	}
	if !def.Schedule.At.IsZero() {
		return agent.TriggerOneShot
	}
	return ""
}

// DeregisterAgent removes a scheduled agent. Safe to call if not registered.
func (s *Scheduler) DeregisterAgent(agentID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.entries[agentID]; ok {
		s.cron.Remove(id)
		delete(s.entries, agentID)
	}
	if cancel, ok := s.oneshot[agentID]; ok {
		cancel()
		delete(s.oneshot, agentID)
	}
}

func (s *Scheduler) addCron(def *agent.Definition) error {
	if def.Schedule.Cron == "" {
		return fmt.Errorf("scheduler: cron expression is empty for agent %s", def.ID)
	}

	agentID := def.ID
	entryID, err := s.cron.AddFunc(def.Schedule.Cron, func() {
		s.fireAt(agentID, "cron", time.Now().UTC())
	})
	if err != nil {
		return fmt.Errorf("scheduler: invalid cron expression %q: %w", def.Schedule.Cron, err)
	}

	s.mu.Lock()
	// Remove previous entry if re-registering
	if old, ok := s.entries[agentID]; ok {
		s.cron.Remove(old)
	}
	s.entries[agentID] = entryID
	s.mu.Unlock()

	s.log.Info("cron agent registered",
		zap.String("agent", agentID),
		zap.String("expr", def.Schedule.Cron),
	)
	return nil
}

func (s *Scheduler) addOneShot(def *agent.Definition) error {
	if def.Schedule.At.IsZero() {
		return fmt.Errorf("scheduler: one-shot time is zero for agent %s", def.ID)
	}

	delay := time.Until(def.Schedule.At)
	if delay <= 0 {
		s.log.Warn("one-shot trigger is in the past, firing immediately",
			zap.String("agent", def.ID))
		delay = 0
	}

	// Derived from s.appCtx so SIGTERM cancels pending one-shots.
	// (PRODUCTION_AUDIT → HIGH/Concurrency)
	ctx, cancel := context.WithCancel(s.appCtx)
	s.mu.Lock()
	if oldCancel, ok := s.oneshot[def.ID]; ok {
		oldCancel()
	}
	s.oneshot[def.ID] = cancel
	s.mu.Unlock()

	agentID := def.ID
	go func() {
		select {
		case <-time.After(delay):
			s.fire(agentID, "oneshot")
			s.mu.Lock()
			delete(s.oneshot, agentID)
			s.mu.Unlock()
		case <-ctx.Done():
		}
	}()

	s.log.Info("one-shot agent scheduled",
		zap.String("agent", def.ID),
		zap.Time("at", def.Schedule.At),
	)
	return nil
}

// fire synthesises a trigger message and dispatches it to the engine.
func (s *Scheduler) fire(agentID, triggerType string) {
	s.fireAt(agentID, triggerType, time.Now().UTC())
}

// definition looks the agent up, tolerating a scheduler built without a loader
// (embedded uses and some tests) — "no opinion" rather than a panic.
func (s *Scheduler) definition(agentID string) *agent.Definition {
	if s.loader == nil {
		return nil
	}
	return s.loader.Get(agentID)
}

// fire synthesises a trigger message and dispatches it to the engine.
func (s *Scheduler) fireAt(agentID, triggerType string, scheduledAt time.Time) {
	// "Disabled" has to mean disabled AT FIRE TIME, not only at registration
	// time. RegisterAgent refuses a disabled agent, and DeregisterAgent drops
	// its cron entry — but that only holds while every writer of Enabled
	// remembers to tell the scheduler. One that forgets leaves a live cron entry
	// pointing at an agent the operator can see is off, and it keeps running on
	// a schedule nobody is watching. (A Studio save did exactly this: it wrote
	// enabled: false and never touched the cron table.)
	//
	// Checking here makes the invariant self-enforcing instead of a convention
	// spread across every call site. Re-read each tick, so re-enabling still
	// takes effect without a restart, and the stale entry is dropped on the way
	// out so this costs one wasted tick, not one per tick forever.
	if def := s.definition(agentID); def != nil && !def.Enabled {
		s.log.Warn("skipping scheduled run — agent is disabled",
			zap.String("agent", agentID), zap.String("trigger", triggerType))
		s.DeregisterAgent(agentID)
		return
	}

	// Readiness gate (ST-16). Checked before the run lock, before the definition
	// lookup, before any provider is dialled — because an agent that must not
	// run must not consume anything either. A nil gate is a no-op, so non-Studio
	// agents behave exactly as before. Re-checked on every tick, so fixing the
	// blocker unblocks the schedule without a restart.
	if s.blockedByReadiness(agentID, triggerType) {
		return
	}

	// Prevent overlapping runs: if a manual or previous scheduled run is still
	// executing, skip this fire rather than running the agent twice concurrently.
	if !s.TryStartRun(agentID) {
		s.log.Warn("skipping scheduled run — agent already running",
			zap.String("agent", agentID), zap.String("trigger", triggerType))
		return
	}
	defer s.FinishRun(agentID)

	s.log.Info("firing scheduled agent",
		zap.String("agent", agentID),
		zap.String("trigger", triggerType),
	)
	if s.requirePrincipal && s.principal.Subject == "" {
		s.log.Error("scheduled run blocked: verified workspace service principal is missing",
			zap.String("agent", agentID), zap.String("trigger", triggerType))
		return
	}

	msg := message.Message{
		ID:        uuid.New().String(),
		SessionID: fmt.Sprintf("sched-%s-%d", agentID, time.Now().UnixNano()),
		AgentID:   agentID,
		Channel:   "internal",
		ThreadID:  "scheduler",
		UserID:    "scheduler",
		Username:  "scheduler",
		Role:      message.RoleUser,
		Parts:     message.Text(fmt.Sprintf("__trigger:%s__", triggerType)),
		Metadata:  map[string]string{"trigger": triggerType},
		CreatedAt: time.Now().UTC(),
	}

	// Honor the agent's declared run_timeout (e.g. NotebookLM pipelines need
	// 30-45m). Fall back to a generous 15-min default for agents that don't
	// override it. Derived from s.appCtx so SIGTERM cancels in-flight runs
	// (PRODUCTION_AUDIT → HIGH/Concurrency: previously context.Background()
	// here meant graceful shutdown could hang for the full run_timeout).
	def := s.definition(agentID)
	if def == nil {
		s.log.Error("scheduled agent definition missing", zap.String("agent", agentID))
		return
	}
	timeout := def.ResolvedRunTimeout(15 * time.Minute)
	ctx, cancel := context.WithTimeout(s.appCtx, timeout)
	defer cancel()
	if s.principal.Subject != "" {
		principal := s.principal
		principal.RequestID = msg.ID
		ctx = runtime.WithPrincipal(ctx, principal)
	}

	runStart := time.Now()
	reply, err := s.engine.Handle(ctx, msg)
	elapsed := time.Since(runStart).Round(time.Millisecond)
	isCron := triggerType == "cron" || triggerType == "cron_missed_startup"
	if err != nil {
		s.log.Error("scheduled agent execution failed",
			zap.String("agent", agentID),
			zap.String("trigger", triggerType),
			zap.Duration("elapsed", elapsed),
			zap.Error(err),
		)
		if isCron {
			// Track consecutive failures; auto-disable a chronically-failing
			// cron agent so it stops firing on a loop nobody is watching.
			disabled, failures := s.recordFireResult(agentID, false)
			s.reportRunFailure(def, msg, triggerType, err, elapsed, failures, disabled)
		}
		return
	}
	if isCron {
		s.recordFireResult(agentID, true) // success resets the failure streak
		s.markScheduleCompleted(agentID, scheduledAt)
		// Push a heads-up that the scheduled run completed (Epic 8). Best-effort,
		// no-op when push isn't configured.
		schedAgentName := agentID
		if def != nil && def.Name != "" {
			schedAgentName = def.Name
		}
		webpush.NotifyDefault(webpush.Notification{
			Title: "Scheduled run completed",
			Body:  schedAgentName + " finished its scheduled run.",
			URL:   "/#mobile",
			Tag:   "sched-" + agentID,
		})
	}
	replyText := ""
	for _, p := range reply.Parts {
		if p.Type == message.ContentText && p.Text != "" {
			replyText = p.Text
			break
		}
	}
	s.sendScheduledOutput(ctx, def, msg, replyText, triggerType, reply.Metadata)
	s.log.Info("scheduled agent completed",
		zap.String("agent", agentID),
		zap.String("trigger", triggerType),
		zap.Duration("elapsed", elapsed),
		zap.Int("reply_len", len(replyText)),
		zap.String("reply_preview", func() string {
			if len(replyText) > 200 {
				return replyText[:200] + "…"
			}
			return replyText
		}()),
	)
}

func (s *Scheduler) reportRunFailure(def *agent.Definition, source message.Message, triggerType string, runErr error, elapsed time.Duration, consecutiveFailures int, autoDisabled bool) {
	if def == nil || runErr == nil {
		return
	}
	s.mu.Lock()
	sink := s.sink
	s.mu.Unlock()
	if sink == nil {
		return
	}
	payload := map[string]any{
		"trigger":              triggerType,
		"error":                runErr.Error(),
		"elapsed_ms":           elapsed.Milliseconds(),
		"consecutive_failures": consecutiveFailures,
		"auto_disabled":        autoDisabled,
		"runbook":              "Open Activity, use Debug in Studio, fix the failing node/provider/channel, then re-enable the cron agent.",
	}
	sink.Emit(message.Event{
		Type:      "schedule.run_failed",
		AgentID:   def.ID,
		SessionID: source.SessionID,
		Timestamp: time.Now().UTC(),
		Payload:   payload,
	})
	if autoDisabled {
		sink.Emit(message.Event{
			Type:      "schedule.auto_disabled",
			AgentID:   def.ID,
			SessionID: source.SessionID,
			Timestamp: time.Now().UTC(),
			Payload: map[string]any{
				"trigger":              triggerType,
				"error":                runErr.Error(),
				"consecutive_failures": consecutiveFailures,
				"runbook":              "The scheduler quarantined this cron agent after repeated failures. Fix the agent, then re-enable it from Studio or Agents.",
			},
		})
	}
}

func (s *Scheduler) runMissedOnStartup() {
	if s.loader == nil {
		return
	}
	now := time.Now().UTC()
	for _, def := range s.loader.All() {
		missedAt, ok := s.missedCronFire(def, now)
		if !ok {
			continue
		}
		s.log.Warn("running missed cron from startup catch-up",
			zap.String("agent", def.ID),
			zap.Time("missed_at", missedAt))
		// E4b (Cohort E — Schedule failure handling): missed-run backfill used
		// to run silently — only a Warn log entry. Now we emit a discoverable
		// `schedule.missed_run_backfilled` event so the GUI Schedule / Activity
		// pages can show "the gateway was down at 03:00 UTC — the run was
		// replayed at startup" instead of the operator having to spelunk the
		// server logs.
		s.emitMissedRunBackfilled(def, missedAt, now)
		go s.fireAt(def.ID, "cron_missed_startup", missedAt)
	}
}

// MissedBackfill is the snapshot record retained per agent so /schedule/status
// can surface "was replayed at boot on <ts>" without walking the actionlog.
type MissedBackfill struct {
	MissedAt   time.Time `json:"missed_at"`
	ReplayedAt time.Time `json:"replayed_at"`
	Cron       string    `json:"cron"`
	Window     string    `json:"window"`
	LateBy     string    `json:"late_by"`
}

// LastBackfill returns the most recent startup catch-up record for agentID, if
// any occurred since this process started. Callers use this to render the
// "auto-replayed" chip on the GUI Schedule page.
func (s *Scheduler) LastBackfill(agentID string) (MissedBackfill, bool) {
	s.backfillMu.RLock()
	defer s.backfillMu.RUnlock()
	b, ok := s.lastBackfills[agentID]
	return b, ok
}

// LastBackfillsSnapshot returns a copy of the in-process backfill map so
// handleScheduleStatus can render every agent's catch-up state in one round trip.
func (s *Scheduler) LastBackfillsSnapshot() map[string]MissedBackfill {
	s.backfillMu.RLock()
	defer s.backfillMu.RUnlock()
	out := make(map[string]MissedBackfill, len(s.lastBackfills))
	for k, v := range s.lastBackfills {
		out[k] = v
	}
	return out
}

// emitMissedRunBackfilled records a discoverable event when a cron agent's
// startup catch-up fires. The window field is the effective (parsed) window
// the missed-run check honored, so operators can tell whether an older missed
// fire was intentionally dropped.
func (s *Scheduler) emitMissedRunBackfilled(def *agent.Definition, missedAt, now time.Time) {
	if def == nil {
		return
	}
	windowStr := "24h"
	if def.Schedule != nil {
		if raw := strings.TrimSpace(def.Schedule.MissedStartupWindow); raw != "" {
			if _, err := time.ParseDuration(raw); err == nil {
				windowStr = raw
			}
		}
	}
	late := now.Sub(missedAt).Round(time.Second).String()
	cronExpr := ""
	if def.Schedule != nil {
		cronExpr = strings.TrimSpace(def.Schedule.Cron)
	}
	// Record the snapshot BEFORE emitting so a fast poller can observe the
	// backfill regardless of event-sink presence.
	s.backfillMu.Lock()
	s.lastBackfills[def.ID] = MissedBackfill{
		MissedAt:   missedAt.UTC(),
		ReplayedAt: now.UTC(),
		Cron:       cronExpr,
		Window:     windowStr,
		LateBy:     late,
	}
	s.backfillMu.Unlock()

	s.mu.Lock()
	sink := s.sink
	s.mu.Unlock()
	if sink == nil {
		return
	}
	sink.Emit(message.Event{
		Type:      "schedule.missed_run_backfilled",
		AgentID:   def.ID,
		Timestamp: now,
		Payload: map[string]any{
			"missed_at":     missedAt.UTC(),
			"replayed_at":   now.UTC(),
			"late_by":       late,
			"window":        windowStr,
			"trigger":       "cron_missed_startup",
			"schedule_expr": cronExpr,
			"reason":        "The gateway was not running when this scheduled fire came due; the most recent fire within the missed-startup window was replayed once at boot.",
			"runbook":       "This is normal after an outage. If you see the same agent backfilled every restart, increase `schedule.missed_startup_window` or check whether the gateway is crashing between runs.",
		},
	})
}

func (s *Scheduler) missedCronFire(def *agent.Definition, now time.Time) (time.Time, bool) {
	if def == nil || !def.Enabled || scheduledKind(def) != agent.TriggerCron || def.Schedule == nil {
		return time.Time{}, false
	}
	if !def.Schedule.RunMissedOnStartup || strings.TrimSpace(def.Schedule.Cron) == "" {
		return time.Time{}, false
	}
	sched, err := cronParser.Parse(def.Schedule.Cron)
	if err != nil {
		s.log.Warn("missed cron check skipped invalid expression",
			zap.String("agent", def.ID),
			zap.String("expr", def.Schedule.Cron),
			zap.Error(err))
		return time.Time{}, false
	}
	window := 24 * time.Hour
	if raw := strings.TrimSpace(def.Schedule.MissedStartupWindow); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed <= 0 {
			s.log.Warn("missed cron check using default window after invalid duration",
				zap.String("agent", def.ID),
				zap.String("missed_startup_window", raw))
		} else {
			window = parsed
		}
	}

	s.stateMu.Lock()
	if err := s.loadStateLocked(); err != nil {
		s.log.Warn("scheduler state load failed; missed cron check will use empty state", zap.Error(err))
	}
	lastCompleted := s.state.LastCompleted[def.ID]
	s.stateMu.Unlock()

	from := now.Add(-window)
	if lastCompleted.After(from) {
		from = lastCompleted
	}
	next := sched.Next(from)
	var latest time.Time
	for next.After(from) && !next.After(now) {
		latest = next.UTC()
		next = sched.Next(next)
	}
	if latest.IsZero() {
		return time.Time{}, false
	}
	if !lastCompleted.IsZero() && !latest.After(lastCompleted) {
		return time.Time{}, false
	}
	return latest, true
}

func (s *Scheduler) markScheduleCompleted(agentID string, completedAt time.Time) {
	completedAt = completedAt.UTC()
	if completedAt.IsZero() {
		completedAt = time.Now().UTC()
	}
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if err := s.loadStateLocked(); err != nil {
		s.log.Warn("scheduler state load failed before update", zap.Error(err))
	}
	if s.state.LastCompleted == nil {
		s.state.LastCompleted = make(map[string]time.Time)
	}
	if prev := s.state.LastCompleted[agentID]; prev.After(completedAt) {
		return
	}
	s.state.LastCompleted[agentID] = completedAt
	if err := s.saveStateLocked(); err != nil {
		s.log.Warn("scheduler state save failed", zap.String("agent", agentID), zap.Error(err))
	}
}

func (s *Scheduler) loadStateLocked() error {
	if s.state.LastCompleted == nil {
		s.state.LastCompleted = make(map[string]time.Time)
	}
	if s.statePath == "" {
		return nil
	}
	data, err := os.ReadFile(s.statePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var loaded scheduleState
	if err := json.Unmarshal(data, &loaded); err != nil {
		return err
	}
	if loaded.LastCompleted == nil {
		loaded.LastCompleted = make(map[string]time.Time)
	}
	s.state = loaded
	return nil
}

func (s *Scheduler) saveStateLocked() error {
	if s.statePath == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.statePath), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.statePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.statePath)
}

// deliveryOutcome is the structured result of attempting to deliver a scheduled
// reply. It always ends up logged and emitted as a schedule.output event so a
// cron result can never silently go nowhere.
type deliveryOutcome struct {
	delivered bool
	fallback  bool
	channel   string
	to        string
	reason    string
	detail    string
	// degraded records that the reasoning run behind this reply ended without
	// confidence (see message.MetaReasoningDegraded). Reported on the
	// schedule.output event so Activity can distinguish "delivered a result"
	// from "delivered whatever the run had left".
	degraded bool
}

// degradedNotice is prepended to a scheduled reply whose run ended degraded.
// Scheduled output is read out of context — hours later, on a phone, with no
// trace in view — so an unmarked degraded reply reads exactly like a finished
// deliverable. Marking beats withholding: the partial text is usually still
// useful, and silently dropping a scheduled result is its own failure mode.
const degradedNotice = "⚠️ This run did not complete cleanly — a tool failed or the reasoning loop had to recover, so the result below may be partial or may be the agent's working notes rather than a finished answer."

// MarkDegradedReply prepends the degraded notice to replyText when the reply's
// metadata says its reasoning run ended without confidence. Returns replyText
// unchanged for confident runs (and for replies carrying no such metadata, so
// non-reasoning agents are untouched).
func MarkDegradedReply(replyText string, meta map[string]string) (string, bool) {
	if meta == nil || meta[message.MetaReasoningDegraded] != "true" {
		return replyText, false
	}
	if strings.TrimSpace(replyText) == "" {
		return replyText, true
	}
	// A run that failed its BUSINESS-OUTCOME contract gets a specific notice
	// naming what went unmet, rather than the generic "didn't complete cleanly".
	// The distinction matters: every node may have executed fine, so a message
	// about tool failures would send the reader looking in the wrong place.
	if outcome := strings.TrimSpace(meta[message.MetaOutcome]); outcome != "" {
		notice := outcomeNotice(outcome)
		if summary := strings.TrimSpace(meta[message.MetaOutcomeSummary]); summary != "" {
			notice += " " + summary + "."
		}
		return notice + "\n\n" + replyText, true
	}
	notice := degradedNotice
	if steps := strings.TrimSpace(meta[message.MetaReasoningSteps]); steps != "" {
		notice = strings.TrimSuffix(notice, ".") + " (" + steps + " step(s) recorded)."
	}
	return notice + "\n\n" + replyText, true
}

// outcomeNotice renders the lead line for a run whose outcome contract went
// unmet, phrased for whoever reads the message on their phone hours later.
func outcomeNotice(outcome string) string {
	switch outcome {
	case "empty":
		return "⚠️ This run completed without errors but produced nothing:"
	case "partial":
		return "⚠️ This run only partly achieved what it was set up to do:"
	case "failed":
		return "⚠️ This run did not achieve what it was set up to do:"
	default:
		return "⚠️ This run did not meet its expected outcome:"
	}
}

// DeliverScheduledOutput publicly runs the same delivery + reporting path a cron
// fire uses, so a MANUAL trigger of a scheduled agent lands in the configured
// channel too — not just in the GUI. It is a safe no-op (with an "undelivered"
// event) for agents that have no resolvable output target.
func (s *Scheduler) DeliverScheduledOutput(ctx context.Context, def *agent.Definition, source message.Message, replyText, triggerType string) {
	s.DeliverScheduledReply(ctx, def, source, replyText, triggerType, nil)
}

// DeliverScheduledReply is DeliverScheduledOutput plus the reply's metadata, so
// a degraded reasoning run is marked as such before it reaches a channel.
// replyMeta is the reply Message's Metadata (nil is fine — treated as
// confident, preserving the older call's behaviour exactly).
func (s *Scheduler) DeliverScheduledReply(ctx context.Context, def *agent.Definition, source message.Message, replyText, triggerType string, replyMeta map[string]string) {
	s.sendScheduledOutput(ctx, def, source, replyText, triggerType, replyMeta)
}

// HasScheduledOutputTarget reports whether the agent has a delivery target the
// scheduler can resolve (explicit schedule.output, or a channel default). Used
// by the manual-trigger path to decide whether to publish the result.
func (s *Scheduler) HasScheduledOutputTarget(def *agent.Definition) bool {
	_, ok := s.resolveScheduledOutput(def)
	return ok
}

func (s *Scheduler) sendScheduledOutput(ctx context.Context, def *agent.Definition, source message.Message, replyText, triggerType string, replyMeta map[string]string) {
	if def == nil || strings.TrimSpace(replyText) == "" {
		return
	}
	replyText, degraded := MarkDegradedReply(replyText, replyMeta)
	outcome := s.deliverScheduled(ctx, def, source, replyText, triggerType)
	outcome.degraded = degraded
	s.reportDelivery(def, source, replyText, triggerType, outcome)
}

// deliverScheduled attempts primary delivery and, if that isn't possible, a
// single-default-channel fallback so results still land somewhere.
func (s *Scheduler) deliverScheduled(ctx context.Context, def *agent.Definition, source message.Message, replyText, triggerType string) deliveryOutcome {
	s.mu.Lock()
	reg := s.channels
	s.mu.Unlock()

	outCfg, ok := s.resolveScheduledOutput(def)
	if !ok {
		// No per-agent or channel-default output resolved. Try a global
		// single-default fallback before giving up.
		if o, done := s.tryFallback(ctx, reg, def, source, replyText, triggerType); done {
			return o
		}
		return deliveryOutcome{reason: deliveryNoOutput}
	}
	channelID := strings.TrimSpace(outCfg.Channel)
	to := strings.TrimSpace(outCfg.To)
	if channelID == "" || to == "" {
		if o, done := s.tryFallback(ctx, reg, def, source, replyText, triggerType); done {
			return o
		}
		return deliveryOutcome{reason: deliveryIncomplete, channel: channelID, to: to}
	}
	if reg == nil {
		return deliveryOutcome{reason: deliveryNoRegistry, channel: channelID, to: to}
	}
	if _, known := reg.Statuses()[channelID]; !known {
		if o, done := s.tryFallback(ctx, reg, def, source, replyText, triggerType); done {
			return o
		}
		return deliveryOutcome{reason: deliveryAdapterUnknown, channel: channelID, to: to}
	}

	text := RenderScheduledOutput(outCfg.Template, def, replyText, triggerType)
	if err := s.sendVia(ctx, reg, def, source, channelID, to, outCfg.BotName, triggerType, text); err != nil {
		return deliveryOutcome{reason: deliverySendFailed, channel: channelID, to: to, detail: err.Error()}
	}
	return deliveryOutcome{delivered: true, reason: deliveryDelivered, channel: channelID, to: to}
}

// tryFallback delivers to the single configured default outbound channel when
// the agent's own output couldn't be resolved, so a result is not lost. Returns
// done=false when no usable fallback exists.
func (s *Scheduler) tryFallback(ctx context.Context, reg *channels.Registry, def *agent.Definition, source message.Message, replyText, triggerType string) (deliveryOutcome, bool) {
	if reg == nil {
		return deliveryOutcome{}, false
	}
	fb, ok := s.singleDefaultOutput()
	if !ok {
		return deliveryOutcome{}, false
	}
	channelID := strings.TrimSpace(fb.Channel)
	to := strings.TrimSpace(fb.To)
	if channelID == "" || to == "" {
		return deliveryOutcome{}, false
	}
	if _, known := reg.Statuses()[channelID]; !known {
		return deliveryOutcome{}, false
	}
	// Prefix so the operator knows this landed on the fallback channel because
	// the agent's own delivery target wasn't configured.
	body := RenderScheduledOutput(fb.Template, def, replyText, triggerType)
	notice := fmt.Sprintf("⚠ Scheduled result for %q had no delivery target — routed to the default channel.\n\n%s", def.ID, body)
	if err := s.sendVia(ctx, reg, def, source, channelID, to, fb.BotName, triggerType, notice); err != nil {
		return deliveryOutcome{reason: deliverySendFailed, channel: channelID, to: to, detail: err.Error(), fallback: true}, true
	}
	return deliveryOutcome{delivered: true, fallback: true, reason: deliveryViaFallback, channel: channelID, to: to}, true
}

// singleDefaultOutput returns the sole default outbound target when exactly one
// is configured — the unambiguous fallback for otherwise-undeliverable results.
func (s *Scheduler) singleDefaultOutput() (agent.ScheduleOutput, bool) {
	s.defaultMu.RLock()
	defer s.defaultMu.RUnlock()
	var only agent.ScheduleOutput
	count := 0
	for _, o := range s.defaultOutputs {
		if strings.TrimSpace(o.To) == "" {
			continue
		}
		only = o
		count++
	}
	if count == 1 {
		return only, true
	}
	return agent.ScheduleOutput{}, false
}

func (s *Scheduler) sendVia(ctx context.Context, reg *channels.Registry, def *agent.Definition, source message.Message, channelID, to, botName, triggerType, text string) error {
	out := message.Message{
		ID:        uuid.New().String(),
		SessionID: source.SessionID,
		AgentID:   def.ID,
		Channel:   channelID,
		ThreadID:  to,
		UserID:    "scheduler",
		Username:  "scheduler",
		Role:      message.RoleAssistant,
		Parts:     message.Text(text),
		Metadata: map[string]string{
			"trigger":  triggerType,
			"bot_name": botName,
		},
		CreatedAt: time.Now().UTC(),
	}
	return reg.Send(ctx, out)
}

// reportDelivery logs the outcome and emits a schedule.output event so every
// scheduled reply's fate is visible in Activity — delivered, routed to a
// fallback, or (loudly) undelivered.
func (s *Scheduler) reportDelivery(def *agent.Definition, source message.Message, replyText, triggerType string, o deliveryOutcome) {
	fields := []zap.Field{
		zap.String("agent", def.ID),
		zap.String("reason", o.reason),
		zap.String("channel", o.channel),
		zap.String("to", o.to),
		zap.Bool("fallback", o.fallback),
	}
	if o.detail != "" {
		fields = append(fields, zap.String("detail", o.detail))
	}
	if o.delivered {
		s.log.Info("scheduled output delivered", fields...)
	} else {
		s.log.Warn("scheduled reply was NOT delivered to any channel — check schedule.output", fields...)
	}

	s.mu.Lock()
	sink := s.sink
	s.mu.Unlock()
	if sink == nil {
		return
	}
	preview := replyText
	if len(preview) > 280 {
		preview = preview[:280] + "…"
	}
	sink.Emit(message.Event{
		Type:      "schedule.output",
		AgentID:   def.ID,
		SessionID: source.SessionID,
		Timestamp: time.Now().UTC(),
		Payload: map[string]any{
			"delivered":     o.delivered,
			"fallback":      o.fallback,
			"reason":        o.reason,
			"channel":       o.channel,
			"to":            o.to,
			"detail":        o.detail,
			"trigger":       triggerType,
			"reply_preview": preview,
			"degraded":      o.degraded,
		},
	})
}

func (s *Scheduler) resolveScheduledOutput(def *agent.Definition) (*agent.ScheduleOutput, bool) {
	if def == nil {
		return nil, false
	}
	if def.Schedule != nil && def.Schedule.Output != nil {
		out := *def.Schedule.Output
		out.Channel = strings.TrimSpace(out.Channel)
		out.To = strings.TrimSpace(out.To)
		out.BotName = strings.TrimSpace(out.BotName)
		out.Template = strings.TrimSpace(out.Template)
		if out.Channel != "" && out.To != "" {
			return &out, true
		}
		if out.Channel != "" {
			if fallback, ok := s.defaultOutput(out.Channel); ok {
				if out.To == "" {
					out.To = fallback.To
				}
				if out.BotName == "" {
					out.BotName = fallback.BotName
				}
				if out.Template == "" {
					out.Template = fallback.Template
				}
				if out.To != "" {
					return &out, true
				}
			}
		}
	}
	for _, channelID := range def.Channels {
		if out, ok := s.defaultOutput(channelID); ok {
			return &out, true
		}
	}
	return nil, false
}

func (s *Scheduler) defaultOutput(channelID string) (agent.ScheduleOutput, bool) {
	channelID = strings.TrimSpace(channelID)
	if channelID == "" {
		return agent.ScheduleOutput{}, false
	}
	s.defaultMu.RLock()
	defer s.defaultMu.RUnlock()
	out, ok := s.defaultOutputs[channelID]
	return out, ok
}

// DefaultOutputsFromChannelConfig extracts shared scheduled-output defaults
// from channel config. Put default_output_to on a channel or on a multi-bot row.
// When placed on a bot row, that bot's adapter id becomes the sender.
func DefaultOutputsFromChannelConfig(cfg map[string]map[string]any) map[string]agent.ScheduleOutput {
	out := map[string]agent.ScheduleOutput{}
	for channelID, channelCfg := range cfg {
		channelID = strings.TrimSpace(channelID)
		if channelID == "" || channelCfg == nil {
			continue
		}
		if dest := cfgString(channelCfg, "default_output_to"); dest != "" {
			out[channelID] = agent.ScheduleOutput{
				Channel:  channelID,
				To:       dest,
				BotName:  cfgString(channelCfg, "bot_name"),
				Template: cfgString(channelCfg, "default_output_template"),
			}
		}
		hasDefaultBot := cfgString(channelCfg, "token") != "" || cfgString(channelCfg, "bot_token") != ""
		for i, bot := range rawBotMaps(channelCfg["bots"]) {
			if dest := cfgString(bot, "default_output_to"); dest != "" {
				adapterID := defaultAdapterID(channelID, bot, i, hasDefaultBot)
				out[adapterID] = agent.ScheduleOutput{
					Channel:  adapterID,
					To:       dest,
					BotName:  cfgString(bot, "bot_name"),
					Template: cfgString(bot, "default_output_template"),
				}
				if _, exists := out[channelID]; !exists {
					out[channelID] = out[adapterID]
				}
			}
		}
	}
	return out
}

func cfgString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

func rawBotMaps(raw any) []map[string]any {
	switch list := raw.(type) {
	case []map[string]any:
		return list
	case []any:
		out := make([]map[string]any, 0, len(list))
		for _, item := range list {
			if m, ok := item.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	default:
		return nil
	}
}

func defaultAdapterID(channelID string, bot map[string]any, index int, defaultReserved bool) string {
	if index == 0 && !defaultReserved {
		return channelID
	}
	suffix := sanitizeDefaultOutputID(cfgString(bot, "agent_id"))
	if suffix == "" {
		suffix = sanitizeDefaultOutputID(cfgString(bot, "bot_name"))
	}
	if suffix == "" {
		suffix = fmt.Sprintf("%d", index+1)
	}
	return channelID + "-" + suffix
}

func sanitizeDefaultOutputID(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return b.String()
}

func RenderScheduledOutput(tpl string, def *agent.Definition, replyText, triggerType string) string {
	if strings.TrimSpace(tpl) == "" {
		return replyText
	}
	replacements := map[string]string{
		"{reply}":      replyText,
		"{agent_id}":   def.ID,
		"{agent_name}": def.Name,
		"{trigger}":    triggerType,
		"{timestamp}":  time.Now().UTC().Format(time.RFC3339),
	}
	out := tpl
	for k, v := range replacements {
		out = strings.ReplaceAll(out, k, v)
	}
	return out
}

// Entries returns a snapshot of all active cron schedules.
func (s *Scheduler) Entries() []ScheduleEntry {
	blocked := s.BlocksSnapshot()
	s.mu.Lock()
	defer s.mu.Unlock()
	var entries []ScheduleEntry
	for agentID, entryID := range s.entries {
		e := s.cron.Entry(entryID)
		se := ScheduleEntry{
			AgentID: agentID,
			Next:    e.Next,
			Prev:    e.Prev,
		}
		// A schedule with a future Next time reads as healthy; without this the
		// GUI would show "next run 03:00" for an agent the gate refuses to fire.
		if b, ok := blocked[agentID]; ok {
			se.Blocked = true
			se.BlockedReason = b.Summary
			se.BlockedRequirements = requirementIDs(b.Failed)
		}
		// Surface missed-run catch-up settings (Story 12) so the Schedule
		// GUI can explain restart behaviour per agent.
		if s.loader != nil {
			if def := s.loader.Get(agentID); def != nil && def.Schedule != nil && def.Schedule.RunMissedOnStartup {
				se.CatchUp = true
				se.CatchUpWindow = strings.TrimSpace(def.Schedule.MissedStartupWindow)
				if se.CatchUpWindow == "" {
					se.CatchUpWindow = "24h" // documented default
				}
			}
		}
		entries = append(entries, se)
	}
	for agentID := range s.oneshot {
		entries = append(entries, ScheduleEntry{
			AgentID: agentID,
			Type:    "oneshot",
		})
	}
	return entries
}

// ScheduleEntry is a summary of one scheduled agent for the admin API.
type ScheduleEntry struct {
	AgentID string    `json:"agent_id"`
	Type    string    `json:"type,omitempty"` // "cron" or "oneshot"
	Next    time.Time `json:"next,omitempty"`
	Prev    time.Time `json:"prev,omitempty"`

	// CatchUp reports run_missed_on_startup; CatchUpWindow is the
	// missed_startup_window ("24h" default). Story 12: lets the GUI show
	// which agents recover missed fires after a restart.
	CatchUp       bool   `json:"catch_up,omitempty"`
	CatchUpWindow string `json:"catch_up_window,omitempty"`

	// Blocked reports that the readiness gate refused the most recent fire, with
	// the one-line reason and the ids of the unmet requirements (ST-16). Zero
	// values mean "not blocked", so an entry built before this existed reads
	// exactly as it did before.
	Blocked             bool     `json:"blocked,omitempty"`
	BlockedReason       string   `json:"blocked_reason,omitempty"`
	BlockedRequirements []string `json:"blocked_requirements,omitempty"`
}

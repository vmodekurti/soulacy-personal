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
	"github.com/soulacy/soulacy/internal/schedules"
	"github.com/soulacy/soulacy/internal/webpush"
	"github.com/soulacy/soulacy/internal/wsroot"
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
	entries  map[scheduleKey]cron.EntryID // (workspace, agent) → cron entry
	oneshot  map[scheduleKey]context.CancelFunc

	// appCtx is the gateway's app-wide context. Every fired run derives its
	// own context from this one so SIGTERM cancellation propagates through
	// engine.Handle → provider HTTP → tool subprocess.
	// (PRODUCTION_AUDIT → HIGH/Concurrency)
	appCtx context.Context

	runMu   sync.Mutex
	running map[scheduleKey]time.Time // (workspace, agent) → run start time (currently executing)

	stateMu   sync.Mutex
	statePath string
	state     scheduleState

	// failMu guards failCounts. consecutiveFailLimit is the number of back-to-back
	// failed fires after which a cron agent is auto-disabled (Story 2 / S7.2) so a
	// permanently-broken agent (bad model, dead provider) stops firing on a loop
	// nobody is watching. A successful run resets the counter. <=0 disables the
	// feature.
	failMu               sync.Mutex
	failCounts           map[scheduleKey]int
	consecutiveFailLimit int

	defaultMu      sync.RWMutex
	defaultOutputs map[string]agent.ScheduleOutput

	// backfillMu guards backfills. lastBackfills records the most recent
	// startup-catch-up fire per agent since the process started; the GUI's
	// Schedule page renders it as an "Auto-replayed on Jul 15 03:04" chip so
	// operators aren't blindsided by an out-of-schedule run after a restart.
	// (E4b — Cohort E Schedule failure handling.)
	backfillMu sync.RWMutex
	// Keyed by (workspace, agent), like every other map here. Keyed by agent
	// alone, one tenant's startup catch-up was reported to every tenant that
	// happened to own an agent of the same name — and the GUI chip that says
	// "auto-replayed at 03:04" would have named a run that never happened in
	// the workspace looking at it.
	lastBackfills map[scheduleKey]MissedBackfill

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
	// Keyed by (workspace, agent). This one was the sharpest of the agent-ID
	// keys: clearBlock deletes on every cleared tick, so tenant A's passing
	// agent silently cleared tenant B's recorded refusal, and B's Schedule page
	// then showed a healthy schedule that was still not firing.
	blocks map[scheduleKey]ScheduleBlock

	// principal is the verified service identity for scheduled work. It is set
	// once during startup; a zero value preserves embedded/test compatibility.
	principal        runtime.Principal
	requirePrincipal bool

	// store, when set, is the durable workspace-scoped schedule record and the
	// claim that makes an occurrence fire exactly once across instances.
	// instanceID identifies this process to that claim. See tenancy.go.
	store      *schedules.Store
	instanceID string
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
		entries:              make(map[scheduleKey]cron.EntryID),
		oneshot:              make(map[scheduleKey]context.CancelFunc),
		running:              make(map[scheduleKey]time.Time),
		state:                scheduleState{LastCompleted: make(map[string]time.Time)},
		failCounts:           make(map[scheduleKey]int),
		defaultOutputs:       make(map[string]agent.ScheduleOutput),
		lastBackfills:        make(map[scheduleKey]MissedBackfill),
		blocks:               make(map[scheduleKey]ScheduleBlock),
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
func (s *Scheduler) recordFireResult(key scheduleKey, ok bool) (bool, int) {
	s.failMu.Lock()
	limit := s.consecutiveFailLimit
	s.failMu.Unlock()

	// DURABLE FIRST, in-memory only as the fallback.
	//
	// Schedules are claimed durably, so each occurrence fires on exactly one
	// replica — but not the same one each time. A per-process counter
	// therefore advances by one per REPLICA per failure round, so with two
	// replicas the limit is reached at best half as often and with three,
	// never. The agent keeps firing on a loop nobody is watching, which is the
	// exact thing the limit exists to stop. A restart does the same to a
	// single process.
	//
	// The in-memory map stays for schedules with no durable row — an agent
	// fired from a SOUL.yaml without one — where it is still better than no
	// counter at all.
	if store, _ := s.scheduleStore(); store != nil {
		ctx, cancel := context.WithTimeout(s.appCtx, 10*time.Second)
		if ok {
			err := store.ClearFailures(ctx, key.workspaceID, key.agentID)
			cancel()
			if err == nil {
				s.failMu.Lock()
				delete(s.failCounts, key)
				s.failMu.Unlock()
				return false, 0
			}
		} else {
			count, err := store.RecordFailure(ctx, key.workspaceID, key.agentID)
			cancel()
			// count == 0 with no error means the schedule has no durable row.
			// Falling through to the map is right there; treating it as "zero
			// failures" would reset the counter on every fire.
			if err == nil && count > 0 {
				return s.applyFailureCount(key, count, limit)
			}
		}
	}

	s.failMu.Lock()
	if ok {
		delete(s.failCounts, key)
		s.failMu.Unlock()
		return false, 0
	}
	s.failCounts[key]++
	count := s.failCounts[key]
	s.failMu.Unlock()
	return s.applyFailureCount(key, count, limit)
}

// applyFailureCount decides what a failure count means and acts on it.
//
// Split out so the durable and in-memory paths cannot drift: the quarantine,
// the deregistration and the recorded reason are one piece of behaviour, and
// two copies of it would be two answers to "when does an agent get switched
// off".
func (s *Scheduler) applyFailureCount(key scheduleKey, count, limit int) (bool, int) {
	if limit <= 0 || count < limit {
		return false, count
	}
	// Quarantine the agent: stop it firing and disable it in memory so a fixed
	// SOUL.yaml (saved later) re-enables it via the normal reload path.
	s.deregister(key)
	disabled := false
	if s.loader != nil {
		disabled = s.loader.SetEnabledInMemoryInWorkspace(key.workspaceID, key.agentID, false)
	}
	s.failMu.Lock()
	delete(s.failCounts, key)
	s.failMu.Unlock()
	// Cleared durably too, or the agent re-enabled by an operator would be
	// disabled again on its very next failure rather than after the limit.
	if store, _ := s.scheduleStore(); store != nil {
		ctx, cancel := context.WithTimeout(s.appCtx, 10*time.Second)
		_ = store.ClearFailures(ctx, key.workspaceID, key.agentID)
		cancel()
	}
	// MU-023 criterion 5: a schedule the system switched off must say why, or
	// "it stopped running and nobody knows" is the operator's whole picture.
	if store, _ := s.scheduleStore(); store != nil {
		ctx, cancel := context.WithTimeout(s.appCtx, 10*time.Second)
		_ = store.Disable(ctx, key.workspaceID, key.agentID,
			fmt.Sprintf("auto-disabled after %d consecutive failed runs", count))
		cancel()
	}
	s.log.Error("cron agent auto-disabled after consecutive failures — fix its config and re-enable",
		zap.String("schedule", key.String()),
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

// PrincipalWorkspace is the workspace every scheduled run acts in, or "" when
// no principal has been set (personal, where "" normalises to personal).
//
// It exists so a readiness gate can read its verdict from the same workspace
// the run will execute under. Reading them from different workspaces would let
// one tenant's certification clear another tenant's run — the two values must
// come from one source, and this is it.
func (s *Scheduler) PrincipalWorkspace() string { return s.principal.WorkspaceID }

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
	return s.tryStartRun(keyFor(s.defaultWorkspace(), agentID))
}

// TryStartRunInWorkspace is the same lock, taken in a named workspace.
func (s *Scheduler) TryStartRunInWorkspace(workspaceID, agentID string) bool {
	return s.tryStartRun(keyFor(workspaceID, agentID))
}

// tryStartRun is the workspace-aware core. The run lock is per (workspace,
// agent): keyed by agent alone, one tenant's long-running "daily-report"
// silently suppressed every other tenant's, and the log line said "already
// running" about an agent that was not theirs.
func (s *Scheduler) tryStartRun(key scheduleKey) bool {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	if started, ok := s.running[key]; ok && time.Since(started) < maxRunDuration {
		return false
	}
	s.running[key] = time.Now()
	return true
}

// FinishRun clears an agent's running state.
func (s *Scheduler) FinishRun(agentID string) {
	s.finishRun(keyFor(s.defaultWorkspace(), agentID))
}

// FinishRunInWorkspace releases the lock TryStartRunInWorkspace took.
func (s *Scheduler) FinishRunInWorkspace(workspaceID, agentID string) {
	s.finishRun(keyFor(workspaceID, agentID))
}

func (s *Scheduler) finishRun(key scheduleKey) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	delete(s.running, key)
}

// IsRunning reports whether an agent is currently executing (and not stale).
func (s *Scheduler) IsRunning(agentID string) bool {
	return s.isRunning(keyFor(s.defaultWorkspace(), agentID))
}

// IsRunningInWorkspace answers for a named tenant's agent.
func (s *Scheduler) IsRunningInWorkspace(workspaceID, agentID string) bool {
	return s.isRunning(keyFor(workspaceID, agentID))
}

func (s *Scheduler) isRunning(key scheduleKey) bool {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	started, ok := s.running[key]
	return ok && time.Since(started) < maxRunDuration
}

// RunningSnapshot returns a copy of currently-running agents and their start
// times, excluding stale entries past maxRunDuration.
func (s *Scheduler) RunningSnapshot() map[string]time.Time {
	return s.runningSnapshot(crossWorkspaceSnapshot)
}

// RunningSnapshotInWorkspace returns only the named tenant's running agents.
//
// The unscoped RunningSnapshot flattens every workspace into one agent-ID map,
// so two tenants running "daily-report" produce one entry and the GUI shows
// one of them a start time from the other. Returning agent IDs (rather than
// keys) is still right HERE because the caller has already named the
// workspace, so the ID is unambiguous within the answer.
func (s *Scheduler) RunningSnapshotInWorkspace(workspaceID string) map[string]time.Time {
	return s.runningSnapshot(wsroot.Normalize(workspaceID))
}

// crossWorkspaceSnapshot is not a workspace ID and can never equal one:
// wsroot.Validate rejects every ID containing a space.
const crossWorkspaceSnapshot = "* all workspaces *"

func (s *Scheduler) runningSnapshot(workspaceID string) map[string]time.Time {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	out := make(map[string]time.Time, len(s.running))
	for k, v := range s.running {
		if workspaceID != crossWorkspaceSnapshot && k.workspaceID != workspaceID {
			continue
		}
		if time.Since(v) < maxRunDuration {
			out[k.agentID] = v
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
	return s.RegisterAgentInWorkspace(s.defaultWorkspace(), def)
}

// RegisterAgentInWorkspace registers one workspace's scheduled agent.
//
// The workspace is a parameter rather than a field because two tenants may
// legitimately schedule agents with the same ID — that collision is what the
// agent-ID-keyed cron table got wrong, silently, by replacement.
func (s *Scheduler) RegisterAgentInWorkspace(workspaceID string, def *agent.Definition) error {
	if def == nil || def.Schedule == nil || !def.Enabled {
		return nil
	}
	key := keyFor(workspaceID, def.ID)
	switch scheduledKind(def) {
	case agent.TriggerCron:
		return s.addCron(key, def)
	case agent.TriggerOneShot:
		return s.addOneShot(key, def)
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
	s.deregister(keyFor(s.defaultWorkspace(), agentID))
}

// DeregisterAgentInWorkspace removes one workspace's scheduled agent.
func (s *Scheduler) DeregisterAgentInWorkspace(workspaceID, agentID string) {
	s.deregister(keyFor(workspaceID, agentID))
}

func (s *Scheduler) deregister(key scheduleKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.entries[key]; ok {
		s.cron.Remove(id)
		delete(s.entries, key)
	}
	if cancel, ok := s.oneshot[key]; ok {
		cancel()
		delete(s.oneshot, key)
	}
}

func (s *Scheduler) addCron(key scheduleKey, def *agent.Definition) error {
	if def.Schedule.Cron == "" {
		return fmt.Errorf("scheduler: cron expression is empty for agent %s", def.ID)
	}

	entryID, err := s.cron.AddFunc(def.Schedule.Cron, func() {
		// Truncated so the stored scheduled_at matches the occurrence key
		// derived from it. The key itself truncates too — that is where the
		// cross-instance agreement actually lives (schedules.OccurrenceKey);
		// this keeps the row and its key describing the same instant.
		s.fireAt(key, "cron", time.Now().UTC().Truncate(time.Second))
	})
	if err != nil {
		return fmt.Errorf("scheduler: invalid cron expression %q: %w", def.Schedule.Cron, err)
	}

	s.mu.Lock()
	// Remove previous entry if re-registering
	if old, ok := s.entries[key]; ok {
		s.cron.Remove(old)
	}
	s.entries[key] = entryID
	s.mu.Unlock()

	s.log.Info("cron agent registered",
		zap.String("schedule", key.String()),
		zap.String("expr", def.Schedule.Cron),
	)
	return nil
}

func (s *Scheduler) addOneShot(key scheduleKey, def *agent.Definition) error {
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
	if oldCancel, ok := s.oneshot[key]; ok {
		oldCancel()
	}
	s.oneshot[key] = cancel
	s.mu.Unlock()

	// The one-shot's occurrence is its DECLARED time, not the moment the timer
	// happens to fire: two instances whose timers drift by milliseconds must
	// still agree on which occurrence this is.
	scheduledAt := def.Schedule.At.UTC().Truncate(time.Second)
	go func() {
		select {
		case <-time.After(delay):
			s.fireAt(key, "oneshot", scheduledAt)
			s.mu.Lock()
			delete(s.oneshot, key)
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
	s.fireAt(keyFor(s.defaultWorkspace(), agentID), triggerType, time.Now().UTC().Truncate(time.Second))
}

// definition looks the agent up, tolerating a scheduler built without a loader
// (embedded uses and some tests) — "no opinion" rather than a panic.
func (s *Scheduler) definition(agentID string) *agent.Definition {
	return s.definitionIn(keyFor(s.defaultWorkspace(), agentID))
}

// definitionIn resolves an agent within its own workspace. Resolving by ID
// alone returned whichever tenant's agent happened to be registered under that
// ID — the collision this story is about, in the one place where getting it
// wrong means running somebody else's prompt.
func (s *Scheduler) definitionIn(key scheduleKey) *agent.Definition {
	if s.loader == nil {
		return nil
	}
	return s.loader.GetInWorkspace(key.workspaceID, key.agentID)
}

func isBudgetHaltReply(text string) bool {
	return strings.Contains(text, "Run paused before the next model call because its prompt no longer fits the run token budget") ||
		strings.Contains(text, "Run halted before the next model call because the token budget cannot fit its prompt")
}

// fire synthesises a trigger message and dispatches it to the engine.
func (s *Scheduler) fireAt(key scheduleKey, triggerType string, scheduledAt time.Time) {
	agentID := key.agentID
	// MU-023 criterion 2. The claim comes FIRST — before the run lock, the
	// definition lookup and the readiness gate — for the same reason the gate
	// comes before the provider is dialled: an instance that is not going to
	// run this occurrence must not spend anything discovering that. With no
	// store this always wins, so a single personal gateway is unchanged.
	claim, mine := s.claimOccurrence(key, scheduledAt)
	if !mine {
		return
	}
	var runErr error
	defer func() { s.completeOccurrence(claim, runErr) }()

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
	if def := s.definitionIn(key); def != nil && !def.Enabled {
		s.log.Warn("skipping scheduled run — agent is disabled",
			zap.String("schedule", key.String()), zap.String("trigger", triggerType))
		s.deregister(key)
		// Criterion 5: the schedule record says why it stopped, so an
		// operator does not have to correlate logs to find out.
		if store, _ := s.scheduleStore(); store != nil {
			ctx, cancel := context.WithTimeout(s.appCtx, 10*time.Second)
			_ = store.Disable(ctx, key.workspaceID, agentID, "the agent was disabled")
			cancel()
		}
		runErr = fmt.Errorf("agent is disabled")
		return
	}

	// Readiness gate (ST-16). Checked before the run lock, before the definition
	// lookup, before any provider is dialled — because an agent that must not
	// run must not consume anything either. A nil gate is a no-op, so non-Studio
	// agents behave exactly as before. Re-checked on every tick, so fixing the
	// blocker unblocks the schedule without a restart.
	if s.blockedByReadiness(key, triggerType) {
		return
	}

	// Prevent overlapping runs: if a manual or previous scheduled run is still
	// executing, skip this fire rather than running the agent twice concurrently.
	if !s.tryStartRun(key) {
		s.log.Warn("skipping scheduled run — agent already running",
			zap.String("schedule", key.String()), zap.String("trigger", triggerType))
		runErr = fmt.Errorf("a previous run of this agent is still executing")
		return
	}
	defer s.finishRun(key)

	s.log.Info("firing scheduled agent",
		zap.String("agent", agentID),
		zap.String("trigger", triggerType),
	)
	principal := s.principalFor(key.workspaceID)
	if s.requirePrincipal && principal.Subject == "" {
		s.log.Error("scheduled run blocked: verified workspace service principal is missing",
			zap.String("schedule", key.String()), zap.String("trigger", triggerType))
		runErr = fmt.Errorf("no verified service principal for this workspace")
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
	def := s.definitionIn(key)
	if def == nil {
		s.log.Error("scheduled agent definition missing", zap.String("schedule", key.String()))
		runErr = fmt.Errorf("agent definition missing")
		return
	}
	timeout := def.ResolvedRunTimeout(15 * time.Minute)
	ctx, cancel := context.WithTimeout(s.appCtx, timeout)
	defer cancel()
	if principal.Subject != "" {
		principal.RequestID = msg.ID
		ctx = runtime.WithPrincipal(ctx, principal)
	}

	runStart := time.Now()
	reply, err := s.engine.Handle(ctx, msg)
	runErr = err
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
			disabled, failures := s.recordFireResult(key, false)
			s.reportRunFailure(def, msg, triggerType, err, elapsed, failures, disabled)
		}
		return
	}
	replyText := ""
	for _, p := range reply.Parts {
		if p.Type == message.ContentText && p.Text != "" {
			replyText = p.Text
			break
		}
	}
	// A budget halt intentionally returns the best partial answer instead of a
	// Go error. That is useful in interactive Chat, but a cron system must not
	// record an incomplete report as a successful occurrence. Preserve and
	// deliver the partial result, while recording a targeted, actionable failure.
	if isBudgetHaltReply(replyText) {
		runErr = fmt.Errorf("scheduled run needs a larger agent token budget: %s", replyText)
		if isCron {
			disabled, failures := s.recordFireResult(key, false)
			s.reportRunFailure(def, msg, triggerType, runErr, elapsed, failures, disabled)
		}
		s.sendScheduledOutput(ctx, def, msg, replyText, triggerType, reply.Metadata)
		return
	}
	if isCron {
		s.recordFireResult(key, true) // success resets the failure streak
		s.markScheduleCompleted(key, scheduledAt)
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
	if strings.Contains(runErr.Error(), "token budget") {
		payload["runbook"] = "Open the agent budget settings, apply the recommended run token budget (within the deployment ceiling), save, then run once manually before re-enabling the schedule."
		payload["remediation"] = "increase_agent_run_budget"
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
	// Across every workspace, and the workspace travels with each definition.
	// loader.All() returns the default workspace's agents only, so a
	// multi-tenant deployment silently skipped every other tenant's catch-up.
	for _, workspaceID := range s.loaderWorkspaces() {
		for _, def := range s.loader.AllInWorkspace(workspaceID) {
			s.backfillOne(keyFor(workspaceID, def.ID), def, now)
		}
	}
}

// loaderWorkspaces lists every workspace with agents, falling back to the
// scheduler's own when the loader predates workspace awareness.
func (s *Scheduler) loaderWorkspaces() []string {
	if workspaces := s.loader.AllWorkspaces(); len(workspaces) > 0 {
		return workspaces
	}
	return []string{s.defaultWorkspace()}
}

func (s *Scheduler) backfillOne(key scheduleKey, def *agent.Definition, now time.Time) {
	{
		missedAt, ok := s.missedCronFire(key, def, now)
		if !ok {
			return
		}
		s.log.Warn("running missed cron from startup catch-up",
			zap.String("schedule", key.String()),
			zap.Time("missed_at", missedAt))
		// E4b (Cohort E — Schedule failure handling): missed-run backfill used
		// to run silently — only a Warn log entry. Now we emit a discoverable
		// `schedule.missed_run_backfilled` event so the GUI Schedule / Activity
		// pages can show "the gateway was down at 03:00 UTC — the run was
		// replayed at startup" instead of the operator having to spelunk the
		// server logs.
		s.emitMissedRunBackfilled(key, def, missedAt, now)
		// The occurrence key is the MISSED instant, so a second instance
		// performing the same startup catch-up collides on the claim instead
		// of replaying the same missed run alongside us.
		go s.fireAt(key, "cron_missed_startup", missedAt.UTC().Truncate(time.Second))
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
	return s.LastBackfillInWorkspace(s.defaultWorkspace(), agentID)
}

// LastBackfillInWorkspace answers for a named tenant's agent.
func (s *Scheduler) LastBackfillInWorkspace(workspaceID, agentID string) (MissedBackfill, bool) {
	s.backfillMu.RLock()
	defer s.backfillMu.RUnlock()
	b, ok := s.lastBackfills[keyFor(workspaceID, agentID)]
	return b, ok
}

// LastBackfillsSnapshot returns a copy of the in-process backfill map so
// handleScheduleStatus can render every agent's catch-up state in one round trip.
func (s *Scheduler) LastBackfillsSnapshot() map[string]MissedBackfill {
	return s.lastBackfillsSnapshot(crossWorkspaceSnapshot)
}

// LastBackfillsInWorkspace returns only the named tenant's catch-up records.
func (s *Scheduler) LastBackfillsInWorkspace(workspaceID string) map[string]MissedBackfill {
	return s.lastBackfillsSnapshot(wsroot.Normalize(workspaceID))
}

func (s *Scheduler) lastBackfillsSnapshot(workspaceID string) map[string]MissedBackfill {
	s.backfillMu.RLock()
	defer s.backfillMu.RUnlock()
	out := make(map[string]MissedBackfill, len(s.lastBackfills))
	for k, v := range s.lastBackfills {
		if workspaceID != crossWorkspaceSnapshot && k.workspaceID != workspaceID {
			continue
		}
		out[k.agentID] = v
	}
	return out
}

// emitMissedRunBackfilled records a discoverable event when a cron agent's
// startup catch-up fires. The window field is the effective (parsed) window
// the missed-run check honored, so operators can tell whether an older missed
// fire was intentionally dropped.
func (s *Scheduler) emitMissedRunBackfilled(key scheduleKey, def *agent.Definition, missedAt, now time.Time) {
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
	s.lastBackfills[key] = MissedBackfill{
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

func (s *Scheduler) missedCronFire(key scheduleKey, def *agent.Definition, now time.Time) (time.Time, bool) {
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
	// Read under the SAME composite key the write uses. Reading by agent ID
	// while writing by (workspace, agent) is the version of this bug that
	// survives a refactor: every tenant would look like it had never run, and
	// every restart would replay everyone's catch-up.
	lastCompleted := s.state.LastCompleted[key.String()]
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

func (s *Scheduler) markScheduleCompleted(key scheduleKey, completedAt time.Time) {
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
	// Keyed by workspace AND agent: two tenants' "daily-report" sharing one
	// last-completed timestamp made each look like it had just run when the
	// other did, which is precisely what the startup catch-up reads.
	stateKey := key.String()
	if prev := s.state.LastCompleted[stateKey]; prev.After(completedAt) {
		return
	}
	s.state.LastCompleted[stateKey] = completedAt
	if err := s.saveStateLocked(); err != nil {
		s.log.Warn("scheduler state save failed", zap.String("schedule", stateKey), zap.Error(err))
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
		ID: uuid.New().String(),
		// The scheduler acts as its own service principal, so the outbound
		// message carries that principal's workspace rather than inheriting
		// one from the source: a scheduled delivery has no request behind it,
		// and the channel-ownership check at send time needs a tenant that is
		// actually attributable.
		WorkspaceID: s.principal.WorkspaceID,
		SessionID:   source.SessionID,
		AgentID:     def.ID,
		Channel:     channelID,
		ThreadID:    to,
		UserID:      "scheduler",
		Username:    "scheduler",
		Role:        message.RoleAssistant,
		Parts:       message.Text(text),
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
	return s.entriesIn(crossWorkspaceSnapshot)
}

// EntriesInWorkspace lists only the named tenant's schedules.
//
// The unscoped Entries flattens every workspace into one list keyed by agent
// ID, which is what let a Team deployment's Schedule page show another
// tenant's cron times beside its own agent of the same name.
func (s *Scheduler) EntriesInWorkspace(workspaceID string) []ScheduleEntry {
	return s.entriesIn(wsroot.Normalize(workspaceID))
}

func (s *Scheduler) entriesIn(workspaceID string) []ScheduleEntry {
	blocked := s.blocksSnapshot(workspaceID)
	s.mu.Lock()
	defer s.mu.Unlock()
	var entries []ScheduleEntry
	for key, entryID := range s.entries {
		if workspaceID != crossWorkspaceSnapshot && key.workspaceID != workspaceID {
			continue
		}
		agentID := key.agentID
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
			if def := s.loader.GetInWorkspace(key.workspaceID, agentID); def != nil && def.Schedule != nil && def.Schedule.RunMissedOnStartup {
				se.CatchUp = true
				se.CatchUpWindow = strings.TrimSpace(def.Schedule.MissedStartupWindow)
				if se.CatchUpWindow == "" {
					se.CatchUpWindow = "24h" // documented default
				}
			}
		}
		entries = append(entries, se)
	}
	for key := range s.oneshot {
		if workspaceID != crossWorkspaceSnapshot && key.workspaceID != workspaceID {
			continue
		}
		entries = append(entries, ScheduleEntry{
			AgentID: key.agentID,
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

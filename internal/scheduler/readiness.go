package scheduler

// readiness.go — the pre-fire readiness gate (ST-16).
//
// Until this existed the scheduler would fire ANY enabled agent whose cron
// matched, with no check that the thing it was about to run had ever been
// proven to work. The failure mode is specific and expensive: an agent whose
// provider credential was never set, or whose delivery channel has no
// destination, fires at 03:00 every night, burns tokens, produces nothing, and
// tells nobody — because scheduled runs have no human watching them. The only
// protection was that Studio saves start Enabled=false, which protects nothing
// once an operator flips the switch.
//
// The gate is injectable for two reasons. First, testability: a scheduler test
// must be able to drive both verdicts without standing up a deployment store.
// Second and more importantly, a NIL gate must preserve today's behaviour
// exactly — most agents in a workspace are hand-written YAML that never passed
// through Studio and have no certification to consult, and blocking them would
// be a far worse regression than the one this fixes. "No opinion" is therefore
// a first-class answer, distinct from "cleared".
//
// The gate is consulted on EVERY fire, never cached. A blocked schedule that
// stayed blocked until a restart would be indistinguishable from a broken one;
// re-reading each tick means an operator who fixes the blocker and re-certifies
// sees the next fire go through.

import (
	"time"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/pkg/message"
)

// ReadinessRequirement is one unmet precondition, carrying the repair action
// that clears it. A blocked schedule that only says "not ready" sends the
// operator hunting; this is what makes the block actionable.
type ReadinessRequirement struct {
	ID     string `json:"id"`
	Title  string `json:"title,omitempty"`
	Detail string `json:"detail,omitempty"`
	Fix    string `json:"fix,omitempty"`
	Action string `json:"action,omitempty"`
}

// ReadinessVerdict is the gate's answer for one agent.
type ReadinessVerdict struct {
	// Blocked reports that scheduled execution must not proceed.
	Blocked bool `json:"blocked"`
	// Summary is the one-line reason for a log or a status row.
	Summary string `json:"summary,omitempty"`
	// Failed lists every unmet requirement, each with its own fix.
	Failed []ReadinessRequirement `json:"failed,omitempty"`
	// Version is the deployment version the verdict was read from, so an
	// operator can tell which deployment is blocking.
	Version int `json:"version,omitempty"`
}

// ReadinessGate decides whether a scheduled agent is cleared to run.
type ReadinessGate interface {
	// ScheduleReadiness returns the verdict for agentID. ok=false means the
	// gate has NO OPINION about this agent (it was not deployed through Studio,
	// so there is nothing to certify) and the fire proceeds unchanged.
	ScheduleReadiness(agentID string) (verdict ReadinessVerdict, ok bool)
}

// ReadinessGateFunc adapts a plain function to ReadinessGate.
type ReadinessGateFunc func(agentID string) (ReadinessVerdict, bool)

// ScheduleReadiness implements ReadinessGate.
func (f ReadinessGateFunc) ScheduleReadiness(agentID string) (ReadinessVerdict, bool) {
	if f == nil {
		return ReadinessVerdict{}, false
	}
	return f(agentID)
}

// ScheduleBlock is the retained record of the most recent refusal to fire, so
// /schedule/status can render "did not run at 03:00 because …" without the
// operator having to correlate log lines. Kept per agent and cleared as soon as
// the agent passes, so a stale block can never keep explaining a schedule that
// is now healthy.
type ScheduleBlock struct {
	AgentID string                 `json:"agent_id"`
	At      time.Time              `json:"at"`
	Trigger string                 `json:"trigger,omitempty"`
	Summary string                 `json:"summary,omitempty"`
	Version int                    `json:"version,omitempty"`
	Failed  []ReadinessRequirement `json:"failed,omitempty"`
}

// SetReadinessGate installs the certification gate consulted before every
// scheduled fire. Passing nil restores the ungated behaviour. Safe to call once
// at startup.
func (s *Scheduler) SetReadinessGate(g ReadinessGate) {
	s.gateMu.Lock()
	s.gate = g
	s.gateMu.Unlock()
}

// LastBlock returns the most recent refusal to fire for agentID, if the agent
// is currently blocked.
func (s *Scheduler) LastBlock(agentID string) (ScheduleBlock, bool) {
	s.gateMu.RLock()
	defer s.gateMu.RUnlock()
	b, ok := s.blocks[agentID]
	return b, ok
}

// BlocksSnapshot returns a copy of every currently-blocked agent, so the
// Schedule page can render all of them in one round trip.
func (s *Scheduler) BlocksSnapshot() map[string]ScheduleBlock {
	s.gateMu.RLock()
	defer s.gateMu.RUnlock()
	out := make(map[string]ScheduleBlock, len(s.blocks))
	for k, v := range s.blocks {
		out[k] = v
	}
	return out
}

// blockedByReadiness consults the gate and, when the agent is not cleared,
// records and announces the refusal. Returns true when the fire must be
// abandoned.
//
// Ordering matters: this runs BEFORE the run lock is taken, so a blocked agent
// never occupies the single-run slot and a concurrent manual run is unaffected.
func (s *Scheduler) blockedByReadiness(agentID, triggerType string) bool {
	s.gateMu.RLock()
	gate := s.gate
	s.gateMu.RUnlock()
	if gate == nil {
		return false
	}

	verdict, ok := gate.ScheduleReadiness(agentID)
	if !ok || !verdict.Blocked {
		// Cleared (or not our business). Clearing here — rather than only on a
		// successful run — is what lets a fixed agent stop advertising a stale
		// blocker on the very next tick.
		s.clearBlock(agentID, triggerType, ok)
		return false
	}

	now := time.Now().UTC()
	block := ScheduleBlock{
		AgentID: agentID,
		At:      now,
		Trigger: triggerType,
		Summary: verdict.Summary,
		Version: verdict.Version,
		Failed:  verdict.Failed,
	}
	s.gateMu.Lock()
	if s.blocks == nil {
		s.blocks = make(map[string]ScheduleBlock)
	}
	s.blocks[agentID] = block
	s.gateMu.Unlock()

	s.log.Warn("scheduled run BLOCKED — the agent is not certified for scheduled execution",
		zap.String("agent", agentID),
		zap.String("trigger", triggerType),
		zap.Int("deployment_version", verdict.Version),
		zap.String("summary", verdict.Summary),
		zap.Strings("failed_requirements", requirementIDs(verdict.Failed)))

	s.emitReadinessEvent("schedule.blocked", agentID, now, map[string]any{
		"trigger":            triggerType,
		"summary":            verdict.Summary,
		"deployment_version": verdict.Version,
		"failed":             requirementPayload(verdict.Failed),
		"failed_ids":         requirementIDs(verdict.Failed),
		"reason":             "This agent's schedule did not fire because its deployment is not certified. Each failed requirement below carries the action that clears it.",
		"runbook":            "Open the agent in Studio, clear every failed requirement (run it live once if it has never run for real), then re-deploy. The next scheduled fire is checked again — no restart needed.",
	})
	return true
}

// clearBlock drops a recorded block and, if there was one, announces the
// recovery. Silence on recovery would leave the GUI showing a permanent red
// state for a schedule that is running fine again.
func (s *Scheduler) clearBlock(agentID, triggerType string, gateHadOpinion bool) {
	s.gateMu.Lock()
	prev, had := s.blocks[agentID]
	if had {
		delete(s.blocks, agentID)
	}
	s.gateMu.Unlock()
	if !had {
		return
	}
	now := time.Now().UTC()
	s.log.Info("scheduled run unblocked — the agent now passes its readiness check",
		zap.String("agent", agentID),
		zap.String("trigger", triggerType),
		zap.String("previous_summary", prev.Summary))
	s.emitReadinessEvent("schedule.unblocked", agentID, now, map[string]any{
		"trigger":           triggerType,
		"previous_summary":  prev.Summary,
		"previously_failed": requirementIDs(prev.Failed),
		"gate_applies":      gateHadOpinion,
		"reason":            "The readiness check now passes, so this schedule fires normally again.",
	})
}

func (s *Scheduler) emitReadinessEvent(kind, agentID string, at time.Time, payload map[string]any) {
	s.mu.Lock()
	sink := s.sink
	s.mu.Unlock()
	if sink == nil {
		return
	}
	sink.Emit(message.Event{
		Type:      kind,
		AgentID:   agentID,
		Timestamp: at,
		Payload:   payload,
	})
}

func requirementIDs(reqs []ReadinessRequirement) []string {
	out := make([]string, 0, len(reqs))
	for _, r := range reqs {
		out = append(out, r.ID)
	}
	return out
}

// requirementPayload renders the failed requirements as plain maps so the event
// serialises identically regardless of who consumes it.
func requirementPayload(reqs []ReadinessRequirement) []map[string]string {
	out := make([]map[string]string, 0, len(reqs))
	for _, r := range reqs {
		out = append(out, map[string]string{
			"id":     r.ID,
			"title":  r.Title,
			"detail": r.Detail,
			"fix":    r.Fix,
			"action": r.Action,
		})
	}
	return out
}

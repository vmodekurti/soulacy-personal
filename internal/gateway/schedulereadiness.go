package gateway

import (
	"fmt"
	"strings"

	"github.com/soulacy/soulacy/pkg/agent"
)

// scheduleReadiness reports whether the scheduler surface — cron-triggered
// agents, their enabled state, and their delivery targets — is production-ready.
// It complements agentReadinessCounts by turning the raw scheduled-agent count
// into a first-class readiness check with actionable next-steps, so a launch
// gate can call out schedules that "fire but drop their output".
type scheduleReadiness struct {
	Status      string   `json:"status"`
	Total       int      `json:"total"`
	Enabled     int      `json:"enabled"`
	Delivering  int      `json:"delivering"`
	Overdue     int      `json:"overdue"`
	Detail      string   `json:"detail"`
	NextActions []string `json:"next_actions,omitempty"`
}

func (s *Server) scheduleReadiness() scheduleReadiness {
	out := scheduleReadiness{Status: "warn"}
	if s == nil || s.loader == nil {
		out.Status = "fail"
		out.Detail = "Agent loader is unavailable, so schedules could not be verified."
		out.NextActions = []string{"Restart the gateway and re-open the Dashboard."}
		return out
	}
	for _, def := range s.loader.All() {
		if def == nil || s.loader.IsBuiltin(def.ID) {
			continue
		}
		if !isScheduledAgentDef(def) {
			continue
		}
		out.Total++
		if !def.Enabled {
			continue
		}
		out.Enabled++
		if s.scheduler != nil && s.scheduler.HasScheduledOutputTarget(def) {
			out.Delivering++
		}
	}
	// Overdue is a scheduler-liveness signal: any cron entry whose next-fire
	// slot has not been populated indicates the cron runtime is not scheduling
	// that agent. Kept as a soft signal — the scheduler itself decides Next.
	if s.scheduler != nil {
		for _, e := range s.scheduler.Entries() {
			if strings.EqualFold(e.Type, "oneshot") {
				continue
			}
			if e.Next.IsZero() {
				out.Overdue++
			}
		}
	}
	switch {
	case out.Total == 0:
		out.Status = "warn"
		out.Detail = "No scheduled agents yet. Automation is idle until at least one agent has a cron schedule."
		out.NextActions = []string{"Open an agent in Studio and add a cron schedule, or create one from an Automations template."}
	case out.Enabled == 0:
		out.Status = "warn"
		out.Detail = plural(out.Total, "scheduled agent") + " defined but none are enabled; the scheduler will not fire them."
		out.NextActions = []string{"Enable at least one scheduled agent from Agents."}
	case out.Overdue > 0:
		out.Status = "fail"
		out.Detail = fmt.Sprintf("%d enabled scheduled agent(s) have no next fire time — the scheduler may be stalled.", out.Overdue)
		out.NextActions = []string{"Restart the gateway and re-check Schedule; if it recurs, capture logs and file a bug."}
	case out.Delivering == 0:
		out.Status = "warn"
		out.Detail = plural(out.Enabled, "scheduled agent") + " enabled but none have a resolvable delivery target; runs fire but output is dropped."
		out.NextActions = []string{"Set schedule.output.channel on each scheduled agent, or configure a channel default outbound destination."}
	case out.Delivering < out.Enabled:
		out.Status = "warn"
		out.Detail = fmt.Sprintf("%d/%d enabled scheduled agents have a delivery target; the rest fire but their output is dropped.", out.Delivering, out.Enabled)
		out.NextActions = []string{"Add schedule.output.channel (or a channel default) for each scheduled agent without a target."}
	default:
		out.Status = "ok"
		out.Detail = plural(out.Enabled, "scheduled agent") + " enabled and configured to deliver."
	}
	return out
}

func isScheduledAgentDef(def *agent.Definition) bool {
	if def == nil {
		return false
	}
	if def.Trigger == agent.TriggerCron {
		return true
	}
	if def.Schedule != nil && strings.TrimSpace(def.Schedule.Cron) != "" {
		return true
	}
	return false
}

func scheduleReadinessItem(sched scheduleReadiness) readinessItem {
	return readinessItem{
		Key:    "schedules",
		Label:  "Schedules",
		Status: sched.Status,
		Detail: sched.Detail,
		Href:   "#schedule",
	}
}

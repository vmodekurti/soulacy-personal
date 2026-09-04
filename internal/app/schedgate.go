package app

// schedgate.go — wiring for the scheduler's readiness gate (ST-16).
//
// The adapter lives HERE rather than in either package it joins, so that
// internal/studio stays a pure, file-backed library with no scheduler
// dependency, and internal/scheduler keeps knowing nothing about Studio,
// certification or deployment records. The app is the one layer that is
// already allowed to see both.

import (
	"github.com/soulacy/soulacy/internal/scheduler"
	"github.com/soulacy/soulacy/internal/studio"
)

// deploymentReadinessGate adapts the Studio deployment history to the
// scheduler's readiness gate.
//
// The ok=false path is the important one: an agent with NO deployment record
// was never created through Studio, so there is nothing to certify and the gate
// must have no opinion. Returning "blocked" for those agents would silently
// stop every hand-written YAML cron agent in the workspace — a far worse
// failure than the one the gate exists to prevent.
func deploymentReadinessGate(store *studio.DeploymentStore) scheduler.ReadinessGate {
	if store == nil {
		return nil
	}
	return scheduler.ReadinessGateFunc(func(agentID string) (scheduler.ReadinessVerdict, bool) {
		r := store.ScheduleReadiness(agentID)
		if !r.Deployed {
			return scheduler.ReadinessVerdict{}, false
		}
		v := scheduler.ReadinessVerdict{
			Blocked: r.Blocked,
			Summary: r.Summary,
			Version: r.Version,
		}
		for _, req := range r.Failed {
			v.Failed = append(v.Failed, scheduler.ReadinessRequirement{
				ID:     req.ID,
				Title:  req.Title,
				Detail: req.Detail,
				Fix:    req.Fix,
				Action: req.Action,
			})
		}
		return v, true
	})
}

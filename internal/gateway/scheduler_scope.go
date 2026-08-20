package gateway

import (
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/internal/scheduler"
	"github.com/soulacy/soulacy/pkg/agent"
)

// scheduler_scope.go — the scheduler half of the tenant boundary, mirroring
// agentScope and studioScope.
//
// MU-023 made every map in internal/scheduler composite-keyed, because agent
// IDs are unique per workspace and an agent-ID key silently merged two
// tenants' schedules. It also kept the workspace-free methods, whose own
// comment says they are "the ones a caller should not be reaching for" in a
// multi-user deployment.
//
// Every one of the gateway's twenty-two scheduler calls was reaching for
// exactly those. So the fix landed inside the package and was undone at its
// boundary: in Team mode a save by tenant B registered B's agent under the
// SCHEDULER's principal workspace, which is one value for the whole process.
// Two tenants with a "daily-report" collided again — one cron entry, one run
// lock, one failure counter — with the added twist that whichever registration
// arrived last silently replaced the other's schedule.
//
// The scope exists so that cannot be re-introduced by writing the obvious
// thing. A handler cannot reach the scheduler without first naming a
// workspace, because the only way to get here is through a request whose
// workspace the server already verified.
type schedulerScope struct {
	sched       *scheduler.Scheduler
	workspaceID string
}

// schedules returns the request's scoped view of the scheduler.
//
// Resolution matches s.agents(c) exactly, deliberately: an agent registered
// through one and scheduled through the other must land in the same tenant, and
// two different resolutions is how they drift.
func (s *Server) schedules(c *fiber.Ctx) schedulerScope {
	scope := schedulerScope{sched: s.scheduler, workspaceID: runtime.PersonalWorkspaceID}
	if c == nil {
		return scope
	}
	if identity, ok := requestIdentity(c); ok {
		scope.workspaceID = runtime.NormalizeWorkspace(identity.WorkspaceID())
	}
	return scope
}

// schedulesForWorkspace scopes the scheduler without a request, for background
// work holding a workspace it obtained from the loader rather than from input.
func (s *Server) schedulesForWorkspace(workspaceID string) schedulerScope {
	return schedulerScope{sched: s.scheduler, workspaceID: runtime.NormalizeWorkspace(workspaceID)}
}

func (a schedulerScope) Register(def *agent.Definition) error {
	if a.sched == nil {
		return nil
	}
	return a.sched.RegisterAgentInWorkspace(a.workspaceID, def)
}

func (a schedulerScope) Deregister(agentID string) {
	if a.sched == nil {
		return
	}
	a.sched.DeregisterAgentInWorkspace(a.workspaceID, agentID)
}

func (a schedulerScope) TryStartRun(agentID string) bool {
	if a.sched == nil {
		return true
	}
	return a.sched.TryStartRunInWorkspace(a.workspaceID, agentID)
}

func (a schedulerScope) FinishRun(agentID string) {
	if a.sched == nil {
		return
	}
	a.sched.FinishRunInWorkspace(a.workspaceID, agentID)
}

func (a schedulerScope) IsRunning(agentID string) bool {
	if a.sched == nil {
		return false
	}
	return a.sched.IsRunningInWorkspace(a.workspaceID, agentID)
}

func (a schedulerScope) Entries() []scheduler.ScheduleEntry {
	if a.sched == nil {
		return nil
	}
	return a.sched.EntriesInWorkspace(a.workspaceID)
}

func (a schedulerScope) RunningSnapshot() map[string]time.Time {
	if a.sched == nil {
		return nil
	}
	return a.sched.RunningSnapshotInWorkspace(a.workspaceID)
}

func (a schedulerScope) LastBackfills() map[string]scheduler.MissedBackfill {
	if a.sched == nil {
		return nil
	}
	return a.sched.LastBackfillsInWorkspace(a.workspaceID)
}

// WorkspaceID exposes the bound workspace for callers that must record or
// namespace something alongside the schedule.
func (a schedulerScope) WorkspaceID() string { return a.workspaceID }

// readiness_workspace_test.go — the two maps MU-023 re-keyed last, and the
// readiness gate that reads them.
//
// `blocks` and `lastBackfills` kept their agent-ID keys after entries,
// running, failCounts and the state file had all become composite, and the
// package's own tenancy.go comment already claimed otherwise. A doc comment
// that describes a fix the code did not receive is worse than no comment: it
// is the thing a reviewer checks instead of the code.
package scheduler

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/pkg/agent"
)

// gateFor answers per workspace, which is the whole question: before the
// workspace was a parameter, this gate could not have been written.
type gateFor map[string]bool // workspaceID → blocked

func (g gateFor) ScheduleReadiness(workspaceID, agentID string) (ReadinessVerdict, bool) {
	blocked, known := g[workspaceID]
	if !known {
		return ReadinessVerdict{}, false
	}
	return ReadinessVerdict{Blocked: blocked, Summary: "verdict for " + workspaceID}, true
}

func scheduledDef(id string) *agent.Definition {
	return &agent.Definition{
		ID: id, Enabled: true, Trigger: agent.TriggerCron,
		Schedule: &agent.Schedule{Cron: "0 7 * * *"},
	}
}

// The gate is handed the workspace the fire belongs to, not the scheduler's
// own principal workspace. Without this, one tenant's Studio certification
// cleared every other tenant's same-named agent.
func TestTheReadinessGateIsAskedAboutTheFiringWorkspace(t *testing.T) {
	s := New(nil, nil, zap.NewNop(), context.Background())
	asked := map[string]int{}
	s.SetReadinessGate(ReadinessGateFunc(func(workspaceID, agentID string) (ReadinessVerdict, bool) {
		asked[workspaceID]++
		return ReadinessVerdict{Blocked: true, Summary: "held"}, true
	}))

	s.blockedByReadiness(keyFor("ws-b", "daily-report"), "cron")

	if asked["ws-b"] != 1 {
		t.Fatalf("the gate was not asked about ws-b: %v", asked)
	}
	if len(asked) != 1 {
		t.Fatalf("the gate was asked about a workspace that was not firing: %v", asked)
	}
}

// A block recorded for one tenant must not be visible to another, and — the
// sharper half — must not be DELETED by another. clearBlock runs on every
// cleared tick, so with an agent-ID key tenant A's healthy agent silently
// erased tenant B's recorded refusal, leaving B's Schedule page green for a
// schedule that was still not firing.
func TestOneWorkspacesClearedScheduleDoesNotEraseAnothersBlock(t *testing.T) {
	s := New(nil, nil, zap.NewNop(), context.Background())
	s.SetReadinessGate(gateFor{"ws-blocked": true, "ws-clear": false})

	if !s.blockedByReadiness(keyFor("ws-blocked", "daily-report"), "cron") {
		t.Fatal("the blocked workspace was allowed to fire")
	}
	if _, ok := s.LastBlockInWorkspace("ws-blocked", "daily-report"); !ok {
		t.Fatal("the refusal was not recorded")
	}

	// The other tenant's same-named agent passes, which runs clearBlock.
	if s.blockedByReadiness(keyFor("ws-clear", "daily-report"), "cron") {
		t.Fatal("the cleared workspace was refused")
	}

	if _, ok := s.LastBlockInWorkspace("ws-blocked", "daily-report"); !ok {
		t.Fatal("another workspace's passing tick erased this workspace's block")
	}
	if _, ok := s.LastBlockInWorkspace("ws-clear", "daily-report"); ok {
		t.Fatal("the cleared workspace is reported as blocked")
	}
	if snap := s.BlocksSnapshotInWorkspace("ws-clear"); len(snap) != 0 {
		t.Fatalf("ws-clear sees another tenant's blocks: %v", snap)
	}
	if snap := s.BlocksSnapshotInWorkspace("ws-blocked"); len(snap) != 1 {
		t.Fatalf("ws-blocked cannot see its own block: %v", snap)
	}
}

// A startup catch-up belongs to the workspace whose schedule was missed. Keyed
// by agent alone, the GUI chip saying "auto-replayed at 03:04" appeared for
// every tenant owning an agent of that name, naming a run that never happened
// in the workspace looking at it.
func TestABackfillIsReportedOnlyToItsOwnWorkspace(t *testing.T) {
	s := New(nil, nil, zap.NewNop(), context.Background())
	def := scheduledDef("daily-report")
	missedAt := time.Now().Add(-2 * time.Hour).UTC()

	s.emitMissedRunBackfilled(keyFor("ws-a", def.ID), def, missedAt, time.Now().UTC())

	if _, ok := s.LastBackfillInWorkspace("ws-a", def.ID); !ok {
		t.Fatal("the backfill was not recorded for the workspace it happened in")
	}
	if _, ok := s.LastBackfillInWorkspace("ws-b", def.ID); ok {
		t.Fatal("another workspace is told about a catch-up that did not happen in it")
	}
	if snap := s.LastBackfillsInWorkspace("ws-b"); len(snap) != 0 {
		t.Fatalf("ws-b sees another tenant's backfills: %v", snap)
	}
	if snap := s.LastBackfillsInWorkspace("ws-a"); len(snap) != 1 {
		t.Fatalf("ws-a cannot see its own backfill: %v", snap)
	}
}

// Entries and RunningSnapshot are what the Schedule page renders. Unscoped
// they flatten every workspace into one agent-ID-keyed answer, so two tenants
// with a "daily-report" see one row carrying the other's cron time.
func TestTheScheduleViewShowsOnlyTheAskingWorkspace(t *testing.T) {
	s := New(nil, nil, zap.NewNop(), context.Background())
	for _, ws := range []string{"ws-a", "ws-b"} {
		if err := s.RegisterAgentInWorkspace(ws, scheduledDef("daily-report")); err != nil {
			t.Fatalf("%s: %v", ws, err)
		}
	}
	if !s.TryStartRunInWorkspace("ws-a", "daily-report") {
		t.Fatal("ws-a could not start its run")
	}

	if got := s.EntriesInWorkspace("ws-a"); len(got) != 1 {
		t.Fatalf("ws-a sees %d entries, want exactly its own: %v", len(got), got)
	}
	if got := len(s.Entries()); got != 2 {
		t.Fatalf("the deployment-wide view lost an entry: %d", got)
	}
	if got := s.RunningSnapshotInWorkspace("ws-b"); len(got) != 0 {
		t.Fatalf("ws-b is shown ws-a's running agent: %v", got)
	}
	if got := s.RunningSnapshotInWorkspace("ws-a"); len(got) != 1 {
		t.Fatalf("ws-a cannot see its own running agent: %v", got)
	}
	// And the run lock itself is per tenant, so ws-b is not blocked by ws-a.
	if !s.TryStartRunInWorkspace("ws-b", "daily-report") {
		t.Fatal("ws-a's run blocked ws-b's same-named agent")
	}
	if !s.IsRunningInWorkspace("ws-b", "daily-report") {
		t.Fatal("ws-b's run was not recorded")
	}
	s.FinishRunInWorkspace("ws-b", "daily-report")
	if s.IsRunningInWorkspace("ws-b", "daily-report") {
		t.Fatal("ws-b's run was not released")
	}
	if !s.IsRunningInWorkspace("ws-a", "daily-report") {
		t.Fatal("finishing ws-b's run released ws-a's")
	}
}

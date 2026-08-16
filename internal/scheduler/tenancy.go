package scheduler

import (
	"context"
	"errors"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/internal/schedules"
	"github.com/soulacy/soulacy/internal/wsroot"
)

// tenancy.go — MU-023 part 2: which workspace a scheduled fire belongs to, and
// which process is allowed to run it.
//
// THE KEY WAS THE BUG. Every map in this package was keyed by agent ID:
// entries, running, failCounts, blocks, lastBackfills, and the completed-fire
// state file. Agent IDs are unique per WORKSPACE, not per deployment — the
// loader has said so since MU-012 — so two tenants with a "daily-report" agent
// shared one cron entry, one run lock, one failure counter and one last-fire
// timestamp. Registering the second replaced the first, and whichever survived
// ran under a principal belonging to neither.
//
// Making the key composite is most of the fix, and it is the kind of fix that
// has to be structural: a scoping *check* added beside a lookup is one a future
// caller forgets, but a key that does not compile without a workspace is not.

// scheduleKey identifies one agent's schedule within one workspace.
//
// A struct rather than a joined string so the compiler refuses a bare agent ID
// where a key is wanted. The old code's bug was passing exactly that.
type scheduleKey struct {
	workspaceID string
	agentID     string
}

func keyFor(workspaceID, agentID string) scheduleKey {
	return scheduleKey{workspaceID: wsroot.Normalize(workspaceID), agentID: strings.TrimSpace(agentID)}
}

// String is the form used in the durable state file and in log fields.
func (k scheduleKey) String() string { return k.workspaceID + "/" + k.agentID }

// defaultWorkspace is the workspace an agent-ID-only call refers to.
//
// It is the scheduler's own principal workspace, which in a personal
// deployment is the only one there is — so the legacy single-argument methods
// keep behaving exactly as they always did (invariant 7). In a multi-user
// deployment those methods are the ones a caller should not be reaching for,
// and the ...InWorkspace variants exist beside each of them.
func (s *Scheduler) defaultWorkspace() string {
	return wsroot.Normalize(s.principal.WorkspaceID)
}

// SetScheduleStore wires the durable, workspace-scoped schedule record and the
// identity of this gateway process.
//
// instanceID must differ between processes. Two instances sharing one would be
// indistinguishable to the claim: each would look like the other re-entering,
// so a lease steal could not be told from a retry and the exactly-once
// guarantee would quietly become at-least-once.
func (s *Scheduler) SetScheduleStore(store *schedules.Store, instanceID string) {
	s.mu.Lock()
	s.store = store
	s.instanceID = strings.TrimSpace(instanceID)
	s.mu.Unlock()
}

func (s *Scheduler) scheduleStore() (*schedules.Store, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.store, s.instanceID
}

// claimOccurrence decides whether THIS process runs this occurrence.
//
// Returns the claim when it won, and ok=false when another instance owns it.
// With no store configured there is nothing to coordinate with — a single
// personal gateway is the only instance — so it always wins, which is what
// keeps invariant 7.
//
// The claim happens BEFORE the run lock, the definition lookup and the
// readiness gate, for the same reason the readiness gate happens before the
// provider is dialled: an instance that is not going to run this occurrence
// must not consume anything deciding that.
func (s *Scheduler) claimOccurrence(key scheduleKey, scheduledAt time.Time) (schedules.Occurrence, bool) {
	store, instanceID := s.scheduleStore()
	if store == nil {
		return schedules.Occurrence{}, true
	}
	if instanceID == "" {
		// The store refuses an anonymous claimant too; this check exists so
		// the failure names the WIRING mistake rather than surfacing as a
		// generic claim error at 03:00. Two instances sharing one identity —
		// or having none — are indistinguishable to the claim, so a lease
		// steal could not be told from a retry.
		s.log.Error("scheduled fire refused: the schedule store is wired without an instance id",
			zap.String("schedule", key.String()))
		return schedules.Occurrence{}, false
	}
	ctx, cancel := context.WithTimeout(s.appCtx, 10*time.Second)
	defer cancel()

	claim, err := store.Claim(ctx, key.workspaceID, key.agentID, scheduledAt, instanceID, 0)
	switch {
	case err == nil:
		return claim, true
	case errors.Is(err, schedules.ErrAlreadyClaimed):
		// Not an error. Exactly one instance is meant to lose, and this is
		// how the loser finds out. Debug rather than Warn: on a two-instance
		// deployment this fires on half of all occurrences, and a log line
		// that appears every time nothing is wrong trains people to ignore it.
		s.log.Debug("scheduled occurrence claimed by another instance",
			zap.String("schedule", key.String()), zap.Time("scheduled_at", scheduledAt))
		return schedules.Occurrence{}, false
	default:
		// A store that cannot be reached must not become "fire anyway". Two
		// instances that both fail to claim would both fire, which is the
		// duplicate execution this whole mechanism exists to prevent — and a
		// missed occurrence is recoverable where a duplicated side effect is
		// not.
		s.log.Error("scheduled fire refused: the occurrence could not be claimed",
			zap.String("schedule", key.String()), zap.Error(err))
		return schedules.Occurrence{}, false
	}
}

// completeOccurrence records the outcome of a claimed occurrence.
func (s *Scheduler) completeOccurrence(claim schedules.Occurrence, runErr error) {
	store, _ := s.scheduleStore()
	if store == nil || claim.Key == "" {
		return
	}
	// context.WithoutCancel: a run cancelled by shutdown still happened, and
	// leaving its occurrence in `claimed` would let the next process steal and
	// re-run it once the lease expired.
	if err := store.Complete(context.WithoutCancel(s.appCtx), claim, runErr); err != nil {
		s.log.Warn("scheduled occurrence outcome could not be recorded",
			zap.String("schedule", claim.WorkspaceID+"/"+claim.ScheduleID), zap.Error(err))
	}
}

// principalFor builds the identity a scheduled run acts under in one
// workspace.
//
// Derived per fire rather than read from one process-wide field. The single
// s.principal was why a multi-user deployment could only be correct by
// refusing to fire at all: every workspace's schedules would otherwise have
// executed as whichever tenant happened to be configured.
func (s *Scheduler) principalFor(workspaceID string) runtime.Principal {
	principal := s.principal
	workspaceID = wsroot.Normalize(workspaceID)
	if workspaceID != "" && workspaceID != wsroot.Normalize(principal.WorkspaceID) {
		// A workspace other than the configured one gets a service principal
		// scoped to ITSELF. Inheriting the configured workspace's
		// organization or membership would carry authority across the tenant
		// boundary — the membership id in particular is a grant.
		principal = runtime.Principal{
			Subject: "scheduler", WorkspaceID: workspaceID,
			Role: "admin", CredentialID: "service:scheduler", Kind: "service",
		}
	}
	return principal
}

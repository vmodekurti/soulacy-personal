package learning

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/wsroot"
	"github.com/soulacy/soulacy/pkg/agent"
)

// revalidate.go — MU-025 criterion 5: "background jobs revalidate workspace
// status and policy before committing."
//
// WHY REVALIDATION IS NOT THE SAME AS ADMISSION. The sweep reads an agent
// definition at the top of a loop, tails thousands of events, builds
// proposals, and only then writes. Everything it decided at the top can have
// stopped being true by the time it writes: an operator turns learning off, a
// workspace is suspended, an agent is deleted. A job that checked once at the
// start and committed at the end is enforcing a policy that was current when
// it began — which for a six-hour sweep interval is exactly the policy nobody
// is looking at.
//
// This is the same shape as MU-022's approval eligibility: authority is
// evaluated at the moment of the decision, not at the moment of the request.
// A learning proposal is a candidate rule that changes agent behaviour once
// accepted, so committing one for a workspace that has since been suspended
// puts a suggestion in a queue nobody should be reading.
//
// WHAT IS AND IS NOT CHECKED HERE. The agent half is checked properly: the
// definition is re-read from the workspace at commit time rather than trusted
// from the loop variable. The WORKSPACE-STATUS half is a hook, because
// workspaces have no lifecycle column on this branch — `memberships` has
// active/suspended/deleted and `workspaces` has nothing. Rather than invent a
// status to check against, the gate takes a WorkspaceStatus function an
// operator (or MU-032, when workspace lifecycle lands) supplies. Unset means
// "no status to check", which is the accurate answer today rather than a
// pretend one.

// ErrWorkspaceNotCommittable reports that a workspace may not receive derived
// learning right now.
type ErrWorkspaceNotCommittable struct {
	WorkspaceID string
	Reason      string
}

func (e *ErrWorkspaceNotCommittable) Error() string {
	return fmt.Sprintf("learning: workspace %s may not receive proposals: %s", e.WorkspaceID, e.Reason)
}

// WorkspaceStatusFunc reports whether a workspace may currently receive
// derived learning, and why not when it may not.
//
// Returning an error is the fail-closed answer: a status source that cannot be
// reached must not be read as "everything is fine". A background job that
// skips a sweep is recoverable; one that writes into a suspended tenant is a
// disclosure with a paper trail.
type WorkspaceStatusFunc func(ctx context.Context, workspaceID string) error

// SetWorkspaceStatus installs the workspace-status revalidation hook.
//
// nil disables the check, which is correct for a personal deployment: there is
// one workspace, it is the one running the sweep, and it cannot be suspended
// from under itself.
func (s *Sweeper) SetWorkspaceStatus(fn WorkspaceStatusFunc) {
	s.workspaceStatus = fn
}

// revalidateWorkspace is called once per workspace, immediately before any of
// its proposals are written.
func (s *Sweeper) revalidateWorkspace(ctx context.Context, workspaceID string) error {
	if s.workspaceStatus == nil {
		return nil
	}
	if err := s.workspaceStatus(ctx, wsroot.Normalize(workspaceID)); err != nil {
		return err
	}
	return nil
}

// revalidateAgent re-reads the agent from ITS OWN workspace at commit time and
// reports whether its learning policy still permits a proposal.
//
// Re-read rather than re-checked: the loop's `def` is a pointer captured
// before the tail, and the definition it points at is not the one a later
// reader would load. Checking `def.Learning.Enabled` again on the same pointer
// tests nothing that was not already true.
func (s *Sweeper) revalidateAgent(workspaceID string, def *agent.Definition) (*agent.Definition, error) {
	if def == nil {
		return nil, fmt.Errorf("learning: no agent to revalidate")
	}
	current := s.currentAgent(workspaceID, def.ID)
	if current == nil {
		// Deleted, renamed, or moved between the tail and the write. Its
		// evidence is still real, but there is nothing left for a proposal to
		// be about.
		return nil, fmt.Errorf("learning: agent %s no longer exists in workspace %s", def.ID, workspaceID)
	}
	if !current.Learning.Enabled || !current.Learning.AutoPropose {
		return nil, fmt.Errorf("learning: agent %s no longer permits automatic proposals", def.ID)
	}
	return current, nil
}

// currentAgent loads an agent from one workspace, or nil.
func (s *Sweeper) currentAgent(workspaceID, agentID string) *agent.Definition {
	agentID = strings.TrimSpace(agentID)
	for _, candidate := range s.agentsIn(workspaceID) {
		if candidate != nil && candidate.ID == agentID {
			return candidate
		}
	}
	return nil
}

// logSkip records a revalidation refusal.
//
// Logged rather than returned as a sweep error: one workspace becoming
// uncommittable must not abort the sweep for every other tenant, which is the
// failure mode a bare `return err` produces in a loop over workspaces.
func (s *Sweeper) logSkip(workspaceID string, err error) {
	s.log.Info("learning proposals skipped after revalidation",
		zap.String("workspace_id", workspaceID), zap.Error(err))
}

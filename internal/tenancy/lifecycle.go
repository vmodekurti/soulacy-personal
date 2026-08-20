package tenancy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// lifecycle.go — MU-032 criterion 3's durable half: the workspace's own
// lifecycle, separate from its members'.
//
// `memberships` has had active/suspended/deleted since M2. `workspaces` did
// not, and the absence was load-bearing in a way that is easy to miss: with no
// workspace status there is nothing for MU-025's background collectors to
// revalidate against, and no way for a deletion to STOP writes while the
// recovery window runs. A deletion that begins by removing data is a deletion
// nobody can undo; a deletion that begins by refusing writes is one that can.

// WorkspaceRecord is the workspace's lifecycle state.
type WorkspaceRecord struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	Name           string    `json:"name"`
	Status         string    `json:"status"`
	RequestedAt    time.Time `json:"deletion_requested_at,omitempty"`
	RecoverUntil   time.Time `json:"recoverable_until,omitempty"`
}

// Recoverable reports whether a deletion can still be cancelled.
//
// Computed from the stored deadline rather than from a flag, so a process that
// was not running when the window closed still answers correctly the moment it
// starts. A boolean written at request time would be wrong for exactly as long
// as nobody was there to update it, which is the whole duration of the window.
func (w WorkspaceRecord) Recoverable(now time.Time) bool {
	return strings.EqualFold(w.Status, WorkspaceDeleting) &&
		!w.RecoverUntil.IsZero() && now.Before(w.RecoverUntil)
}

// PurgeDue reports whether the recovery window has closed on a workspace whose
// deletion was requested, so the purge may proceed.
func (w WorkspaceRecord) PurgeDue(now time.Time) bool {
	return strings.EqualFold(w.Status, WorkspaceDeleting) &&
		!w.RecoverUntil.IsZero() && !now.Before(w.RecoverUntil)
}

// ErrWorkspaceNotDeleting is returned when a cancellation is asked for on a
// workspace that is not being deleted.
var ErrWorkspaceNotDeleting = errors.New("tenancy: this workspace is not being deleted")

// ErrRecoveryWindowClosed is returned when a cancellation arrives too late.
//
// Distinct from ErrWorkspaceNotDeleting because the two mean opposite things
// to the person asking: one is "there was nothing to cancel", the other is
// "there was, and you have missed it". Collapsing them would tell an owner
// racing the deadline that they had imagined the deletion.
var ErrRecoveryWindowClosed = errors.New("tenancy: the recovery window has closed")

// ErrWorkspaceLifecycleUnavailable is returned by deployments that have no
// workspace lifecycle to manage.
var ErrWorkspaceLifecycleUnavailable = errors.New("tenancy: this deployment does not support workspace deletion")

// WorkspaceLifecycle transitions a workspace between lifecycle states.
//
// A SEPARATE interface from MemberManager, deliberately. Membership
// administration is something an admin does routinely; beginning a workspace
// deletion is the one irreversible action in the product. Folding it into the
// interface every membership handler already holds would mean every one of
// those handlers is one typo away from it.
type WorkspaceLifecycle interface {
	// Workspace returns the current lifecycle record.
	Workspace(ctx context.Context, workspaceID string) (WorkspaceRecord, error)
	// BeginDeletion moves an active workspace to `deleting` and records the
	// recovery deadline. It must refuse a workspace that is not active, so two
	// concurrent requests cannot both start a deletion with different windows.
	BeginDeletion(ctx context.Context, mutation Mutation, workspaceID string, recoverUntil time.Time) (WorkspaceRecord, error)
	// CancelDeletion returns a `deleting` workspace to `active`, and refuses
	// once the recovery window has closed.
	CancelDeletion(ctx context.Context, mutation Mutation, workspaceID string) (WorkspaceRecord, error)
	// CompleteDeletion marks a purged workspace `deleted`. Called after the
	// purge, never before: `deleted` is the state that makes a workspace
	// unreadable, and setting it first would hide the data still being removed
	// from the people entitled to see it go.
	CompleteDeletion(ctx context.Context, mutation Mutation, workspaceID string) (WorkspaceRecord, error)
	// DueForPurge lists workspaces whose recovery window has closed.
	DueForPurge(ctx context.Context, now time.Time, limit int) ([]WorkspaceRecord, error)
	// ClaimPurge takes an exclusive, expiring lease on purging one workspace,
	// returning false when another instance already holds one.
	//
	// A LEASE rather than a lock, and expiring rather than held, because the
	// process that takes it can die mid-purge. A lock released only on a clean
	// exit would leave that workspace unpurgeable forever, with the customer
	// having been told it was deleted; a lease that expires means the next
	// sweep picks it up. The cost is that a hung instance's work can be
	// duplicated after the lease runs out, which is acceptable precisely
	// because every purger is idempotent — a DELETE of rows already gone and a
	// RemoveAll of a directory already removed both succeed.
	ClaimPurge(ctx context.Context, mutation Mutation, workspaceID string, leaseUntil time.Time) (bool, error)
}

// PersonalWorkspaceLifecycle is the Personal deployment's answer: there is one
// workspace, it is the installation, and it has no lifecycle.
//
// Refusing rather than pretending. A personal install deleting its only
// workspace is uninstalling — done by removing the data directory, not by an
// API call — and a no-op success here would report a deletion that did not
// happen (product invariant 7 cuts both ways: Personal must not acquire
// multi-user behaviour, and must not acquire a lie either).
type PersonalWorkspaceLifecycle struct{ tenant PersonalTenant }

func NewPersonalWorkspaceLifecycle(tenant PersonalTenant) *PersonalWorkspaceLifecycle {
	return &PersonalWorkspaceLifecycle{tenant: tenant}
}

func (p *PersonalWorkspaceLifecycle) Workspace(_ context.Context, workspaceID string) (WorkspaceRecord, error) {
	return WorkspaceRecord{
		ID: p.tenant.WorkspaceID, OrganizationID: p.tenant.OrganizationID,
		Name: "Personal", Status: WorkspaceActive,
	}, nil
}

func (p *PersonalWorkspaceLifecycle) BeginDeletion(context.Context, Mutation, string, time.Time) (WorkspaceRecord, error) {
	return WorkspaceRecord{}, fmt.Errorf("%w: a personal installation's only workspace is the installation itself", ErrWorkspaceLifecycleUnavailable)
}

func (p *PersonalWorkspaceLifecycle) CancelDeletion(context.Context, Mutation, string) (WorkspaceRecord, error) {
	return WorkspaceRecord{}, ErrWorkspaceLifecycleUnavailable
}

func (p *PersonalWorkspaceLifecycle) CompleteDeletion(context.Context, Mutation, string) (WorkspaceRecord, error) {
	return WorkspaceRecord{}, ErrWorkspaceLifecycleUnavailable
}

// DueForPurge is empty rather than an error: a sweep running on a personal
// installation should find nothing to do and say nothing, not log a failure
// every tick for a feature that deployment does not have.
func (p *PersonalWorkspaceLifecycle) DueForPurge(context.Context, time.Time, int) ([]WorkspaceRecord, error) {
	return nil, nil
}

func (p *PersonalWorkspaceLifecycle) ClaimPurge(context.Context, Mutation, string, time.Time) (bool, error) {
	return false, nil
}

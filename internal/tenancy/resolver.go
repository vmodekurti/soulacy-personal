package tenancy

import (
	"context"
	"errors"
	"strings"
	"time"
)

var ErrMembershipNotFound = errors.New("workspace membership not found")

type Membership struct {
	OrganizationID string
	WorkspaceID    string
	MembershipID   string
	UserID         string
	Role           string

	// WorkspaceStatus is the LIFECYCLE of the workspace itself, distinct from
	// the status of this member's membership in it (MU-032, and the hook
	// MU-025 criterion 5 has been waiting on).
	//
	// The two are genuinely different questions and conflating them is the
	// mistake to avoid: a fully active member of a workspace that is being
	// deleted must not be able to write to it, and a suspended member of a
	// healthy workspace is a different refusal with a different remedy.
	//
	// Resolved on every request alongside the membership rather than looked up
	// separately, because a status read at a different moment from the
	// membership read is a window in which a deletion begins and a write still
	// lands. Empty means WorkspaceActive: the column postdates existing rows,
	// and treating an unset value as "being deleted" would take a deployment
	// offline on upgrade.
	WorkspaceStatus string
}

// Workspace lifecycle states.
//
// Deliberately few. Each one has to answer "what may happen now" differently,
// and a state that answers the same as another is a state nobody can act on.
const (
	// WorkspaceActive is the normal state: everything is permitted.
	WorkspaceActive = "active"
	// WorkspaceSuspended is an administrative hold. Reads are permitted and
	// writes are not — a member has to be able to SEE that the workspace is
	// suspended, and a suspension that hid the workspace would be
	// indistinguishable from having been removed from it.
	WorkspaceSuspended = "suspended"
	// WorkspaceDeleting means deletion has begun and the recovery window has
	// not elapsed (MU-032 criterion 4: "new runs and writes stop when deletion
	// begins"). Reads stay open for the same reason as suspension, and more
	// so: the recovery window is worthless if nobody can look at what is about
	// to be destroyed.
	WorkspaceDeleting = "deleting"
	// WorkspaceDeleted is past the recovery window. Nothing is reachable.
	WorkspaceDeleted = "deleted"
)

// WorkspaceAcceptsWrites reports whether a workspace in this state may be
// changed.
//
// An unknown status is treated as NOT accepting writes. This is the one place
// in the tenancy package that fails closed on an unrecognised value, and the
// reason is asymmetric cost: a status this build does not know about is
// overwhelmingly likely to be one a NEWER build introduced to stop writes, and
// guessing "active" would have an old replica happily writing into a workspace
// a new one is deleting.
func WorkspaceAcceptsWrites(status string) bool {
	switch strings.TrimSpace(strings.ToLower(status)) {
	case "", WorkspaceActive:
		return true
	default:
		return false
	}
}

// WorkspaceIsReadable reports whether a workspace in this state may be looked
// at. Only a fully deleted workspace is not.
func WorkspaceIsReadable(status string) bool {
	return strings.TrimSpace(strings.ToLower(status)) != WorkspaceDeleted
}

// Resolver verifies that an authenticated subject belongs to the requested
// workspace. Callers may use a requested workspace only as a selector; the
// returned membership is the sole authority source.
type Resolver interface {
	ResolveMembership(ctx context.Context, subject, requestedWorkspaceID string) (Membership, error)
}

// PersonalResolver maps every authenticated local credential to the one
// implicit owner membership. It is intentionally valid only for Personal mode.
type PersonalResolver struct{ tenant PersonalTenant }

func NewPersonalResolver(tenant PersonalTenant) *PersonalResolver {
	return &PersonalResolver{tenant: tenant}
}

func (r *PersonalResolver) ResolveMembership(_ context.Context, subject, requestedWorkspaceID string) (Membership, error) {
	if r == nil || strings.TrimSpace(subject) == "" || strings.TrimSpace(r.tenant.WorkspaceID) == "" {
		return Membership{}, ErrMembershipNotFound
	}
	if requested := strings.TrimSpace(requestedWorkspaceID); requested != "" && requested != r.tenant.WorkspaceID {
		return Membership{}, ErrMembershipNotFound
	}
	return Membership{
		OrganizationID: r.tenant.OrganizationID,
		WorkspaceID:    r.tenant.WorkspaceID,
		MembershipID:   r.tenant.MembershipID,
		UserID:         r.tenant.UserID,
		Role:           "owner",
	}, nil
}

// SubjectWorkspace is one workspace an authenticated subject may select, with
// the display metadata a context switcher needs. The role is the stored
// membership role, never a role asserted by the client.
type SubjectWorkspace struct {
	OrganizationID   string `json:"organization_id"`
	OrganizationName string `json:"organization_name,omitempty"`
	WorkspaceID      string `json:"workspace_id"`
	WorkspaceName    string `json:"workspace_name,omitempty"`
	MembershipID     string `json:"membership_id"`
	Role             string `json:"role"`
	PrincipalKind    string `json:"principal_kind"`
	OrganizationLogo string `json:"organization_logo,omitempty"`
	WorkspaceLogo    string `json:"workspace_logo,omitempty"`
}

// WorkspaceLister enumerates the workspaces a subject may act in. It exists so
// a CLI or GUI can offer a context switcher without inferring authority from a
// token claim: the server answers from stored memberships every time.
type WorkspaceLister interface {
	ListSubjectWorkspaces(ctx context.Context, subject string) ([]SubjectWorkspace, error)
}

// WorkspaceCreator creates a workspace and its initial owner membership in
// one transaction. The source workspace is supplied so the store can verify
// that the actor is still an active owner in this organization at commit time;
// a handler check alone would leave a demotion race between authorization and
// the insert.
type WorkspaceCreator interface {
	CreateWorkspaceForOwner(ctx context.Context, mutation Mutation, organizationID, sourceWorkspaceID, name, ownerUserID string) (Workspace, StoredMembership, error)
}

// PlatformCatalog exposes deployment-level tenant metadata to the control
// plane. It intentionally contains counts and lifecycle state only: a host
// operator can diagnose the service without becoming a member of, or reading
// data from, any workspace.
type PlatformCatalog interface {
	PlatformOverview(ctx context.Context) (PlatformOverview, error)
	ListPlatformOrganizations(ctx context.Context) ([]PlatformOrganization, error)
}

// PlatformAuditReader exposes only deployment lifecycle audit metadata. The
// control plane deliberately receives no before/after tenant payloads.
type PlatformAuditReader interface {
	ListPlatformAuditPage(ctx context.Context, limit int, cursor string) (AuditPage, error)
}

// PlatformProvisioner creates tenant boundaries and assigns their initial
// owner. The deployment operator is the actor, never the resulting member.
type PlatformProvisioner interface {
	ProvisionOrganization(ctx context.Context, mutation Mutation, request BootstrapRequest) (BootstrapResult, error)
	ProvisionWorkspace(ctx context.Context, mutation Mutation, organizationID string, request WorkspaceProvisionRequest) (BootstrapResult, error)
}

// SelfServiceProvisioner creates exactly one first tenant for a verified local
// user. Implementations must derive the owner from subject and reject a second
// attempt; callers are not trusted to nominate an arbitrary owner email.
type SelfServiceProvisioner interface {
	ProvisionSelfServiceOrganization(ctx context.Context, mutation Mutation, subject string, request BootstrapRequest) (BootstrapResult, error)
}

// PlatformLifecycleManager changes tenant availability without granting the
// deployment operator membership in tenant data.
type PlatformLifecycleManager interface {
	SetOrganizationStatus(ctx context.Context, mutation Mutation, organizationID, status, reason string) (Organization, error)
	SetWorkspaceStatus(ctx context.Context, mutation Mutation, workspaceID, status, reason string) (WorkspaceRecord, error)
}

type WorkspaceProvisionRequest struct {
	WorkspaceName    string `json:"workspace_name"`
	WorkspaceSlug    string `json:"workspace_slug,omitempty"`
	OwnerEmail       string `json:"owner_email"`
	OwnerDisplayName string `json:"owner_display_name"`
	WorkspaceLogo    string `json:"workspace_logo,omitempty"`
}

type WorkspaceAddressManager interface {
	SetWorkspaceSlug(ctx context.Context, mutation Mutation, workspaceID, slug string) (Workspace, error)
}

type PlatformOverview struct {
	Organizations          int `json:"organizations"`
	SuspendedOrganizations int `json:"suspended_organizations"`
	Workspaces             int `json:"workspaces"`
	ActiveWorkspaces       int `json:"active_workspaces"`
	SuspendedWorkspaces    int `json:"suspended_workspaces"`
	DeletingWorkspaces     int `json:"deleting_workspaces"`
	Users                  int `json:"users"`
	ActiveMemberships      int `json:"active_memberships"`
	PendingInvitations     int `json:"pending_invitations"`
}

type PlatformWorkspace struct {
	ID                 string    `json:"id"`
	Slug               string    `json:"slug"`
	Name               string    `json:"name"`
	Status             string    `json:"status"`
	CreatedAt          time.Time `json:"created_at"`
	ActiveMembers      int       `json:"active_members"`
	PendingInvitations int       `json:"pending_invitations"`
	LogoDataURL        string    `json:"logo_data_url,omitempty"`
	IdentityStatus     string    `json:"identity_status"`
	ProviderType       string    `json:"provider_type,omitempty"`
}

type PlatformOrganization struct {
	ID          string              `json:"id"`
	Name        string              `json:"name"`
	Status      string              `json:"status"`
	CreatedAt   time.Time           `json:"created_at"`
	Workspaces  []PlatformWorkspace `json:"workspaces"`
	LogoDataURL string              `json:"logo_data_url,omitempty"`
}

// ListSubjectWorkspaces returns the single implicit workspace. Personal mode
// has exactly one, so the switcher degrades to a no-op rather than an error.
func (r *PersonalResolver) ListSubjectWorkspaces(_ context.Context, subject string) ([]SubjectWorkspace, error) {
	if r == nil || strings.TrimSpace(subject) == "" || strings.TrimSpace(r.tenant.WorkspaceID) == "" {
		return nil, ErrMembershipNotFound
	}
	return []SubjectWorkspace{{
		OrganizationID: r.tenant.OrganizationID, OrganizationName: "Personal",
		WorkspaceID: r.tenant.WorkspaceID, WorkspaceName: "Personal",
		MembershipID: r.tenant.MembershipID, Role: "owner", PrincipalKind: "user",
	}}, nil
}

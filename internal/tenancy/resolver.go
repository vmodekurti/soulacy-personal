package tenancy

import (
	"context"
	"errors"
	"strings"
)

var ErrMembershipNotFound = errors.New("workspace membership not found")

type Membership struct {
	OrganizationID string
	WorkspaceID    string
	MembershipID   string
	UserID         string
	Role           string
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
}

// WorkspaceLister enumerates the workspaces a subject may act in. It exists so
// a CLI or GUI can offer a context switcher without inferring authority from a
// token claim: the server answers from stored memberships every time.
type WorkspaceLister interface {
	ListSubjectWorkspaces(ctx context.Context, subject string) ([]SubjectWorkspace, error)
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

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

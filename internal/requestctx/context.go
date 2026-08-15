// Package requestctx carries the verified identity and tenant boundary for one
// request. Values can only be created through New, and all returned slices are
// copied, so downstream code cannot mutate another component's authority.
package requestctx

import (
	"context"
	"errors"
	"strings"
)

// Identity is the immutable authority attached to a request after
// authentication and membership resolution.
type Identity struct {
	subject        string
	organizationID string
	workspaceID    string
	membershipID   string
	role           string
	scopes         []string
	credentialID   string
	requestID      string
	principalKind  string
}

type Input struct {
	Subject        string
	OrganizationID string
	WorkspaceID    string
	MembershipID   string
	Role           string
	Scopes         []string
	CredentialID   string
	RequestID      string
	PrincipalKind  string
}

func New(in Input) (Identity, error) {
	in.Subject = strings.TrimSpace(in.Subject)
	in.OrganizationID = strings.TrimSpace(in.OrganizationID)
	in.WorkspaceID = strings.TrimSpace(in.WorkspaceID)
	in.MembershipID = strings.TrimSpace(in.MembershipID)
	in.Role = strings.ToLower(strings.TrimSpace(in.Role))
	in.RequestID = strings.TrimSpace(in.RequestID)
	if in.Subject == "" || in.OrganizationID == "" || in.WorkspaceID == "" || in.MembershipID == "" || in.Role == "" || in.RequestID == "" {
		return Identity{}, errors.New("request identity requires subject, organization, workspace, membership, role, and request ID")
	}
	return Identity{
		subject:        strings.Clone(in.Subject),
		organizationID: strings.Clone(in.OrganizationID),
		workspaceID:    strings.Clone(in.WorkspaceID),
		membershipID:   strings.Clone(in.MembershipID),
		role:           strings.Clone(in.Role),
		scopes:         append([]string(nil), in.Scopes...),
		credentialID:   strings.Clone(strings.TrimSpace(in.CredentialID)),
		requestID:      strings.Clone(in.RequestID),
		principalKind:  strings.Clone(strings.TrimSpace(in.PrincipalKind)),
	}, nil
}

func (i Identity) Subject() string        { return i.subject }
func (i Identity) OrganizationID() string { return i.organizationID }
func (i Identity) WorkspaceID() string    { return i.workspaceID }
func (i Identity) MembershipID() string   { return i.membershipID }
func (i Identity) Role() string           { return i.role }
func (i Identity) CredentialID() string   { return i.credentialID }
func (i Identity) RequestID() string      { return i.requestID }
func (i Identity) PrincipalKind() string  { return i.principalKind }
func (i Identity) Scopes() []string       { return append([]string(nil), i.scopes...) }

type contextKey struct{}

func With(ctx context.Context, identity Identity) context.Context {
	return context.WithValue(ctx, contextKey{}, identity)
}

func From(ctx context.Context) (Identity, bool) {
	identity, ok := ctx.Value(contextKey{}).(Identity)
	return identity, ok
}

package runtime

import (
	"context"
	"strings"

	"github.com/soulacy/soulacy/internal/wsroot"
)

// Principal is the immutable authentication identity supplied by the gateway.
// Message.UserID/Username are user content and are never authority inputs.
type Principal struct {
	Subject        string
	OrganizationID string
	WorkspaceID    string
	MembershipID   string
	Role           string
	Scopes         []string
	CredentialID   string
	RequestID      string
	Kind           string
}

type principalContextKey struct{}

func WithPrincipal(ctx context.Context, p Principal) context.Context {
	p.Subject = strings.Clone(strings.TrimSpace(p.Subject))
	p.Role = strings.Clone(strings.ToLower(strings.TrimSpace(p.Role)))
	p.Scopes = append([]string(nil), p.Scopes...)
	p.OrganizationID = strings.Clone(strings.TrimSpace(p.OrganizationID))
	p.WorkspaceID = strings.Clone(strings.TrimSpace(p.WorkspaceID))
	p.MembershipID = strings.Clone(strings.TrimSpace(p.MembershipID))
	p.CredentialID = strings.Clone(strings.TrimSpace(p.CredentialID))
	p.RequestID = strings.Clone(strings.TrimSpace(p.RequestID))
	p.Kind = strings.Clone(strings.TrimSpace(p.Kind))
	return context.WithValue(ctx, principalContextKey{}, p)
}

func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalContextKey{}).(Principal)
	return p, ok
}

func callerAllowsTool(ctx context.Context, toolName string) bool {
	p, ok := PrincipalFromContext(ctx)
	if !ok {
		return true // scheduler/channel/internal invocations are trusted services
	}
	// Workspace memberships use owner/admin/developer/operator/viewer. Owner is
	// the highest workspace role and must retain the same execution authority as
	// admin; omitting it silently removed every tool from an owner's chat run.
	if p.Role == "owner" || p.Role == "admin" {
		return true
	}
	if isPrivilegedSystemTool(toolName) {
		return p.Role == "operator"
	}
	if strings.HasPrefix(toolName, "mcp__") || strings.HasPrefix(toolName, "plugin__") {
		return p.Role == "operator"
	}
	return p.Role == "developer" || p.Role == "operator" || p.Role == "viewer"
}

// WorkspaceFromContext returns the workspace a run is acting in, falling back
// to the implicit personal workspace when no principal is present.
//
// Scheduler, channel, and internal invocations reach the engine without a
// request principal. Those are Personal-mode paths today; a multi-user
// deployment establishes a service principal before the engine is reached, so
// this fallback is the single-tenant answer rather than a way to bypass the
// boundary.
func WorkspaceFromContext(ctx context.Context) string {
	if p, ok := PrincipalFromContext(ctx); ok {
		if workspace := strings.TrimSpace(p.WorkspaceID); workspace != "" {
			return workspace
		}
	}
	return wsroot.PersonalWorkspaceID
}

// SubjectFromContext returns the principal a run is acting for, or "" when
// there is none.
//
// The empty string is the correct answer, not a placeholder: user-private
// stores treat it as its own owner — the implicit local user a single-tenant
// installation has always had — and it is the value those installations' rows
// already carry. Substituting anything else would make one user's history
// invisible or, worse, merge two users' into one.
func SubjectFromContext(ctx context.Context) string {
	if p, ok := PrincipalFromContext(ctx); ok {
		return strings.TrimSpace(p.Subject)
	}
	return ""
}

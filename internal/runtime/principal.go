package runtime

import (
	"context"
	"strings"
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
	if p.Role == "admin" {
		return true
	}
	if isPrivilegedSystemTool(toolName) {
		return p.Role == "operator"
	}
	if strings.HasPrefix(toolName, "mcp__") || strings.HasPrefix(toolName, "plugin__") {
		return p.Role == "operator"
	}
	return p.Role == "operator" || p.Role == "viewer"
}

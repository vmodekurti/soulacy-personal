package runtime

import (
	"context"
	"strings"

	"github.com/soulacy/soulacy/pkg/agent"
)

// Principal is the immutable authentication identity supplied by the gateway.
// Message.UserID/Username are user content and are never authority inputs.
type Principal struct {
	Subject string
	Role    string
	Scopes  []string
}

type principalContextKey struct{}

func WithPrincipal(ctx context.Context, p Principal) context.Context {
	p.Subject = strings.Clone(strings.TrimSpace(p.Subject))
	p.Role = strings.Clone(strings.ToLower(strings.TrimSpace(p.Role)))
	p.Scopes = append([]string(nil), p.Scopes...)
	return context.WithValue(ctx, principalContextKey{}, p)
}

func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalContextKey{}).(Principal)
	return p, ok
}

// applyAgentPrincipalBoundary prevents a protected non-admin agent from
// inheriting elevated authority from either an administrator's chat session or
// a trusted internal/scheduler invocation. Viewer stays viewer; every stronger
// or absent role is capped at operator.
func applyAgentPrincipalBoundary(ctx context.Context, def *agent.Definition) context.Context {
	if def == nil || (strings.TrimSpace(def.ID) != GenieAgentID && def.Labels["soulacy.owner"] != GenieAgentID) {
		return ctx
	}
	p, ok := PrincipalFromContext(ctx)
	if !ok {
		return WithPrincipal(ctx, Principal{Subject: "builtin:genie", Role: "operator"})
	}
	if p.Role != "viewer" {
		p.Role = "operator"
	}
	return WithPrincipal(ctx, p)
}

func callerAllowsTool(ctx context.Context, toolName string) bool {
	p, ok := PrincipalFromContext(ctx)
	if !ok {
		return true // scheduler/channel/internal invocations are trusted services
	}
	if p.Role == "admin" {
		return true
	}
	if strings.HasPrefix(toolName, "safe_undo.") {
		return p.Role == "operator"
	}
	if isPrivilegedSystemTool(toolName) {
		return p.Role == "operator"
	}
	if strings.HasPrefix(toolName, "mcp__") || strings.HasPrefix(toolName, "plugin__") {
		return p.Role == "operator"
	}
	return p.Role == "operator" || p.Role == "viewer"
}
